package onebot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xibodev/compa/v2/pkg/bus"
	"github.com/xibodev/compa/v2/pkg/config"
	"github.com/xibodev/compa/v2/pkg/media"
)

func TestParseMessageSegments_BlocksLoopbackInboundMediaURL(t *testing.T) {
	ch := &OneBotChannel{}
	store := media.NewFileMediaStore()

	raw := json.RawMessage(`[
		{"type":"text","data":{"text":"see attachment"}},
		{"type":"image","data":{"url":"http://127.0.0.1:8080/evil.png","file":"evil.png"}}
	]`)

	result := ch.parseMessageSegments(raw, 0, store, "onebot:test:msg1")

	if got := result.Text; got != "see attachment" {
		t.Fatalf("Text = %q, want %q", got, "see attachment")
	}
	if len(result.Media) != 0 {
		t.Fatalf("Media count = %d, want 0", len(result.Media))
	}
}

func TestParseMessageSegments_BlocksInboundMediaRedirectToLoopback(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("secret"))
	}))
	defer target.Close()

	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.String(), "http://example.com/evil.png"; got != want {
			t.Fatalf("proxy request URL = %q, want %q", got, want)
		}
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer proxy.Close()

	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("http_proxy", proxy.URL)
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("https_proxy", "")
	t.Setenv("ALL_PROXY", "")
	t.Setenv("all_proxy", "")
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")

	ch := &OneBotChannel{}
	store := media.NewFileMediaStore()

	raw := json.RawMessage(`[
		{"type":"text","data":{"text":"see attachment"}},
		{"type":"image","data":{"url":"http://example.com/evil.png","file":"evil.png"}}
	]`)

	result := ch.parseMessageSegments(raw, 0, store, "onebot:test:msg-redirect")

	if got := result.Text; got != "see attachment" {
		t.Fatalf("Text = %q, want %q", got, "see attachment")
	}
	if len(result.Media) != 0 {
		t.Fatalf("Media count = %d, want 0", len(result.Media))
	}
}

func TestParseMessageSegments_StoresDownloadedMediaRef(t *testing.T) {
	tmpDir := t.TempDir()
	localPath := filepath.Join(tmpDir, "image.png")
	if err := os.WriteFile(localPath, []byte("fake-image"), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}

	ch := &OneBotChannel{
		downloadFn: func(urlStr, filename string) string {
			if urlStr != "https://cdn.example.com/image.png" {
				t.Fatalf("download url = %q, want %q", urlStr, "https://cdn.example.com/image.png")
			}
			if filename != "image.png" {
				t.Fatalf("download filename = %q, want %q", filename, "image.png")
			}
			return localPath
		},
	}
	store := media.NewFileMediaStore()

	raw := json.RawMessage(`[
		{"type":"text","data":{"text":"see attachment"}},
		{"type":"image","data":{"url":"https://cdn.example.com/image.png","file":"image.png"}}
	]`)

	result := ch.parseMessageSegments(raw, 0, store, "onebot:test:msg2")

	if got := result.Text; got != "see attachment[image]" {
		t.Fatalf("Text = %q, want %q", got, "see attachment[image]")
	}
	if len(result.Media) != 1 {
		t.Fatalf("Media count = %d, want 1", len(result.Media))
	}
	if !strings.HasPrefix(result.Media[0], "media://") {
		t.Fatalf("media ref = %q, want media:// prefix", result.Media[0])
	}

	resolvedPath, meta, err := store.ResolveWithMeta(result.Media[0])
	if err != nil {
		t.Fatalf("ResolveWithMeta() error = %v", err)
	}
	if resolvedPath != localPath {
		t.Fatalf("resolved path = %q, want %q", resolvedPath, localPath)
	}
	if meta.Source != "onebot" {
		t.Fatalf("meta.Source = %q, want %q", meta.Source, "onebot")
	}
	if meta.Filename != "image.png" {
		t.Fatalf("meta.Filename = %q, want %q", meta.Filename, "image.png")
	}
}

func TestParseSegments_AtAllIsNotAMention(t *testing.T) {
	raw := json.RawMessage(`[{"type":"at","data":{"qq":"all"}},{"type":"text","data":{"text":" meeting at 5"}}]`)
	if parseSegments(raw, 10001).IsBotMentioned {
		t.Fatal("@all addresses everyone, not the bot")
	}
	raw = json.RawMessage(`[{"type":"at","data":{"qq":"10001"}},{"type":"text","data":{"text":" hi"}}]`)
	if !parseSegments(raw, 10001).IsBotMentioned {
		t.Fatal("@bot is a mention")
	}
}

func newGroupTestChannel(t *testing.T, gt config.GroupTriggerConfig, downloads *int) (*OneBotChannel, *bus.MessageBus) {
	t.Helper()
	messageBus := bus.NewMessageBus()
	ch, err := NewOneBotChannel(&config.Channel{Type: config.ChannelOneBot, Enabled: true, GroupTrigger: gt},
		&config.OneBotSettings{}, messageBus)
	if err != nil {
		t.Fatalf("NewOneBotChannel: %v", err)
	}
	ch.ctx = context.Background()
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

func groupImageEvent(id string, mention bool) *oneBotRawEvent {
	segments := `[{"type":"image","data":{"url":"https://cdn.example.com/a.png","file":"a.png"}}]`
	if mention {
		segments = `[{"type":"at","data":{"qq":"10001"}},{"type":"image","data":{"url":"https://cdn.example.com/a.png","file":"a.png"}}]`
	}
	return &oneBotRawEvent{
		PostType:    "message",
		MessageType: "group",
		MessageID:   json.RawMessage(`"` + id + `"`),
		UserID:      json.RawMessage(`20002`),
		GroupID:     json.RawMessage(`30003`),
		SelfID:      json.RawMessage(`10001`),
		Message:     json.RawMessage(segments),
	}
}

func TestHandleMessage_GroupTriggerBeforeDownload(t *testing.T) {
	downloads := 0
	ch, messageBus := newGroupTestChannel(t, config.GroupTriggerConfig{MentionOnly: true}, &downloads)

	ch.handleRawEvent(groupImageEvent("m1", false))
	select {
	case inbound := <-messageBus.InboundChan():
		t.Fatalf("an unmentioned group message is ignored: %#v", inbound)
	case <-time.After(50 * time.Millisecond):
	}
	if downloads != 0 {
		t.Fatalf("media of an ignored message was downloaded %d times", downloads)
	}

	ch.handleRawEvent(groupImageEvent("m2", true))
	select {
	case inbound := <-messageBus.InboundChan():
		if inbound.Content != "[image]" || len(inbound.Media) != 1 {
			t.Fatalf("content = %q, media = %v", inbound.Content, inbound.Media)
		}
	case <-time.After(time.Second):
		t.Fatal("a mentioned group message with an image is forwarded")
	}
	if downloads != 1 {
		t.Fatalf("downloads = %d, want 1", downloads)
	}
}

// Each media tag stays where its segment was, between the right words, and
// the refs follow the tags; a download that failed leaves neither.
func TestParseMessageSegments_KeepsMediaInPlace(t *testing.T) {
	dir := t.TempDir()
	ch := &OneBotChannel{
		downloadFn: func(urlStr, filename string) string {
			if strings.HasSuffix(urlStr, "a.png") {
				return ""
			}
			localPath := filepath.Join(dir, filename)
			if err := os.WriteFile(localPath, []byte("fake-"+filename), 0o600); err != nil {
				t.Error(err)
				return ""
			}
			return localPath
		},
	}

	raw := json.RawMessage(`[
		{"type":"text","data":{"text":" 看这个 "}},
		{"type":"image","data":{"url":"https://cdn.example.com/a.png","file":"a.png"}},
		{"type":"text","data":{"text":"中间"}},
		{"type":"image","data":{"url":"https://cdn.example.com/b.png","file":"b.png"}},
		{"type":"text","data":{"text":"after"}},
		{"type":"video","data":{"url":"https://cdn.example.com/c.mp4","file":"c.mp4"}}
	]`)
	store := media.NewFileMediaStore()
	result := ch.parseMessageSegments(raw, 0, store, "onebot:test:order")

	if want := "看这个 中间[image]after[video]"; result.Text != want {
		t.Fatalf("Text = %q, want %q", result.Text, want)
	}
	var names []string
	for _, ref := range result.Media {
		_, meta, err := store.ResolveWithMeta(ref)
		if err != nil {
			t.Fatalf("ResolveWithMeta(%q): %v", ref, err)
		}
		names = append(names, meta.Filename)
	}
	if got := strings.Join(names, ","); got != "b.png,c.mp4" {
		t.Fatalf("media = %s, want b.png,c.mp4", got)
	}
}

// A group trigger takes its prefix off the text; the tags still go where
// their segments were.
func TestHandleMessage_MediaStaysInPlaceAfterTheGroupTrigger(t *testing.T) {
	messageBus := bus.NewMessageBus()
	ch, err := NewOneBotChannel(&config.Channel{
		Type: config.ChannelOneBot, Enabled: true, AllowFrom: config.FlexibleStringSlice{"group:30003"},
		GroupTrigger: config.GroupTriggerConfig{Prefixes: []string{"/ai"}},
	}, &config.OneBotSettings{}, messageBus)
	if err != nil {
		t.Fatalf("NewOneBotChannel: %v", err)
	}
	ch.ctx = context.Background()
	ch.SetAccessPolicy(config.DMPolicyPairing, config.GroupPolicyAllowlist)
	localPath := filepath.Join(t.TempDir(), "a.png")
	if err := os.WriteFile(localPath, []byte("img"), 0o600); err != nil {
		t.Fatal(err)
	}
	ch.downloadFn = func(string, string) string { return localPath }

	ch.handleRawEvent(&oneBotRawEvent{
		PostType:    "message",
		MessageType: "group",
		MessageID:   json.RawMessage(`"m9"`),
		UserID:      json.RawMessage(`20002`),
		GroupID:     json.RawMessage(`30003`),
		SelfID:      json.RawMessage(`10001`),
		RawMessage:  "/ai before [CQ:image,file=a.png,url=https://cdn.example.com/a.png] after",
		Message: json.RawMessage(`[
			{"type":"text","data":{"text":"/ai before "}},
			{"type":"image","data":{"url":"https://cdn.example.com/a.png","file":"a.png"}},
			{"type":"text","data":{"text":" after"}}
		]`),
	})

	select {
	case inbound := <-messageBus.InboundChan():
		if want := "before [image] after"; inbound.Content != want {
			t.Fatalf("published %q, want %q", inbound.Content, want)
		}
	case <-time.After(time.Second):
		t.Fatal("the group message was not published")
	}
}
