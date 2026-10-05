package approval

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestDecideTakesTheFirstMatchingRule(t *testing.T) {
	policy := Policy{
		Default: Deny,
		Rules: []Rule{
			{Tool: "exec", Origin: []Origin{OriginChat, OriginCron}, Action: Ask},
			{Tool: "mcp_github_*", Action: Hide},
			{Source: "mcp:*", Hints: []string{HintReadOnly}, Action: Allow},
			{Source: "mcp:files", Action: Ask},
			{Source: "builtin", Action: Allow},
		},
	}
	exec := Tool{Name: "exec", Source: SourceBuiltin}
	for _, tc := range []struct {
		name   string
		tool   Tool
		origin Origin
		want   Action
		index  int
	}{
		{"origin listed", exec, OriginChat, Ask, 0},
		{"another origin listed", exec, OriginCron, Ask, 0},
		{"origin not listed falls through", exec, OriginWeb, Allow, 4},
		{"tool glob", Tool{Name: "mcp_github_create_issue", Source: "mcp:github"}, OriginWeb, Hide, 1},
		{"hint", Tool{Name: "mcp_files_read", Source: "mcp:files", Hints: []string{HintReadOnly, HintOpenWorld}}, OriginCLI, Allow, 2},
		{"hint missing falls through", Tool{Name: "mcp_files_write", Source: "mcp:files", Hints: []string{HintDestructive}}, OriginCLI, Ask, 3},
		{"no rule matches", Tool{Name: "mcp_other_x", Source: "mcp:other"}, OriginWeb, Deny, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := policy.Decide(tc.tool, tc.origin)
			if d.Action != tc.want || d.Index != tc.index {
				t.Fatalf("Decide() = %s by rule %d, want %s by rule %d", d.Action, d.Index, tc.want, tc.index)
			}
			if (d.Rule == nil) != (tc.index < 0) {
				t.Fatalf("Decide().Rule = %v for index %d", d.Rule, tc.index)
			}
			if d.Rule != nil && d.Rule.Action != tc.want {
				t.Fatalf("Decide().Rule = %+v, want the rule with action %s", d.Rule, tc.want)
			}
		})
	}
}

func TestARuleMatchesEveryFieldItSets(t *testing.T) {
	rule := Rule{Tool: "mcp_*", Source: "mcp:github", Origin: []Origin{OriginChat}, Hints: []string{HintDestructive}, Action: Deny}
	policy := Policy{Rules: []Rule{rule}}
	match := Tool{Name: "mcp_github_delete", Source: "mcp:github", Hints: []string{HintDestructive}}
	if got := policy.Decide(match, OriginChat).Action; got != Deny {
		t.Fatalf("a call matching every field = %s, want deny", got)
	}
	for name, tool := range map[string]Tool{
		"tool":   {Name: "exec", Source: "mcp:github", Hints: []string{HintDestructive}},
		"source": {Name: "mcp_github_delete", Source: "mcp:gitlab", Hints: []string{HintDestructive}},
		"hints":  {Name: "mcp_github_delete", Source: "mcp:github", Hints: []string{HintReadOnly}},
	} {
		if got := policy.Decide(tool, OriginChat).Action; got != Allow {
			t.Errorf("a call that differs in %s = %s, want the default, allow", name, got)
		}
	}
	if got := policy.Decide(match, OriginWeb).Action; got != Allow {
		t.Errorf("a call from another origin = %s, want the default, allow", got)
	}
	if got := (Policy{Rules: []Rule{{Action: Ask}}}).Decide(match, OriginWeb).Action; got != Ask {
		t.Errorf("a rule that sets no field = %s, want it to match every call", got)
	}
}

func TestTheEmptyDefaultAllows(t *testing.T) {
	d := Policy{}.Decide(Tool{Name: "exec", Source: SourceBuiltin}, OriginWeb)
	if d.Action != Allow || d.Index != -1 || d.Rule != nil || d.Approved() {
		t.Fatalf("Decide() with no policy = %+v, want the default allow, not approved", d)
	}
}

func TestOnlyARuleThatAllowsIsAnApproval(t *testing.T) {
	policy := Policy{Default: Allow, Rules: []Rule{{Tool: "a", Action: Allow}, {Tool: "b", Action: Ask}}}
	if !policy.Decide(Tool{Name: "a"}, OriginWeb).Approved() {
		t.Error("a rule that allows is not an approval")
	}
	if policy.Decide(Tool{Name: "b"}, OriginWeb).Approved() {
		t.Error("a rule that asks is an approval")
	}
	if policy.Decide(Tool{Name: "c"}, OriginWeb).Approved() {
		t.Error("the default is an approval")
	}
}

func TestTheDefaultPolicy(t *testing.T) {
	policy := DefaultPolicy()
	if err := policy.Validate(); err != nil {
		t.Fatalf("DefaultPolicy().Validate() = %v", err)
	}
	for _, tc := range []struct {
		name string
		tool Tool
		want Action
	}{
		{"module that may cost money", Tool{Name: "render__image", Source: ModuleSource("render"), Hints: ModuleHints(false, false, false)}, Ask},
		{"module that reaches the network", Tool{Name: "fetch__page", Source: ModuleSource("fetch"), Hints: ModuleHints(true, false, true)}, Ask},
		{"module that writes outside the host", Tool{Name: "post__send", Source: ModuleSource("post"), Hints: ModuleHints(false, true, true)}, Ask},
		{"module without those effects", Tool{Name: "zip__pack", Source: ModuleSource("zip"), Hints: ModuleHints(false, false, true)}, Allow},
		{"install_skill", Tool{Name: "install_skill", Source: SourceBuiltin}, Ask},
		{"exec", Tool{Name: "exec", Source: SourceBuiltin}, Allow},
		{"untrusted MCP tool", Tool{Name: "mcp_x_y", Source: MCPSource("x"), Hints: MCPHints(MCPAnnotations{}, false)}, Allow},
	} {
		for _, origin := range []Origin{OriginWeb, OriginCLI, OriginChat, OriginCron} {
			if got := policy.Decide(tc.tool, origin).Action; got != tc.want {
				t.Errorf("%s from %s = %s, want %s", tc.name, origin, got, tc.want)
			}
		}
	}
}

// Decoding over a policy, as config loading over the defaults does, replaces
// its rules with those given rather than mixing their fields, and keeps what
// is not given.
func TestDecodingOverAPolicyReplacesItsRules(t *testing.T) {
	policy := DefaultPolicy()
	if err := json.Unmarshal([]byte(`{"rules":[{"tool":"exec","action":"deny"}]}`), &policy); err != nil {
		t.Fatalf("Unmarshal() = %v", err)
	}
	want := Policy{Default: Allow, Rules: []Rule{{Tool: "exec", Action: Deny}}}
	if !reflect.DeepEqual(policy, want) {
		t.Fatalf("policy = %+v, want %+v", policy, want)
	}

	policy = DefaultPolicy()
	if err := json.Unmarshal([]byte(`{"default":"ask"}`), &policy); err != nil {
		t.Fatalf("Unmarshal() = %v", err)
	}
	if policy.Default != Ask || !reflect.DeepEqual(policy.Rules, DefaultPolicy().Rules) {
		t.Fatalf("policy = %+v, want the default rules and default ask", policy)
	}

	policy = DefaultPolicy()
	if err := json.Unmarshal([]byte(`{"rules":[]}`), &policy); err != nil {
		t.Fatalf("Unmarshal() = %v", err)
	}
	if len(policy.Rules) != 0 {
		t.Fatalf("rules = %+v, want none", policy.Rules)
	}
	if err := json.Unmarshal([]byte(`{"rules":{}}`), &policy); err == nil {
		t.Fatal("Unmarshal() of rules that are not a list succeeded")
	}
}

func TestPolicyJSON(t *testing.T) {
	var policy Policy
	data := `{"default":"ask","rules":[{"tool":"exec","origin":["chat","cron"],"action":"deny"},` +
		`{"source":"mcp:*","hints":["read_only"],"action":"allow"}]}`
	if err := json.Unmarshal([]byte(data), &policy); err != nil {
		t.Fatalf("Unmarshal() = %v", err)
	}
	if err := policy.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
	if got := policy.Decide(Tool{Name: "exec"}, OriginCron).Action; got != Deny {
		t.Fatalf("exec from cron = %s, want deny", got)
	}
	out, err := json.Marshal(policy)
	if err != nil {
		t.Fatalf("Marshal() = %v", err)
	}
	if string(out) != data {
		t.Fatalf("Marshal() = %s, want %s", out, data)
	}
}

func TestValidateNamesTheBadSetting(t *testing.T) {
	for _, tc := range []struct {
		policy Policy
		field  string
	}{
		{Policy{Default: "maybe"}, "tools.approval.default"},
		{Policy{Rules: []Rule{{Tool: "exec"}}}, "tools.approval.rules[0].action"},
		{Policy{Rules: []Rule{{Action: Allow}, {Action: "permit"}}}, "tools.approval.rules[1].action"},
		{Policy{Rules: []Rule{{Origin: []Origin{"web", "telegram"}, Action: Ask}}}, "tools.approval.rules[0].origin"},
		{Policy{Rules: []Rule{{Hints: []string{"read-only"}, Action: Ask}}}, "tools.approval.rules[0].hints"},
	} {
		err := tc.policy.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.field) {
			t.Errorf("Validate(%+v) = %v, want an error naming %s", tc.policy, err, tc.field)
		}
	}
	valid := Policy{Default: Hide, Rules: []Rule{
		{Tool: "*", Source: "?uiltin", Origin: origins, Hints: hints, Action: Allow},
		{Action: Ask}, {Action: Deny}, {Action: Hide},
	}}
	if err := valid.Validate(); err != nil {
		t.Errorf("Validate() of every valid value = %v", err)
	}
}

func TestGlob(t *testing.T) {
	for _, tc := range []struct {
		pattern, s string
		want       bool
	}{
		{"exec", "exec", true},
		{"exec", "exec2", false},
		{"exec", "Exec", false},
		{"", "", true},
		{"", "x", false},
		{"*", "", true},
		{"*", "anything at all", true},
		{"mcp_*", "mcp_github_create", true},
		{"mcp_*", "xmcp_github", false},
		{"*_create", "mcp_github_create", true},
		{"mcp_*_create", "mcp_github_create", true},
		{"mcp_*_create", "mcp_github_create_issue", false},
		{"*a*b*c", "xxaxxbxxcxc", true},
		{"*a*b*c", "xxaxxcxxb", false},
		{"?xec", "exec", true},
		{"?xec", "xec", false},
		{"module:*", "module:render.v2", true},
		{"mcp:*", "mcp:team/files", true},
		{"mcp:fichiers-é?", "mcp:fichiers-éà", true},
		{"**", "x", true},
	} {
		if got := match(tc.pattern, tc.s); got != tc.want {
			t.Errorf("match(%q, %q) = %v, want %v", tc.pattern, tc.s, got, tc.want)
		}
	}
}

func TestMCPHintsAreBelievedOnlyFromTrustedServers(t *testing.T) {
	no, yes := false, true
	readOnly := MCPAnnotations{ReadOnly: true, Idempotent: true, OpenWorld: &no}
	for _, tc := range []struct {
		name    string
		a       MCPAnnotations
		trusted bool
		want    []string
	}{
		{"untrusted, nothing declared", MCPAnnotations{}, false, []string{HintDestructive, HintOpenWorld}},
		{"untrusted, declares read-only", readOnly, false, []string{HintDestructive, HintOpenWorld}},
		{"trusted, nothing declared", MCPAnnotations{}, true, []string{HintDestructive, HintOpenWorld}},
		{"trusted, read-only", readOnly, true, []string{HintReadOnly, HintIdempotent}},
		{"trusted, additive", MCPAnnotations{Destructive: &no, OpenWorld: &no}, true, nil},
		{"trusted, destructive and idempotent", MCPAnnotations{Destructive: &yes, Idempotent: true, OpenWorld: &yes},
			true, []string{HintDestructive, HintIdempotent, HintOpenWorld}},
	} {
		if got := MCPHints(tc.a, tc.trusted); !slices.Equal(got, tc.want) {
			t.Errorf("%s: MCPHints() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestModuleHints(t *testing.T) {
	if got := ModuleHints(true, true, false); !slices.Equal(got, []string{HintCostUnknown, HintNetwork, HintExternalWrites}) {
		t.Errorf("ModuleHints(every effect) = %v", got)
	}
	if got := ModuleHints(false, false, true); got != nil {
		t.Errorf("ModuleHints(no effect) = %v, want none", got)
	}
}

type describedTool struct{ info Info }

func (d describedTool) ApprovalInfo() Info { return d.info }

func TestDescribe(t *testing.T) {
	tool, info := Describe("exec", struct{}{})
	if tool.Source != SourceBuiltin || info.Source != SourceBuiltin || tool.Name != "exec" {
		t.Errorf("Describe(a builtin) = %+v, %+v", tool, info)
	}
	mcpInfo := Info{Source: MCPSource("github"), Name: "create_issue", Hints: []string{HintOpenWorld}, Meta: map[string]any{"k": "v"}}
	tool, info = Describe("mcp_github_create_issue", describedTool{mcpInfo})
	if tool.Source != "mcp:github" || !slices.Equal(tool.Hints, mcpInfo.Hints) || info.Name != "create_issue" {
		t.Errorf("Describe(an MCP tool) = %+v, %+v", tool, info)
	}
}

func TestApprovedTravelsInTheContext(t *testing.T) {
	ctx := context.Background()
	if Approved(ctx) {
		t.Fatal("a plain context is approved")
	}
	if !Approved(WithApproved(ctx)) {
		t.Fatal("WithApproved() is not approved")
	}
}
