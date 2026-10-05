package discord

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/xibodev/compa/pkg/bus"
	"github.com/xibodev/compa/pkg/channels"
	"github.com/xibodev/compa/pkg/config"
	"github.com/xibodev/compa/pkg/pairing"
)

func newAccessTestChannel(t *testing.T, allowFrom ...string) (*DiscordChannel, *discordgo.Session, *bus.MessageBus, *int) {
	t.Helper()
	session, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatalf("discordgo.New() error: %v", err)
	}
	session.State.User = &discordgo.User{ID: "bot"}

	messageBus := bus.NewMessageBus()
	ch := &DiscordChannel{
		BaseChannel: channels.NewBaseChannel("discord", nil, messageBus, allowFrom),
		session:     session,
		ctx:         context.Background(),
		typingStop:  make(map[string]chan struct{}),
		voiceSSRC:   make(map[string]map[uint32]string),
		botUserID:   "bot",
	}
	ch.SetAccessPolicy(config.DMPolicyPairing, config.GroupPolicyAllowlist)

	downloads := 0
	saved := fetchAttachment
	fetchAttachment = func(*DiscordChannel, string, string) string {
		downloads++
		return ""
	}
	t.Cleanup(func() { fetchAttachment = saved })
	return ch, session, messageBus, &downloads
}

func receiveInbound(messageBus *bus.MessageBus, wait time.Duration) (bus.InboundMessage, bool) {
	select {
	case msg := <-messageBus.InboundChan():
		return msg, true
	case <-time.After(wait):
		return bus.InboundMessage{}, false
	}
}

func dmMessage(id, authorID, text string) *discordgo.MessageCreate {
	return &discordgo.MessageCreate{Message: &discordgo.Message{
		ID:          id,
		ChannelID:   "dm-" + authorID,
		Content:     text,
		Author:      &discordgo.User{ID: authorID, Username: "user" + authorID},
		Attachments: []*discordgo.MessageAttachment{{URL: "https://cdn.example.com/a.png", Filename: "a.png"}},
	}}
}

func TestHandleMessage_UnpairedSenderMediaIsNotFetched(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	ch, session, messageBus, downloads := newAccessTestChannel(t, "111")

	ch.handleMessage(session, dmMessage("m1", "999", "hello"))

	if *downloads != 0 {
		t.Fatalf("downloaded %d attachments for an unpaired sender", *downloads)
	}
	if msg, ok := receiveInbound(messageBus, 50*time.Millisecond); ok {
		t.Fatalf("an unpaired sender's message was published: %#v", msg)
	}
	requests, err := pairing.List(home, "discord")
	if err != nil || len(requests) != 1 || requests[0].SenderID != "discord:999" {
		t.Fatalf("pairing requests = %+v, %v; want the stranger", requests, err)
	}
}

func TestHandleMessage_OwnerIsPublishedAsOwner(t *testing.T) {
	ch, session, messageBus, downloads := newAccessTestChannel(t, "111")

	ch.handleMessage(session, dmMessage("m2", "111", "hello"))

	msg, ok := receiveInbound(messageBus, time.Second)
	if !ok {
		t.Fatal("the owner's message was not published")
	}
	if !msg.Context.SenderIsOwner || msg.Context.ChatType != "direct" || *downloads != 1 {
		t.Fatalf("owner %v, chat type %q, downloads %d", msg.Context.SenderIsOwner, msg.Context.ChatType, *downloads)
	}
}

func TestHandleMessage_AllowListedChannelMemberIsAdmitted(t *testing.T) {
	ch, session, messageBus, _ := newAccessTestChannel(t, "111", "chan-1")

	ch.handleMessage(session, &discordgo.MessageCreate{Message: &discordgo.Message{
		ID:        "m3",
		GuildID:   "g1",
		ChannelID: "chan-1",
		Content:   "status?",
		Author:    &discordgo.User{ID: "555", Username: "member"},
	}})

	msg, ok := receiveInbound(messageBus, time.Second)
	if !ok {
		t.Fatal("a member of an allow-listed channel was not admitted")
	}
	if msg.Content != "status?" || msg.Context.SenderIsOwner {
		t.Fatalf("published %q, owner %v", msg.Content, msg.Context.SenderIsOwner)
	}
}

func TestHandleMessage_IgnoresOtherBots(t *testing.T) {
	ch, session, messageBus, _ := newAccessTestChannel(t, "*")

	m := dmMessage("m4", "777", "hello")
	m.Author.Bot = true
	ch.handleMessage(session, m)

	if msg, ok := receiveInbound(messageBus, 50*time.Millisecond); ok {
		t.Fatalf("another bot's message was published: %#v", msg)
	}
}

func TestHandleMessage_RecoversFromPanic(t *testing.T) {
	ch, session, _, _ := newAccessTestChannel(t, "111")
	fetchAttachment = func(*DiscordChannel, string, string) string { panic("malformed event") }

	// discordgo runs handlers on their own goroutines; a panic must not escape.
	ch.handleMessage(session, dmMessage("m5", "111", "hello"))
}

func TestSend_AllowsNoMentions(t *testing.T) {
	var (
		mu     sync.Mutex
		bodies []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(body))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"sent-1"}`)
	}))
	defer server.Close()

	origChannels := discordgo.EndpointChannels
	discordgo.EndpointChannels = server.URL + "/channels/"
	defer func() { discordgo.EndpointChannels = origChannels }()

	ch, session, _, _ := newAccessTestChannel(t, "111")
	session.Client = server.Client()
	ch.progress = channels.NewToolFeedbackAnimator(ch.EditMessage)
	ch.SetRunning(true)

	if _, err := ch.Send(context.Background(), bus.OutboundMessage{
		ChatID:  "chat-1",
		Content: "@everyone <@&123> look",
		Context: bus.InboundContext{Channel: "discord", ChatID: "chat-1"},
	}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if err := ch.EditMessage(context.Background(), "chat-1", "sent-1", "@here"); err != nil {
		t.Fatalf("EditMessage() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("requests = %d, want 2", len(bodies))
	}
	for _, body := range bodies {
		if !strings.Contains(body, `"allowed_mentions":{"parse":[]`) {
			t.Errorf("request lets the text ping people: %s", body)
		}
	}
}

func TestCanReadChannel(t *testing.T) {
	session, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatalf("discordgo.New() error: %v", err)
	}
	everyone := &discordgo.Role{ID: "g1", Permissions: readPermissions}
	if err := session.State.GuildAdd(&discordgo.Guild{ID: "g1", OwnerID: "owner", Roles: []*discordgo.Role{everyone}}); err != nil {
		t.Fatal(err)
	}
	for _, channel := range []*discordgo.Channel{
		{ID: "public", GuildID: "g1"},
		{ID: "secret", GuildID: "g1", PermissionOverwrites: []*discordgo.PermissionOverwrite{
			{ID: "g1", Type: discordgo.PermissionOverwriteTypeRole, Deny: discordgo.PermissionViewChannel},
		}},
	} {
		if err := session.State.ChannelAdd(channel); err != nil {
			t.Fatal(err)
		}
	}
	if err := session.State.MemberAdd(&discordgo.Member{GuildID: "g1", User: &discordgo.User{ID: "u1"}}); err != nil {
		t.Fatal(err)
	}

	if !canReadChannel(session, "u1", "public") {
		t.Error("a member may read a channel open to everyone")
	}
	if canReadChannel(session, "u1", "secret") {
		t.Error("a member may not read a channel hidden from them")
	}
}
