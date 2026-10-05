package config

import (
	"os"
	"path/filepath"
	"testing"
)

// A missing or near-empty config.json still gets .security.yml, the
// environment overrides, the channel set-up and validation.
func TestLoadConfigCompletesTheDefaults(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content *string
	}{
		{name: "no file"},
		{name: "empty object", content: ptr("{}")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv(EnvHome, dir)
			path := filepath.Join(dir, "config.json")
			if tc.content != nil {
				if err := os.WriteFile(path, []byte(*tc.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			secrets := "channel_list:\n  telegram:\n    settings:\n      token: tg-from-security\n"
			if err := os.WriteFile(filepath.Join(dir, SecurityConfigFile), []byte(secrets), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("COMPA_GATEWAY_PORT", "19999")

			cfg, err := LoadConfig(path)
			if err != nil {
				t.Fatalf("LoadConfig() error = %v", err)
			}
			if cfg.Gateway.Port != 19999 {
				t.Fatalf("gateway.port = %d, want 19999 from COMPA_GATEWAY_PORT", cfg.Gateway.Port)
			}
			decoded, err := cfg.Channels.Get("telegram").GetDecoded()
			if err != nil {
				t.Fatalf("GetDecoded() error = %v", err)
			}
			if got := decoded.(*TelegramSettings).Token.String(); got != "tg-from-security" {
				t.Fatalf("telegram token = %q, want the .security.yml value", got)
			}
		})
	}
}

// Validation runs on the defaults path too: a malformed override is reported
// rather than silently ignored.
func TestLoadConfigValidatesWithoutAFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvHome, dir)
	t.Setenv("COMPA_TOOLS_MESSAGE_TARGETS", "everyone")

	if _, err := LoadConfig(filepath.Join(dir, "config.json")); err == nil {
		t.Fatal("LoadConfig() accepted an invalid tools.message.targets from the environment")
	}
}

func ptr[T any](v T) *T { return &v }
