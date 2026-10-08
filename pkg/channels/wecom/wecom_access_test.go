//go:build paused_channels

package wecom

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xibodev/compa/v3/pkg/bus"
	"github.com/xibodev/compa/v3/pkg/config"
	"github.com/xibodev/compa/v3/pkg/pairing"
)

func TestDispatchIncoming_UnpairedSenderGetsNoReplyAndNoDownload(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)

	var fetches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
	}))
	defer server.Close()

	messageBus := bus.NewMessageBus()
	ch := newTestWeComChannel(t, messageBus)
	ch.SetAccessPolicy(config.DMPolicyPairing, config.GroupPolicyAllowlist)
	var commands atomic.Int32
	ch.commandSend = func(cmd wecomCommand, _ time.Duration) (wecomEnvelope, error) {
		commands.Add(1)
		return wecomTestAck(nil), nil
	}

	msg := wecomIncomingMessage{MsgID: "msg-1", ChatType: "single", MsgType: "image"}
	msg.From.UserID = "stranger"
	msg.Image = &struct {
		URL    string `json:"url"`
		AESKey string `json:"aeskey,omitempty"`
	}{URL: server.URL + "/image"}

	if err := ch.dispatchIncoming("req-1", msg); err != nil {
		t.Fatalf("dispatchIncoming() error = %v", err)
	}
	if n := fetches.Load(); n != 0 {
		t.Fatalf("fetched an unpaired sender's media %d times", n)
	}
	if n := commands.Load(); n != 0 {
		t.Fatalf("answered an unpaired sender with %d commands", n)
	}
	select {
	case inbound := <-messageBus.InboundChan():
		t.Fatalf("an unpaired sender's message was published: %#v", inbound)
	default:
	}
	requests, err := pairing.List(home, "wecom")
	if err != nil || len(requests) != 1 || requests[0].SenderID != "wecom:stranger" {
		t.Fatalf("pairing requests = %+v, %v; want the stranger", requests, err)
	}
}

func TestDispatchIncoming_MediaMessageWithoutMediaObject(t *testing.T) {
	messageBus := bus.NewMessageBus()
	ch := newTestWeComChannel(t, messageBus)
	ch.commandSend = func(cmd wecomCommand, _ time.Duration) (wecomEnvelope, error) {
		return wecomTestAck(nil), nil
	}

	for _, msgType := range []string{"image", "file", "video"} {
		msg := wecomIncomingMessage{MsgID: "msg-" + msgType, ChatType: "single", MsgType: msgType}
		msg.From.UserID = "user-1"
		// Used to dereference the missing object and crash the kernel.
		if err := ch.dispatchIncoming("req-"+msgType, msg); err == nil {
			t.Errorf("%s message without its media object: want an error", msgType)
		}
	}
}
