package channels

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/xibodev/compa/v2/pkg/bus"
	"github.com/xibodev/compa/v2/pkg/config"
	"github.com/xibodev/compa/v2/pkg/pairing"
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

func TestAccessPolicyMatrix(t *testing.T) {
	owner := telegramSender("111", "alice")
	stranger := telegramSender("999", "mallory")
	const (
		listedGroup   = "-100500"
		unlistedGroup = "-100600"
	)
	allowFrom := []string{"telegram:111", "telegram:" + listedGroup}

	tests := []struct {
		name      string
		dm, group string
		chatType  string
		chatID    string
		sender    bus.SenderInfo
		admitted  bool
		owner     bool
		paired    bool
	}{
		// Direct messages.
		{"dm open owner", "open", "allowlist", "direct", "111", owner, true, true, false},
		{"dm open stranger", "open", "allowlist", "direct", "999", stranger, true, false, false},
		{"dm allowlist owner", "allowlist", "allowlist", "direct", "111", owner, true, true, false},
		{"dm allowlist stranger", "allowlist", "allowlist", "direct", "999", stranger, false, false, false},
		{"dm pairing owner", "pairing", "allowlist", "direct", "111", owner, true, true, false},
		{"dm pairing stranger", "pairing", "allowlist", "direct", "999", stranger, false, false, true},
		{"dm disabled owner", "disabled", "allowlist", "", "111", owner, false, false, false},
		{"dm disabled stranger", "disabled", "allowlist", "", "999", stranger, false, false, false},
		// Group messages.
		{"group open owner", "pairing", "open", "group", unlistedGroup, owner, true, true, false},
		{"group open stranger", "pairing", "open", "group", unlistedGroup, stranger, true, false, false},
		{"group allowlist owner", "pairing", "allowlist", "group", unlistedGroup, owner, true, true, false},
		{"group allowlist stranger in listed group", "pairing", "allowlist", "group", listedGroup, stranger, true, false, false},
		{"group allowlist stranger elsewhere", "pairing", "allowlist", "group", unlistedGroup, stranger, false, false, false},
		{"channel chat is a group", "pairing", "allowlist", "channel", unlistedGroup, stranger, false, false, false},
		{"group disabled owner", "open", "disabled", "group", listedGroup, owner, false, false, false},
		{"group disabled stranger", "open", "disabled", "group", listedGroup, stranger, false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorded := stubPairing(t)
			msgBus := bus.NewMessageBus()
			ch := NewBaseChannel("my_telegram", nil, msgBus, allowFrom)
			ch.SetAccessPolicy(tt.dm, tt.group)

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
			if published && msg.Context.SenderIsOwner != tt.owner {
				t.Fatalf("SenderIsOwner = %v, want %v", msg.Context.SenderIsOwner, tt.owner)
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
			// The early platform check never rejects what the policy admits
			// or records.
			if (tt.admitted || tt.paired) && !ch.IsAllowedSender(tt.sender) {
				t.Fatal("IsAllowedSender rejected an admitted sender")
			}
		})
	}
}

func TestAccessPolicyWildcardIsNotOwner(t *testing.T) {
	stubPairing(t)
	msgBus := bus.NewMessageBus()
	ch := NewBaseChannel("telegram", nil, msgBus, []string{"*"})
	ch.SetAccessPolicy(config.DMPolicyAllowlist, config.GroupPolicyAllowlist)

	for _, chatType := range []string{"direct", "group"} {
		msg, ok := deliver(t, ch, msgBus, chatType, "999", telegramSender("999", ""))
		if !ok {
			t.Fatalf("%s: \"*\" did not admit the sender", chatType)
		}
		if msg.Context.SenderIsOwner {
			t.Fatalf("%s: \"*\" made the sender the owner", chatType)
		}
	}
}

func TestAccessPolicyPairingRecordsOncePerInterval(t *testing.T) {
	recorded := stubPairing(t)
	msgBus := bus.NewMessageBus()
	ch := NewBaseChannel("telegram", nil, msgBus, nil)
	ch.SetAccessPolicy(config.DMPolicyPairing, config.GroupPolicyAllowlist)

	for range 3 {
		if _, ok := deliver(t, ch, msgBus, "direct", "999", telegramSender("999", "")); ok {
			t.Fatal("an unpaired sender's message was published")
		}
	}
	if got := len(recorded()); got != 1 {
		t.Fatalf("recorded %d pairing requests, want 1", got)
	}
}

func TestAccessPolicyPairingWritesStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	msgBus := bus.NewMessageBus()
	ch := NewBaseChannel("telegram", nil, msgBus, nil)
	ch.SetAccessPolicy(config.DMPolicyPairing, config.GroupPolicyAllowlist)

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

func TestAccessPolicyWithoutPolicyFollowsAllowFrom(t *testing.T) {
	recorded := stubPairing(t)
	stranger := telegramSender("999", "")

	msgBus := bus.NewMessageBus()
	open := NewBaseChannel("telegram", nil, msgBus, nil)
	for _, chatType := range []string{"direct", "group"} {
		msg, ok := deliver(t, open, msgBus, chatType, "999", stranger)
		if !ok {
			t.Fatalf("%s: empty allow_from without a policy no longer admits everyone", chatType)
		}
		if msg.Context.SenderIsOwner {
			t.Fatalf("%s: an open channel made a stranger the owner", chatType)
		}
	}

	listed := NewBaseChannel("telegram", nil, msgBus, []string{"telegram:111"})
	if _, ok := deliver(t, listed, msgBus, "direct", "999", stranger); ok {
		t.Fatal("allow_from without a policy admitted a stranger")
	}
	if msg, ok := deliver(t, listed, msgBus, "direct", "111", telegramSender("111", "")); !ok ||
		!msg.Context.SenderIsOwner {
		t.Fatalf("listed sender: published=%v owner=%v, want both", ok, msg.Context.SenderIsOwner)
	}
	if len(recorded()) != 0 {
		t.Fatal("a channel without a pairing policy recorded a pairing request")
	}
}

func TestAccessPolicyBypasses(t *testing.T) {
	stubPairing(t)
	stranger := telegramSender("999", "")

	// The dashboard's web chat: only authenticated users reach it.
	msgBus := bus.NewMessageBus()
	web := NewBaseChannel("web", nil, msgBus, []string{"someone"}, WithAuthenticatedAccess())
	web.SetAccessPolicy(config.DMPolicyDisabled, config.GroupPolicyDisabled)
	msg, ok := deliver(t, web, msgBus, "direct", "chat", stranger)
	if !ok || !msg.Context.SenderIsOwner {
		t.Fatalf("web chat: published=%v owner=%v, want both", ok, msg.Context.SenderIsOwner)
	}

	// Internal channels.
	cli := NewBaseChannel("cli", nil, msgBus, []string{"someone"})
	cli.SetAccessPolicy(config.DMPolicyDisabled, config.GroupPolicyDisabled)
	if _, ok := deliver(t, cli, msgBus, "direct", "chat", stranger); !ok {
		t.Fatal("internal channel message was dropped")
	}

	// A platform that vouched for the owner (WhatsApp's own chat).
	vouched := NewBaseChannel("whatsapp_native", nil, msgBus, nil)
	vouched.SetAccessPolicy(config.DMPolicyPairing, config.GroupPolicyAllowlist)
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

func TestIsAllowedSenderFollowsPolicy(t *testing.T) {
	stranger := telegramSender("999", "")
	tests := []struct {
		dm, group string
		want      bool
	}{
		{config.DMPolicyPairing, config.GroupPolicyAllowlist, true},
		{config.DMPolicyOpen, config.GroupPolicyDisabled, true},
		{config.DMPolicyDisabled, config.GroupPolicyOpen, true},
		{config.DMPolicyAllowlist, config.GroupPolicyAllowlist, false},
		{config.DMPolicyDisabled, config.GroupPolicyDisabled, false},
	}
	for _, tt := range tests {
		ch := NewBaseChannel("telegram", nil, nil, []string{"telegram:111"})
		ch.SetAccessPolicy(tt.dm, tt.group)
		if got := ch.IsAllowedSender(stranger); got != tt.want {
			t.Errorf("%s/%s: IsAllowedSender(stranger) = %v, want %v", tt.dm, tt.group, got, tt.want)
		}
		if got := ch.IsAllowed("999"); got != tt.want {
			t.Errorf("%s/%s: IsAllowed(stranger) = %v, want %v", tt.dm, tt.group, got, tt.want)
		}
		if !ch.IsAllowedSender(telegramSender("111", "")) {
			t.Errorf("%s/%s: IsAllowedSender rejected a listed sender", tt.dm, tt.group)
		}
	}
}

func TestOpenToEveryoneFollowsPolicy(t *testing.T) {
	ch := NewBaseChannel("telegram", nil, nil, nil)
	if !ch.OpenToEveryone() {
		t.Fatal("empty allow_from without a policy should be open to everyone")
	}
	ch.SetAccessPolicy(config.DMPolicyPairing, config.GroupPolicyAllowlist)
	if ch.OpenToEveryone() {
		t.Fatal("a pairing channel is not open to everyone")
	}
	ch.SetAccessPolicy(config.DMPolicyOpen, config.GroupPolicyAllowlist)
	if !ch.OpenToEveryone() {
		t.Fatal("an open dm policy without \"*\" is open to everyone")
	}
}

func TestChatListedMatchesGroupIDForms(t *testing.T) {
	tests := []struct {
		name      string
		platform  string
		allowFrom string
		chatID    string
		want      bool
	}{
		{"kind-prefixed chat, raw entry", "onebot", "30003", "group:30003", true},
		{"kind-prefixed chat, canonical raw entry", "onebot", "onebot:30003", "group:30003", true},
		{"kind-prefixed chat, kind-prefixed entry", "onebot", "group:30003", "group:30003", true},
		{"kind-prefixed chat, canonical kind-prefixed entry", "onebot", "onebot:group:30003", "group:30003", true},
		{"raw chat, kind-prefixed entry", "telegram", "group:-100500", "-100500", true},
		{"raw chat, canonical kind-prefixed entry", "telegram", "telegram:group:-100500", "-100500", true},
		{"other group", "onebot", "30003", "group:30004", false},
		{"other platform's entry", "onebot", "telegram:30003", "group:30003", false},
		{"irc channel ignores case", "irc", "#Compa", "#compa", true},
		{"irc canonical entry ignores case", "irc", "irc:#compa", "#COMPA", true},
		{"irc other channel", "irc", "#compa", "#other", false},
		{"other platforms keep case", "slack", "c123", "C123", false},
		{"matrix room id", "matrix", "!room:example.org", "!room:example.org", true},
		{"telegram forum topic, group entry", "telegram", "-100500", "-100500/42", true},
		{"telegram forum topic, canonical group entry", "telegram", "telegram:-100500", "-100500/42", true},
		{"telegram forum topic of another group", "telegram", "-100500", "-100600/42", false},
		{"slack thread, channel entry", "slack", "C123", "C123/1700000000.000100", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch := NewBaseChannel(tt.platform, nil, nil, []string{tt.allowFrom})
			ch.SetAccessPolicy(config.DMPolicyPairing, config.GroupPolicyAllowlist)
			stranger := bus.SenderInfo{
				Platform:    tt.platform,
				PlatformID:  "stranger",
				CanonicalID: tt.platform + ":stranger",
			}
			if got := ch.Admits("group", stranger, tt.chatID); got != tt.want {
				t.Fatalf("Admits(group %q) with allow_from %q = %v, want %v", tt.chatID, tt.allowFrom, got, tt.want)
			}
		})
	}
}

// The manager gives every chat channel its access policies, the web client
// included; only the dashboard's web chat has none.
func TestApplyChannelConfigSetsAccessPolicies(t *testing.T) {
	m := &Manager{config: &config.Config{Channels: config.ChannelsConfig{
		"remote": {Type: config.ChannelWebClient, Enabled: true},
		"web":    {Type: config.ChannelWeb, Enabled: true},
	}}}

	client := &mockChannel{}
	m.applyChannelConfig("remote", config.ChannelWebClient, client)
	if dm, group := client.policies(); dm != config.DMPolicyPairing || group != config.GroupPolicyAllowlist {
		t.Fatalf("web client policies = %s/%s, want pairing/allowlist", dm, group)
	}
	if client.OpenToEveryone() {
		t.Fatal("a web client without allow_from is open to everyone")
	}

	web := &mockChannel{}
	m.applyChannelConfig("web", config.ChannelWeb, web)
	if web.policy.Load() != nil {
		t.Fatal("the web chat was given an access policy")
	}
}
