package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/xibodev/compa/v3/pkg/config"
)

func TestHandleGetChannelConfig_ReturnsSecretPresenceWithoutLeakingSecrets(t *testing.T) {
	configPath, cleanup := setupCredentialTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	bc := cfg.Channels[config.ChannelSlack]
	bc.Enabled = true
	decoded, err := bc.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	bcfg := decoded.(*config.SlackSettings)
	bcfg.BotToken = *config.NewSecureString("xoxb-secret-from-security")
	bc.AllowFrom = config.FlexibleStringSlice{"U0TESTUSER"}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/channels/slack/config", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf(
			"GET /api/channels/slack/config status = %d, want %d, body=%s",
			rec.Code,
			http.StatusOK,
			rec.Body.String(),
		)
	}
	if strings.Contains(rec.Body.String(), "xoxb-secret-from-security") {
		t.Fatalf("response leaked secret value: %s", rec.Body.String())
	}

	var resp struct {
		Config            map[string]any `json:"config"`
		ConfiguredSecrets []string       `json:"configured_secrets"`
		ConfigKey         string         `json:"config_key"`
		Variant           string         `json:"variant"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if got := resp.ConfigKey; got != "slack" {
		t.Fatalf("config_key = %q, want %q", got, "slack")
	}
	if got := resp.Config["enabled"]; got != true {
		t.Fatalf("config.enabled = %#v, want true", got)
	}
	allowFrom, ok := resp.Config["allow_from"].([]any)
	if !ok || len(allowFrom) != 1 || allowFrom[0] != "U0TESTUSER" {
		t.Fatalf("config.allow_from = %#v, want [\"U0TESTUSER\"]", resp.Config["allow_from"])
	}
	if _, exists := resp.Config["bot_token"]; exists {
		t.Fatalf("config should omit bot_token, got %#v", resp.Config["bot_token"])
	}
	if len(resp.ConfiguredSecrets) != 1 || resp.ConfiguredSecrets[0] != "bot_token" {
		t.Fatalf("configured_secrets = %#v, want [\"bot_token\"]", resp.ConfiguredSecrets)
	}
}

func TestHandleGetChannelConfig_ReturnsNotFoundForUnknownChannel(t *testing.T) {
	configPath, cleanup := setupCredentialTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/channels/not-a-channel/config", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /api/channels/not-a-channel/config status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestHandleGetChannelConfig_ReturnsCommonFieldsWhenSettingsEmpty(t *testing.T) {
	configPath, cleanup := setupCredentialTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	bc := cfg.Channels[config.ChannelDiscord]
	bc.Enabled = true
	bc.AllowFrom = config.FlexibleStringSlice{"discord:1234"}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/channels/discord/config", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf(
			"GET /api/channels/discord/config status = %d, want %d, body=%s",
			rec.Code,
			http.StatusOK,
			rec.Body.String(),
		)
	}

	var resp struct {
		Config map[string]any `json:"config"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if got := resp.Config["enabled"]; got != true {
		t.Fatalf("config.enabled = %#v, want true", got)
	}
	allowFrom, ok := resp.Config["allow_from"].([]any)
	if !ok || len(allowFrom) != 1 || allowFrom[0] != "discord:1234" {
		t.Fatalf("config.allow_from = %#v, want [\"discord:1234\"]", resp.Config["allow_from"])
	}
}

func TestHandleGetChannelConfig_OmitsUnconfiguredStreaming(t *testing.T) {
	configPath, cleanup := setupCredentialTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/channels/telegram/config", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf(
			"GET /api/channels/telegram/config status = %d, want %d, body=%s",
			rec.Code,
			http.StatusOK,
			rec.Body.String(),
		)
	}

	var resp struct {
		Config map[string]any `json:"config"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if _, ok := resp.Config["streaming"]; ok {
		t.Fatalf("config.streaming = %#v, want omitted when not configured", resp.Config["streaming"])
	}
}

func TestHandleGetChannelConfig_ReturnsConfiguredStreaming(t *testing.T) {
	configPath, cleanup := setupCredentialTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	web := cfg.Channels.Get(config.ChannelWeb)
	if web == nil {
		t.Fatal("missing web channel")
	}
	web.Settings = config.RawNode(`{"streaming":{"enabled":true,"throttle_seconds":2,"min_growth_chars":80}}`)
	if err := config.InitChannelList(cfg.Channels); err != nil {
		t.Fatalf("InitChannelList() error = %v", err)
	}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/channels/web/config", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf(
			"GET /api/channels/web/config status = %d, want %d, body=%s",
			rec.Code,
			http.StatusOK,
			rec.Body.String(),
		)
	}

	var resp struct {
		Config map[string]any `json:"config"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	streaming, ok := resp.Config["streaming"].(map[string]any)
	if !ok {
		t.Fatalf("config.streaming = %#v, want object", resp.Config["streaming"])
	}
	if got := streaming["enabled"]; got != true {
		t.Fatalf("config.streaming.enabled = %#v, want true", got)
	}
	if got := streaming["throttle_seconds"]; got != float64(2) {
		t.Fatalf("config.streaming.throttle_seconds = %#v, want 2", got)
	}
	if got := streaming["min_growth_chars"]; got != float64(80) {
		t.Fatalf("config.streaming.min_growth_chars = %#v, want 80", got)
	}
}

func TestHandleGetChannelConfig_ReturnsDefaultShapeForMissingChannel(t *testing.T) {
	configPath, cleanup := setupCredentialTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	delete(cfg.Channels, config.ChannelWhatsApp)
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/channels/whatsapp/config", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf(
			"GET /api/channels/whatsapp/config status = %d, want %d, body=%s",
			rec.Code,
			http.StatusOK,
			rec.Body.String(),
		)
	}

	var resp struct {
		Config map[string]any `json:"config"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if got := resp.Config["bridge_url"]; got != "ws://localhost:3001" {
		t.Fatalf("config.bridge_url = %#v, want the default %q", got, "ws://localhost:3001")
	}
	if got := resp.Config["enabled"]; got != false {
		t.Fatalf("config.enabled = %#v, want false", got)
	}
}

// The catalog lists the channels this build includes: the supported ones
// always, the paused ones only with the paused_channels build tag, and
// native WhatsApp only with whatsapp_native.
func TestChannelCatalogListsTheChannelsInTheBuild(t *testing.T) {
	configPath, cleanup := setupCredentialTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/channels/catalog", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/channels/catalog status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Channels []channelCatalogItem `json:"channels"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	var names []string
	for _, channel := range resp.Channels {
		names = append(names, channel.Name)
	}

	want := []string{"telegram", "discord", "slack", "whatsapp", "web"}
	if config.PausedChannelsInBuild {
		want = append(want, "weixin", "feishu", "dingtalk", "line", "qq", "onebot", "wecom", "maixcam", "matrix", "irc", "mqtt")
	}
	if config.WhatsAppNativeInBuild {
		want = append(want, "whatsapp_native")
	}
	slices.Sort(names)
	slices.Sort(want)
	if !slices.Equal(names, want) {
		t.Fatalf("catalog = %v, want %v", names, want)
	}
}

// In a build without the paused channels, their pages and the WeChat and
// WeCom QR logins are not found.
func TestPausedChannelsAreNotFoundInABuildWithoutThem(t *testing.T) {
	if config.PausedChannelsInBuild {
		t.Skip("this build includes the paused channels")
	}
	configPath, cleanup := setupCredentialTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	for _, req := range []struct{ method, path string }{
		{http.MethodGet, "/api/channels/onebot/config"},
		{http.MethodGet, "/api/channels/weixin/config"},
		{http.MethodPost, "/api/weixin/flows"},
		{http.MethodGet, "/api/weixin/flows/some-flow"},
		{http.MethodPost, "/api/wecom/flows"},
		{http.MethodGet, "/api/wecom/flows/some-flow"},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(req.method, req.path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s status = %d, want %d", req.method, req.path, rec.Code, http.StatusNotFound)
		}
	}
}
