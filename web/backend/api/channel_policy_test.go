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

// A channel's page shows its Allow From, and none of the access settings
// that are gone.
func TestHandleGetChannelConfig_ShowsNoRetiredAccessSettings(t *testing.T) {
	configPath, cleanup := setupCredentialTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.Channels.Get(config.ChannelSlack).AllowFrom = config.FlexibleStringSlice{"slack:U111"}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	for _, name := range []string{"slack", "whatsapp", "web"} {
		got := getChannelConfigMap(t, configPath, name)
		for _, key := range []string{"dm_policy", "group_policy", "group_trigger"} {
			if _, ok := got[key]; ok {
				t.Errorf("%s config has %s = %#v", name, key, got[key])
			}
		}
	}
	if got := getChannelConfigMap(t, configPath, "slack")["allow_from"]; len(got.([]any)) != 1 {
		t.Fatalf("slack allow_from = %#v", got)
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

// An older client may still send the retired access settings: the save
// succeeds, and drops them.
func TestHandlePatchConfig_DropsRetiredAccessSettings(t *testing.T) {
	configPath, cleanup := setupCredentialTestEnv(t)
	defer cleanup()

	patchConfig(t, configPath, `{"channel_list":{"slack":{"allow_from":["slack:U111"],"dm_policy":"open",`+
		`"group_policy":"disabled","group_trigger":{"mention_only":false,"prefixes":"!,?"}}}}`, http.StatusOK)

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	for _, key := range []string{`"dm_policy"`, `"group_policy"`, `"group_trigger"`} {
		if strings.Contains(string(data), key) {
			t.Errorf("the saved config has %s:\n%s", key, data)
		}
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if tg := cfg.Channels.Get(config.ChannelSlack); len(tg.AllowFrom) != 1 {
		t.Fatalf("slack allow_from = %v", tg.AllowFrom)
	}
}

func TestHandlePatchConfig_RejectsUnknownPolicyValues(t *testing.T) {
	for name, body := range map[string]string{
		"chats":    `{"channel_list":{"whatsapp":{"settings":{"use_native":true,"chats":"friends"}}}}`,
		"approval": `{"tools":{"approval":{"rules":[{"tool":"exec","action":"maybe"}]}}}`,
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
