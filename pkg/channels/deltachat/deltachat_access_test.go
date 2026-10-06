package deltachat

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xibodev/compa/v2/pkg/bus"
	"github.com/xibodev/compa/v2/pkg/config"
	"github.com/xibodev/compa/v2/pkg/media"
	"github.com/xibodev/compa/v2/pkg/pairing"
)

// deliverAttachment runs handleMessage for one message with an attachment
// from alice@example.org in chat. It returns the bus, and whether the
// attachment was copied into the media directory.
func deliverAttachment(t *testing.T, chat dcChat, allowFrom ...string) (*bus.MessageBus, bool) {
	t.Helper()
	msgBus := bus.NewMessageBus()
	t.Cleanup(msgBus.Close)
	ch := newTestChannelWithBus(t, msgBus, func(bc *config.Channel) { bc.AllowFrom = allowFrom })
	ch.SetAccessPolicy(config.DMPolicyPairing, config.GroupPolicyAllowlist)
	ch.SetMediaStore(media.NewFileMediaStore())
	ch.ctx = context.Background()
	ch.accountID = 7
	ch.selfAddr = "bot@example.org"

	name := fmt.Sprintf("photo-%d.jpg", time.Now().UnixNano())
	file := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(file, []byte("img"), 0o600); err != nil {
		t.Fatal(err)
	}
	rpc, cleanup := newMockRPC(t, func(req rpcRequest) string {
		switch req.Method {
		case "get_message":
			return rpcResult(req, dcMessage{
				ID: 5, ChatID: chat.ID, File: file, FileName: name, FileMime: "image/jpeg",
				Sender: &dcContact{Address: "alice@example.org"},
			})
		case "get_full_chat_by_id":
			return rpcResult(req, chat)
		case "markseen_msgs":
			return rpcResult(req, nil)
		default:
			return rpcUnexpectedMethod(req)
		}
	})
	t.Cleanup(cleanup)
	ch.rpc = rpc

	ch.handleMessage(5)
	entries, _ := os.ReadDir(media.TempDir())
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), "_"+name) {
			_ = os.Remove(filepath.Join(media.TempDir(), entry.Name()))
			return msgBus, true
		}
	}
	return msgBus, false
}

func TestHandleMessage_UnpairedSenderAttachmentIsNotCopied(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	msgBus, copied := deliverAttachment(t, dcChat{ID: 99, Name: "dm", ChatType: chatTypeSingle})
	if copied {
		t.Fatal("the attachment of a message the policy rejects was copied")
	}
	select {
	case inbound := <-msgBus.InboundChan():
		t.Fatalf("an unpaired sender's message was published: %#v", inbound)
	case <-time.After(50 * time.Millisecond):
	}
	requests, err := pairing.List(home, config.ChannelDeltaChat)
	if err != nil || len(requests) != 1 || requests[0].SenderID != "deltachat:alice@example.org" {
		t.Fatalf("pairing requests = %+v, %v; want alice", requests, err)
	}
}

func TestHandleMessage_AllowListedGroupMemberIsAdmitted(t *testing.T) {
	msgBus, _ := deliverAttachment(t, dcChat{ID: 99, Name: "team", ChatType: "Group"}, "deltachat:owner@example.org", "99")

	select {
	case inbound := <-msgBus.InboundChan():
		if len(inbound.Media) != 1 || inbound.Context.SenderIsOwner {
			t.Fatalf("media = %v, owner = %v", inbound.Media, inbound.Context.SenderIsOwner)
		}
	case <-time.After(time.Second):
		t.Fatal("a member of an allow-listed group was not admitted")
	}
}
