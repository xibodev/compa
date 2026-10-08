package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/xibodev/compa/v3/pkg/config"
)

func getChannelConfigMap(t *testing.T, configPath, name string) map[string]any {
	t.Helper()
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/channels/"+name+"/config", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s config status = %d, body = %s", name, rec.Code, rec.Body.String())
	}
	var resp struct {
		Config map[string]any `json:"config"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	return resp.Config
}

func TestHandleGetChannelConfig_ReturnsPoliciesAndMentionOnly(t *testing.T) {
	configPath, cleanup := setupCredentialTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	tg := cfg.Channels.Get(config.ChannelSlack)
	tg.DMPolicy = config.DMPolicyAllowlist
	tg.GroupPolicy = config.GroupPolicyDisabled
	tg.GroupTrigger.MentionOnly = false
	// No policy saved: the policies allow_from implies are shown.
	dc := cfg.Channels.Get(config.ChannelWhatsApp)
	dc.DMPolicy, dc.GroupPolicy = "", ""
	dc.AllowFrom = config.FlexibleStringSlice{"*"}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	got := getChannelConfigMap(t, configPath, "slack")
	if got["dm_policy"] != "allowlist" || got["group_policy"] != "disabled" {
		t.Fatalf("telegram policies = %#v/%#v, want allowlist/disabled", got["dm_policy"], got["group_policy"])
	}
	trigger, ok := got["group_trigger"].(map[string]any)
	if !ok || trigger["mention_only"] != false {
		t.Fatalf("telegram group_trigger = %#v, want mention_only false present", got["group_trigger"])
	}

	got = getChannelConfigMap(t, configPath, "whatsapp")
	if got["dm_policy"] != "open" || got["group_policy"] != "open" {
		t.Fatalf("discord policies = %#v/%#v, want open/open from allow_from *", got["dm_policy"], got["group_policy"])
	}
}

func TestHandleGetChannelConfig_ReturnsNativeWhatsAppChats(t *testing.T) {
	configPath, cleanup := setupCredentialTestEnv(t)
	defer cleanup()

	if got := getChannelConfigMap(t, configPath, "whatsapp_native")["chats"]; got != "self" {
		t.Fatalf("chats = %#v, want the default self", got)
	}
	if _, ok := getChannelConfigMap(t, configPath, "whatsapp")["chats"]; ok {
		t.Fatal("the bridge variant shows chats, which only the native client reads")
	}

	patchConfig(t, configPath, `{"channel_list":{"whatsapp":{"type":"whatsapp","settings":{"use_native":true,"chats":"all"}}}}`, http.StatusOK)
	if got := getChannelConfigMap(t, configPath, "whatsapp_native")["chats"]; got != "all" {
		t.Fatalf("chats = %#v, want the saved all", got)
	}
}

func patchConfig(t *testing.T, configPath, body string, wantStatus int) *httptest.ResponseRecorder {
	t.Helper()
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodPatch, "/api/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != wantStatus {
		t.Fatalf("PATCH %s status = %d, want %d, body = %s", body, rec.Code, wantStatus, rec.Body.String())
	}
	return rec
}

func TestHandlePatchConfig_SavesChannelPolicies(t *testing.T) {
	configPath, cleanup := setupCredentialTestEnv(t)
	defer cleanup()

	patchConfig(t, configPath, `{"channel_list":{"slack":{"dm_policy":"open","group_policy":"disabled","group_trigger":{"mention_only":false}}}}`, http.StatusOK)

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	tg := cfg.Channels.Get(config.ChannelSlack)
	if tg.DMPolicy != config.DMPolicyOpen || tg.GroupPolicy != config.GroupPolicyDisabled || tg.GroupTrigger.MentionOnly {
		t.Fatalf("telegram = %q/%q mention_only=%v, want open/disabled false", tg.DMPolicy, tg.GroupPolicy, tg.GroupTrigger.MentionOnly)
	}
}

func TestHandlePatchConfig_RejectsUnknownPolicyValues(t *testing.T) {
	for name, body := range map[string]string{
		"dm_policy":    `{"channel_list":{"telegram":{"dm_policy":"everyone"}}}`,
		"group_policy": `{"channel_list":{"telegram":{"group_policy":"pairing"}}}`,
		"chats":        `{"channel_list":{"whatsapp":{"settings":{"use_native":true,"chats":"friends"}}}}`,
		"approval":     `{"tools":{"approval":{"rules":[{"tool":"exec","action":"maybe"}]}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			configPath, cleanup := setupCredentialTestEnv(t)
			defer cleanup()
			before, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatalf("ReadFile() error = %v", err)
			}

			rec := patchConfig(t, configPath, body, http.StatusBadRequest)
			var resp struct {
				Status string   `json:"status"`
				Errors []string `json:"errors"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.Status != "validation_error" || len(resp.Errors) == 0 {
				t.Fatalf("body = %s, want a validation_error naming the value", rec.Body.String())
			}
			after, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatalf("ReadFile() error = %v", err)
			}
			if string(after) != string(before) {
				t.Fatal("a rejected value was saved")
			}
			if _, err := config.LoadConfig(configPath); err != nil {
				t.Fatalf("the config no longer loads: %v", err)
			}
		})
	}
}
