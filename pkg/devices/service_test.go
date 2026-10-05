package devices

import (
	"testing"
	"time"

	"github.com/xibodev/compa/pkg/bus"
	"github.com/xibodev/compa/pkg/devices/events"
	"github.com/xibodev/compa/pkg/state"
)

func newTestService(t *testing.T) (*Service, *state.Manager, *bus.MessageBus) {
	t.Helper()
	sm := state.NewManager(t.TempDir())
	msgBus := bus.NewMessageBus()
	t.Cleanup(msgBus.Close)
	s := NewService(Config{}, sm)
	s.SetBus(msgBus)
	return s, sm, msgBus
}

func usbEvent() *events.DeviceEvent {
	return &events.DeviceEvent{Action: events.ActionAdd, Kind: events.KindUSB, Vendor: "Acme", Product: "Stick"}
}

func TestNotificationGoesToTheOwnerChatNotTheLastChat(t *testing.T) {
	s, sm, msgBus := newTestService(t)
	if err := sm.SetLastChannel("discord:stranger"); err != nil {
		t.Fatalf("SetLastChannel: %v", err)
	}
	if err := sm.SetOwnerChat("telegram", "owner-chat"); err != nil {
		t.Fatalf("SetOwnerChat: %v", err)
	}

	s.sendNotification(usbEvent())

	select {
	case out := <-msgBus.OutboundChan():
		if out.Context.Channel != "telegram" || out.Context.ChatID != "owner-chat" {
			t.Fatalf("notification went to %s/%s, want telegram/owner-chat", out.Context.Channel, out.Context.ChatID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no notification was sent")
	}
}

func TestNotificationSkippedWithoutAnOwnerChat(t *testing.T) {
	s, sm, msgBus := newTestService(t)
	if err := sm.SetLastChannel("discord:stranger"); err != nil {
		t.Fatalf("SetLastChannel: %v", err)
	}

	s.sendNotification(usbEvent())

	select {
	case out := <-msgBus.OutboundChan():
		t.Fatalf("notification sent to %s/%s with no owner chat known", out.Context.Channel, out.Context.ChatID)
	case <-time.After(100 * time.Millisecond):
	}
}
