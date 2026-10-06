package slack

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"

	"github.com/xibodev/compa/v2/pkg/bus"
	"github.com/xibodev/compa/v2/pkg/config"
	"github.com/xibodev/compa/v2/pkg/pairing"
)

func newAccessTestChannel(t *testing.T, allowFrom ...string) (*SlackChannel, *bus.MessageBus, *int) {
	t.Helper()
	cfg := &config.SlackSettings{}
	cfg.BotToken = *config.NewSecureString("xoxb-test")
	cfg.AppToken = *config.NewSecureString("xapp-test")
	messageBus := bus.NewMessageBus()
	ch, err := NewSlackChannel(&config.Channel{Type: "slack", Enabled: true, AllowFrom: allowFrom}, cfg, messageBus)
	if err != nil {
		t.Fatalf("NewSlackChannel: %v", err)
	}
	ch.ctx = context.Background()
	ch.botUserID = "UBOT"
	ch.SetAccessPolicy(config.DMPolicyPairing, config.GroupPolicyAllowlist)

	downloads := 0
	saved := fetchFile
	fetchFile = func(*SlackChannel, slack.File) string {
		downloads++
		return ""
	}
	t.Cleanup(func() { fetchFile = saved })
	return ch, messageBus, &downloads
}

func receiveInbound(messageBus *bus.MessageBus, wait time.Duration) (bus.InboundMessage, bool) {
	select {
	case msg := <-messageBus.InboundChan():
		return msg, true
	case <-time.After(wait):
		return bus.InboundMessage{}, false
	}
}

func fileMessage(channelID, user, text string) *slackevents.MessageEvent {
	return &slackevents.MessageEvent{
		Channel:   channelID,
		User:      user,
		Text:      text,
		TimeStamp: "1700000000.000100",
		SubType:   "file_share",
		Message:   &slack.Msg{Files: []slack.File{{ID: "F1", Name: "a.png"}}},
	}
}

func TestHandleMessageEvent_UnpairedSenderFilesAreNotFetched(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	ch, messageBus, downloads := newAccessTestChannel(t, "UOWNER")

	ch.handleMessageEvent(fileMessage("D999", "USTRANGER", "hello"))

	if *downloads != 0 {
		t.Fatalf("downloaded %d files for an unpaired sender", *downloads)
	}
	if msg, ok := receiveInbound(messageBus, 50*time.Millisecond); ok {
		t.Fatalf("an unpaired sender's message was published: %#v", msg)
	}
	if _, ok := ch.pendingAcks.Load("D999"); ok {
		t.Error("an unpaired sender's message would be acknowledged")
	}
	requests, err := pairing.List(home, "slack")
	if err != nil || len(requests) != 1 || requests[0].SenderID != "slack:USTRANGER" {
		t.Fatalf("pairing requests = %+v, %v; want the stranger", requests, err)
	}
}

func TestHandleMessageEvent_AllowListedChannelMemberIsAdmitted(t *testing.T) {
	ch, messageBus, _ := newAccessTestChannel(t, "UOWNER", "C100")

	ch.handleMessageEvent(&slackevents.MessageEvent{
		Channel: "C100", User: "UMEMBER", Text: "status?", TimeStamp: "1700000000.000200",
	})

	msg, ok := receiveInbound(messageBus, time.Second)
	if !ok {
		t.Fatal("a member of an allow-listed channel was not admitted")
	}
	if msg.Content != "status?" || msg.Context.SenderIsOwner {
		t.Fatalf("published %q, owner %v", msg.Content, msg.Context.SenderIsOwner)
	}
}

// A thread's chat ID is "<channel>/<thread ts>"; the channel's allow_from
// entry admits its members there too.
func TestHandleMessageEvent_AllowListedChannelThreadMemberIsAdmitted(t *testing.T) {
	ch, messageBus, _ := newAccessTestChannel(t, "UOWNER", "C100")

	ch.handleMessageEvent(&slackevents.MessageEvent{
		Channel: "C100", User: "UMEMBER", Text: "in a thread",
		TimeStamp: "1700000000.000400", ThreadTimeStamp: "1700000000.000200",
	})

	msg, ok := receiveInbound(messageBus, time.Second)
	if !ok {
		t.Fatal("a member of an allow-listed channel was not admitted in a thread")
	}
	if msg.Content != "in a thread" || msg.Context.TopicID != "1700000000.000200" || msg.Context.SenderIsOwner {
		t.Fatalf("published %q in thread %q, owner %v", msg.Content, msg.Context.TopicID, msg.Context.SenderIsOwner)
	}
}

func TestChannelMentionIsAnsweredOnce(t *testing.T) {
	ch, messageBus, _ := newAccessTestChannel(t, "UOWNER")

	// Slack delivers a channel mention as both a message and an app_mention.
	ch.handleMessageEvent(&slackevents.MessageEvent{
		Channel: "C100", User: "UOWNER", Text: "<@UBOT> status?", TimeStamp: "1700000000.000300",
	})
	ch.handleAppMention(&slackevents.AppMentionEvent{
		Channel: "C100", User: "UOWNER", Text: "<@UBOT> status?", TimeStamp: "1700000000.000300",
	})

	msg, ok := receiveInbound(messageBus, time.Second)
	if !ok {
		t.Fatal("the mention was not answered")
	}
	if msg.Content != "status?" || !msg.Context.Mentioned {
		t.Fatalf("published %q, mentioned %v", msg.Content, msg.Context.Mentioned)
	}
	if extra, ok := receiveInbound(messageBus, 50*time.Millisecond); ok {
		t.Fatalf("the mention was answered twice: %#v", extra)
	}
}

func TestSend_EscapesModelText(t *testing.T) {
	var (
		mu   sync.Mutex
		text string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		text = r.PostForm.Get("text")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"channel":"C100","ts":"1.2"}`))
	}))
	defer server.Close()

	ch, _, _ := newAccessTestChannel(t, "UOWNER")
	ch.api = slack.New("xoxb-test", slack.OptionAPIURL(server.URL+"/"))
	ch.SetRunning(true)

	if _, err := ch.Send(context.Background(), bus.OutboundMessage{
		ChatID:  "C100",
		Content: "<!channel> & <@U1> *done*",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if want := "&lt;!channel&gt; &amp; &lt;@U1&gt; *done*"; text != want {
		t.Fatalf("sent %q, want %q", text, want)
	}
}
