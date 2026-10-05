package dingtalk

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/open-dingtalk/dingtalk-stream-sdk-go/chatbot"

	"github.com/xibodev/compa/pkg/config"
	"github.com/xibodev/compa/pkg/pairing"
)

func TestOnChatBotMessageReceived_UnpairedSenderKeepsNoWebhook(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	ch, msgBus := newTestDingTalkChannel(t, config.DingTalkSettings{},
		&config.Channel{Type: config.ChannelDingTalk, Enabled: true, AllowFrom: []string{"owner"}})
	ch.SetAccessPolicy(config.DMPolicyPairing, config.GroupPolicyAllowlist)

	data := &chatbot.BotCallbackDataModel{
		ConversationId:   "conv-1",
		ConversationType: "1",
		SenderStaffId:    "stranger",
		SessionWebhook:   "https://example.com/webhook-secret",
	}
	data.Text.Content = "hello"
	if _, err := ch.onChatBotMessageReceived(context.Background(), data); err != nil {
		t.Fatal(err)
	}

	select {
	case msg := <-msgBus.InboundChan():
		t.Fatalf("an unpaired sender's message was published: %#v", msg)
	case <-time.After(50 * time.Millisecond):
	}
	if _, ok := ch.sessionWebhooks.Load("conv-1"); ok {
		t.Error("an unpaired sender's reply webhook was kept")
	}
	requests, err := pairing.List(home, "dingtalk")
	if err != nil || len(requests) != 1 || requests[0].SenderID != "dingtalk:stranger" {
		t.Fatalf("pairing requests = %+v, %v; want the stranger", requests, err)
	}
}

func TestOnChatBotMessageReceived_WebhookStaysOutOfMetadata(t *testing.T) {
	ch, msgBus := newTestDingTalkChannel(t, config.DingTalkSettings{},
		&config.Channel{Type: config.ChannelDingTalk, Enabled: true, AllowFrom: []string{"owner"}})
	ch.SetAccessPolicy(config.DMPolicyPairing, config.GroupPolicyAllowlist)

	data := &chatbot.BotCallbackDataModel{
		ConversationId:   "conv-2",
		ConversationType: "1",
		SenderStaffId:    "owner",
		SessionWebhook:   "https://example.com/webhook-secret",
	}
	data.Text.Content = "hello"
	if _, err := ch.onChatBotMessageReceived(context.Background(), data); err != nil {
		t.Fatal(err)
	}

	msg := mustReceiveInbound(t, msgBus)
	if !msg.Context.SenderIsOwner {
		t.Error("the owner's message is not marked as the owner's")
	}
	for k, v := range msg.Context.Raw {
		if strings.Contains(v, "webhook-secret") {
			t.Errorf("metadata %q holds the session webhook", k)
		}
	}
	for k, v := range msg.Context.ReplyHandles {
		if strings.Contains(v, "webhook-secret") {
			t.Errorf("reply handle %q holds the session webhook", k)
		}
	}
	if hook, ok := ch.sessionWebhooks.Load("conv-2"); !ok || hook != "https://example.com/webhook-secret" {
		t.Fatalf("session webhook for replies = %v, %v", hook, ok)
	}
}

func TestOnChatBotMessageReceived_AllowListedGroupMemberIsAdmitted(t *testing.T) {
	ch, msgBus := newTestDingTalkChannel(t, config.DingTalkSettings{},
		&config.Channel{Type: config.ChannelDingTalk, Enabled: true, AllowFrom: []string{"owner", "conv-group"}})
	ch.SetAccessPolicy(config.DMPolicyPairing, config.GroupPolicyAllowlist)

	data := &chatbot.BotCallbackDataModel{
		ConversationId:   "conv-group",
		ConversationType: "2",
		SenderStaffId:    "member",
		IsInAtList:       true,
	}
	data.Text.Content = "@bot status?"
	if _, err := ch.onChatBotMessageReceived(context.Background(), data); err != nil {
		t.Fatal(err)
	}

	msg := mustReceiveInbound(t, msgBus)
	if msg.Content != "status?" || msg.Context.SenderIsOwner {
		t.Fatalf("published %q, owner %v", msg.Content, msg.Context.SenderIsOwner)
	}
}
