package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xibodev/compa/v2/pkg/logger"
)

// plainSecretsConfig is a config.json with plain secrets, as an edit sends
// them: the MCP env and headers, a hook's env, a provider instance's headers
// and the Matrix passphrase.
const plainSecretsConfig = `{
  "tools": {"mcp": {"enabled": true, "servers": {"github": {
    "enabled": true, "command": "npx",
    "env": {"GITHUB_PERSONAL_ACCESS_TOKEN": "ghp-plain-token", "DEBUG": "true"},
    "headers": {"Authorization": "Bearer mcp-plain-header"}
  }}}},
  "hooks": {"processes": {"audit": {"enabled": true, "command": ["audit"], "env": {"AUDIT_KEY": "hook-plain-key"}}}},
  "provider_instances": [{"id": "gw", "provider_kind": "openai", "adapter": "openai-compatible", "protocol": "openai",
    "state": "enabled", "headers": {"X-Api-Key": "provider-plain-header"}}],
  "channel_list": {"matrix": {"enabled": false, "type": "matrix", "settings": {"crypto_passphrase": "matrix-plain-pass"}}}
}`

var plainSecretValues = []string{
	"ghp-plain-token", "mcp-plain-header", "hook-plain-key", "provider-plain-header", "matrix-plain-pass",
}

func matrixPassphrase(t *testing.T, cfg *Config) string {
	t.Helper()
	decoded, err := cfg.Channels.Get("matrix").GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	return decoded.(*MatrixSettings).CryptoPassphrase.String()
}

func assertPlainSecretsLoaded(t *testing.T, cfg *Config) {
	t.Helper()
	server := cfg.Tools.MCP.Servers["github"]
	if server.Env["GITHUB_PERSONAL_ACCESS_TOKEN"] != "ghp-plain-token" || server.Env["DEBUG"] != "true" ||
		server.Headers["Authorization"] != "Bearer mcp-plain-header" {
		t.Fatalf("mcp server = %+v, want its env and headers", server)
	}
	if got := cfg.Hooks.Processes["audit"].Env["AUDIT_KEY"]; got != "hook-plain-key" {
		t.Fatalf("hook env AUDIT_KEY = %q", got)
	}
	if got := cfg.ProviderInstances[0].Headers["X-Api-Key"]; got != "provider-plain-header" {
		t.Fatalf("provider header = %q", got)
	}
	if got := matrixPassphrase(t, cfg); got != "matrix-plain-pass" {
		t.Fatalf("matrix crypto_passphrase = %q", got)
	}
}

// Plain secrets in config.json load; saving moves them to .security.yml,
// leaves config.json with placeholders, and loads back.
func TestSecretSettingsMoveToSecurityFile(t *testing.T) {
	path := writeEnvTestConfig(t, plainSecretsConfig, "")

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	assertPlainSecretsLoaded(t, cfg)

	if err := SaveConfig(path, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	saved := readFile(t, path)
	secrets := readFile(t, filepath.Join(filepath.Dir(path), SecurityConfigFile))
	for _, value := range plainSecretValues {
		if strings.Contains(saved, value) {
			t.Fatalf("config.json still shows %q:\n%s", value, saved)
		}
		if !strings.Contains(secrets, value) {
			t.Fatalf(".security.yml lacks %q:\n%s", value, secrets)
		}
	}
	if !strings.Contains(saved, `"GITHUB_PERSONAL_ACCESS_TOKEN": "[NOT_HERE]"`) {
		t.Fatalf("config.json no longer names the env variable:\n%s", saved)
	}

	reloaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() after save error = %v", err)
	}
	assertPlainSecretsLoaded(t, reloaded)
}

// The dashboard edits a config decoded from the masked JSON; the secrets in
// .security.yml must survive, and a placeholder is never handed out as a
// value.
func TestSecretSettingsSurviveAMaskedJSONRoundTrip(t *testing.T) {
	path := writeEnvTestConfig(t, plainSecretsConfig, "")
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if err := SaveConfig(path, loaded); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	data, err := json.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range plainSecretValues {
		if strings.Contains(string(data), value) {
			t.Fatalf("JSON shows %q", value)
		}
	}

	var edited Config
	if err := json.Unmarshal(data, &edited); err != nil {
		t.Fatal(err)
	}
	if err := edited.SecurityCopyFrom(path); err != nil {
		t.Fatalf("SecurityCopyFrom() error = %v", err)
	}
	assertPlainSecretsLoaded(t, &edited)

	// Saving the decoded config as is keeps the stored values too.
	var direct Config
	if err := json.Unmarshal(data, &direct); err != nil {
		t.Fatal(err)
	}
	if err := SaveConfig(path, &direct); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	reloaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	server := reloaded.Tools.MCP.Servers["github"]
	if server.Env["GITHUB_PERSONAL_ACCESS_TOKEN"] != "ghp-plain-token" ||
		reloaded.ProviderInstances[0].Headers["X-Api-Key"] != "provider-plain-header" {
		t.Fatalf("saving the decoded config lost the stored secrets: %+v", server)
	}
}

func TestSecretPlaceholderWithoutAValueIsDropped(t *testing.T) {
	path := writeEnvTestConfig(t, `{
  "tools": {"mcp": {"servers": {"github": {"enabled": true, "command": "npx",
    "env": {"TOKEN": "[NOT_HERE]", "MODE": "fast"}}}}}
}`, "")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	env := cfg.Tools.MCP.Servers["github"].Env
	if _, ok := env["TOKEN"]; ok || env["MODE"] != "fast" {
		t.Fatalf("env = %v, want the unfilled placeholder dropped and MODE kept", env)
	}
}

func TestSecretSettingsAreFilteredAndRedacted(t *testing.T) {
	path := writeEnvTestConfig(t, plainSecretsConfig, "")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	sensitive := cfg.collectSensitiveValues()
	for _, value := range []string{
		"ghp-plain-token", "Bearer mcp-plain-header", "hook-plain-key", "provider-plain-header", "matrix-plain-pass",
	} {
		found := false
		for _, s := range sensitive {
			found = found || s == value
		}
		if !found {
			t.Fatalf("sensitive values %v lack %q", sensitive, value)
		}
	}
	for _, s := range sensitive {
		if s == "true" {
			t.Fatal("a flag value is treated as a secret")
		}
	}
	if got := cfg.FilterSensitiveData("token=ghp-plain-token"); strings.Contains(got, "ghp-plain-token") {
		t.Fatalf("FilterSensitiveData() = %q", got)
	}
	if got := logger.Redact("pass matrix-plain-pass"); strings.Contains(got, "matrix-plain-pass") {
		t.Fatalf("logger.Redact() = %q, want the passphrase registered", got)
	}
}

// A secret .security.yml holds as a file:// reference stays a reference
// when the config is saved, after a load and after the config API's round
// trip through masked JSON, until its value is changed.
func TestSecretFileReferencesSurviveSaves(t *testing.T) {
	path := writeEnvTestConfig(t, `{
  "tools": {"mcp": {"enabled": true, "servers": {"github": {
    "enabled": true, "command": "npx", "env": {"TOKEN": "[NOT_HERE]", "DEBUG": "true"}
  }}}},
  "hooks": {"processes": {"audit": {"enabled": true, "command": ["audit"], "env": {"AUDIT_KEY": "[NOT_HERE]"}}}},
  "provider_instances": [{"id": "gw", "provider_kind": "openai", "adapter": "openai-compatible", "protocol": "openai",
    "state": "enabled", "headers": {"X-Api-Key": "[NOT_HERE]"}}],
  "channel_list": {"matrix": {"enabled": false, "type": "matrix", "settings": {}}}
}`, `channel_list:
  matrix:
    settings:
      crypto_passphrase: file://matrix.pass
mcp:
  servers:
    github:
      env:
        TOKEN: file://gh.token
hooks:
  processes:
    audit:
      env:
        AUDIT_KEY: file://hook.key
provider_instances:
  gw:
    headers:
      X-Api-Key: file://provider.key
`)
	dir := filepath.Dir(path)
	files := map[string]string{
		"gh.token": "ghp-from-file", "hook.key": "hook-from-file",
		"provider.key": "provider-from-file", "matrix.pass": "matrix-from-file",
	}
	for name, value := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	references := []string{
		"TOKEN: file://gh.token", "AUDIT_KEY: file://hook.key", "X-Api-Key: file://provider.key",
		"crypto_passphrase: file://matrix.pass",
	}
	assertReferencesKept := func(t *testing.T, step string, references []string) {
		t.Helper()
		secrets := readFile(t, filepath.Join(dir, SecurityConfigFile))
		for _, ref := range references {
			if !strings.Contains(secrets, ref) {
				t.Fatalf("%s: .security.yml lost %q:\n%s", step, ref, secrets)
			}
		}
		for _, value := range files {
			if strings.Contains(secrets, value) {
				t.Fatalf("%s: .security.yml holds the value %q:\n%s", step, value, secrets)
			}
		}
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if got := cfg.Tools.MCP.Servers["github"].Env["TOKEN"]; got != "ghp-from-file" {
		t.Fatalf("TOKEN = %q, want the referenced file's value", got)
	}
	if got := matrixPassphrase(t, cfg); got != "matrix-from-file" {
		t.Fatalf("crypto_passphrase = %q, want the referenced file's value", got)
	}
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	assertReferencesKept(t, "load and save", references)

	// The config API decodes the masked JSON into a fresh config, fills its
	// secrets from .security.yml and saves it.
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var edited Config
	if err := json.Unmarshal(data, &edited); err != nil {
		t.Fatal(err)
	}
	if err := edited.SecurityCopyFrom(path); err != nil {
		t.Fatalf("SecurityCopyFrom() error = %v", err)
	}
	if err := SaveConfig(path, &edited); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	assertReferencesKept(t, "config API save", references)

	// Saved as decoded, a map's placeholder keeps the stored reference too.
	// (The masked JSON has no channel secrets: those come back only through
	// SecurityCopyFrom.)
	var direct Config
	if err := json.Unmarshal(data, &direct); err != nil {
		t.Fatal(err)
	}
	if err := SaveConfig(path, &direct); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	assertReferencesKept(t, "masked JSON save", references[:3])

	// A changed value is written as given.
	reloaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	reloaded.Tools.MCP.Servers["github"].Env["TOKEN"] = "ghp-changed"
	if err := SaveConfig(path, reloaded); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	secrets := readFile(t, filepath.Join(dir, SecurityConfigFile))
	if !strings.Contains(secrets, "TOKEN: ghp-changed") || strings.Contains(secrets, "file://gh.token") {
		t.Fatalf(".security.yml does not hold the changed value as given:\n%s", secrets)
	}
	if !strings.Contains(secrets, "AUDIT_KEY: file://hook.key") {
		t.Fatalf(".security.yml lost an unchanged reference:\n%s", secrets)
	}
}

// The config API sends the config as JSON, with a placeholder for each set
// secret and channel settings without theirs, and decodes what comes back:
// the stored secrets are kept, and an edited one is saved.
func TestSecureStringsRoundTripThroughTheConfigAPI(t *testing.T) {
	path := writeEnvTestConfig(t, `{"channel_list": {"telegram": {"enabled": true, "type": "telegram", "settings": {}}}}`, "")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.Tools.Web.Brave.SetAPIKey("brave-stored")
	cfg.Tools.Web.Tavily.SetAPIKey("tavily-stored")
	telegram, err := cfg.Channels.Get("telegram").GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	telegram.(*TelegramSettings).Token = *NewSecureString("telegram-stored")
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"brave-stored", "tavily-stored", "telegram-stored"} {
		if strings.Contains(string(data), value) {
			t.Fatalf("JSON shows %q", value)
		}
	}
	if !strings.Contains(string(data), `"api_keys":"[NOT_HERE]"`) {
		t.Fatalf("JSON lacks the placeholder of a set secret:\n%s", data)
	}

	var edited Config
	if err := json.Unmarshal(data, &edited); err != nil {
		t.Fatal(err)
	}
	if got := edited.Tools.Web.Brave.APIKey(); got != "" {
		t.Fatalf("the placeholder read back as %q, want unset", got)
	}
	if err := edited.SecurityCopyFrom(path); err != nil {
		t.Fatalf("SecurityCopyFrom() error = %v", err)
	}
	edited.Tools.Web.Tavily.SetAPIKey("tavily-new")
	if err := SaveConfig(path, &edited); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	reloaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if got := reloaded.Tools.Web.Brave.APIKey(); got != "brave-stored" {
		t.Errorf("brave key = %q, want the stored one kept", got)
	}
	if got := reloaded.Tools.Web.Tavily.APIKey(); got != "tavily-new" {
		t.Errorf("tavily key = %q, want the edit saved", got)
	}
	decoded, err := reloaded.Channels.Get("telegram").GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	if got := decoded.(*TelegramSettings).Token.String(); got != "telegram-stored" {
		t.Errorf("telegram token = %q, want the stored one kept", got)
	}
}

// .security.yml keeps its format: plain values, file:// references as
// written, and only the secrets that are set.
func TestSecurityFileOutput(t *testing.T) {
	cfg := &Config{Channels: ChannelsConfig{}}
	cfg.Tools.Web.Brave.APIKeys = SimpleSecureStrings("brave-key", "file://brave.key")
	cfg.Tools.Skills.Registries = SkillsRegistriesConfig{
		&SkillRegistryConfig{Name: "github", AuthToken: *NewSecureString("ghp-token")},
	}
	cfg.Tools.MCP.Servers = map[string]MCPServerConfig{
		"github": {Command: "npx", Env: map[string]string{"TOKEN": "mcp-token"}},
	}
	telegram := &Channel{Type: ChannelTelegram, Enabled: true}
	if err := telegram.Decode(&TelegramSettings{Token: *NewSecureString("tg-token")}); err != nil {
		t.Fatal(err)
	}
	cfg.Channels["telegram"] = telegram

	data, err := marshalSecurityConfig(cfg, secretMapsFile{})
	if err != nil {
		t.Fatalf("marshalSecurityConfig() error = %v", err)
	}
	const want = `channel_list:
  telegram:
    settings:
      token: tg-token
web:
  brave:
    api_keys:
      - brave-key
      - file://brave.key
skills:
  registries:
    github:
      auth_token: ghp-token
mcp:
  servers:
    github:
      env:
        TOKEN: mcp-token
`
	if string(data) != want {
		t.Fatalf(".security.yml =\n%s\nwant\n%s", data, want)
	}
}
