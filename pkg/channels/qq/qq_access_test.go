package qq

import (
	"context"
	"testing"
	"time"

	"github.com/tencent-connect/botgo/dto"

	"github.com/xibodev/compa/v3/pkg/bus"
	"github.com/xibodev/compa/v3/pkg/channels"
	"github.com/xibodev/compa/v3/pkg/config"
	"github.com/xibodev/compa/v3/pkg/pairing"
)

func newAccessTestChannel(t *testing.T, allowFrom ...string) (*QQChannel, *bus.MessageBus, *int) {
	t.Helper()
	messageBus := bus.NewMessageBus()
	ch := &QQChannel{
		BaseChannel: channels.NewBaseChannel("qq", nil, messageBus, allowFrom),
		dedup:       make(map[string]time.Time),
		done:        make(chan struct{}),
		ctx:         context.Background(),
	}
	ch.SetAccessPolicy(config.DMPolicyPairing, config.GroupPolicyAllowlist)

	downloads := 0
	saved := extractAttachments
	extractAttachments = func(_ *QQChannel, _, _ string, attachments []*dto.MessageAttachment) ([]string, []string) {
		downloads += len(attachments)
		return nil, nil
	}
	t.Cleanup(func() { extractAttachments = saved })
	return ch, messageBus, &downloads
}

func receiveInbound(messageBus *bus.MessageBus, wait time.Duration) (bus.InboundMessage, bool) {
	select {
	case msg := <-messageBus.InboundChan():
		return msg, true
	case <-time.After(wait):
		return bus.InboundMessage{}, false
	}
}

func TestHandleC2CMessage_UnpairedSenderAttachmentsAreNotFetched(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	ch, messageBus, downloads := newAccessTestChannel(t, "OWNER")

	err := ch.handleC2CMessage()(nil, &dto.WSC2CMessageData{
		ID:          "msg-1",
		Content:     "hello",
		Author:      &dto.User{ID: "STRANGER"},
		Attachments: []*dto.MessageAttachment{{URL: "https://example.com/a.png", FileName: "a.png"}},
	})
	if err != nil {
		t.Fatalf("handleC2CMessage() error = %v", err)
	}

	if *downloads != 0 {
		t.Fatalf("downloaded %d attachments for an unpaired sender", *downloads)
	}
	if msg, ok := receiveInbound(messageBus, 50*time.Millisecond); ok {
		t.Fatalf("an unpaired sender's message was published: %#v", msg)
	}
	if _, ok := ch.lastMsgID.Load("STRANGER"); ok {
		t.Error("an unpaired sender's chat was remembered")
	}
	requests, err := pairing.List(home, "qq")
	if err != nil || len(requests) != 1 || requests[0].SenderID != "qq:STRANGER" {
		t.Fatalf("pairing requests = %+v, %v; want the stranger", requests, err)
	}
}

func TestHandleGroupATMessage_AllowListedGroupMemberIsAdmitted(t *testing.T) {
	ch, messageBus, _ := newAccessTestChannel(t, "OWNER", "GROUP1")

	err := ch.handleGroupATMessage()(nil, &dto.WSGroupATMessageData{
		ID:      "msg-2",
		GroupID: "GROUP1",
		Content: "status?",
		Author:  &dto.User{ID: "MEMBER"},
	})
	if err != nil {
		t.Fatalf("handleGroupATMessage() error = %v", err)
	}

	msg, ok := receiveInbound(messageBus, time.Second)
	if !ok {
		t.Fatal("a member of an allow-listed group was not admitted")
	}
	if msg.Content != "status?" || msg.Context.SenderIsOwner {
		t.Fatalf("published %q, owner %v", msg.Content, msg.Context.SenderIsOwner)
	}

	// Members of other groups are not.
	err = ch.handleGroupATMessage()(nil, &dto.WSGroupATMessageData{
		ID:          "msg-3",
		GroupID:     "GROUP2",
		Content:     "status?",
		Author:      &dto.User{ID: "MEMBER"},
		Attachments: []*dto.MessageAttachment{{URL: "https://example.com/a.png", FileName: "a.png"}},
	})
	if err != nil {
		t.Fatalf("handleGroupATMessage() error = %v", err)
	}
	if msg, ok := receiveInbound(messageBus, 50*time.Millisecond); ok {
		t.Fatalf("a message from a group not listed was published: %#v", msg)
	}
}

func TestHandleC2CMessage_RecoversFromPanic(t *testing.T) {
	ch, _, _ := newAccessTestChannel(t, "OWNER")
	extractAttachments = func(*QQChannel, string, string, []*dto.MessageAttachment) ([]string, []string) {
		panic("malformed event")
	}

	// botgo runs handlers on its own goroutines; a panic must not escape.
	_ = ch.handleC2CMessage()(nil, &dto.WSC2CMessageData{
		ID:     "msg-4",
		Author: &dto.User{ID: "OWNER"},
	})
}
