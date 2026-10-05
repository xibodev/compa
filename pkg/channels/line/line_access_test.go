package line

import (
	"context"
	"testing"
	"time"

	"github.com/line/line-bot-sdk-go/v8/linebot/webhook"

	"github.com/xibodev/compa/pkg/bus"
	"github.com/xibodev/compa/pkg/channels"
	"github.com/xibodev/compa/pkg/config"
	"github.com/xibodev/compa/pkg/pairing"
)

func newAccessTestChannel(t *testing.T, allowFrom ...string) (*LINEChannel, *bus.MessageBus, *int) {
	t.Helper()
	messageBus := bus.NewMessageBus()
	ch := &LINEChannel{
		BaseChannel: channels.NewBaseChannel("line", nil, messageBus, allowFrom),
		config:      &config.LINESettings{},
		ctx:         context.Background(),
	}
	ch.SetAccessPolicy(config.DMPolicyPairing, config.GroupPolicyAllowlist)

	downloads := 0
	saved := fetchContent
	fetchContent = func(*LINEChannel, string, string) string {
		downloads++
		return ""
	}
	t.Cleanup(func() { fetchContent = saved })
	return ch, messageBus, &downloads
}

func receive(messageBus *bus.MessageBus) (bus.InboundMessage, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	select {
	case <-ctx.Done():
		return bus.InboundMessage{}, false
	case msg := <-messageBus.InboundChan():
		return msg, true
	}
}

func TestProcessEvent_UnpairedSenderMediaIsNotFetched(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	ch, messageBus, downloads := newAccessTestChannel(t, "U_OWNER")

	ch.processEvent(webhook.MessageEvent{
		Source:     webhook.UserSource{UserId: "U_STRANGER"},
		ReplyToken: "token",
		Message:    webhook.ImageMessageContent{Id: "m1"},
	})

	if *downloads != 0 {
		t.Fatalf("downloaded %d files for an unpaired sender", *downloads)
	}
	if msg, ok := receive(messageBus); ok {
		t.Fatalf("an unpaired sender's message was published: %#v", msg)
	}
	if _, ok := ch.replyTokens.Load("U_STRANGER"); ok {
		t.Error("an unpaired sender's reply token was kept")
	}
	requests, err := pairing.List(home, "line")
	if err != nil || len(requests) != 1 || requests[0].SenderID != "line:U_STRANGER" {
		t.Fatalf("pairing requests = %+v, %v; want the stranger", requests, err)
	}
}

func TestProcessEvent_GroupTriggerRunsBeforeDownload(t *testing.T) {
	ch, messageBus, downloads := newAccessTestChannel(t, "U_OWNER")
	ch.BaseChannel = channels.NewBaseChannel("line", nil, messageBus, []string{"U_OWNER"},
		channels.WithGroupTrigger(config.GroupTriggerConfig{MentionOnly: true}))
	ch.SetAccessPolicy(config.DMPolicyPairing, config.GroupPolicyOpen)

	ch.processEvent(webhook.MessageEvent{
		Source:  webhook.GroupSource{GroupId: "C1", UserId: "U_MEMBER"},
		Message: webhook.ImageMessageContent{Id: "m1"},
	})

	if *downloads != 0 {
		t.Fatalf("downloaded %d files for an unaddressed group message", *downloads)
	}
	if msg, ok := receive(messageBus); ok {
		t.Fatalf("an unaddressed group message was published: %#v", msg)
	}
}

func TestProcessEvent_AdmittedTextIsPublished(t *testing.T) {
	ch, messageBus, _ := newAccessTestChannel(t, "U_OWNER")

	ch.processEvent(webhook.MessageEvent{
		Source:  webhook.UserSource{UserId: "U_OWNER"},
		Message: webhook.TextMessageContent{Id: "m1", Text: "hello"},
	})

	msg, ok := receive(messageBus)
	if !ok {
		t.Fatal("the owner's message was not published")
	}
	if msg.Content != "hello" || msg.Context.ChatType != "direct" || !msg.Context.SenderIsOwner {
		t.Fatalf("published %q, chat type %q, owner %v", msg.Content, msg.Context.ChatType, msg.Context.SenderIsOwner)
	}
}

func TestProcessEvent_RecoversFromPanic(t *testing.T) {
	ch, _, _ := newAccessTestChannel(t, "U_OWNER")
	fetchContent = func(*LINEChannel, string, string) string { panic("malformed event") }

	// Runs on its own goroutine in production; a panic must not escape.
	ch.processEvent(webhook.MessageEvent{
		Source:  webhook.UserSource{UserId: "U_OWNER"},
		Message: webhook.ImageMessageContent{Id: "m1"},
	})
}
