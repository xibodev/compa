//go:build paused_channels && (amd64 || arm64 || riscv64 || mips64 || ppc64)

package feishu

import (
	"context"
	"testing"
	"time"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/xibodev/compa/v4/pkg/bus"
	"github.com/xibodev/compa/v4/pkg/config"
	"github.com/xibodev/compa/v4/pkg/media"
	"github.com/xibodev/compa/v4/pkg/pairing"
)

func newFeishuAccessChannel(t *testing.T, downloads *int, allowFrom ...string) (*FeishuChannel, *bus.MessageBus) {
	t.Helper()
	messageBus := bus.NewMessageBus()
	ch, err := NewFeishuChannel(&config.Channel{Type: config.ChannelFeishu, Enabled: true, AllowFrom: allowFrom},
		&config.FeishuSettings{AppID: "app", AppSecret: *config.NewSecureString("secret")}, messageBus)
	if err != nil {
		t.Fatalf("NewFeishuChannel: %v", err)
	}
	ch.SetAccessPolicy(config.DMPolicyPairing, config.GroupPolicyAllowlist)
	ch.SetMediaStore(media.NewFileMediaStore())
	old := fetchInboundMedia
	fetchInboundMedia = func(*FeishuChannel, context.Context, string, string, string, string, media.MediaStore) []string {
		*downloads++
		return []string{"media://fake"}
	}
	t.Cleanup(func() { fetchInboundMedia = old })
	return ch, messageBus
}

func feishuImageEvent(chatType, chatID, senderID string) *larkim.P2MessageReceiveV1 {
	str := func(s string) *string { return &s }
	return &larkim.P2MessageReceiveV1{Event: &larkim.P2MessageReceiveV1Data{
		Sender: &larkim.EventSender{SenderId: &larkim.UserId{OpenId: str(senderID)}},
		Message: &larkim.EventMessage{
			MessageId:   str("om_" + senderID),
			ChatId:      str(chatID),
			ChatType:    str(chatType),
			MessageType: str("image"),
			Content:     str(`{"image_key":"img_1"}`),
		},
	}}
}

func TestHandleMessageReceive_UnpairedSenderMediaIsNotFetched(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	downloads := 0
	ch, messageBus := newFeishuAccessChannel(t, &downloads, "feishu:ou_owner")

	if err := ch.handleMessageReceive(context.Background(), feishuImageEvent("p2p", "oc_dm", "ou_stranger")); err != nil {
		t.Fatal(err)
	}
	if downloads != 0 {
		t.Fatalf("downloaded media %d times for an unpaired sender", downloads)
	}
	select {
	case inbound := <-messageBus.InboundChan():
		t.Fatalf("an unpaired sender's message was published: %#v", inbound)
	case <-time.After(50 * time.Millisecond):
	}
	requests, err := pairing.List(home, "feishu")
	if err != nil || len(requests) != 1 || requests[0].SenderID != "feishu:ou_stranger" {
		t.Fatalf("pairing requests = %+v, %v; want the stranger", requests, err)
	}
}

func TestHandleMessageReceive_AllowListedGroupMemberIsAdmitted(t *testing.T) {
	downloads := 0
	ch, messageBus := newFeishuAccessChannel(t, &downloads, "feishu:ou_owner", "oc_group")

	if err := ch.handleMessageReceive(context.Background(), feishuImageEvent("group", "oc_group", "ou_member")); err != nil {
		t.Fatal(err)
	}
	select {
	case inbound := <-messageBus.InboundChan():
		if len(inbound.Media) != 1 || inbound.Context.SenderIsOwner {
			t.Fatalf("media = %v, owner = %v", inbound.Media, inbound.Context.SenderIsOwner)
		}
	case <-time.After(time.Second):
		t.Fatal("a member of an allow-listed group was not admitted")
	}
	if downloads != 1 {
		t.Fatalf("downloads = %d, want 1", downloads)
	}
}

func TestHandleMessageReceive_UnlistedGroupIsNotFetched(t *testing.T) {
	downloads := 0
	ch, messageBus := newFeishuAccessChannel(t, &downloads, "feishu:ou_owner")

	if err := ch.handleMessageReceive(context.Background(), feishuImageEvent("group", "oc_other", "ou_member")); err != nil {
		t.Fatal(err)
	}
	if downloads != 0 {
		t.Fatalf("downloaded media %d times for an unlisted group", downloads)
	}
	select {
	case inbound := <-messageBus.InboundChan():
		t.Fatalf("an unlisted group's message was published: %#v", inbound)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestHandleMessageReceive_RecoversFromPanic(t *testing.T) {
	downloads := 0
	ch, _ := newFeishuAccessChannel(t, &downloads, "feishu:ou_owner")
	fetchInboundMedia = func(*FeishuChannel, context.Context, string, string, string, string, media.MediaStore) []string {
		panic("malformed event")
	}
	if err := ch.handleMessageReceive(context.Background(), feishuImageEvent("p2p", "oc_dm", "ou_owner")); err != nil {
		t.Fatal(err)
	}
}
