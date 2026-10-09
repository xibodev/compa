package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xibodev/compa/v4/pkg/approval"
	"github.com/xibodev/compa/v4/pkg/bus"
	"github.com/xibodev/compa/v4/pkg/channels"
	"github.com/xibodev/compa/v4/pkg/config"
	"github.com/xibodev/compa/v4/pkg/cron"
)

// approvalChannels is a channel manager whose named channels are running;
// it sends nothing, so what the loop posts stays on the bus.
type approvalChannels struct {
	running map[string]bool
}

func (c approvalChannels) GetChannel(name string) (channels.Channel, bool) {
	if !c.running[name] {
		return nil, false
	}
	return approvalChannel{name: name}, true
}

// approvalChannel is a running channel that sends nothing.
type approvalChannel struct{ name string }

func (c approvalChannel) Name() string                                              { return c.name }
func (approvalChannel) Start(context.Context) error                                 { return nil }
func (approvalChannel) Stop(context.Context) error                                  { return nil }
func (approvalChannel) Send(context.Context, bus.OutboundMessage) ([]string, error) { return nil, nil }
func (approvalChannel) IsRunning() bool                                             { return true }
func (approvalChannel) IsAllowed(string) bool                                       { return true }
func (approvalChannel) IsAllowedSender(bus.SenderInfo) bool                         { return true }
func (approvalChannel) ReasoningChannelID() string                                  { return "" }
func (c approvalChannels) GetEnabledChannels() []string                             { return nil }
func (c approvalChannels) InvokeTypingStop(string, string)                          {}
func (c approvalChannels) SendMessage(context.Context, bus.OutboundMessage) error {
	return nil
}

func (c approvalChannels) SendMedia(context.Context, bus.OutboundMediaMessage) error {
	return nil
}
func (c approvalChannels) SendPlaceholder(context.Context, string, string) bool { return false }
func (c approvalChannels) DismissToolFeedback(context.Context, string, string, *bus.InboundContext) {
}

func newApprovalTestLoop(t *testing.T) (*AgentLoop, *bus.MessageBus) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	msgBus := bus.NewMessageBus()
	t.Cleanup(msgBus.Close)
	al := NewAgentLoop(cfg, msgBus, &mockProvider{})
	t.Cleanup(al.Close)
	al.channelManager = approvalChannels{running: map[string]bool{"telegram": true, config.ChannelWeb: true}}
	return al, msgBus
}

func runCommand(command string) map[string]any {
	return map[string]any{"action": "run", "command": command}
}

func ownerTelegram(chatID string) *bus.InboundContext {
	return &bus.InboundContext{Channel: "telegram", ChatID: chatID, SenderID: "telegram:1", SenderIsOwner: true}
}

type ownerAnswer struct {
	approved bool
	reason   string
}

// askOwnerInBackground asks the owner to approve a call of tool with args, for
// a turn answering inbound, and returns the answer once it comes.
func askOwnerInBackground(
	al *AgentLoop,
	ctx context.Context,
	inbound *bus.InboundContext,
	tool string,
	args map[string]any,
) <-chan ownerAnswer {
	done := make(chan ownerAnswer, 1)
	go func() {
		approved, reason := al.askOwnerApproval(ctx, inbound, tool, args, "")
		done <- ownerAnswer{approved, reason}
	}()
	return done
}

// nextApprovalRequest returns the next request the loop posted and its id.
func nextApprovalRequest(t *testing.T, msgBus *bus.MessageBus) (bus.OutboundMessage, string) {
	t.Helper()
	select {
	case out := <-msgBus.OutboundChan():
		fields := strings.Fields(out.Content)
		for i, field := range fields {
			if field == "/approve" && i+1 < len(fields) {
				return out, fields[i+1]
			}
		}
		t.Fatalf("posted message %q names no request id", out.Content)
	case <-time.After(5 * time.Second):
		t.Fatal("no approval request was posted")
	}
	return bus.OutboundMessage{}, ""
}

func waitOwnerAnswer(t *testing.T, done <-chan ownerAnswer) ownerAnswer {
	t.Helper()
	select {
	case answer := <-done:
		return answer
	case <-time.After(5 * time.Second):
		t.Fatal("the request was not decided")
	}
	return ownerAnswer{}
}

func noPost(t *testing.T, msgBus *bus.MessageBus) {
	t.Helper()
	select {
	case out := <-msgBus.OutboundChan():
		t.Fatalf("posted %q", out.Content)
	default:
	}
}

func reply(al *AgentLoop, inbound *bus.InboundContext, content string) string {
	msg := bus.InboundMessage{Context: *inbound, Content: content}
	msg = bus.NormalizeInboundMessage(msg)
	text, _ := al.approvalReply(context.Background(), msg)
	return text
}

func TestOwnerApprovalApprove(t *testing.T) {
	al, msgBus := newApprovalTestLoop(t)
	inbound := ownerTelegram("chat-1")

	done := askOwnerInBackground(al, context.Background(), inbound, "exec", runCommand("ls -la"))
	out, id := nextApprovalRequest(t, msgBus)

	want := "Approve running: `ls -la`? Reply /approve " + id + " or /deny " + id
	if out.Content != want {
		t.Fatalf("request = %q, want %q", out.Content, want)
	}
	if out.Context.Channel != "telegram" || out.Context.ChatID != "chat-1" {
		t.Fatalf("request posted in %s/%s, want the owner's turn chat telegram/chat-1", out.Context.Channel, out.Context.ChatID)
	}
	if len(id) != approvalIDLength {
		t.Fatalf("id %q has %d characters, want %d", id, len(id), approvalIDLength)
	}

	if got := reply(al, inbound, "/approve "+strings.ToUpper(id)); got != "Approved." {
		t.Fatalf("/approve reply = %q", got)
	}
	if answer := waitOwnerAnswer(t, done); !answer.approved {
		t.Fatalf("answer = %+v, want approved", answer)
	}
}

func TestOwnerApprovalDeny(t *testing.T) {
	al, msgBus := newApprovalTestLoop(t)
	inbound := ownerTelegram("chat-1")

	done := askOwnerInBackground(al, context.Background(), inbound, "exec", runCommand("rm -rf build"))
	_, id := nextApprovalRequest(t, msgBus)

	if got := reply(al, inbound, "/deny "+id); got != "Denied." {
		t.Fatalf("/deny reply = %q", got)
	}
	if answer := waitOwnerAnswer(t, done); answer.approved || answer.reason != approvalDeniedReason {
		t.Fatalf("answer = %+v, want denied with %q", answer, approvalDeniedReason)
	}
}

func TestOwnerApprovalExpires(t *testing.T) {
	al, msgBus := newApprovalTestLoop(t)
	al.approvals.timeout = 50 * time.Millisecond
	inbound := ownerTelegram("chat-1")

	done := askOwnerInBackground(al, context.Background(), inbound, "exec", runCommand("ls"))
	_, id := nextApprovalRequest(t, msgBus)

	if answer := waitOwnerAnswer(t, done); answer.approved || answer.reason != approvalExpiredReason {
		t.Fatalf("answer = %+v, want denied with %q", answer, approvalExpiredReason)
	}
	if got := reply(al, inbound, "/approve "+id); !strings.Contains(got, "No request "+id) {
		t.Fatalf("/approve after expiry = %q, want no waiting request", got)
	}
}

func TestOwnerApprovalWithdrawnWhenTheTurnEnds(t *testing.T) {
	al, msgBus := newApprovalTestLoop(t)
	ctx, cancel := context.WithCancel(context.Background())

	done := askOwnerInBackground(al, ctx, ownerTelegram("chat-1"), "exec", runCommand("ls"))
	nextApprovalRequest(t, msgBus)
	cancel()
	if answer := waitOwnerAnswer(t, done); answer.approved || answer.reason != approvalWithdrawnReason {
		t.Fatalf("answer = %+v, want denied with %q", answer, approvalWithdrawnReason)
	}
}

func TestOwnerApprovalRejectsNonOwnerAnswer(t *testing.T) {
	al, msgBus := newApprovalTestLoop(t)
	owner := ownerTelegram("chat-1")

	done := askOwnerInBackground(al, context.Background(), owner, "exec", runCommand("ls"))
	_, id := nextApprovalRequest(t, msgBus)

	stranger := &bus.InboundContext{Channel: "telegram", ChatID: "chat-1", SenderID: "telegram:2"}
	if got := reply(al, stranger, "/approve "+id); got != "Only the owner can approve or deny requests." {
		t.Fatalf("non-owner /approve reply = %q", got)
	}
	select {
	case answer := <-done:
		t.Fatalf("a non-owner's /approve decided the request: %+v", answer)
	case <-time.After(50 * time.Millisecond):
	}

	if got := reply(al, owner, "/approve "+id); got != "Approved." {
		t.Fatalf("owner /approve reply = %q", got)
	}
	if answer := waitOwnerAnswer(t, done); !answer.approved {
		t.Fatalf("answer = %+v, want approved", answer)
	}
}

func TestOwnerApprovalForAnotherSenderAsksInTheOwnersChat(t *testing.T) {
	al, msgBus := newApprovalTestLoop(t)
	if err := al.state.SetOwnerChat("telegram", "owner-dm"); err != nil {
		t.Fatalf("SetOwnerChat: %v", err)
	}
	group := &bus.InboundContext{Channel: "telegram", ChatID: "group-1", SenderID: "telegram:9", ChatType: "group"}

	done := askOwnerInBackground(al, context.Background(), group, "exec", runCommand("ls"))
	out, id := nextApprovalRequest(t, msgBus)
	if out.Context.Channel != "telegram" || out.Context.ChatID != "owner-dm" {
		t.Fatalf("request posted in %s/%s, want the owner's chat telegram/owner-dm", out.Context.Channel, out.Context.ChatID)
	}
	if got := reply(al, ownerTelegram("owner-dm"), "/approve "+id); got != "Approved." {
		t.Fatalf("/approve reply = %q", got)
	}
	if answer := waitOwnerAnswer(t, done); !answer.approved {
		t.Fatalf("answer = %+v, want approved", answer)
	}
}

func TestOwnerApprovalDeniedWithoutAnOwnerChat(t *testing.T) {
	al, msgBus := newApprovalTestLoop(t)
	stranger := &bus.InboundContext{Channel: "telegram", ChatID: "group-1", SenderID: "telegram:9"}

	approved, reason := al.askOwnerApproval(context.Background(), stranger, "exec", runCommand("ls"), "")
	if approved || reason != approvalNoChatReason {
		t.Fatalf("askOwnerApproval = %v, %q; want denied with %q", approved, reason, approvalNoChatReason)
	}
	noPost(t, msgBus)
}

// The owner approves the whole call, so the request shows all of it: a
// command over many lines, or with backticks, goes in a code block nothing in
// it can close.
func TestOwnerApprovalQuotesTheWholeCommand(t *testing.T) {
	al, msgBus := newApprovalTestLoop(t)
	inbound := ownerTelegram("chat-1")

	long := strings.Repeat("x", 500) + "; rm -rf ~/notes"
	done := askOwnerInBackground(al, context.Background(), inbound, "exec", runCommand(long))
	out, id := nextApprovalRequest(t, msgBus)
	if want := "Approve running: `" + long + "`? Reply /approve " + id + " or /deny " + id; out.Content != want {
		t.Fatalf("request = %q, want %q", out.Content, want)
	}
	reply(al, inbound, "/deny "+id)
	waitOwnerAnswer(t, done)

	multi := "echo `date`\n```\necho done"
	done = askOwnerInBackground(al, context.Background(), inbound, "exec", runCommand(multi))
	out, id = nextApprovalRequest(t, msgBus)
	want := "Approve running this command? Reply /approve " + id + " or /deny " + id +
		"\n````\n" + multi + "\n````"
	if out.Content != want {
		t.Fatalf("request = %q, want %q", out.Content, want)
	}
	reply(al, inbound, "/deny "+id)
	waitOwnerAnswer(t, done)
}

// An exec call is shown as what it does: a write or send-keys sends its data
// or keys, not a command it may also carry, and a run in another folder
// shows the folder. Only a plain run reads as its command.
func TestOwnerApprovalShowsWhatAnExecCallDoes(t *testing.T) {
	al, msgBus := newApprovalTestLoop(t)
	inbound := ownerTelegram("chat-1")

	for _, args := range []map[string]any{
		{"action": "write", "sessionId": "s1", "data": "rm -rf ~/notes\n", "command": "echo safe"},
		{"action": "send-keys", "sessionId": "s1", "keys": "rm -rf ~/notes Enter", "command": "echo safe"},
		{"action": "run", "command": "rm -rf build", "cwd": "/home/me"},
	} {
		done := askOwnerInBackground(al, context.Background(), inbound, "exec", args)
		out, id := nextApprovalRequest(t, msgBus)
		want := "Approve calling exec with `" + compactJSON(args) + "`? Reply /approve " + id + " or /deny " + id
		if out.Content != want {
			t.Fatalf("request = %q, want %q", out.Content, want)
		}
		reply(al, inbound, "/deny "+id)
		waitOwnerAnswer(t, done)
	}

	// A run whose other arguments only shape how it runs reads as the command.
	args := map[string]any{"action": "run", "command": "make test", "timeout": 60, "background": true}
	done := askOwnerInBackground(al, context.Background(), inbound, "exec", args)
	out, id := nextApprovalRequest(t, msgBus)
	if want := "Approve running: `make test`? Reply /approve " + id + " or /deny " + id; out.Content != want {
		t.Fatalf("request = %q, want %q", out.Content, want)
	}
	reply(al, inbound, "/deny "+id)
	waitOwnerAnswer(t, done)
}

// A call too long to show in full is refused without asking: the owner would
// approve what they could not read.
func TestOwnerApprovalRefusesACallTooLongToShow(t *testing.T) {
	al, msgBus := newApprovalTestLoop(t)

	approved, reason := al.askOwnerApproval(context.Background(), ownerTelegram("chat-1"), "exec",
		runCommand(strings.Repeat("x", approvalQuoteMaxRunes+1)), "")
	if approved || !strings.Contains(reason, "too long") {
		t.Fatalf("askOwnerApproval = %v, %q; want refused as too long", approved, reason)
	}
	noPost(t, msgBus)
}

func TestOwnerApprovalQuotesAToolAndItsArguments(t *testing.T) {
	al, msgBus := newApprovalTestLoop(t)
	inbound := ownerTelegram("chat-1")

	done := askOwnerInBackground(al, context.Background(), inbound, "install_skill",
		map[string]any{"slug": "acme/notes", "force": true})
	out, id := nextApprovalRequest(t, msgBus)
	want := "Approve calling install_skill with `{\"force\":true,\"slug\":\"acme/notes\"}`? Reply /approve " +
		id + " or /deny " + id
	if out.Content != want {
		t.Fatalf("request = %q, want %q", out.Content, want)
	}
	reply(al, inbound, "/deny "+id)
	waitOwnerAnswer(t, done)

	content := strings.Repeat("y", 500) + "`tail`"
	done = askOwnerInBackground(al, context.Background(), inbound, "write_file",
		map[string]any{"content": content})
	out, id = nextApprovalRequest(t, msgBus)
	want = "Approve calling write_file with these arguments? Reply /approve " + id + " or /deny " + id +
		"\n```\n{\"content\":\"" + content + "\"}\n```"
	if out.Content != want {
		t.Fatalf("request = %q, want %q", out.Content, want)
	}
	reply(al, inbound, "/deny "+id)
	waitOwnerAnswer(t, done)
}

func TestApproveCommandReachesTheReceiveLoop(t *testing.T) {
	al, msgBus := newApprovalTestLoop(t)
	web := &bus.InboundContext{Channel: config.ChannelWeb, ChatID: "web-session", SenderID: "web"}

	done := askOwnerInBackground(al, context.Background(), web, "exec", runCommand("ls"))
	out, id := nextApprovalRequest(t, msgBus)
	if out.Context.Channel != config.ChannelWeb || out.Context.ChatID != "web-session" {
		t.Fatalf("request posted in %s/%s, want the web chat", out.Context.Channel, out.Context.ChatID)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = al.Run(ctx) }()
	if err := msgBus.PublishInbound(ctx, bus.InboundMessage{Context: *web, Content: "/approve " + id}); err != nil {
		t.Fatalf("PublishInbound: %v", err)
	}
	if answer := waitOwnerAnswer(t, done); !answer.approved {
		t.Fatalf("answer = %+v, want approved", answer)
	}
}

func TestOwnerChatIsRecordedForTheOwnersTurnsOnly(t *testing.T) {
	al, _ := newApprovalTestLoop(t)
	if al.StateManager() == nil {
		t.Fatal("StateManager() = nil")
	}

	al.recordOwnerChat(context.Background(), &bus.InboundContext{Channel: "telegram", ChatID: "stranger"})
	al.recordOwnerChat(context.Background(), &bus.InboundContext{Channel: "cli", ChatID: "direct"})
	al.recordOwnerChat(withScheduledTurn(context.Background()), ownerTelegram("job-chat"))
	if channel, chatID := al.StateManager().GetOwnerChat(); channel != "" || chatID != "" {
		t.Fatalf("owner chat = %s/%s, want none recorded", channel, chatID)
	}

	al.recordOwnerChat(context.Background(), ownerTelegram("owner-dm"))
	if channel, chatID := al.StateManager().GetOwnerChat(); channel != "telegram" || chatID != "owner-dm" {
		t.Fatalf("owner chat = %s/%s, want telegram/owner-dm", channel, chatID)
	}
	al.recordOwnerChat(context.Background(), &bus.InboundContext{Channel: config.ChannelWeb, ChatID: "web-1"})
	if channel, chatID := al.StateManager().GetOwnerChat(); channel != config.ChannelWeb || chatID != "web-1" {
		t.Fatalf("owner chat = %s/%s, want web/web-1", channel, chatID)
	}
}

func TestTerminalTurnAsksInTheTerminalOnlyWhenItShowsRequests(t *testing.T) {
	al, msgBus := newApprovalTestLoop(t)
	terminal := &bus.InboundContext{Channel: "cli", ChatID: "direct", SenderID: "cron"}

	// `compa-kernel agent -m` prints only the reply: nobody would see it.
	if approved, reason := al.askOwnerApproval(context.Background(), terminal, "exec", runCommand("ls"), ""); approved ||
		reason != approvalNoChatReason {
		t.Fatalf("askOwnerApproval = %v, %q; want denied with %q", approved, reason, approvalNoChatReason)
	}

	al.SetTerminalChat(true)
	done := askOwnerInBackground(al, context.Background(), terminal, "exec", runCommand("ls"))
	out, id := nextApprovalRequest(t, msgBus)
	if out.Context.Channel != "cli" || out.Context.ChatID != "direct" {
		t.Fatalf("request posted in %s/%s, want the terminal's chat", out.Context.Channel, out.Context.ChatID)
	}
	if got, err := al.ProcessDirect(context.Background(), "/approve "+id, "cli-session"); err != nil || got != "Approved." {
		t.Fatalf("terminal /approve = %q, %v; want Approved.", got, err)
	}
	if answer := waitOwnerAnswer(t, done); !answer.approved {
		t.Fatalf("answer = %+v, want approved", answer)
	}
}

// scheduledCommand is the call a cron command job makes through the gate.
func scheduledCommand(command string) approval.Call {
	return approval.Call{
		Tool:      "exec",
		Arguments: runCommand(command),
		Origin:    approval.OriginCron,
		Job:       "disk check",
		Channel:   "cli",
		ChatID:    "direct",
	}
}

func checkInBackground(al *AgentLoop, ctx context.Context, call approval.Call) <-chan error {
	done := make(chan error, 1)
	go func() { done <- al.Check(ctx, call) }()
	return done
}

func waitCheck(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Check did not return")
	}
	return nil
}

func TestCheckDecidesByThePolicy(t *testing.T) {
	tests := []struct {
		name   string
		policy approval.Policy
		call   approval.Call
		want   string // "" lets the call run
	}{
		{"default allow", approval.Policy{}, scheduledCommand("df -h"), ""},
		{
			"denied",
			approval.Policy{Rules: []approval.Rule{{Tool: "exec", Origin: []approval.Origin{approval.OriginCron}, Action: approval.Deny}}},
			scheduledCommand("df -h"),
			approvalPolicyDeniedReason,
		},
		{
			"another origin's rule",
			approval.Policy{Rules: []approval.Rule{{Tool: "exec", Origin: []approval.Origin{approval.OriginChat}, Action: approval.Deny}}},
			scheduledCommand("df -h"),
			"",
		},
		{
			"hidden",
			approval.Policy{Rules: []approval.Rule{{Tool: "exec", Action: approval.Hide}}},
			scheduledCommand("df -h"),
			`Tool "exec" is not available.`,
		},
		{
			"asked, with no chat to ask in",
			approval.Policy{Rules: []approval.Rule{{Tool: "exec", Action: approval.Ask}}},
			scheduledCommand("df -h"),
			approvalNoChatReason,
		},
		{
			"an origin-less call is cron's",
			approval.Policy{Rules: []approval.Rule{{Origin: []approval.Origin{approval.OriginCron}, Action: approval.Deny}}},
			approval.Call{Tool: "exec", Arguments: runCommand("df -h")},
			approvalPolicyDeniedReason,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			al, msgBus := newApprovalTestLoop(t)
			al.cfg.Tools.Approval = tt.policy
			err := al.Check(context.Background(), tt.call)
			if tt.want == "" && err != nil {
				t.Fatalf("Check() = %v, want the call allowed", err)
			}
			if tt.want != "" && (err == nil || err.Error() != tt.want) {
				t.Fatalf("Check() = %v, want %q", err, tt.want)
			}
			noPost(t, msgBus)
		})
	}
}

func TestCheckAsksTheOwnerInTheOwnersChat(t *testing.T) {
	al, msgBus := newApprovalTestLoop(t)
	al.cfg.Tools.Approval = approval.Policy{Rules: []approval.Rule{{Tool: "exec", Action: approval.Ask}}}
	if err := al.state.SetOwnerChat("telegram", "owner-dm"); err != nil {
		t.Fatalf("SetOwnerChat: %v", err)
	}
	al.SetTerminalChat(true) // even so, a job's request isn't the terminal's

	done := checkInBackground(al, context.Background(), scheduledCommand("df -h"))
	out, id := nextApprovalRequest(t, msgBus)
	// The job reports to the terminal's chat, but nobody wrote the job's
	// call: the request goes to the owner's chat.
	if out.Context.Channel != "telegram" || out.Context.ChatID != "owner-dm" {
		t.Fatalf("request posted in %s/%s, want the owner's chat telegram/owner-dm", out.Context.Channel, out.Context.ChatID)
	}
	want := "Approve running: `df -h` (scheduled job \"disk check\")? Reply /approve " + id + " or /deny " + id
	if out.Content != want {
		t.Fatalf("request = %q, want %q", out.Content, want)
	}
	if got := reply(al, ownerTelegram("owner-dm"), "/approve "+id); got != "Approved." {
		t.Fatalf("/approve reply = %q", got)
	}
	if err := waitCheck(t, done); err != nil {
		t.Fatalf("Check() = %v, want approved", err)
	}

	done = checkInBackground(al, context.Background(), scheduledCommand("df -h"))
	_, id = nextApprovalRequest(t, msgBus)
	reply(al, ownerTelegram("owner-dm"), "/deny "+id)
	if err := waitCheck(t, done); err == nil || err.Error() != approvalDeniedReason {
		t.Fatalf("Check() = %v, want %q", err, approvalDeniedReason)
	}

	al.approvals.timeout = 50 * time.Millisecond
	done = checkInBackground(al, context.Background(), scheduledCommand("df -h"))
	nextApprovalRequest(t, msgBus)
	if err := waitCheck(t, done); err == nil || err.Error() != approvalExpiredReason {
		t.Fatalf("Check() = %v, want %q", err, approvalExpiredReason)
	}
}

func TestCheckAsksTheApproversFirst(t *testing.T) {
	al, msgBus := newApprovalTestLoop(t)
	al.cfg.Tools.Approval = approval.Policy{Rules: []approval.Rule{{Tool: "exec", Action: approval.Ask}}}
	approver := &recordingApprover{decision: ApprovalDecision{Reason: "not now"}}
	if err := al.MountHook(NamedHook("approver", approver)); err != nil {
		t.Fatalf("MountHook: %v", err)
	}

	err := al.Check(context.Background(), scheduledCommand("df -h"))
	if err == nil || !strings.Contains(err.Error(), "not now") {
		t.Fatalf("Check() = %v, want the approver's denial", err)
	}
	req := approver.last()
	if req == nil || req.Tool != "exec" || req.Origin != approval.OriginCron || req.Job != "disk check" ||
		req.Info.Source != approval.SourceBuiltin || req.Decision.Action != approval.Ask || req.Decision.Index != 0 {
		t.Fatalf("approval request = %+v", req)
	}
	if req.Context == nil || req.Context.Inbound == nil || req.Context.Inbound.Channel != "cli" {
		t.Fatalf("approval request context = %+v, want the job's chat", req.Context)
	}
	noPost(t, msgBus)
}

func TestCheckWaitIsNotPartOfTheJobTimeout(t *testing.T) {
	al, msgBus := newApprovalTestLoop(t)
	al.cfg.Tools.Approval = approval.Policy{Rules: []approval.Rule{{Tool: "exec", Action: approval.Ask}}}
	if err := al.state.SetOwnerChat("telegram", "owner-dm"); err != nil {
		t.Fatalf("SetOwnerChat: %v", err)
	}

	cs := cron.NewCronService(filepath.Join(t.TempDir(), "jobs.json"), nil, cron.WithJobTimeout(100*time.Millisecond))
	checked := make(chan error, 1)
	cs.SetOnJobContext(func(ctx context.Context, job *cron.CronJob) (string, error) {
		checked <- al.Check(ctx, scheduledCommand("df -h"))
		return "ok", ctx.Err()
	})
	if err := cs.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer cs.Stop()
	at := time.Now().Add(50 * time.Millisecond).UnixMilli()
	if _, err := cs.AddJob("disk check", cron.CronSchedule{Kind: "at", AtMS: &at}, "", "", ""); err != nil {
		t.Fatalf("AddJob: %v", err)
	}

	_, id := nextApprovalRequest(t, msgBus)
	time.Sleep(300 * time.Millisecond) // the owner answers after the job timeout
	if got := reply(al, ownerTelegram("owner-dm"), "/approve "+id); got != "Approved." {
		t.Fatalf("/approve reply = %q", got)
	}
	if err := waitCheck(t, checked); err != nil {
		t.Fatalf("Check() = %v, want approved: the wait counted against the job timeout", err)
	}
}
