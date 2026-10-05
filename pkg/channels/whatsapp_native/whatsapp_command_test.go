//go:build whatsapp_native

package whatsapp

import (
	"context"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/xibodev/compa/pkg/bus"
	"github.com/xibodev/compa/pkg/channels"
	"github.com/xibodev/compa/pkg/config"
	"github.com/xibodev/compa/pkg/pairing"
)

const (
	testOwnPhone = "15550001111"
	testOwnLID   = "98765"
)

func newTestNativeChannel(chats string, bc *config.Channel) (*WhatsAppNativeChannel, *bus.MessageBus) {
	messageBus := bus.NewMessageBus()
	if bc == nil {
		bc = &config.Channel{Type: config.ChannelWhatsAppNative, Enabled: true}
	}
	settings := &config.WhatsAppSettings{Chats: chats}
	ch := &WhatsAppNativeChannel{
		BaseChannel: channels.NewBaseChannel("whatsapp_native", settings, messageBus, bc.AllowFrom,
			channels.WithGroupTrigger(bc.GroupTrigger)),
		config: settings,
		sent:   newSentMessages(16),
		runCtx: context.Background(),
	}
	if bc.DMPolicy != "" || bc.GroupPolicy != "" {
		ch.SetAccessPolicy(bc.EffectiveDMPolicy(), bc.EffectiveGroupPolicy())
	}
	return ch, messageBus
}

func textEvent(id string, chat, sender types.JID, fromMe bool, text string) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Sender:   sender,
				Chat:     chat,
				IsFromMe: fromMe,
				IsGroup:  chat.Server == types.GroupServer,
			},
			ID:       id,
			PushName: "Alice",
		},
		Message: &waE2E.Message{Conversation: proto.String(text)},
	}
}

func receiveInbound(t *testing.T, messageBus *bus.MessageBus) (bus.InboundMessage, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	select {
	case <-ctx.Done():
		return bus.InboundMessage{}, false
	case inbound := <-messageBus.InboundChan():
		return inbound, true
	}
}

func TestHandleIncoming_DoesNotConsumeGenericCommandsLocally(t *testing.T) {
	ch, messageBus := newTestNativeChannel(config.WhatsAppChatsAll, nil)

	ch.handleIncoming(textEvent("mid1",
		types.NewJID("1001", types.DefaultUserServer),
		types.NewJID("1001", types.DefaultUserServer), false, "/new"))

	inbound, ok := receiveInbound(t, messageBus)
	if !ok {
		t.Fatal("expected inbound message to be forwarded")
	}
	if inbound.Channel != "whatsapp_native" {
		t.Fatalf("channel=%q", inbound.Channel)
	}
	if inbound.Content != "/new" {
		t.Fatalf("content=%q", inbound.Content)
	}
}

func TestHandleMessage_SelfChatIsOwnerInput(t *testing.T) {
	for _, mode := range []string{config.WhatsAppChatsSelf, config.WhatsAppChatsAllowed, config.WhatsAppChatsAll} {
		t.Run(mode, func(t *testing.T) {
			ch, messageBus := newTestNativeChannel(mode, nil)
			own := types.NewJID(testOwnPhone, types.DefaultUserServer)

			ch.handleMessage(textEvent("m1", own, own, true, "remind me at 5"), testOwnPhone, testOwnLID)

			inbound, ok := receiveInbound(t, messageBus)
			if !ok {
				t.Fatal("the owner's own chat is input")
			}
			if !inbound.Context.SenderIsOwner {
				t.Error("the owner's message in their own chat is from the owner")
			}
			if inbound.Context.ChatType != "direct" {
				t.Errorf("chat type = %q, want direct", inbound.Context.ChatType)
			}
		})
	}
}

func TestHandleMessage_SelfChatByLID(t *testing.T) {
	ch, messageBus := newTestNativeChannel(config.WhatsAppChatsSelf, nil)
	ownLID := types.NewJID(testOwnLID, types.HiddenUserServer)

	ch.handleMessage(textEvent("m1", ownLID, ownLID, true, "hello"), testOwnPhone, testOwnLID)

	if _, ok := receiveInbound(t, messageBus); !ok {
		t.Fatal("the own chat addressed by LID is input")
	}
}

func TestHandleMessage_SelfModeIgnoresOtherChats(t *testing.T) {
	ch, messageBus := newTestNativeChannel(config.WhatsAppChatsSelf, nil)
	friend := types.NewJID("15550003333", types.DefaultUserServer)

	ch.handleMessage(textEvent("m1", friend, friend, false, "hi"), testOwnPhone, testOwnLID)

	if inbound, ok := receiveInbound(t, messageBus); ok {
		t.Fatalf("a friend's chat is not input in self mode: %#v", inbound)
	}
}

func TestHandleMessage_OwnMessagesElsewhereAreNeverInput(t *testing.T) {
	ch, messageBus := newTestNativeChannel(config.WhatsAppChatsAll, nil)
	own := types.NewJID(testOwnPhone, types.DefaultUserServer)
	friend := types.NewJID("15550003333", types.DefaultUserServer)
	group := types.NewJID("120363000000000001", types.GroupServer)

	ch.handleMessage(textEvent("m1", friend, own, true, "see you at 8"), testOwnPhone, testOwnLID)
	ch.handleMessage(textEvent("m2", group, own, true, "hello all"), testOwnPhone, testOwnLID)

	if inbound, ok := receiveInbound(t, messageBus); ok {
		t.Fatalf("the owner's messages to others are not input: %#v", inbound)
	}
}

func TestHandleMessage_IgnoresEchoOfSentMessages(t *testing.T) {
	ch, messageBus := newTestNativeChannel(config.WhatsAppChatsSelf, nil)
	own := types.NewJID(testOwnPhone, types.DefaultUserServer)
	ch.sent.add("reply-1")

	ch.handleMessage(textEvent("reply-1", own, own, true, "Here is your reminder"), testOwnPhone, testOwnLID)

	if inbound, ok := receiveInbound(t, messageBus); ok {
		t.Fatalf("Compa's own reply is not input: %#v", inbound)
	}
}

func TestHandleMessage_IgnoresStatusBroadcasts(t *testing.T) {
	ch, messageBus := newTestNativeChannel(config.WhatsAppChatsAll, nil)
	friend := types.NewJID("15550003333", types.DefaultUserServer)

	ch.handleMessage(textEvent("m1", types.StatusBroadcastJID, friend, false, "my status"), testOwnPhone, testOwnLID)

	if inbound, ok := receiveInbound(t, messageBus); ok {
		t.Fatalf("status updates are not input: %#v", inbound)
	}
}

func TestHandleMessage_AllowedModeTakesOnlyAdmittedChats(t *testing.T) {
	bc := &config.Channel{
		Type:      config.ChannelWhatsAppNative,
		Enabled:   true,
		AllowFrom: config.FlexibleStringSlice{"15550003333@s.whatsapp.net"},
	}
	ch, messageBus := newTestNativeChannel(config.WhatsAppChatsAllowed, bc)
	friend := types.NewJID("15550003333", types.DefaultUserServer)
	stranger := types.NewJID("15550004444", types.DefaultUserServer)

	ch.handleMessage(textEvent("m1", stranger, stranger, false, "hi"), testOwnPhone, testOwnLID)
	if inbound, ok := receiveInbound(t, messageBus); ok {
		t.Fatalf("a stranger's chat is not input in allowed mode: %#v", inbound)
	}

	ch.handleMessage(textEvent("m2", friend, friend, false, "hi"), testOwnPhone, testOwnLID)
	inbound, ok := receiveInbound(t, messageBus)
	if !ok {
		t.Fatal("a listed contact's chat is input in allowed mode")
	}
	if inbound.Context.ChatType != "direct" {
		t.Errorf("chat type = %q, want direct", inbound.Context.ChatType)
	}
}

// pairingRequests returns the pairing requests recorded under home.
func pairingRequests(t *testing.T, home string) []pairing.Request {
	t.Helper()
	requests, err := pairing.List(home, "whatsapp_native")
	if err != nil {
		t.Fatalf("pairing.List: %v", err)
	}
	return requests
}

func TestHandleMessage_AllowedModeMakesNoPairingRequests(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	bc := &config.Channel{
		Type:        config.ChannelWhatsAppNative,
		Enabled:     true,
		AllowFrom:   config.FlexibleStringSlice{"15550003333@s.whatsapp.net"},
		DMPolicy:    config.DMPolicyPairing,
		GroupPolicy: config.GroupPolicyAllowlist,
	}
	ch, messageBus := newTestNativeChannel(config.WhatsAppChatsAllowed, bc)
	stranger := types.NewJID("15550004444", types.DefaultUserServer)

	ch.handleMessage(textEvent("m1", stranger, stranger, false, "hi"), testOwnPhone, testOwnLID)

	if inbound, ok := receiveInbound(t, messageBus); ok {
		t.Fatalf("a stranger's chat is not input in allowed mode: %#v", inbound)
	}
	if requests := pairingRequests(t, home); len(requests) != 0 {
		t.Fatalf("the owner's own conversations make no pairing requests: %+v", requests)
	}
}

func TestHandleMessage_AllModeFollowsPolicies(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	stranger := types.NewJID("15550004444", types.DefaultUserServer)

	// With the pairing policy, a stranger is held for the owner's approval.
	paired := &config.Channel{
		Type:        config.ChannelWhatsAppNative,
		Enabled:     true,
		DMPolicy:    config.DMPolicyPairing,
		GroupPolicy: config.GroupPolicyAllowlist,
	}
	ch, messageBus := newTestNativeChannel(config.WhatsAppChatsAll, paired)
	ch.handleMessage(textEvent("m1", stranger, stranger, false, "hi"), testOwnPhone, testOwnLID)
	if inbound, ok := receiveInbound(t, messageBus); ok {
		t.Fatalf("an unpaired stranger is not input: %#v", inbound)
	}
	requests := pairingRequests(t, home)
	if len(requests) != 1 || requests[0].SenderID != "whatsapp:15550004444@s.whatsapp.net" {
		t.Fatalf("pairing requests = %+v, want the stranger", requests)
	}

	// With open policies (what a 1.0 config migrates to), everyone is input.
	open := &config.Channel{
		Type:        config.ChannelWhatsAppNative,
		Enabled:     true,
		DMPolicy:    config.DMPolicyOpen,
		GroupPolicy: config.GroupPolicyOpen,
	}
	ch, messageBus = newTestNativeChannel(config.WhatsAppChatsAll, open)
	ch.handleMessage(textEvent("m2", stranger, stranger, false, "hi"), testOwnPhone, testOwnLID)
	inbound, ok := receiveInbound(t, messageBus)
	if !ok {
		t.Fatal("open policies take every chat in all mode")
	}
	if inbound.Context.SenderIsOwner {
		t.Error("a sender admitted by an open policy is not the owner")
	}
}

func TestHandleMessage_AllowedModeTakesListedGroup(t *testing.T) {
	bc := &config.Channel{
		Type:        config.ChannelWhatsAppNative,
		Enabled:     true,
		AllowFrom:   config.FlexibleStringSlice{"120363000000000001@g.us"},
		DMPolicy:    config.DMPolicyAllowlist,
		GroupPolicy: config.GroupPolicyAllowlist,
	}
	ch, messageBus := newTestNativeChannel(config.WhatsAppChatsAllowed, bc)
	listedGroup := types.NewJID("120363000000000001", types.GroupServer)
	otherGroup := types.NewJID("120363000000000002", types.GroupServer)
	member := types.NewJID("15550005555", types.DefaultUserServer)

	ch.handleMessage(textEvent("m1", otherGroup, member, false, "hi"), testOwnPhone, testOwnLID)
	if inbound, ok := receiveInbound(t, messageBus); ok {
		t.Fatalf("an unlisted group is not input: %#v", inbound)
	}

	ch.handleMessage(textEvent("m2", listedGroup, member, false, "hi"), testOwnPhone, testOwnLID)
	inbound, ok := receiveInbound(t, messageBus)
	if !ok {
		t.Fatal("a listed group is input in allowed mode")
	}
	if inbound.Context.ChatType != "group" || inbound.Context.SenderIsOwner {
		t.Errorf("chat type = %q, owner = %v", inbound.Context.ChatType, inbound.Context.SenderIsOwner)
	}
}

func TestHandleMessage_GroupNeedsMentionWhenMentionOnly(t *testing.T) {
	bc := &config.Channel{
		Type:         config.ChannelWhatsAppNative,
		Enabled:      true,
		GroupTrigger: config.GroupTriggerConfig{MentionOnly: true},
	}
	ch, messageBus := newTestNativeChannel(config.WhatsAppChatsAll, bc)
	group := types.NewJID("120363000000000001", types.GroupServer)
	member := types.NewJID("15550003333", types.DefaultUserServer)

	ch.handleMessage(textEvent("m1", group, member, false, "lunch?"), testOwnPhone, testOwnLID)
	if inbound, ok := receiveInbound(t, messageBus); ok {
		t.Fatalf("an unaddressed group message is not input: %#v", inbound)
	}

	mention := textEvent("m2", group, member, false, "")
	mention.Message = &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
		Text: proto.String("@" + testOwnPhone + " what's the weather?"),
		ContextInfo: &waE2E.ContextInfo{
			MentionedJID: []string{testOwnPhone + "@s.whatsapp.net"},
		},
	}}
	ch.handleMessage(mention, testOwnPhone, testOwnLID)
	inbound, ok := receiveInbound(t, messageBus)
	if !ok {
		t.Fatal("a group message mentioning the account is input")
	}
	if inbound.Context.ChatType != "group" || !inbound.Context.Mentioned {
		t.Errorf("chat type = %q, mentioned = %v", inbound.Context.ChatType, inbound.Context.Mentioned)
	}
	if inbound.Content != "what's the weather?" {
		t.Errorf("content = %q", inbound.Content)
	}
}
