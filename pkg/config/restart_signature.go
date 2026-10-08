package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/xibodev/compa/v3/pkg/approval"
)

// RestartSignature is the part of a config the gateway applies only when it
// starts or reloads, which the web UI's restart indicator compares, keyed by
// what it covers: the model selections, the approval policy, every tool
// setting, the guards around the tools, and per channel its access and the
// rest of its config, secrets included. Other settings, such as model
// parameters, apply the next time the gateway starts without asking for it.
type RestartSignature map[string]string

// Keys of a RestartSignature. The channel keys are followed by the channel's
// name.
const (
	signatureModels   = "models"
	signatureApproval = "approval"
	// signatureTools: every tool setting but the approval policy, MCP
	// servers included.
	signatureTools = "tools"
	// signatureGuards: the workspaces and the restriction to them,
	// isolation, and who may run commands or approve tool calls (commands,
	// hooks).
	signatureGuards = "guards"
	// signatureAccess: a channel's allow_from.
	signatureAccess = "access:"
	// signatureChannel: the rest of a channel's config, secrets included.
	signatureChannel = "channel:"
)

// NewRestartSignature returns cfg's RestartSignature, nil for a nil cfg.
func NewRestartSignature(cfg *Config) RestartSignature {
	if cfg == nil {
		return nil
	}
	tools := cfg.Tools
	tools.Approval = approval.Policy{}
	// An agent's model is a live model selection.
	agents := make([]AgentConfig, len(cfg.Agents.List))
	copy(agents, cfg.Agents.List)
	for i := range agents {
		agents[i].Model = ""
	}
	signature := RestartSignature{
		signatureModels:   modelSelectionSignature(cfg),
		signatureApproval: signatureJSON(cfg.Tools.Approval),
		signatureTools:    signatureJSON(tools),
		signatureGuards: signatureJSON(struct {
			Workspace                 string          `json:"workspace"`
			RestrictToWorkspace       bool            `json:"restrict_to_workspace"`
			AllowReadOutsideWorkspace bool            `json:"allow_read_outside_workspace"`
			Agents                    []AgentConfig   `json:"agents"`
			Isolation                 IsolationConfig `json:"isolation"`
			Commands                  CommandsConfig  `json:"commands"`
			Hooks                     HooksConfig     `json:"hooks"`
		}{
			Workspace:                 cfg.Agents.Defaults.Workspace,
			RestrictToWorkspace:       cfg.Agents.Defaults.RestrictToWorkspace,
			AllowReadOutsideWorkspace: cfg.Agents.Defaults.AllowReadOutsideWorkspace,
			Agents:                    agents,
			Isolation:                 cfg.Isolation,
			Commands:                  cfg.Commands,
			Hooks:                     cfg.Hooks,
		}),
	}
	for name, channel := range cfg.Channels {
		addChannelSignature(signature, name, channel)
	}
	return signature
}

// Equal reports whether s and other cover the same config.
func (s RestartSignature) Equal(other RestartSignature) bool {
	return maps.Equal(s, other)
}

// isLiveSignatureKey reports whether a change of what key covers takes
// effect in the running gateway through its /reload, without a restart: the
// model selections, the approval policy and the channels' access lists and
// policies.
func isLiveSignatureKey(key string) bool {
	return key == signatureModels || key == signatureApproval || strings.HasPrefix(key, signatureAccess)
}

// LiveEqual reports whether s and other agree on their live parts (see
// isLiveSignatureKey).
func (s RestartSignature) LiveEqual(other RestartSignature) bool {
	return s.equalOn(other, isLiveSignatureKey)
}

// EqualBesidesLive reports whether s and other agree on all but their live
// parts.
func (s RestartSignature) EqualBesidesLive(other RestartSignature) bool {
	return s.equalOn(other, func(key string) bool { return !isLiveSignatureKey(key) })
}

// equalOn reports whether s and other agree on the keys covered selects.
func (s RestartSignature) equalOn(other RestartSignature, covered func(key string) bool) bool {
	for key, value := range s {
		if otherValue, ok := other[key]; covered(key) && (!ok || otherValue != value) {
			return false
		}
	}
	for key := range other {
		if _, ok := s[key]; covered(key) && !ok {
			return false
		}
	}
	return true
}

// Digest identifies s: signatures are Equal exactly when their digests are,
// but the digest shows none of the settings, which the gateway's /ready
// reports for the config it applied. It is "" for an empty signature.
func (s RestartSignature) Digest() string {
	if len(s) == 0 {
		return ""
	}
	sum := sha256.New()
	for _, key := range slices.Sorted(maps.Keys(s)) {
		fmt.Fprintf(sum, "%d:%s%d:%s", len(key), key, len(s[key]), s[key])
	}
	return hex.EncodeToString(sum.Sum(nil))
}

// ModelSelection is one model selection a config holds.
type ModelSelection struct {
	// Key names the setting across edits: an agent's model is keyed by the
	// agent's ID, which a reordered agent list keeps.
	Key string
	// Path is the setting's config path.
	Path  string
	Value string
}

// ModelSelections returns the model selections cfg holds: the default, image
// and light models and each agent's model, trimmed.
func ModelSelections(cfg *Config) []ModelSelection {
	if cfg == nil {
		return nil
	}
	defaults := cfg.Agents.Defaults
	selections := []ModelSelection{
		{Key: "default", Path: "agents.defaults.model_name", Value: defaults.ModelName},
		{Key: "image", Path: "agents.defaults.image_model", Value: defaults.ImageModel},
	}
	if defaults.Routing != nil {
		selections = append(selections, ModelSelection{Key: "light", Path: "agents.defaults.routing.light_model", Value: defaults.Routing.LightModel})
	}
	for i, agent := range cfg.Agents.List {
		selections = append(selections, ModelSelection{Key: "agent:" + agent.ID, Path: fmt.Sprintf("agents.list[%d].model", i), Value: agent.Model})
	}
	for i := range selections {
		selections[i].Value = strings.TrimSpace(selections[i].Value)
	}
	return selections
}

// modelSelectionSignatures returns the model settings the gateway reads from
// the config it boots with — the default, image and light model selections,
// the light-model routing and each agent's model — so changing one requires
// a reload. Provider instances, their runtime settings, routes and catalogs
// are left out: the gateway resolves a selection against them as saved when
// a turn runs.
func modelSelectionSignatures(cfg *Config) []string {
	selections := ModelSelections(cfg)
	signatures := make([]string, 0, len(selections)+1)
	for _, selection := range selections {
		signatures = append(signatures, selection.Key+"="+selection.Value)
	}
	if routing := cfg.Agents.Defaults.Routing; routing != nil {
		signatures = append(signatures, fmt.Sprintf(
			"routing=%t/%s", routing.Enabled, strconv.FormatFloat(routing.Threshold, 'g', -1, 64),
		))
	}
	return signatures
}

// modelSelectionSignature is modelSelectionSignatures of cfg, joined.
func modelSelectionSignature(cfg *Config) string {
	if cfg == nil {
		return ""
	}
	return strings.Join(modelSelectionSignatures(cfg), ",")
}

// addChannelSignature adds the signature of the channel name to signature:
// its access, and the rest of its config, secrets included.
func addChannelSignature(signature RestartSignature, name string, channel *Channel) {
	if channel == nil {
		signature[signatureChannel+name] = "<nil>"
		return
	}
	signature[signatureAccess+name] = marshalSignature(struct {
		AllowFrom FlexibleStringSlice `json:"allow_from,omitempty"`
	}{
		AllowFrom: channel.AllowFrom,
	})
	signature[signatureChannel+name] = marshalSignature(struct {
		Enabled            bool              `json:"enabled"`
		Type               string            `json:"type"`
		ReasoningChannelID string            `json:"reasoning_channel_id,omitempty"`
		Typing             TypingConfig      `json:"typing,omitempty"`
		Placeholder        PlaceholderConfig `json:"placeholder,omitempty"`
		Settings           json.RawMessage   `json:"settings,omitempty"`
	}{
		Enabled:            channel.Enabled,
		Type:               channel.Type,
		ReasoningChannelID: channel.ReasoningChannelID,
		Typing:             channel.Typing,
		Placeholder:        channel.Placeholder,
		Settings:           normalizeChannelSettings(channel),
	})
}

// signatureJSON is value as canonical JSON, secrets included.
func signatureJSON(value any) string {
	return marshalSignature(canonicalizeSignatureValue(reflect.ValueOf(value)))
}

func marshalSignature(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "<invalid>"
	}
	return string(encoded)
}

// normalizeChannelSettings is the channel's settings as canonical JSON,
// secrets included.
func normalizeChannelSettings(channel *Channel) json.RawMessage {
	if channel == nil {
		return nil
	}

	decoded, err := channel.GetDecoded()
	if err == nil && decoded != nil {
		normalized, err := json.Marshal(canonicalizeSignatureValue(reflect.ValueOf(decoded)))
		if err == nil {
			return normalized
		}
	}

	return normalizeRawJSON(channel.Settings)
}

func normalizeRawJSON(raw RawNode) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}

	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return bytes.TrimSpace(raw)
	}

	normalized, err := json.Marshal(value)
	if err != nil {
		return bytes.TrimSpace(raw)
	}
	return normalized
}

// canonicalizeSignatureValue turns value into maps, slices and plain values
// that encode the same way whatever the order of its maps. A secret becomes
// its value.
func canonicalizeSignatureValue(value reflect.Value) any {
	if !value.IsValid() {
		return nil
	}

	if value.CanInterface() {
		if secret, ok := secretSignatureValue(value.Interface()); ok {
			return secret
		}
	}

	switch value.Kind() {
	case reflect.Interface, reflect.Pointer:
		if value.IsNil() {
			return nil
		}
		return canonicalizeSignatureValue(value.Elem())
	case reflect.Struct:
		result := make(map[string]any)
		valueType := value.Type()
		for i := 0; i < value.NumField(); i++ {
			field := valueType.Field(i)
			if field.PkgPath != "" {
				continue
			}
			tag := field.Tag.Get("json")
			name := field.Name
			if tag != "" {
				if comma := strings.Index(tag, ","); comma >= 0 {
					tag = tag[:comma]
				}
				if tag == "-" {
					continue
				}
				if tag != "" {
					name = tag
				}
			}
			result[name] = canonicalizeSignatureValue(value.Field(i))
		}
		return result
	case reflect.Slice, reflect.Array:
		length := value.Len()
		result := make([]any, 0, length)
		for i := 0; i < length; i++ {
			result = append(result, canonicalizeSignatureValue(value.Index(i)))
		}
		return result
	case reflect.Map:
		if value.Type().Key().Kind() != reflect.String {
			return value.Interface()
		}
		result := make(map[string]any, value.Len())
		iter := value.MapRange()
		for iter.Next() {
			result[iter.Key().String()] = canonicalizeSignatureValue(iter.Value())
		}
		return result
	default:
		if value.CanInterface() {
			return value.Interface()
		}
		return nil
	}
}

// secretSignatureValue returns the value of v when v is a secret.
func secretSignatureValue(v any) (any, bool) {
	switch typed := v.(type) {
	case SecureString:
		return typed.String(), true
	case *SecureString:
		return typed.String(), true
	case SecureStrings:
		return typed.Values(), true
	case *SecureStrings:
		return typed.Values(), true
	}
	return nil, false
}
