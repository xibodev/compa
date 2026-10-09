package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xibodev/compa/v4/pkg/auth"
	"github.com/xibodev/compa/v4/pkg/config"
)

func credentialRequest(t *testing.T, mux http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	return rec
}

func TestSaveCredentialRejectsInvalidRequests(t *testing.T) {
	configPath, cleanup := setupCredentialTestEnv(t)
	defer cleanup()
	resetCredentialHooks(t)

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	bodies := []string{
		`{"provider":"unknown-provider","token":"x"}`,
		`{"token":"x"}`,
		`{"provider":"openai"}`,
		`{"provider":"anthropic","token":"   "}`,
		`not json`,
	}
	for _, body := range bodies {
		rec := credentialRequest(t, mux, http.MethodPost, "/api/credentials", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want %d, body=%s", body, rec.Code, http.StatusBadRequest, rec.Body.String())
		}
	}
	if rec := credentialRequest(t, mux, http.MethodDelete, "/api/credentials/unknown-provider", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("delete unknown provider status = %d, body=%s", rec.Code, rec.Body.String())
	}
}

func TestCredentialProvidersReportStoredKeys(t *testing.T) {
	configPath, cleanup := setupCredentialTestEnv(t)
	defer cleanup()
	resetCredentialHooks(t)
	if err := auth.SetCredential(credentialProviderAnthropic, &auth.AuthCredential{AccessToken: "anthropic-key"}); err != nil {
		t.Fatal(err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	rec := credentialRequest(t, mux, http.MethodGet, "/api/credentials/providers", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	want := []map[string]any{
		{"provider": "openai", "display_name": "OpenAI", "logged_in": false},
		{"provider": "anthropic", "display_name": "Anthropic", "logged_in": true},
	}
	if len(resp.Providers) != len(want) {
		t.Fatalf("providers = %#v", resp.Providers)
	}
	for i, provider := range resp.Providers {
		if len(provider) != len(want[i]) {
			t.Fatalf("provider %d fields = %#v, want %#v", i, provider, want[i])
		}
		for field, value := range want[i] {
			if provider[field] != value {
				t.Fatalf("provider %d %s = %#v, want %#v", i, field, provider[field], value)
			}
		}
	}
}

func TestOpenAIAPIKeyCreatesUsableProviderInstance(t *testing.T) {
	configPath, cleanup := setupCredentialTestEnv(t)
	defer cleanup()
	resetCredentialHooks(t)
	h := NewHandler(configPath)
	h.providerCatalogSync = func(_ context.Context, input ProviderCatalogSyncInput) ([]CatalogModel, error) {
		if input.InstanceID != "openai" || input.Secret != "sk-test" {
			t.Fatalf("sync input = %#v", input)
		}
		return []CatalogModel{{ID: "gpt-test", OwnedBy: "openai"}}, nil
	}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	rec := credentialRequest(t, mux, http.MethodPost, "/api/credentials", `{"provider":"openai","token":"sk-test"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp) != 2 || resp["status"] != "ok" || resp["provider"] != "openai" {
		t.Fatalf("response = %#v", resp)
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.ProviderInstances) != 1 || cfg.ProviderInstances[0].ID != "openai" || cfg.ProviderInstances[0].Adapter != "openai-compatible" {
		t.Fatalf("instances = %#v", cfg.ProviderInstances)
	}
	if cfg.ProviderInstances[0].AuthConnectionRef != "credential:openai" {
		t.Fatalf("auth ref = %q", cfg.ProviderInstances[0].AuthConnectionRef)
	}
	cred, err := auth.GetCredential("openai")
	if err != nil || cred == nil || cred.AccessToken != "sk-test" || cred.AuthMethod != credentialMethodAPIKey {
		t.Fatalf("api credential=%#v err=%v", cred, err)
	}
	store, err := loadCatalogs()
	if err != nil || len(store.Entries["openai"].Models) != 1 {
		t.Fatalf("catalog=%#v err=%v", store, err)
	}
}

func TestAnthropicAPIKeyCreatesCompatibleInstanceFromDiscovery(t *testing.T) {
	configPath, cleanup := setupCredentialTestEnv(t)
	defer cleanup()
	resetCredentialHooks(t)
	h := NewHandler(configPath)
	h.providerCatalogSync = func(_ context.Context, input ProviderCatalogSyncInput) ([]CatalogModel, error) {
		if input.Adapter != "anthropic-compatible" || input.Secret != "anthropic-key" {
			t.Fatalf("sync input=%#v", input)
		}
		return []CatalogModel{{ID: "claude-discovered", OwnedBy: "anthropic"}}, nil
	}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	rec := credentialRequest(t, mux, http.MethodPost, "/api/credentials", `{"provider":"anthropic","token":"anthropic-key"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.ProviderInstances) != 1 || cfg.ProviderInstances[0].Adapter != "anthropic-compatible" {
		t.Fatalf("instances=%#v", cfg.ProviderInstances)
	}
	store, err := loadCatalogs()
	if err != nil || len(store.Entries["anthropic"].Models) != 1 || store.Entries["anthropic"].Models[0].ID != "claude-discovered" {
		t.Fatalf("catalog=%#v err=%v", store, err)
	}
}

func TestFailedProviderDiscoveryRestoresPreviousKey(t *testing.T) {
	configPath, cleanup := setupCredentialTestEnv(t)
	defer cleanup()
	resetCredentialHooks(t)
	h := NewHandler(configPath)
	h.providerCatalogSync = func(context.Context, ProviderCatalogSyncInput) ([]CatalogModel, error) {
		return nil, context.DeadlineExceeded
	}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	if rec := credentialRequest(t, mux, http.MethodPost, "/api/credentials", `{"provider":"openai","token":"bad-key"}`); rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if cred, err := auth.GetCredential("openai"); err != nil || cred != nil {
		t.Fatalf("credential=%#v err=%v", cred, err)
	}

	if err := auth.SetCredential("openai", &auth.AuthCredential{AccessToken: "sk-old", AuthMethod: credentialMethodAPIKey}); err != nil {
		t.Fatal(err)
	}
	if rec := credentialRequest(t, mux, http.MethodPost, "/api/credentials", `{"provider":"openai","token":"bad-key"}`); rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if cred, err := auth.GetCredential("openai"); err != nil || cred == nil || cred.AccessToken != "sk-old" {
		t.Fatalf("credential=%#v err=%v, want the previous key", cred, err)
	}
}

func TestDeleteCredentialClearsCredentialAndConfig(t *testing.T) {
	configPath, cleanup := setupCredentialTestEnv(t)
	defer cleanup()
	resetCredentialHooks(t)

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	cfg.ProviderInstances = append(cfg.ProviderInstances, &config.ProviderInstanceConfig{
		ID: "openai", ProviderKind: "openai", Adapter: config.ProviderAdapterOpenAICompatible, Protocol: "openai",
		AuthConnectionRef: "credential:openai", State: config.ProviderInstanceStateEnabled,
	})
	cfg.ActiveModels = []string{"openai/gpt-5.3"}
	cfg.Agents.Defaults.ModelName = "openai/gpt-5.3"
	if err = config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig error: %v", err)
	}
	if err = auth.SetCredential(credentialProviderOpenAI, &auth.AuthCredential{
		AccessToken: "key-before-delete",
		AuthMethod:  credentialMethodAPIKey,
	}); err != nil {
		t.Fatalf("SetCredential error: %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := credentialRequest(t, mux, http.MethodDelete, "/api/credentials/openai", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	cred, err := auth.GetCredential(credentialProviderOpenAI)
	if err != nil {
		t.Fatalf("GetCredential error: %v", err)
	}
	if cred != nil {
		t.Fatalf("expected credential deleted, got %#v", cred)
	}

	updated, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	if updated.ProviderInstances[len(updated.ProviderInstances)-1].State != config.ProviderInstanceStateDisabled {
		t.Fatalf("provider instance remained enabled: %#v", updated.ProviderInstances)
	}
	if len(updated.ActiveModels) != 0 {
		t.Fatalf("active models remained after delete: %#v", updated.ActiveModels)
	}
	if updated.Agents.Defaults.ModelName != "" {
		t.Fatalf("default selection %q names the disabled instance", updated.Agents.Defaults.ModelName)
	}
}

func TestDeleteCredentialRestoresKeyWhenConfigUpdateFails(t *testing.T) {
	configPath, cleanup := setupCredentialTestEnv(t)
	defer cleanup()
	resetCredentialHooks(t)
	if err := auth.SetCredential(credentialProviderAnthropic, &auth.AuthCredential{AccessToken: "kept-key"}); err != nil {
		t.Fatal(err)
	}
	saveConfigFile = func(string, *config.Config) error { return errors.New("disk full") }

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	if rec := credentialRequest(t, mux, http.MethodDelete, "/api/credentials/anthropic", ""); rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if cred, err := auth.GetCredential(credentialProviderAnthropic); err != nil || cred == nil || cred.AccessToken != "kept-key" {
		t.Fatalf("credential=%#v err=%v, want the key restored", cred, err)
	}
}

func setupCredentialTestEnv(t *testing.T) (string, func()) {
	t.Helper()

	tmp := t.TempDir()
	oldHome := os.Getenv("HOME")
	oldCompaHome := os.Getenv("COMPA_HOME")

	if err := os.Setenv("HOME", tmp); err != nil {
		t.Fatalf("set HOME: %v", err)
	}
	if err := os.Setenv("COMPA_HOME", filepath.Join(tmp, ".compa")); err != nil {
		t.Fatalf("set COMPA_HOME: %v", err)
	}

	cfg := config.DefaultConfig()

	configPath := filepath.Join(tmp, "config.json")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig error: %v", err)
	}

	cleanup := func() {
		_ = os.Setenv("HOME", oldHome)
		if oldCompaHome == "" {
			_ = os.Unsetenv("COMPA_HOME")
		} else {
			_ = os.Setenv("COMPA_HOME", oldCompaHome)
		}
	}
	return configPath, cleanup
}

func resetCredentialHooks(t *testing.T) {
	t.Helper()

	origGetCredential := getStoredCredential
	origSetCredential := setStoredCredential
	origDeleteCredential := deleteStoredCredential
	origDeleteCredentials := deleteStoredCredentials
	origLoadConfig := loadConfigFile
	origSaveConfig := saveConfigFile

	t.Cleanup(func() {
		getStoredCredential = origGetCredential
		setStoredCredential = origSetCredential
		deleteStoredCredential = origDeleteCredential
		deleteStoredCredentials = origDeleteCredentials
		loadConfigFile = origLoadConfig
		saveConfigFile = origSaveConfig
	})
}
