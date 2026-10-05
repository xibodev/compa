package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// envKeys returns the environment variable names env.Parse reads for t,
// with the envPrefix of every enclosing field applied, as env.Parse applies
// them.
func envKeys(t reflect.Type, prefix string, depth int) []string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || depth > 12 {
		return nil
	}
	var keys []string
	for i := range t.NumField() {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(field.Tag.Get("env"), ",")
		if name == "-" {
			continue
		}
		if name != "" {
			keys = append(keys, prefix+name)
		}
		nested := prefix + field.Tag.Get("envPrefix")
		switch ft := field.Type; {
		case ft.Kind() == reflect.Struct, ft.Kind() == reflect.Pointer && ft.Elem().Kind() == reflect.Struct:
			keys = append(keys, envKeys(ft, nested, depth+1)...)
		case ft.Kind() == reflect.Slice && ft.Elem().Kind() == reflect.Struct:
			// env.Parse numbers the elements of a slice of structs.
			keys = append(keys, envKeys(ft.Elem(), nested+"0_", depth+1)...)
		}
	}
	return keys
}

// Every environment variable Compa reads carries the COMPA_ prefix, once, so
// an unrelated variable such as SECRET never becomes a setting.
func TestEveryEnvOverrideHasTheCompaPrefix(t *testing.T) {
	types := map[string]reflect.Type{"config": reflect.TypeOf(Config{})}
	channelSettingsMu.RLock()
	for name, proto := range channelSettingsFactory {
		types["channel "+name] = reflect.TypeOf(proto)
	}
	channelSettingsMu.RUnlock()

	names := make([]string, 0, len(types))
	for name := range types {
		names = append(names, name)
	}
	sort.Strings(names)

	checked := 0
	for _, name := range names {
		for _, key := range envKeys(types[name], "", 0) {
			checked++
			if !strings.HasPrefix(key, "COMPA_") || strings.Count(key, "COMPA_") != 1 {
				t.Errorf("%s reads environment variable %q, want one COMPA_ prefix", name, key)
			}
		}
	}
	if checked < 100 {
		t.Fatalf("checked only %d environment variables; the walk is missing fields", checked)
	}
}

func TestWeComIgnoresUnprefixedEnvironment(t *testing.T) {
	t.Setenv("SECRET", "unrelated-secret")
	t.Setenv("BOT_ID", "unrelated-bot")
	t.Setenv("COMPA_CHANNELS_WECOM_WEBSOCKET_URL", "wss://wecom.example")

	channels := ChannelsConfig{
		"wecom": {Type: ChannelWeCom, Settings: RawNode(`{"bot_id":"bot-1"}`)},
	}
	if err := InitChannelList(channels); err != nil {
		t.Fatalf("InitChannelList() error = %v", err)
	}
	decoded, err := channels["wecom"].GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	settings := decoded.(*WeComSettings)
	if settings.Secret.String() != "" || settings.BotID != "bot-1" {
		t.Fatalf("WeCom took unprefixed variables: secret=%q bot_id=%q", settings.Secret.String(), settings.BotID)
	}
	if settings.WebSocketURL != "wss://wecom.example" {
		t.Fatalf("websocket_url = %q, want the COMPA_CHANNELS_WECOM_WEBSOCKET_URL value", settings.WebSocketURL)
	}
}

func TestChannelEnvOverrideErrorNamesTheChannel(t *testing.T) {
	t.Setenv("COMPA_CHANNELS_TELEGRAM_MEDIA_GROUP_DELAY_MS", "soon")

	channels := ChannelsConfig{"my_bot": {Type: ChannelTelegram}}
	err := InitChannelList(channels)
	if err == nil {
		t.Fatal("InitChannelList() accepted a malformed environment override")
	}
	if !strings.Contains(err.Error(), `"my_bot"`) || !strings.Contains(err.Error(), "MediaGroupDelayMS") {
		t.Fatalf("error = %q, want the channel and the setting named", err)
	}
}

func TestSubTurnEnvOverridesUseTheirDocumentedNames(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"agents":{"defaults":{"workspace":"./workspace"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COMPA_AGENTS_DEFAULTS_SUBTURN_MAX_DEPTH", "7")

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.Agents.Defaults.SubTurn.MaxDepth != 7 {
		t.Fatalf("subturn.max_depth = %d, want 7 from COMPA_AGENTS_DEFAULTS_SUBTURN_MAX_DEPTH", cfg.Agents.Defaults.SubTurn.MaxDepth)
	}
}

// writeEnvTestConfig writes config.json, and .security.yml when secrets is
// not empty, into a fresh directory and returns the config path.
func writeEnvTestConfig(t *testing.T, configJSON, secrets string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(EnvHome, dir)
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(configJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if secrets != "" {
		if err := os.WriteFile(filepath.Join(dir, SecurityConfigFile), []byte(secrets), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(data)
}

func telegramSettings(t *testing.T, cfg *Config) *TelegramSettings {
	t.Helper()
	decoded, err := cfg.Channels.Get("telegram").GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	return decoded.(*TelegramSettings)
}

const envTestConfig = `{
  "gateway": {"host": "localhost", "port": 18790},
  "channel_list": {"telegram": {"enabled": true, "type": "telegram", "settings": {"base_url": "https://api.telegram.org"}}}
}`

// A token set only in the environment must not reach .security.yml when an
// unrelated setting is saved, and a file's own token must survive.
func TestSaveConfigKeepsEnvOnlySecretsOutOfTheFiles(t *testing.T) {
	for _, tc := range []struct {
		name, secrets, wantSaved string
	}{
		{name: "no file token", secrets: ""},
		{
			name:      "file token",
			secrets:   "channel_list:\n  telegram:\n    settings:\n      token: tg-from-file\n",
			wantSaved: "tg-from-file",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeEnvTestConfig(t, envTestConfig, tc.secrets)
			t.Setenv("COMPA_CHANNELS_TELEGRAM_TOKEN", "tg-from-env")
			t.Setenv("COMPA_SKILLS_REGISTRIES_GITHUB_AUTH_TOKEN", "ghp-from-env")

			cfg, err := LoadConfig(path)
			if err != nil {
				t.Fatalf("LoadConfig() error = %v", err)
			}
			if got := telegramSettings(t, cfg).Token.String(); got != "tg-from-env" {
				t.Fatalf("loaded token = %q, want the environment's", got)
			}
			cfg.Agents.Defaults.MaxTokens = 1234
			if err := SaveConfig(path, cfg); err != nil {
				t.Fatalf("SaveConfig() error = %v", err)
			}

			secrets := readFile(t, filepath.Join(filepath.Dir(path), SecurityConfigFile))
			saved := readFile(t, path)
			for _, envValue := range []string{"tg-from-env", "ghp-from-env"} {
				if strings.Contains(secrets, envValue) || strings.Contains(saved, envValue) {
					t.Fatalf("the environment's %q was written:\n%s\n%s", envValue, secrets, saved)
				}
			}
			if tc.wantSaved != "" && !strings.Contains(secrets, tc.wantSaved) {
				t.Fatalf(".security.yml lost the file's token:\n%s", secrets)
			}
			if !strings.Contains(saved, `"max_tokens": 1234`) {
				t.Fatalf("the unrelated change was not saved:\n%s", saved)
			}
			// Saving does not touch the config in use.
			if got := telegramSettings(t, cfg).Token.String(); got != "tg-from-env" {
				t.Fatalf("SaveConfig changed the live token to %q", got)
			}
		})
	}
}

func TestSaveConfigKeepsEnvOverriddenSettingsOutOfTheFiles(t *testing.T) {
	path := writeEnvTestConfig(t, envTestConfig, "")
	t.Setenv("COMPA_GATEWAY_PORT", "19999")
	t.Setenv(EnvGatewayHost, "0.0.0.0")
	t.Setenv("COMPA_CHANNELS_TELEGRAM_BASE_URL", "https://telegram.example")

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.Gateway.Port != 19999 || cfg.Gateway.Host != "0.0.0.0" {
		t.Fatalf("gateway = %+v, want the environment's host and port", cfg.Gateway)
	}
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	var saved struct {
		Gateway  GatewayConfig `json:"gateway"`
		Channels map[string]struct {
			Settings TelegramSettings `json:"settings"`
		} `json:"channel_list"`
	}
	if err := json.Unmarshal([]byte(readFile(t, path)), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Gateway.Port != 18790 || saved.Gateway.Host != "localhost" {
		t.Fatalf("saved gateway = %+v, want the file's localhost:18790", saved.Gateway)
	}
	if got := saved.Channels["telegram"].Settings.BaseURL; got != "https://api.telegram.org" {
		t.Fatalf("saved telegram base_url = %q, want the file's", got)
	}
	if cfg.Gateway.Port != 19999 {
		t.Fatalf("SaveConfig changed the live port to %d", cfg.Gateway.Port)
	}
}

// A setting changed after the load is the user's, even where the
// environment set it, and is saved.
func TestSaveConfigWritesSettingsChangedAfterLoad(t *testing.T) {
	path := writeEnvTestConfig(t, envTestConfig, "")
	t.Setenv("COMPA_GATEWAY_PORT", "19999")
	t.Setenv("COMPA_CHANNELS_TELEGRAM_TOKEN", "tg-from-env")

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.Gateway.Port = 20000
	telegramSettings(t, cfg).Token = *NewSecureString("tg-from-user")
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	if saved := readFile(t, path); !strings.Contains(saved, `"port": 20000`) {
		t.Fatalf("the changed port was not saved:\n%s", saved)
	}
	if secrets := readFile(t, filepath.Join(filepath.Dir(path), SecurityConfigFile)); !strings.Contains(secrets, "tg-from-user") {
		t.Fatalf("the changed token was not saved:\n%s", secrets)
	}
}

// A config rebuilt from a loaded one's JSON, as the dashboard's settings
// handlers do, inherits the record of what the environment set.
func TestInheritEnvOverridesCoversAJSONCopy(t *testing.T) {
	path := writeEnvTestConfig(t, envTestConfig, "")
	t.Setenv("COMPA_GATEWAY_PORT", "19999")

	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	data, err := json.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	var rebuilt Config
	if err := json.Unmarshal(data, &rebuilt); err != nil {
		t.Fatal(err)
	}
	rebuilt.InheritEnvOverrides(loaded)
	if err := SaveConfig(path, &rebuilt); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	if saved := readFile(t, path); !strings.Contains(saved, `"port": 18790`) {
		t.Fatalf("the environment's port was saved:\n%s", saved)
	}
}
