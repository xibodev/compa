package channels

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/xibodev/compa/v3/pkg/bus"
	"github.com/xibodev/compa/v3/pkg/media"
)

// fileAttachment is an attachment whose download writes a file in dir and
// counts the downloads; fail makes it fail instead.
func fileAttachment(t *testing.T, dir, kind, name string, downloads *int, fail bool) Attachment {
	t.Helper()
	return Attachment{
		Kind: kind,
		Name: name,
		Download: func(context.Context) (string, error) {
			*downloads++
			if fail {
				return "", errors.New("gone")
			}
			path := filepath.Join(dir, kind+"-"+name)
			return path, os.WriteFile(path, []byte("x"), 0o600)
		},
	}
}

func TestReceiveDownloadsAfterTheAccessCheckAndNotesEachFile(t *testing.T) {
	stubPairing(t)
	dir := t.TempDir()
	msgBus := bus.NewMessageBus()
	ch := NewBaseChannel("whatsapp", nil, msgBus, []string{"telegram:111"})
	ch.RequireOwner()
	store := media.NewFileMediaStore()
	ch.SetMediaStore(store)

	downloads := 0
	attachments := []Attachment{
		fileAttachment(t, dir, "image", "", &downloads, false),
		fileAttachment(t, dir, "file", "report.pdf", &downloads, true),
		fileAttachment(t, dir, "voice", "", &downloads, false),
	}

	// A stranger's message: nothing is downloaded, nothing published.
	stranger := telegramSender("999", "")
	if err := ch.Receive(context.Background(), "999", "look", attachments, bus.InboundContext{
		ChatID: "999", ChatType: "direct", SenderID: "999",
	}, stranger); err != nil {
		t.Fatalf("Receive() error = %v", err)
	}
	if downloads != 0 {
		t.Fatalf("downloaded %d files of a stranger's message", downloads)
	}
	select {
	case msg := <-msgBus.InboundChan():
		t.Fatalf("a stranger's message was published: %+v", msg)
	default:
	}

	// The owner's message: every file is downloaded, noted in order, and the
	// failed one is noted as unavailable.
	owner := telegramSender("111", "")
	if err := ch.Receive(context.Background(), "111", "look", attachments, bus.InboundContext{
		ChatID: "111", ChatType: "direct", SenderID: "111", MessageID: "m1",
	}, owner); err != nil {
		t.Fatalf("Receive() error = %v", err)
	}
	msg := <-msgBus.InboundChan()
	if want := "look\n[image]\n[attachment unavailable: report.pdf]\n[voice]"; msg.Content != want {
		t.Fatalf("content = %q, want %q", msg.Content, want)
	}
	if len(msg.Media) != 2 || !msg.Context.SenderIsOwner {
		t.Fatalf("media = %v, owner = %v; want the two stored files and the owner", msg.Media, msg.Context.SenderIsOwner)
	}
	for _, ref := range msg.Media {
		if _, err := store.Resolve(ref); err != nil {
			t.Fatalf("ref %s is not in the store: %v", ref, err)
		}
	}
}

// Without an owner yet, a stranger's message is recorded for pairing, and
// its files are not downloaded.
func TestReceiveRecordsPairingWithoutDownloading(t *testing.T) {
	recorded := stubPairing(t)
	msgBus := bus.NewMessageBus()
	ch := NewBaseChannel("whatsapp", nil, msgBus, nil)
	ch.RequireOwner()

	downloads := 0
	err := ch.Receive(context.Background(), "999", "", []Attachment{
		fileAttachment(t, t.TempDir(), "image", "", &downloads, false),
	}, bus.InboundContext{ChatID: "999", ChatType: "direct", SenderID: "999"}, telegramSender("999", ""))
	if err != nil {
		t.Fatalf("Receive() error = %v", err)
	}
	if downloads != 0 || len(recorded()) != 1 {
		t.Fatalf("downloads = %d, pairing requests = %d; want 0 and 1", downloads, len(recorded()))
	}
}

func TestAttachmentNotes(t *testing.T) {
	for _, tc := range []struct {
		a          Attachment
		note, name string
	}{
		{Attachment{Kind: "image"}, "[image]", "image"},
		{Attachment{Kind: "image", Name: "cat.jpg"}, "[image: cat.jpg]", "cat.jpg"},
		{Attachment{Kind: "voice"}, "[voice]", "voice message"},
		{Attachment{Kind: "audio", Name: "song.mp3"}, "[audio: song.mp3]", "song.mp3"},
		{Attachment{Kind: "video"}, "[video]", "video"},
		{Attachment{Kind: "sticker"}, "[file]", "file"},
	} {
		if got := attachmentNote(tc.a); got != tc.note {
			t.Errorf("attachmentNote(%+v) = %q, want %q", tc.a, got, tc.note)
		}
		if got := attachmentLabel(tc.a); got != tc.name {
			t.Errorf("attachmentLabel(%+v) = %q, want %q", tc.a, got, tc.name)
		}
	}
}
