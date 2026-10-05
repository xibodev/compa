// Compa - Ultra-lightweight personal AI agent

package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/xibodev/compa/pkg/bus"
	"github.com/xibodev/compa/pkg/commands"
	"github.com/xibodev/compa/pkg/constants"
	"github.com/xibodev/compa/pkg/cron"
	"github.com/xibodev/compa/pkg/logger"
)

// In-chat approvals: a call the approval policy says to ask about, when no
// approver is registered, waits until the owner answers "/approve <id>" or
// "/deny <id>" in chat. The request is posted in the turn's chat when the
// owner wrote the message the turn answers, and otherwise in the owner's chat.

const (
	// ownerApprovalTimeout is how long a request waits; no answer denies it.
	ownerApprovalTimeout = 10 * time.Minute
	// approvalIDLength is the length of the id /approve and /deny name.
	approvalIDLength = 6
	// approvalIDAlphabet leaves out characters easily misread for others.
	approvalIDAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	// approvalQuoteMaxRunes bounds the command or arguments a request quotes.
	// The owner approves the whole call, so a request shows it in full, and a
	// call longer than this is refused rather than asked about: a chat is no
	// place to review it.
	approvalQuoteMaxRunes = 3000

	approvalDeniedReason    = "The owner denied this call."
	approvalExpiredReason   = "No approval within 10 minutes."
	approvalNoChatReason    = "The owner's approval is needed, and there is no chat to ask the owner in."
	approvalWithdrawnReason = "The turn ended before the owner answered."
	// approvalTooLongFormat refuses a call too long to show the owner in full.
	approvalTooLongFormat = "The call is too long (%d characters) to show the owner in full for approval; " +
		"split it into shorter steps."
)

var errNoApprovalChat = errors.New("no chat to ask the owner in")

type approvalOutcome int

const (
	approvalGranted approvalOutcome = iota
	approvalRefused
	approvalExpired
	// approvalUnavailable: the owner could not be asked, or the turn ended
	// first.
	approvalUnavailable
)

// ownerApprovals holds the requests waiting for the owner's answer.
type ownerApprovals struct {
	mu      sync.Mutex
	pending map[string]chan bool
	timeout time.Duration
}

func newOwnerApprovals() *ownerApprovals {
	return &ownerApprovals{pending: make(map[string]chan bool), timeout: ownerApprovalTimeout}
}

// ask registers a request under a new id, posts it through post and waits
// for the owner's answer, the timeout, or the end of ctx.
func (a *ownerApprovals) ask(ctx context.Context, post func(id string) error) (approvalOutcome, error) {
	answer := make(chan bool, 1)
	a.mu.Lock()
	id := a.newIDLocked()
	a.pending[id] = answer
	timeout := a.timeout
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.pending, id)
		a.mu.Unlock()
	}()

	if err := post(id); err != nil {
		return approvalUnavailable, err
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case approved := <-answer:
		if approved {
			return approvalGranted, nil
		}
		return approvalRefused, nil
	case <-timer.C:
		return approvalExpired, nil
	case <-ctx.Done():
		return approvalUnavailable, ctx.Err()
	}
}

// answer delivers the owner's answer to request id. It reports false when no
// request of that id is waiting: unknown, already answered or expired.
func (a *ownerApprovals) answer(id string, approve bool) bool {
	id = strings.ToLower(strings.TrimSpace(id))
	a.mu.Lock()
	defer a.mu.Unlock()
	answer, ok := a.pending[id]
	if !ok {
		return false
	}
	delete(a.pending, id)
	answer <- approve // buffered: the waiter takes it, or has stopped waiting
	return true
}

// newIDLocked returns an id no waiting request has. Must be called with the
// lock held.
func (a *ownerApprovals) newIDLocked() string {
	for {
		buf := make([]byte, approvalIDLength)
		_, _ = rand.Read(buf)
		for i, b := range buf {
			buf[i] = approvalIDAlphabet[int(b)%len(approvalIDAlphabet)]
		}
		if id := string(buf); a.pending[id] == nil {
			return id
		}
	}
}

// askOwnerApproval asks the owner to approve a call of toolName with args,
// for the scheduled job named job if any, and waits for the answer. reason
// tells the model why a call that was not approved may not run.
func (al *AgentLoop) askOwnerApproval(
	ctx context.Context,
	inbound *bus.InboundContext,
	toolName string,
	args map[string]any,
	job string,
) (approved bool, reason string) {
	if n := utf8.RuneCountInString(approvalQuote(toolName, args)); n > approvalQuoteMaxRunes {
		return false, fmt.Sprintf(approvalTooLongFormat, n)
	}
	outcome, err := al.askOwner(ctx, inbound, func(id string) string {
		return ownerApprovalText(toolName, args, job, id)
	})
	switch outcome {
	case approvalGranted:
		return true, ""
	case approvalRefused:
		return false, approvalDeniedReason
	case approvalExpired:
		return false, approvalExpiredReason
	}
	if errors.Is(err, errNoApprovalChat) {
		return false, approvalNoChatReason
	}
	logger.WarnCF("agent", "Approval request could not be completed", map[string]any{"error": fmt.Sprint(err)})
	return false, approvalWithdrawnReason
}

// approvalQuote is what a request shows of a call: exec's command, or the
// arguments of any other tool.
func approvalQuote(toolName string, args map[string]any) string {
	if command, ok := args["command"].(string); ok && toolName == "exec" {
		return command
	}
	return compactJSON(args)
}

// ownerApprovalText is the request the owner answers: the whole command to
// run, or the tool and all of its arguments, and the scheduled job it runs
// for. One line without backticks is quoted inline; anything else goes in a
// code block that nothing in it can close, so the call reads as it is.
func ownerApprovalText(toolName string, args map[string]any, job, id string) string {
	var forJob string
	if job != "" {
		forJob = fmt.Sprintf(" (scheduled job %q)", job)
	}
	reply := fmt.Sprintf("Reply /approve %s or /deny %s", id, id)
	quote := approvalQuote(toolName, args)
	inline := !strings.ContainsAny(quote, "`\r\n")
	if _, ok := args["command"].(string); ok && toolName == "exec" {
		if inline {
			return fmt.Sprintf("Approve running: `%s`%s? %s", quote, forJob, reply)
		}
		return fmt.Sprintf("Approve running this command%s? %s\n%s", forJob, reply, codeBlock(quote))
	}
	if inline {
		return fmt.Sprintf("Approve calling %s with `%s`%s? %s", toolName, quote, forJob, reply)
	}
	return fmt.Sprintf("Approve calling %s with these arguments%s? %s\n%s", toolName, forJob, reply, codeBlock(quote))
}

// codeBlock fences text with more backticks than any run of them in it, so
// nothing in text ends the block early.
func codeBlock(text string) string {
	longest, run := 0, 0
	for _, r := range text {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	fence := strings.Repeat("`", max(3, longest+1))
	return fence + "\n" + text + "\n" + fence
}

// compactJSON renders args on one line, as written.
func compactJSON(args map[string]any) string {
	if args == nil {
		args = map[string]any{}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(args); err != nil {
		return fmt.Sprint(args)
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

// turnSenderIsOwner reports whether the owner wrote the message a turn
// answers. A scheduled turn answers nobody's message.
func turnSenderIsOwner(ctx context.Context, inbound *bus.InboundContext) bool {
	return !isScheduledTurn(ctx) && inboundFromOwner(inbound)
}

// askOwner posts the request text gives for a new id where the owner can
// answer it, and waits for the answer. A scheduled job's run doesn't count
// the wait against its timeout.
func (al *AgentLoop) askOwner(
	ctx context.Context,
	inbound *bus.InboundContext,
	text func(id string) string,
) (approvalOutcome, error) {
	if al.approvals == nil {
		return approvalUnavailable, errNoApprovalChat
	}
	target, ok := al.approvalTarget(ctx, inbound)
	if !ok {
		return approvalUnavailable, errNoApprovalChat
	}
	resume := cron.PauseJobTimeout(ctx)
	defer resume()
	return al.approvals.ask(ctx, func(id string) error {
		return al.postApprovalRequest(ctx, target, text(id))
	})
}

// approvalChat is the chat a request is posted in.
type approvalChat struct {
	channel, chatID string
	// inbound is the turn's message when the request goes to its chat, to
	// keep its account and topic.
	inbound *bus.InboundContext
}

// approvalTarget returns the chat to ask the owner in: the turn's own when
// the owner wrote its message, otherwise the owner's chat. ok is false when
// that chat can't receive the request.
func (al *AgentLoop) approvalTarget(ctx context.Context, inbound *bus.InboundContext) (approvalChat, bool) {
	var target approvalChat
	if turnSenderIsOwner(ctx, inbound) {
		target = approvalChat{channel: inbound.Channel, chatID: inbound.ChatID, inbound: inbound}
	} else if al.state != nil {
		target.channel, target.chatID = al.state.GetOwnerChat()
	}
	return target, al.canPostTo(target.channel, target.chatID)
}

// canPostTo reports whether a message to channel's chatID reaches someone:
// the channel is running, and isn't one that receives nothing. The terminal
// receives the requests only when it shows them (SetTerminalChat).
func (al *AgentLoop) canPostTo(channel, chatID string) bool {
	channel = strings.TrimSpace(channel)
	if channel == "" || strings.TrimSpace(chatID) == "" || al.bus == nil {
		return false
	}
	if channel == terminalChannel {
		return al.terminalChat.Load()
	}
	if constants.IsInternalChannel(channel) {
		return false
	}
	cm := al.currentChannelManager()
	if cm == nil {
		return false
	}
	_, ok := cm.GetChannel(channel)
	return ok
}

// SetTerminalChat says whether the terminal shows what the loop posts in its
// chat, as interactive `compa-kernel agent` does. Then the owner's approval of
// a terminal turn's call is asked there; otherwise the call is denied, as
// nobody would see the request.
func (al *AgentLoop) SetTerminalChat(enabled bool) {
	al.terminalChat.Store(enabled)
}

func (al *AgentLoop) postApprovalRequest(ctx context.Context, target approvalChat, text string) error {
	outCtx := bus.NewOutboundContext(target.channel, target.chatID, "")
	if target.inbound != nil {
		outCtx = outboundContextFromInbound(target.inbound, target.channel, target.chatID, target.inbound.MessageID)
	}
	pubCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return al.bus.PublishOutbound(pubCtx, bus.OutboundMessage{Context: outCtx, Content: text})
}

// parseApprovalReply parses "/approve <id>" and "/deny <id>"; ok is false
// for any other message.
func parseApprovalReply(content string) (id string, approve, ok bool) {
	name, isCommand := commands.CommandName(content)
	if !isCommand || (name != "approve" && name != "deny") {
		return "", false, false
	}
	if fields := strings.Fields(content); len(fields) > 1 {
		id = strings.ToLower(fields[1])
	}
	return id, name == "approve", true
}

// approvalReply answers a waiting request with msg's /approve or /deny and
// returns the reply for msg's chat; handled is false for any other message.
// Only the owner's answer counts, whatever commands.owner_only says.
func (al *AgentLoop) approvalReply(ctx context.Context, msg bus.InboundMessage) (reply string, handled bool) {
	id, approve, ok := parseApprovalReply(msg.Content)
	if !ok {
		return "", false
	}
	if isScheduledTurn(ctx) || !senderIsOwner(msg) {
		logger.InfoCF("agent", "Approval answer refused: sender is not the owner",
			map[string]any{"channel": msg.Channel})
		return "Only the owner can approve or deny requests.", true
	}
	al.recordOwnerChat(ctx, &msg.Context)
	if id == "" {
		return "Usage: /approve <id> or /deny <id>", true
	}
	if al.approvals == nil || !al.approvals.answer(id, approve) {
		return fmt.Sprintf("No request %s is waiting for an answer; it may have expired.", id), true
	}
	if approve {
		return "Approved.", true
	}
	return "Denied.", true
}

// handleApprovalReply acts on an /approve or /deny as soon as it arrives,
// reporting whether msg was one: the turn waiting for the answer may hold
// msg's session, or every worker.
func (al *AgentLoop) handleApprovalReply(ctx context.Context, msg bus.InboundMessage, sessionKey string) bool {
	msg = bus.NormalizeInboundMessage(msg)
	reply, handled := al.approvalReply(ctx, msg)
	if !handled {
		return false
	}
	go al.publishDirectReply(ctx, msg, sessionKey, reply)
	return true
}
