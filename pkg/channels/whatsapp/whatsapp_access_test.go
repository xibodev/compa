package whatsapp

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/xibodev/compa/v3/pkg/bus"
	"github.com/xibodev/compa/v3/pkg/config"
	"github.com/xibodev/compa/v3/pkg/pairing"
)

func newAccessTestChannel(t *testing.T, allowFrom ...string) (*WhatsAppChannel, *bus.MessageBus) {
	t.Helper()
	messageBus := bus.NewMessageBus()
	ch, err := NewWhatsAppChannel(&config.Channel{Type: config.ChannelWhatsApp, Enabled: true, AllowFrom: allowFrom},
		&config.WhatsAppSettings{BridgeURL: "ws://127.0.0.1:3001"}, messageBus)
	if err != nil {
		t.Fatalf("NewWhatsAppChannel: %v", err)
	}
	ch.ctx = context.Background()
	ch.SetAccessPolicy(config.DMPolicyPairing, config.GroupPolicyAllowlist)
	return ch, messageBus
}

func bridgeMediaFile(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp("", "bridge-access-*.jpg")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("img")
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })
	return f.Name()
}

func TestIncomingMessage_AllowListedGroupMemberIsAdmitted(t *testing.T) {
	ch, messageBus := newAccessTestChannel(t, "whatsapp:111", "group1@g.us")

	// The sender is not listed; the group is.
	ch.handleIncomingMessage(map[string]any{
		"from": "222", "chat": "group1@g.us", "content": "hello", "id": "m1",
		"media": []any{bridgeMediaFile(t)},
	})

	select {
	case inbound := <-messageBus.InboundChan():
		if len(inbound.Media) != 1 || inbound.Context.SenderIsOwner || inbound.Context.ChatType != "group" {
			t.Fatalf("media = %v, owner = %v, chat type = %q",
				inbound.Media, inbound.Context.SenderIsOwner, inbound.Context.ChatType)
		}
	case <-time.After(time.Second):
		t.Fatal("a member of an allow-listed group was not admitted")
	}
}

func TestIncomingMessage_UnpairedSenderIsRecordedWithoutMedia(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	ch, messageBus := newAccessTestChannel(t, "whatsapp:111")

	ch.handleIncomingMessage(map[string]any{
		"from": "999", "chat": "999", "content": "hi", "id": "m2",
		"media": []any{bridgeMediaFile(t)},
	})

	select {
	case inbound := <-messageBus.InboundChan():
		t.Fatalf("an unpaired sender's message was published: %#v", inbound)
	case <-time.After(50 * time.Millisecond):
	}
	requests, err := pairing.List(home, "whatsapp")
	if err != nil || len(requests) != 1 || requests[0].SenderID != "whatsapp:999" {
		t.Fatalf("pairing requests = %+v, %v; want the stranger", requests, err)
	}

	// An unlisted group is dropped without a pairing request.
	ch.handleIncomingMessage(map[string]any{"from": "999", "chat": "other@g.us", "content": "hi", "id": "m3"})
	select {
	case inbound := <-messageBus.InboundChan():
		t.Fatalf("an unlisted group's message was published: %#v", inbound)
	case <-time.After(50 * time.Millisecond):
	}
}
