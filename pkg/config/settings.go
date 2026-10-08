package config

import (
	"fmt"
	"strings"
)

// Values of channel_list.<whatsapp native>.settings.chats.
const (
	// WhatsAppChatsSelf takes input only from the owner's own "message
	// yourself" chat.
	WhatsAppChatsSelf = "self"
	// WhatsAppChatsAllowed adds the direct chats with the accounts allow_from
	// lists, and records others for pairing while it lists none.
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
