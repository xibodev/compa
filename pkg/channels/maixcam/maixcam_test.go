//go:build paused_channels

package maixcam

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/xibodev/compa/v3/pkg/bus"
	"github.com/xibodev/compa/v3/pkg/channels"
	"github.com/xibodev/compa/v3/pkg/config"
	"github.com/xibodev/compa/v3/pkg/pairing"
)

func newTestMaixCam(t *testing.T, settings config.MaixCamSettings) (*MaixCamChannel, *bus.MessageBus) {
	t.Helper()
	messageBus := bus.NewMessageBus()
	ch, err := NewMaixCamChannel(&config.Channel{Type: config.ChannelMaixCam, Enabled: true}, &settings, messageBus)
	if err != nil {
		t.Fatalf("NewMaixCamChannel: %v", err)
	}
	ch.ctx, ch.cancel = context.WithCancel(context.Background())
	t.Cleanup(ch.cancel)
	ch.SetRunning(true)
	return ch, messageBus
}

func TestListenAddressDefaultsToLoopback(t *testing.T) {
	ch, _ := newTestMaixCam(t, config.MaixCamSettings{})
	if got := ch.listenAddress(); got != "127.0.0.1:18792" {
		t.Fatalf("listenAddress() = %q, want 127.0.0.1:18792", got)
	}

	ch, _ = newTestMaixCam(t, config.MaixCamSettings{Host: "0.0.0.0", Port: 9000})
	if got := ch.listenAddress(); got != "0.0.0.0:9000" {
		t.Fatalf("listenAddress() = %q, want the configured address", got)
	}
}

func TestAuthorizedComparesToken(t *testing.T) {
	open, _ := newTestMaixCam(t, config.MaixCamSettings{})
	if !open.authorized("") || !open.authorized("anything") {
		t.Fatal("without a token every message is accepted")
	}

	settings := config.MaixCamSettings{}
	settings.Token = *config.NewSecureString("s3cret")
	locked, _ := newTestMaixCam(t, settings)
	if !locked.authorized("s3cret") {
		t.Fatal("the configured token is accepted")
	}
	for _, token := range []string{"", "s3cre", "s3cret!", "S3CRET"} {
		if locked.authorized(token) {
			t.Fatalf("token %q must be refused", token)
		}
	}
}

func personDetected(token string) string {
	msg := map[string]any{
		"type":      "person_detected",
		"timestamp": 1,
		"data":      map[string]any{"class_name": "person", "score": 0.9},
	}
	if token != "" {
		msg["token"] = token
	}
	b, _ := json.Marshal(msg)
	return string(b)
}

// serve runs handleConnection on one end of a pipe and returns the other.
func serve(ch *MaixCamChannel) (client net.Conn, done chan struct{}) {
	server, client := net.Pipe()
	ch.clientsMux.Lock()
	ch.clients[server] = deviceChatID(server.RemoteAddr())
	ch.clientsMux.Unlock()
	done = make(chan struct{})
	go func() {
		ch.handleConnection(server)
		close(done)
	}()
	return client, done
}

func waitClosed(t *testing.T, done chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("connection should have been closed")
	}
}

func noInbound(t *testing.T, messageBus *bus.MessageBus) {
	t.Helper()
	select {
	case inbound := <-messageBus.InboundChan():
		t.Fatalf("unexpected inbound message: %#v", inbound)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestHandleConnectionClosesOnWrongToken(t *testing.T) {
	settings := config.MaixCamSettings{}
	settings.Token = *config.NewSecureString("s3cret")
	ch, messageBus := newTestMaixCam(t, settings)

	client, done := serve(ch)
	defer client.Close()
	if _, err := io.WriteString(client, personDetected("guess")); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitClosed(t, done)
	noInbound(t, messageBus)
}

func TestHandleConnectionAcceptsToken(t *testing.T) {
	settings := config.MaixCamSettings{}
	settings.Token = *config.NewSecureString("s3cret")
	ch, messageBus := newTestMaixCam(t, settings)

	client, _ := serve(ch)
	defer client.Close()
	if _, err := io.WriteString(client, personDetected("s3cret")); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case inbound := <-messageBus.InboundChan():
		if !strings.Contains(inbound.Content, "Person detected") {
			t.Fatalf("content = %q", inbound.Content)
		}
		if inbound.Context.ChatType != "direct" {
			t.Fatalf("chat type = %q", inbound.Context.ChatType)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected the authorized detection to be published")
	}
}

func TestHandleConnectionClosesOnOversizedMessage(t *testing.T) {
	ch, messageBus := newTestMaixCam(t, config.MaixCamSettings{})

	client, done := serve(ch)
	defer client.Close()
	go func() {
		// One JSON value larger than the limit, never terminated.
		_, _ = io.WriteString(client, `{"type":"status","tips":"`+strings.Repeat("a", maxMessageBytes+1024))
	}()
	waitClosed(t, done)
	noInbound(t, messageBus)
}

func TestSendRepliesOnlyToTheAskingDevice(t *testing.T) {
	ch, _ := newTestMaixCam(t, config.MaixCamSettings{})

	askingServer, askingClient := net.Pipe()
	otherServer, otherClient := net.Pipe()
	defer askingClient.Close()
	defer otherClient.Close()
	ch.clients[askingServer] = "192.168.1.20"
	ch.clients[otherServer] = "192.168.1.21"

	got := make(chan string, 1)
	go func() {
		buf := make([]byte, 4096)
		n, _ := askingClient.Read(buf)
		got <- string(buf[:n])
	}()
	otherGot := make(chan struct{}, 1)
	go func() {
		buf := make([]byte, 4096)
		if n, _ := otherClient.Read(buf); n > 0 {
			otherGot <- struct{}{}
		}
	}()

	if _, err := ch.Send(context.Background(), bus.OutboundMessage{ChatID: "192.168.1.20", Content: "hello"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	select {
	case reply := <-got:
		if !strings.Contains(reply, `"message":"hello"`) {
			t.Fatalf("reply = %q", reply)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the asking device should receive the reply")
	}
	select {
	case <-otherGot:
		t.Fatal("another device must not receive the reply")
	case <-time.After(50 * time.Millisecond):
	}

	if _, err := ch.Send(context.Background(), bus.OutboundMessage{ChatID: "10.0.0.9", Content: "x"}); err == nil {
		t.Fatal("a reply for a device that is not connected fails")
	}
}

func TestPersonDetectionFollowsAccessPolicy(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	ch, messageBus := newTestMaixCam(t, config.MaixCamSettings{})
	ch.SetAccessPolicy(config.DMPolicyPairing, config.GroupPolicyAllowlist)

	var msg MaixCamMessage
	if err := json.Unmarshal([]byte(personDetected("")), &msg); err != nil {
		t.Fatal(err)
	}
	ch.processMessage(msg, "192.168.1.20")
	noInbound(t, messageBus)
	requests, err := pairing.List(home, "maixcam")
	if err != nil || len(requests) != 1 || requests[0].SenderID != "maixcam:maixcam" {
		t.Fatalf("pairing requests = %+v, %v; want the device", requests, err)
	}

	listed, listedBus := newTestMaixCam(t, config.MaixCamSettings{})
	listed.BaseChannel = channels.NewBaseChannel("maixcam", listed.config, listedBus, []string{"maixcam:maixcam"})
	listed.SetAccessPolicy(config.DMPolicyPairing, config.GroupPolicyAllowlist)
	listed.processMessage(msg, "192.168.1.20")
	select {
	case inbound := <-listedBus.InboundChan():
		if !inbound.Context.SenderIsOwner || inbound.ChatID != "192.168.1.20" {
			t.Fatalf("owner = %v, chat = %q", inbound.Context.SenderIsOwner, inbound.ChatID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a listed device's detection was not published")
	}
}
