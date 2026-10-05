package channels

import (
	"strings"
	"sync"
	"time"

	"github.com/xibodev/compa/pkg/bus"
	"github.com/xibodev/compa/pkg/config"
	"github.com/xibodev/compa/pkg/constants"
	"github.com/xibodev/compa/pkg/identity"
	"github.com/xibodev/compa/pkg/logger"
	"github.com/xibodev/compa/pkg/pairing"
)

// accessPolicy holds a channel's dm_policy and group_policy.
type accessPolicy struct {
	dm    string
	group string
}

// admission is what the access policy does with one inbound message.
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

// SetAccessPolicy sets the channel's dm_policy and group_policy, as
// config.Channel.EffectiveDMPolicy and EffectiveGroupPolicy return them. The
// manager calls it right after constructing a chat channel, so every channel
// it configures has a policy.
func (c *BaseChannel) SetAccessPolicy(dm, group string) {
	c.policy.Store(&accessPolicy{
		dm:    strings.TrimSpace(dm),
		group: strings.TrimSpace(group),
	})
}

// Admits reports whether a message of chatType ("group" and "channel" are
// group chats, anything else a direct message) from sender in chatID would
// be processed. It makes the same decision as HandleMessageWithContext,
// without its side effects, so a platform can call it before downloading a
// message's media. A direct message the pairing policy would record is not
// admitted; pass it to HandleMessageWithContext without its media to have
// the sender recorded.
func (c *BaseChannel) Admits(chatType string, sender bus.SenderInfo, chatID string) bool {
	decision, _ := c.admission(chatType, sender, "", chatID)
	return decision == admitAccept
}

// policies returns the channel's effective dm and group policies. A channel
// built without a policy, as in tests, is decided by allow_from alone: an
// empty allow_from or "*" admits everyone, other entries the senders listed.
func (c *BaseChannel) policies() (dm, group string) {
	if p := c.policy.Load(); p != nil {
		dm, group = p.dm, p.group
	}
	if dm == "" {
		dm = config.DMPolicyOpen
		if len(c.allowList) > 0 && !c.allowsEveryone() {
			dm = config.DMPolicyAllowlist
		}
	}
	if group == "" {
		group = config.GroupPolicyOpen
		if len(c.allowList) > 0 && !c.allowsEveryone() {
			group = config.GroupPolicyAllowlist
		}
	}
	return dm, group
}

// bypassesAccessPolicy reports whether every message is admitted from the
// owner: the dashboard's web chat, which only authenticated users reach, and
// internal channels.
func (c *BaseChannel) bypassesAccessPolicy() bool {
	return c.authenticatedAccess || constants.IsInternalChannel(c.name)
}

// admission applies the access policy to a message of chatType from sender
// (or, without sender details, the raw senderID) in chatID. owner reports
// whether the sender matches an explicit allow_from entry.
func (c *BaseChannel) admission(
	chatType string,
	sender bus.SenderInfo,
	senderID, chatID string,
) (decision admission, owner bool) {
	if c.bypassesAccessPolicy() {
		return admitAccept, true
	}
	owner = c.senderListed(sender, senderID)
	listed := owner || c.allowsEveryone()
	dm, group := c.policies()

	if isGroupChat(chatType) {
		switch group {
		case config.GroupPolicyOpen:
			return admitAccept, owner
		case config.GroupPolicyDisabled:
			return admitDrop, owner
		}
		if listed || c.chatListed(chatID, sender.Platform) {
			return admitAccept, owner
		}
		return admitDrop, owner
	}

	switch dm {
	case config.DMPolicyOpen:
		return admitAccept, owner
	case config.DMPolicyDisabled:
		return admitDrop, owner
	}
	if listed {
		return admitAccept, owner
	}
	if dm == config.DMPolicyPairing {
		return admitPair, owner
	}
	return admitDrop, owner
}

// mayAdmit reports whether a sender could be admitted in some chat. It is
// what IsAllowed and IsAllowedSender answer, so that the platforms' early
// checks let through the messages the policy may still accept, or record.
func (c *BaseChannel) mayAdmit(listed bool) bool {
	if c.bypassesAccessPolicy() || listed || c.allowsEveryone() {
		return true
	}
	dm, group := c.policies()
	return dm == config.DMPolicyOpen || dm == config.DMPolicyPairing || group == config.GroupPolicyOpen
}

// isGroupChat reports whether an InboundContext.ChatType is a group chat.
func isGroupChat(chatType string) bool {
	switch strings.ToLower(strings.TrimSpace(chatType)) {
	case "group", "channel":
		return true
	}
	return false
}

// allowsEveryone reports whether allow_from contains "*".
func (c *BaseChannel) allowsEveryone() bool {
	for _, allowed := range c.allowList {
		if strings.TrimSpace(allowed) == "*" {
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

// chatListed reports whether a group chat is named in allow_from, by its ID
// or its canonical "platform:id" form. A group may be listed by the
// platform's own ID or by the channel's chat ID, which some channels form
// with a kind prefix (OneBot's "group:<id>"). A thread's chat ID,
// "<group>/<thread>" (a Telegram forum topic, a Slack thread), is listed by
// its group's entry too. IRC channel names ignore case.
func (c *BaseChannel) chatListed(chatID, platform string) bool {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return false
	}
	if platform == "" {
		platform = c.name
	}
	foldCase := strings.EqualFold(platform, "irc")
	if foldCase {
		chatID = strings.ToLower(chatID)
	}
	ids := []string{chatID}
	if i := strings.LastIndexByte(chatID, '/'); i > 0 {
		ids = append(ids, chatID[:i])
	}
	var chats []string
	for _, id := range ids {
		chats = append(chats, id)
		if bare, ok := withoutGroupKind(id); ok {
			chats = append(chats, bare)
		}
	}
	for _, allowed := range c.allowList {
		allowed = strings.TrimSpace(allowed)
		if allowed == "" || allowed == "*" || strings.HasPrefix(allowed, "@") {
			continue
		}
		if foldCase {
			allowed = strings.ToLower(allowed)
		}
		for _, entry := range groupEntryForms(allowed, platform) {
			for _, id := range chats {
				chat := bus.SenderInfo{
					Platform:    platform,
					PlatformID:  id,
					CanonicalID: identity.BuildCanonicalID(platform, id),
				}
				if identity.MatchAllowed(chat, entry) {
					return true
				}
			}
		}
	}
	return false
}

// groupKindPrefixes are the prefixes a channel may put before a platform's
// group ID to form its chat ID, as OneBot does with "group:<id>".
var groupKindPrefixes = []string{"group:", "channel:", "room:"}

// withoutGroupKind returns id without its group-kind prefix, and whether it
// had one.
func withoutGroupKind(id string) (string, bool) {
	for _, prefix := range groupKindPrefixes {
		if len(id) > len(prefix) && strings.EqualFold(id[:len(prefix)], prefix) {
			return id[len(prefix):], true
		}
	}
	return id, false
}

// groupEntryForms returns an allow_from entry and, when it names a group by
// a kind-prefixed chat ID ("group:<id>" or "platform:group:<id>"), the same
// entry with the platform's own group ID.
func groupEntryForms(entry, platform string) []string {
	forms := []string{entry}
	if prefix, rest, ok := identity.ParseCanonicalID(entry); ok && strings.EqualFold(prefix, platform) {
		if bare, ok := withoutGroupKind(rest); ok {
			forms = append(forms, prefix+":"+bare)
		}
	} else if bare, ok := withoutGroupKind(entry); ok {
		forms = append(forms, bare)
	}
	return forms
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

// recordPairingRequest records the sender of a direct message the pairing
// policy did not admit, for the owner to approve in the dashboard.
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
