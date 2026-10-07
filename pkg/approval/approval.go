// Package approval is the one policy that decides each tool call: whether it
// runs, waits for an approval, is refused, or whether the tool is not offered
// to the agent at all. The policy is tools.approval: rules, the first that
// matches a call deciding it, and a default for the calls no rule matches.
package approval

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
)

// Action is what the policy does with a call.
type Action string

const (
	// Allow runs the call.
	Allow Action = "allow"
	// Ask runs the call once it is approved: by a registered approver, or
	// else by the owner in chat. No answer denies it.
	Ask Action = "ask"
	// Deny refuses the call.
	Deny Action = "deny"
	// Hide does not offer the tool to the agent; a call of it is refused.
	Hide Action = "hide"
)

// Origin is where the turn that makes a call came from.
type Origin string

const (
	OriginWeb  Origin = "web"  // the web UI
	OriginCLI  Origin = "cli"  // the terminal
	OriginChat Origin = "chat" // a chat app
	OriginCron Origin = "cron" // a scheduled job or the heartbeat
)

// The hints a rule can match. MCP servers declare the first four as tool
// annotations, which count only from trusted servers (MCPHints); modules
// declare the last three as effects of a capability (ModuleHints).
const (
	HintReadOnly       = "read_only"
	HintDestructive    = "destructive"
	HintIdempotent     = "idempotent"
	HintOpenWorld      = "open_world"
	HintCostUnknown    = "cost_unknown"
	HintNetwork        = "network"
	HintExternalWrites = "external_writes"
)

var (
	actions = []Action{Allow, Ask, Deny, Hide}
	origins = []Origin{OriginWeb, OriginCLI, OriginChat, OriginCron}
	hints   = []string{
		HintReadOnly, HintDestructive, HintIdempotent, HintOpenWorld,
		HintCostUnknown, HintNetwork, HintExternalWrites,
	}
)

// SourceBuiltin is the source of Compa's own tools. MCP and module tools
// have the sources MCPSource and ModuleSource return.
const SourceBuiltin = "builtin"

// MCPSource returns the source of the tools of the MCP server named server.
func MCPSource(server string) string { return "mcp:" + server }

// ModuleSource returns the source of the capabilities of the module id.
func ModuleSource(id string) string { return "module:" + id }

// Rule decides the calls that match every field it sets. In Tool and Source,
// '*' matches any run of characters and '?' any one character.
type Rule struct {
	// Tool matches the name the agent sees, such as "exec" or "mcp_github_*".
	Tool string `json:"tool,omitempty"`
	// Source matches the tool's source: "builtin", "mcp:<server>" or
	// "module:<id>".
	Source string `json:"source,omitempty"`
	// Origin matches calls from any of these origins.
	Origin []Origin `json:"origin,omitempty"`
	// Hints matches tools that have any of these hints.
	Hints  []string `json:"hints,omitempty"`
	Action Action   `json:"action"`
}

// Policy is tools.approval.
type Policy struct {
	// Default decides the calls no rule matches; empty means allow.
	Default Action `json:"default,omitempty"`
	Rules   []Rule `json:"rules"`
}

// UnmarshalJSON sets the fields data holds and keeps the others, as config
// loading over the defaults needs. Rules that data holds replace p's rules:
// decoding them into p's would mix the fields of both.
func (p *Policy) UnmarshalJSON(data []byte) error {
	var fields struct {
		Default *Action `json:"default"`
		Rules   *[]Rule `json:"rules"`
	}
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if fields.Default != nil {
		p.Default = *fields.Default
	}
	if fields.Rules != nil {
		p.Rules = *fields.Rules
	}
	return nil
}

// DefaultPolicy asks before a module capability that may cost money, reaches
// the network or writes outside the host runs, and before a skill is
// installed. Everything else is allowed.
func DefaultPolicy() Policy {
	return Policy{
		Default: Allow,
		Rules: []Rule{
			{
				Source: "module:*",
				Hints:  []string{HintCostUnknown, HintNetwork, HintExternalWrites},
				Action: Ask,
			},
			{Tool: "install_skill", Action: Ask},
		},
	}
}

// Tool is what the policy knows of a tool.
type Tool struct {
	// Name is the name the agent sees.
	Name string
	// Source is SourceBuiltin, MCPSource(server) or ModuleSource(id).
	Source string
	Hints  []string
}

// Decision is the action for a call and the rule that chose it.
type Decision struct {
	Action Action `json:"action"`
	// Index is the position of the rule that decided in Policy.Rules, or -1
	// when the default did.
	Index int `json:"index"`
	// Rule is the rule that decided; nil when the default did.
	Rule *Rule `json:"rule,omitempty"`
}

// Approved reports whether a rule, rather than the default, allows the call:
// a standing approval by the operator, as an approved ask is a single one.
func (d Decision) Approved() bool {
	return d.Action == Allow && d.Rule != nil
}

// Decide returns the action for a call of tool from origin: that of the first
// rule matching it, or the default.
func (p Policy) Decide(tool Tool, origin Origin) Decision {
	for i := range p.Rules {
		if p.Rules[i].matches(tool, origin) {
			rule := p.Rules[i]
			return Decision{Action: rule.Action, Index: i, Rule: &rule}
		}
	}
	action := p.Default
	if action == "" {
		action = Allow
	}
	return Decision{Action: action, Index: -1}
}

func (r *Rule) matches(tool Tool, origin Origin) bool {
	if r.Tool != "" && !match(r.Tool, tool.Name) {
		return false
	}
	if r.Source != "" && !match(r.Source, tool.Source) {
		return false
	}
	if len(r.Origin) > 0 && !slices.Contains(r.Origin, origin) {
		return false
	}
	if len(r.Hints) > 0 && !slices.ContainsFunc(r.Hints, func(h string) bool {
		return slices.Contains(tool.Hints, h)
	}) {
		return false
	}
	return true
}

// Validate returns an error naming the first setting of p that is not valid.
func (p Policy) Validate() error {
	if p.Default != "" && !slices.Contains(actions, p.Default) {
		return fmt.Errorf("tools.approval.default %q must be allow, ask, deny or hide", p.Default)
	}
	for i, r := range p.Rules {
		if !slices.Contains(actions, r.Action) {
			return fmt.Errorf("tools.approval.rules[%d].action %q must be allow, ask, deny or hide", i, r.Action)
		}
		for _, o := range r.Origin {
			if !slices.Contains(origins, o) {
				return fmt.Errorf("tools.approval.rules[%d].origin %q must be web, cli, chat or cron", i, o)
			}
		}
		for _, h := range r.Hints {
			if !slices.Contains(hints, h) {
				return fmt.Errorf("tools.approval.rules[%d].hints %q must be read_only, destructive, "+
					"idempotent, open_world, cost_unknown, network or external_writes", i, h)
			}
		}
	}
	return nil
}

// match reports whether s matches the glob pattern: '*' matches any run of
// characters, '?' any one character, and any other character itself.
func match(pattern, s string) bool {
	p, str := []rune(pattern), []rune(s)
	pi, si := 0, 0
	star, mark := -1, 0
	for si < len(str) {
		switch {
		case pi < len(p) && (p[pi] == '?' || p[pi] == str[si]):
			pi++
			si++
		case pi < len(p) && p[pi] == '*':
			star, mark = pi, si
			pi++
		case star >= 0:
			// Let the last '*' take one more character and try again.
			mark++
			pi, si = star+1, mark
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}

// MCPAnnotations are the annotations an MCP server declares for a tool. A
// nil pointer is an annotation the server left out.
type MCPAnnotations struct {
	ReadOnly    bool
	Destructive *bool
	Idempotent  bool
	OpenWorld   *bool
}

// MCPHints returns the hints of an MCP tool whose server declares a for it.
// Left-out annotations take the MCP defaults: not read-only, destructive
// (unless read-only), not idempotent, open world. A server that is not
// trusted is not believed: its tools get the defaults whatever it declares.
func MCPHints(a MCPAnnotations, trusted bool) []string {
	if !trusted {
		a = MCPAnnotations{}
	}
	var out []string
	if a.ReadOnly {
		out = append(out, HintReadOnly)
	} else if a.Destructive == nil || *a.Destructive {
		out = append(out, HintDestructive)
	}
	if a.Idempotent {
		out = append(out, HintIdempotent)
	}
	if a.OpenWorld == nil || *a.OpenWorld {
		out = append(out, HintOpenWorld)
	}
	return out
}

// ModuleHints returns the hints of a module capability that declares these
// effects.
func ModuleHints(network, externalWrites, costKnown bool) []string {
	var out []string
	if !costKnown {
		out = append(out, HintCostUnknown)
	}
	if network {
		out = append(out, HintNetwork)
	}
	if externalWrites {
		out = append(out, HintExternalWrites)
	}
	return out
}

// Info is what a tool that is not Compa's own tells the policy and the
// approvers about itself.
type Info struct {
	// Source is MCPSource(server) or ModuleSource(id).
	Source string `json:"source"`
	// Name is the tool's own name at its source: the MCP tool's name, or the
	// module capability's ID.
	Name string `json:"name,omitempty"`
	// Hints are what rules match: MCPHints or ModuleHints.
	Hints []string `json:"hints,omitempty"`
	// Trusted reports, for an MCP tool, whether its server is trusted.
	Trusted bool `json:"trusted,omitempty"`
	// Annotations are an MCP tool's annotations as its server declares them,
	// trusted or not.
	Annotations any `json:"annotations,omitempty"`
	// Meta is an MCP tool's _meta.
	Meta map[string]any `json:"meta,omitempty"`
	// Effects are a module capability's declared effects.
	Effects any `json:"effects,omitempty"`
}

// Described is implemented by the tools that are not Compa's own.
type Described interface {
	ApprovalInfo() Info
}

// Describe returns the policy's view of the tool the agent sees as name, and
// its Info. A tool that does not implement Described is a builtin.
func Describe(name string, tool any) (Tool, Info) {
	info := Info{Source: SourceBuiltin}
	if d, ok := tool.(Described); ok {
		info = d.ApprovalInfo()
		if info.Source == "" {
			info.Source = SourceBuiltin
		}
	}
	return Tool{Name: name, Source: info.Source, Hints: info.Hints}, info
}

type approvedKey struct{}

// WithApproved returns ctx for a call that the operator approved, by a rule
// that allows it (Decision.Approved) or by answering an ask. A module
// capability run with it may publish.
func WithApproved(ctx context.Context) context.Context {
	return context.WithValue(ctx, approvedKey{}, true)
}

// Approved reports whether ctx is that of a call the operator approved
// (WithApproved).
func Approved(ctx context.Context) bool {
	approved, _ := ctx.Value(approvedKey{}).(bool)
	return approved
}

// Call is a tool call made outside an agent turn, such as a scheduled
// command.
type Call struct {
	Tool      string
	Arguments map[string]any
	Origin    Origin
	// Job is the scheduled job the call runs for, named in the request.
	Job string
	// Channel and ChatID are the chat the call reports to.
	Channel string
	ChatID  string
}

// Gate decides the calls made outside an agent turn by the policy, asking
// when it says to. A nil error lets the call run; the error of a call that
// may not run says why.
type Gate interface {
	Check(ctx context.Context, call Call) error
}
