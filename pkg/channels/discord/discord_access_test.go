//go:build paused_channels

package discord

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/xibodev/compa/v3/pkg/bus"
	"github.com/xibodev/compa/v3/pkg/channels"
	"github.com/xibodev/compa/v3/pkg/config"
	"github.com/xibodev/compa/v3/pkg/pairing"
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

	public, _ := session.State.Channel("public")
	secret, _ := session.State.Channel("secret")
	if !canReadChannel(session, "u1", public) {
		t.Error("a member may read a channel open to everyone")
	}
	if canReadChannel(session, "u1", secret) {
		t.Error("a member may not read a channel hidden from them")
	}

	// A thread is as readable as its parent; a private thread never counts.
	thread := func(parent string, kind discordgo.ChannelType) *discordgo.Channel {
		ch := &discordgo.Channel{ID: "t-" + parent + "-" + fmt.Sprint(kind), GuildID: "g1", ParentID: parent, Type: kind}
		if err := session.State.ChannelAdd(ch); err != nil {
			t.Fatal(err)
		}
		return ch
	}
	if !canReadChannel(session, "u1", thread("public", discordgo.ChannelTypeGuildPublicThread)) {
		t.Error("a member may read a thread of a channel open to everyone")
	}
	if canReadChannel(session, "u1", thread("secret", discordgo.ChannelTypeGuildPublicThread)) {
		t.Error("a member may not read a thread of a channel hidden from them")
	}
	if canReadChannel(session, "u1", thread("public", discordgo.ChannelTypeGuildPrivateThread)) {
		t.Error("a private thread counts as unreadable")
	}
}

// A <#id> mention gets the channel's name only for a channel of the same
// guild that the author may read, as a message link does.
func TestResolveDiscordRefsNamesOnlyReadableChannels(t *testing.T) {
	session, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatalf("discordgo.New() error: %v", err)
	}
	for _, guild := range []*discordgo.Guild{
		{ID: "1", OwnerID: "owner", Roles: []*discordgo.Role{{ID: "1", Permissions: readPermissions}}},
		{ID: "2", OwnerID: "owner", Roles: []*discordgo.Role{{ID: "2", Permissions: readPermissions}}},
	} {
		if err := session.State.GuildAdd(guild); err != nil {
			t.Fatal(err)
		}
	}
	for _, channel := range []*discordgo.Channel{
		{ID: "11", GuildID: "1", Name: "general"},
		{ID: "12", GuildID: "1", Name: "staff", PermissionOverwrites: []*discordgo.PermissionOverwrite{
			{ID: "1", Type: discordgo.PermissionOverwriteTypeRole, Deny: discordgo.PermissionViewChannel},
		}},
		{ID: "21", GuildID: "2", Name: "other-server"},
	} {
		if err := session.State.ChannelAdd(channel); err != nil {
			t.Fatal(err)
		}
	}
	if err := session.State.MemberAdd(&discordgo.Member{GuildID: "1", User: &discordgo.User{ID: "u1"}}); err != nil {
		t.Fatal(err)
	}

	got := (&DiscordChannel{}).resolveDiscordRefs(session, "see <#11>, <#12> and <#21>", "1", "u1")
	if want := "see #general, <#12> and <#21>"; got != want {
		t.Errorf("resolveDiscordRefs() = %q, want %q", got, want)
	}
	// A channel missing from the cache is asked of Discord; count those asks.
	transport := &countingTransport{}
	session.Client = &http.Client{Transport: transport}
	// A direct message has no server, so nothing is looked up.
	if got := (&DiscordChannel{}).resolveDiscordRefs(session, "see <#999>", "", "u1"); got != "see <#999>" {
		t.Errorf("resolveDiscordRefs() in a direct message = %q, want it unchanged", got)
	}
	if transport.requests != 0 {
		t.Errorf("a direct message made %d requests, want none", transport.requests)
	}
	// A channel mentioned twice is looked up once.
	if got := (&DiscordChannel{}).resolveDiscordRefs(session, "<#999> and <#999>", "1", "u1"); got != "<#999> and <#999>" {
		t.Errorf("resolveDiscordRefs() = %q, want it unchanged", got)
	}
	if transport.requests != 1 {
		t.Errorf("a channel mentioned twice made %d requests, want 1", transport.requests)
	}

	// At most maxChannelRefs mentions of one message are looked up.
	var text, want strings.Builder
	for i := range maxChannelRefs + 1 {
		id := fmt.Sprint(100 + i)
		if err := session.State.ChannelAdd(&discordgo.Channel{ID: id, GuildID: "1", Name: "c" + id}); err != nil {
			t.Fatal(err)
		}
		text.WriteString(" <#" + id + ">")
		if i < maxChannelRefs {
			want.WriteString(" #c" + id)
		} else {
			want.WriteString(" <#" + id + ">")
		}
	}
	if got := (&DiscordChannel{}).resolveDiscordRefs(session, text.String(), "1", "u1"); got != want.String() {
		t.Errorf("resolveDiscordRefs() = %q, want %q", got, want.String())
	}
}

// countingTransport answers every Discord API request with 404 Unknown
// Channel and counts them.
type countingTransport struct{ requests int }

func (c *countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	c.requests++
	return &http.Response{
		StatusCode: http.StatusNotFound,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"message":"Unknown Channel","code":10003}`)),
	}, nil
}
