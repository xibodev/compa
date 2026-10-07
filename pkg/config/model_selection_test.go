package config

import (
	"strings"
	"testing"
)

func TestValidateSelectionSyntax(t *testing.T) {
	for _, selection := range []string{
		"",
		"   ",
		"openai/gpt-5.4",
		"openrouter/anthropic/claude-sonnet-4.6",
		" openai/gpt-5.4 ",
		"chat-default",
		"route.v2_fast",
	} {
		if err := ValidateSelectionSyntax(selection); err != nil {
			t.Errorf("ValidateSelectionSyntax(%q) error = %v", selection, err)
		}
	}

	for _, selection := range []string{
		"GPT-4",           // route names are lowercase
		"my route",        // route names have no spaces
		"-route",          // nor a leading separator
		"/gpt-4o",         // a target needs its instance
		"openai/",         // and its model
		"OpenAI/gpt-4o",   // instance IDs are lowercase
		"openai/gpt 4o",   // model IDs have no spaces
		"openai/gpt//4o",  // nor empty segments
		"claude-sonnet!",  // nor punctuation outside . _ -
		"primary,backup",  // one selection, not a list
		"openai:gpt-5.4",  // nor a provider:model pair
		"route.",          // nor a trailing separator
		"_leading-under",  // nor a leading underscore
		"openai/ gpt-4o ", // the model ID itself must be trimmed
	} {
		if err := ValidateSelectionSyntax(selection); err == nil {
			t.Errorf("ValidateSelectionSyntax(%q) = nil, want an error", selection)
		}
	}
}

func TestLoadConfigRejectsMalformedSelections(t *testing.T) {
	for _, tc := range []struct {
		name    string
		json    string
		wantErr string
	}{
		{"default model", `{"agents":{"defaults":{"model_name":"GPT 4"}}}`, "agents.defaults.model_name"},
		{"image model", `{"agents":{"defaults":{"image_model":"vision/"}}}`, "agents.defaults.image_model"},
		{"light model", `{"agents":{"defaults":{"routing":{"enabled":true,"light_model":"Light Model"}}}}`, "agents.defaults.routing.light_model"},
		{"agent model", `{"agents":{"list":[{"id":"main"},{"id":"helper","model":"Helper Model"}]}}`, "agents.list[1].model"},
		{"stt target", `{"voice":{"stt_target":"whisper"}}`, "voice.stt_target"},
		{"tts target", `{"voice":{"tts_target":"Voice/tts-1"}}`, "voice.tts_target"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadConfig(writeTestConfig(t, tc.json))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("LoadConfig() error = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

// TestLoadConfigAcceptsDanglingSelections pins that selections are checked
// for syntax only when the config loads: a target on a missing instance or
// a missing route must not make the config unloadable.
func TestLoadConfigAcceptsDanglingSelections(t *testing.T) {
	cfg, err := LoadConfig(writeTestConfig(t, `{
		"agents": {
			"defaults": {
				"model_name": "missing-instance/gpt-5.4",
				"image_model": "missing-route",
				"routing": {"enabled": true, "light_model": "gone/small-model", "threshold": 0.3}
			},
			"list": [{"id": "main", "model": "no-such-route"}]
		},
		"voice": {"stt_target": "gone/whisper-1", "tts_target": "gone/tts-1"}
	}`))
	if err != nil {
		t.Fatalf("LoadConfig() error = %v, want dangling selections to load", err)
	}
	if cfg.Agents.Defaults.GetModelName() != "missing-instance/gpt-5.4" || cfg.Agents.List[0].Model != "no-such-route" {
		t.Fatalf("selections were not kept: %+v", cfg.Agents)
	}
}

func TestLoadConfigRejectsStructuredAgentModel(t *testing.T) {
	_, err := LoadConfig(writeTestConfig(t, `{"agents":{"list":[{"id":"main","model":{"primary":"openai/gpt-5.4","fallbacks":["backup"]}}]}}`))
	if err == nil || !strings.Contains(err.Error(), "model") {
		t.Fatalf("LoadConfig() error = %v, want the object form of agents.list[].model rejected", err)
	}
}

func TestLoadConfigRejectsRemovedModelFields(t *testing.T) {
	for name, content := range map[string]string{
		"model_list":           `{"model_list":[{"model_name":"gpt","model":"openai/gpt-5.4"}]}`,
		"defaults provider":    `{"agents":{"defaults":{"provider":"openai"}}}`,
		"model fallbacks":      `{"agents":{"defaults":{"model_fallbacks":["backup"]}}}`,
		"image fallbacks":      `{"agents":{"defaults":{"image_model_fallbacks":["backup"]}}}`,
		"subagent model":       `{"agents":{"list":[{"id":"main","subagents":{"model":"openai/gpt-5.4"}}]}}`,
		"voice model name":     `{"voice":{"model_name":"whisper"}}`,
		"voice endpoint":       `{"voice":{"stt_endpoint":"https://voice.example.test/v1/audio/transcriptions"}}`,
		"voice live endpoint":  `{"voice":{"live_endpoint":"wss://live.example.test"}}`,
		"voice elevenlabs key": `{"voice":{"elevenlabs_api_key":"secret"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := LoadConfig(writeTestConfig(t, content))
			if err == nil || !strings.Contains(err.Error(), "unknown field") {
				t.Fatalf("LoadConfig() error = %v, want the removed field reported as unknown", err)
			}
		})
	}
}
