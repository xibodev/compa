package whatsapp

import (
	"context"
	"testing"
	"time"

	"github.com/xibodev/compa/v3/pkg/bus"
	"github.com/xibodev/compa/v3/pkg/channels"
	"github.com/xibodev/compa/v3/pkg/config"
)

func TestHandleIncomingMessage_DoesNotConsumeGenericCommandsLocally(t *testing.T) {
	messageBus := bus.NewMessageBus()
	ch := &WhatsAppChannel{
		BaseChannel: channels.NewBaseChannel("whatsapp", config.WhatsAppSettings{}, messageBus, nil),
		ctx:         context.Background(),
	}

	// A direct message: its chat is the sender's.
	ch.handleIncomingMessage(map[string]any{
		"type":    "message",
		"id":      "mid1",
		"from":    "user1",
		"chat":    "user1",
		"content": "/help",
	})

	select {
	case inbound, ok := <-messageBus.InboundChan():
		if !ok {
			t.Fatal("expected inbound message to be forwarded")
		}
		if inbound.Channel != "whatsapp" {
			t.Fatalf("channel=%q", inbound.Channel)
		}
		if inbound.Content != "/help" {
			t.Fatalf("content=%q", inbound.Content)
		}
	case <-time.After(time.Second):
		t.Fatal("expected inbound message to be forwarded")
	}
}
