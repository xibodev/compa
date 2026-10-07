package telegram

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mymmrac/telego"
	ta "github.com/mymmrac/telego/telegoapi"

	"github.com/xibodev/compa/v3/pkg/bus"
	"github.com/xibodev/compa/v3/pkg/channels"
	"github.com/xibodev/compa/v3/pkg/config"
)

type getMeCaller struct {
	username string
}

func (c getMeCaller) Call(_ context.Context, url string, _ *ta.RequestData) (*ta.Response, error) {
	if strings.HasSuffix(url, "/getMe") {
		result := fmt.Sprintf(`{"id":1,"is_bot":true,"first_name":"bot","username":%q}`, c.username)
		return &ta.Response{Ok: true, Result: []byte(result)}, nil
	}
	return &ta.Response{Ok: true, Result: []byte("true")}, nil
}

func newTestTelegramBot(t *testing.T, username string) *telego.Bot {
	t.Helper()

	token := "123456:" + strings.Repeat("a", 35)
	bot, err := telego.NewBot(token,
		telego.WithAPICaller(getMeCaller{username: username}),
		telego.WithDiscardLogger(),
	)
	if err != nil {
		t.Fatalf("NewBot error: %v", err)
	}
	return bot
}

func newGroupMentionOnlyChannel(t *testing.T, botUsername string) (*TelegramChannel, *bus.MessageBus) {
	t.Helper()

	messageBus := bus.NewMessageBus()
	ch := &TelegramChannel{
		BaseChannel: channels.NewBaseChannel("telegram", nil, messageBus, nil,
			channels.WithGroupTrigger(config.GroupTriggerConfig{MentionOnly: true}),
		),
		bot:     newTestTelegramBot(t, botUsername),
		chatIDs: make(map[string]int64),
		ctx:     context.Background(),
	}
	return ch, messageBus
}

func TestHandleMessage_GroupMentionOnly_BotCommandEntity(t *testing.T) {
	tests := []struct {
		name          string
		text          string
		wantForwarded bool
		wantContent   string
	}{
		{
			name:          "command with bot username",
			text:          "/new@testbot",
			wantForwarded: true,
			wantContent:   "/new",
		},
		{
			name:          "bare command goes to every bot, not a mention",
			text:          "/new",
			wantForwarded: false,
		},
		{
			name:          "command for another bot",
			text:          "/new@otherbot",
			wantForwarded: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ch, messageBus := newGroupMentionOnlyChannel(t, "testbot")

			msg := &telego.Message{
				Text: tc.text,
				Entities: []telego.MessageEntity{{
					Type:   telego.EntityTypeBotCommand,
					Offset: 0,
					Length: len([]rune(tc.text)),
				}},
				MessageID: 42,
				Chat: telego.Chat{
					ID:   123,
					Type: "group",
				},
				From: &telego.User{
					ID:        7,
					FirstName: "Alice",
				},
			}

			if err := ch.handleMessage(context.Background(), msg); err != nil {
				t.Fatalf("handleMessage error: %v", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			select {
			case <-ctx.Done():
				if tc.wantForwarded {
					t.Fatal("timeout waiting for message to be forwarded")
					return
				}
			case inbound, ok := <-messageBus.InboundChan():
				if tc.wantForwarded {
					if !ok {
						t.Fatal("expected inbound message to be forwarded")
					}
					if inbound.Content != tc.wantContent {
						t.Fatalf("content=%q want=%q", inbound.Content, tc.wantContent)
					}
					return
				}
			}
		})
	}
}

func TestIsBotMentioned_MentionEntityUnaffected(t *testing.T) {
	ch, _ := newGroupMentionOnlyChannel(t, "testbot")

	msg := &telego.Message{
		Text: "@testbot hello",
		Entities: []telego.MessageEntity{{
			Type:   telego.EntityTypeMention,
			Offset: 0,
			Length: len("@testbot"),
		}},
	}

	if !ch.isBotMentioned(msg) {
		t.Fatal("expected mention entity to be treated as bot mention")
	}
}

// fileCountingCaller answers getMe and counts getFile requests.
type fileCountingCaller struct {
	getMeCaller
	getFiles *int
}

func (c fileCountingCaller) Call(ctx context.Context, url string, data *ta.RequestData) (*ta.Response, error) {
	if strings.HasSuffix(url, "/getFile") {
		*c.getFiles++
		return &ta.Response{Ok: true, Result: []byte(`{"file_id":"p1","file_path":""}`)}, nil
	}
	return c.getMeCaller.Call(ctx, url, data)
}

func TestHandleMessage_GroupIgnoredBeforeDownloadingMedia(t *testing.T) {
	ch, messageBus := newGroupMentionOnlyChannel(t, "testbot")
	getFiles := 0
	bot, err := telego.NewBot("123456:"+strings.Repeat("a", 35),
		telego.WithAPICaller(fileCountingCaller{getMeCaller: getMeCaller{username: "testbot"}, getFiles: &getFiles}),
		telego.WithDiscardLogger(),
	)
	if err != nil {
		t.Fatalf("NewBot: %v", err)
	}
	ch.bot = bot

	msg := &telego.Message{
		Caption:   "look at this",
		Photo:     []telego.PhotoSize{{FileID: "p1"}},
		MessageID: 7,
		Chat:      telego.Chat{ID: 123, Type: "group"},
		From:      &telego.User{ID: 7, FirstName: "Alice"},
	}
	if err := ch.handleMessage(context.Background(), msg); err != nil {
		t.Fatalf("handleMessage: %v", err)
	}

	select {
	case inbound := <-messageBus.InboundChan():
		t.Fatalf("an unaddressed group message is ignored: %#v", inbound)
	case <-time.After(50 * time.Millisecond):
	}
	if getFiles != 0 {
		t.Fatalf("media of an ignored message was requested %d times", getFiles)
	}
}

func TestHandleMessage_ReplyToBotIsAMention(t *testing.T) {
	ch, messageBus := newGroupMentionOnlyChannel(t, "testbot")

	msg := &telego.Message{
		Text:      "and tomorrow?",
		MessageID: 8,
		Chat:      telego.Chat{ID: 123, Type: "group"},
		From:      &telego.User{ID: 7, FirstName: "Alice"},
		ReplyToMessage: &telego.Message{
			MessageID: 5,
			Text:      "Sunny today.",
			From:      &telego.User{ID: 1, IsBot: true, Username: "testbot"},
		},
	}
	if err := ch.handleMessage(context.Background(), msg); err != nil {
		t.Fatalf("handleMessage: %v", err)
	}

	select {
	case inbound := <-messageBus.InboundChan():
		if !inbound.Context.Mentioned {
			t.Fatal("a reply to the bot counts as a mention")
		}
	case <-time.After(time.Second):
		t.Fatal("a reply to the bot's message is answered in mention-only groups")
	}
}
