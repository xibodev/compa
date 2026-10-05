package config

import (
	"fmt"
	"strings"
)

// Direct-message policies for channel_list.<name>.dm_policy.
const (
	// DMPolicyPairing processes direct messages from senders in allow_from and
	// records any other sender as a pairing request for the owner to approve.
	DMPolicyPairing = "pairing"
	// DMPolicyAllowlist processes direct messages from senders in allow_from
	// only.
	DMPolicyAllowlist = "allowlist"
	// DMPolicyOpen processes direct messages from anyone.
	DMPolicyOpen = "open"
	// DMPolicyDisabled ignores direct messages.
	DMPolicyDisabled = "disabled"
)

// Group policies for channel_list.<name>.group_policy.
const (
	// GroupPolicyAllowlist processes group messages from senders, or in
	// groups, listed in allow_from.
	GroupPolicyAllowlist = "allowlist"
	// GroupPolicyOpen processes group messages from any member.
	GroupPolicyOpen = "open"
	// GroupPolicyDisabled ignores group messages.
	GroupPolicyDisabled = "disabled"
)

// Values of channel_list.<whatsapp native>.settings.chats.
const (
	// WhatsAppChatsSelf takes input only from the owner's own "message
	// yourself" chat.
	WhatsAppChatsSelf = "self"
	// WhatsAppChatsAllowed adds the chats whose senders or groups the
	// channel's dm_policy and group_policy admit.
	WhatsAppChatsAllowed = "allowed"
	// WhatsAppChatsAll takes every chat as input and replies in it on the
	// owner's behalf.
	WhatsAppChatsAll = "all"
)

// Values of tools.message.targets.
const (
	// MessageTargetsCurrentChat lets the message tool send only to the chat
	// the turn came from.
	MessageTargetsCurrentChat = "current_chat"
	// MessageTargetsAny lets the message tool send to any chat of any
	// connected channel.
	MessageTargetsAny = "any"
)

// Defaults of the logging settings.
const (
	DefaultLogMaxSizeMB = 10
	DefaultLogMaxFiles  = 5
)

// CommandsConfig configures the chat commands (/reload, /switch, ...).
type CommandsConfig struct {
	// OwnerOnly runs commands sent from chat apps only when the sender is the
	// owner: a sender matching an explicit allow_from entry. The web UI and
	// the terminal are always the owner.
	OwnerOnly bool `json:"owner_only" env:"COMPA_COMMANDS_OWNER_ONLY"`
}

// LoggingConfig configures Compa's log files.
type LoggingConfig struct {
	// RedactSecrets replaces the secrets Compa stores, and tokens inside
	// URLs, with [FILTERED] in log entries.
	RedactSecrets bool `json:"redact_secrets" env:"COMPA_LOGGING_REDACT_SECRETS"`
	// MaxSizeMB rotates a log file when it reaches this size.
	MaxSizeMB int `json:"max_size_mb" env:"COMPA_LOGGING_MAX_SIZE_MB"`
	// MaxFiles is how many rotated files are kept beside the current one.
	MaxFiles int `json:"max_files" env:"COMPA_LOGGING_MAX_FILES"`
}

// EffectiveMaxSizeMB returns MaxSizeMB, or its default when unset.
func (c LoggingConfig) EffectiveMaxSizeMB() int {
	if c.MaxSizeMB > 0 {
		return c.MaxSizeMB
	}
	return DefaultLogMaxSizeMB
}

// EffectiveMaxFiles returns MaxFiles, or its default when unset.
func (c LoggingConfig) EffectiveMaxFiles() int {
	if c.MaxFiles > 0 {
		return c.MaxFiles
	}
	return DefaultLogMaxFiles
}

// EffectiveTargets returns the message tool's target policy.
func (c MessageToolsConfig) EffectiveTargets() string {
	if strings.TrimSpace(c.Targets) == MessageTargetsAny {
		return MessageTargetsAny
	}
	return MessageTargetsCurrentChat
}

// EffectiveDMPolicy returns the channel's direct-message policy. A channel
// without one derives it from allow_from: "*" opens it, entries make it an
// allowlist, and none pairs.
func (b *Channel) EffectiveDMPolicy() string {
	switch p := strings.TrimSpace(b.DMPolicy); p {
	case DMPolicyPairing, DMPolicyAllowlist, DMPolicyOpen, DMPolicyDisabled:
		return p
	}
	switch {
	case b.AllowsEveryone():
		return DMPolicyOpen
	case b.HasAllowEntries():
		return DMPolicyAllowlist
	default:
		return DMPolicyPairing
	}
}

// EffectiveGroupPolicy returns the channel's group policy. A channel without
// one derives it from allow_from: "*" opens it, otherwise allow_from decides.
func (b *Channel) EffectiveGroupPolicy() string {
	switch p := strings.TrimSpace(b.GroupPolicy); p {
	case GroupPolicyAllowlist, GroupPolicyOpen, GroupPolicyDisabled:
		return p
	}
	if b.AllowsEveryone() {
		return GroupPolicyOpen
	}
	return GroupPolicyAllowlist
}

// AllowsEveryone reports whether allow_from contains "*".
func (b *Channel) AllowsEveryone() bool {
	for _, entry := range b.AllowFrom {
		if strings.TrimSpace(entry) == "*" {
			return true
		}
	}
	return false
}

// HasAllowEntries reports whether allow_from names at least one sender or
// group, not counting "*".
func (b *Channel) HasAllowEntries() bool {
	for _, entry := range b.AllowFrom {
		if e := strings.TrimSpace(entry); e != "" && e != "*" {
			return true
		}
	}
	return false
}

// EffectiveChats returns which chats the native WhatsApp client takes as
// input.
func (s WhatsAppSettings) EffectiveChats() string {
	switch strings.TrimSpace(s.Chats) {
	case WhatsAppChatsAllowed:
		return WhatsAppChatsAllowed
	case WhatsAppChatsAll:
		return WhatsAppChatsAll
	default:
		return WhatsAppChatsSelf
	}
}

// validateChannelPolicies rejects unknown policy values, so that a typo does
// not silently fall back to another policy.
func validateChannelPolicies(name string, b *Channel) error {
	if p := strings.TrimSpace(b.DMPolicy); p != "" {
		switch p {
		case DMPolicyPairing, DMPolicyAllowlist, DMPolicyOpen, DMPolicyDisabled:
		default:
			return fmt.Errorf("channel %q dm_policy %q must be one of pairing, allowlist, open, disabled", name, p)
		}
	}
	if p := strings.TrimSpace(b.GroupPolicy); p != "" {
		switch p {
		case GroupPolicyAllowlist, GroupPolicyOpen, GroupPolicyDisabled:
		default:
			return fmt.Errorf("channel %q group_policy %q must be one of allowlist, open, disabled", name, p)
		}
	}
	return nil
}

// validateChannelSettingsValues checks enumerated values inside typed channel
// settings.
func validateChannelSettingsValues(name string, target any) error {
	if settings, ok := target.(*WhatsAppSettings); ok {
		if c := strings.TrimSpace(settings.Chats); c != "" {
			switch c {
			case WhatsAppChatsSelf, WhatsAppChatsAllowed, WhatsAppChatsAll:
			default:
				return fmt.Errorf("channel %q settings.chats %q must be one of self, allowed, all", name, c)
			}
		}
	}
	return nil
}

// ValidateSettings checks the enumerated settings outside the channels.
func (c *Config) ValidateSettings() error {
	if c == nil {
		return nil
	}
	if t := strings.TrimSpace(c.Tools.Message.Targets); t != "" &&
		t != MessageTargetsCurrentChat && t != MessageTargetsAny {
		return fmt.Errorf("tools.message.targets %q must be current_chat or any", t)
	}
	if err := c.Tools.Approval.Validate(); err != nil {
		return err
	}
	if c.Logging.MaxSizeMB < 0 {
		return fmt.Errorf("logging.max_size_mb must be >= 0")
	}
	if c.Logging.MaxFiles < 0 {
		return fmt.Errorf("logging.max_files must be >= 0")
	}
	return nil
}
