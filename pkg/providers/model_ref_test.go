package providers

import "testing"

func TestNormalizeProviderResolvesCoreRegistryAliases(t *testing.T) {
	for input, want := range map[string]string{
		"OpenAI":            "openai",
		" ANTHROPIC ":       "anthropic",
		"google":            "gemini",
		"opencode":          "opencode_zen",
		"openai-compatible": "custom_openai",
		"Unknown-Kind":      "unknown-kind",
		"":                  "",
	} {
		if got := NormalizeProvider(input); got != want {
			t.Errorf("NormalizeProvider(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestModelKey(t *testing.T) {
	for _, tt := range []struct{ provider, model, want string }{
		{"openai", "gpt-4", "openai/gpt-4"},
		{"Google", "Gemini-Pro", "gemini/gemini-pro"},
		{"custom-kind", "Model-X", "custom-kind/model-x"},
	} {
		if got := ModelKey(tt.provider, tt.model); got != tt.want {
			t.Errorf("ModelKey(%q, %q) = %q, want %q", tt.provider, tt.model, got, tt.want)
		}
	}
}
