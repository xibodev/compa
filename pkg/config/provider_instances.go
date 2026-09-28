package config

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"

	core "github.com/xibodev/llmgw-core"

	providercommon "github.com/xibodev/compa/pkg/providers/common"
)

type ProviderInstanceState string

const (
	ProviderInstanceStateEnabled  ProviderInstanceState = "enabled"
	ProviderInstanceStateDisabled ProviderInstanceState = "disabled"
)

var providerInstanceIDPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]*[a-z0-9])?$`)

// ProviderInstanceConfig owns one provider adapter connection independently of
// any model selected from its catalog.
type ProviderInstanceConfig struct {
	ID                string            `json:"id"`
	ProviderKind      string            `json:"provider_kind"`
	Adapter           string            `json:"adapter"`
	Protocol          string            `json:"protocol"`
	Endpoint          string            `json:"endpoint,omitempty"`
	AuthConnectionRef string            `json:"auth_connection_ref,omitempty"`
	Headers           map[string]string `json:"headers,omitempty"`
	// Settings configure the adapter: display_name, project and location for
	// Vertex AI and, for an extension instance, the extension_* settings.
	Settings map[string]any `json:"settings,omitempty"`
	// Runtime holds the request settings every model of the instance runs
	// with; nil uses the provider defaults.
	Runtime *ProviderInstanceRuntime `json:"runtime,omitempty"`
	State   ProviderInstanceState    `json:"state"`
}

// ProviderInstanceRuntime holds the typed request settings of a provider
// instance, applied to every model call on it.
type ProviderInstanceRuntime struct {
	// Proxy is the http, https, socks5 or socks5h proxy URL requests go
	// through; empty uses none.
	Proxy string `json:"proxy,omitempty"`
	// RequestTimeout bounds a request, in seconds; 0 uses the provider
	// default.
	RequestTimeout int `json:"request_timeout,omitempty"`
	// RPM caps requests per minute; 0 is unlimited.
	RPM int `json:"rpm,omitempty"`
	// Streaming turns provider streaming on or off; nil leaves it on, so
	// answers show as they arrive.
	Streaming *bool `json:"streaming,omitempty"`
	// ThinkingLevel is the extended-thinking level: off, low, medium, high,
	// xhigh or adaptive; empty leaves it to the agent.
	ThinkingLevel string `json:"thinking_level,omitempty"`
	// MaxTokensField names the request's max-tokens field, such as
	// "max_completion_tokens"; empty uses the provider's choice.
	MaxTokensField string `json:"max_tokens_field,omitempty"`
	// ToolSchemaTransform adapts tool schemas for providers that reject
	// complex JSON Schema: "off" (or empty) or "simple".
	ToolSchemaTransform string `json:"tool_schema_transform,omitempty"`
	// ExtraBody holds fields merged into every request body.
	ExtraBody map[string]any `json:"extra_body,omitempty"`
}

// thinkingLevels are the extended-thinking levels the agent understands.
var thinkingLevels = []string{"off", "low", "medium", "high", "xhigh", "adaptive"}

// ValidThinkingLevel reports whether level is an extended-thinking level the
// agent understands, ignoring case and surrounding space. Empty is not a
// level.
func ValidThinkingLevel(level string) bool {
	level = strings.ToLower(strings.TrimSpace(level))
	for _, known := range thinkingLevels {
		if level == known {
			return true
		}
	}
	return false
}

// Validate checks the runtime settings' values.
func (r *ProviderInstanceRuntime) Validate() error {
	if r == nil {
		return nil
	}
	if proxy := strings.TrimSpace(r.Proxy); proxy != "" {
		parsed, err := url.Parse(proxy)
		if err != nil || parsed.Host == "" {
			return fmt.Errorf("runtime.proxy must be an absolute proxy URL")
		}
		switch strings.ToLower(parsed.Scheme) {
		case "http", "https", "socks5", "socks5h":
		default:
			return fmt.Errorf("runtime.proxy scheme must be http, https, socks5 or socks5h")
		}
	}
	if r.RequestTimeout < 0 {
		return fmt.Errorf("runtime.request_timeout must not be negative")
	}
	if r.RPM < 0 {
		return fmt.Errorf("runtime.rpm must not be negative")
	}
	if r.ThinkingLevel != "" && !ValidThinkingLevel(r.ThinkingLevel) {
		return fmt.Errorf("runtime.thinking_level %q is not one of %s", r.ThinkingLevel, strings.Join(thinkingLevels, ", "))
	}
	if strings.ContainsAny(r.MaxTokensField, " \t\r\n") {
		return fmt.Errorf("runtime.max_tokens_field must be a field name")
	}
	if _, err := providercommon.NormalizeToolSchemaTransform(r.ToolSchemaTransform); err != nil {
		return fmt.Errorf("runtime.tool_schema_transform: %w", err)
	}
	for name := range r.ExtraBody {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("runtime.extra_body field name must not be empty")
		}
	}
	return nil
}

func (c *ProviderInstanceConfig) Validate() error {
	if c == nil {
		return fmt.Errorf("provider instance is required")
	}
	if !providerInstanceIDPattern.MatchString(c.ID) {
		return fmt.Errorf("id must be a stable lowercase identifier using letters, numbers, '.', '_', or '-'")
	}
	if strings.TrimSpace(c.ProviderKind) == "" {
		return fmt.Errorf("provider_kind is required")
	}
	if strings.TrimSpace(c.Adapter) == "" {
		return fmt.Errorf("adapter is required")
	}
	if strings.TrimSpace(c.Protocol) == "" {
		return fmt.Errorf("protocol is required")
	}
	switch c.State {
	case ProviderInstanceStateEnabled, ProviderInstanceStateDisabled:
	default:
		return fmt.Errorf("state must be %q or %q", ProviderInstanceStateEnabled, ProviderInstanceStateDisabled)
	}
	for name := range c.Headers {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("header name must not be empty")
		}
	}
	return c.Runtime.Validate()
}

// ExtensionDaemonConfig locates the extension daemon.
type ExtensionDaemonConfig struct {
	// URL is the daemon's origin, such as "http://127.0.0.1:18888".
	URL string `json:"url"`
}

// Adapters of the provider instances Compa runs.
const (
	ProviderAdapterOpenAICompatible    = "openai-compatible"
	ProviderAdapterAnthropicCompatible = "anthropic-compatible"
	// ProviderAdapterNative runs the llmgw-core provider the core registry
	// records for the instance's provider kind, such as Google, Azure
	// OpenAI, Bedrock or Ollama.
	ProviderAdapterNative = "native"
	// ProviderAdapterExtension reaches one provider an extension daemon
	// serves. Endpoint is the daemon's URL, Protocol the provider's primary
	// llmgw-core surface (a chat surface when it serves one, else any surface
	// core defines, such as audio_speech), the setting ExtensionSurfacesSetting
	// every surface the daemon lists for it, and ExtensionProviderSetting the
	// daemon's id of the provider.
	ProviderAdapterExtension = "extension"
)

// Settings of an extension provider instance.
const (
	// ExtensionProviderSetting is the daemon's id of the provider.
	ExtensionProviderSetting = "extension_provider"
	// ExtensionCredentialSetting is what the provider needs: "none",
	// "token" or "oauth".
	ExtensionCredentialSetting = "extension_credential"
	// ExtensionCredentialKeySetting is the auth store key of a signed-in
	// provider's credential. Such a credential refreshes, so it is read per
	// request rather than through auth_connection_ref, which resolves once.
	ExtensionCredentialKeySetting = "extension_credential_key"
	// ExtensionDisplayNameSetting is the daemon's display name of the provider.
	ExtensionDisplayNameSetting = "display_name"
	// ExtensionSurfacesSetting lists the llmgw-core surfaces the daemon
	// serves for the provider, such as ["audio_speech"]. The instance's
	// core provider serves exactly these.
	ExtensionSurfacesSetting = "extension_surfaces"
)

// ExtensionProvider returns the daemon's id of an extension instance's
// provider, or "" for any other instance.
func (c *ProviderInstanceConfig) ExtensionProvider() string {
	if c == nil || !strings.EqualFold(strings.TrimSpace(c.Adapter), ProviderAdapterExtension) {
		return ""
	}
	value, _ := c.Settings[ExtensionProviderSetting].(string)
	return strings.TrimSpace(value)
}

// SupportedProviderInstanceAdapter returns an error for an instance whose
// adapter Compa does not run. Compa reaches every provider through an
// llmgw-core provider: an OpenAI- or Anthropic-compatible endpoint, the native
// provider of its kind, or an extension daemon; an extension instance must name
// the daemon's provider, and its protocol and listed surfaces must be surfaces
// llmgw-core defines (chat or not, such as audio_speech).
func SupportedProviderInstanceAdapter(instance *ProviderInstanceConfig) error {
	if instance == nil {
		return fmt.Errorf("provider instance is required")
	}
	switch strings.ToLower(strings.TrimSpace(instance.Adapter)) {
	case ProviderAdapterOpenAICompatible, ProviderAdapterAnthropicCompatible, ProviderAdapterNative:
		return nil
	case ProviderAdapterExtension:
		if instance.ExtensionProvider() == "" {
			return fmt.Errorf("provider instance %q has no %s setting", instance.ID, ExtensionProviderSetting)
		}
		protocol := strings.ToLower(strings.TrimSpace(instance.Protocol))
		if !core.KnownModelSurface(core.ModelSurface(protocol)) {
			return fmt.Errorf("provider instance %q uses surface %q, which llmgw-core does not define", instance.ID, instance.Protocol)
		}
		if raw, ok := instance.Settings[ExtensionSurfacesSetting]; ok {
			surfaces, valid := extensionSurfaceList(raw)
			if !valid || len(surfaces) == 0 {
				return fmt.Errorf("provider instance %q: %s must list llmgw-core surfaces", instance.ID, ExtensionSurfacesSetting)
			}
			for _, surface := range surfaces {
				if !core.KnownModelSurface(core.ModelSurface(surface)) {
					return fmt.Errorf("provider instance %q: %s lists surface %q, which llmgw-core does not define", instance.ID, ExtensionSurfacesSetting, surface)
				}
			}
			if !slices.Contains(surfaces, protocol) {
				return fmt.Errorf("provider instance %q: %s does not list its protocol %q", instance.ID, ExtensionSurfacesSetting, protocol)
			}
		}
		return nil
	}
	return fmt.Errorf(
		"provider instance %q uses adapter %q, which Compa does not include; remove it, or connect the provider through an OpenAI- or Anthropic-compatible endpoint or its native provider",
		instance.ID, instance.Adapter,
	)
}

// Chat surfaces of an extension instance's protocol, as llmgw-core names
// them. An extension instance's protocol may be any surface core defines,
// such as audio_speech for a speech-only daemon provider.
const (
	ExtensionSurfaceChatCompletions = "chat_completions"
	ExtensionSurfaceMessages        = "messages"
)

// ExtensionSurfaces returns the surfaces the daemon lists for an extension
// instance's provider (the ExtensionSurfacesSetting), or just its protocol
// when the setting is absent. Other instances have none.
func (c *ProviderInstanceConfig) ExtensionSurfaces() []string {
	if c.ExtensionProvider() == "" {
		return nil
	}
	if surfaces, ok := extensionSurfaceList(c.Settings[ExtensionSurfacesSetting]); ok && len(surfaces) > 0 {
		return surfaces
	}
	if protocol := strings.ToLower(strings.TrimSpace(c.Protocol)); protocol != "" {
		return []string{protocol}
	}
	return nil
}

// extensionSurfaceList reads the ExtensionSurfacesSetting, a list of strings
// in memory ([]string) or after a JSON round trip ([]any).
func extensionSurfaceList(raw any) ([]string, bool) {
	var values []string
	switch list := raw.(type) {
	case []string:
		values = list
	case []any:
		for _, item := range list {
			text, ok := item.(string)
			if !ok {
				return nil, false
			}
			values = append(values, text)
		}
	default:
		return nil, false
	}
	surfaces := make([]string, 0, len(values))
	for _, value := range values {
		if surface := strings.ToLower(strings.TrimSpace(value)); surface != "" && !slices.Contains(surfaces, surface) {
			surfaces = append(surfaces, surface)
		}
	}
	return surfaces, true
}

// ExactModelTarget identifies one model owned by one configured provider
// instance. ModelID may contain additional slashes.
type ExactModelTarget struct {
	InstanceID string
	ModelID    string
}

func ParseExactModelTarget(raw string) (ExactModelTarget, error) {
	raw = strings.TrimSpace(raw)
	instanceID, modelID, found := strings.Cut(raw, "/")
	if !found || !providerInstanceIDPattern.MatchString(instanceID) {
		return ExactModelTarget{}, fmt.Errorf("target must be instance-id/model-id")
	}
	if modelID == "" || strings.TrimSpace(modelID) != modelID || strings.ContainsAny(modelID, "\t\n\r ") || strings.Contains(modelID, "//") {
		return ExactModelTarget{}, fmt.Errorf("target model-id is invalid")
	}
	return ExactModelTarget{InstanceID: instanceID, ModelID: modelID}, nil
}

func (t ExactModelTarget) String() string {
	return t.InstanceID + "/" + t.ModelID
}

// ModelRouteConfig is an ordered failover route of exact instance-owned
// targets. Ordering is significant and duplicates are invalid.
type ModelRouteConfig struct {
	Name    string   `json:"name"`
	Targets []string `json:"targets"`
}

// ValidateProviderInstances checks every provider instance, including that
// Compa runs its adapter, and every model route's targets.
func (c *Config) ValidateProviderInstances() error {
	instances := make(map[string]*ProviderInstanceConfig, len(c.ProviderInstances))
	for i, instance := range c.ProviderInstances {
		if err := instance.Validate(); err != nil {
			return fmt.Errorf("provider_instances[%d]: %w", i, err)
		}
		if err := SupportedProviderInstanceAdapter(instance); err != nil {
			return fmt.Errorf("provider_instances[%d]: %w", i, err)
		}
		if _, exists := instances[instance.ID]; exists {
			return fmt.Errorf("provider_instances[%d]: duplicate id %q", i, instance.ID)
		}
		instances[instance.ID] = instance
	}

	routes := make(map[string]struct{}, len(c.ModelRoutes))
	for i, route := range c.ModelRoutes {
		if err := validateModelRoute(route, instances); err != nil {
			return fmt.Errorf("model_routes[%d]: %w", i, err)
		}
		if _, exists := routes[route.Name]; exists {
			return fmt.Errorf("model_routes[%d]: duplicate name %q", i, route.Name)
		}
		routes[route.Name] = struct{}{}
	}
	return nil
}

func validateModelRoute(route *ModelRouteConfig, instances map[string]*ProviderInstanceConfig) error {
	if route == nil {
		return fmt.Errorf("route is required")
	}
	if !providerInstanceIDPattern.MatchString(route.Name) {
		return fmt.Errorf("name must be a stable lowercase identifier using letters, numbers, '.', '_', or '-'")
	}
	if len(route.Targets) == 0 {
		return fmt.Errorf("targets must contain at least one exact target")
	}
	seen := make(map[string]struct{}, len(route.Targets))
	for i, raw := range route.Targets {
		target, err := ParseExactModelTarget(raw)
		if err != nil {
			return fmt.Errorf("targets[%d]: %w", i, err)
		}
		instance, exists := instances[target.InstanceID]
		if !exists {
			return fmt.Errorf("targets[%d]: provider instance %q not found", i, target.InstanceID)
		}
		if instance.State == ProviderInstanceStateDisabled {
			return fmt.Errorf("targets[%d]: provider instance %q is disabled", i, target.InstanceID)
		}
		key := target.String()
		if _, exists := seen[key]; exists {
			return fmt.Errorf("targets[%d]: duplicate target %q", i, key)
		}
		seen[key] = struct{}{}
	}
	return nil
}
