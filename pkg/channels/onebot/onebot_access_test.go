package onebot

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xibodev/compa/v3/pkg/bus"
	"github.com/xibodev/compa/v3/pkg/config"
	"github.com/xibodev/compa/v3/pkg/pairing"
)

func newAccessTestChannel(t *testing.T, downloads *int, allowFrom ...string) (*OneBotChannel, *bus.MessageBus) {
	t.Helper()
	messageBus := bus.NewMessageBus()
	ch, err := NewOneBotChannel(&config.Channel{Type: config.ChannelOneBot, Enabled: true, AllowFrom: allowFrom},
		&config.OneBotSettings{}, messageBus)
	if err != nil {
		t.Fatalf("NewOneBotChannel: %v", err)
	}
	ch.ctx = context.Background()
	ch.SetAccessPolicy(config.DMPolicyPairing, config.GroupPolicyAllowlist)
	tmp := filepath.Join(t.TempDir(), "image.png")
	if err := os.WriteFile(tmp, []byte("img"), 0o600); err != nil {
		t.Fatal(err)
	}
	ch.downloadFn = func(string, string) string {
		*downloads++
		return tmp
	}
	return ch, messageBus
}

func privateImageEvent(id, userID string) *oneBotRawEvent {
	return &oneBotRawEvent{
		PostType:    "message",
		MessageType: "private",
		MessageID:   json.RawMessage(`"` + id + `"`),
		UserID:      json.RawMessage(userID),
		SelfID:      json.RawMessage(`10001`),
		Message:     json.RawMessage(`[{"type":"image","data":{"url":"https://cdn.example.com/a.png","file":"a.png"}}]`),
	}
}

func TestHandleMessage_UnpairedSenderMediaIsNotFetched(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	downloads := 0
	ch, messageBus := newAccessTestChannel(t, &downloads, "111")

	ch.handleRawEvent(privateImageEvent("m1", "999"))

	if downloads != 0 {
		t.Fatalf("downloaded %d files for an unpaired sender", downloads)
	}
	select {
	case inbound := <-messageBus.InboundChan():
		t.Fatalf("an unpaired sender's message was published: %#v", inbound)
	case <-time.After(50 * time.Millisecond):
	}
	requests, err := pairing.List(home, "onebot")
	if err != nil || len(requests) != 1 || requests[0].SenderID != "onebot:999" {
		t.Fatalf("pairing requests = %+v, %v; want the stranger", requests, err)
	}
}

func TestHandleMessage_AllowListedGroupMemberIsAdmitted(t *testing.T) {
	downloads := 0
	ch, messageBus := newAccessTestChannel(t, &downloads, "111", "group:30003")

	// groupImageEvent's sender, 20002, is not listed; its group is.
	ch.handleRawEvent(groupImageEvent("m2", true))

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

func TestHandleMessage_OwnerIsPublishedAsOwner(t *testing.T) {
	downloads := 0
	ch, messageBus := newAccessTestChannel(t, &downloads, "111")

	ch.handleRawEvent(privateImageEvent("m3", "111"))

	select {
	case inbound := <-messageBus.InboundChan():
		if inbound.Context.ChatType != "direct" || !inbound.Context.SenderIsOwner || len(inbound.Media) != 1 {
			t.Fatalf("chat type %q, owner %v, media %v",
				inbound.Context.ChatType, inbound.Context.SenderIsOwner, inbound.Media)
		}
	case <-time.After(time.Second):
		t.Fatal("the owner's message was not published")
	}
}
