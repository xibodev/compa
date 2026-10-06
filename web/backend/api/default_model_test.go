package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xibodev/compa/v2/pkg/config"
	"github.com/xibodev/compa/v2/pkg/modelservice"
)

// defaultModelTestHandler serves a config whose provider instance "owned"
// serves "gpt-owned" and "vendor/model-x", whose disabled instance "off"
// serves "gpt-off", and whose route "fast" targets owned/gpt-owned.
func defaultModelTestHandler(t *testing.T) (*http.ServeMux, string) {
	t.Helper()
	off := providerInstanceFixture("off", "https://off.example.test/v1")
	off.State = config.ProviderInstanceStateDisabled
	_, mux, configPath := providerInstanceTestHandler(t, providerInstanceFixture("owned", "https://owned.example.test/v1"), off)
	if err := saveCatalogs(&CatalogStore{Entries: map[string]*CatalogEntry{
		"owned": {ID: "owned", InstanceID: "owned", Provider: "openai", Models: []CatalogModel{{ID: "gpt-owned"}, {ID: "vendor/model-x"}}},
		"off":   {ID: "off", InstanceID: "off", Provider: "openai", Models: []CatalogModel{{ID: "gpt-off"}}},
	}}); err != nil {
		t.Fatalf("saveCatalogs() error = %v", err)
	}
	editSavedConfig(t, configPath, func(cfg *config.Config) {
		cfg.ModelRoutes = []*config.ModelRouteConfig{{Name: "fast", Targets: []string{"owned/gpt-owned"}}}
	})
	return mux, configPath
}

// editSavedConfig applies change to the config saved at configPath.
func editSavedConfig(t *testing.T, configPath string, change func(cfg *config.Config)) {
	t.Helper()
	cfg := loadSavedConfig(t, configPath)
	change(cfg)
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
}

func loadSavedConfig(t *testing.T, configPath string) *config.Config {
	t.Helper()
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	return cfg
}

func defaultModelRequest(t *testing.T, mux *http.ServeMux, method, body string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, "/api/default-model", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		return recorder, ""
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("Unmarshal(%s) error = %v", recorder.Body.String(), err)
	}
	selection, ok := response["selection"].(string)
	if !ok || len(response) != 1 {
		t.Fatalf("response = %s, want exactly {\"selection\": string}", recorder.Body.String())
	}
	return recorder, selection
}

func TestDefaultModelGetSetAndClear(t *testing.T) {
	mux, configPath := defaultModelTestHandler(t)

	if recorder, selection := defaultModelRequest(t, mux, http.MethodGet, ""); recorder.Code != http.StatusOK || selection != "" {
		t.Fatalf("GET without a default = %d %q, body = %s", recorder.Code, selection, recorder.Body.String())
	}

	for _, tc := range []struct{ body, want string }{
		{body: `{"selection":"owned/gpt-owned"}`, want: "owned/gpt-owned"},
		{body: `{"selection":" fast "}`, want: "fast"},
		{body: `{"selection":"owned/vendor/model-x"}`, want: "owned/vendor/model-x"},
	} {
		recorder, selection := defaultModelRequest(t, mux, http.MethodPut, tc.body)
		if recorder.Code != http.StatusOK || selection != tc.want {
			t.Fatalf("PUT %s = %d %q, body = %s, want %q", tc.body, recorder.Code, selection, recorder.Body.String(), tc.want)
		}
		if saved := loadSavedConfig(t, configPath).Agents.Defaults.ModelName; saved != tc.want {
			t.Fatalf("saved default = %q, want %q", saved, tc.want)
		}
		if recorder, selection := defaultModelRequest(t, mux, http.MethodGet, ""); recorder.Code != http.StatusOK || selection != tc.want {
			t.Fatalf("GET after PUT %s = %d %q, want %q", tc.body, recorder.Code, selection, tc.want)
		}
	}

	recorder, selection := defaultModelRequest(t, mux, http.MethodPut, `{"selection":""}`)
	if recorder.Code != http.StatusOK || selection != "" {
		t.Fatalf("clearing PUT = %d %q, body = %s", recorder.Code, selection, recorder.Body.String())
	}
	if saved := loadSavedConfig(t, configPath).Agents.Defaults.ModelName; saved != "" {
		t.Fatalf("saved default after clearing = %q, want empty", saved)
	}
}

func TestDefaultModelRejectsSelectionsThatDoNotResolve(t *testing.T) {
	mux, configPath := defaultModelTestHandler(t)
	editSavedConfig(t, configPath, func(cfg *config.Config) { cfg.Agents.Defaults.ModelName = "owned/gpt-owned" })

	for _, tc := range []struct{ body, contains string }{
		{body: `{"selection":"owned/missing"}`, contains: `model "missing" not found in provider instance "owned" catalog`},
		{body: `{"selection":"ghost/gpt-owned"}`, contains: `provider instance "ghost" not found`},
		{body: `{"selection":"off/gpt-off"}`, contains: `provider instance "off" is disabled`},
		{body: `{"selection":"no-such-route"}`, contains: "route was not found"},
		{body: `{"selection":"Not A Selection"}`, contains: "must be an exact target instance-id/model-id or a model route name"},
		{body: `{"selection":"owned/"}`, contains: "model-id is invalid"},
		{body: `{}`, contains: "selection is required"},
		{body: `{"selection":"owned/gpt-owned","fallbacks":[]}`, contains: "unknown field"},
		{body: `not json`, contains: "Invalid JSON"},
	} {
		recorder, _ := defaultModelRequest(t, mux, http.MethodPut, tc.body)
		if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), tc.contains) {
			t.Fatalf("PUT %s = %d, body = %s, want 400 containing %q", tc.body, recorder.Code, recorder.Body.String(), tc.contains)
		}
	}
	if saved := loadSavedConfig(t, configPath).Agents.Defaults.ModelName; saved != "owned/gpt-owned" {
		t.Fatalf("a refused PUT changed the default to %q", saved)
	}
}

func TestDefaultModelRejectsModelsThatDoNotServeChat(t *testing.T) {
	mux, _ := defaultModelTestHandler(t)
	if err := saveCatalogs(&CatalogStore{Entries: map[string]*CatalogEntry{
		"owned": {ID: "owned", InstanceID: "owned", Provider: "openai", Models: []CatalogModel{
			{ID: "gpt-owned", Surfaces: []string{"chat_completions"}},
			{ID: "voice-only", Surfaces: []string{"audio_speech"}},
		}},
	}}); err != nil {
		t.Fatalf("saveCatalogs() error = %v", err)
	}

	recorder, _ := defaultModelRequest(t, mux, http.MethodPut, `{"selection":"owned/voice-only"}`)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "does not serve chat") {
		t.Fatalf("speech-only default: status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if recorder, got := defaultModelRequest(t, mux, http.MethodPut, `{"selection":"owned/gpt-owned"}`); got != "owned/gpt-owned" {
		t.Fatalf("chat default: status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestModelListAPIRoutesAreGone(t *testing.T) {
	mux, _ := defaultModelTestHandler(t)
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/models"},
		{http.MethodPost, "/api/models"},
		{http.MethodPost, "/api/models/default"},
		{http.MethodGet, "/api/models/default-chain"},
		{http.MethodPut, "/api/models/default-chain"},
		{http.MethodGet, "/api/models/catalog"},
		{http.MethodDelete, "/api/models/catalog/some-key"},
		{http.MethodPost, "/api/models/fetch"},
		{http.MethodPut, "/api/models/0"},
		{http.MethodPost, "/api/models/test-inline"},
	} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(route.method, route.path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s %s = %d, want 404: the model list API is gone", route.method, route.path, recorder.Code)
		}
	}
}

func configPatchRequest(t *testing.T, mux *http.ServeMux, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(recorder, request)
	return recorder
}

func TestConfigUpdateChecksTheModelSelectionsItChanges(t *testing.T) {
	mux, configPath := defaultModelTestHandler(t)
	// A default left dangling by a provider change does not block an edit
	// that leaves it alone.
	editSavedConfig(t, configPath, func(cfg *config.Config) { cfg.Agents.Defaults.ModelName = "owned/retired" })
	if recorder := configPatchRequest(t, mux, `{"agents":{"defaults":{"max_tokens":2048}}}`); recorder.Code != http.StatusOK {
		t.Fatalf("unrelated PATCH = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	for _, tc := range []struct{ body, contains string }{
		{body: `{"agents":{"defaults":{"image_model":"owned/missing"}}}`, contains: "agents.defaults.image_model"},
		{body: `{"agents":{"defaults":{"routing":{"enabled":true,"light_model":"off/gpt-off","threshold":0.5}}}}`, contains: "agents.defaults.routing.light_model"},
		{body: `{"agents":{"list":[{"id":"helper","model":"no-such-route"}]}}`, contains: "agents.list[0].model"},
		{body: `{"agents":{"defaults":{"model_name":"Not A Selection"}}}`, contains: "agents.defaults.model_name"},
		{body: `{"model_routes":[{"name":"broken","targets":["ghost/model"]}]}`, contains: `provider instance \"ghost\" not found`},
	} {
		recorder := configPatchRequest(t, mux, tc.body)
		if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), tc.contains) {
			t.Fatalf("PATCH %s = %d, body = %s, want 400 containing %q", tc.body, recorder.Code, recorder.Body.String(), tc.contains)
		}
	}

	recorder := configPatchRequest(t, mux, `{"agents":{"defaults":{"image_model":"owned/vendor/model-x"},"list":[{"id":"helper","model":"fast"}]}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("PATCH with resolving selections = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	saved := loadSavedConfig(t, configPath)
	if saved.Agents.Defaults.ImageModel != "owned/vendor/model-x" || len(saved.Agents.List) != 1 || saved.Agents.List[0].Model != "fast" {
		t.Fatalf("saved selections = image %q, agents %#v", saved.Agents.Defaults.ImageModel, saved.Agents.List)
	}
	if saved.Agents.Defaults.ModelName != "owned/retired" {
		t.Fatalf("saved default = %q, want the untouched dangling selection", saved.Agents.Defaults.ModelName)
	}
}

// activeModelsRequest posts body to path and returns the response's
// active models and default model.
func activeModelsRequest(t *testing.T, mux *http.ServeMux, path, body string) (int, []string, string) {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(recorder, request)
	var response struct {
		ActiveModels []string `json:"active_models"`
		DefaultModel string   `json:"default_model"`
	}
	if recorder.Code == http.StatusOK {
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("Unmarshal(%s) error = %v", recorder.Body.String(), err)
		}
	}
	return recorder.Code, response.ActiveModels, response.DefaultModel
}

// The first model added to the chat shortlist becomes the default model
// when none is set; a default already set stays.
func TestShortlistingAModelMakesItTheDefaultWhenNoneIsSet(t *testing.T) {
	mux, configPath := defaultModelTestHandler(t)

	code, active, defaultModel := activeModelsRequest(t, mux, "/api/active-models/add", `{"target":"owned/gpt-owned"}`)
	if code != http.StatusOK || strings.Join(active, ",") != "owned/gpt-owned" || defaultModel != "owned/gpt-owned" {
		t.Fatalf("add = %d %v default %q, want the added model as the default", code, active, defaultModel)
	}
	if saved := loadSavedConfig(t, configPath).Agents.Defaults.ModelName; saved != "owned/gpt-owned" {
		t.Fatalf("saved default = %q", saved)
	}
	code, active, defaultModel = activeModelsRequest(t, mux, "/api/active-models/add", `{"target":"owned/vendor/model-x"}`)
	if code != http.StatusOK || len(active) != 2 || defaultModel != "owned/gpt-owned" {
		t.Fatalf("second add = %d %v default %q, want the first default kept", code, active, defaultModel)
	}

	// Replacing the shortlist adopts the first model it gains, once the
	// default is cleared.
	if recorder, _ := defaultModelRequest(t, mux, http.MethodPut, `{"selection":""}`); recorder.Code != http.StatusOK {
		t.Fatalf("clear default = %d", recorder.Code)
	}
	code, _, defaultModel = activeModelsRequest(t, mux, "/api/active-models", `{"models":["owned/vendor/model-x","owned/gpt-owned"]}`)
	if code != http.StatusOK || defaultModel != "" {
		t.Fatalf("reordering the shortlist = %d default %q, want no default adopted: nothing was added", code, defaultModel)
	}
	editSavedConfig(t, configPath, func(cfg *config.Config) { cfg.ActiveModels = []string{"owned/vendor/model-x"} })
	code, _, defaultModel = activeModelsRequest(t, mux, "/api/active-models", `{"models":["owned/vendor/model-x","owned/gpt-owned"]}`)
	if code != http.StatusOK || defaultModel != "owned/gpt-owned" {
		t.Fatalf("set = %d default %q, want the added model adopted", code, defaultModel)
	}
	if saved := loadSavedConfig(t, configPath).Agents.Defaults.ModelName; saved != "owned/gpt-owned" {
		t.Fatalf("saved default = %q", saved)
	}

	// A refused addition adopts nothing.
	if recorder, _ := defaultModelRequest(t, mux, http.MethodPut, `{"selection":""}`); recorder.Code != http.StatusOK {
		t.Fatalf("clear default = %d", recorder.Code)
	}
	if code, _, _ := activeModelsRequest(t, mux, "/api/active-models/add", `{"target":"off/gpt-off"}`); code != http.StatusBadRequest {
		t.Fatalf("adding a disabled instance's model = %d, want 400", code)
	}
	if saved := loadSavedConfig(t, configPath).Agents.Defaults.ModelName; saved != "" {
		t.Fatalf("a refused addition set the default to %q", saved)
	}
}

// The free-provider connection makes its first verified model the default
// model when none is set.
func TestAutoConnectFreeSetsTheDefaultModelWhenNoneIsSet(t *testing.T) {
	h, mux, configPath := providerInstanceTestHandler(t)
	h.providerAnonymousVerify = func(context.Context, *config.Config) []modelservice.AnonymousProviderOutcome {
		return []modelservice.AnonymousProviderOutcome{{
			RegistryID: "pollinations", ProviderID: "pollinations", Status: "verified",
			Models: []string{"openai-fast"}, ProbeModel: "openai-fast",
		}}
	}
	rec := providerInstanceRequest(t, mux, http.MethodPost, "/api/provider-instances/auto-connect-free", "")
	var response struct {
		Verified     int    `json:"verified"`
		DefaultModel string `json:"default_model"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("auto-connect = %d %s", rec.Code, rec.Body.String())
	}
	if response.Verified != 1 || response.DefaultModel != "pollinations/openai-fast" {
		t.Fatalf("auto-connect = %+v, want the verified model as the default", response)
	}
	if saved := loadSavedConfig(t, configPath).Agents.Defaults.ModelName; saved != "pollinations/openai-fast" {
		t.Fatalf("saved default = %q", saved)
	}

	editSavedConfig(t, configPath, func(cfg *config.Config) { cfg.Agents.Defaults.ModelName = "pollinations/chosen" })
	rec = providerInstanceRequest(t, mux, http.MethodPost, "/api/provider-instances/auto-connect-free", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("second auto-connect = %d %s", rec.Code, rec.Body.String())
	}
	if saved := loadSavedConfig(t, configPath).Agents.Defaults.ModelName; saved != "pollinations/chosen" || response.DefaultModel != "" {
		t.Fatalf("saved default = %q, reported %q; want the chosen default kept", saved, response.DefaultModel)
	}
}
