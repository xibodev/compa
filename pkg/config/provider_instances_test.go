package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func validProviderInstance(id string) *ProviderInstanceConfig {
	return &ProviderInstanceConfig{
		ID:                id,
		ProviderKind:      "openai",
		Adapter:           "openai-compatible",
		Protocol:          "openai",
		Endpoint:          "https://api.example.test/v1",
		AuthConnectionRef: "credential:test",
		Headers:           map[string]string{"X-Test": "fixture"},
		Settings:          map[string]any{"organization": "example"},
		State:             ProviderInstanceStateEnabled,
	}
}

func TestProviderInstanceConfigStableIDRoundTrip(t *testing.T) {
	want := validProviderInstance("openai.primary-1")
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	var got ProviderInstanceConfig
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got.ID != want.ID {
		t.Fatalf("ID = %q, want stable ID %q", got.ID, want.ID)
	}
	if got.ProviderKind != want.ProviderKind || got.Adapter != want.Adapter || got.Protocol != want.Protocol {
		t.Fatalf("adapter identity changed after round trip: got %#v, want %#v", got, *want)
	}
	if got.AuthConnectionRef != want.AuthConnectionRef || got.Headers["X-Test"] != "fixture" {
		t.Fatalf("connection ownership changed after round trip: got %#v", got)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestProviderInstanceFoundationJSONRoundTrip(t *testing.T) {
	want := Config{
		ProviderInstances: []*ProviderInstanceConfig{validProviderInstance("openai-primary")},
		ModelRoutes: []*ModelRouteConfig{{
			Name:    "chat-default",
			Targets: []string{"openai-primary/openai/gpt-5"},
		}},
	}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	var got Config
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(got.ProviderInstances) != 1 || got.ProviderInstances[0].ID != "openai-primary" {
		t.Fatalf("provider_instances = %#v", got.ProviderInstances)
	}
	if len(got.ModelRoutes) != 1 || got.ModelRoutes[0].Targets[0] != "openai-primary/openai/gpt-5" {
		t.Fatalf("model_routes = %#v", got.ModelRoutes)
	}
	if err := got.ValidateProviderInstances(); err != nil {
		t.Fatalf("ValidateProviderInstances() error = %v", err)
	}
}

func TestProviderInstanceConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*ProviderInstanceConfig)
		wantErr string
	}{
		{name: "valid"},
		{name: "disabled is a valid lifecycle state", mutate: func(c *ProviderInstanceConfig) { c.State = ProviderInstanceStateDisabled }},
		{name: "missing id", mutate: func(c *ProviderInstanceConfig) { c.ID = "" }, wantErr: "id must be"},
		{name: "unstable uppercase id", mutate: func(c *ProviderInstanceConfig) { c.ID = "OpenAI" }, wantErr: "id must be"},
		{name: "missing provider kind", mutate: func(c *ProviderInstanceConfig) { c.ProviderKind = " " }, wantErr: "provider_kind is required"},
		{name: "missing adapter", mutate: func(c *ProviderInstanceConfig) { c.Adapter = "" }, wantErr: "adapter is required"},
		{name: "missing protocol", mutate: func(c *ProviderInstanceConfig) { c.Protocol = "" }, wantErr: "protocol is required"},
		{name: "missing state", mutate: func(c *ProviderInstanceConfig) { c.State = "" }, wantErr: "state must be"},
		{name: "unknown state", mutate: func(c *ProviderInstanceConfig) { c.State = "paused" }, wantErr: "state must be"},
		{name: "empty header", mutate: func(c *ProviderInstanceConfig) { c.Headers = map[string]string{" ": "value"} }, wantErr: "header name must not be empty"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			instance := validProviderInstance("fixture")
			if tc.mutate != nil {
				tc.mutate(instance)
			}
			err := instance.Validate()
			if tc.wantErr == "" && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("Validate() error = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestParseExactModelTarget(t *testing.T) {
	target, err := ParseExactModelTarget("openrouter-main/anthropic/claude-sonnet")
	if err != nil {
		t.Fatalf("ParseExactModelTarget() error = %v", err)
	}
	if target.InstanceID != "openrouter-main" || target.ModelID != "anthropic/claude-sonnet" {
		t.Fatalf("target = %#v", target)
	}
	if target.String() != "openrouter-main/anthropic/claude-sonnet" {
		t.Fatalf("String() = %q", target.String())
	}

	for _, raw := range []string{"", "instance", "/model", "Instance/model", "instance/", "instance/model id", "instance/model//variant"} {
		t.Run(raw, func(t *testing.T) {
			if _, err := ParseExactModelTarget(raw); err == nil {
				t.Fatalf("ParseExactModelTarget(%q) expected error", raw)
			}
		})
	}
}

func TestConfigValidateProviderInstancesAndRoutes(t *testing.T) {
	openAI := validProviderInstance("openai-main")
	anthropic := validProviderInstance("anthropic-backup")
	anthropic.ProviderKind = "anthropic"
	anthropic.Adapter = ProviderAdapterAnthropicCompatible
	anthropic.Protocol = "anthropic-messages"

	cfg := &Config{
		ProviderInstances: []*ProviderInstanceConfig{openAI, anthropic},
		ModelRoutes: []*ModelRouteConfig{{
			Name:    "primary-chat",
			Targets: []string{"openai-main/shared-model", "anthropic-backup/shared-model"},
		}},
	}
	if err := cfg.ValidateProviderInstances(); err != nil {
		t.Fatalf("ValidateProviderInstances() error = %v", err)
	}
	if got := cfg.ModelRoutes[0].Targets; got[0] != "openai-main/shared-model" || got[1] != "anthropic-backup/shared-model" {
		t.Fatalf("route order changed: %#v", got)
	}
}

func TestConfigValidateProviderInstancesRejectsInvalidOwnership(t *testing.T) {
	tests := []struct {
		name    string
		build   func() *Config
		wantErr string
	}{
		{
			name: "duplicate instance ids",
			build: func() *Config {
				return &Config{ProviderInstances: []*ProviderInstanceConfig{validProviderInstance("same"), validProviderInstance("same")}}
			},
			wantErr: "duplicate id",
		},
		{
			name: "disabled instance cannot be routed",
			build: func() *Config {
				instance := validProviderInstance("disabled-one")
				instance.State = ProviderInstanceStateDisabled
				return &Config{ProviderInstances: []*ProviderInstanceConfig{instance}, ModelRoutes: []*ModelRouteConfig{{Name: "chat", Targets: []string{"disabled-one/model"}}}}
			},
			wantErr: "is disabled",
		},
		{
			name: "malformed target",
			build: func() *Config {
				return &Config{ProviderInstances: []*ProviderInstanceConfig{validProviderInstance("one")}, ModelRoutes: []*ModelRouteConfig{{Name: "chat", Targets: []string{"one"}}}}
			},
			wantErr: "instance-id/model-id",
		},
		{
			name: "duplicate target",
			build: func() *Config {
				return &Config{ProviderInstances: []*ProviderInstanceConfig{validProviderInstance("one")}, ModelRoutes: []*ModelRouteConfig{{Name: "chat", Targets: []string{"one/model", "one/model"}}}}
			},
			wantErr: "duplicate target",
		},
		{
			name: "missing instance is not borrowed from another instance",
			build: func() *Config {
				return &Config{ProviderInstances: []*ProviderInstanceConfig{validProviderInstance("configured")}, ModelRoutes: []*ModelRouteConfig{{Name: "chat", Targets: []string{"missing/model"}}}}
			},
			wantErr: `provider instance "missing" not found`,
		},
		{
			name: "empty route",
			build: func() *Config {
				return &Config{ProviderInstances: []*ProviderInstanceConfig{validProviderInstance("one")}, ModelRoutes: []*ModelRouteConfig{{Name: "chat"}}}
			},
			wantErr: "targets must contain",
		},
		{
			name: "duplicate route name",
			build: func() *Config {
				return &Config{ProviderInstances: []*ProviderInstanceConfig{validProviderInstance("one")}, ModelRoutes: []*ModelRouteConfig{{Name: "chat", Targets: []string{"one/a"}}, {Name: "chat", Targets: []string{"one/b"}}}}
			},
			wantErr: "duplicate name",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.build().ValidateProviderInstances()
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ValidateProviderInstances() error = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestConfigValidateProviderInstancesRejectsUnsupportedAdapters(t *testing.T) {
	native := validProviderInstance("native")
	native.Adapter = "anthropic-messages"
	extension := validProviderInstance("daemon")
	extension.Adapter = ProviderAdapterExtension
	extension.Protocol = ExtensionSurfaceChatCompletions
	extension.Settings = nil
	surface := validProviderInstance("daemon-surface")
	surface.Adapter = ProviderAdapterExtension
	surface.Protocol = "telepathy"
	surface.Settings = map[string]any{ExtensionProviderSetting: "acme"}
	listed := validProviderInstance("daemon-listed")
	listed.Adapter = ProviderAdapterExtension
	listed.Protocol = "audio_speech"
	listed.Settings = map[string]any{ExtensionProviderSetting: "acme", ExtensionSurfacesSetting: []any{"audio_speech", "smell"}}
	unlisted := validProviderInstance("daemon-unlisted")
	unlisted.Adapter = ProviderAdapterExtension
	unlisted.Protocol = "audio_speech"
	unlisted.Settings = map[string]any{ExtensionProviderSetting: "acme", ExtensionSurfacesSetting: []any{"audio_transcriptions"}}

	for _, tc := range []struct {
		instance *ProviderInstanceConfig
		wantErr  string
	}{
		{native, `adapter "anthropic-messages", which Compa does not include`},
		{extension, "has no extension_provider setting"},
		{surface, `surface "telepathy", which llmgw-core does not define`},
		{listed, `lists surface "smell"`},
		{unlisted, `does not list its protocol "audio_speech"`},
	} {
		t.Run(tc.instance.ID, func(t *testing.T) {
			cfg := &Config{ProviderInstances: []*ProviderInstanceConfig{tc.instance}}
			err := cfg.ValidateProviderInstances()
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ValidateProviderInstances() error = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}

	supported := validProviderInstance("daemon-ok")
	supported.Adapter = ProviderAdapterExtension
	supported.Protocol = ExtensionSurfaceMessages
	supported.Settings = map[string]any{ExtensionProviderSetting: "acme"}
	if err := (&Config{ProviderInstances: []*ProviderInstanceConfig{supported}}).ValidateProviderInstances(); err != nil {
		t.Fatalf("ValidateProviderInstances() rejected a supported extension instance: %v", err)
	}

	// A daemon provider serving audio surfaces only is a valid instance.
	speechOnly := validProviderInstance("daemon-speech")
	speechOnly.Adapter = ProviderAdapterExtension
	speechOnly.Protocol = "audio_speech"
	speechOnly.Settings = map[string]any{ExtensionProviderSetting: "acme", ExtensionSurfacesSetting: []string{"audio_speech", "Audio_Transcriptions"}}
	if err := (&Config{ProviderInstances: []*ProviderInstanceConfig{speechOnly}}).ValidateProviderInstances(); err != nil {
		t.Fatalf("ValidateProviderInstances() rejected a speech-only extension instance: %v", err)
	}
	if got := strings.Join(speechOnly.ExtensionSurfaces(), ","); got != "audio_speech,audio_transcriptions" {
		t.Fatalf("ExtensionSurfaces() = %q", got)
	}
	if got := strings.Join(supported.ExtensionSurfaces(), ","); got != "messages" {
		t.Fatalf("ExtensionSurfaces() without the setting = %q, want the protocol", got)
	}
}

func TestLoadConfigRejectsUnsupportedAdapter(t *testing.T) {
	path := writeTestConfig(t, `{"provider_instances":[{"id":"native","provider_kind":"anthropic","adapter":"anthropic-messages","protocol":"anthropic","state":"enabled"}]}`)
	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "does not include") {
		t.Fatalf("LoadConfig() error = %v, want the unsupported adapter rejected", err)
	}
}

func boolPtr(value bool) *bool { return &value }

func TestProviderInstanceRuntimeRoundTrip(t *testing.T) {
	instance := validProviderInstance("tuned")
	instance.Runtime = &ProviderInstanceRuntime{
		Proxy:               "socks5://127.0.0.1:1080",
		RequestTimeout:      90,
		RPM:                 30,
		Streaming:           boolPtr(true),
		ThinkingLevel:       "high",
		MaxTokensField:      "max_completion_tokens",
		ToolSchemaTransform: "simple",
		ExtraBody:           map[string]any{"reasoning_split": true, "top_k": float64(40)},
	}
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := DefaultConfig()
	cfg.ProviderInstances = []*ProviderInstanceConfig{instance}
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		ProviderInstances []map[string]json.RawMessage `json:"provider_instances"`
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.ProviderInstances) != 1 {
		t.Fatalf("saved provider_instances = %s", data)
	}
	var runtime map[string]any
	if err := json.Unmarshal(saved.ProviderInstances[0]["runtime"], &runtime); err != nil {
		t.Fatalf("runtime was not saved as an object: %v", err)
	}
	for _, key := range []string{"proxy", "request_timeout", "rpm", "streaming", "thinking_level", "max_tokens_field", "tool_schema_transform", "extra_body"} {
		if _, ok := runtime[key]; !ok {
			t.Fatalf("saved runtime %v lacks %q", runtime, key)
		}
	}

	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	got := loaded.ProviderInstances[0].Runtime
	if got == nil || !reflect.DeepEqual(*got, *instance.Runtime) {
		t.Fatalf("runtime = %#v, want %#v", got, instance.Runtime)
	}
}

func TestProviderInstanceRuntimeOmittedWhenUnset(t *testing.T) {
	data, err := json.Marshal(validProviderInstance("plain"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"runtime"`) {
		t.Fatalf("instance without runtime settings marshaled %s", data)
	}
}

func TestProviderInstanceRuntimeValidate(t *testing.T) {
	valid := []ProviderInstanceRuntime{
		{},
		{ThinkingLevel: "adaptive"},
		{ThinkingLevel: " High "},
		{ToolSchemaTransform: "off"},
		{ToolSchemaTransform: "simple"},
		{Proxy: "http://proxy.example.test:8080"},
		{Proxy: "socks5h://127.0.0.1:1080"},
		{Streaming: boolPtr(false), RequestTimeout: 0, RPM: 0},
	}
	for _, runtime := range valid {
		if err := runtime.Validate(); err != nil {
			t.Fatalf("Validate(%+v) error = %v", runtime, err)
		}
	}

	invalid := []struct {
		name    string
		runtime ProviderInstanceRuntime
		wantErr string
	}{
		{"negative timeout", ProviderInstanceRuntime{RequestTimeout: -1}, "runtime.request_timeout"},
		{"negative rpm", ProviderInstanceRuntime{RPM: -5}, "runtime.rpm"},
		{"unknown thinking level", ProviderInstanceRuntime{ThinkingLevel: "maximum"}, "runtime.thinking_level"},
		{"unknown tool schema transform", ProviderInstanceRuntime{ToolSchemaTransform: "loose"}, "runtime.tool_schema_transform"},
		{"proxy scheme", ProviderInstanceRuntime{Proxy: "ftp://proxy.example.test"}, "runtime.proxy"},
		{"proxy without host", ProviderInstanceRuntime{Proxy: "proxy.example.test"}, "runtime.proxy"},
		{"max tokens field", ProviderInstanceRuntime{MaxTokensField: "max tokens"}, "runtime.max_tokens_field"},
		{"empty extra body field", ProviderInstanceRuntime{ExtraBody: map[string]any{" ": 1}}, "runtime.extra_body"},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			instance := validProviderInstance("tuned")
			instance.Runtime = &tc.runtime
			err := (&Config{ProviderInstances: []*ProviderInstanceConfig{instance}}).ValidateProviderInstances()
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ValidateProviderInstances() error = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestLoadConfigRejectsInvalidRuntime(t *testing.T) {
	const instance = `"id":"tuned","provider_kind":"openai","adapter":"openai-compatible","protocol":"openai","state":"enabled"`
	for name, tc := range map[string]struct {
		runtime string
		wantErr string
	}{
		"negative value": {`{"rpm":-1}`, "runtime.rpm"},
		"unknown field":  {`{"stream":true}`, "provider_instances[0].runtime.stream"},
		"untyped value":  {`{"request_timeout":"30"}`, "request_timeout"},
	} {
		t.Run(name, func(t *testing.T) {
			path := writeTestConfig(t, `{"provider_instances":[{`+instance+`,"runtime":`+tc.runtime+`}]}`)
			_, err := LoadConfig(path)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("LoadConfig() error = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidThinkingLevel(t *testing.T) {
	for _, level := range []string{"off", "low", "medium", "high", "xhigh", "adaptive", "HIGH", " low "} {
		if !ValidThinkingLevel(level) {
			t.Errorf("ValidThinkingLevel(%q) = false", level)
		}
	}
	for _, level := range []string{"", "max", "on", "extreme"} {
		if ValidThinkingLevel(level) {
			t.Errorf("ValidThinkingLevel(%q) = true", level)
		}
	}
}

// writeTestConfig writes content as a config.json in a fresh directory and
// returns its path.
func writeTestConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
