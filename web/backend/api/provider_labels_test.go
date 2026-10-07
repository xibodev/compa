package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/xibodev/compa/v3/pkg/config"
	"github.com/xibodev/compa/v3/pkg/modelservice"
)

// listedInstances returns GET /api/provider-instances keyed by id, both as
// the typed view and as raw JSON, so a field's presence is checked too.
func listedInstances(t *testing.T, mux http.Handler) (map[string]providerInstanceResponse, map[string]map[string]any) {
	t.Helper()
	rec := extensionRequest(t, mux, http.MethodGet, "/api/provider-instances", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var typed struct {
		Instances []providerInstanceResponse `json:"instances"`
	}
	var raw struct {
		Instances []map[string]any `json:"instances"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &typed); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]providerInstanceResponse, len(typed.Instances))
	rawByID := make(map[string]map[string]any, len(raw.Instances))
	for i, instance := range typed.Instances {
		byID[instance.ID] = instance
		rawByID[instance.ID] = raw.Instances[i]
	}
	return byID, rawByID
}

func TestProviderInstancesReportLabelsAndCredentialStatus(t *testing.T) {
	keyed := providerInstanceFixture("openai-main", "https://api.openai.example.test/v1")
	unkeyed := providerInstanceFixture("openai-bare", "https://api.openai.example.test/v1")
	unkeyed.AuthConnectionRef = ""
	lost := providerInstanceFixture("openai-lost", "https://api.openai.example.test/v1")
	local := &config.ProviderInstanceConfig{
		ID: "local", ProviderKind: "ollama", Adapter: config.ProviderAdapterNative, Protocol: "ollama",
		Endpoint: "http://127.0.0.1:11434", State: config.ProviderInstanceStateEnabled,
	}
	custom := &config.ProviderInstanceConfig{
		ID: "my-box", ProviderKind: "my-box", Adapter: config.ProviderAdapterOpenAICompatible, Protocol: "openai",
		Endpoint: "http://box.example.test/v1", State: config.ProviderInstanceStateEnabled,
	}
	customKeyed := &config.ProviderInstanceConfig{
		ID: "my-keyed-box", ProviderKind: "my-box", Adapter: config.ProviderAdapterOpenAICompatible, Protocol: "openai",
		Endpoint: "http://box.example.test/v1", AuthConnectionRef: "credential:my-keyed-box", State: config.ProviderInstanceStateEnabled,
	}
	h, mux, _ := providerInstanceTestHandler(t, keyed, unkeyed, lost, local, custom, customKeyed)
	h.providerCredentialResolver = func(ref string) (string, error) {
		switch ref {
		case "credential:openai-main", "credential:my-keyed-box":
			return "stored-secret", nil
		}
		return "", errors.New("credential not found")
	}

	byID, raw := listedInstances(t, mux)
	for id, want := range map[string]struct {
		display, kind string
		ready         bool
	}{
		"openai-main":  {"OpenAI", credentialKindAPIKey, true},
		"openai-bare":  {"OpenAI", credentialKindAPIKey, false},
		"openai-lost":  {"OpenAI", credentialKindAPIKey, false},
		"local":        {"Ollama", credentialKindNone, true},
		"my-box":       {"my-box", credentialKindNone, true},
		"my-keyed-box": {"my-keyed-box", credentialKindAPIKey, true},
	} {
		got := byID[id]
		if got.DisplayName != want.display || got.ManagedBy != "" || got.CredentialKind != want.kind || got.CredentialReady != want.ready {
			t.Errorf("%s: display %q managed_by %q credential %q ready %v, want %q \"\" %q %v",
				id, got.DisplayName, got.ManagedBy, got.CredentialKind, got.CredentialReady, want.display, want.kind, want.ready)
		}
		for _, field := range []string{"display_name", "managed_by", "credential_kind", "credential_ready"} {
			if _, ok := raw[id][field]; !ok {
				t.Errorf("%s: response has no %s: %v", id, field, raw[id])
			}
		}
	}
	body := extensionRequest(t, mux, http.MethodGet, "/api/provider-instances", nil).Body.String()
	for _, secret := range []string{"stored-secret", "credential:"} {
		if strings.Contains(body, secret) {
			t.Fatalf("list exposed %q: %s", secret, body)
		}
	}
}

// Extension instances are named as the daemon names their providers, say
// the daemon manages them, and report the credential each provider needs
// and whether it is in place, through the connect, token and sign-in flows.
func TestExtensionInstancesReportDaemonNamesAndCredentialStatus(t *testing.T) {
	mux, _, daemon := extensionTestMux(t)
	if rec := extensionRequest(t, mux, http.MethodPut, "/api/extension", map[string]any{"url": daemon.server.URL, "secret": testDaemonSecret}); rec.Code != http.StatusOK {
		t.Fatalf("connect status = %d, body=%s", rec.Code, rec.Body.String())
	}
	check := func(stage string, want map[string]struct {
		display, kind string
		ready         bool
	}) {
		t.Helper()
		byID, _ := listedInstances(t, mux)
		for id, want := range want {
			got, ok := byID[id]
			if !ok {
				t.Fatalf("%s: no instance %s in %v", stage, id, byID)
			}
			if got.DisplayName != want.display || got.ManagedBy != managedByExtension || got.CredentialKind != want.kind || got.CredentialReady != want.ready {
				t.Errorf("%s: %s display %q managed_by %q credential %q ready %v, want %q %q %q %v", stage, id,
					got.DisplayName, got.ManagedBy, got.CredentialKind, got.CredentialReady, want.display, managedByExtension, want.kind, want.ready)
			}
		}
	}
	type status = struct {
		display, kind string
		ready         bool
	}
	check("connected", map[string]status{
		"ext-free":    {"Free", credentialKindNone, true},
		"ext-pasted":  {"Pasted", credentialKindToken, false},
		"ext-account": {"Account", credentialKindOAuth, false},
		"ext-speaker": {"Speaker", credentialKindNone, true},
	})

	if rec := extensionRequest(t, mux, http.MethodPost, "/api/extension/providers/pasted/token", map[string]any{"token": "pasted-token"}); rec.Code != http.StatusOK {
		t.Fatalf("token status = %d, body=%s", rec.Code, rec.Body.String())
	}
	daemon.mu.Lock()
	daemon.approved = true
	daemon.mu.Unlock()
	rec := extensionRequest(t, mux, http.MethodPost, "/api/extension/providers/account/signin", map[string]any{"method": "device"})
	var flow extensionFlowResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &flow); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("device start = %d %s", rec.Code, rec.Body.String())
	}
	if rec := extensionRequest(t, mux, http.MethodPost, "/api/extension/signin/"+flow.FlowID+"/poll", nil); rec.Code != http.StatusOK {
		t.Fatalf("poll = %d %s", rec.Code, rec.Body.String())
	}
	check("signed in", map[string]status{
		"ext-pasted":  {"Pasted", credentialKindToken, true},
		"ext-account": {"Account", credentialKindOAuth, true},
	})

	if rec := extensionRequest(t, mux, http.MethodDelete, "/api/extension/providers/account/credential", nil); rec.Code != http.StatusOK {
		t.Fatalf("sign out status = %d", rec.Code)
	}
	check("signed out", map[string]status{"ext-account": {"Account", credentialKindOAuth, false}})

	// The targets carry the names to show for the model and its instance.
	rec = extensionRequest(t, mux, http.MethodGet, "/api/provider-targets?all=true", nil)
	var targets struct {
		Targets []providerTargetResponse `json:"targets"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &targets); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, target := range targets.Targets {
		if target.Target == "ext-free/free-model" {
			found = true
			if target.Label != "free-model" || target.InstanceLabel != "Free" {
				t.Fatalf("target = %+v, want label free-model and instance label Free", target)
			}
		}
	}
	if !found {
		t.Fatalf("targets = %+v, want ext-free/free-model", targets.Targets)
	}
}

func TestProviderTargetsCarryModelAndInstanceLabels(t *testing.T) {
	first := providerInstanceFixture("first", "https://first.example.test/v1")
	custom := &config.ProviderInstanceConfig{
		ID: "box", ProviderKind: "my-box", Adapter: config.ProviderAdapterOpenAICompatible, Protocol: "openai",
		Endpoint: "http://box.example.test/v1", State: config.ProviderInstanceStateEnabled,
	}
	_, mux, configPath := providerInstanceTestHandler(t, first, custom)
	if err := saveCatalogs(&CatalogStore{Entries: map[string]*CatalogEntry{
		"first": {ID: "first", InstanceID: "first", Provider: "openai", Models: []CatalogModel{
			{ID: "gpt-named", DisplayName: "GPT Named"}, {ID: "gpt-bare"},
		}},
		"box": {ID: "box", InstanceID: "box", Provider: "my-box", Models: []CatalogModel{{ID: "box-model", DisplayName: " "}}},
	}}); err != nil {
		t.Fatal(err)
	}
	editSavedConfig(t, configPath, func(cfg *config.Config) { cfg.ActiveModels = []string{"first/gpt-named", "box/box-model"} })

	for _, path := range []string{"/api/provider-targets?all=true", "/api/provider-targets"} {
		rec := providerInstanceRequest(t, mux, http.MethodGet, path, "")
		var response struct {
			Targets []map[string]any `json:"targets"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		labels := map[string][2]any{}
		for _, target := range response.Targets {
			labels[target["target"].(string)] = [2]any{target["label"], target["instance_label"]}
			for _, kept := range []string{"target", "instance_id", "model_id", "provider_kind", "fetched_at"} {
				if _, ok := target[kept]; !ok {
					t.Errorf("%s: target lost %s: %v", path, kept, target)
				}
			}
		}
		want := map[string][2]any{
			"first/gpt-named": {"GPT Named", "OpenAI"},
			"box/box-model":   {"box-model", "box"},
		}
		if path == "/api/provider-targets?all=true" {
			want["first/gpt-bare"] = [2]any{"gpt-bare", "OpenAI"}
		}
		if len(labels) != len(want) {
			t.Fatalf("%s: targets = %v, want %v", path, labels, want)
		}
		for target, pair := range want {
			if labels[target] != pair {
				t.Errorf("%s: %s labels = %v, want %v", path, target, labels[target], pair)
			}
		}
	}
}

// The roster offers Google's Gemini API once: core lists it as both gemini
// and ai_studio.
func TestProviderRosterListsEachProviderOnce(t *testing.T) {
	_, mux, _ := providerInstanceTestHandler(t)
	rec := providerInstanceRequest(t, mux, http.MethodGet, "/api/provider-roster", "")
	var response struct {
		Providers []modelservice.ProviderRosterItem `json:"providers"`
		Total     int                               `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("roster = %d %s", rec.Code, rec.Body.String())
	}
	ids := map[string]bool{}
	for _, item := range response.Providers {
		if ids[item.ID] {
			t.Fatalf("roster lists %s twice", item.ID)
		}
		ids[item.ID] = true
	}
	if ids["ai_studio"] || !ids["gemini"] || response.Total != len(response.Providers) {
		t.Fatalf("roster ids = %v total %d, want gemini without ai_studio", ids, response.Total)
	}
}
