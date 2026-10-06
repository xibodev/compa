package agent

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xibodev/compa/v2/pkg/bus"
)

// syncBuffer is a bytes.Buffer safe for the chat's concurrent writers.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// returnsSoon fails the test when fn doesn't return within a few seconds.
func returnsSoon(t *testing.T, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		fn()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("%s did not return", what)
	}
}

func TestTerminalChatAnswersAnApprovalWhileTheTurnWaits(t *testing.T) {
	msgBus := bus.NewMessageBus()
	defer msgBus.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	approved := make(chan struct{})
	var out syncBuffer
	var mu sync.Mutex
	var ran []string
	chat := newTerminalChat(func(input string) (string, error) {
		mu.Lock()
		ran = append(ran, input)
		mu.Unlock()
		switch input {
		case "run ls":
			// The agent asks the owner in the chat and waits for the answer.
			_ = msgBus.PublishOutbound(context.Background(), bus.OutboundMessage{
				Context: bus.NewOutboundContext(cliChannel, "direct", ""),
				Content: "Approve running: `ls`? Reply /approve abc234 or /deny abc234",
			})
			select {
			case <-approved:
				return "listed", nil
			case <-time.After(5 * time.Second):
				return "no answer", nil
			}
		case "/approve abc234":
			close(approved)
			return "Approved.", nil
		}
		return "reply to " + input, nil
	}, &out)
	go chat.printPosted(ctx, msgBus.OutboundChan())

	returnsSoon(t, "a turn that posted an approval request", func() { chat.handle("run ls") })
	if !strings.Contains(out.String(), "Approve running: `ls`?") {
		t.Fatalf("output = %q, want the approval request", out.String())
	}
	// While the request waits, other lines queue up behind the turn instead
	// of keeping the answer from being read.
	returnsSoon(t, "a line typed while a request waits", func() { chat.handle("hello") })
	returnsSoon(t, "the answer", func() { chat.handle("/approve abc234") })

	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), "reply to hello") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	got := out.String()
	listed, hello := strings.Index(got, "listed"), strings.Index(got, "reply to hello")
	if listed < 0 || hello < 0 || listed > hello {
		t.Fatalf("output = %q, want the approved turn's reply before the next turn's", got)
	}

	// With the turns done, a line waits for its reply again.
	chat.handle("next")
	if !strings.Contains(out.String(), "reply to next") {
		t.Fatalf("output = %q, want the reply printed before the next line is read", out.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(ran, "|") != "run ls|/approve abc234|hello|next" {
		t.Fatalf("ran %q", ran)
	}
}

func TestTerminalChatPrintsOnlyChatMessagesForTheTerminal(t *testing.T) {
	cli := bus.NewOutboundContext(cliChannel, "direct", "")
	feedback := bus.NewOutboundContext(cliChannel, "direct", "")
	feedback.Raw = map[string]string{"message_kind": "tool_feedback"}
	for _, tc := range []struct {
		msg  bus.OutboundMessage
		want bool
	}{
		{bus.OutboundMessage{Context: cli, Content: "Approve running: `ls`?"}, true},
		{bus.OutboundMessage{Channel: cliChannel, Content: "posted"}, true},
		{bus.OutboundMessage{Context: feedback, Content: "🔧 exec"}, false},
		{bus.OutboundMessage{Context: bus.NewOutboundContext("telegram", "1", ""), Content: "hi"}, false},
		{bus.OutboundMessage{Context: cli, Content: "  "}, false},
	} {
		if got := isTerminalChatMessage(tc.msg); got != tc.want {
			t.Errorf("isTerminalChatMessage(%+v) = %v, want %v", tc.msg, got, tc.want)
		}
	}
}
