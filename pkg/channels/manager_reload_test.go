package channels

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xibodev/compa/v3/pkg/bus"
	"github.com/xibodev/compa/v3/pkg/config"
)

// reloadTestType is a channel type whose factory builds mock channels, so
// that Reload runs end to end.
const reloadTestType = "reload_test"

// reloadTestChannel records what the manager does with one instance.
type reloadTestChannel struct {
	mockChannel
	stopped atomic.Bool
}

type reloadTestFactory struct {
	created chan *reloadTestChannel
	sent    chan string // "<channel>:<content>" of every delivered message
}

// registerReloadTestFactory registers the reload_test factory. A channel
// whose settings contain "fail" fails to initialize; one whose settings
// contain "refuse" refuses every message.
func registerReloadTestFactory(t *testing.T) *reloadTestFactory {
	t.Helper()
	f := &reloadTestFactory{
		created: make(chan *reloadTestChannel, 16),
		sent:    make(chan string, 64),
	}
	RegisterFactory(reloadTestType, func(name, _ string, cfg *config.Config, _ *bus.MessageBus) (Channel, error) {
		settings := string(cfg.Channels[name].Settings)
		if strings.Contains(settings, "fail") {
			return nil, errors.New("init failed")
		}
		ch := &reloadTestChannel{}
		// Platform constructors hand allow_from to NewBaseChannel.
		ch.allowList = cfg.Channels[name].AllowFrom
		ch.sendFn = func(_ context.Context, msg bus.OutboundMessage) error {
			if strings.Contains(settings, "refuse") {
				return ErrSendFailed
			}
			f.sent <- name + ":" + msg.Content
			return nil
		}
		ch.stopFn = func(context.Context) error {
			ch.stopped.Store(true)
			return nil
		}
		f.created <- ch
		return ch, nil
	})
	t.Cleanup(func() {
		factoriesMu.Lock()
		delete(factories, reloadTestType)
		factoriesMu.Unlock()
	})
	return f
}

func reloadTestConfig(settings map[string]string) *config.Config {
	cfg := &config.Config{Channels: config.ChannelsConfig{}}
	for name, s := range settings {
		cfg.Channels[name] = &config.Channel{
			Enabled:  true,
			Type:     reloadTestType,
			Settings: config.RawNode(s),
		}
	}
	return cfg
}

func (f *reloadTestFactory) next(t *testing.T) *reloadTestChannel {
	t.Helper()
	select {
	case ch := <-f.created:
		return ch
	default:
		t.Fatal("no channel was created")
		return nil
	}
}

func (f *reloadTestFactory) expectDelivered(t *testing.T, want string) {
	t.Helper()
	select {
	case got := <-f.sent:
		if got != want {
			t.Fatalf("delivered %q, want %q", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("%q was not delivered", want)
	}
}

func publishOutbound(t *testing.T, m *Manager, channel, content string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := m.bus.PublishOutbound(ctx, testOutboundMessage(bus.OutboundMessage{
		Channel: channel,
		ChatID:  "chat",
		Content: content,
	}))
	if err != nil {
		t.Fatalf("PublishOutbound() error = %v", err)
	}
}

func startReloadTestManager(t *testing.T, cfg *config.Config) *Manager {
	t.Helper()
	m, err := NewManager(cfg, bus.NewMessageBus(), nil)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	if err := m.StartAll(context.Background()); err != nil {
		t.Fatalf("StartAll() error = %v", err)
	}
	t.Cleanup(func() {
		if err := m.StopAll(context.Background()); err != nil {
			t.Errorf("StopAll() error = %v", err)
		}
	})
	return m
}

func TestReloadChangedChannelKeepsWorker(t *testing.T) {
	f := registerReloadTestFactory(t)
	m := startReloadTestManager(t, reloadTestConfig(map[string]string{"rt": `{"v":1}`}))
	old := f.next(t)

	if err := m.Reload(context.Background(), reloadTestConfig(map[string]string{"rt": `{"v":2}`})); err != nil {
		t.Fatalf("Reload() error = %v", err)
	}
	fresh := f.next(t)
	if !old.stopped.Load() {
		t.Fatal("the replaced channel was not stopped")
	}
	if got, _ := m.GetChannel("rt"); got != fresh {
		t.Fatal("the changed channel was not replaced")
	}
	m.mu.RLock()
	w := m.workers["rt"]
	m.mu.RUnlock()
	if w == nil || w.ch != fresh {
		t.Fatal("the changed channel has no worker of its own")
	}

	publishOutbound(t, m, "rt", "after reload")
	f.expectDelivered(t, "rt:after reload")
}

func TestReloadAddsAndRemovesChannels(t *testing.T) {
	f := registerReloadTestFactory(t)
	m := startReloadTestManager(t, reloadTestConfig(map[string]string{"a": `{}`}))
	a := f.next(t)

	if err := m.Reload(context.Background(), reloadTestConfig(map[string]string{"b": `{}`})); err != nil {
		t.Fatalf("Reload() error = %v", err)
	}
	f.next(t)
	if !a.stopped.Load() {
		t.Fatal("the removed channel was not stopped")
	}
	if _, ok := m.GetChannel("a"); ok {
		t.Fatal("the removed channel is still registered")
	}
	m.mu.RLock()
	_, aWorker := m.workers["a"]
	m.mu.RUnlock()
	if aWorker {
		t.Fatal("the removed channel kept its worker")
	}

	publishOutbound(t, m, "b", "hello")
	f.expectDelivered(t, "b:hello")
	if err := m.SendMessage(context.Background(), testOutboundMessage(bus.OutboundMessage{
		Channel: "a", ChatID: "chat", Content: "gone",
	})); err == nil {
		t.Fatal("SendMessage to a removed channel succeeded")
	}
}

func TestReloadSkipsChannelsThatFailedToInitialize(t *testing.T) {
	f := registerReloadTestFactory(t)
	m := startReloadTestManager(t, reloadTestConfig(map[string]string{
		"ok":  `{}`,
		"bad": `{"fail":1}`,
	}))
	f.next(t)

	// "bad" is enabled but never initialized: changing it must not
	// dereference a missing channel, and a fixed config starts it.
	if err := m.Reload(context.Background(), reloadTestConfig(map[string]string{
		"ok":  `{}`,
		"bad": `{"v":2}`,
	})); err != nil {
		t.Fatalf("Reload() error = %v", err)
	}
	f.next(t)
	publishOutbound(t, m, "bad", "fixed")
	f.expectDelivered(t, "bad:fixed")

	// Breaking it again stops it and leaves nothing behind to start.
	if err := m.Reload(context.Background(), reloadTestConfig(map[string]string{
		"ok":  `{}`,
		"bad": `{"fail":2}`,
	})); err != nil {
		t.Fatalf("Reload() error = %v", err)
	}
	if _, ok := m.GetChannel("bad"); ok {
		t.Fatal("a channel that failed to initialize is registered")
	}
	publishOutbound(t, m, "ok", "still here")
	f.expectDelivered(t, "ok:still here")
}

func TestStopAllAfterReloadThenSend(t *testing.T) {
	f := registerReloadTestFactory(t)
	m, err := NewManager(reloadTestConfig(map[string]string{"rt": `{"v":1}`}), bus.NewMessageBus(), nil)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	if err := m.StartAll(context.Background()); err != nil {
		t.Fatalf("StartAll() error = %v", err)
	}
	if err := m.Reload(context.Background(), reloadTestConfig(map[string]string{"rt": `{"v":2}`})); err != nil {
		t.Fatalf("Reload() error = %v", err)
	}
	f.next(t)
	f.next(t)
	if err := m.StopAll(context.Background()); err != nil {
		t.Fatalf("StopAll() error = %v", err)
	}

	// No dispatcher survives StopAll: a published message stays on the bus,
	// instead of being sent to a stopped worker.
	publishOutbound(t, m, "rt", "after stop")
	time.Sleep(100 * time.Millisecond)
	select {
	case msg := <-m.bus.OutboundChan():
		if msg.Content != "after stop" {
			t.Fatalf("unexpected message on the bus: %q", msg.Content)
		}
	default:
		t.Fatal("a dispatcher was still running after StopAll")
	}

	msg := testOutboundMessage(bus.OutboundMessage{Channel: "rt", ChatID: "chat", Content: "direct"})
	if err := m.SendMessage(context.Background(), msg); err == nil {
		t.Fatal("SendMessage after StopAll succeeded")
	}
	if err := m.SendToChannel(context.Background(), "rt", "chat", "queued"); err == nil {
		t.Fatal("SendToChannel after StopAll succeeded")
	}
	select {
	case got := <-f.sent:
		t.Fatalf("delivered %q after StopAll", got)
	default:
	}
}

func TestSendMessageReturnsDeliveryError(t *testing.T) {
	f := registerReloadTestFactory(t)
	m := startReloadTestManager(t, reloadTestConfig(map[string]string{
		"good": `{}`,
		"bad":  `{"refuse":true}`,
	}))
	f.next(t)
	f.next(t)

	msg := testOutboundMessage(bus.OutboundMessage{Channel: "bad", ChatID: "chat", Content: "hi"})
	if err := m.SendMessage(context.Background(), msg); !errors.Is(err, ErrSendFailed) {
		t.Fatalf("SendMessage() error = %v, want ErrSendFailed", err)
	}
	msg = testOutboundMessage(bus.OutboundMessage{Channel: "good", ChatID: "chat", Content: "hi"})
	if err := m.SendMessage(context.Background(), msg); err != nil {
		t.Fatalf("SendMessage() error = %v", err)
	}
	f.expectDelivered(t, "good:hi")
}

func TestManagerAppliesChannelConfig(t *testing.T) {
	f := registerReloadTestFactory(t)
	cfg := reloadTestConfig(map[string]string{"my_rt": `{}`})
	cfg.Channels["my_rt"].AllowFrom = config.FlexibleStringSlice{"rt:owner"}
	if _, err := NewManager(cfg, bus.NewMessageBus(), nil); err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	ch := f.next(t)

	if got := ch.Name(); got != "my_rt" {
		t.Fatalf("Name() = %q, want the configured name", got)
	}
	stranger := bus.SenderInfo{Platform: "rt", PlatformID: "x", CanonicalID: "rt:x"}
	if ch.Admits("direct", stranger, "x") {
		t.Fatal("a stranger was admitted")
	}
	owner := bus.SenderInfo{Platform: "rt", PlatformID: "owner", CanonicalID: "rt:owner"}
	if !ch.Admits("direct", owner, "owner") {
		t.Fatal("allow_from was not applied")
	}
	if ch.Admits("group", owner, "g") {
		t.Fatal("a group message was admitted")
	}
}
