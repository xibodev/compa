package channels

import (
	"strings"
	"sync"
	"time"

	"github.com/xibodev/compa/v4/pkg/bus"
	"github.com/xibodev/compa/v4/pkg/config"
	"github.com/xibodev/compa/v4/pkg/constants"
	"github.com/xibodev/compa/v4/pkg/identity"
	"github.com/xibodev/compa/v4/pkg/logger"
	"github.com/xibodev/compa/v4/pkg/pairing"
)

// Compa serves one person, its owner. A channel answers only the owner's
// accounts, the senders allow_from lists, and only in direct messages: group
// chats, rooms and the threads in them are ignored. While allow_from lists
// no account, a direct message records its sender as a pairing request, so
// the owner can bind their account from the dashboard; once it lists one,
// everyone else is ignored. "*" and group IDs in allow_from admit no one.

// admission is what the access check does with one inbound message.
type admission int

const (
	// admitDrop ignores the message.
	admitDrop admission = iota
	// admitPair ignores the message and records its sender as a pairing
	// request for the owner to approve.
	admitPair
	// admitAccept processes the message.
	admitAccept
)

// pairingRecordInterval bounds how often one sender's pairing request is
// rewritten, so a stranger repeating a message does not rewrite the store
// on every message.
const pairingRecordInterval = time.Minute

// recordPairing stores a pairing request; tests replace it.
var recordPairing = func(r pairing.Request) error {
	return pairing.Record(config.GetHome(), r)
}

// RequireOwner makes the channel admit only its owner: the senders
// allow_from lists, in direct messages. The manager calls it right after
// constructing a chat channel, so every channel it configures checks access
// this way; a channel nothing configured, as in tests, admits every direct
// message while allow_from lists no account.
func (c *BaseChannel) RequireOwner() {
	c.ownerOnly.Store(true)
	if c.allowsEveryone() {
		logger.WarnCF("channels", "\"*\" in allow_from no longer admits anyone; the channel answers only the accounts it lists", map[string]any{
			"channel": c.name,
		})
	}
}

// Admits reports whether a message of chatType ("group" and "channel" are
// group chats, anything else a direct message) from sender would be
// processed. It makes the same decision as HandleMessageWithContext, without
// its side effects, so a platform can call it before downloading a
// message's media. A direct message that would be recorded as a pairing
// request is not admitted; pass it to HandleMessageWithContext without its
// media to have the sender recorded. The chat the message is in decides
// nothing.
func (c *BaseChannel) Admits(chatType string, sender bus.SenderInfo, _ string) bool {
	decision, _ := c.admission(chatType, sender, "")
	return decision == admitAccept
}

// bypassesAccessPolicy reports whether every message is admitted from the
// owner: the dashboard's web chat, which only authenticated users reach, and
// internal channels.
func (c *BaseChannel) bypassesAccessPolicy() bool {
	return c.authenticatedAccess || constants.IsInternalChannel(c.name)
}

// admission decides a message of chatType from sender (or, without sender
// details, the raw senderID). owner reports whether the sender is the owner.
func (c *BaseChannel) admission(chatType string, sender bus.SenderInfo, senderID string) (decision admission, owner bool) {
	if c.bypassesAccessPolicy() {
		return admitAccept, true
	}
	if isGroupChat(chatType) {
		return admitDrop, false
	}
	if c.senderListed(sender, senderID) {
		return admitAccept, true
	}
	if c.hasOwnerAccount() {
		return admitDrop, false
	}
	if c.ownerOnly.Load() {
		return admitPair, false
	}
	// A channel nothing configured, as in tests.
	return admitAccept, false
}

// mayAdmit reports whether a sender could be admitted in a direct message,
// or recorded for pairing. It is what IsAllowed and IsAllowedSender answer,
// so that the platforms' early checks let those messages through.
func (c *BaseChannel) mayAdmit(listed bool) bool {
	return c.bypassesAccessPolicy() || listed || !c.hasOwnerAccount()
}

// isGroupChat reports whether an InboundContext.ChatType is a group chat.
func isGroupChat(chatType string) bool {
	switch strings.ToLower(strings.TrimSpace(chatType)) {
	case "group", "channel":
		return true
	}
	return false
}

// allowsEveryone reports whether allow_from contains "*", which admits no
// one.
func (c *BaseChannel) allowsEveryone() bool {
	for _, allowed := range c.allowList {
		if strings.TrimSpace(allowed) == "*" {
			return true
		}
	}
	return false
}

// hasOwnerAccount reports whether allow_from lists an account: an entry
// other than "*".
func (c *BaseChannel) hasOwnerAccount() bool {
	for _, allowed := range c.allowList {
		if allowed = strings.TrimSpace(allowed); allowed != "" && allowed != "*" {
			return true
		}
	}
	return false
}

// senderListed reports whether the sender matches an explicit (not "*")
// allow_from entry. A raw senderID without platform IDs in sender is
// compared as a string, as earlier versions did.
func (c *BaseChannel) senderListed(sender bus.SenderInfo, senderID string) bool {
	senderID = strings.TrimSpace(senderID)
	useInfo := sender.CanonicalID != "" || sender.PlatformID != "" || senderID == ""
	for _, allowed := range c.allowList {
		allowed = strings.TrimSpace(allowed)
		if allowed == "" || allowed == "*" {
			continue
		}
		if useInfo {
			if identity.MatchAllowed(sender, allowed) {
				return true
			}
			continue
		}
		// An "@username" entry also matches a sender ID equal to the username.
		if senderID == allowed || senderID == strings.TrimPrefix(allowed, "@") {
			return true
		}
	}
	return false
}

// pairingThrottle remembers when each sender's pairing request was last
// written.
type pairingThrottle struct {
	mu   sync.Mutex
	last map[string]time.Time
}

// due reports whether key's request should be written now, and if so marks
// it written.
func (t *pairingThrottle) due(key string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.last == nil {
		t.last = make(map[string]time.Time)
	}
	if at, ok := t.last[key]; ok && now.Sub(at) < pairingRecordInterval {
		return false
	}
	if len(t.last) >= 1024 {
		for k, at := range t.last {
			if now.Sub(at) >= pairingRecordInterval {
				delete(t.last, k)
			}
		}
	}
	t.last[key] = now
	return true
}

// recordPairingRequest records the sender of a direct message to a channel
// without an owner yet, for the owner to approve in the dashboard.
func (c *BaseChannel) recordPairingRequest(sender bus.SenderInfo, senderID string) {
	request := pairing.Request{
		Channel:     c.name,
		SenderID:    pairingSenderID(sender, senderID),
		PlatformID:  sender.PlatformID,
		DisplayName: sender.DisplayName,
	}
	if request.PlatformID == "" {
		request.PlatformID = strings.TrimSpace(senderID)
	}
	if request.DisplayName == "" {
		request.DisplayName = sender.Username
	}
	if request.SenderID == "" || !c.pairingThrottle.due(request.SenderID, time.Now()) {
		return
	}
	if err := recordPairing(request); err != nil {
		logger.WarnCF("channels", "Failed to record pairing request", map[string]any{
			"channel": c.name,
			"error":   err.Error(),
		})
		return
	}
	logger.InfoCF("channels", "Direct message from an unpaired sender held for approval", map[string]any{
		"channel":   c.name,
		"sender_id": request.SenderID,
	})
}

// pairingSenderID is the allow_from entry that admits sender: its canonical
// "platform:id" form when known, else its raw ID.
func pairingSenderID(sender bus.SenderInfo, senderID string) string {
	if id := strings.TrimSpace(sender.CanonicalID); id != "" {
		return id
	}
	if id := identity.BuildCanonicalID(sender.Platform, sender.PlatformID); id != "" {
		return id
	}
	if id := strings.TrimSpace(sender.PlatformID); id != "" {
		return id
	}
	return strings.TrimSpace(senderID)
}
