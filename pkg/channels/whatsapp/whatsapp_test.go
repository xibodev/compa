package whatsapp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/xibodev/compa/v3/pkg/bus"
	"github.com/xibodev/compa/v3/pkg/config"
	"github.com/xibodev/compa/v3/pkg/media"
	"github.com/xibodev/compa/v3/pkg/pairing"
)

const (
	testOwnPhone = "15550001111"
	testOwnLID   = "98765"
)

var (
	ownChat = types.NewJID(testOwnPhone, types.DefaultUserServer)
	friend  = types.NewJID("15550003333", types.DefaultUserServer)
	group   = types.NewJID("120363000000000001", types.GroupServer)
)

// newTestChannel is the channel as the manager configures it, with the
// given allow_from; its downloads return "data", or fail with downloadErr.
func newTestChannel(t *testing.T, downloadErr error, allowFrom ...string) (*WhatsAppChannel, *bus.MessageBus, *int) {
	t.Helper()
	t.Setenv(config.EnvHome, t.TempDir())
	messageBus := bus.NewMessageBus()
	bc := &config.Channel{Type: config.ChannelWhatsApp, Enabled: true, AllowFrom: allowFrom}
	ch := NewWhatsAppChannel(bc, "whatsapp", messageBus, t.TempDir())
	ch.runCtx = context.Background()
	ch.RequireOwner()
	ch.SetMediaStore(media.NewFileMediaStore())
	downloads := 0
	ch.download = func(context.Context, whatsmeow.DownloadableMessage) ([]byte, error) {
		downloads++
		if downloadErr != nil {
			return nil, downloadErr
		}
		return []byte("data"), nil
	}
	return ch, messageBus, &downloads
}

func event(id string, chat, sender types.JID, fromMe bool, msg *waE2E.Message) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Sender:   sender,
				Chat:     chat,
				IsFromMe: fromMe,
				IsGroup:  chat.Server == types.GroupServer,
			},
			ID:       id,
			PushName: "Alice",
		},
		Message: msg,
	}
}

func text(s string) *waE2E.Message { return &waE2E.Message{Conversation: proto.String(s)} }

func receive(t *testing.T, messageBus *bus.MessageBus) (bus.InboundMessage, bool) {
	t.Helper()
	select {
	case inbound := <-messageBus.InboundChan():
		return inbound, true
	case <-time.After(100 * time.Millisecond):
		return bus.InboundMessage{}, false
	}
}

func TestSelfChatIsOwnerInput(t *testing.T) {
	ch, messageBus, _ := newTestChannel(t, nil)

	ch.handleMessage(event("m1", ownChat, ownChat, true, text("/new")), testOwnPhone, testOwnLID)

	inbound, ok := receive(t, messageBus)
	if !ok || inbound.Content != "/new" || !inbound.Context.SenderIsOwner || inbound.Context.ChatType != "direct" {
		t.Fatalf("inbound = %+v, published = %v; want the owner's direct message", inbound, ok)
	}
}

func TestSelfChatByLID(t *testing.T) {
	ch, messageBus, _ := newTestChannel(t, nil)
	ownLID := types.NewJID(testOwnLID, types.HiddenUserServer)

	ch.handleMessage(event("m1", ownLID, ownLID, true, text("hello")), testOwnPhone, testOwnLID)

	if _, ok := receive(t, messageBus); !ok {
		t.Fatal("the own chat addressed by LID is input")
	}
}

// A contact's message is input only when allow_from lists them, and an
// unlisted one makes no pairing request: the owner's contacts write to the
// owner, not to Compa.
func TestOtherChatsAreInputOnlyFromAllowFrom(t *testing.T) {
	ch, messageBus, _ := newTestChannel(t, nil)
	ch.handleMessage(event("m1", friend, friend, false, text("hi")), testOwnPhone, testOwnLID)
	if inbound, ok := receive(t, messageBus); ok {
		t.Fatalf("an unlisted contact's message is not input: %+v", inbound)
	}
	if requests, _ := pairing.List(config.GetHome(), "whatsapp"); len(requests) != 0 {
		t.Fatalf("a contact's message made pairing requests: %+v", requests)
	}

	listed, messageBus, _ := newTestChannel(t, nil, "15550003333")
	listed.handleMessage(event("m2", friend, friend, false, text("hi")), testOwnPhone, testOwnLID)
	inbound, ok := receive(t, messageBus)
	if !ok || !inbound.Context.SenderIsOwner || inbound.Context.ChatType != "direct" {
		t.Fatalf("inbound = %+v, published = %v; want the listed account's direct message", inbound, ok)
	}
}

func TestIgnoredMessages(t *testing.T) {
	ch, messageBus, _ := newTestChannel(t, nil, "whatsapp:15550003333", "whatsapp:"+group.String())
	ch.sent.add("reply-1")

	for name, evt := range map[string]*events.Message{
		"the owner writing to a friend": event("m1", friend, ownChat, true, text("see you at 8")),
		"the owner writing in a group":  event("m2", group, ownChat, true, text("hello all")),
		"a group, even a listed one":    event("m3", group, friend, false, text("hi")),
		"the echo of Compa's reply":     event("reply-1", ownChat, ownChat, true, text("Here is your reminder")),
		"a status update":               event("m4", types.StatusBroadcastJID, friend, false, text("my status")),
		"someone else in the own chat":  event("m5", ownChat, friend, false, text("hi")),
	} {
		ch.handleMessage(evt, testOwnPhone, testOwnLID)
		if inbound, ok := receive(t, messageBus); ok {
			t.Errorf("%s is not input: %+v", name, inbound)
		}
	}
}

func TestFilesAreDownloadedAndNoted(t *testing.T) {
	ch, messageBus, downloads := newTestChannel(t, nil)
	msg := &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
		Caption: proto.String("what is this?"), Mimetype: proto.String("image/jpeg"),
	}}

	ch.handleMessage(event("m1", ownChat, ownChat, true, msg), testOwnPhone, testOwnLID)

	inbound, ok := receive(t, messageBus)
	if !ok || inbound.Content != "what is this?\n[image]" || len(inbound.Media) != 1 || *downloads != 1 {
		t.Fatalf("inbound = %+v, published = %v, downloads = %d", inbound, ok, *downloads)
	}
}

func TestAFailedDownloadIsNoted(t *testing.T) {
	ch, messageBus, _ := newTestChannel(t, errors.New("expired"))
	msg := &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{FileName: proto.String("report.pdf")}}

	ch.handleMessage(event("m1", ownChat, ownChat, true, msg), testOwnPhone, testOwnLID)

	inbound, ok := receive(t, messageBus)
	if !ok || inbound.Content != "[attachment unavailable: report.pdf]" || len(inbound.Media) != 0 {
		t.Fatalf("inbound = %+v, published = %v", inbound, ok)
	}
}

func TestAStrangersFilesAreNotDownloaded(t *testing.T) {
	ch, _, downloads := newTestChannel(t, nil)
	msg := &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Mimetype: proto.String("image/jpeg")}}

	ch.handleMessage(event("m1", friend, friend, false, msg), testOwnPhone, testOwnLID)

	if *downloads != 0 {
		t.Fatalf("downloaded %d files of a stranger's message", *downloads)
	}
}

func TestATooLargeFileIsNotDownloaded(t *testing.T) {
	ch, _, downloads := newTestChannel(t, nil)
	if _, err := ch.saveFile(context.Background(), waFile{kind: "video", size: maxAttachmentBytes + 1}, "m1"); err == nil ||
		!strings.Contains(err.Error(), "limit") || *downloads != 0 {
		t.Fatalf("err = %v, downloads = %d", err, *downloads)
	}
}

func TestMessageParts(t *testing.T) {
	for name, tc := range map[string]struct {
		msg   *waE2E.Message
		text  string
		kinds string
	}{
		"text":            {text("hi"), "hi", ""},
		"extended text":   {&waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("hi")}}, "hi", ""},
		"voice note":      {&waE2E.Message{AudioMessage: &waE2E.AudioMessage{PTT: proto.Bool(true)}}, "", "voice"},
		"audio file":      {&waE2E.Message{AudioMessage: &waE2E.AudioMessage{}}, "", "audio"},
		"video, captured": {&waE2E.Message{VideoMessage: &waE2E.VideoMessage{Caption: proto.String("look")}}, "look", "video"},
		"sticker":         {&waE2E.Message{StickerMessage: &waE2E.StickerMessage{}}, "", "image"},
		"document": {&waE2E.Message{DocumentWithCaptionMessage: &waE2E.FutureProofMessage{Message: &waE2E.Message{
			DocumentMessage: &waE2E.DocumentMessage{FileName: proto.String("../a.pdf"), Caption: proto.String("read")},
		}}}, "read", "file:a.pdf"},
	} {
		got, files := messageParts(tc.msg)
		var kinds []string
		for _, f := range files {
			k := f.kind
			if f.name != "" {
				k += ":" + f.name
			}
			kinds = append(kinds, k)
		}
		if got != tc.text || strings.Join(kinds, ",") != tc.kinds {
			t.Errorf("%s: text %q, files %v; want %q, %q", name, got, kinds, tc.text, tc.kinds)
		}
	}
}
