//go:build paused_channels

package telegram

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/xibodev/compa/v4/pkg/bus"
	"github.com/xibodev/compa/v4/pkg/channels"
	"github.com/xibodev/compa/v4/pkg/config"
	"github.com/xibodev/compa/v4/pkg/pairing"
)

func newAccessTestChannel(t *testing.T, allowFrom ...string) (*TelegramChannel, *bus.MessageBus, *int) {
	t.Helper()
	getFiles := 0
	bot, err := telego.NewBot("123456:"+strings.Repeat("a", 35),
		telego.WithAPICaller(errorFillingCaller{fileCountingCaller{getMeCaller: getMeCaller{username: "testbot"}, getFiles: &getFiles}}),
		telego.WithDiscardLogger(),
	)
	if err != nil {
		t.Fatalf("NewBot: %v", err)
	}
	messageBus := bus.NewMessageBus()
	ch := &TelegramChannel{
		BaseChannel: channels.NewBaseChannel("telegram", nil, messageBus, allowFrom),
		bot:         bot,
		chatIDs:     make(map[string]int64),
		ctx:         context.Background(),
	}
	ch.SetAccessPolicy(config.DMPolicyPairing, config.GroupPolicyAllowlist)
	return ch, messageBus, &getFiles
}

func receiveInbound(messageBus *bus.MessageBus, wait time.Duration) (bus.InboundMessage, bool) {
	select {
	case msg := <-messageBus.InboundChan():
		return msg, true
	case <-time.After(wait):
		return bus.InboundMessage{}, false
	}
}

func TestHandleMessage_UnpairedSenderMediaIsNotFetched(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	ch, messageBus, getFiles := newAccessTestChannel(t, "111")

	err := ch.handleMessage(context.Background(), &telego.Message{
		MessageID: 1,
		Caption:   "look",
		Photo:     []telego.PhotoSize{{FileID: "p1"}},
		Chat:      telego.Chat{ID: 999, Type: "private"},
		From:      &telego.User{ID: 999, FirstName: "Eve"},
	})
	if err != nil {
		t.Fatalf("handleMessage: %v", err)
	}

	if *getFiles != 0 {
		t.Fatalf("requested %d files for an unpaired sender", *getFiles)
	}
	if msg, ok := receiveInbound(messageBus, 50*time.Millisecond); ok {
		t.Fatalf("an unpaired sender's message was published: %#v", msg)
	}
	ch.chatIDsMu.Lock()
	_, kept := ch.chatIDs["999"]
	ch.chatIDsMu.Unlock()
	if kept {
		t.Error("an unpaired sender's chat was remembered")
	}
	requests, err := pairing.List(home, "telegram")
	if err != nil || len(requests) != 1 || requests[0].SenderID != "telegram:999" {
		t.Fatalf("pairing requests = %+v, %v; want the stranger", requests, err)
	}
}

func TestHandleMessage_AllowListedGroupMemberIsAdmitted(t *testing.T) {
	ch, messageBus, _ := newAccessTestChannel(t, "111", "-100200")

	err := ch.handleMessage(context.Background(), &telego.Message{
		MessageID: 2,
		Text:      "hello",
		Chat:      telego.Chat{ID: -100200, Type: "supergroup"},
		From:      &telego.User{ID: 555, FirstName: "Member"},
	})
	if err != nil {
		t.Fatalf("handleMessage: %v", err)
	}

	msg, ok := receiveInbound(messageBus, time.Second)
	if !ok {
		t.Fatal("a member of an allow-listed group was not admitted")
	}
	if msg.Content != "hello" || msg.Context.SenderIsOwner {
		t.Fatalf("published %q, owner %v", msg.Content, msg.Context.SenderIsOwner)
	}
}

// In a forum topic the chat ID is "<group>/<topic>"; the group's allow_from
// entry admits its members there too.
func TestHandleMessage_AllowListedForumTopicMemberIsAdmitted(t *testing.T) {
	ch, messageBus, _ := newAccessTestChannel(t, "111", "-100200")

	err := ch.handleMessage(context.Background(), &telego.Message{
		MessageID:       4,
		MessageThreadID: 42,
		Text:            "in a topic",
		Chat:            telego.Chat{ID: -100200, Type: "supergroup", IsForum: true},
		From:            &telego.User{ID: 555, FirstName: "Member"},
	})
	if err != nil {
		t.Fatalf("handleMessage: %v", err)
	}

	msg, ok := receiveInbound(messageBus, time.Second)
	if !ok {
		t.Fatal("a member of an allow-listed forum group was not admitted in a topic")
	}
	if msg.Content != "in a topic" || msg.Context.ChatID != "-100200/42" || msg.Context.SenderIsOwner {
		t.Fatalf("published %q in %q, owner %v", msg.Content, msg.Context.ChatID, msg.Context.SenderIsOwner)
	}
}

func TestHandleMessage_OwnerIsPublishedAsOwner(t *testing.T) {
	ch, messageBus, _ := newAccessTestChannel(t, "111")

	err := ch.handleMessage(context.Background(), &telego.Message{
		MessageID: 3,
		Text:      "hi",
		Chat:      telego.Chat{ID: 111, Type: "private"},
		From:      &telego.User{ID: 111, FirstName: "Owner"},
	})
	if err != nil {
		t.Fatalf("handleMessage: %v", err)
	}

	msg, ok := receiveInbound(messageBus, time.Second)
	if !ok {
		t.Fatal("the owner's message was not published")
	}
	if msg.Content != "hi" || msg.Context.ChatType != "direct" || !msg.Context.SenderIsOwner {
		t.Fatalf("published %q, chat type %q, owner %v", msg.Content, msg.Context.ChatType, msg.Context.SenderIsOwner)
	}
}
