package modelservice

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xibodev/compa/pkg/config"
	llmgwproviders "github.com/xibodev/llmgw-core/providers"
)

func TestListRoster(t *testing.T) {
	cfg := &config.Config{
		ProviderInstances: []*config.ProviderInstanceConfig{
			{
				ID:           "openrouter",
				ProviderKind: "openrouter",
				Adapter:      "openai-compatible",
			},
		},
	}
	roster := ListRoster(cfg)
	if len(roster) == 0 {
		t.Fatal("expected non-empty roster")
	}

	foundProvider := false
	for _, item := range roster {
		if item.ID == "openrouter" {
			foundProvider = true
			if !item.Configured {
				t.Fatalf("expected openrouter to be marked configured, got %+v", item)
			}
		}
	}
	if !foundProvider {
		t.Fatal("expected openrouter in roster")
	}
}

func TestListRosterDeduplicatesConfiguredInstances(t *testing.T) {
	cfg := &config.Config{ProviderInstances: []*config.ProviderInstanceConfig{{
		ID:           "openai",
		ProviderKind: "openai",
	}}}
	for _, item := range ListRoster(cfg) {
		if item.ID != "openai" {
			continue
		}
		if item.InstanceCount != 1 || len(item.ConfiguredInstances) != 1 {
			t.Fatalf("configured instances were not deduplicated: %+v", item)
		}
		return
	}
	t.Fatal("openai roster item not found")
}

func TestAutoConnectFreeRequiresInferenceVerification(t *testing.T) {
	t.Setenv("COMPA_HOME", t.TempDir())
	cfg := config.DefaultConfig()
	result, err := AutoConnectFree(context.Background(), cfg, func(context.Context, *config.Config) []AnonymousProviderOutcome {
		return []AnonymousProviderOutcome{{
			RegistryID: "pollinations", ProviderID: "pollinations",
			Status: "connected", Models: []string{"openai-fast"}, ProbeModel: "openai-fast",
			ErrorClass: "rate_limited", Error: "status 429",
		}}
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.OK || result.CatalogDiscovered != 1 || result.Verified != 0 {
		t.Fatalf("result = %#v", result)
	}
	if len(cfg.ProviderInstances) != 0 || len(cfg.ActiveModels) != 0 {
		t.Fatalf("failed inference mutated config: instances=%#v active=%#v", cfg.ProviderInstances, cfg.ActiveModels)
	}
}

func TestAutoConnectFreeActivatesOnlyVerifiedProbeModel(t *testing.T) {
	t.Setenv("COMPA_HOME", t.TempDir())
	cfg := config.DefaultConfig()
	result, err := AutoConnectFree(context.Background(), cfg, func(context.Context, *config.Config) []AnonymousProviderOutcome {
		return []AnonymousProviderOutcome{{
			RegistryID: "pollinations", ProviderID: "pollinations", Status: "verified",
			Models: []string{"openai-fast", "other"}, ProbeModel: "openai-fast", LatencyMS: 10,
		}}
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.Verified != 1 || len(cfg.ProviderInstances) != 1 {
		t.Fatalf("result=%#v instances=%#v", result, cfg.ProviderInstances)
	}
	if len(cfg.ActiveModels) != 1 || cfg.ActiveModels[0] != "pollinations/openai-fast" {
		t.Fatalf("active models = %#v", cfg.ActiveModels)
	}
}

func TestSyncCatalogListsThroughCoreProvider(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer sk-test" || r.Header.Get("X-Team") != "core" {
			t.Errorf("unexpected catalog request: %s %v", r.URL.Path, r.Header)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"test-model","owned_by":"test","display_name":"Test Model"},{"id":"whisper-1","supported_endpoints":["/v1/audio/transcriptions"]},{"id":"tts-1","supported_endpoints":["/v1/audio/speech"]}]}`))
	}))
	defer ts.Close()

	input := ProviderCatalogSyncInput{
		InstanceID: "custom", ProviderKind: "custom", Adapter: "openai-compatible", Protocol: "openai",
		Endpoint: ts.URL + "/v1", Headers: map[string]string{"X-Team": "core"}, Secret: "sk-test",
	}
	models, err := SyncCatalog(context.Background(), input)
	if err != nil {
		t.Fatalf("SyncCatalog failed: %v", err)
	}
	if len(models) != 3 || models[0].ID != "test-model" || models[0].OwnedBy != "test" || models[0].DisplayName != "Test Model" {
		t.Fatalf("unexpected models: %+v", models)
	}
	if strings.Join(models[1].Surfaces, ",") != "audio_transcriptions" || strings.Join(models[2].Surfaces, ",") != "audio_speech" {
		t.Fatalf("surfaces = %v / %v", models[1].Surfaces, models[2].Surfaces)
	}
}

func TestSyncCatalogListsAnthropicModels(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("X-Api-Key") != "sk-ant" || r.Header.Get("Anthropic-Version") == "" {
			t.Errorf("unexpected catalog request: %s %v", r.URL.Path, r.Header)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-test","type":"model","display_name":"Claude Test"}],"has_more":false}`))
	}))
	defer ts.Close()
	input := ProviderCatalogSyncInput{
		InstanceID: "anthropic", ProviderKind: "anthropic", Adapter: "anthropic-compatible", Protocol: "anthropic-messages",
		Endpoint: ts.URL + "/v1", Secret: "sk-ant",
	}
	models, err := SyncCatalog(context.Background(), input)
	if err != nil {
		t.Fatalf("SyncCatalog failed: %v", err)
	}
	if len(models) != 1 || models[0].ID != "claude-test" || models[0].DisplayName != "Claude Test" {
		t.Fatalf("unexpected models: %+v", models)
	}
}

// The roster is derived from llmgw-core's registry alone: every item is a
// registry entry carrying the registry's own identity, and Compa adds none.
func TestListRosterIsDerivedFromTheCoreRegistry(t *testing.T) {
	roster := ListRoster(nil)
	if len(roster) == 0 {
		t.Fatal("empty roster")
	}
	for _, item := range roster {
		entry, ok := llmgwproviders.RegistryProviderByID(item.ID)
		if !ok {
			t.Fatalf("roster item %q is not a core registry entry", item.ID)
		}
		if item.DisplayName != entry.Label || item.DefaultEndpoint != entry.DefaultBaseURL ||
			item.RequiresAPIKey != entry.RequiresAPIKey || item.Description != entry.Description {
			t.Fatalf("roster item %q = %+v, not the registry's identity %+v", item.ID, item, entry)
		}
		if item.Compatibility != "compatible" || item.Adapter == "" {
			t.Fatalf("roster item %q is not servable: %+v", item.ID, item)
		}
	}
	for _, studioOnly := range []string{"deepseek", "openai_compat", "anthropic-messages", "qwen-portal"} {
		for _, item := range roster {
			if item.ID == studioOnly {
				t.Fatalf("roster still carries the Studio preset %q", studioOnly)
			}
		}
	}
}
