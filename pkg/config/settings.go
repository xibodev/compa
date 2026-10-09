package config

import (
	"fmt"
	"strings"
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

// EffectiveTargets returns the message tool's target policy: any chat,
// unless the config restricts it to the current one.
func (c MessageToolsConfig) EffectiveTargets() string {
	if strings.TrimSpace(c.Targets) == MessageTargetsCurrentChat {
		return MessageTargetsCurrentChat
	}
	return MessageTargetsAny
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
