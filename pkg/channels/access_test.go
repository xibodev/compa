package channels

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xibodev/compa/v4/pkg/bus"
	"github.com/xibodev/compa/v4/pkg/config"
	"github.com/xibodev/compa/v4/pkg/logger"
	"github.com/xibodev/compa/v4/pkg/pairing"
)

// stubPairing replaces recordPairing for the test and returns the requests
// it receives.
func stubPairing(t *testing.T) func() []pairing.Request {
	t.Helper()
	var mu sync.Mutex
	var recorded []pairing.Request
	old := recordPairing
	recordPairing = func(r pairing.Request) error {
		mu.Lock()
		defer mu.Unlock()
		recorded = append(recorded, r)
		return nil
	}
	t.Cleanup(func() { recordPairing = old })
	return func() []pairing.Request {
		mu.Lock()
		defer mu.Unlock()
		return append([]pairing.Request(nil), recorded...)
	}
}

func telegramSender(id, username string) bus.SenderInfo {
	return bus.SenderInfo{
		Platform:    "telegram",
		PlatformID:  id,
		CanonicalID: "telegram:" + id,
		Username:    username,
		DisplayName: "Name " + id,
	}
}

// deliver passes one message through HandleMessageWithContext and returns
// the published message, if any.
func deliver(
	t *testing.T,
	ch *BaseChannel,
	msgBus *bus.MessageBus,
	chatType, chatID string,
	sender bus.SenderInfo,
) (bus.InboundMessage, bool) {
	t.Helper()
	err := ch.HandleMessageWithContext(context.Background(), chatID, "hi", nil, bus.InboundContext{
		ChatID:   chatID,
		ChatType: chatType,
		SenderID: sender.PlatformID,
	}, sender)
	if err != nil {
		t.Fatalf("HandleMessageWithContext() error = %v", err)
	}
	select {
	case msg := <-msgBus.InboundChan():
		return msg, true
	default:
		return bus.InboundMessage{}, false
	}
}

// A channel answers only its owner, the accounts allow_from lists, and only
// in direct messages. While allow_from lists no account, a stranger's direct
// message is recorded for pairing; once it lists one, strangers are ignored.
// "*" and group IDs admit no one.
func TestOwnerOnlyAccess(t *testing.T) {
	owner := telegramSender("111", "alice")
	stranger := telegramSender("999", "mallory")
	const group = "-100500"

	tests := []struct {
		name      string
		allowFrom []string
		chatType  string
		chatID    string
		sender    bus.SenderInfo
		admitted  bool
		paired    bool
	}{
		{"owner, direct", []string{"telegram:111"}, "direct", "111", owner, true, false},
		{"owner, no chat type", []string{"telegram:111"}, "", "111", owner, true, false},
		{"owner listed by bare ID", []string{"111"}, "direct", "111", owner, true, false},
		{"owner listed by username", []string{"@alice"}, "direct", "111", owner, true, false},
		{"stranger, owner bound", []string{"telegram:111"}, "direct", "999", stranger, false, false},
		{"stranger, no owner yet", nil, "direct", "999", stranger, false, true},
		{"stranger, only \"*\"", []string{"*"}, "direct", "999", stranger, false, true},
		{"stranger, \"*\" and the owner", []string{"*", "telegram:111"}, "direct", "999", stranger, false, false},
		{"owner in a group", []string{"telegram:111"}, "group", group, owner, false, false},
		{"owner in a channel chat", []string{"telegram:111"}, "channel", group, owner, false, false},
		{"stranger in a listed group", []string{"telegram:111", "telegram:" + group}, "group", group, stranger, false, false},
		{"stranger in a group, no owner yet", nil, "group", group, stranger, false, false},
		{"stranger in a group, \"*\"", []string{"*"}, "Group", group, stranger, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorded := stubPairing(t)
			msgBus := bus.NewMessageBus()
			ch := NewBaseChannel("my_telegram", nil, msgBus, tt.allowFrom)
			ch.RequireOwner()

			if got := ch.Admits(tt.chatType, tt.sender, tt.chatID); got != tt.admitted {
				t.Fatalf("Admits() = %v, want %v", got, tt.admitted)
			}
			if len(recorded()) != 0 {
				t.Fatal("Admits recorded a pairing request")
			}

			msg, published := deliver(t, ch, msgBus, tt.chatType, tt.chatID, tt.sender)
			if published != tt.admitted {
				t.Fatalf("published = %v, want %v", published, tt.admitted)
			}
			if published && !msg.Context.SenderIsOwner {
				t.Fatal("an admitted sender is not the owner")
			}
			got := recorded()
			if tt.paired != (len(got) == 1) {
				t.Fatalf("pairing requests = %+v, want paired=%v", got, tt.paired)
			}
			if tt.paired {
				want := pairing.Request{
					Channel:     "my_telegram",
					SenderID:    "telegram:999",
					PlatformID:  "999",
					DisplayName: "Name 999",
				}
				if got[0] != want {
					t.Fatalf("pairing request = %+v, want %+v", got[0], want)
				}
			}
			// The early platform check never rejects what the check admits
			// or records.
			if (tt.admitted || tt.paired) && !ch.IsAllowedSender(tt.sender) {
				t.Fatal("IsAllowedSender rejected an admitted sender")
			}
		})
	}
}

func TestPairingRecordsOncePerInterval(t *testing.T) {
	recorded := stubPairing(t)
	msgBus := bus.NewMessageBus()
	ch := NewBaseChannel("telegram", nil, msgBus, nil)
	ch.RequireOwner()

	for range 3 {
		if _, ok := deliver(t, ch, msgBus, "direct", "999", telegramSender("999", "")); ok {
			t.Fatal("an unpaired sender's message was published")
		}
	}
	if got := len(recorded()); got != 1 {
		t.Fatalf("recorded %d pairing requests, want 1", got)
	}
}

func TestPairingWritesStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	msgBus := bus.NewMessageBus()
	ch := NewBaseChannel("telegram", nil, msgBus, nil)
	ch.RequireOwner()

	if _, ok := deliver(t, ch, msgBus, "direct", "42", telegramSender("42", "bob")); ok {
		t.Fatal("an unpaired sender's message was published")
	}
	requests, err := pairing.List(home, "telegram")
	if err != nil {
		t.Fatalf("pairing.List() error = %v", err)
	}
	if len(requests) != 1 || requests[0].SenderID != "telegram:42" {
		t.Fatalf("pairing requests = %+v, want telegram:42", requests)
	}
}

// A channel nothing configured, as in tests, admits every direct message
// while allow_from lists no account, but no group message.
func TestAccessWithoutRequireOwner(t *testing.T) {
	recorded := stubPairing(t)
	stranger := telegramSender("999", "")

	msgBus := bus.NewMessageBus()
	open := NewBaseChannel("telegram", nil, msgBus, nil)
	msg, ok := deliver(t, open, msgBus, "direct", "999", stranger)
	if !ok {
		t.Fatal("empty allow_from without RequireOwner no longer admits a direct message")
	}
	if msg.Context.SenderIsOwner {
		t.Fatal("an unconfigured channel made a stranger the owner")
	}
	if _, ok := deliver(t, open, msgBus, "group", "-100500", stranger); ok {
		t.Fatal("an unconfigured channel admitted a group message")
	}

	listed := NewBaseChannel("telegram", nil, msgBus, []string{"telegram:111"})
	if _, ok := deliver(t, listed, msgBus, "direct", "999", stranger); ok {
		t.Fatal("allow_from without RequireOwner admitted a stranger")
	}
	if msg, ok := deliver(t, listed, msgBus, "direct", "111", telegramSender("111", "")); !ok ||
		!msg.Context.SenderIsOwner {
		t.Fatalf("listed sender: published=%v owner=%v, want both", ok, msg.Context.SenderIsOwner)
	}
	if len(recorded()) != 0 {
		t.Fatal("a channel without RequireOwner recorded a pairing request")
	}
}

func TestAccessBypasses(t *testing.T) {
	stubPairing(t)
	stranger := telegramSender("999", "")

	// The dashboard's web chat: only authenticated users reach it.
	msgBus := bus.NewMessageBus()
	web := NewBaseChannel("web", nil, msgBus, []string{"someone"}, WithAuthenticatedAccess())
	web.RequireOwner()
	msg, ok := deliver(t, web, msgBus, "direct", "chat", stranger)
	if !ok || !msg.Context.SenderIsOwner {
		t.Fatalf("web chat: published=%v owner=%v, want both", ok, msg.Context.SenderIsOwner)
	}

	// Internal channels.
	cli := NewBaseChannel("cli", nil, msgBus, []string{"someone"})
	cli.RequireOwner()
	if _, ok := deliver(t, cli, msgBus, "direct", "chat", stranger); !ok {
		t.Fatal("internal channel message was dropped")
	}

	// A platform that vouched for the owner (WhatsApp's own chat).
	vouched := NewBaseChannel("whatsapp_native", nil, msgBus, nil)
	vouched.RequireOwner()
	err := vouched.HandleMessageWithContext(context.Background(), "self", "hi", nil, bus.InboundContext{
		ChatID:        "self",
		ChatType:      "direct",
		SenderID:      "self",
		SenderIsOwner: true,
	}, bus.SenderInfo{Platform: "whatsapp", PlatformID: "self"})
	if err != nil {
		t.Fatalf("HandleMessageWithContext() error = %v", err)
	}
	select {
	case msg := <-msgBus.InboundChan():
		if !msg.Context.SenderIsOwner {
			t.Fatal("the platform's owner flag was cleared")
		}
	case <-time.After(time.Second):
		t.Fatal("a message the platform vouched for was dropped")
	}
}

// The platforms' early checks pass the owner, and strangers only while
// their direct messages are recorded for pairing.
func TestEarlyCheckWithAndWithoutAnOwner(t *testing.T) {
	stranger := telegramSender("999", "")
	for _, tt := range []struct {
		allowFrom []string
		want      bool
	}{
		{nil, true},
		{[]string{"*"}, true},
		{[]string{"telegram:111"}, false},
		{[]string{"*", "telegram:111"}, false},
	} {
		ch := NewBaseChannel("telegram", nil, nil, tt.allowFrom)
		ch.RequireOwner()
		if got := ch.IsAllowedSender(stranger); got != tt.want {
			t.Errorf("allow_from %v: IsAllowedSender(stranger) = %v, want %v", tt.allowFrom, got, tt.want)
		}
		if got := ch.IsAllowed("999"); got != tt.want {
			t.Errorf("allow_from %v: IsAllowed(stranger) = %v, want %v", tt.allowFrom, got, tt.want)
		}
	}
	ch := NewBaseChannel("telegram", nil, nil, []string{"telegram:111"})
	ch.RequireOwner()
	if !ch.IsAllowedSender(telegramSender("111", "")) {
		t.Error("the early check rejected the owner")
	}
	bare := NewBaseChannel("telegram", nil, nil, []string{"111"})
	bare.RequireOwner()
	if !bare.IsAllowed("111") {
		t.Error("the early check rejected the owner's raw ID")
	}
}

// "*" in allow_from is reported once the channel is configured: it admits
// no one any more.
func TestRequireOwnerWarnsAboutWildcard(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "access.log")
	prevLevel := logger.GetLevel()
	logger.SetLevel(logger.WARN)
	if err := logger.EnableFileLogging(logFile); err != nil {
		t.Fatalf("EnableFileLogging() error = %v", err)
	}
	t.Cleanup(func() {
		logger.DisableFileLogging()
		logger.SetLevel(prevLevel)
	})

	NewBaseChannel("quiet", nil, nil, []string{"telegram:111"}).RequireOwner()
	NewBaseChannel("starred", nil, nil, []string{"*"}).RequireOwner()
	logger.DisableFileLogging()

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	if text := string(data); strings.Count(text, "no longer admits anyone") != 1 ||
		!strings.Contains(text, `"channel":"starred"`) {
		t.Fatalf("log = %s, want one warning, for the channel with \"*\"", text)
	}
}

// The manager makes every chat channel owner-only, the web client included;
// only the dashboard's web chat, which bypasses the check, is left alone.
func TestApplyChannelConfigRequiresOwner(t *testing.T) {
	m := &Manager{config: &config.Config{Channels: config.ChannelsConfig{
		"remote": {Type: config.ChannelWebClient, Enabled: true},
		"web":    {Type: config.ChannelWeb, Enabled: true},
	}}}

	client := &mockChannel{}
	m.applyChannelConfig("remote", config.ChannelWebClient, client)
	if !client.ownerOnly.Load() {
		t.Fatal("the web client is not owner-only")
	}

	web := &mockChannel{}
	m.applyChannelConfig("web", config.ChannelWeb, web)
	if web.ownerOnly.Load() {
		t.Fatal("the web chat was made owner-only")
	}
}
