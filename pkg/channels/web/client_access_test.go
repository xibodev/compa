package web

import (
	"context"
	"testing"
	"time"

	"github.com/xibodev/compa/pkg/bus"
	"github.com/xibodev/compa/pkg/config"
	"github.com/xibodev/compa/pkg/pairing"
)

// newAccessTestWebClient builds a web client whose channel_list entry has
// the given dm_policy, group_policy and allow_from, with the access policies
// the manager gives it.
func newAccessTestWebClient(t *testing.T, dm, group string, allowFrom ...string) (*WebClientChannel, *bus.MessageBus) {
	t.Helper()

	mb := bus.NewMessageBus()
	bc := &config.Channel{
		Type: config.ChannelWebClient, Enabled: true, AllowFrom: allowFrom, DMPolicy: dm, GroupPolicy: group,
	}
	ch, err := NewWebClientChannel(bc, &config.WebChatClientSettings{URL: "ws://localhost:8080/ws"}, mb)
	if err != nil {
		t.Fatalf("NewWebClientChannel() error = %v", err)
	}
	ch.ctx = context.Background()
	ch.SetAccessPolicy(bc.EffectiveDMPolicy(), bc.EffectiveGroupPolicy())
	return ch, mb
}

func assertNoInbound(t *testing.T, mb *bus.MessageBus, what string) {
	t.Helper()

	select {
	case inbound := <-mb.InboundChan():
		t.Fatalf("%s was published: %#v", what, inbound)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestWebClientChannel_UnpairedServerIsRecordedWithoutDecodingMedia(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	// No allow_from and no policies: direct messages pair, as on every chat
	// channel.
	ch, mb := newAccessTestWebClient(t, "", "")

	// Media the channel would refuse to decode: the message is decided on first.
	ch.handleServerMessage(&webConn{sessionID: "sess-unpaired"}, WebMessage{
		Type:    TypeMessageCreate,
		Payload: map[string]any{"attachments": "not-an-array"},
	})

	assertNoInbound(t, mb, "an unpaired server's message")
	requests, err := pairing.List(home, "web_client")
	if err != nil || len(requests) != 1 || requests[0].SenderID != "web_client:web-remote" {
		t.Fatalf("pairing requests = %+v, %v; want the remote server", requests, err)
	}
}

func TestWebClientChannel_DirectMessagesFollowTheDMPolicy(t *testing.T) {
	// An open group policy would let IsAllowedSender wave the server through;
	// its messages are direct ones, which the allowlist rejects.
	ch, mb := newAccessTestWebClient(t, config.DMPolicyAllowlist, config.GroupPolicyOpen, "web_client:someone-else")

	ch.handleServerMessage(&webConn{sessionID: "sess-unlisted"}, WebMessage{
		Type:    TypeMessageCreate,
		Payload: map[string]any{PayloadKeyContent: "hello"},
	})
	assertNoInbound(t, mb, "an unlisted server's message")
}

func TestWebClientChannel_ListedServerIsAdmittedWithMedia(t *testing.T) {
	ch, mb := newAccessTestWebClient(t, "", "", "web_client:web-remote")
	imageURL := "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+X2ioAAAAASUVORK5CYII="

	ch.handleServerMessage(&webConn{sessionID: "sess-listed"}, WebMessage{
		Type: TypeMessageCreate,
		Payload: map[string]any{
			PayloadKeyContent: "look",
			"attachments":     []any{map[string]any{"type": "image", "url": imageURL}},
		},
	})
	assertInboundMessage(t, mb, "look", []string{imageURL}, "timed out waiting for the listed server's message")
}
