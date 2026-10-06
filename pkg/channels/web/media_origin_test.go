package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xibodev/compa/v2/pkg/bus"
	"github.com/xibodev/compa/v2/pkg/config"
	"github.com/xibodev/compa/v2/pkg/media"
)

// serveStoredMedia stores content under filename with contentType and
// downloads it through the channel.
func serveStoredMedia(t *testing.T, filename, contentType, content string) *httptest.ResponseRecorder {
	t.Helper()
	ch := newTestWebChannel(t)
	store := media.NewFileMediaStore()
	ch.SetMediaStore(store)
	if err := ch.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() { _ = ch.Stop(context.Background()) })

	localPath := filepath.Join(t.TempDir(), "stored")
	if err := os.WriteFile(localPath, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	ref, err := store.Store(localPath, media.MediaMeta{Filename: filename, ContentType: contentType}, "test-scope")
	if err != nil {
		t.Fatalf("Store() error = %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/web/media/"+strings.TrimPrefix(ref, "media://"), nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	ch.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	return rec
}

// An HTML file a model named "photo.png" must not render on the dashboard's
// origin: the stored type decides, and HTML downloads.
func TestMediaDownloadDecidesInlineByStoredTypeNotName(t *testing.T) {
	for _, tc := range []struct {
		filename, contentType string
		inline                bool
	}{
		{"photo.png", "text/html", false},
		{"photo.png", "image/svg+xml", false},
		{"photo.jpg", "application/xml", false},
		{"photo.png", "application/octet-stream", false},
		{"page.html", "text/html; charset=utf-8", false},
		{"report.txt", "text/plain", false},
		{"photo.png", "image/png", true},
		{"anything", "image/jpeg", true},
		{"clip", "video/mp4", true},
		{"voice", "audio/ogg; codecs=opus", true},
	} {
		rec := serveStoredMedia(t, tc.filename, tc.contentType, "<html><script>alert(1)</script></html>")
		disposition := rec.Header().Get("Content-Disposition")
		if got := strings.HasPrefix(disposition, "inline"); got != tc.inline {
			t.Errorf("%s as %s: Content-Disposition = %q, want inline=%t", tc.filename, tc.contentType, disposition, tc.inline)
		}
		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s as %s: X-Content-Type-Options = %q, want nosniff", tc.filename, tc.contentType, got)
		}
		if got := rec.Header().Get("Content-Security-Policy"); got != MediaContentSecurityPolicy {
			t.Errorf("%s as %s: Content-Security-Policy = %q, want %q", tc.filename, tc.contentType, got, MediaContentSecurityPolicy)
		}
	}
}

func TestWebSocketOriginCheckWithoutAllowOrigins(t *testing.T) {
	bc := &config.Channel{Type: config.ChannelWeb, Enabled: true}
	cfg := &config.WebChatSettings{}
	cfg.SetToken("test-token")
	ch, err := NewWebChannel(bc, cfg, bus.NewMessageBus())
	if err != nil {
		t.Fatalf("NewWebChannel: %v", err)
	}

	for _, tc := range []struct {
		origin string
		want   bool
	}{
		{"", true},
		{"http://127.0.0.1:18790", true},
		{"http://evil.example", false},
		{"http://127.0.0.1:18800", false},
		{"null", false},
	} {
		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18790/web/ws", nil)
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		if got := ch.upgrader.CheckOrigin(req); got != tc.want {
			t.Errorf("CheckOrigin(Origin %q) = %t, want %t", tc.origin, got, tc.want)
		}
	}
}

func TestWebSocketOriginCheckHonorsAllowOrigins(t *testing.T) {
	bc := &config.Channel{Type: config.ChannelWeb, Enabled: true}
	cfg := &config.WebChatSettings{AllowOrigins: []string{"https://app.example"}}
	cfg.SetToken("test-token")
	ch, err := NewWebChannel(bc, cfg, bus.NewMessageBus())
	if err != nil {
		t.Fatalf("NewWebChannel: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18790/web/ws", nil)
	req.Header.Set("Origin", "https://app.example")
	if !ch.upgrader.CheckOrigin(req) {
		t.Fatal("CheckOrigin rejected an allowed origin")
	}
	req.Header.Set("Origin", "https://other.example")
	if ch.upgrader.CheckOrigin(req) {
		t.Fatal("CheckOrigin accepted an origin allow_origins does not list")
	}
}
