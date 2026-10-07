// Package config defines Compa's configuration and loads and saves it.
// config.json holds the settings; .security.yml beside it holds the secrets
// (API keys, tokens and passwords), and COMPA_* environment variables
// override both.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"

	"github.com/xibodev/compa/v3/pkg"
	"github.com/xibodev/compa/v3/pkg/approval"
	"github.com/xibodev/compa/v3/pkg/fileutil"
	"github.com/xibodev/compa/v3/pkg/logger"
)

func init() {
	initChannel()
}

// Config is Compa's configuration, as stored in config.json and
// .security.yml.
type Config struct {
	Isolation IsolationConfig `json:"isolation,omitempty" yaml:"-"`
	Agents    AgentsConfig    `json:"agents"              yaml:"-"`
	Session   SessionConfig   `json:"session,omitempty"   yaml:"-"`
	Evolution EvolutionConfig `json:"evolution,omitempty" yaml:"-"`
	Channels  ChannelsConfig  `json:"channel_list"        yaml:"channel_list"`
	// ProviderInstances are the provider connections models run on; each
	// owns its endpoint, credential reference, runtime settings and model
	// catalog. ModelRoutes are named failover routes over their exact
	// targets, and every model selection in the config names a target or a
	// route. ActiveModels is the shortlist of targets offered for chat.
	ProviderInstances []*ProviderInstanceConfig `json:"provider_instances,omitempty" yaml:"-"`
	ModelRoutes       []*ModelRouteConfig       `json:"model_routes,omitempty"       yaml:"-"`
	ActiveModels      []string                  `json:"active_models,omitempty"      yaml:"-"`
	Gateway           GatewayConfig             `json:"gateway"             yaml:"-"`
	Events            EventsConfig              `json:"events,omitempty"    yaml:"-"`
	Hooks             HooksConfig               `json:"hooks,omitempty"     yaml:"-"`
	Tools             ToolsConfig               `json:"tools"               yaml:",inline"`
	Heartbeat         HeartbeatConfig           `json:"heartbeat"           yaml:"-"`
	Devices           DevicesConfig             `json:"devices"             yaml:"-"`
	Voice             VoiceConfig               `json:"voice"               yaml:"-"`
	// Commands configures who may run the chat commands.
	Commands CommandsConfig `json:"commands" yaml:"-"`
	// Logging configures Compa's log files.
	Logging LoggingConfig `json:"logging" yaml:"-"`
	// Modules configures the detached-module host, including the named
	// source roots a module may be granted read-only.
	Modules ModulesConfig `json:"modules,omitzero" yaml:"-"`
	// BuildInfo contains build-time version information
	BuildInfo BuildInfo `json:"build_info,omitempty" yaml:"-"`

	// Extension is the extension daemon Compa reaches providers through, if
	// any. Its shared secret lives in the auth store.
	Extension *ExtensionDaemonConfig `json:"extension,omitempty" yaml:"-"`

	// sensitiveCache holds the secrets FilterSensitiveData replaces.
	sensitiveCache *SensitiveDataCache

	// envOverrides are the settings the environment changed while the
	// config was loaded; SaveConfig keeps them out of the files.
	envOverrides []envOverride

	// sourceDigest identifies the files LoadConfig read; see SourceDigest.
	sourceDigest string
}

// SourceDigest identifies the content of the config.json and .security.yml
// LoadConfig read for c: configs loaded from the same files have the same
// one, whatever process loaded them. It is empty for a config LoadConfig
// didn't load.
func (c *Config) SourceDigest() string {
	if c == nil {
		return ""
	}
	return c.sourceDigest
}

// extendSourceDigest is digest extended by the content of one more file.
func extendSourceDigest(digest string, data []byte) string {
	sum := sha256.New()
	sum.Write([]byte(digest))
	sum.Write([]byte{0})
	sum.Write(data)
	return hex.EncodeToString(sum.Sum(nil))
}

type EvolutionConfig struct {
	Enabled         bool     `json:"enabled,omitempty"`
	Mode            string   `json:"mode,omitempty"`
	StateDir        string   `json:"state_dir,omitempty"`
	MinTaskCount    int      `json:"min_task_count,omitempty"`
	MinSuccessRatio float64  `json:"min_success_ratio,omitempty"`
	ColdPathTrigger string   `json:"cold_path_trigger,omitempty"`
	ColdPathTimes   []string `json:"cold_path_times,omitempty"`
}

func (c EvolutionConfig) MarshalJSON() ([]byte, error) {
	out := struct {
		Enabled         bool     `json:"enabled,omitempty"`
		Mode            string   `json:"mode,omitempty"`
		StateDir        string   `json:"state_dir,omitempty"`
		MinTaskCount    int      `json:"min_task_count,omitempty"`
		MinSuccessRatio float64  `json:"min_success_ratio,omitempty"`
		ColdPathTrigger string   `json:"cold_path_trigger,omitempty"`
		ColdPathTimes   []string `json:"cold_path_times,omitempty"`
	}{
		Enabled:         c.Enabled,
		Mode:            c.Mode,
		StateDir:        c.StateDir,
		MinTaskCount:    c.EffectiveMinTaskCount(),
		MinSuccessRatio: c.EffectiveMinSuccessRatio(),
		ColdPathTrigger: strings.TrimSpace(c.ColdPathTrigger),
		ColdPathTimes:   c.EffectiveColdPathTimes(),
	}
	if !out.Enabled {
		out.Mode = ""
		out.ColdPathTrigger = ""
		out.ColdPathTimes = nil
	}
	return json.Marshal(out)
}

func (c EvolutionConfig) EffectiveMode() string {
	if !c.Enabled {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(c.Mode)) {
	case "draft":
		return "draft"
	case "apply":
		return "apply"
	case "", "observe":
		return "observe"
	default:
		return "observe"
	}
}

func (c EvolutionConfig) RunsColdPathAutomatically() bool {
	return c.RunsColdPathAfterTurn() || c.RunsColdPathScheduled()
}

func (c EvolutionConfig) ColdPathTriggerMode() string {
	if c.EffectiveMode() != "draft" && c.EffectiveMode() != "apply" {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(c.ColdPathTrigger)) {
	case "", "after_turn":
		return "after_turn"
	case "scheduled":
		return "scheduled"
	case "manual", "none", "off":
		return "manual"
	default:
		return "after_turn"
	}
}

func (c EvolutionConfig) RunsColdPathAfterTurn() bool {
	return c.ColdPathTriggerMode() == "after_turn"
}

func (c EvolutionConfig) RunsColdPathScheduled() bool {
	return c.ColdPathTriggerMode() == "scheduled"
}

func (c EvolutionConfig) EffectiveMinTaskCount() int {
	if c.MinTaskCount > 0 {
		return c.MinTaskCount
	}
	return 2
}

func (c EvolutionConfig) EffectiveMinSuccessRatio() float64 {
	if c.MinSuccessRatio > 0 {
		return c.MinSuccessRatio
	}
	return 0.7
}

func (c EvolutionConfig) EffectiveColdPathTimes() []string {
	out := make([]string, 0, len(c.ColdPathTimes))
	for _, value := range c.ColdPathTimes {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		out = append(out, value)
	}
	return out
}

func (c EvolutionConfig) AutoAppliesDrafts() bool {
	return c.EffectiveMode() == "apply"
}

// IsolationConfig controls subprocess isolation for commands started by Compa.
// It is applied by the isolation package rather than by sandboxing the main process.
type IsolationConfig struct {
	Enabled     bool         `json:"enabled,omitempty"`
	ExposePaths []ExposePath `json:"expose_paths,omitempty"`
}

// ExposePath describes a host path that should remain visible inside the isolated
// child-process environment. This is currently implemented on Linux only.
type ExposePath struct {
	Source string `json:"source"`
	Target string `json:"target,omitempty"`
	Mode   string `json:"mode"`
}

// FilterSensitiveData filters sensitive values from content before sending to LLM.
// This prevents the LLM from seeing its own credentials: the config's own
// secrets and every value a source registered with
// RegisterSensitiveValuesSource returns, consulted on each call.
// Short content (below FilterMinLength) is returned unchanged for performance.
func (c *Config) FilterSensitiveData(content string) string {
	if c == nil {
		return content
	}
	// Check if filtering is enabled (default: true)
	if !c.Tools.IsFilterSensitiveDataEnabled() {
		return content
	}
	// Fast path: skip filtering for short content
	if len(content) < c.Tools.GetFilterMinLength() {
		return content
	}
	return c.SensitiveDataReplacer().Replace(content)
}

type HooksConfig struct {
	Enabled   bool                         `json:"enabled"`
	Defaults  HookDefaultsConfig           `json:"defaults,omitempty"`
	Builtins  map[string]BuiltinHookConfig `json:"builtins,omitempty"`
	Processes map[string]ProcessHookConfig `json:"processes,omitempty"`
}

type HookDefaultsConfig struct {
	ObserverTimeoutMS    int `json:"observer_timeout_ms,omitempty"`
	InterceptorTimeoutMS int `json:"interceptor_timeout_ms,omitempty"`
	ApprovalTimeoutMS    int `json:"approval_timeout_ms,omitempty"`
}

type BuiltinHookConfig struct {
	Enabled  bool            `json:"enabled"`
	Priority int             `json:"priority,omitempty"`
	Config   json.RawMessage `json:"config,omitempty"`
}

type ProcessHookConfig struct {
	Enabled   bool              `json:"enabled"`
	Priority  int               `json:"priority,omitempty"`
	Transport string            `json:"transport,omitempty"`
	Command   []string          `json:"command,omitempty"`
	Dir       string            `json:"dir,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Observe   []string          `json:"observe,omitempty"`
	Intercept []string          `json:"intercept,omitempty"`
}

// BuildInfo contains build-time version information
type BuildInfo struct {
	Version   string `json:"version"`
	GitCommit string `json:"git_commit"`
	BuildTime string `json:"build_time"`
	GoVersion string `json:"go_version"`
}

// MarshalJSON implements custom JSON marshaling for Config
// to omit the session section when it is empty.
func (c *Config) MarshalJSON() ([]byte, error) {
	type Alias Config
	aux := &struct {
		Session *SessionConfig `json:"session,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(c),
	}

	if len(c.Session.Dimensions) > 0 || len(c.Session.IdentityLinks) > 0 || c.Session.DmScope != "" {
		sessionCfg := c.Session
		aux.Session = &sessionCfg
	}

	return json.Marshal(aux)
}

type AgentsConfig struct {
	Defaults AgentDefaults   `json:"defaults"`
	List     []AgentConfig   `json:"list,omitempty"`
	Dispatch *DispatchConfig `json:"dispatch,omitempty"`
}

type AgentConfig struct {
	ID        string           `json:"id"`
	Default   bool             `json:"default,omitempty"`
	Name      string           `json:"name,omitempty"`
	Workspace string           `json:"workspace,omitempty"`
	Model     string           `json:"model,omitempty"` // model selection: exact target "instance-id/model-id" or route name; empty uses the default
	Skills    []string         `json:"skills,omitempty"`
	Subagents *SubagentsConfig `json:"subagents,omitempty"`
}

type SubagentsConfig struct {
	AllowAgents []string `json:"allow_agents,omitempty"`
}

type DispatchConfig struct {
	Rules []DispatchRule `json:"rules,omitempty"`
}

type DispatchRule struct {
	Name              string           `json:"name,omitempty"`
	Agent             string           `json:"agent"`
	When              DispatchSelector `json:"when"`
	SessionDimensions []string         `json:"session_dimensions,omitempty"`
}

type DispatchSelector struct {
	Channel   string `json:"channel,omitempty"`
	Account   string `json:"account,omitempty"`
	Space     string `json:"space,omitempty"`
	Chat      string `json:"chat,omitempty"`
	Topic     string `json:"topic,omitempty"`
	Sender    string `json:"sender,omitempty"`
	Mentioned *bool  `json:"mentioned,omitempty"`
}

type SessionConfig struct {
	Dimensions    []string            `json:"dimensions,omitempty"`
	IdentityLinks map[string][]string `json:"identity_links,omitempty"`
	DmScope       string              `json:"dm_scope,omitempty"`
}

// ApplyDmScope translates the user-facing dm_scope value into the internal
// dimensions array that the routing layer consumes. It is a no-op when
// DmScope is empty or when Dimensions is already set (explicit Dimensions
// take precedence over the derived value).
func (s *SessionConfig) ApplyDmScope() {
	if s.DmScope == "" || len(s.Dimensions) > 0 {
		return
	}
	switch s.DmScope {
	case "per-channel-peer":
		s.Dimensions = []string{"chat", "sender"}
	case "per-channel":
		s.Dimensions = []string{"chat"}
	case "per-peer":
		s.Dimensions = []string{"sender"}
	case "global":
		s.Dimensions = nil
	}
}

// DeriveDmScope sets DmScope based on Dimensions when DmScope is empty.
// This handles configs, including the defaults, that set Dimensions
// without a corresponding DmScope value, ensuring the API response always
// includes a dm_scope that matches the actual runtime dimensions.
func (s *SessionConfig) DeriveDmScope() {
	if s.DmScope != "" || len(s.Dimensions) == 0 {
		return
	}
	switch {
	case slices.Equal(s.Dimensions, []string{"chat", "sender"}):
		s.DmScope = "per-channel-peer"
	case slices.Equal(s.Dimensions, []string{"chat"}):
		s.DmScope = "per-channel"
	case slices.Equal(s.Dimensions, []string{"sender"}):
		s.DmScope = "per-peer"
	}
	// Dimensions not matching any known scope mapping (custom array)
	// is fine — DmScope stays empty and the UI can handle it.
}

// RoutingConfig controls the intelligent model routing feature.
// When enabled, each incoming message is scored against structural features
// (message length, code blocks, tool call history, conversation depth, attachments).
// Messages scoring below Threshold are sent to LightModel; all others use the
// agent's primary model. This reduces cost and latency for simple tasks without
// requiring any keyword matching — all scoring is language-agnostic.
type RoutingConfig struct {
	Enabled    bool    `json:"enabled"`
	LightModel string  `json:"light_model"` // model selection (exact target or model route name) for simple tasks
	Threshold  float64 `json:"threshold"`   // complexity score in [0,1]; score >= threshold → primary model
}

// SubTurnConfig configures the SubTurn execution system. Its env tags are
// the full COMPA_AGENTS_DEFAULTS_SUBTURN_* names, so the field that holds it
// carries no envPrefix, which would double them.
type SubTurnConfig struct {
	MaxDepth              int `json:"max_depth"               env:"COMPA_AGENTS_DEFAULTS_SUBTURN_MAX_DEPTH"`
	MaxConcurrent         int `json:"max_concurrent"          env:"COMPA_AGENTS_DEFAULTS_SUBTURN_MAX_CONCURRENT"`
	DefaultTimeoutMinutes int `json:"default_timeout_minutes" env:"COMPA_AGENTS_DEFAULTS_SUBTURN_DEFAULT_TIMEOUT_MINUTES"`
	ConcurrencyTimeoutSec int `json:"concurrency_timeout_sec" env:"COMPA_AGENTS_DEFAULTS_SUBTURN_CONCURRENCY_TIMEOUT_SEC"`
}

type ToolFeedbackConfig struct {
	Enabled          bool `json:"enabled"           env:"COMPA_AGENTS_DEFAULTS_TOOL_FEEDBACK_ENABLED"`
	MaxArgsLength    int  `json:"max_args_length"   env:"COMPA_AGENTS_DEFAULTS_TOOL_FEEDBACK_MAX_ARGS_LENGTH"`
	SeparateMessages bool `json:"separate_messages" env:"COMPA_AGENTS_DEFAULTS_TOOL_FEEDBACK_SEPARATE_MESSAGES"`
}

type AgentDefaults struct {
	Workspace                 string             `json:"workspace"                        env:"COMPA_AGENTS_DEFAULTS_WORKSPACE"`
	RestrictToWorkspace       bool               `json:"restrict_to_workspace"            env:"COMPA_AGENTS_DEFAULTS_RESTRICT_TO_WORKSPACE"`
	AllowReadOutsideWorkspace bool               `json:"allow_read_outside_workspace"     env:"COMPA_AGENTS_DEFAULTS_ALLOW_READ_OUTSIDE_WORKSPACE"`
	ModelName                 string             `json:"model_name"                       env:"COMPA_AGENTS_DEFAULTS_MODEL_NAME"`  // default model selection: exact target "instance-id/model-id" or route name; empty means none
	ImageModel                string             `json:"image_model,omitempty"            env:"COMPA_AGENTS_DEFAULTS_IMAGE_MODEL"` // model selection turns carrying images are rerouted to; empty keeps the agent's model
	MaxTokens                 int                `json:"max_tokens"                       env:"COMPA_AGENTS_DEFAULTS_MAX_TOKENS"`
	ContextWindow             int                `json:"context_window,omitempty"         env:"COMPA_AGENTS_DEFAULTS_CONTEXT_WINDOW"`
	Temperature               *float64           `json:"temperature,omitempty"            env:"COMPA_AGENTS_DEFAULTS_TEMPERATURE"`
	MaxToolIterations         int                `json:"max_tool_iterations"              env:"COMPA_AGENTS_DEFAULTS_MAX_TOOL_ITERATIONS"`
	SummarizeMessageThreshold int                `json:"summarize_message_threshold"      env:"COMPA_AGENTS_DEFAULTS_SUMMARIZE_MESSAGE_THRESHOLD"`
	SummarizeTokenPercent     int                `json:"summarize_token_percent"          env:"COMPA_AGENTS_DEFAULTS_SUMMARIZE_TOKEN_PERCENT"`
	MaxMediaSize              int                `json:"max_media_size,omitempty"         env:"COMPA_AGENTS_DEFAULTS_MAX_MEDIA_SIZE"`
	Routing                   *RoutingConfig     `json:"routing,omitempty"`
	SteeringMode              string             `json:"steering_mode,omitempty"          env:"COMPA_AGENTS_DEFAULTS_STEERING_MODE"`      // "one-at-a-time" (default) or "all"
	MaxParallelTurns          int                `json:"max_parallel_turns,omitempty"     env:"COMPA_AGENTS_DEFAULTS_MAX_PARALLEL_TURNS"` // Max turns of different sessions at once (0 = default 4, 1 = sequential)
	SubTurn                   SubTurnConfig      `json:"subturn"`
	ToolFeedback              ToolFeedbackConfig `json:"tool_feedback,omitempty"`
	SplitOnMarker             bool               `json:"split_on_marker"                  env:"COMPA_AGENTS_DEFAULTS_SPLIT_ON_MARKER"` // split messages on <|[SPLIT]|> marker
	ContextManager            string             `json:"context_manager,omitempty"        env:"COMPA_AGENTS_DEFAULTS_CONTEXT_MANAGER"`
	ContextManagerConfig      json.RawMessage    `json:"context_manager_config,omitempty" env:"COMPA_AGENTS_DEFAULTS_CONTEXT_MANAGER_CONFIG"`
	TurnProfile               TurnProfileConfig  `json:"turn_profile,omitempty"`
	MaxLLMRetries             int                `json:"max_llm_retries,omitempty"        env:"COMPA_AGENTS_DEFAULTS_MAX_LLM_RETRIES"`
	LLMRetryBackoffSecs       int                `json:"llm_retry_backoff_secs,omitempty" env:"COMPA_AGENTS_DEFAULTS_LLM_RETRY_BACKOFF_SECS"`
}

const DefaultMaxMediaSize = 20 * 1024 * 1024 // 20 MB

func (d *AgentDefaults) GetMaxMediaSize() int {
	if d.MaxMediaSize > 0 {
		return d.MaxMediaSize
	}
	return DefaultMaxMediaSize
}

// GetToolFeedbackMaxArgsLength returns the max visible text length for tool argument previews.
func (d *AgentDefaults) GetToolFeedbackMaxArgsLength() int {
	if d.ToolFeedback.MaxArgsLength > 0 {
		return d.ToolFeedback.MaxArgsLength
	}
	return 300
}

// IsToolFeedbackEnabled returns true when tool feedback messages should be sent to the chat.
func (d *AgentDefaults) IsToolFeedbackEnabled() bool {
	return d.ToolFeedback.Enabled
}

// IsToolFeedbackSeparateMessagesEnabled returns true when each tool feedback
// update should be sent as its own chat message instead of editing a single
// in-place progress message.
func (d *AgentDefaults) IsToolFeedbackSeparateMessagesEnabled() bool {
	return d.ToolFeedback.SeparateMessages
}

// GetModelName returns the default model selection: an exact target
// "instance-id/model-id", a model route name, or "" when there is none.
func (d *AgentDefaults) GetModelName() string {
	return d.ModelName
}

// GroupTriggerConfig controls when the bot responds in group chats.
type GroupTriggerConfig struct {
	// MentionOnly answers in groups only when the bot is mentioned. A channel
	// entry that leaves it out gets true (ChannelsConfig.UnmarshalJSON).
	MentionOnly bool     `json:"mention_only"`
	Prefixes    []string `json:"prefixes,omitempty"`
}

// TypingConfig controls typing indicator behavior (Phase 10).
type TypingConfig struct {
	Enabled bool `json:"enabled,omitempty"`
}

// PlaceholderConfig controls placeholder message behavior (Phase 10).
type PlaceholderConfig struct {
	Enabled bool                `json:"enabled"`
	Text    FlexibleStringSlice `json:"text,omitempty"`
}

// GetRandomText returns a random placeholder text, or default if none set.
func (p *PlaceholderConfig) GetRandomText() string {
	if len(p.Text) == 0 {
		return "Thinking..."
	}
	if len(p.Text) == 1 {
		return p.Text[0]
	}
	idx := rand.Intn(len(p.Text))
	return p.Text[idx]
}

type StreamingConfig struct {
	Enabled         bool `json:"enabled,omitempty"`
	ThrottleSeconds int  `json:"throttle_seconds,omitempty"`
	MinGrowthChars  int  `json:"min_growth_chars,omitempty"`
}

func (c StreamingConfig) IsZero() bool {
	return !c.Enabled && c.ThrottleSeconds == 0 && c.MinGrowthChars == 0
}

func (c StreamingConfig) WithDefaults(throttleSeconds, minGrowthChars int) StreamingConfig {
	if c.Enabled {
		if c.ThrottleSeconds == 0 {
			c.ThrottleSeconds = throttleSeconds
		}
		if c.MinGrowthChars == 0 {
			c.MinGrowthChars = minGrowthChars
		}
	}
	return c
}

type WhatsAppSettings struct {
	BridgeURL        string `json:"bridge_url"         yaml:"-" env:"COMPA_CHANNELS_WHATSAPP_BRIDGE_URL"`
	UseNative        bool   `json:"use_native"         yaml:"-" env:"COMPA_CHANNELS_WHATSAPP_USE_NATIVE"`
	SessionStorePath string `json:"session_store_path" yaml:"-" env:"COMPA_CHANNELS_WHATSAPP_SESSION_STORE_PATH"`
	// Chats selects which chats the native client takes as input: "self",
	// "allowed" or "all" (see the WhatsAppChats* constants).
	Chats string `json:"chats,omitempty" yaml:"-" env:"COMPA_CHANNELS_WHATSAPP_CHATS"`
}

type TelegramSettings struct {
	Token             SecureString    `json:"token,omitzero"       yaml:"token,omitempty" env:"COMPA_CHANNELS_TELEGRAM_TOKEN"`
	BaseURL           string          `json:"base_url"             yaml:"-"               env:"COMPA_CHANNELS_TELEGRAM_BASE_URL"`
	Proxy             string          `json:"proxy"                yaml:"-"               env:"COMPA_CHANNELS_TELEGRAM_PROXY"`
	Streaming         StreamingConfig `json:"streaming,omitzero"   yaml:"-"`
	UseMarkdownV2     bool            `json:"use_markdown_v2"      yaml:"-"               env:"COMPA_CHANNELS_TELEGRAM_USE_MARKDOWN_V2"`
	MediaGroupDelayMS int             `json:"media_group_delay_ms" yaml:"-"               env:"COMPA_CHANNELS_TELEGRAM_MEDIA_GROUP_DELAY_MS"`
}

type FeishuSettings struct {
	AppID               string              `json:"app_id"                      yaml:"-"                            env:"COMPA_CHANNELS_FEISHU_APP_ID"`
	AppSecret           SecureString        `json:"app_secret,omitzero"         yaml:"app_secret,omitempty"         env:"COMPA_CHANNELS_FEISHU_APP_SECRET"`
	EncryptKey          SecureString        `json:"encrypt_key,omitzero"        yaml:"encrypt_key,omitempty"        env:"COMPA_CHANNELS_FEISHU_ENCRYPT_KEY"`
	VerificationToken   SecureString        `json:"verification_token,omitzero" yaml:"verification_token,omitempty" env:"COMPA_CHANNELS_FEISHU_VERIFICATION_TOKEN"`
	RandomReactionEmoji FlexibleStringSlice `json:"random_reaction_emoji"       yaml:"-"                            env:"COMPA_CHANNELS_FEISHU_RANDOM_REACTION_EMOJI"`
	IsLark              bool                `json:"is_lark"                     yaml:"-"                            env:"COMPA_CHANNELS_FEISHU_IS_LARK"`
}

type DiscordSettings struct {
	Token SecureString `json:"token,omitzero" yaml:"token,omitempty" env:"COMPA_CHANNELS_DISCORD_TOKEN"`
	Proxy string       `json:"proxy"          yaml:"-"               env:"COMPA_CHANNELS_DISCORD_PROXY"`
}

type MaixCamSettings struct {
	Host  string       `json:"host"           yaml:"-"               env:"COMPA_CHANNELS_MAIXCAM_HOST"`
	Port  int          `json:"port"           yaml:"-"               env:"COMPA_CHANNELS_MAIXCAM_PORT"`
	Token SecureString `json:"token,omitzero" yaml:"token,omitempty" env:"COMPA_CHANNELS_MAIXCAM_TOKEN"`
}

type QQSettings struct {
	AppID                string       `json:"app_id"                   yaml:"-"                    env:"COMPA_CHANNELS_QQ_APP_ID"`
	AppSecret            SecureString `json:"app_secret,omitzero"      yaml:"app_secret,omitempty" env:"COMPA_CHANNELS_QQ_APP_SECRET"`
	MaxMessageLength     int          `json:"max_message_length"       yaml:"-"                    env:"COMPA_CHANNELS_QQ_MAX_MESSAGE_LENGTH"`
	MaxBase64FileSizeMiB int64        `json:"max_base64_file_size_mib" yaml:"-"                    env:"COMPA_CHANNELS_QQ_MAX_BASE64_FILE_SIZE_MIB"`
	SendMarkdown         bool         `json:"send_markdown"            yaml:"-"                    env:"COMPA_CHANNELS_QQ_SEND_MARKDOWN"`
}

type DingTalkSettings struct {
	ClientID     string       `json:"client_id"              yaml:"-"                       env:"COMPA_CHANNELS_DINGTALK_CLIENT_ID"`
	ClientSecret SecureString `json:"client_secret,omitzero" yaml:"client_secret,omitempty" env:"COMPA_CHANNELS_DINGTALK_CLIENT_SECRET"`
}

type SlackSettings struct {
	BotToken SecureString `json:"bot_token,omitzero" yaml:"bot_token,omitempty" env:"COMPA_CHANNELS_SLACK_BOT_TOKEN"`
	AppToken SecureString `json:"app_token,omitzero" yaml:"app_token,omitempty" env:"COMPA_CHANNELS_SLACK_APP_TOKEN"`
}

type MatrixSettings struct {
	Homeserver         string       `json:"homeserver"                     yaml:"-"                      env:"COMPA_CHANNELS_MATRIX_HOMESERVER"`
	UserID             string       `json:"user_id"                        yaml:"-"                      env:"COMPA_CHANNELS_MATRIX_USER_ID"`
	AccessToken        SecureString `json:"access_token,omitzero"          yaml:"access_token,omitempty" env:"COMPA_CHANNELS_MATRIX_ACCESS_TOKEN"`
	DeviceID           string       `json:"device_id,omitempty"            yaml:"-"`
	JoinOnInvite       bool         `json:"join_on_invite"                 yaml:"-"`
	MessageFormat      string       `json:"message_format,omitempty"       yaml:"-"`
	CryptoDatabasePath string       `json:"crypto_database_path,omitempty" yaml:"-"`
	// CryptoPassphrase protects the end-to-end encryption keys.
	CryptoPassphrase SecureString `json:"crypto_passphrase,omitzero" yaml:"crypto_passphrase,omitempty"`
}

// DeltaChatSettings configures the Delta Chat channel. Delta Chat is an
// email-based, end-to-end encrypted messenger; Compa talks to a local
// `deltachat-rpc-server` process over JSON-RPC (stdio).
//
// Email is the only required setting. A full address selects an already
// configured account in DataDir; a first-run marker such as "@nine.testrun.org"
// creates a chatmail account and tells the user which full email to save.
// Mailbox credentials stay in the Delta Chat account store. DisplayName and
// AvatarImage are optional profile settings applied on startup. Password, with
// the optional IMAP/SMTP settings, is for an email account Compa
// configures itself rather than one already in the account store.
type DeltaChatSettings struct {
	Email          string       `json:"email"                     yaml:"-"                  env:"COMPA_CHANNELS_DELTACHAT_EMAIL"`
	Password       SecureString `json:"password,omitzero"         yaml:"password,omitempty" env:"COMPA_CHANNELS_DELTACHAT_PASSWORD"`
	DisplayName    string       `json:"display_name,omitempty"    yaml:"-"                  env:"COMPA_CHANNELS_DELTACHAT_DISPLAY_NAME"`
	AvatarImage    string       `json:"avatar_image,omitempty"    yaml:"-"                  env:"COMPA_CHANNELS_DELTACHAT_AVATAR_IMAGE"`
	DataDir        string       `json:"data_dir,omitempty"        yaml:"-"                  env:"COMPA_CHANNELS_DELTACHAT_DATA_DIR"`
	RPCServerPath  string       `json:"rpc_server_path,omitempty" yaml:"-"                  env:"COMPA_CHANNELS_DELTACHAT_RPC_SERVER_PATH"`
	InviteLink     string       `json:"invite_link,omitempty"     yaml:"-"                  env:"COMPA_CHANNELS_DELTACHAT_INVITE_LINK"`
	AllowCrosspost bool         `json:"allow_crosspost,omitempty" yaml:"-"                  env:"COMPA_CHANNELS_DELTACHAT_ALLOW_CROSSPOST"`
	IMAPServer     string       `json:"imap_server,omitempty"     yaml:"-"`
	IMAPPort       int          `json:"imap_port,omitempty"       yaml:"-"`
	SMTPServer     string       `json:"smtp_server,omitempty"     yaml:"-"`
	SMTPPort       int          `json:"smtp_port,omitempty"       yaml:"-"`
}

// LINESettings configures the LINE channel. Its webhook is served by the
// gateway's own server, at WebhookPath.
type LINESettings struct {
	ChannelSecret      SecureString `json:"channel_secret,omitzero"       yaml:"channel_secret,omitempty"       env:"COMPA_CHANNELS_LINE_CHANNEL_SECRET"`
	ChannelAccessToken SecureString `json:"channel_access_token,omitzero" yaml:"channel_access_token,omitempty" env:"COMPA_CHANNELS_LINE_CHANNEL_ACCESS_TOKEN"`
	WebhookPath        string       `json:"webhook_path"                  yaml:"-"                              env:"COMPA_CHANNELS_LINE_WEBHOOK_PATH"`
}

type OneBotSettings struct {
	WSUrl             string       `json:"ws_url"                yaml:"-"                      env:"COMPA_CHANNELS_ONEBOT_WS_URL"`
	AccessToken       SecureString `json:"access_token,omitzero" yaml:"access_token,omitempty" env:"COMPA_CHANNELS_ONEBOT_ACCESS_TOKEN"`
	ReconnectInterval int          `json:"reconnect_interval"    yaml:"-"                      env:"COMPA_CHANNELS_ONEBOT_RECONNECT_INTERVAL"`
}

type WeComGroupConfig struct {
	AllowFrom FlexibleStringSlice `json:"allow_from,omitempty"`
}

type WeComSettings struct {
	BotID               string          `json:"bot_id"                  yaml:"-"                env:"COMPA_CHANNELS_WECOM_BOT_ID"`
	Secret              SecureString    `json:"secret,omitzero"         yaml:"secret,omitempty" env:"COMPA_CHANNELS_WECOM_SECRET"`
	WebSocketURL        string          `json:"websocket_url,omitempty" yaml:"-"                env:"COMPA_CHANNELS_WECOM_WEBSOCKET_URL"`
	SendThinkingMessage bool            `json:"send_thinking_message"   yaml:"-"                env:"COMPA_CHANNELS_WECOM_SEND_THINKING_MESSAGE"`
	Streaming           StreamingConfig `json:"streaming,omitzero"      yaml:"-"`
}

func (c *WeComSettings) SetSecret(secret string) {
	c.Secret = *NewSecureString(secret)
}

type WeixinSettings struct {
	Token      SecureString `json:"token,omitzero"       yaml:"token,omitempty" env:"COMPA_CHANNELS_WEIXIN_TOKEN"`
	AccountID  string       `json:"account_id,omitempty" yaml:"-"               env:"COMPA_CHANNELS_WEIXIN_ACCOUNT_ID"`
	BaseURL    string       `json:"base_url"             yaml:"-"               env:"COMPA_CHANNELS_WEIXIN_BASE_URL"`
	CDNBaseURL string       `json:"cdn_base_url"         yaml:"-"               env:"COMPA_CHANNELS_WEIXIN_CDN_BASE_URL"`
	Proxy      string       `json:"proxy"                yaml:"-"               env:"COMPA_CHANNELS_WEIXIN_PROXY"`
}

// SetToken sets the Weixin token and marks it as dirty for security saving
func (c *WeixinSettings) SetToken(token string) {
	c.Token = *NewSecureString(token)
}

type WebChatSettings struct {
	Token           SecureString    `json:"token,omitzero"              yaml:"token,omitempty" env:"COMPA_CHANNELS_WEB_TOKEN"`
	AllowTokenQuery bool            `json:"allow_token_query,omitempty" yaml:"-"`
	AllowOrigins    []string        `json:"allow_origins,omitempty"     yaml:"-"`
	Streaming       StreamingConfig `json:"streaming,omitzero"          yaml:"-"`
	PingInterval    int             `json:"ping_interval,omitempty"     yaml:"-"`
	ReadTimeout     int             `json:"read_timeout,omitempty"      yaml:"-"`
	WriteTimeout    int             `json:"write_timeout,omitempty"     yaml:"-"`
	MaxConnections  int             `json:"max_connections,omitempty"   yaml:"-"`
}

// SetToken sets the web chat token and marks it as dirty for security saving
func (c *WebChatSettings) SetToken(token string) {
	c.Token = *NewSecureString(token)
}

type WebChatClientSettings struct {
	URL          string       `json:"url"                     yaml:"-"               env:"COMPA_CHANNELS_WEB_CLIENT_URL"`
	Token        SecureString `json:"token,omitzero"          yaml:"token,omitempty" env:"COMPA_CHANNELS_WEB_CLIENT_TOKEN"`
	SessionID    string       `json:"session_id,omitempty"    yaml:"-"`
	PingInterval int          `json:"ping_interval,omitempty" yaml:"-"`
	ReadTimeout  int          `json:"read_timeout,omitempty"  yaml:"-"`
}

type IRCSettings struct {
	Server           string              `json:"server"                     yaml:"-"                           env:"COMPA_CHANNELS_IRC_SERVER"`
	TLS              bool                `json:"tls"                        yaml:"-"                           env:"COMPA_CHANNELS_IRC_TLS"`
	Nick             string              `json:"nick"                       yaml:"-"                           env:"COMPA_CHANNELS_IRC_NICK"`
	User             string              `json:"user,omitempty"             yaml:"-"                           env:"COMPA_CHANNELS_IRC_USER"`
	RealName         string              `json:"real_name,omitempty"        yaml:"-"`
	Password         SecureString        `json:"password,omitzero"          yaml:"password,omitempty"          env:"COMPA_CHANNELS_IRC_PASSWORD"`
	NickServPassword SecureString        `json:"nickserv_password,omitzero" yaml:"nickserv_password,omitempty" env:"COMPA_CHANNELS_IRC_NICKSERV_PASSWORD"`
	SASLUser         string              `json:"sasl_user"                  yaml:"-"                           env:"COMPA_CHANNELS_IRC_SASL_USER"`
	SASLPassword     SecureString        `json:"sasl_password,omitzero"     yaml:"sasl_password,omitempty"     env:"COMPA_CHANNELS_IRC_SASL_PASSWORD"`
	Channels         FlexibleStringSlice `json:"channels"                   yaml:"-"                           env:"COMPA_CHANNELS_IRC_CHANNELS"`
	RequestCaps      FlexibleStringSlice `json:"request_caps,omitempty"     yaml:"-"`
}

type VKSettings struct {
	Token   SecureString `json:"token,omitzero" yaml:"token,omitempty" env:"COMPA_CHANNELS_VK_TOKEN"`
	GroupID int          `json:"group_id"       yaml:"-"               env:"COMPA_CHANNELS_VK_GROUP_ID"`
}

func (c *VKSettings) SetToken(token string) {
	c.Token = *NewSecureString(token)
}

// TeamsWebhookSettings configures the output-only Microsoft Teams webhook channel.
// Multiple webhook targets can be configured and selected via ChatID at send time.
type TeamsWebhookSettings struct {
	Webhooks map[string]TeamsWebhookTarget `json:"webhooks" yaml:"webhooks,omitempty"`
}

// TeamsWebhookTarget represents a single Teams webhook destination.
type TeamsWebhookTarget struct {
	WebhookURL SecureString `json:"webhook_url,omitzero" yaml:"webhook_url,omitempty"`
	Title      string       `json:"title,omitempty"      yaml:"-"`
}

type MQTTSettings struct {
	Broker      string       `json:"broker"                 yaml:"-"                  env:"COMPA_CHANNELS_MQTT_BROKER"`
	AgentID     string       `json:"agent_id"               yaml:"-"                  env:"COMPA_CHANNELS_MQTT_AGENT_ID"`
	TopicPrefix string       `json:"topic_prefix,omitempty" yaml:"-"                  env:"COMPA_CHANNELS_MQTT_TOPIC_PREFIX"`
	Username    SecureString `json:"username,omitzero"      yaml:"username,omitempty" env:"COMPA_CHANNELS_MQTT_USERNAME"`
	Password    SecureString `json:"password,omitzero"      yaml:"password,omitempty" env:"COMPA_CHANNELS_MQTT_PASSWORD"`
	ClientID    string       `json:"client_id,omitempty"    yaml:"-"                  env:"COMPA_CHANNELS_MQTT_CLIENT_ID"`
	KeepAlive   int          `json:"keep_alive,omitempty"   yaml:"-"                  env:"COMPA_CHANNELS_MQTT_KEEP_ALIVE"`
	QoS         int          `json:"qos,omitempty"          yaml:"-"                  env:"COMPA_CHANNELS_MQTT_QOS"`
	// TLSInsecureSkipVerify turns off verification of the broker's TLS
	// certificate.
	TLSInsecureSkipVerify bool `json:"tls_insecure_skip_verify" yaml:"-" env:"COMPA_CHANNELS_MQTT_TLS_INSECURE_SKIP_VERIFY"`
}

// SlackWebhookSettings configures the output-only Slack webhook channel.
type SlackWebhookSettings struct {
	Webhooks map[string]SlackWebhookTarget `json:"webhooks" yaml:"webhooks,omitempty"`
}

// SlackWebhookTarget represents a single Slack Incoming Webhook destination.
type SlackWebhookTarget struct {
	WebhookURL SecureString `json:"webhook_url,omitzero" yaml:"webhook_url,omitempty"`
	Username   string       `json:"username,omitempty"   yaml:"-"`
	IconEmoji  string       `json:"icon_emoji,omitempty" yaml:"-"`
}

type HeartbeatConfig struct {
	Enabled  bool `json:"enabled"  env:"COMPA_HEARTBEAT_ENABLED"`
	Interval int  `json:"interval" env:"COMPA_HEARTBEAT_INTERVAL"` // minutes, min 5
}

type DevicesConfig struct {
	Enabled    bool `json:"enabled"     env:"COMPA_DEVICES_ENABLED"`
	MonitorUSB bool `json:"monitor_usb" env:"COMPA_DEVICES_MONITOR_USB"`
}

// VoiceConfig configures voice conversation. Speech-to-text and
// text-to-speech each run on an exact provider instance target; voice runs
// with either or both: speech-to-text alone dictates, text-to-speech alone
// speaks the replies.
type VoiceConfig struct {
	Enabled bool `json:"enabled"`
	// Mode is "cascade" (push-to-talk) or "live" (hands-free).
	Mode string `json:"mode,omitempty"`
	// STTTarget is the exact target "instance-id/model-id" that transcribes
	// speech.
	STTTarget string `json:"stt_target,omitempty"`
	// STTViaChat opts STTTarget in as a chat model that transcribes the audio
	// it is sent (llmgw-core's chat transcription adapter) instead of a model
	// serving audio_transcriptions. It is never inferred, and holds only for
	// a model whose catalog declares audio input.
	STTViaChat bool `json:"stt_via_chat,omitempty"`
	// TTSTarget is the exact target "instance-id/model-id" that synthesizes
	// speech.
	TTSTarget string `json:"tts_target,omitempty"`
	// TTSVoice is the synthesis voice; empty uses the provider's default.
	TTSVoice          string `json:"tts_voice,omitempty"`
	EchoTranscription bool   `json:"echo_transcription" env:"COMPA_VOICE_ECHO_TRANSCRIPTION"`
}

type ToolDiscoveryConfig struct {
	Enabled          bool `json:"enabled"            env:"COMPA_TOOLS_DISCOVERY_ENABLED"`
	TTL              int  `json:"ttl"                env:"COMPA_TOOLS_DISCOVERY_TTL"`
	MaxSearchResults int  `json:"max_search_results" env:"COMPA_MAX_SEARCH_RESULTS"`
	UseBM25          bool `json:"use_bm25"           env:"COMPA_TOOLS_DISCOVERY_USE_BM25"`
	UseRegex         bool `json:"use_regex"          env:"COMPA_TOOLS_DISCOVERY_USE_REGEX"`
}

type ToolConfig struct {
	Enabled bool `json:"enabled" yaml:"-" env:"ENABLED"`
}

type MessageToolsConfig struct {
	ToolConfig `yaml:"-" envPrefix:"COMPA_TOOLS_MESSAGE_"`

	MediaEnabled bool `json:"media_enabled" yaml:"-" env:"COMPA_TOOLS_MESSAGE_MEDIA_ENABLED"`
	// Targets is where the message tool may send: "current_chat" or "any".
	Targets string `json:"targets" yaml:"-" env:"COMPA_TOOLS_MESSAGE_TARGETS"`
}

type BraveConfig struct {
	Enabled    bool          `json:"enabled"           yaml:"-"                  env:"COMPA_TOOLS_WEB_BRAVE_ENABLED"`
	APIKeys    SecureStrings `json:"api_keys,omitzero" yaml:"api_keys,omitempty" env:"COMPA_TOOLS_WEB_BRAVE_API_KEYS"`
	MaxResults int           `json:"max_results"       yaml:"-"                  env:"COMPA_TOOLS_WEB_BRAVE_MAX_RESULTS"`
}

// APIKey returns the Brave API key
func (c *BraveConfig) APIKey() string {
	if len(c.APIKeys) == 0 {
		return ""
	}
	return c.APIKeys[0].String()
}

// SetAPIKey sets the Brave API key
func (c *BraveConfig) SetAPIKey(key string) {
	c.APIKeys = SimpleSecureStrings(key)
}

func (c *BraveConfig) SetAPIKeys(keys []string) {
	c.APIKeys = SimpleSecureStrings(keys...)
}

type TavilyConfig struct {
	Enabled    bool          `json:"enabled"           yaml:"-"                  env:"COMPA_TOOLS_WEB_TAVILY_ENABLED"`
	APIKeys    SecureStrings `json:"api_keys,omitzero" yaml:"api_keys,omitempty" env:"COMPA_TOOLS_WEB_TAVILY_API_KEYS"`
	BaseURL    string        `json:"base_url"          yaml:"-"                  env:"COMPA_TOOLS_WEB_TAVILY_BASE_URL"`
	MaxResults int           `json:"max_results"       yaml:"-"                  env:"COMPA_TOOLS_WEB_TAVILY_MAX_RESULTS"`
}

// APIKey returns the Tavily API key
func (c *TavilyConfig) APIKey() string {
	if len(c.APIKeys) == 0 {
		return ""
	}
	return c.APIKeys[0].String()
}

// SetAPIKey sets the Tavily API key
func (c *TavilyConfig) SetAPIKey(key string) {
	c.APIKeys = SimpleSecureStrings(key)
}

// SetAPIKeys sets the Tavily API keys
func (c *TavilyConfig) SetAPIKeys(keys []string) {
	c.APIKeys = make(SecureStrings, len(keys))
	for i, k := range keys {
		c.APIKeys[i] = NewSecureString(k)
	}
}

type KagiConfig struct {
	Enabled    bool          `json:"enabled"           yaml:"-"                  env:"COMPA_TOOLS_WEB_KAGI_ENABLED"`
	APIKeys    SecureStrings `json:"api_keys,omitzero" yaml:"api_keys,omitempty" env:"COMPA_TOOLS_WEB_KAGI_API_KEYS"`
	BaseURL    string        `json:"base_url"          yaml:"-"                  env:"COMPA_TOOLS_WEB_KAGI_BASE_URL"`
	MaxResults int           `json:"max_results"       yaml:"-"                  env:"COMPA_TOOLS_WEB_KAGI_MAX_RESULTS"`
}

// APIKey returns the Kagi API key
func (c *KagiConfig) APIKey() string {
	if len(c.APIKeys) == 0 {
		return ""
	}
	return c.APIKeys[0].String()
}

// SetAPIKey sets the Kagi API key
func (c *KagiConfig) SetAPIKey(key string) {
	c.APIKeys = SimpleSecureStrings(key)
}

// SetAPIKeys sets the Kagi API keys
func (c *KagiConfig) SetAPIKeys(keys []string) {
	c.APIKeys = SimpleSecureStrings(keys...)
}

type DuckDuckGoConfig struct {
	Enabled    bool `json:"enabled"     env:"COMPA_TOOLS_WEB_DUCKDUCKGO_ENABLED"`
	MaxResults int  `json:"max_results" env:"COMPA_TOOLS_WEB_DUCKDUCKGO_MAX_RESULTS"`
}

type SogouConfig struct {
	Enabled    bool `json:"enabled"     env:"COMPA_TOOLS_WEB_SOGOU_ENABLED"`
	MaxResults int  `json:"max_results" env:"COMPA_TOOLS_WEB_SOGOU_MAX_RESULTS"`
}

type GeminiSearchConfig struct {
	Enabled    bool         `json:"enabled"          yaml:"-"                 env:"COMPA_TOOLS_WEB_GEMINI_ENABLED"`
	APIKey     SecureString `json:"api_key,omitzero" yaml:"api_key,omitempty" env:"COMPA_TOOLS_WEB_GEMINI_API_KEY"`
	Model      string       `json:"model"            yaml:"-"                 env:"COMPA_TOOLS_WEB_GEMINI_MODEL"`
	MaxResults int          `json:"max_results"      yaml:"-"                 env:"COMPA_TOOLS_WEB_GEMINI_MAX_RESULTS"`
}

type PerplexityConfig struct {
	Enabled    bool          `json:"enabled"           yaml:"-"                  env:"COMPA_TOOLS_WEB_PERPLEXITY_ENABLED"`
	APIKeys    SecureStrings `json:"api_keys,omitzero" yaml:"api_keys,omitempty" env:"COMPA_TOOLS_WEB_PERPLEXITY_API_KEYS"`
	MaxResults int           `json:"max_results"       yaml:"-"                  env:"COMPA_TOOLS_WEB_PERPLEXITY_MAX_RESULTS"`
}

// APIKey returns the Perplexity API key
func (c *PerplexityConfig) APIKey() string {
	if len(c.APIKeys) == 0 {
		return ""
	}
	return c.APIKeys[0].String()
}

// SetAPIKey sets the Perplexity API key
func (c *PerplexityConfig) SetAPIKey(key string) {
	c.APIKeys = SimpleSecureStrings(key)
}

type SearXNGConfig struct {
	Enabled    bool   `json:"enabled"     env:"COMPA_TOOLS_WEB_SEARXNG_ENABLED"`
	BaseURL    string `json:"base_url"    env:"COMPA_TOOLS_WEB_SEARXNG_BASE_URL"`
	MaxResults int    `json:"max_results" env:"COMPA_TOOLS_WEB_SEARXNG_MAX_RESULTS"`
}

type GLMSearchConfig struct {
	Enabled bool         `json:"enabled"          yaml:"-"                 env:"COMPA_TOOLS_WEB_GLM_ENABLED"`
	APIKey  SecureString `json:"api_key,omitzero" yaml:"api_key,omitempty" env:"COMPA_TOOLS_WEB_GLM_API_KEY"`
	BaseURL string       `json:"base_url"         yaml:"-"                 env:"COMPA_TOOLS_WEB_GLM_BASE_URL"`
	// SearchEngine specifies the search backend: "search_std" (default),
	// "search_pro", "search_pro_sogou", or "search_pro_quark".
	SearchEngine string `json:"search_engine" yaml:"-" env:"COMPA_TOOLS_WEB_GLM_SEARCH_ENGINE"`
	MaxResults   int    `json:"max_results"   yaml:"-" env:"COMPA_TOOLS_WEB_GLM_MAX_RESULTS"`
}

type BaiduSearchConfig struct {
	Enabled    bool         `json:"enabled"          yaml:"-"                 env:"COMPA_TOOLS_WEB_BAIDU_ENABLED"`
	APIKey     SecureString `json:"api_key,omitzero" yaml:"api_key,omitempty" env:"COMPA_TOOLS_WEB_BAIDU_API_KEY"`
	BaseURL    string       `json:"base_url"         yaml:"-"                 env:"COMPA_TOOLS_WEB_BAIDU_BASE_URL"`
	MaxResults int          `json:"max_results"      yaml:"-"                 env:"COMPA_TOOLS_WEB_BAIDU_MAX_RESULTS"`
}

type WebToolsConfig struct {
	ToolConfig  `                   yaml:"-"                      envPrefix:"COMPA_TOOLS_WEB_"`
	Brave       BraveConfig        `yaml:"brave,omitempty"                                        json:"brave"`
	Tavily      TavilyConfig       `yaml:"tavily,omitempty"                                       json:"tavily"`
	Kagi        KagiConfig         `yaml:"kagi,omitempty"                                         json:"kagi"`
	Sogou       SogouConfig        `yaml:"-"                                                      json:"sogou"`
	DuckDuckGo  DuckDuckGoConfig   `yaml:"-"                                                      json:"duckduckgo"`
	Gemini      GeminiSearchConfig `yaml:"gemini,omitempty"                                       json:"gemini"`
	Perplexity  PerplexityConfig   `yaml:"perplexity,omitempty"                                   json:"perplexity"`
	SearXNG     SearXNGConfig      `yaml:"-"                                                      json:"searxng"`
	GLMSearch   GLMSearchConfig    `yaml:"glm_search,omitempty"                                   json:"glm_search"`
	BaiduSearch BaiduSearchConfig  `yaml:"baidu_search,omitempty"                                 json:"baidu_search"`
	Provider    string             `yaml:"-"                                                      json:"provider,omitempty" env:"COMPA_TOOLS_WEB_PROVIDER"`
	// PreferNative controls whether to use provider-native web search when
	// the active LLM supports it (e.g. OpenAI web_search_preview). When true,
	// the client-side web_search tool is hidden to avoid duplicate search surfaces,
	// and the provider's built-in search is used instead. Falls back to client-side
	// search when the provider does not support native search.
	PreferNative bool `yaml:"-" json:"prefer_native" env:"COMPA_TOOLS_WEB_PREFER_NATIVE"`
	// Proxy is an optional proxy URL for web tools (http/https/socks5/socks5h).
	// For authenticated proxies, prefer HTTP_PROXY/HTTPS_PROXY env vars instead of embedding credentials in config.
	Proxy                string              `yaml:"-" json:"proxy,omitempty"                  env:"COMPA_TOOLS_WEB_PROXY"`
	FetchLimitBytes      int64               `yaml:"-" json:"fetch_limit_bytes,omitempty"      env:"COMPA_TOOLS_WEB_FETCH_LIMIT_BYTES"`
	Format               string              `yaml:"-" json:"format,omitempty"                 env:"COMPA_TOOLS_WEB_FORMAT"`
	PrivateHostWhitelist FlexibleStringSlice `yaml:"-" json:"private_host_whitelist,omitempty" env:"COMPA_TOOLS_WEB_PRIVATE_HOST_WHITELIST"`
}

type CronToolsConfig struct {
	ToolConfig `envPrefix:"COMPA_TOOLS_CRON_"`
	// 0 means no timeout.
	ExecTimeoutMinutes    int      `json:"exec_timeout_minutes"    env:"COMPA_TOOLS_CRON_EXEC_TIMEOUT_MINUTES"`
	AllowCommand          bool     `json:"allow_command"           env:"COMPA_TOOLS_CRON_ALLOW_COMMAND"`
	CommandAllowedRemotes []string `json:"command_allowed_remotes" env:"COMPA_TOOLS_CRON_COMMAND_ALLOWED_REMOTES"`
}

type ExecConfig struct {
	ToolConfig          `         envPrefix:"COMPA_TOOLS_EXEC_"`
	EnableDenyPatterns  bool     `                                 json:"enable_deny_patterns"  env:"COMPA_TOOLS_EXEC_ENABLE_DENY_PATTERNS"`
	AllowRemote         bool     `                                 json:"allow_remote"          env:"COMPA_TOOLS_EXEC_ALLOW_REMOTE"`
	CustomDenyPatterns  []string `                                 json:"custom_deny_patterns"  env:"COMPA_TOOLS_EXEC_CUSTOM_DENY_PATTERNS"`
	CustomAllowPatterns []string `                                 json:"custom_allow_patterns" env:"COMPA_TOOLS_EXEC_CUSTOM_ALLOW_PATTERNS"`
	TimeoutSeconds      int      `                                 json:"timeout_seconds"       env:"COMPA_TOOLS_EXEC_TIMEOUT_SECONDS"` // 0 means use default (60s)
}

type SkillsToolsConfig struct {
	ToolConfig            `                       yaml:"-"                    envPrefix:"COMPA_TOOLS_SKILLS_"`
	Registries            SkillsRegistriesConfig `yaml:"registries,omitempty"                                    json:"registries"`
	MaxConcurrentSearches int                    `yaml:"-"                    json:"max_concurrent_searches" env:"COMPA_TOOLS_SKILLS_MAX_CONCURRENT_SEARCHES"`
	SearchCache           SearchCacheConfig      `yaml:"-"                    json:"search_cache"`
}

type MediaCleanupConfig struct {
	ToolConfig `    envPrefix:"COMPA_MEDIA_CLEANUP_"`
	MaxAge     int `                                    json:"max_age_minutes"  env:"COMPA_MEDIA_CLEANUP_MAX_AGE"`
	Interval   int `                                    json:"interval_minutes" env:"COMPA_MEDIA_CLEANUP_INTERVAL"`
}

type ReadFileToolConfig struct {
	Enabled         bool   `json:"enabled"`
	Mode            string `json:"mode"`
	MaxReadFileSize int    `json:"max_read_file_size"`
}

const (
	ReadFileModeBytes = "bytes"
	ReadFileModeLines = "lines"
)

func (c ReadFileToolConfig) EffectiveMode() string {
	switch strings.ToLower(strings.TrimSpace(c.Mode)) {
	case ReadFileModeLines:
		return ReadFileModeLines
	case "", ReadFileModeBytes:
		return ReadFileModeBytes
	default:
		return ReadFileModeBytes
	}
}

type ToolsConfig struct {
	AllowReadPaths  []string `json:"allow_read_paths"  yaml:"-" env:"COMPA_TOOLS_ALLOW_READ_PATHS"`
	AllowWritePaths []string `json:"allow_write_paths" yaml:"-" env:"COMPA_TOOLS_ALLOW_WRITE_PATHS"`
	// FilterSensitiveData controls whether to filter sensitive values (API keys,
	// tokens, secrets) from tool results before sending to the LLM.
	// Default: true (enabled)
	FilterSensitiveData bool `json:"filter_sensitive_data" yaml:"-" env:"COMPA_TOOLS_FILTER_SENSITIVE_DATA"`
	// FilterMinLength is the minimum content length required for filtering.
	// Content shorter than this will be returned unchanged for performance.
	// Default: 8
	FilterMinLength int                `json:"filter_min_length" yaml:"-"                env:"COMPA_TOOLS_FILTER_MIN_LENGTH"`
	Approval        approval.Policy    `json:"approval"          yaml:"-"`
	Web             WebToolsConfig     `json:"web"               yaml:"web,omitempty"`
	Cron            CronToolsConfig    `json:"cron"              yaml:"-"`
	Exec            ExecConfig         `json:"exec"              yaml:"-"`
	Skills          SkillsToolsConfig  `json:"skills"            yaml:"skills,omitempty"`
	MediaCleanup    MediaCleanupConfig `json:"media_cleanup"     yaml:"-"`
	MCP             MCPConfig          `json:"mcp"               yaml:"-"`
	AppendFile      ToolConfig         `json:"append_file"       yaml:"-"                                                       envPrefix:"COMPA_TOOLS_APPEND_FILE_"`
	EditFile        ToolConfig         `json:"edit_file"         yaml:"-"                                                       envPrefix:"COMPA_TOOLS_EDIT_FILE_"`
	FindSkills      ToolConfig         `json:"find_skills"       yaml:"-"                                                       envPrefix:"COMPA_TOOLS_FIND_SKILLS_"`
	I2C             ToolConfig         `json:"i2c"               yaml:"-"                                                       envPrefix:"COMPA_TOOLS_I2C_"`
	InstallSkill    ToolConfig         `json:"install_skill"     yaml:"-"                                                       envPrefix:"COMPA_TOOLS_INSTALL_SKILL_"`
	ListDir         ToolConfig         `json:"list_dir"          yaml:"-"                                                       envPrefix:"COMPA_TOOLS_LIST_DIR_"`
	LoadImage       ToolConfig         `json:"load_image"        yaml:"-"                                                       envPrefix:"COMPA_TOOLS_LOAD_IMAGE_"`
	Message         MessageToolsConfig `json:"message"           yaml:"-"`
	ReadFile        ReadFileToolConfig `json:"read_file"         yaml:"-"                                                       envPrefix:"COMPA_TOOLS_READ_FILE_"`
	Serial          ToolConfig         `json:"serial"            yaml:"-"                                                       envPrefix:"COMPA_TOOLS_SERIAL_"`
	SendFile        ToolConfig         `json:"send_file"         yaml:"-"                                                       envPrefix:"COMPA_TOOLS_SEND_FILE_"`
	SendTTS         ToolConfig         `json:"send_tts"          yaml:"-"                                                       envPrefix:"COMPA_TOOLS_SEND_TTS_"`
	Spawn           ToolConfig         `json:"spawn"             yaml:"-"                                                       envPrefix:"COMPA_TOOLS_SPAWN_"`
	SpawnStatus     ToolConfig         `json:"spawn_status"      yaml:"-"                                                       envPrefix:"COMPA_TOOLS_SPAWN_STATUS_"`
	SPI             ToolConfig         `json:"spi"               yaml:"-"                                                       envPrefix:"COMPA_TOOLS_SPI_"`
	Subagent        ToolConfig         `json:"subagent"          yaml:"-"                                                       envPrefix:"COMPA_TOOLS_SUBAGENT_"`
	WebFetch        ToolConfig         `json:"web_fetch"         yaml:"-"                                                       envPrefix:"COMPA_TOOLS_WEB_FETCH_"`
	WriteFile       ToolConfig         `json:"write_file"        yaml:"-"                                                       envPrefix:"COMPA_TOOLS_WRITE_FILE_"`
}

// IsFilterSensitiveDataEnabled returns true if sensitive data filtering is enabled
func (c *ToolsConfig) IsFilterSensitiveDataEnabled() bool {
	return c.FilterSensitiveData
}

// GetFilterMinLength returns the minimum content length for filtering (default: 8)
func (c *ToolsConfig) GetFilterMinLength() int {
	if c.FilterMinLength <= 0 {
		return 8
	}
	return c.FilterMinLength
}

type SearchCacheConfig struct {
	MaxSize    int `json:"max_size"    env:"COMPA_SKILLS_SEARCH_CACHE_MAX_SIZE"`
	TTLSeconds int `json:"ttl_seconds" env:"COMPA_SKILLS_SEARCH_CACHE_TTL_SECONDS"`
}

type SkillsRegistriesConfig []*SkillRegistryConfig

func (c *SkillsRegistriesConfig) Get(name string) (SkillRegistryConfig, bool) {
	if c == nil {
		return SkillRegistryConfig{}, false
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return SkillRegistryConfig{}, false
	}
	for _, registry := range *c {
		if registry == nil || registry.Name != name {
			continue
		}
		return *registry, true
	}
	return SkillRegistryConfig{}, false
}

func (c *SkillsRegistriesConfig) Set(name string, cfg SkillRegistryConfig) {
	if c == nil {
		return
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	cfg.Name = name
	for i, registry := range *c {
		if registry == nil || registry.Name != name {
			continue
		}
		(*c)[i] = &cfg
		return
	}
	*c = append(*c, &cfg)
}

type SkillRegistryConfig struct {
	Name      string         `json:"name,omitempty"      yaml:"-"                    env:"-"`
	Enabled   bool           `json:"enabled"             yaml:"-"                    env:"-"`
	BaseURL   string         `json:"base_url"            yaml:"-"                    env:"-"`
	AuthToken SecureString   `json:"auth_token,omitzero" yaml:"auth_token,omitempty" env:"-"`
	Param     map[string]any `json:"-"                   yaml:"-"                    env:"-"`
}

const (
	envSkillsClawHubEnabled         = "COMPA_SKILLS_REGISTRIES_CLAWHUB_ENABLED"
	envSkillsClawHubBaseURL         = "COMPA_SKILLS_REGISTRIES_CLAWHUB_BASE_URL"
	envSkillsClawHubAuthToken       = "COMPA_SKILLS_REGISTRIES_CLAWHUB_AUTH_TOKEN"
	envSkillsClawHubSearchPath      = "COMPA_SKILLS_REGISTRIES_CLAWHUB_SEARCH_PATH"
	envSkillsClawHubSkillsPath      = "COMPA_SKILLS_REGISTRIES_CLAWHUB_SKILLS_PATH"
	envSkillsClawHubDownloadPath    = "COMPA_SKILLS_REGISTRIES_CLAWHUB_DOWNLOAD_PATH"
	envSkillsClawHubTimeout         = "COMPA_SKILLS_REGISTRIES_CLAWHUB_TIMEOUT"
	envSkillsClawHubMaxZipSize      = "COMPA_SKILLS_REGISTRIES_CLAWHUB_MAX_ZIP_SIZE"
	envSkillsClawHubMaxResponseSize = "COMPA_SKILLS_REGISTRIES_CLAWHUB_MAX_RESPONSE_SIZE"
	envSkillsGitHubEnabled          = "COMPA_SKILLS_REGISTRIES_GITHUB_ENABLED"
	envSkillsGitHubBaseURL          = "COMPA_SKILLS_REGISTRIES_GITHUB_BASE_URL"
	envSkillsGitHubAuthToken        = "COMPA_SKILLS_REGISTRIES_GITHUB_AUTH_TOKEN"
	envSkillsGitHubProxy            = "COMPA_SKILLS_REGISTRIES_GITHUB_PROXY"
)

func (c *SkillRegistryConfig) DecodeParam(target any) error {
	if c == nil {
		return nil
	}
	if len(c.Param) == 0 {
		return nil
	}
	data, err := json.Marshal(c.Param)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

// MCPServerConfig defines configuration for a single MCP server
type MCPServerConfig struct {
	// Enabled indicates whether this MCP server is active
	Enabled bool `json:"enabled"`
	// Deferred controls whether this server's tools are registered as hidden (deferred/discovery mode).
	// When nil, the global Discovery.Enabled setting applies.
	// When explicitly set to true or false, it overrides the global setting for this server only.
	Deferred *bool `json:"deferred,omitempty"`
	// Command is the executable to run (e.g., "npx", "python", "/path/to/server")
	Command string `json:"command"`
	// Args are the arguments to pass to the command
	Args []string `json:"args,omitempty"`
	// Env are environment variables to set for the server process (stdio only)
	Env map[string]string `json:"env,omitempty"`
	// EnvFile is the path to a file containing environment variables (stdio only)
	EnvFile string `json:"env_file,omitempty"`
	// Type is "stdio", "sse", "http", or "streamable-http".
	// "http" and "streamable-http" both select streamable HTTP request-response
	// mode, while "sse" keeps the standalone SSE listener enabled for
	// server-initiated notifications. Defaults: stdio if command is set, sse if
	// url is set.
	Type string `json:"type,omitempty"`
	// URL is used for SSE/HTTP transport
	URL string `json:"url,omitempty"`
	// Headers are HTTP headers to send with requests (sse/http only)
	Headers map[string]string `json:"headers,omitempty"`
	// Trusted lets Compa act on what the server declares about its tools
	// (annotations such as readOnlyHint): approval rules may match them, and a
	// read-only or idempotent call is retried once after a lost session.
	// Annotations from servers that are not trusted are ignored.
	Trusted bool `json:"trusted,omitempty"`
	// Cwd is the working folder of a stdio server; empty means the agent
	// workspace.
	Cwd string `json:"cwd,omitempty"`
	// CallTimeoutSeconds bounds one tool call to this server; 0 means the
	// MCP default (MCPConfig.CallTimeoutSeconds).
	CallTimeoutSeconds int `json:"call_timeout_seconds,omitempty"`
}

// MCPConfig defines configuration for all MCP servers
type MCPConfig struct {
	ToolConfig `                    envPrefix:"COMPA_TOOLS_MCP_"`
	Discovery  ToolDiscoveryConfig `                                json:"discovery"`
	// MaxInlineTextChars controls how much MCP text stays inline before it is saved as an artifact.
	MaxInlineTextChars int `json:"max_inline_text_chars,omitempty" env:"COMPA_TOOLS_MCP_MAX_INLINE_TEXT_CHARS"`
	// CallTimeoutSeconds bounds one tool call to a server without its own
	// call_timeout_seconds; 0 means 300 (mcp.DefaultCallTimeout).
	CallTimeoutSeconds int `json:"call_timeout_seconds,omitempty" env:"COMPA_TOOLS_MCP_CALL_TIMEOUT_SECONDS"`
	// Servers is a map of server name to server configuration
	Servers map[string]MCPServerConfig `json:"servers,omitempty"`
}

const DefaultMCPMaxInlineTextChars = 16 * 1024

func (c *MCPConfig) GetMaxInlineTextChars() int {
	if c.MaxInlineTextChars > 0 {
		return c.MaxInlineTextChars
	}
	return DefaultMCPMaxInlineTextChars
}

// LoadConfig reads config.json at path, merges .security.yml beside it,
// applies the COMPA_* environment overrides and validates the result. A
// missing config.json, or one too short to hold a setting, yields the
// defaults, completed the same way. LoadConfig never writes either file.
func LoadConfig(path string) (*Config, error) {
	updateResolver(filepath.Dir(path))

	cfg, data, err := readConfigFile(path)
	if err != nil {
		return nil, err
	}
	cfg.sourceDigest = extendSourceDigest("", data)
	if err = cfg.completeLoad(path); err != nil {
		return nil, err
	}
	return cfg, nil
}

// readConfigFile decodes config.json over the defaults, and returns what it
// read. A missing file, or content such as "{}" that cannot hold a setting,
// gives the defaults.
func readConfigFile(path string) (*Config, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			logger.WarnF(
				"config file not found, using default config",
				map[string]any{"path": path},
			)
			return DefaultConfig(), nil, nil
		}
		return nil, nil, err
	}

	// Reject malformed JSON before the shortcut below, so a broken config.json
	// is reported instead of silently replaced by the defaults.
	var probe map[string]json.RawMessage
	if e := json.Unmarshal(data, &probe); e != nil {
		e = wrapJSONError(data, e, "config.json")
		logger.ErrorCF("config", formatDiagnosticLogMessage("Malformed config file", e), map[string]any{"path": path})
		return nil, nil, e
	}
	// Content this short, such as "{}", cannot hold a setting.
	if len(data) <= 10 {
		logger.Warn(fmt.Sprintf("content is [%s]", string(data)))
		return DefaultConfig(), data, nil
	}

	cfg, err := loadConfig(data)
	if err != nil {
		logger.ErrorCF(
			"config",
			formatDiagnosticLogMessage("Failed to load config", err),
			map[string]any{"path": path},
		)
		return nil, nil, err
	}
	return cfg, data, nil
}

// completeLoad readies a config decoded from config.json at path, or the
// defaults when there is none: it merges .security.yml, applies the
// environment overrides, initializes the channels and validates the result.
func (c *Config) completeLoad(path string) error {
	secPath := securityPath(path)
	secData, err := os.ReadFile(secPath)
	switch {
	case err == nil:
		c.sourceDigest = extendSourceDigest(c.sourceDigest, secData)
		if err := mergeSecurityConfig(c, secPath, secData); err != nil {
			return fmt.Errorf("failed to load security config: %w", err)
		}
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("failed to load security config: failed to read security config: %w", err)
	}
	// A secret config.json masks but .security.yml lacks is gone; never hand
	// out its placeholder as a value.
	c.applySecretMaps(secretMapsFile{}, true)

	gatewayHostBeforeEnv := c.Gateway.Host
	// Remember what the files hold before the environment changes it, so that
	// SaveConfig never writes an override that exists only there.
	snapshot := c.snapshotEnvSettings()
	c.envOverrides = nil

	if err = env.Parse(c); err != nil {
		return err
	}
	applySkillsRegistryEnvOverrides(c)

	if err = initChannelList(c.Channels, channelEnvRecorder{overrides: &c.envOverrides}); err != nil {
		return err
	}
	if err = c.ValidateTurnProfile(); err != nil {
		return err
	}
	c.Gateway.Host, err = resolveGatewayHostFromEnv(gatewayHostBeforeEnv)
	if err != nil {
		return fmt.Errorf("invalid gateway host: %w", err)
	}
	c.recordEnvOverrides(snapshot)

	if err = c.ValidateProviderInstances(); err != nil {
		return err
	}
	if err = c.ValidateModelSelections(); err != nil {
		return err
	}
	if err = c.ValidateModules(); err != nil {
		return err
	}
	if err = c.ValidateSettings(); err != nil {
		return err
	}

	// Ensure Workspace has a default if not set
	if c.Agents.Defaults.Workspace == "" {
		homePath := GetHome()
		c.Agents.Defaults.Workspace = filepath.Join(homePath, pkg.WorkspaceName)
	}

	c.Session.ApplyDmScope()
	c.Session.DeriveDmScope()

	// Log lines mask every secret the config holds.
	for _, secret := range c.collectSensitiveValues() {
		logger.RegisterSecret(secret)
	}
	return nil
}

// loadConfig decodes config.json content over the defaults. A field this
// version does not know fails the load, and the error names it.
func loadConfig(data []byte) (*Config, error) {
	cfg := DefaultConfig()
	evolutionModeExplicit := configObjectHasField(data, "evolution", "mode")
	evolutionExplicitWithoutMode := configObjectHasTopLevelField(data, "evolution") && !evolutionModeExplicit

	if err := decodeJSONWithDiagnostics(data, cfg, "config.json"); err != nil {
		return nil, err
	}
	if evolutionExplicitWithoutMode {
		cfg.Evolution.Mode = ""
	}
	return cfg, nil
}

func configObjectHasTopLevelField(data []byte, field string) bool {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return false
	}
	_, ok := raw[field]
	return ok
}

func configObjectHasField(data []byte, objectField, nestedField string) bool {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return false
	}
	objectData, ok := raw[objectField]
	if !ok {
		return false
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(objectData, &object); err != nil {
		return false
	}
	_, ok = object[nestedField]
	return ok
}

// applySkillsRegistryEnvOverrides applies the COMPA_SKILLS_REGISTRIES_*
// environment variables to the clawhub and github registries. Registries are a
// list, which env struct tags cannot address.
func applySkillsRegistryEnvOverrides(cfg *Config) {
	if cfg == nil {
		return
	}

	registryCfg, foundClawHub := cfg.Tools.Skills.Registries.Get("clawhub")
	if !foundClawHub {
		registryCfg = SkillRegistryConfig{
			Name:  "clawhub",
			Param: map[string]any{},
		}
	}
	if registryCfg.Param == nil {
		registryCfg.Param = map[string]any{}
	}

	if raw, envSet := os.LookupEnv(envSkillsClawHubEnabled); envSet {
		if value, err := strconv.ParseBool(strings.TrimSpace(raw)); err == nil {
			registryCfg.Enabled = value
		}
	}
	if value, envSet := os.LookupEnv(envSkillsClawHubBaseURL); envSet {
		registryCfg.BaseURL = value
	}
	if value, envSet := os.LookupEnv(envSkillsClawHubAuthToken); envSet {
		registryCfg.AuthToken = *NewSecureString(value)
	}
	if value, envSet := os.LookupEnv(envSkillsClawHubSearchPath); envSet {
		registryCfg.Param["search_path"] = value
	}
	if value, envSet := os.LookupEnv(envSkillsClawHubSkillsPath); envSet {
		registryCfg.Param["skills_path"] = value
	}
	if value, envSet := os.LookupEnv(envSkillsClawHubDownloadPath); envSet {
		registryCfg.Param["download_path"] = value
	}
	if raw, envSet := os.LookupEnv(envSkillsClawHubTimeout); envSet {
		if value, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil {
			registryCfg.Param["timeout"] = value
		}
	}
	if raw, envSet := os.LookupEnv(envSkillsClawHubMaxZipSize); envSet {
		if value, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil {
			registryCfg.Param["max_zip_size"] = value
		}
	}
	if raw, envSet := os.LookupEnv(envSkillsClawHubMaxResponseSize); envSet {
		if value, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil {
			registryCfg.Param["max_response_size"] = value
		}
	}

	cfg.Tools.Skills.Registries.Set("clawhub", registryCfg)

	githubCfg, foundGitHub := cfg.Tools.Skills.Registries.Get("github")
	if !foundGitHub {
		githubCfg = SkillRegistryConfig{
			Name:  "github",
			Param: map[string]any{},
		}
	}
	if githubCfg.Param == nil {
		githubCfg.Param = map[string]any{}
	}

	if raw, envSet := os.LookupEnv(envSkillsGitHubEnabled); envSet {
		if value, err := strconv.ParseBool(strings.TrimSpace(raw)); err == nil {
			githubCfg.Enabled = value
		}
	}
	if value, envSet := os.LookupEnv(envSkillsGitHubBaseURL); envSet {
		githubCfg.BaseURL = value
	}
	if value, envSet := os.LookupEnv(envSkillsGitHubAuthToken); envSet {
		githubCfg.AuthToken = *NewSecureString(value)
	}
	if value, envSet := os.LookupEnv(envSkillsGitHubProxy); envSet {
		githubCfg.Param["proxy"] = value
	}

	cfg.Tools.Skills.Registries.Set("github", githubCfg)
}

func MakeBackup(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	dateSuffix := time.Now().Format(".20060102.bak")
	// Backup config file
	bakPath := path + dateSuffix
	if err := fileutil.CopyFile(path, bakPath, 0o600); err != nil {
		logger.ErrorF("failed to create config backup", map[string]any{"error": err})
		return fmt.Errorf("failed to create config backup: %w", err)
	}
	// Backup security config file
	secPath := securityPath(path)
	if _, err := os.Stat(secPath); err == nil {
		secBakPath := secPath + dateSuffix
		if secErr := fileutil.CopyFile(secPath, secBakPath, 0o600); secErr != nil {
			logger.ErrorF("failed to create security backup", map[string]any{"error": secErr})
			return fmt.Errorf("failed to create security backup: %w", secErr)
		}
	}
	return nil
}

// SaveConfig writes cfg to config.json at path and its secrets to
// .security.yml beside it, in the current format with every setting
// explicit. A setting the environment overrode when cfg was loaded keeps the
// value the files held, unless it was changed since. cfg itself is not
// modified.
//
// Every process that saves the config (launcher, CLI, onboarding, kernel)
// takes the lock beside config.json, and the two files are replaced together:
// a save that fails leaves the previous pair (see writeFilePair).
func SaveConfig(path string, cfg *Config) error {
	out := cfg.copyForSave()
	return WithFileLock(path, func() error {
		// A secret map value that is unchanged, or a placeholder as a
		// config decoded from masked JSON holds, keeps what .security.yml
		// holds, as written. Without a readable file, only placeholders
		// can't be saved; the other values are written as given.
		stored, err := readSecretMaps(path)
		if err != nil && out.hasSecretPlaceholders() {
			return fmt.Errorf("read the stored secrets: %w", err)
		}
		secData, err := marshalSecurityConfig(out, stored)
		if err != nil {
			logger.ErrorCF("config", "cannot save .security.yml", map[string]any{"error": err})
			return err
		}
		data, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return err
		}
		if err := writeFilePair(securityPath(path), secData, path, data); err != nil {
			logger.ErrorCF("config", "cannot save the config", map[string]any{"error": err})
			return err
		}
		return nil
	})
}

func (c *Config) WorkspacePath() string {
	return ExpandHome(c.Agents.Defaults.Workspace)
}

// ExpandHome replaces a leading "~" with the user's home directory: "~"
// alone, "~/x" and "~\x" (the Windows form, accepted everywhere). Any other
// path, including "~user/x", is returned unchanged, as is every path when the
// home directory is unknown; a bare home directory is never substituted for a
// path it does not name.
func ExpandHome(path string) string {
	if path == "" || path[0] != '~' {
		return path
	}
	if len(path) > 1 && path[1] != '/' && path[1] != '\\' {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if len(path) <= 2 {
		return home
	}
	return filepath.Join(home, path[2:])
}

// SecurityCopyFrom fills c's secrets from the .security.yml beside path, as
// a config decoded from JSON, which never holds them, needs.
func (c *Config) SecurityCopyFrom(path string) error {
	if err := loadSecurityConfig(c, securityPath(path)); err != nil {
		return err
	}
	c.applySecretMaps(secretMapsFile{}, true)
	return nil
}

// ResetToDefaults backs up the current config, creates a default config,
// preserves security credentials from the existing config, and saves it.
func ResetToDefaults(configPath string) error {
	if err := MakeBackup(configPath); err != nil {
		return fmt.Errorf("backup before reset: %w", err)
	}
	cfg := DefaultConfig()
	cfg.Session.ApplyDmScope()
	cfg.Session.DeriveDmScope()
	if err := cfg.SecurityCopyFrom(configPath); err != nil {
		logger.WarnF("could not preserve security config", map[string]any{"error": err})
	}
	return SaveConfig(configPath, cfg)
}

func (t *ToolsConfig) IsToolEnabled(name string) bool {
	switch name {
	case "web":
		return t.Web.Enabled
	case "cron":
		return t.Cron.Enabled
	case "exec":
		return t.Exec.Enabled
	case "skills":
		return t.Skills.Enabled
	case "media_cleanup":
		return t.MediaCleanup.Enabled
	case "append_file":
		return t.AppendFile.Enabled
	case "edit_file":
		return t.EditFile.Enabled
	case "find_skills":
		return t.FindSkills.Enabled
	case "i2c":
		return t.I2C.Enabled
	case "install_skill":
		return t.InstallSkill.Enabled
	case "list_dir":
		return t.ListDir.Enabled
	case "load_image":
		return t.LoadImage.Enabled
	case "message":
		return t.Message.Enabled
	case "read_file":
		return t.ReadFile.Enabled
	case "serial":
		return t.Serial.Enabled
	case "spawn":
		return t.Spawn.Enabled
	case "spawn_status":
		return t.SpawnStatus.Enabled
	case "spi":
		return t.SPI.Enabled
	case "subagent":
		return t.Subagent.Enabled
	case "web_fetch":
		return t.WebFetch.Enabled
	case "send_file":
		return t.SendFile.Enabled
	case "send_tts":
		return t.SendTTS.Enabled
	case "write_file":
		return t.WriteFile.Enabled
	case "mcp":
		return t.MCP.Enabled
	default:
		return true
	}
}
