package whatsapp

import (
	"sync"

	"github.com/xibodev/compa/v2/pkg/bus"
	"github.com/xibodev/compa/v2/pkg/config"
	"github.com/xibodev/compa/v2/pkg/identity"
)

// This file holds the chat-selection rules of the native client. It has no
// build tag, so the rules are tested in every build.

// WhatsApp servers of the two forms an account's own address takes.
const (
	phoneServer = "s.whatsapp.net" // types.DefaultUserServer
	lidServer   = "lid"            // types.HiddenUserServer
)

// chatRole is what a message on the linked account is to Compa.
type chatRole int

const (
	// chatIgnored is not input: another chat in "self" mode, or the owner's
	// own message in a chat other than their "message yourself" chat.
	chatIgnored chatRole = iota
	// chatSelf is the owner writing in their own "message yourself" chat. It
	// is input in every mode, and it comes from the owner.
	chatSelf
	// chatOther is a message someone else sent in another chat; the chats
	// setting and the channel policies decide whether it is input.
	chatOther
)

// classifyChat decides what a message is to Compa from the chats setting,
// whether it was written in the owner's "message yourself" chat and whether
// the owner wrote it. The owner's messages to friends and groups are the owner
// talking to them, never instructions, whatever the setting.
func classifyChat(mode string, selfChat, fromMe bool) chatRole {
	switch {
	case selfChat && fromMe:
		return chatSelf
	case selfChat, fromMe:
		return chatIgnored
	case mode == config.WhatsAppChatsSelf:
		return chatIgnored
	default:
		return chatOther
	}
}

// isOwnAddress reports whether user@server is the linked account itself,
// addressed by phone number or by the account's LID.
func isOwnAddress(user, server, ownPhone, ownLID string) bool {
	switch server {
	case phoneServer:
		return ownPhone != "" && user == ownPhone
	case lidServer:
		return ownLID != "" && user == ownLID
	default:
		return false
	}
}

// whatsAppIdentity is the SenderInfo of a WhatsApp address.
func whatsAppIdentity(jid, displayName string) bus.SenderInfo {
	return bus.SenderInfo{
		Platform:    "whatsapp",
		PlatformID:  jid,
		CanonicalID: identity.BuildCanonicalID("whatsapp", jid),
		DisplayName: displayName,
	}
}

// sentMessages remembers the IDs of the latest messages Compa sent. Those
// messages come back as the owner's own messages on the linked account; in
// the "message yourself" chat they would otherwise be read as new requests.
type sentMessages struct {
	mu   sync.Mutex
	ids  map[string]struct{}
	ring []string
	next int
}

func newSentMessages(capacity int) *sentMessages {
	return &sentMessages{
		ids:  make(map[string]struct{}, capacity),
		ring: make([]string, capacity),
	}
}

func (s *sentMessages) add(id string) {
	if id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.ids[id]; ok {
		return
	}
	if old := s.ring[s.next]; old != "" {
		delete(s.ids, old)
	}
	s.ring[s.next] = id
	s.next = (s.next + 1) % len(s.ring)
	s.ids[id] = struct{}{}
}

func (s *sentMessages) contains(id string) bool {
	if id == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.ids[id]
	return ok
}
