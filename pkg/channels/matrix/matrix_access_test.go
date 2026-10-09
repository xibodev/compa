//go:build paused_channels

package matrix

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/xibodev/compa/v4/pkg/bus"
	"github.com/xibodev/compa/v4/pkg/channels"
	"github.com/xibodev/compa/v4/pkg/config"
	"github.com/xibodev/compa/v4/pkg/media"
	"github.com/xibodev/compa/v4/pkg/pairing"
	"github.com/xibodev/compa/v4/pkg/utils"
)

// newAccessTestChannel returns a channel whose homeserver counts the
// requests it gets and fails them.
func newAccessTestChannel(t *testing.T, allowFrom ...string) (*MatrixChannel, *bus.MessageBus, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, `{"errcode":"M_UNKNOWN"}`, http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	client, err := mautrix.NewClient(server.URL, id.UserID("@compa:matrix.test"), "")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	messageBus := bus.NewMessageBus()
	ch := &MatrixChannel{
		BaseChannel:   channels.NewBaseChannel("matrix", nil, messageBus, allowFrom),
		bc:            &config.Channel{},
		client:        client,
		config:        &config.MatrixSettings{JoinOnInvite: true},
		ctx:           context.Background(),
		roomKindCache: newRoomKindCache(roomKindCacheMaxEntries, roomKindCacheTTL),
	}
	ch.SetAccessPolicy(config.DMPolicyPairing, config.GroupPolicyAllowlist)
	return ch, messageBus, &requests
}

func TestHandleMessageEvent_UnpairedSenderMediaIsNotDownloaded(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	ch, messageBus, requests := newAccessTestChannel(t, "matrix:@owner:matrix.test")
	ch.roomKindCache.set("!dm:matrix.test", false, time.Now())

	ch.handleMessageEvent(context.Background(), &event.Event{
		Type:      event.EventMessage,
		Sender:    id.UserID("@stranger:matrix.test"),
		RoomID:    id.RoomID("!dm:matrix.test"),
		ID:        id.EventID("$1"),
		Timestamp: time.Now().UnixMilli(),
		Content: event.Content{Parsed: &event.MessageEventContent{
			MsgType: event.MsgImage,
			Body:    "image.png",
			URL:     id.ContentURIString("mxc://matrix.test/abc123"),
		}},
	})

	if n := requests.Load(); n != 0 {
		t.Fatalf("made %d homeserver requests for an unpaired sender", n)
	}
	select {
	case msg := <-messageBus.InboundChan():
		t.Fatalf("an unpaired sender's message was published: %#v", msg)
	default:
	}
	recorded, err := pairing.List(home, "matrix")
	if err != nil || len(recorded) != 1 || recorded[0].SenderID != "matrix:@stranger:matrix.test" {
		t.Fatalf("pairing requests = %+v, %v; want the stranger", recorded, err)
	}
}

func TestInvitationAdmitted(t *testing.T) {
	ch, _, _ := newAccessTestChannel(t, "matrix:@owner:matrix.test", "matrix:!listed:matrix.test")

	if !ch.invitationAdmitted("@owner:matrix.test", "!any:matrix.test") {
		t.Error("the owner's invite is accepted")
	}
	if ch.invitationAdmitted("@stranger:matrix.test", "!any:matrix.test") {
		t.Error("a stranger's invite is ignored")
	}
	if !ch.invitationAdmitted("@stranger:matrix.test", "!listed:matrix.test") {
		t.Error("an invite to a listed room is accepted")
	}

	ch.SetAccessPolicy(config.DMPolicyOpen, config.GroupPolicyOpen)
	if !ch.invitationAdmitted("@stranger:matrix.test", "!any:matrix.test") {
		t.Error("open policies accept every invite, as before")
	}
}

func TestIsGroupRoom_LookupFailureIsGroup(t *testing.T) {
	ch, _, requests := newAccessTestChannel(t)

	if !ch.isGroupRoom(context.Background(), "!unknown:matrix.test") {
		t.Error("a room whose members can't be listed is treated as a group")
	}
	if requests.Load() == 0 {
		t.Error("expected a member lookup")
	}
	if _, cached := ch.roomKindCache.get("!unknown:matrix.test", time.Now()); cached {
		t.Error("a failed lookup is not cached")
	}
}

func TestSplitForEvent_KeepsEventsUnderByteLimit(t *testing.T) {
	ch := &MatrixChannel{config: &config.MatrixSettings{}}
	// 30,000 three-byte characters: under the 65,536-character split, but
	// about 90 KB in the body alone.
	text := strings.Repeat("漢字", 15000)

	chunks := ch.splitForEvent(text)
	if len(chunks) < 2 {
		t.Fatalf("got %d chunks, want the text split", len(chunks))
	}
	if strings.Join(chunks, "") != text {
		t.Error("splitting lost text")
	}
	for i, chunk := range chunks {
		if n := ch.eventBytes(chunk); n > maxEventContentBytes {
			t.Errorf("chunk %d encodes to %d bytes, over %d", i, n, maxEventContentBytes)
		}
	}
	if got := ch.splitForEvent("short"); len(got) != 1 || got[0] != "short" {
		t.Errorf("short text = %q, want unchanged", got)
	}
}

// A message sent in parts that fails after a part was delivered is not sent
// again: that would repeat the delivered parts.
func TestSendFailingAfterADeliveredPartIsNotRetried(t *testing.T) {
	var sends atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch {
		case strings.Contains(r.URL.Path, "/redact/"):
			_, _ = w.Write([]byte(`{"event_id":"$redacted"}`))
		case strings.Contains(string(body), "m.replace"):
			// The progress message can't take the reply, so it's sent anew.
			http.Error(w, `{"errcode":"M_UNKNOWN"}`, http.StatusInternalServerError)
		case sends.Add(1) == 1:
			_, _ = w.Write([]byte(`{"event_id":"$1"}`))
		default:
			http.Error(w, `{"errcode":"M_UNKNOWN"}`, http.StatusInternalServerError)
		}
	}))
	t.Cleanup(server.Close)
	client, err := mautrix.NewClient(server.URL, id.UserID("@compa:matrix.test"), "")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ch := &MatrixChannel{
		BaseChannel: channels.NewBaseChannel("matrix", nil, bus.NewMessageBus(), nil),
		client:      client,
		config:      &config.MatrixSettings{},
		progress:    channels.NewToolFeedbackAnimator(nil),
	}
	ch.SetRunning(true)
	ch.RecordToolFeedbackMessage("!room:matrix.test", "$feedback", "working")

	_, err = ch.Send(context.Background(), bus.OutboundMessage{
		ChatID:  "!room:matrix.test",
		Content: strings.Repeat("漢字", 15000),
	})
	if !errors.Is(err, channels.ErrSendFailed) || errors.Is(err, channels.ErrTemporary) {
		t.Fatalf("Send() error = %v, want a failure that is not retried", err)
	}
	if n := sends.Load(); n != 2 {
		t.Fatalf("sent %d parts, want the delivered one and the failed one", n)
	}
	if _, tracked := ch.currentToolFeedbackMessage("!room:matrix.test"); tracked {
		t.Error("the progress message is still tracked after part of the reply went out")
	}
}

func TestDownloadMedia_RefusesDeclaredOversize(t *testing.T) {
	ch, _, requests := newAccessTestChannel(t)
	_, err := ch.downloadMedia(context.Background(), &event.MessageEventContent{
		MsgType: event.MsgFile,
		Body:    "big.bin",
		URL:     id.ContentURIString("mxc://matrix.test/big"),
		Info:    &event.FileInfo{Size: config.DefaultMaxMediaSize + 1},
	}, "file")
	if err == nil {
		t.Fatal("want an error for a file over the media size limit")
	}
	if requests.Load() != 0 {
		t.Error("an oversized file is not requested")
	}
}

// Media that fails after a part was delivered is not sent again either.
func TestSendMediaFailingAfterADeliveredPartIsNotRetried(t *testing.T) {
	var uploads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/upload") {
			_, _ = w.Write([]byte(`{"event_id":"$1"}`))
			return
		}
		if uploads.Add(1) > 1 {
			http.Error(w, `{"errcode":"M_UNKNOWN"}`, http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"content_uri":"mxc://matrix.test/1"}`))
	}))
	t.Cleanup(server.Close)
	client, err := mautrix.NewClient(server.URL, id.UserID("@compa:matrix.test"), "")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	store := media.NewFileMediaStore()
	ch := &MatrixChannel{
		BaseChannel: channels.NewBaseChannel("matrix", nil, bus.NewMessageBus(), nil),
		client:      client,
		config:      &config.MatrixSettings{},
		progress:    channels.NewToolFeedbackAnimator(nil),
	}
	ch.SetRunning(true)
	ch.SetMediaStore(store)
	// Part of the reply goes out, so the progress message is done.
	ch.RecordToolFeedbackMessage("!room:matrix.test", "$feedback", "working")
	var parts []bus.MediaPart
	for _, name := range []string{"a.txt", "b.txt"} {
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
		ref, err := store.Store(path, media.MediaMeta{Filename: name, ContentType: "text/plain"}, "scope")
		if err != nil {
			t.Fatal(err)
		}
		parts = append(parts, bus.MediaPart{Ref: ref, Type: "file", Filename: name})
	}

	_, err = ch.SendMedia(context.Background(), bus.OutboundMediaMessage{ChatID: "!room:matrix.test", Parts: parts})
	if !errors.Is(err, channels.ErrSendFailed) || errors.Is(err, channels.ErrTemporary) {
		t.Fatalf("SendMedia() error = %v, want a failure that is not retried", err)
	}
	if n := uploads.Load(); n != 2 {
		t.Fatalf("uploaded %d parts, want the delivered one and the failed one", n)
	}
	if _, tracked := ch.currentToolFeedbackMessage("!room:matrix.test"); tracked {
		t.Error("the progress message is still tracked after part of the reply went out")
	}
}

// sizeTestChannel is a Matrix channel whose homeserver records the size of
// each event it gets and answers with status.
func sizeTestChannel(t *testing.T, status int, body string) (*MatrixChannel, *[]int) {
	t.Helper()
	var sizes []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		sizes = append(sizes, len(data))
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	client, err := mautrix.NewClient(server.URL, id.UserID("@compa:matrix.test"), "")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ch := &MatrixChannel{
		BaseChannel: channels.NewBaseChannel("matrix", nil, bus.NewMessageBus(), nil),
		client:      client,
		config:      &config.MatrixSettings{},
		progress:    channels.NewToolFeedbackAnimator(nil),
	}
	ch.SetRunning(true)
	return ch, &sizes
}

// Tool feedback goes out as one event, and its animated edits replace it, so
// it is shortened until each of them fits one event.
func TestToolFeedbackFitsOneEvent(t *testing.T) {
	ch, sizes := sizeTestChannel(t, http.StatusOK, `{"event_id":"$1"}`)
	feedback := "running a tool\n" + strings.Repeat("ü", 70000)

	_, err := ch.Send(context.Background(), bus.OutboundMessage{
		ChatID:  "!room:matrix.test",
		Content: feedback,
		Context: bus.InboundContext{Raw: map[string]string{"message_kind": "tool_feedback"}},
	})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if len(*sizes) != 1 || (*sizes)[0] > maxEventContentBytes {
		t.Fatalf("sent events of %v bytes, want one of at most %d", *sizes, maxEventContentBytes)
	}
	fitted := ch.PrepareToolFeedbackMessageContent(feedback)
	if !strings.HasPrefix(fitted, "running a tool") {
		t.Fatalf("the fitted feedback lost its first line: %.40q", fitted)
	}
	// An animated edit, of an event with an ID as long as Matrix allows, fits.
	longID := "$" + strings.Repeat("a", maxEventIDBytes-1)
	if err := ch.EditMessage(context.Background(), "!room:matrix.test", longID,
		channels.InitialAnimatedToolFeedbackContent(fitted)); err != nil {
		t.Fatalf("EditMessage() of the progress message error = %v", err)
	}

	// Truncation can be switched off for debugging; the feedback fits anyway.
	utils.SetDisableTruncation(true)
	t.Cleanup(func() { utils.SetDisableTruncation(false) })
	fitted = ch.PrepareToolFeedbackMessageContent(strings.Repeat("ü", 70000))
	if n := ch.editBytes(channels.InitialAnimatedToolFeedbackContent(fitted)); n > maxEventContentBytes {
		t.Fatalf("with truncation off, an edit of the progress message is %d bytes, more than %d", n, maxEventContentBytes)
	}
	if n := len([]rune(fitted)); n < 1000 {
		t.Fatalf("with truncation off, the feedback was cut to %d characters", n)
	}
}

// An edit too large for one event fails at once, without retries, so the
// caller sends the text anew.
func TestEditTooLargeForOneEventFails(t *testing.T) {
	ch, sizes := sizeTestChannel(t, http.StatusOK, `{"event_id":"$1"}`)

	err := ch.EditMessage(context.Background(), "!room:matrix.test", "$old", strings.Repeat("ü", 40000))
	if !errors.Is(err, channels.ErrSendFailed) {
		t.Fatalf("EditMessage() error = %v, want a failure that is not retried", err)
	}
	if len(*sizes) != 0 {
		t.Fatalf("sent %d events, want none", len(*sizes))
	}
}

// The homeserver refusing an event as too large isn't retried either: not a
// message, an edit, nor media.
func TestSendRefusedAsTooLargeIsNotRetried(t *testing.T) {
	ch, _ := sizeTestChannel(t, http.StatusRequestEntityTooLarge, `{"errcode":"M_TOO_LARGE","error":"too large"}`)
	notRetried := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, channels.ErrSendFailed) || errors.Is(err, channels.ErrTemporary) {
			t.Errorf("%s error = %v, want a failure that is not retried", what, err)
		}
	}

	_, err := ch.Send(context.Background(), bus.OutboundMessage{ChatID: "!room:matrix.test", Content: "hello"})
	notRetried("Send()", err)
	notRetried("EditMessage()", ch.EditMessage(context.Background(), "!room:matrix.test", "$old", "hello"))

	store := media.NewFileMediaStore()
	ch.SetMediaStore(store)
	path := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	ref, err := store.Store(path, media.MediaMeta{Filename: "a.txt", ContentType: "text/plain"}, "scope")
	if err != nil {
		t.Fatal(err)
	}
	_, err = ch.SendMedia(context.Background(), bus.OutboundMediaMessage{
		ChatID: "!room:matrix.test",
		Parts:  []bus.MediaPart{{Ref: ref, Type: "file", Filename: "a.txt"}},
	})
	notRetried("SendMedia()", err)
}
