package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xibodev/compa/v3/pkg/bus"
	"github.com/xibodev/compa/v3/pkg/config"
	"github.com/xibodev/compa/v3/pkg/media"
	"github.com/xibodev/compa/v3/pkg/providers"
)

// observerChannel observes turns and records, in order, the notices and
// messages that reach it.
type observerChannel struct {
	fakeChannel
	mu     sync.Mutex
	events []string
	ended  chan struct{}
}

func newObserverChannel() *observerChannel {
	return &observerChannel{ended: make(chan struct{}, 8)}
}

func (c *observerChannel) record(event string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event)
}

func (c *observerChannel) recorded() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.events...)
}

func (c *observerChannel) Send(_ context.Context, msg bus.OutboundMessage) ([]string, error) {
	c.record("send " + msg.Content)
	return nil, nil
}

func (c *observerChannel) TurnStarted(_ context.Context, _ string, turn bus.TurnNotice) error {
	c.record("start " + strings.Join(turn.MessageIDs, ","))
	return nil
}

func (c *observerChannel) TurnEnded(_ context.Context, _ string, turn bus.TurnNotice) error {
	event := "end " + turn.Status + " " + strings.Join(turn.MessageIDs, ",")
	if turn.Error != "" {
		event += " (" + turn.Error + ")"
	}
	c.record(event)
	c.ended <- struct{}{}
	return nil
}

func turnNoticeTestLoop(t *testing.T, provider providers.LLMProvider) (*AgentLoop, *bus.MessageBus, *observerChannel) {
	t.Helper()
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         t.TempDir(),
				ModelName:         "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
	}
	msgBus := bus.NewMessageBus()
	al := NewAgentLoop(cfg, msgBus, provider)
	channel := newObserverChannel()
	al.SetChannelManager(newStartedTestChannelManager(t, msgBus, media.NewFileMediaStore(), "test", channel))

	runCtx, cancelRun := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- al.Run(runCtx) }()
	t.Cleanup(func() {
		cancelRun()
		select {
		case <-runErr:
		case <-time.After(turnNoticeTestWait):
			t.Error("Run did not stop")
		}
	})
	return al, msgBus, channel
}

// turnNoticeTestWait bounds each wait for something a turn must do; only a
// failing test waits that long.
const turnNoticeTestWait = 10 * time.Second

func turnNoticeInbound(id, content string) bus.InboundMessage {
	return bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:   "test",
			ChatID:    "chat1",
			ChatType:  "direct",
			SenderID:  "user1",
			MessageID: id,
		},
		Content: content,
	}
}

func publishTurnNoticeInbound(t *testing.T, msgBus *bus.MessageBus, id, content string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := msgBus.PublishInbound(ctx, turnNoticeInbound(id, content)); err != nil {
		t.Fatalf("PublishInbound(%s) error = %v", id, err)
	}
}

// checkTurnFrames checks that events open with a turn's start, close with
// its end, and that every message the channel got came inside a turn.
func checkTurnFrames(t *testing.T, events []string) {
	t.Helper()
	open := false
	for _, event := range events {
		switch {
		case strings.HasPrefix(event, "start "):
			if open {
				t.Fatalf("a turn started inside another: %q", events)
			}
			open = true
		case strings.HasPrefix(event, "end "):
			if !open {
				t.Fatalf("a turn ended without starting: %q", events)
			}
			open = false
		case !open:
			t.Fatalf("%q came outside a turn: %q", event, events)
		}
	}
	if open {
		t.Fatalf("a turn never ended: %q", events)
	}
}

// A channel that observes turns hears that a turn started before any of its
// reply, and that it ended after all of it, with the messages it answers,
// including one sent while it ran.
func TestTurnNotices_FrameTheReplyAndNameItsMessages(t *testing.T) {
	provider := &lateSteeringProvider{
		firstCallStarted: make(chan struct{}),
		releaseFirstCall: make(chan struct{}),
	}
	al, msgBus, channel := turnNoticeTestLoop(t, provider)

	publishTurnNoticeInbound(t, msgBus, "msg-1", "first message")
	select {
	case <-provider.firstCallStarted:
	case <-time.After(turnNoticeTestWait):
		t.Fatal("the first model call did not start")
	}

	// msg-2 joins the running turn: the first answer is released once it
	// waits as steering.
	publishTurnNoticeInbound(t, msgBus, "msg-2", "late append")
	sessionKey, _, ok := al.resolveSteeringTarget(turnNoticeInbound("msg-2", "late append"))
	if !ok {
		t.Fatal("no session for the test chat")
	}
	deadline := time.Now().Add(turnNoticeTestWait)
	for al.pendingSteeringCountForScope(sessionKey) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("msg-2 never waited as steering")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(provider.releaseFirstCall)

	select {
	case <-channel.ended:
	case <-time.After(turnNoticeTestWait):
		t.Fatalf("the turn did not end: %q", channel.recorded())
	}
	events := channel.recorded()
	checkTurnFrames(t, events)
	want := []string{"start msg-1", "send first response", "send continued response", "end completed msg-1,msg-2"}
	if strings.Join(events, "|") != strings.Join(want, "|") {
		t.Fatalf("events = %q, want %q", events, want)
	}
}

// failingProvider fails every call with an error that isn't retried.
type failingProvider struct{}

func (failingProvider) Chat(context.Context, []providers.Message, []providers.ToolDefinition, string, map[string]any) (*providers.LLMResponse, error) {
	return nil, errors.New("model unavailable")
}

func (failingProvider) GetDefaultModel() string { return "failing-model" }

// A failed turn ends after its error reply, with status error and the
// reason.
func TestTurnNotices_AFailedTurnEndsWithItsError(t *testing.T) {
	_, msgBus, channel := turnNoticeTestLoop(t, failingProvider{})

	publishTurnNoticeInbound(t, msgBus, "msg-1", "hello")
	select {
	case <-channel.ended:
	case <-time.After(turnNoticeTestWait):
		t.Fatalf("the turn did not end: %q", channel.recorded())
	}

	events := channel.recorded()
	checkTurnFrames(t, events)
	if len(events) != 3 || events[0] != "start msg-1" || !strings.HasPrefix(events[1], "send ") ||
		!strings.HasPrefix(events[2], "end error msg-1 (") || !strings.Contains(events[2], "model unavailable") {
		t.Fatalf("events = %q, want start, the error reply, then end with the error", events)
	}
}
