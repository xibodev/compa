package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/xibodev/compa/v4/pkg/config"
	"github.com/xibodev/compa/v4/pkg/modelservice"
)

func providerInstanceFixture(id, endpoint string) *config.ProviderInstanceConfig {
	return &config.ProviderInstanceConfig{
		ID:                id,
		ProviderKind:      "openai",
		Adapter:           "openai-compatible",
		Protocol:          "openai",
		Endpoint:          endpoint,
		AuthConnectionRef: "credential:" + id,
		Headers:           map[string]string{"X-Instance": id},
		Settings:          map[string]any{"tenant": id},
		State:             config.ProviderInstanceStateEnabled,
	}
}

func providerInstanceTestHandler(t *testing.T, instances ...*config.ProviderInstanceConfig) (*Handler, *http.ServeMux, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("COMPA_HOME", home)
	configPath := filepath.Join(home, "config.json")
	cfg := config.DefaultConfig()
	cfg.ProviderInstances = instances
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return h, mux, configPath
}

func providerInstanceRequest(t *testing.T, mux *http.ServeMux, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(recorder, request)
	return recorder
}

func providerInstanceAdminBody(t *testing.T, instance *config.ProviderInstanceConfig, includeSensitive bool) string {
	t.Helper()
	request := providerInstanceRequestBody{
		ID: instance.ID, ProviderKind: instance.ProviderKind, Adapter: instance.Adapter,
		Protocol: instance.Protocol, Endpoint: instance.Endpoint, State: instance.State,
	}
	if includeSensitive {
		authRef := instance.AuthConnectionRef
		headers := cloneStringValues(instance.Headers)
		settings := cloneAnyValues(instance.Settings)
		request.WriteOnly = &providerInstanceWriteOnly{
			AuthConnectionRef: &authRef, Headers: &headers, Settings: &settings,
		}
	}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	return string(body)
}

func TestActiveModelsAreCanonicalAndEmptyMeansNoChatTargets(t *testing.T) {
	first := providerInstanceFixture("openai-main", "https://one.example.test/v1")
	second := providerInstanceFixture("other", "https://two.example.test/v1")
	_, mux, configPath := providerInstanceTestHandler(t, first, second)
	if err := saveCatalogs(&CatalogStore{Entries: map[string]*CatalogEntry{
		"openai-main": {ID: "openai-main", InstanceID: "openai-main", Provider: "openai", Models: []CatalogModel{{ID: "gpt-test"}}},
		"other":       {ID: "other", InstanceID: "other", Provider: "openai", Models: []CatalogModel{{ID: "model"}}},
	}}); err != nil {
		t.Fatalf("saveCatalogs() error = %v", err)
	}

	recorder := providerInstanceRequest(
		t,
		mux,
		http.MethodPost,
		"/api/active-models",
		`{"models":[" openai-main/gpt-test ","openai-main/gpt-test","other/model"]}`,
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("set active models status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	want := []string{"openai-main/gpt-test", "other/model"}
	if len(cfg.ActiveModels) != len(want) {
		t.Fatalf("active models = %#v, want %#v", cfg.ActiveModels, want)
	}
	for i := range want {
		if cfg.ActiveModels[i] != want[i] {
			t.Fatalf("active models = %#v, want %#v", cfg.ActiveModels, want)
		}
	}

	recorder = providerInstanceRequest(t, mux, http.MethodPost, "/api/active-models", `{"models":[]}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("clear active models status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	recorder = providerInstanceRequest(t, mux, http.MethodGet, "/api/provider-targets", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("list targets status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Targets []providerTargetResponse `json:"targets"`
		Total   int                      `json:"total"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if response.Total != 0 || len(response.Targets) != 0 {
		t.Fatalf("empty shortlist returned targets: %#v", response)
	}
}

func TestActiveModelsRejectMalformedTarget(t *testing.T) {
	_, mux, _ := providerInstanceTestHandler(t)
	recorder := providerInstanceRequest(
		t,
		mux,
		http.MethodPost,
		"/api/active-models",
		`{"models":["not-an-exact-target"]}`,
	)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
}

func TestActiveModelsRejectUnavailableTarget(t *testing.T) {
	instance := providerInstanceFixture("openai-main", "https://one.example.test/v1")
	_, mux, _ := providerInstanceTestHandler(t, instance)
	if err := saveCatalogs(&CatalogStore{Entries: map[string]*CatalogEntry{
		"openai-main": {ID: "openai-main", InstanceID: "openai-main", Provider: "openai", Models: []CatalogModel{{ID: "available"}}},
	}}); err != nil {
		t.Fatalf("saveCatalogs() error = %v", err)
	}
	recorder := providerInstanceRequest(
		t,
		mux,
		http.MethodPost,
		"/api/active-models/add",
		`{"target":"openai-main/missing"}`,
	)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
}

func TestAddActiveModelRejectsBlankTarget(t *testing.T) {
	_, mux, _ := providerInstanceTestHandler(t)
	for _, target := range []string{"", "   "} {
		recorder := providerInstanceRequest(
			t,
			mux,
			http.MethodPost,
			"/api/active-models/add",
			`{"target":`+strconv.Quote(target)+`}`,
		)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("target %q status = %d, want %d; body = %s", target, recorder.Code, http.StatusBadRequest, recorder.Body.String())
		}
	}
}

func TestProviderInstanceCRUD(t *testing.T) {
	_, mux, configPath := providerInstanceTestHandler(t)
	created := providerInstanceFixture("openai-main", "https://one.example.test/v1")
	body := providerInstanceAdminBody(t, created, true)

	recorder := providerInstanceRequest(t, mux, http.MethodPost, "/api/provider-instances", string(body))
	if recorder.Code != http.StatusOK {
		t.Fatalf("create status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if len(cfg.ProviderInstances) != 1 || cfg.ProviderInstances[0].ID != "openai-main" {
		t.Fatalf("provider instances = %#v", cfg.ProviderInstances)
	}

	recorder = providerInstanceRequest(t, mux, http.MethodGet, "/api/provider-instances", "")
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"openai-main"`) {
		t.Fatalf("list status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	created.Endpoint = "https://updated.example.test/v1"
	created.State = config.ProviderInstanceStateDisabled
	body = providerInstanceAdminBody(t, created, false)
	recorder = providerInstanceRequest(t, mux, http.MethodPut, "/api/provider-instances/openai-main", string(body))
	if recorder.Code != http.StatusOK {
		t.Fatalf("update status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	cfg, err = config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() after update error = %v", err)
	}
	if cfg.ProviderInstances[0].Endpoint != created.Endpoint || cfg.ProviderInstances[0].State != config.ProviderInstanceStateDisabled {
		t.Fatalf("updated instance = %#v", cfg.ProviderInstances[0])
	}

	recorder = providerInstanceRequest(t, mux, http.MethodDelete, "/api/provider-instances/openai-main", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	cfg, err = config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() after delete error = %v", err)
	}
	if len(cfg.ProviderInstances) != 0 {
		t.Fatalf("provider instances after delete = %#v", cfg.ProviderInstances)
	}
}

func TestProviderInstanceCRUDRejectsIdentityChangesAndDuplicates(t *testing.T) {
	_, mux, _ := providerInstanceTestHandler(t, providerInstanceFixture("existing", "https://existing.example.test/v1"))
	duplicate := providerInstanceAdminBody(t, providerInstanceFixture("existing", "https://other.example.test/v1"), true)
	recorder := providerInstanceRequest(t, mux, http.MethodPost, "/api/provider-instances", duplicate)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("duplicate status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	renamed := providerInstanceAdminBody(t, providerInstanceFixture("renamed", "https://other.example.test/v1"), false)
	recorder = providerInstanceRequest(t, mux, http.MethodPut, "/api/provider-instances/existing", renamed)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "immutable") {
		t.Fatalf("rename status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestProviderInstanceCatalogSyncUsesPersistedInstanceOwnership(t *testing.T) {
	first := providerInstanceFixture("first", "https://first.example.test/v1")
	second := providerInstanceFixture("second", "https://second.example.test/v1")
	h, mux, _ := providerInstanceTestHandler(t, first, second)
	var inputs []ProviderCatalogSyncInput
	h.providerCredentialResolver = func(ref string) (string, error) {
		return "fixture-secret", nil
	}
	h.providerCatalogSync = func(_ context.Context, input ProviderCatalogSyncInput) ([]CatalogModel, error) {
		inputs = append(inputs, input)
		return []CatalogModel{{ID: input.InstanceID + "-model"}}, nil
	}

	for _, id := range []string{"first", "second"} {
		recorder := providerInstanceRequest(t, mux, http.MethodPost, "/api/provider-instances/"+id+"/catalog/sync", `{}`)
		if recorder.Code != http.StatusOK {
			t.Fatalf("sync %s status = %d, body = %s", id, recorder.Code, recorder.Body.String())
		}
	}
	if len(inputs) != 2 {
		t.Fatalf("sync inputs = %#v", inputs)
	}
	if inputs[0].Endpoint != first.Endpoint || inputs[0].AuthConnectionRef != first.AuthConnectionRef || inputs[0].Headers["X-Instance"] != "first" || inputs[0].Settings["tenant"] != "first" {
		t.Fatalf("first sync input = %#v", inputs[0])
	}
	if inputs[1].Endpoint != second.Endpoint || inputs[1].AuthConnectionRef != second.AuthConnectionRef {
		t.Fatalf("second sync input = %#v", inputs[1])
	}

	store, err := loadCatalogs()
	if err != nil {
		t.Fatalf("loadCatalogs() error = %v", err)
	}
	if len(store.Entries) != 2 || store.Entries["first"].Models[0].ID != "first-model" || store.Entries["second"].Models[0].ID != "second-model" {
		t.Fatalf("instance-owned catalogs = %#v", store.Entries)
	}
}

func TestProviderInstanceCatalogSyncRejectsBrowserOverrides(t *testing.T) {
	h, mux, _ := providerInstanceTestHandler(t, providerInstanceFixture("owned", "https://owned.example.test/v1"))
	called := false
	h.providerCatalogSync = func(context.Context, ProviderCatalogSyncInput) ([]CatalogModel, error) {
		called = true
		return nil, nil
	}

	for _, body := range []string{
		`{"endpoint":"http://localhost:11434/v1"}`,
		`{"api_key":"browser-secret"}`,
		`{"provider":"ollama"}`,
	} {
		recorder := providerInstanceRequest(t, mux, http.MethodPost, "/api/provider-instances/owned/catalog/sync", body)
		if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "unknown field") {
			t.Fatalf("override %s status = %d, body = %s", body, recorder.Code, recorder.Body.String())
		}
	}
	if called {
		t.Fatal("catalog sync driver called for browser-provided overrides")
	}
}

func TestProviderInstanceCatalogSyncAllowsAnonymousCompatibleInstance(t *testing.T) {
	instance := providerInstanceFixture("anonymous", "https://anonymous.example.test/v1")
	instance.AuthConnectionRef = ""
	h, mux, _ := providerInstanceTestHandler(t, instance)
	h.providerCatalogSync = func(_ context.Context, input ProviderCatalogSyncInput) ([]CatalogModel, error) {
		if input.Secret != "" {
			t.Fatalf("anonymous sync secret = %q, want empty", input.Secret)
		}
		return []CatalogModel{{ID: "anonymous-model"}}, nil
	}

	recorder := providerInstanceRequest(t, mux, http.MethodPost, "/api/provider-instances/anonymous/catalog/sync", `{}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestProviderInstanceCatalogSyncRefusesDisabledAndEndpointlessInstances(t *testing.T) {
	disabled := providerInstanceFixture("disabled", "https://disabled.example.test/v1")
	disabled.State = config.ProviderInstanceStateDisabled
	missingEndpoint := providerInstanceFixture("missing-endpoint", "")
	h, mux, _ := providerInstanceTestHandler(t, disabled, missingEndpoint)
	called := false
	h.providerCatalogSync = func(context.Context, ProviderCatalogSyncInput) ([]CatalogModel, error) {
		called = true
		return nil, nil
	}

	tests := []struct {
		id       string
		status   int
		contains string
	}{
		{id: "disabled", status: http.StatusConflict, contains: "disabled"},
		{id: "missing-endpoint", status: http.StatusBadRequest, contains: "endpoint is required"},
	}
	for _, tc := range tests {
		recorder := providerInstanceRequest(t, mux, http.MethodPost, "/api/provider-instances/"+tc.id+"/catalog/sync", `{}`)
		if recorder.Code != tc.status || !strings.Contains(recorder.Body.String(), tc.contains) {
			t.Fatalf("sync %s status = %d, body = %s", tc.id, recorder.Code, recorder.Body.String())
		}
	}
	if called {
		t.Fatal("catalog sync driver called for a disabled or endpoint-less instance")
	}
}

func TestProviderCatalogSyncUsesResolvedCredentialAndPersistedConnection(t *testing.T) {
	tests := []struct {
		name             string
		kind, adapter    string
		base             string
		response         string
		wantPath         string
		wantAuthHeader   string
		wantAPIKeyHeader string
		wantModel        CatalogModel
	}{
		{
			name: "openai envelope", kind: "openai", adapter: "openai-compatible", base: "/base",
			response:       `{"data":[{"id":"gpt-fixture","owned_by":"fixture"}]}`,
			wantPath:       "/base/models",
			wantAuthHeader: "Bearer resolved-secret",
			wantModel:      CatalogModel{ID: "gpt-fixture", OwnedBy: "fixture"},
		},
		{
			name: "anthropic catalog", kind: "anthropic", adapter: "anthropic-compatible", base: "/v1",
			response:         `{"data":[{"id":"claude-fixture","type":"model","display_name":"Claude Fixture"}],"has_more":false}`,
			wantPath:         "/v1/models",
			wantAPIKeyHeader: "resolved-secret",
			wantModel:        CatalogModel{ID: "claude-fixture", OwnedBy: "anthropic", DisplayName: "Claude Fixture"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var requests int
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				requests++
				if request.URL.Path != tc.wantPath {
					t.Errorf("request path = %q", request.URL.Path)
				}
				if request.Header.Get("X-Persisted") != "yes" {
					t.Errorf("persisted header = %q", request.Header.Get("X-Persisted"))
				}
				if request.Header.Get("Authorization") != tc.wantAuthHeader {
					t.Errorf("Authorization = %q", request.Header.Get("Authorization"))
				}
				if request.Header.Get("X-Api-Key") != tc.wantAPIKeyHeader {
					t.Errorf("X-Api-Key = %q", request.Header.Get("X-Api-Key"))
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.response)
			}))
			defer upstream.Close()
			instance := providerInstanceFixture("owned", upstream.URL+tc.base)
			instance.ProviderKind = tc.kind
			instance.Adapter = tc.adapter
			instance.Headers = map[string]string{"X-Persisted": "yes"}
			h, mux, _ := providerInstanceTestHandler(t, instance)
			h.providerCredentialResolver = func(ref string) (string, error) {
				if ref != "credential:owned" {
					t.Fatalf("credential reference = %q", ref)
				}
				return "resolved-secret", nil
			}

			recorder := providerInstanceRequest(t, mux, http.MethodPost, "/api/provider-instances/owned/catalog/sync", `{}`)
			if recorder.Code != http.StatusOK {
				t.Fatalf("sync status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			if requests != 1 {
				t.Fatalf("requests = %d, want 1", requests)
			}
			store, err := loadCatalogs()
			if err != nil {
				t.Fatalf("loadCatalogs() error = %v", err)
			}
			entry := store.Entries["owned"]
			if entry == nil || entry.InstanceID != "owned" || len(entry.Models) != 1 {
				t.Fatalf("catalog entry = %#v", entry)
			}
			got := entry.Models[0]
			if got.ID != tc.wantModel.ID || got.OwnedBy != tc.wantModel.OwnedBy || got.DisplayName != tc.wantModel.DisplayName {
				t.Fatalf("catalog model = %#v, want %#v", got, tc.wantModel)
			}
		})
	}
}

func TestProviderInstanceCatalogSyncResolverFailureMakesNoRequest(t *testing.T) {
	requests := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer upstream.Close()
	h, mux, _ := providerInstanceTestHandler(t, providerInstanceFixture("owned", upstream.URL+"/v1"))
	h.providerCredentialResolver = func(ref string) (string, error) {
		if ref != "credential:owned" {
			t.Fatalf("credential reference = %q", ref)
		}
		return "", errors.New("fixture resolver unavailable")
	}

	recorder := providerInstanceRequest(t, mux, http.MethodPost, "/api/provider-instances/owned/catalog/sync", `{}`)
	if recorder.Code != http.StatusBadGateway || !strings.Contains(recorder.Body.String(), "fixture resolver unavailable") {
		t.Fatalf("sync status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want 0", requests)
	}
}

func TestProviderInstanceCatalogSyncSameKindHTTPIsolation(t *testing.T) {
	upstream := func(id string) *httptest.Server {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			if request.Header.Get("Authorization") != "Bearer "+id+"-secret" {
				t.Errorf("%s Authorization = %q", id, request.Header.Get("Authorization"))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"id":"`+id+`-model"}]}`)
		}))
		t.Cleanup(server.Close)
		return server
	}
	first := providerInstanceFixture("first", upstream("first").URL+"/v1")
	second := providerInstanceFixture("second", upstream("second").URL+"/v1")
	h, mux, _ := providerInstanceTestHandler(t, first, second)
	h.providerCredentialResolver = func(ref string) (string, error) {
		return strings.TrimPrefix(ref, "credential:") + "-secret", nil
	}

	for _, id := range []string{"first", "second"} {
		recorder := providerInstanceRequest(t, mux, http.MethodPost, "/api/provider-instances/"+id+"/catalog/sync", `{}`)
		if recorder.Code != http.StatusOK {
			t.Fatalf("sync %s status = %d, body = %s", id, recorder.Code, recorder.Body.String())
		}
	}
	store, err := loadCatalogs()
	if err != nil {
		t.Fatalf("loadCatalogs() error = %v", err)
	}
	if store.Entries["first"].Models[0].ID != "first-model" || store.Entries["second"].Models[0].ID != "second-model" {
		t.Fatalf("catalog entries = %#v", store.Entries)
	}
}
func TestProviderInstanceListRedactsSensitiveConfigurationAndSorts(t *testing.T) {
	second := providerInstanceFixture("second", "https://second.example.test/v1")
	second.Headers = map[string]string{"X-Zeta": "header-secret", "Authorization": "secret-token"}
	second.Settings = map[string]any{"zeta": "setting-secret", "api_key": "secret-key"}
	first := providerInstanceFixture("first", "https://first.example.test/v1")
	_, mux, _ := providerInstanceTestHandler(t, second, first)

	recorder := providerInstanceRequest(t, mux, http.MethodGet, "/api/provider-instances", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, secret := range []string{"credential:first", "credential:second", "header-secret", "secret-token", "setting-secret", "secret-key"} {
		if strings.Contains(body, secret) {
			t.Fatalf("response exposed %q: %s", secret, body)
		}
	}
	var response struct {
		Instances []providerInstanceResponse `json:"instances"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(response.Instances) != 2 || response.Instances[0].ID != "first" || response.Instances[1].ID != "second" {
		t.Fatalf("instances = %#v", response.Instances)
	}
	if !response.Instances[1].AuthConfigured || strings.Join(response.Instances[1].HeaderNames, ",") != "Authorization,X-Zeta" || strings.Join(response.Instances[1].SettingNames, ",") != "api_key,zeta" {
		t.Fatalf("safe instance DTO = %#v", response.Instances[1])
	}
}

func TestProviderInstanceCatalogAndTargetProjectionsExcludeOrphanDisabledAndInvalid(t *testing.T) {
	first := providerInstanceFixture("first", "https://first.example.test/v1")
	second := providerInstanceFixture("second", "https://second.example.test/v1")
	disabled := providerInstanceFixture("disabled", "https://disabled.example.test/v1")
	disabled.State = config.ProviderInstanceStateDisabled
	_, mux, _ := providerInstanceTestHandler(t, second, disabled, first)
	store := &CatalogStore{Entries: map[string]*CatalogEntry{
		"first":    {ID: "first", InstanceID: "first", Provider: "openai", Models: []CatalogModel{{ID: "shared"}, {ID: "alpha"}}, FetchedAt: "2026-01-01T00:00:00Z"},
		"second":   {ID: "second", InstanceID: "second", Provider: "openai", Models: []CatalogModel{{ID: "shared"}}, FetchedAt: "2026-01-02T00:00:00Z"},
		"disabled": {ID: "disabled", InstanceID: "disabled", Provider: "openai", Models: []CatalogModel{{ID: "hidden"}}},
		"orphan":   {ID: "orphan", InstanceID: "orphan", Provider: "openai", Models: []CatalogModel{{ID: "orphan-model"}}},
		"wrong":    {ID: "wrong", InstanceID: "other", Provider: "openai", Models: []CatalogModel{{ID: "wrong-model"}}},
	}}
	if err := saveCatalogs(store); err != nil {
		t.Fatalf("saveCatalogs() error = %v", err)
	}

	catalogRecorder := providerInstanceRequest(t, mux, http.MethodGet, "/api/provider-instances/catalogs", "")
	if catalogRecorder.Code != http.StatusOK {
		t.Fatalf("catalog status = %d, body = %s", catalogRecorder.Code, catalogRecorder.Body.String())
	}
	var catalogs struct {
		Catalogs []providerInstanceCatalogResponse `json:"catalogs"`
	}
	if err := json.Unmarshal(catalogRecorder.Body.Bytes(), &catalogs); err != nil {
		t.Fatalf("catalog Unmarshal() error = %v", err)
	}
	if len(catalogs.Catalogs) != 3 || catalogs.Catalogs[0].InstanceID != "disabled" || catalogs.Catalogs[1].InstanceID != "first" || catalogs.Catalogs[2].InstanceID != "second" {
		t.Fatalf("catalogs = %#v", catalogs.Catalogs)
	}
	if catalogs.Catalogs[1].Models[0].ID != "alpha" {
		t.Fatalf("catalog models not sorted: %#v", catalogs.Catalogs[1].Models)
	}

	targetRecorder := providerInstanceRequest(t, mux, http.MethodGet, "/api/provider-targets?all=true", "")
	var targets struct {
		Targets []providerTargetResponse `json:"targets"`
	}
	if err := json.Unmarshal(targetRecorder.Body.Bytes(), &targets); err != nil {
		t.Fatalf("target Unmarshal() error = %v", err)
	}
	want := []string{"first/alpha", "first/shared", "second/shared"}
	if len(targets.Targets) != len(want) {
		t.Fatalf("targets = %#v", targets.Targets)
	}
	for i := range want {
		if targets.Targets[i].Target != want[i] {
			t.Fatalf("targets[%d] = %q, want %q", i, targets.Targets[i].Target, want[i])
		}
	}
}

func TestProviderRouteCRUDPreservesOrderAndValidatesAuthoritativeTargets(t *testing.T) {
	first := providerInstanceFixture("first", "https://first.example.test/v1")
	second := providerInstanceFixture("second", "https://second.example.test/v1")
	_, mux, configPath := providerInstanceTestHandler(t, first, second)
	if err := saveCatalogs(&CatalogStore{Entries: map[string]*CatalogEntry{
		"first":  {ID: "first", InstanceID: "first", Provider: "openai", Models: []CatalogModel{{ID: "one"}}},
		"second": {ID: "second", InstanceID: "second", Provider: "openai", Models: []CatalogModel{{ID: "two"}}},
	}}); err != nil {
		t.Fatalf("saveCatalogs() error = %v", err)
	}

	recorder := providerInstanceRequest(t, mux, http.MethodPost, "/api/model-routes", `{"name":"route-b","targets":["second/two","first/one"]}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("create status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if strings.Join(cfg.ModelRoutes[0].Targets, ",") != "second/two,first/one" {
		t.Fatalf("route order = %#v", cfg.ModelRoutes[0].Targets)
	}

	recorder = providerInstanceRequest(t, mux, http.MethodPost, "/api/model-routes", `{"name":"route-a","targets":["first/missing"]}`)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "enabled instance-owned catalog") {
		t.Fatalf("invalid target status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	recorder = providerInstanceRequest(t, mux, http.MethodPut, "/api/model-routes/route-b", `{"name":"renamed","targets":["first/one"]}`)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "immutable") {
		t.Fatalf("rename status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	recorder = providerInstanceRequest(t, mux, http.MethodPut, "/api/model-routes/route-b", `{"name":"route-b","targets":["first/one","second/two"]}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("update status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	recorder = providerInstanceRequest(t, mux, http.MethodGet, "/api/model-routes", "")
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"targets":["first/one","second/two"]`) {
		t.Fatalf("list status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	recorder = providerInstanceRequest(t, mux, http.MethodDelete, "/api/model-routes/route-b", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("delete route status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestProviderInstanceMutationConflictsWithRouteReferences(t *testing.T) {
	instance := providerInstanceFixture("owned", "https://owned.example.test/v1")
	_, mux, configPath := providerInstanceTestHandler(t, instance)
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelRoutes = []*config.ModelRouteConfig{{Name: "used-route", Targets: []string{"owned/model"}}}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	recorder := providerInstanceRequest(t, mux, http.MethodDelete, "/api/provider-instances/owned", "")
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "used-route") {
		t.Fatalf("delete status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	instance.State = config.ProviderInstanceStateDisabled
	body := providerInstanceAdminBody(t, instance, false)
	recorder = providerInstanceRequest(t, mux, http.MethodPut, "/api/provider-instances/owned", body)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "used-route") {
		t.Fatalf("disable status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestProviderInstanceAdminWriteOnlyCreatePreserveAndClear(t *testing.T) {
	_, mux, configPath := providerInstanceTestHandler(t)
	instance := providerInstanceFixture("secure", "https://secure.example.test/v1")
	createBody := providerInstanceAdminBody(t, instance, true)
	recorder := providerInstanceRequest(t, mux, http.MethodPost, "/api/provider-instances", createBody)
	if recorder.Code != http.StatusOK {
		t.Fatalf("create status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	for _, secret := range []string{instance.AuthConnectionRef, "secure", instance.Headers["X-Instance"]} {
		if secret != "secure" && strings.Contains(recorder.Body.String(), secret) {
			t.Fatalf("create response exposed %q: %s", secret, recorder.Body.String())
		}
	}
	if strings.Contains(recorder.Body.String(), "auth_connection_ref") || strings.Contains(recorder.Body.String(), `"headers"`) || strings.Contains(recorder.Body.String(), `"settings"`) {
		t.Fatalf("create response exposed write-only fields: %s", recorder.Body.String())
	}

	instance.Endpoint = "https://changed.example.test/v1"
	recorder = providerInstanceRequest(t, mux, http.MethodPut, "/api/provider-instances/secure", providerInstanceAdminBody(t, instance, false))
	if recorder.Code != http.StatusOK {
		t.Fatalf("preserve update status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	stored := cfg.ProviderInstances[0]
	if stored.AuthConnectionRef != "credential:secure" || stored.Headers["X-Instance"] != "secure" || stored.Settings["tenant"] != "secure" {
		t.Fatalf("omitted sensitive values were not preserved: %#v", stored)
	}

	clearBody := providerInstanceRequestBody{
		ID: "secure", ProviderKind: "openai", Adapter: "openai-compatible", Protocol: "openai",
		Endpoint: instance.Endpoint, State: config.ProviderInstanceStateEnabled,
		ClearAuth: true, ClearHeaders: true, ClearSettings: true,
	}
	data, _ := json.Marshal(clearBody)
	recorder = providerInstanceRequest(t, mux, http.MethodPut, "/api/provider-instances/secure", string(data))
	if recorder.Code != http.StatusOK {
		t.Fatalf("clear update status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	cfg, err = config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() after clear error = %v", err)
	}
	stored = cfg.ProviderInstances[0]
	if stored.AuthConnectionRef != "" || stored.Headers != nil || stored.Settings != nil {
		t.Fatalf("sensitive values not cleared: %#v", stored)
	}
	if strings.Contains(recorder.Body.String(), "credential:secure") || strings.Contains(recorder.Body.String(), "X-Instance\":\"secure") {
		t.Fatalf("clear response exposed secret data: %s", recorder.Body.String())
	}
}

func TestProviderInstanceAdminRejectsCredentialBearingEndpoint(t *testing.T) {
	_, mux, _ := providerInstanceTestHandler(t)
	for _, endpoint := range []string{
		"https://user:secret@example.test/v1",
		"https://example.test/v1?api_key=secret",
		"example.test/v1",
	} {
		instance := providerInstanceFixture("unsafe", endpoint)
		recorder := providerInstanceRequest(t, mux, http.MethodPost, "/api/provider-instances", providerInstanceAdminBody(t, instance, true))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("endpoint %q status = %d, body = %s", endpoint, recorder.Code, recorder.Body.String())
		}
		if strings.Contains(recorder.Body.String(), "secret") {
			t.Fatalf("endpoint error echoed credential: %s", recorder.Body.String())
		}
	}
}

func TestProviderRosterSeparatesCompatibleAndDiscoveryOnlyWithoutModels(t *testing.T) {
	_, mux, _ := providerInstanceTestHandler(t)
	recorder := providerInstanceRequest(t, mux, http.MethodGet, "/api/provider-roster", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if strings.Contains(body, "common_models") || strings.Contains(body, "gpt-5.4-mini") {
		t.Fatalf("roster exposed executable template models: %s", body)
	}
	var response struct {
		Providers []modelservice.ProviderRosterItem `json:"providers"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	entries := make(map[string]modelservice.ProviderRosterItem, len(response.Providers))
	for index, entry := range response.Providers {
		entries[entry.ID] = entry
		if index > 0 && response.Providers[index-1].ID > entry.ID {
			t.Fatalf("roster not sorted: %#v", response.Providers)
		}
	}
	if entries["openai"].Compatibility != "compatible" || entries["openai"].Adapter != "openai-compatible" {
		t.Fatalf("openai roster entry = %#v", entries["openai"])
	}
	for id, entry := range entries {
		if entry.Compatibility == "native" {
			t.Fatalf("roster exposed native-adapter provider %q: %#v", id, entry)
		}
	}
}

func TestProviderInstancePingAndAutoConnectFree(t *testing.T) {
	h, mux, _ := providerInstanceTestHandler(t)
	h.providerAnonymousVerify = func(context.Context, *config.Config) []modelservice.AnonymousProviderOutcome {
		return []modelservice.AnonymousProviderOutcome{{
			RegistryID: "pollinations", ProviderID: "pollinations", Status: "connected",
			Models: []string{"openai-fast"}, ProbeModel: "openai-fast",
			ErrorClass: "rate_limited", Error: "status 429",
		}}
	}
	h.providerCatalogSync = func(_ context.Context, input ProviderCatalogSyncInput) ([]CatalogModel, error) {
		if input.InstanceID == "test-reachable" {
			return []CatalogModel{{ID: "m1"}, {ID: "m2"}}, nil
		}
		return nil, errors.New("upstream unreachable")
	}

	// 1. Auto connect free providers
	rec := providerInstanceRequest(t, mux, http.MethodPost, "/api/provider-instances/auto-connect-free", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("auto-connect status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var autoRes struct {
		OK                bool `json:"ok"`
		Total             int  `json:"total"`
		CatalogDiscovered int  `json:"catalog_discovered"`
		Verified          int  `json:"verified"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &autoRes); err != nil {
		t.Fatalf("Unmarshal auto-connect error = %v", err)
	}
	if autoRes.OK || autoRes.Total == 0 || autoRes.CatalogDiscovered != 1 || autoRes.Verified != 0 {
		t.Fatalf("unexpected auto-connect result: %#v", autoRes)
	}

	// 2. Ping reachable instance
	inst := providerInstanceFixture("test-reachable", "https://api.test/v1")
	createRec := providerInstanceRequest(t, mux, http.MethodPost, "/api/provider-instances", providerInstanceAdminBody(t, inst, true))
	if createRec.Code != http.StatusOK {
		t.Fatalf("create status = %d, body = %s", createRec.Code, createRec.Body.String())
	}

	pingRec := providerInstanceRequest(t, mux, http.MethodPost, "/api/provider-instances/test-reachable/ping", "")
	if pingRec.Code != http.StatusOK {
		t.Fatalf("ping status = %d, body = %s", pingRec.Code, pingRec.Body.String())
	}
	var pingRes struct {
		OK         bool   `json:"ok"`
		InstanceID string `json:"instance_id"`
		ModelCount int    `json:"model_count"`
		Status     string `json:"status"`
	}
	if err := json.Unmarshal(pingRec.Body.Bytes(), &pingRes); err != nil {
		t.Fatalf("Unmarshal ping error = %v", err)
	}
	if !pingRes.OK || pingRes.Status != "reachable" || pingRes.ModelCount != 2 {
		t.Fatalf("unexpected ping result: %#v", pingRes)
	}
}

const tunedInstanceBody = `{"id":"tuned","provider_kind":"openai","adapter":"openai-compatible","protocol":"openai",` +
	`"endpoint":"https://tuned.example.test/v1","state":"enabled"`

func decodeProviderInstanceResponse(t *testing.T, recorder *httptest.ResponseRecorder) (providerInstanceResponse, map[string]any) {
	t.Helper()
	var typed struct {
		Instance providerInstanceResponse `json:"instance"`
	}
	var raw struct {
		Instance map[string]any `json:"instance"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &typed); err != nil {
		t.Fatalf("Unmarshal(%s) error = %v", recorder.Body.String(), err)
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &raw); err != nil {
		t.Fatalf("Unmarshal(%s) error = %v", recorder.Body.String(), err)
	}
	return typed.Instance, raw.Instance
}

func TestProviderInstanceRuntimeReadWrite(t *testing.T) {
	_, mux, configPath := providerInstanceTestHandler(t)

	create := tunedInstanceBody + `,"runtime":{"proxy":" http://proxy.example.test:8080 ","request_timeout":90,"rpm":30,` +
		`"streaming":true,"thinking_level":" High ","max_tokens_field":"max_completion_tokens",` +
		`"tool_schema_transform":"simple","extra_body":{"store":false}}}`
	recorder := providerInstanceRequest(t, mux, http.MethodPost, "/api/provider-instances", create)
	if recorder.Code != http.StatusOK {
		t.Fatalf("create status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	created, _ := decodeProviderInstanceResponse(t, recorder)
	if created.Runtime == nil || created.Runtime.RequestTimeout != 90 || created.Runtime.Proxy != "http://proxy.example.test:8080" || created.Runtime.ThinkingLevel != "high" {
		t.Fatalf("created runtime = %#v", created.Runtime)
	}
	stored := loadSavedConfig(t, configPath).ProviderInstances[0].Runtime
	if stored == nil || stored.Proxy != "http://proxy.example.test:8080" || stored.RequestTimeout != 90 || stored.RPM != 30 ||
		stored.Streaming == nil || !*stored.Streaming || stored.ThinkingLevel != "high" || stored.MaxTokensField != "max_completion_tokens" ||
		stored.ToolSchemaTransform != "simple" || stored.ExtraBody["store"] != false {
		t.Fatalf("stored runtime = %#v", stored)
	}

	recorder = providerInstanceRequest(t, mux, http.MethodGet, "/api/provider-instances", "")
	var listed struct {
		Instances []providerInstanceResponse `json:"instances"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &listed); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(listed.Instances) != 1 || listed.Instances[0].Runtime == nil || listed.Instances[0].Runtime.RPM != 30 {
		t.Fatalf("listed instances = %#v", listed.Instances)
	}

	// Runtime settings change how requests go, not which models the
	// instance serves: its catalog and the selections naming it stay.
	if err := saveCatalogs(&CatalogStore{Entries: map[string]*CatalogEntry{
		"tuned": {ID: "tuned", InstanceID: "tuned", Provider: "openai", Models: []CatalogModel{{ID: "gpt-tuned"}}},
	}}); err != nil {
		t.Fatalf("saveCatalogs() error = %v", err)
	}
	editSavedConfig(t, configPath, func(cfg *config.Config) {
		cfg.ActiveModels = []string{"tuned/gpt-tuned"}
		cfg.Agents.Defaults.ModelName = "tuned/gpt-tuned"
	})

	recorder = providerInstanceRequest(t, mux, http.MethodPut, "/api/provider-instances/tuned", tunedInstanceBody+`}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("update without runtime status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if kept := loadSavedConfig(t, configPath).ProviderInstances[0].Runtime; kept == nil || kept.RequestTimeout != 90 || kept.RPM != 30 {
		t.Fatalf("an update without runtime dropped it: %#v", kept)
	}

	recorder = providerInstanceRequest(t, mux, http.MethodPut, "/api/provider-instances/tuned", tunedInstanceBody+`,"runtime":{"request_timeout":15}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("runtime update status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	cfg := loadSavedConfig(t, configPath)
	if runtime := cfg.ProviderInstances[0].Runtime; runtime == nil || runtime.RequestTimeout != 15 || runtime.RPM != 0 || runtime.Proxy != "" || runtime.Streaming != nil {
		t.Fatalf("runtime after replacing it = %#v", runtime)
	}
	if strings.Join(cfg.ActiveModels, ",") != "tuned/gpt-tuned" || cfg.Agents.Defaults.ModelName != "tuned/gpt-tuned" {
		t.Fatalf("a runtime change dropped targets: active %#v, default %q", cfg.ActiveModels, cfg.Agents.Defaults.ModelName)
	}
	if store, err := loadCatalogs(); err != nil || store.Entries["tuned"] == nil {
		t.Fatalf("a runtime change invalidated the catalog: %#v, %v", store, err)
	}

	recorder = providerInstanceRequest(t, mux, http.MethodPut, "/api/provider-instances/tuned", tunedInstanceBody+`,"runtime":{}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("runtime reset status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if _, raw := decodeProviderInstanceResponse(t, recorder); raw["runtime"] != nil {
		t.Fatalf("reset instance response carries runtime %#v", raw["runtime"])
	}
	if runtime := loadSavedConfig(t, configPath).ProviderInstances[0].Runtime; runtime != nil {
		t.Fatalf("runtime {} kept settings: %#v", runtime)
	}

	for _, runtime := range []string{
		`{"thinking_level":"extreme"}`,
		`{"rpm":-1}`,
		`{"request_timeout":-5}`,
		`{"proxy":"ftp://proxy.example.test"}`,
		`{"tool_schema_transform":"fancy"}`,
		`{"max_tokens_field":"max tokens"}`,
		`{"extra_body":{" ":1}}`,
		`{"unknown_setting":1}`,
	} {
		recorder = providerInstanceRequest(t, mux, http.MethodPut, "/api/provider-instances/tuned", tunedInstanceBody+`,"runtime":`+runtime+`}`)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("runtime %s status = %d, body = %s, want 400", runtime, recorder.Code, recorder.Body.String())
		}
	}
	if runtime := loadSavedConfig(t, configPath).ProviderInstances[0].Runtime; runtime != nil {
		t.Fatalf("a refused runtime was saved: %#v", runtime)
	}
}

func TestProviderInstanceDeleteDropsItsTargetsFromEverySelection(t *testing.T) {
	_, mux, configPath := providerInstanceTestHandler(t,
		providerInstanceFixture("owned", "https://owned.example.test/v1"),
		providerInstanceFixture("other", "https://other.example.test/v1"),
	)
	editSavedConfig(t, configPath, func(cfg *config.Config) {
		cfg.ActiveModels = []string{"owned/chat", "other/chat"}
		cfg.Agents.Defaults.ModelName = "owned/chat"
		cfg.Agents.Defaults.ImageModel = "owned/vision"
		cfg.Agents.Defaults.Routing = &config.RoutingConfig{Enabled: true, LightModel: "owned/mini", Threshold: 0.5}
		cfg.Agents.List = []config.AgentConfig{{ID: "helper", Model: "owned/chat"}, {ID: "writer", Model: "other/chat"}}
	})

	recorder := providerInstanceRequest(t, mux, http.MethodDelete, "/api/provider-instances/owned", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	cfg := loadSavedConfig(t, configPath)
	defaults := cfg.Agents.Defaults
	if strings.Join(cfg.ActiveModels, ",") != "other/chat" || defaults.ModelName != "" || defaults.ImageModel != "" || defaults.Routing.LightModel != "" {
		t.Fatalf("selections after delete: active %#v, default %q, image %q, light %q", cfg.ActiveModels, defaults.ModelName, defaults.ImageModel, defaults.Routing.LightModel)
	}
	if cfg.Agents.List[0].Model != "" || cfg.Agents.List[1].Model != "other/chat" {
		t.Fatalf("agent models after delete = %#v", cfg.Agents.List)
	}
}

func TestProviderInstanceDisableDropsItsTargetsFromSelections(t *testing.T) {
	instance := providerInstanceFixture("owned", "https://owned.example.test/v1")
	_, mux, configPath := providerInstanceTestHandler(t, instance)
	editSavedConfig(t, configPath, func(cfg *config.Config) {
		cfg.ActiveModels = []string{"owned/chat"}
		cfg.Agents.Defaults.ModelName = "owned/chat"
		cfg.Agents.Defaults.ImageModel = "owned/vision"
	})

	instance.State = config.ProviderInstanceStateDisabled
	recorder := providerInstanceRequest(t, mux, http.MethodPut, "/api/provider-instances/owned", providerInstanceAdminBody(t, instance, false))
	if recorder.Code != http.StatusOK {
		t.Fatalf("disable status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	cfg := loadSavedConfig(t, configPath)
	if len(cfg.ActiveModels) != 0 || cfg.Agents.Defaults.ModelName != "" || cfg.Agents.Defaults.ImageModel != "" {
		t.Fatalf("selections after disabling: active %#v, default %q, image %q", cfg.ActiveModels, cfg.Agents.Defaults.ModelName, cfg.Agents.Defaults.ImageModel)
	}
}

func TestProviderInstanceEndpointChangeDropsTargetsAndEmptiedRoutes(t *testing.T) {
	owned := providerInstanceFixture("owned", "https://owned.example.test/v1")
	_, mux, configPath := providerInstanceTestHandler(t, owned, providerInstanceFixture("other", "https://other.example.test/v1"))
	if err := saveCatalogs(&CatalogStore{Entries: map[string]*CatalogEntry{
		"owned": {ID: "owned", InstanceID: "owned", Provider: "openai", Models: []CatalogModel{{ID: "chat"}}},
		"other": {ID: "other", InstanceID: "other", Provider: "openai", Models: []CatalogModel{{ID: "chat"}}},
	}}); err != nil {
		t.Fatalf("saveCatalogs() error = %v", err)
	}
	editSavedConfig(t, configPath, func(cfg *config.Config) {
		cfg.ModelRoutes = []*config.ModelRouteConfig{
			{Name: "mixed", Targets: []string{"owned/chat", "other/chat"}},
			{Name: "solo", Targets: []string{"owned/chat"}},
		}
		cfg.Agents.Defaults.ModelName = "solo"
		cfg.Agents.Defaults.ImageModel = "mixed"
	})

	owned.Endpoint = "https://moved.example.test/v1"
	recorder := providerInstanceRequest(t, mux, http.MethodPut, "/api/provider-instances/owned", providerInstanceAdminBody(t, owned, false))
	if recorder.Code != http.StatusOK {
		t.Fatalf("update status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	cfg := loadSavedConfig(t, configPath)
	if len(cfg.ModelRoutes) != 1 || cfg.ModelRoutes[0].Name != "mixed" || strings.Join(cfg.ModelRoutes[0].Targets, ",") != "other/chat" {
		t.Fatalf("routes after the endpoint change = %#v", cfg.ModelRoutes)
	}
	if cfg.Agents.Defaults.ModelName != "" || cfg.Agents.Defaults.ImageModel != "mixed" {
		t.Fatalf("selections after the endpoint change: default %q, image %q", cfg.Agents.Defaults.ModelName, cfg.Agents.Defaults.ImageModel)
	}
	if store, err := loadCatalogs(); err != nil || store.Entries["owned"] != nil || store.Entries["other"] == nil {
		t.Fatalf("catalogs after the endpoint change = %#v, %v", store, err)
	}
}

func TestProviderInstanceCatalogSyncDropsTargetsTheCatalogNoLongerHolds(t *testing.T) {
	h, mux, configPath := providerInstanceTestHandler(t, providerInstanceFixture("owned", "https://owned.example.test/v1"))
	h.providerCredentialResolver = func(string) (string, error) { return "secret", nil }
	h.providerCatalogSync = func(context.Context, ProviderCatalogSyncInput) ([]CatalogModel, error) {
		return []CatalogModel{{ID: "kept"}}, nil
	}
	editSavedConfig(t, configPath, func(cfg *config.Config) {
		cfg.ActiveModels = []string{"owned/kept", "owned/gone"}
		cfg.ModelRoutes = []*config.ModelRouteConfig{{Name: "fallback", Targets: []string{"owned/gone"}}}
		cfg.Agents.Defaults.ModelName = "owned/gone"
		cfg.Agents.Defaults.ImageModel = "owned/kept"
		cfg.Agents.List = []config.AgentConfig{{ID: "helper", Model: "fallback"}}
	})

	recorder := providerInstanceRequest(t, mux, http.MethodPost, "/api/provider-instances/owned/catalog/sync", `{}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("sync status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	cfg := loadSavedConfig(t, configPath)
	if strings.Join(cfg.ActiveModels, ",") != "owned/kept" || len(cfg.ModelRoutes) != 0 {
		t.Fatalf("after sync: active %#v, routes %#v", cfg.ActiveModels, cfg.ModelRoutes)
	}
	if cfg.Agents.Defaults.ModelName != "" || cfg.Agents.Defaults.ImageModel != "owned/kept" || cfg.Agents.List[0].Model != "" {
		t.Fatalf("after sync: default %q, image %q, agent %q", cfg.Agents.Defaults.ModelName, cfg.Agents.Defaults.ImageModel, cfg.Agents.List[0].Model)
	}
}

func TestDeleteProviderRouteClearsSelectionsNamingIt(t *testing.T) {
	_, mux, configPath := providerInstanceTestHandler(t, providerInstanceFixture("owned", "https://owned.example.test/v1"))
	editSavedConfig(t, configPath, func(cfg *config.Config) {
		cfg.ModelRoutes = []*config.ModelRouteConfig{
			{Name: "primary", Targets: []string{"owned/chat"}},
			{Name: "backup", Targets: []string{"owned/chat"}},
		}
		cfg.Agents.Defaults.ModelName = "primary"
		cfg.Agents.Defaults.Routing = &config.RoutingConfig{Enabled: true, LightModel: "primary", Threshold: 0.5}
		cfg.Agents.List = []config.AgentConfig{{ID: "helper", Model: "backup"}}
	})

	recorder := providerInstanceRequest(t, mux, http.MethodDelete, "/api/model-routes/primary", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("delete route status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	cfg := loadSavedConfig(t, configPath)
	if cfg.Agents.Defaults.ModelName != "" || cfg.Agents.Defaults.Routing.LightModel != "" || cfg.Agents.List[0].Model != "backup" {
		t.Fatalf("after deleting the route: default %q, light %q, agent %q", cfg.Agents.Defaults.ModelName, cfg.Agents.Defaults.Routing.LightModel, cfg.Agents.List[0].Model)
	}
}
