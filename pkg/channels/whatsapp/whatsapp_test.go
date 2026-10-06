package whatsapp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/xibodev/compa/v2/pkg/bus"
	"github.com/xibodev/compa/v2/pkg/config"
)

func TestCheckBridgeURL(t *testing.T) {
	ctx := context.Background()
	for _, ok := range []string{
		"ws://127.0.0.1:3001",
		"ws://localhost:3001",
		"ws://192.168.1.10:3001",
		"ws://[::1]:3001",
		"ws://100.101.102.103:3001",
		"wss://bridge.example.com",
	} {
		if err := checkBridgeURL(ctx, ok); err != nil {
			t.Errorf("checkBridgeURL(%q) = %v, want accepted", ok, err)
		}
	}
	for _, bad := range []string{
		"ws://8.8.8.8:3001",
		"http://127.0.0.1:3001",
		"",
	} {
		if err := checkBridgeURL(ctx, bad); err == nil {
			t.Errorf("checkBridgeURL(%q) accepted, want refused", bad)
		}
	}
}

func TestBridgeMediaPathOnlyAcceptsTempFiles(t *testing.T) {
	tempFile, err := os.CreateTemp("", "bridge-media-*.jpg")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	_, _ = tempFile.WriteString("image")
	tempFile.Close()
	defer os.Remove(tempFile.Name())

	if _, err := bridgeMediaPath(tempFile.Name(), 1024); err != nil {
		t.Fatalf("a downloaded file in the temp dir is accepted: %v", err)
	}
	if _, err := bridgeMediaPath(tempFile.Name(), 2); err == nil {
		t.Fatal("a file over the size limit is refused")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	outside := filepath.Join(home, ".compa", "config.json")
	if _, err := bridgeMediaPath(outside, 0); err == nil {
		t.Fatal("a path outside the temp directory is refused")
	}
	if _, err := bridgeMediaPath("relative/file.jpg", 0); err == nil {
		t.Fatal("a relative path is refused")
	}
	if _, err := bridgeMediaPath(filepath.Join(os.TempDir(), "..", "secret"), 0); err == nil {
		t.Fatal("a path escaping the temp directory is refused")
	}
}

func TestListenRedialsAfterReadError(t *testing.T) {
	upgrader := websocket.Upgrader{}
	var connections atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		if connections.Add(1) == 1 {
			// The first connection dies right away.
			conn.Close()
			return
		}
		_ = conn.WriteMessage(websocket.TextMessage,
			[]byte(`{"type":"message","id":"m1","from":"user1","chat":"user1","content":"after reconnect"}`))
		time.Sleep(time.Second)
		conn.Close()
	}))
	defer server.Close()

	messageBus := bus.NewMessageBus()
	settings := &config.WhatsAppSettings{BridgeURL: "ws" + strings.TrimPrefix(server.URL, "http")}
	ch, err := NewWhatsAppChannel(&config.Channel{Type: config.ChannelWhatsApp, Enabled: true}, settings, messageBus)
	if err != nil {
		t.Fatalf("NewWhatsAppChannel: %v", err)
	}
	if err := ch.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer ch.Stop(context.Background())

	select {
	case inbound := <-messageBus.InboundChan():
		if inbound.Content != "after reconnect" {
			t.Fatalf("content = %q", inbound.Content)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the channel should redial the bridge after a read error")
	}
}
