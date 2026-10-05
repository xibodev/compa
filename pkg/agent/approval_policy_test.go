package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/xibodev/compa/pkg/approval"
	"github.com/xibodev/compa/pkg/bus"
	"github.com/xibodev/compa/pkg/config"
	runtimeevents "github.com/xibodev/compa/pkg/events"
	"github.com/xibodev/compa/pkg/providers"
	"github.com/xibodev/compa/pkg/tools"
)

// policyProbe is a tool that records, for each call it runs, whether the
// call ran with the operator's approval.
type policyProbe struct {
	name string

	mu       sync.Mutex
	approved []bool
}

func (p *policyProbe) Name() string        { return p.name }
func (p *policyProbe) Description() string { return "records its calls" }
func (p *policyProbe) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"action":  map[string]any{"type": "string"},
		"command": map[string]any{"type": "string"},
	}}
}

func (p *policyProbe) Execute(ctx context.Context, _ map[string]any) *tools.ToolResult {
	p.mu.Lock()
	p.approved = append(p.approved, approval.Approved(ctx))
	p.mu.Unlock()
	return tools.SilentResult("ran " + p.name)
}

// runs returns, for each call that ran, whether it ran approved.
func (p *policyProbe) runs() []bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]bool(nil), p.approved...)
}

// policyProvider has the model call one tool, records the tools each request
// offered, and then answers with the last message: the call's result, or why
// it did not run.
type policyProvider struct {
	call providers.ToolCall

	mu      sync.Mutex
	offered [][]string
}

func (p *policyProvider) Chat(
	_ context.Context,
	messages []providers.Message,
	defs []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	names := make([]string, 0, len(defs))
	for _, def := range defs {
		names = append(names, def.Function.Name)
	}
	p.offered = append(p.offered, names)
	if len(p.offered) == 1 {
		return &providers.LLMResponse{ToolCalls: []providers.ToolCall{p.call}}, nil
	}
	return &providers.LLMResponse{Content: messages[len(messages)-1].Content}, nil
}

func (p *policyProvider) GetDefaultModel() string { return "policy-model" }

// firstOffered returns the tools the first request offered.
func (p *policyProvider) firstOffered() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.offered) == 0 {
		return nil
	}
	return p.offered[0]
}

// recordingApprover is an in-process approver that records each request and
// answers decision.
type recordingApprover struct {
	decision ApprovalDecision

	mu       sync.Mutex
	requests []*ToolApprovalRequest
}

func (a *recordingApprover) ApproveTool(_ context.Context, req *ToolApprovalRequest) (ApprovalDecision, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.requests = append(a.requests, req)
	return a.decision, nil
}

func (a *recordingApprover) last() *ToolApprovalRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.requests) == 0 {
		return nil
	}
	return a.requests[len(a.requests)-1]
}

func rules(rs ...approval.Rule) approval.Policy {
	return approval.Policy{Default: approval.Allow, Rules: rs}
}

// newPolicyTestLoop returns a loop under policy whose model calls tool with
// args, and the probes registered as tools.
func newPolicyTestLoop(
	t *testing.T,
	policy approval.Policy,
	tool string,
	args map[string]any,
	probes ...tools.Tool,
) (*AgentLoop, *AgentInstance, *policyProvider, *bus.MessageBus) {
	t.Helper()
	provider := &policyProvider{call: providers.ToolCall{ID: "call-1", Name: tool, Arguments: args}}
	al, agent, cleanup := newHookTestLoop(t, provider)
	t.Cleanup(cleanup)
	al.cfg.Tools.Approval = policy
	al.channelManager = approvalChannels{running: map[string]bool{"telegram": true, config.ChannelWeb: true}}
	for _, probe := range probes {
		al.RegisterTool(probe)
	}
	msgBus, ok := al.bus.(*bus.MessageBus)
	if !ok {
		t.Fatalf("bus is a %T", al.bus)
	}
	return al, agent, provider, msgBus
}

// runPolicyTurn runs a turn answering a message from inbound and returns its
// answer: the call's result, or why it did not run.
func runPolicyTurn(
	t *testing.T,
	ctx context.Context,
	al *AgentLoop,
	agent *AgentInstance,
	inbound *bus.InboundContext,
) string {
	t.Helper()
	resp, err := al.runAgentLoop(ctx, agent, processOptions{
		Dispatch:        DispatchRequest{SessionKey: "session-1", UserMessage: "go", InboundContext: inbound},
		DefaultResponse: defaultResponse,
	})
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	return resp
}

func runPolicyTurnInBackground(ctx context.Context, al *AgentLoop, agent *AgentInstance, inbound *bus.InboundContext) <-chan string {
	done := make(chan string, 1)
	go func() {
		resp, err := al.runAgentLoop(ctx, agent, processOptions{
			Dispatch:        DispatchRequest{SessionKey: "session-1", UserMessage: "go", InboundContext: inbound},
			DefaultResponse: defaultResponse,
		})
		if err != nil {
			resp = "error: " + err.Error()
		}
		done <- resp
	}()
	return done
}

var cliTurn = &bus.InboundContext{Channel: "cli", ChatID: "direct"}

func TestApprovalPolicyDecidesEachCall(t *testing.T) {
	tests := []struct {
		name   string
		policy approval.Policy
		want   string
		// runs says, for each run of the call, whether it ran approved.
		runs []bool
	}{
		{"default allow", approval.Policy{}, "ran probe", []bool{false}},
		{"allowed by a rule", rules(approval.Rule{Tool: "probe", Action: approval.Allow}), "ran probe", []bool{true}},
		{"denied", rules(approval.Rule{Tool: "probe", Action: approval.Deny}), approvalPolicyDeniedReason, nil},
		{"hidden", rules(approval.Rule{Tool: "probe", Action: approval.Hide}), `Tool "probe" is not available.`, nil},
		{"denied by default", approval.Policy{Default: approval.Deny}, approvalPolicyDeniedReason, nil},
		{"glob and source", rules(approval.Rule{Tool: "pr?b*", Source: "builtin", Action: approval.Deny}), approvalPolicyDeniedReason, nil},
		{"another source", rules(approval.Rule{Tool: "probe", Source: "mcp:*", Action: approval.Deny}), "ran probe", []bool{false}},
		{
			"first match wins",
			rules(approval.Rule{Tool: "probe", Action: approval.Allow}, approval.Rule{Tool: "*", Action: approval.Deny}),
			"ran probe",
			[]bool{true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			probe := &policyProbe{name: "probe"}
			al, agent, _, _ := newPolicyTestLoop(t, tt.policy, "probe", map[string]any{}, probe)
			if got := runPolicyTurn(t, context.Background(), al, agent, cliTurn); got != tt.want {
				t.Fatalf("answer = %q, want %q", got, tt.want)
			}
			if got := probe.runs(); !slices.Equal(got, tt.runs) {
				t.Fatalf("runs = %v, want %v", got, tt.runs)
			}
		})
	}
}

func TestApprovalPolicyRefusesAnUnknownTool(t *testing.T) {
	al, agent, _, _ := newPolicyTestLoop(t, approval.Policy{}, "nowhere", map[string]any{})
	if got := runPolicyTurn(t, context.Background(), al, agent, cliTurn); got != `Tool "nowhere" is not available.` {
		t.Fatalf("answer = %q", got)
	}
}

func TestApprovalPolicyOrigins(t *testing.T) {
	origins := []approval.Origin{approval.OriginWeb, approval.OriginCLI, approval.OriginChat, approval.OriginCron}
	tests := []struct {
		name      string
		inbound   *bus.InboundContext
		scheduled bool
		want      approval.Origin
	}{
		{"web", &bus.InboundContext{Channel: config.ChannelWeb, ChatID: "web-1"}, false, approval.OriginWeb},
		{"cli", cliTurn, false, approval.OriginCLI},
		{"chat app", &bus.InboundContext{Channel: "telegram", ChatID: "chat-1"}, false, approval.OriginChat},
		{"web client", &bus.InboundContext{Channel: "web_client", ChatID: "c-1"}, false, approval.OriginChat},
		{"scheduled", cliTurn, true, approval.OriginCron},
		{"scheduled chat", &bus.InboundContext{Channel: "telegram", ChatID: "chat-1"}, true, approval.OriginCron},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			if tt.scheduled {
				ctx = withScheduledTurn(ctx)
			}
			// A rule for the turn's origin decides; one for the others doesn't.
			others := slices.DeleteFunc(slices.Clone(origins), func(o approval.Origin) bool { return o == tt.want })
			for _, c := range []struct {
				origins []approval.Origin
				want    string
			}{
				{[]approval.Origin{tt.want}, approvalPolicyDeniedReason},
				{others, "ran probe"},
			} {
				policy := rules(approval.Rule{Tool: "probe", Origin: c.origins, Action: approval.Deny})
				al, agent, _, _ := newPolicyTestLoop(t, policy, "probe", map[string]any{}, &policyProbe{name: "probe"})
				if got := runPolicyTurn(t, ctx, al, agent, tt.inbound); got != c.want {
					t.Fatalf("rule for %v: answer = %q, want %q", c.origins, got, c.want)
				}
			}
		})
	}
}

func TestApprovalPolicyAskWithAnApprover(t *testing.T) {
	policy := rules(approval.Rule{Tool: "probe", Origin: []approval.Origin{approval.OriginCLI}, Action: approval.Ask})
	for _, tt := range []struct {
		name     string
		decision ApprovalDecision
		want     string
		runs     []bool
	}{
		{"approved", ApprovalDecision{Approved: true}, "ran probe", []bool{true}},
		{"denied", ApprovalDecision{Reason: "not today"}, "Tool execution denied by approval hook: not today", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			probe := &policyProbe{name: "probe"}
			al, agent, _, msgBus := newPolicyTestLoop(t, policy, "probe", map[string]any{"command": "x"}, probe)
			approver := &recordingApprover{decision: tt.decision}
			if err := al.MountHook(NamedHook("approver", approver)); err != nil {
				t.Fatalf("MountHook: %v", err)
			}
			if got := runPolicyTurn(t, context.Background(), al, agent, cliTurn); got != tt.want {
				t.Fatalf("answer = %q, want %q", got, tt.want)
			}
			if got := probe.runs(); !slices.Equal(got, tt.runs) {
				t.Fatalf("runs = %v, want %v", got, tt.runs)
			}
			req := approver.last()
			if req == nil {
				t.Fatal("the approver was not asked")
			}
			if req.Tool != "probe" || req.Arguments["command"] != "x" || req.Origin != approval.OriginCLI ||
				req.Info.Source != approval.SourceBuiltin {
				t.Fatalf("request = %+v", req)
			}
			if d := req.Decision; d.Action != approval.Ask || d.Index != 0 || d.Rule == nil || d.Rule.Tool != "probe" {
				t.Fatalf("request decision = %+v", d)
			}
			noPost(t, msgBus) // an approver decides: the owner is not asked
		})
	}
}

// subscribeApprovalEvents returns the approval events al publishes.
func subscribeApprovalEvents(t *testing.T, al *AgentLoop) <-chan runtimeevents.Event {
	t.Helper()
	sub, events, err := al.RuntimeEvents().
		OfKind(runtimeevents.KindApprovalRequested, runtimeevents.KindApprovalResolved).
		SubscribeChan(context.Background(), runtimeevents.SubscribeOptions{Name: "approval-events", Buffer: 8})
	if err != nil {
		t.Fatalf("SubscribeChan() error = %v", err)
	}
	t.Cleanup(func() { _ = sub.Close() })
	return events
}

// receiveApprovalEvents returns the next request and its answer.
func receiveApprovalEvents(
	t *testing.T,
	events <-chan runtimeevents.Event,
) (runtimeevents.Event, ApprovalRequestedPayload, ApprovalResolvedPayload) {
	t.Helper()
	requested := receiveRuntimeEvent(t, events)
	request, ok := requested.Payload.(ApprovalRequestedPayload)
	if requested.Kind != runtimeevents.KindApprovalRequested || !ok {
		t.Fatalf("first event = %s with %T, want approval.requested", requested.Kind, requested.Payload)
	}
	resolved := receiveRuntimeEvent(t, events)
	answer, ok := resolved.Payload.(ApprovalResolvedPayload)
	if resolved.Kind != runtimeevents.KindApprovalResolved || !ok {
		t.Fatalf("second event = %s with %T, want approval.resolved", resolved.Kind, resolved.Payload)
	}
	if answer.ApprovalID != request.ApprovalID {
		t.Fatalf("resolved approval %q, want %q", answer.ApprovalID, request.ApprovalID)
	}
	return requested, request, answer
}

// An ask is published: approval.requested before the approvers answer, and
// approval.resolved with their answer.
func TestApprovalPolicyAskPublishesApprovalEvents(t *testing.T) {
	policy := rules(approval.Rule{Tool: "probe", Action: approval.Ask})
	for _, tt := range []struct {
		name       string
		decision   ApprovalDecision
		resolution string
		reason     string
	}{
		{"approved", ApprovalDecision{Approved: true}, "approved", ""},
		{"denied", ApprovalDecision{Reason: "not today"}, "rejected", "Tool execution denied by approval hook: not today"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			al, agent, _, _ := newPolicyTestLoop(t, policy, "probe", map[string]any{"command": "x"},
				&policyProbe{name: "probe"})
			events := subscribeApprovalEvents(t, al)
			if err := al.MountHook(NamedHook("approver", &recordingApprover{decision: tt.decision})); err != nil {
				t.Fatalf("MountHook: %v", err)
			}
			runPolicyTurn(t, context.Background(), al, agent, cliTurn)

			requested, request, answer := receiveApprovalEvents(t, events)
			if request.ApprovalID == "" || request.ActionID != "call-1" || request.Tool != "probe" ||
				request.Parameters["command"] != "x" || request.State != "pending" ||
				request.Reason != "tools.approval.rules[0] asks about this call" {
				t.Fatalf("requested = %+v", request)
			}
			if requested.Scope.Channel != "cli" || requested.Scope.TurnID == "" {
				t.Fatalf("requested scope = %+v, want the turn's", requested.Scope)
			}
			if answer.Resolution != tt.resolution || answer.Reason != tt.reason {
				t.Fatalf("resolved = %+v, want %s with %q", answer, tt.resolution, tt.reason)
			}
		})
	}

	t.Run("turn ended first", func(t *testing.T) {
		al, agent, _, msgBus := newPolicyTestLoop(t, policy, "probe", map[string]any{}, &policyProbe{name: "probe"})
		events := subscribeApprovalEvents(t, al)
		ctx, cancel := context.WithCancel(context.Background())
		done := runPolicyTurnInBackground(ctx, al, agent, ownerTelegram("chat-1"))
		nextApprovalRequest(t, msgBus)
		cancel()
		waitTurn(t, done)

		_, _, answer := receiveApprovalEvents(t, events)
		if answer.Resolution != "cancelled" || answer.Reason != approvalWithdrawnReason {
			t.Fatalf("resolved = %+v, want cancelled", answer)
		}
	})
}

func TestApprovalPolicyApproversAreAskedOnlyForAsk(t *testing.T) {
	probe := &policyProbe{name: "probe"}
	al, agent, _, _ := newPolicyTestLoop(t, approval.Policy{}, "probe", map[string]any{}, probe)
	approver := &recordingApprover{decision: ApprovalDecision{Reason: "no"}}
	if err := al.MountHook(NamedHook("approver", approver)); err != nil {
		t.Fatalf("MountHook: %v", err)
	}
	if got := runPolicyTurn(t, context.Background(), al, agent, cliTurn); got != "ran probe" {
		t.Fatalf("answer = %q, want the allowed call run", got)
	}
	if approver.last() != nil {
		t.Fatal("an approver was asked about an allowed call")
	}
}

func TestApprovalPolicyAskWithAProcessHook(t *testing.T) {
	policy := rules(approval.Rule{Tool: "probe", Action: approval.Ask})
	probe := &policyProbe{name: "probe"}
	al, agent, _, _ := newPolicyTestLoop(t, policy, "probe", map[string]any{}, probe)
	requestLog := filepath.Join(t.TempDir(), "request.json")
	if err := al.MountProcessHook(context.Background(), "ipc-approval", ProcessHookOptions{
		Command:     processHookHelperCommand(),
		Env:         processHookHelperEnv("record", requestLog),
		ApproveTool: true,
	}); err != nil {
		t.Fatalf("MountProcessHook: %v", err)
	}

	if got := runPolicyTurn(t, context.Background(), al, agent, &bus.InboundContext{Channel: "telegram", ChatID: "c"}); got != "ran probe" {
		t.Fatalf("answer = %q, want the call approved by the hook", got)
	}
	data, err := os.ReadFile(requestLog)
	if err != nil {
		t.Fatalf("the hook recorded no request: %v", err)
	}
	var req struct {
		Tool     string          `json:"tool"`
		Origin   approval.Origin `json:"origin"`
		Info     approval.Info   `json:"info"`
		Decision struct {
			Action approval.Action `json:"action"`
			Index  int             `json:"index"`
			Rule   *approval.Rule  `json:"rule"`
		} `json:"decision"`
	}
	if err := json.Unmarshal(data, &req); err != nil {
		t.Fatalf("request %s: %v", data, err)
	}
	if req.Tool != "probe" || req.Origin != approval.OriginChat || req.Info.Source != approval.SourceBuiltin ||
		req.Decision.Action != approval.Ask || req.Decision.Index != 0 || req.Decision.Rule == nil ||
		req.Decision.Rule.Tool != "probe" {
		t.Fatalf("request = %s", data)
	}
}

func TestApprovalPolicyAskWithoutApproversAsksTheOwner(t *testing.T) {
	policy := rules(approval.Rule{Tool: "probe", Action: approval.Ask})
	owner := ownerTelegram("chat-1")

	t.Run("approved", func(t *testing.T) {
		probe := &policyProbe{name: "probe"}
		al, agent, _, msgBus := newPolicyTestLoop(t, policy, "probe", map[string]any{"slug": "a"}, probe)
		done := runPolicyTurnInBackground(context.Background(), al, agent, owner)
		out, id := nextApprovalRequest(t, msgBus)
		if want := "Approve calling probe with `{\"slug\":\"a\"}`? Reply /approve " + id + " or /deny " + id; out.Content != want {
			t.Fatalf("request = %q, want %q", out.Content, want)
		}
		reply(al, owner, "/approve "+id)
		if got := waitTurn(t, done); got != "ran probe" {
			t.Fatalf("answer = %q, want the approved call run", got)
		}
		if got := probe.runs(); !slices.Equal(got, []bool{true}) {
			t.Fatalf("runs = %v, want one approved run", got)
		}
	})

	t.Run("denied", func(t *testing.T) {
		probe := &policyProbe{name: "probe"}
		al, agent, _, msgBus := newPolicyTestLoop(t, policy, "probe", map[string]any{}, probe)
		done := runPolicyTurnInBackground(context.Background(), al, agent, owner)
		_, id := nextApprovalRequest(t, msgBus)
		reply(al, owner, "/deny "+id)
		if got := waitTurn(t, done); got != approvalDeniedReason {
			t.Fatalf("answer = %q, want %q", got, approvalDeniedReason)
		}
		if len(probe.runs()) != 0 {
			t.Fatal("the denied call ran")
		}
	})

	t.Run("timeout", func(t *testing.T) {
		probe := &policyProbe{name: "probe"}
		al, agent, _, msgBus := newPolicyTestLoop(t, policy, "probe", map[string]any{}, probe)
		al.approvals.timeout = 50 * time.Millisecond
		done := runPolicyTurnInBackground(context.Background(), al, agent, owner)
		nextApprovalRequest(t, msgBus)
		if got := waitTurn(t, done); got != approvalExpiredReason {
			t.Fatalf("answer = %q, want %q", got, approvalExpiredReason)
		}
		if len(probe.runs()) != 0 {
			t.Fatal("the unanswered call ran")
		}
	})

	t.Run("no chat to ask in", func(t *testing.T) {
		probe := &policyProbe{name: "probe"}
		al, agent, _, msgBus := newPolicyTestLoop(t, policy, "probe", map[string]any{}, probe)
		stranger := &bus.InboundContext{Channel: "telegram", ChatID: "group-1", SenderID: "telegram:9"}
		if got := runPolicyTurn(t, context.Background(), al, agent, stranger); got != approvalNoChatReason {
			t.Fatalf("answer = %q, want %q", got, approvalNoChatReason)
		}
		if len(probe.runs()) != 0 {
			t.Fatal("the call ran without an approval")
		}
		noPost(t, msgBus)
	})
}

// An ask covers what runs something: exec's run, and write and send-keys,
// which send input to a running process. Looking at a process or stopping
// it is not asked about.
func TestApprovalPolicyExecAsksBeforeRunningAnything(t *testing.T) {
	ask := rules(approval.Rule{Tool: "exec", Action: approval.Ask})
	deny := rules(approval.Rule{Tool: "exec", Action: approval.Deny})
	for _, tt := range []struct {
		policy approval.Policy
		action string
		want   string
	}{
		{ask, "list", "ran exec"},
		{ask, "poll", "ran exec"},
		{ask, "read", "ran exec"},
		{ask, "kill", "ran exec"},
		// Nobody can be asked in a terminal that shows no requests.
		{ask, "run", approvalNoChatReason},
		{ask, "write", approvalNoChatReason},
		{ask, "send-keys", approvalNoChatReason},
		{deny, "poll", approvalPolicyDeniedReason},
	} {
		t.Run(string(tt.policy.Rules[0].Action)+" "+tt.action, func(t *testing.T) {
			exec := &policyProbe{name: "exec"}
			al, agent, _, _ := newPolicyTestLoop(t, tt.policy, "exec",
				map[string]any{"action": tt.action, "command": "ls"}, exec)
			if got := runPolicyTurn(t, context.Background(), al, agent, cliTurn); got != tt.want {
				t.Fatalf("answer = %q, want %q", got, tt.want)
			}
			if tt.want == "ran exec" && !slices.Equal(exec.runs(), []bool{false}) {
				t.Fatalf("runs = %v, want one run, not approved", exec.runs())
			}
			if tt.want != "ran exec" && len(exec.runs()) != 0 {
				t.Fatalf("runs = %v, want the call not run", exec.runs())
			}
		})
	}
}

func TestApprovalPolicyHiddenToolsAreNotOffered(t *testing.T) {
	policy := rules(approval.Rule{Tool: "secret", Origin: []approval.Origin{approval.OriginChat}, Action: approval.Hide})

	al, agent, provider, _ := newPolicyTestLoop(t, policy, "secret", map[string]any{},
		&policyProbe{name: "probe"}, &policyProbe{name: "secret"})
	got := runPolicyTurn(t, context.Background(), al, agent, &bus.InboundContext{Channel: "telegram", ChatID: "c"})
	if got != `Tool "secret" is not available.` {
		t.Fatalf("answer = %q, want the hidden tool's call refused", got)
	}
	if offered := provider.firstOffered(); slices.Contains(offered, "secret") || !slices.Contains(offered, "probe") {
		t.Fatalf("offered %v, want probe and not secret", offered)
	}

	// The rule hides it from chat apps only.
	al, agent, provider, _ = newPolicyTestLoop(t, policy, "secret", map[string]any{},
		&policyProbe{name: "probe"}, &policyProbe{name: "secret"})
	if got := runPolicyTurn(t, context.Background(), al, agent, cliTurn); got != "ran secret" {
		t.Fatalf("answer = %q, want the tool run from the terminal", got)
	}
	if offered := provider.firstOffered(); !slices.Contains(offered, "secret") {
		t.Fatalf("offered %v, want secret offered to the terminal", offered)
	}
}

// The installed modules catalog does not name a tool the turn is not
// offered: the hidden capability's line goes, and with it a module none of
// whose capabilities are left.
func TestApprovalPolicyHiddenModuleToolsLeaveTheCatalog(t *testing.T) {
	policy := rules(approval.Rule{
		Tool: "*__render", Origin: []approval.Origin{approval.OriginChat}, Action: approval.Hide,
	})
	al, agent, _, _ := newPolicyTestLoop(t, policy, "media__probe", map[string]any{},
		&policyProbe{name: "media__render"}, &policyProbe{name: "media__probe"}, &policyProbe{name: "film__render"})
	agent.ModuleSummaries = []string{
		"Media (module media):\n  - media__render (render): renders\n    in two lines [cost unknown]\n  - media__probe (probe): reads",
		"Film (module film):\n  - film__render (render): renders",
		"Module guidance: call module_knowledge first.",
	}
	catalog := slices.Clone(agent.ModuleSummaries)
	summaries := func(origin approval.Origin) []string {
		ts := &turnState{al: al, agent: agent, origin: origin}
		return promptBuildRequestForTurn(ts, nil, "", "go", nil, al.GetConfig()).ModuleSummaries
	}

	want := []string{"Media (module media):\n  - media__probe (probe): reads", catalog[2]}
	if got := summaries(approval.OriginChat); !slices.Equal(got, want) {
		t.Fatalf("chat catalog = %q, want %q", got, want)
	}
	if got := summaries(approval.OriginCLI); !slices.Equal(got, catalog) {
		t.Fatalf("terminal catalog = %q, want it whole", got)
	}
	if !slices.Equal(agent.ModuleSummaries, catalog) {
		t.Fatalf("the agent's catalog changed: %q", agent.ModuleSummaries)
	}
}

// describedProbe is a policyProbe from outside Compa.
type describedProbe struct {
	*policyProbe
	info approval.Info
}

func (p describedProbe) ApprovalInfo() approval.Info { return p.info }

func TestApprovalPolicyMatchesADescribedToolsSourceAndHints(t *testing.T) {
	policy := rules(
		approval.Rule{Source: "mcp:github", Hints: []string{approval.HintReadOnly}, Action: approval.Allow},
		approval.Rule{Source: "mcp:*", Action: approval.Deny},
	)
	for _, tt := range []struct {
		name    string
		trusted bool
		want    string
	}{
		{"trusted read-only", true, "ran mcp_github_list"},
		{"untrusted", false, approvalPolicyDeniedReason},
	} {
		t.Run(tt.name, func(t *testing.T) {
			probe := describedProbe{
				policyProbe: &policyProbe{name: "mcp_github_list"},
				info: approval.Info{
					Source: approval.MCPSource("github"),
					Name:   "list",
					Hints:  approval.MCPHints(approval.MCPAnnotations{ReadOnly: true}, tt.trusted),
				},
			}
			al, agent, _, _ := newPolicyTestLoop(t, policy, "mcp_github_list", map[string]any{}, probe)
			if got := runPolicyTurn(t, context.Background(), al, agent, cliTurn); got != tt.want {
				t.Fatalf("answer = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHookManagerApproversAreTheHooksThatApprove(t *testing.T) {
	hm := NewHookManager(nil)
	defer hm.Close()
	req := &ToolApprovalRequest{Tool: "probe"}

	if _, decided := hm.ApproveTool(context.Background(), req); decided {
		t.Fatal("decided with no approver registered")
	}
	// A hook that has ApproveTool but does not approve is no approver.
	if err := hm.Mount(HookRegistration{Name: "observer", Hook: notApprovingHook{}}); err != nil {
		t.Fatal(err)
	}
	if _, decided := hm.ApproveTool(context.Background(), req); decided {
		t.Fatal("decided by a hook that does not approve")
	}

	first := &recordingApprover{decision: ApprovalDecision{Approved: true}}
	second := &recordingApprover{decision: ApprovalDecision{Reason: "second says no"}}
	if err := hm.Mount(HookRegistration{Name: "a", Hook: first}); err != nil {
		t.Fatal(err)
	}
	if decision, decided := hm.ApproveTool(context.Background(), req); !decided || !decision.Approved {
		t.Fatalf("ApproveTool = %+v, %v; want approved by the one approver", decision, decided)
	}
	if err := hm.Mount(HookRegistration{Name: "b", Hook: second}); err != nil {
		t.Fatal(err)
	}
	if decision, decided := hm.ApproveTool(context.Background(), req); !decided || decision.Approved ||
		decision.Reason != "second says no" {
		t.Fatalf("ApproveTool = %+v, %v; want the second approver's denial", decision, decided)
	}
}

type notApprovingHook struct{}

func (notApprovingHook) ApproveTool(context.Context, *ToolApprovalRequest) (ApprovalDecision, error) {
	return ApprovalDecision{Approved: true}, nil
}

func (notApprovingHook) approvesTools() bool { return false }

func TestTurnOrigin(t *testing.T) {
	for _, tt := range []struct {
		scheduled bool
		channel   string
		want      approval.Origin
	}{
		{false, " web ", approval.OriginWeb},
		{false, "cli", approval.OriginCLI},
		{false, "slack", approval.OriginChat},
		{false, "", approval.OriginChat},
		{true, "web", approval.OriginCron},
	} {
		if got := turnOrigin(tt.scheduled, tt.channel); got != tt.want {
			t.Errorf("turnOrigin(%v, %q) = %q, want %q", tt.scheduled, tt.channel, got, tt.want)
		}
	}
}

func TestSubTurnInheritsTheOrigin(t *testing.T) {
	policy := rules(approval.Rule{Tool: "probe", Origin: []approval.Origin{approval.OriginCron}, Action: approval.Deny})
	al, agent, _, _ := newPolicyTestLoop(t, policy, "probe", map[string]any{}, &policyProbe{name: "probe"})
	// A scheduled turn's child: its own channel would make it a chat's.
	parent := &turnState{
		ctx:            context.Background(),
		turnID:         "parent-1",
		agent:          agent,
		pendingResults: make(chan *tools.ToolResult, 1),
		session:        &ephemeralSessionStore{},
		origin:         approval.OriginCron,
	}
	result, err := spawnSubTurn(context.Background(), al, parent, SubTurnConfig{SystemPrompt: "task"})
	if err != nil {
		t.Fatalf("spawnSubTurn: %v", err)
	}
	if result.ForLLM != approvalPolicyDeniedReason {
		t.Fatalf("child's answer = %q, want its call denied as the parent's", result.ForLLM)
	}
}

func waitTurn(t *testing.T, done <-chan string) string {
	t.Helper()
	select {
	case resp := <-done:
		return resp
	case <-time.After(10 * time.Second):
		t.Fatal("the turn did not end")
	}
	return ""
}
