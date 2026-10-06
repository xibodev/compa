package modelservice

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/xibodev/compa/v2/pkg/auth"
	"github.com/xibodev/compa/v2/pkg/config"
)

// connectFixture points the auth store and catalogs at a fresh home and
// saves cfg as the config file, whose path it returns.
func connectFixture(t *testing.T, cfg *config.Config) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	path := filepath.Join(home, "config.json")
	if cfg == nil {
		cfg = config.DefaultConfig()
	}
	if err := config.SaveConfig(path, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	return path
}

func catalogOf(models ...string) CatalogSyncFunc {
	return func(context.Context, ProviderCatalogSyncInput) ([]CatalogModel, error) {
		catalog := make([]CatalogModel, 0, len(models))
		for _, model := range models {
			catalog = append(catalog, CatalogModel{ID: model})
		}
		return catalog, nil
	}
}

func mustLoad(t *testing.T, path string) *config.Config {
	t.Helper()
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	return cfg
}

func instanceByID(cfg *config.Config, id string) *config.ProviderInstanceConfig {
	for _, instance := range cfg.ProviderInstances {
		if instance != nil && instance.ID == id {
			return instance
		}
	}
	return nil
}

func TestConnectAPIKeyProviderRecordsInstanceKeyAndCatalog(t *testing.T) {
	path := connectFixture(t, nil)
	var synced ProviderCatalogSyncInput
	deps := ConnectDeps{SyncCatalog: func(_ context.Context, input ProviderCatalogSyncInput) ([]CatalogModel, error) {
		synced = input
		return []CatalogModel{{ID: "gpt-test", OwnedBy: "openai"}}, nil
	}}

	if err := ConnectAPIKeyProvider(context.Background(), path, " OpenAI ", " sk-test ", deps); err != nil {
		t.Fatalf("ConnectAPIKeyProvider() error = %v", err)
	}
	if synced.InstanceID != "openai" || synced.Adapter != config.ProviderAdapterOpenAICompatible || synced.Secret != "sk-test" {
		t.Fatalf("catalog sync input = %#v", synced)
	}

	cfg := mustLoad(t, path)
	instance := instanceByID(cfg, "openai")
	if instance == nil || instance.ProviderKind != "openai" || instance.Protocol != "openai" ||
		instance.Endpoint != "https://api.openai.com/v1" || instance.AuthConnectionRef != "credential:openai" ||
		instance.State != config.ProviderInstanceStateEnabled {
		t.Fatalf("instance = %#v", instance)
	}
	cred, err := auth.GetCredential("openai")
	if err != nil || cred == nil || cred.AccessToken != "sk-test" || cred.AuthMethod != APIKeyAuthMethod {
		t.Fatalf("credential = %#v, %v", cred, err)
	}
	store, err := LoadCatalogs()
	if err != nil || len(store.Entries["openai"].Models) != 1 || store.Entries["openai"].Models[0].ID != "gpt-test" {
		t.Fatalf("catalogs = %#v, %v", store, err)
	}
	if err := NewResolver().Check(cfg, "openai/gpt-test"); err != nil {
		t.Fatalf("the connected target does not resolve: %v", err)
	}
}

func TestConnectAPIKeyProviderConnectsAnthropic(t *testing.T) {
	path := connectFixture(t, nil)
	if err := ConnectAPIKeyProvider(context.Background(), path, "anthropic", "sk-ant", ConnectDeps{SyncCatalog: catalogOf("claude-test")}); err != nil {
		t.Fatalf("ConnectAPIKeyProvider() error = %v", err)
	}
	instance := instanceByID(mustLoad(t, path), "anthropic")
	if instance == nil || instance.Adapter != config.ProviderAdapterAnthropicCompatible || instance.Protocol != "anthropic-messages" ||
		instance.AuthConnectionRef != "credential:anthropic" {
		t.Fatalf("instance = %#v", instance)
	}
}

func TestConnectAPIKeyProviderRejectsBadInput(t *testing.T) {
	path := connectFixture(t, nil)
	deps := ConnectDeps{SyncCatalog: func(context.Context, ProviderCatalogSyncInput) ([]CatalogModel, error) {
		t.Fatal("bad input reached catalog discovery")
		return nil, nil
	}}
	if err := ConnectAPIKeyProvider(context.Background(), path, "gemini", "key", deps); err == nil || !strings.Contains(err.Error(), "unsupported provider") {
		t.Fatalf("unsupported provider error = %v", err)
	}
	if err := ConnectAPIKeyProvider(context.Background(), path, "openai", "   ", deps); err == nil || !strings.Contains(err.Error(), "API key is required") {
		t.Fatalf("empty key error = %v", err)
	}
	if err := DisconnectAPIKeyProvider(path, "gemini", deps); err == nil || !strings.Contains(err.Error(), "unsupported provider") {
		t.Fatalf("unsupported disconnect error = %v", err)
	}
}

func TestConnectAPIKeyProviderRestoresThePreviousKeyWhenItCannotConnect(t *testing.T) {
	path := connectFixture(t, nil)
	failing := ConnectDeps{SyncCatalog: func(context.Context, ProviderCatalogSyncInput) ([]CatalogModel, error) {
		return nil, context.DeadlineExceeded
	}}

	if err := ConnectAPIKeyProvider(context.Background(), path, "openai", "bad-key", failing); err == nil {
		t.Fatal("ConnectAPIKeyProvider() error = nil for a failing discovery")
	}
	if cred, err := auth.GetCredential("openai"); err != nil || cred != nil {
		t.Fatalf("credential = %#v, %v; want the new key removed", cred, err)
	}

	if err := auth.SetCredential("openai", &auth.AuthCredential{AccessToken: "sk-old", AuthMethod: APIKeyAuthMethod}); err != nil {
		t.Fatal(err)
	}
	empty := ConnectDeps{SyncCatalog: catalogOf()}
	if err := ConnectAPIKeyProvider(context.Background(), path, "openai", "sk-new", empty); err == nil || !strings.Contains(err.Error(), "no usable models") {
		t.Fatalf("ConnectAPIKeyProvider() with an empty catalog error = %v", err)
	}
	if cred, err := auth.GetCredential("openai"); err != nil || cred == nil || cred.AccessToken != "sk-old" {
		t.Fatalf("credential = %#v, %v; want the previous key", cred, err)
	}
	if cfg := mustLoad(t, path); len(cfg.ProviderInstances) != 0 {
		t.Fatalf("a failed connect recorded instances: %#v", cfg.ProviderInstances)
	}
}

func TestConnectAPIKeyProviderRestoresCatalogsWhenTheConfigCannotBeSaved(t *testing.T) {
	path := connectFixture(t, nil)
	deps := ConnectDeps{
		SyncCatalog: catalogOf("gpt-test"),
		SaveConfig:  func(string, *config.Config) error { return errors.New("disk full") },
	}
	if err := ConnectAPIKeyProvider(context.Background(), path, "openai", "sk-test", deps); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("ConnectAPIKeyProvider() error = %v", err)
	}
	if store, err := LoadCatalogs(); err != nil || store.Entries["openai"] != nil {
		t.Fatalf("catalogs = %#v, %v; want the catalog save rolled back", store, err)
	}
	if cred, err := auth.GetCredential("openai"); err != nil || cred != nil {
		t.Fatalf("credential = %#v, %v; want the key removed", cred, err)
	}
}

// reconnectFixture is a config whose openai instance already serves targets
// in the active models, routes and every kind of selection.
func reconnectFixture() *config.Config {
	cfg := config.DefaultConfig()
	openai, _ := APIKeyProviderInstance(APIKeyProviderOpenAI)
	openai.Headers = map[string]string{"OpenAI-Organization": "org-1"}
	openai.Runtime = &config.ProviderInstanceRuntime{RPM: 30, ThinkingLevel: "low"}
	other := &config.ProviderInstanceConfig{
		ID: "other", ProviderKind: "groq", Adapter: config.ProviderAdapterOpenAICompatible, Protocol: "openai",
		Endpoint: "https://api.groq.com/openai/v1", State: config.ProviderInstanceStateEnabled,
	}
	cfg.ProviderInstances = []*config.ProviderInstanceConfig{openai, other}
	cfg.ActiveModels = []string{"openai/old", "openai/kept", "other/x"}
	cfg.ModelRoutes = []*config.ModelRouteConfig{
		{Name: "only-old", Targets: []string{"openai/old"}},
		{Name: "mixed", Targets: []string{"openai/old", "other/x"}},
	}
	cfg.Agents.Defaults.ModelName = "openai/old"
	cfg.Agents.Defaults.ImageModel = "openai/kept"
	cfg.Agents.Defaults.Routing = &config.RoutingConfig{Enabled: true, LightModel: "mixed", Threshold: 0.3}
	cfg.Agents.List = []config.AgentConfig{{ID: "main", Model: "only-old"}, {ID: "helper", Model: "other/x"}}
	return cfg
}

func TestConnectAPIKeyProviderDropsTargetsTheNewCatalogLacks(t *testing.T) {
	path := connectFixture(t, reconnectFixture())
	if err := ConnectAPIKeyProvider(context.Background(), path, "openai", "sk-test", ConnectDeps{SyncCatalog: catalogOf("kept", "new")}); err != nil {
		t.Fatalf("ConnectAPIKeyProvider() error = %v", err)
	}

	cfg := mustLoad(t, path)
	if !slices.Equal(cfg.ActiveModels, []string{"openai/kept", "other/x"}) {
		t.Fatalf("active models = %v", cfg.ActiveModels)
	}
	if len(cfg.ModelRoutes) != 1 || cfg.ModelRoutes[0].Name != "mixed" || !slices.Equal(cfg.ModelRoutes[0].Targets, []string{"other/x"}) {
		t.Fatalf("routes = %#v", cfg.ModelRoutes)
	}
	defaults := cfg.Agents.Defaults
	if defaults.ModelName != "" {
		t.Fatalf("default selection %q names a dropped target", defaults.ModelName)
	}
	if defaults.ImageModel != "openai/kept" || defaults.Routing.LightModel != "mixed" {
		t.Fatalf("selections on kept targets and routes changed: image=%q light=%q", defaults.ImageModel, defaults.Routing.LightModel)
	}
	if cfg.Agents.List[0].Model != "" || cfg.Agents.List[1].Model != "other/x" {
		t.Fatalf("agent selections = %q, %q; want the removed route cleared", cfg.Agents.List[0].Model, cfg.Agents.List[1].Model)
	}
	instance := instanceByID(cfg, "openai")
	if instance.Headers["OpenAI-Organization"] != "org-1" || instance.Runtime == nil || instance.Runtime.RPM != 30 || instance.Runtime.ThinkingLevel != "low" {
		t.Fatalf("reconnecting lost the instance's headers or runtime settings: %#v", instance)
	}
}

func TestDisconnectAPIKeyProviderDisablesInstancesAndClearsSelections(t *testing.T) {
	cfg := reconnectFixture()
	proxy := &config.ProviderInstanceConfig{
		ID: "openai-proxy", ProviderKind: "openai", Adapter: config.ProviderAdapterOpenAICompatible, Protocol: "openai",
		Endpoint: "https://proxy.example.test/v1", AuthConnectionRef: "credential:openai", State: config.ProviderInstanceStateEnabled,
	}
	cfg.ProviderInstances = append(cfg.ProviderInstances, proxy)
	cfg.ActiveModels = append(cfg.ActiveModels, "openai-proxy/p")
	cfg.Agents.Defaults.ImageModel = "openai-proxy/p"
	path := connectFixture(t, cfg)
	for _, instance := range cfg.ProviderInstances {
		saveCatalogFixture(t, instance, "old", "kept", "x", "p")
	}
	if err := auth.SetCredential("openai", &auth.AuthCredential{AccessToken: "sk-test", AuthMethod: APIKeyAuthMethod}); err != nil {
		t.Fatal(err)
	}

	if err := DisconnectAPIKeyProvider(path, "openai", ConnectDeps{}); err != nil {
		t.Fatalf("DisconnectAPIKeyProvider() error = %v", err)
	}

	if cred, err := auth.GetCredential("openai"); err != nil || cred != nil {
		t.Fatalf("credential = %#v, %v; want it deleted", cred, err)
	}
	updated := mustLoad(t, path)
	for _, id := range []string{"openai", "openai-proxy"} {
		if instance := instanceByID(updated, id); instance == nil || instance.State != config.ProviderInstanceStateDisabled {
			t.Fatalf("instance %q = %#v, want it disabled", id, instance)
		}
	}
	if instance := instanceByID(updated, "other"); instance.State != config.ProviderInstanceStateEnabled {
		t.Fatalf("an instance with its own credential was disabled: %#v", instance)
	}
	if !slices.Equal(updated.ActiveModels, []string{"other/x"}) {
		t.Fatalf("active models = %v", updated.ActiveModels)
	}
	if len(updated.ModelRoutes) != 1 || !slices.Equal(updated.ModelRoutes[0].Targets, []string{"other/x"}) {
		t.Fatalf("routes = %#v", updated.ModelRoutes)
	}
	defaults := updated.Agents.Defaults
	if defaults.ModelName != "" || defaults.ImageModel != "" || updated.Agents.List[0].Model != "" {
		t.Fatalf("selections on disabled instances or removed routes remain: default=%q image=%q agent=%q",
			defaults.ModelName, defaults.ImageModel, updated.Agents.List[0].Model)
	}
	if defaults.Routing.LightModel != "mixed" || updated.Agents.List[1].Model != "other/x" {
		t.Fatalf("selections on surviving targets changed: light=%q agent=%q", defaults.Routing.LightModel, updated.Agents.List[1].Model)
	}
	store, err := LoadCatalogs()
	if err != nil {
		t.Fatal(err)
	}
	if store.Entries["openai"] != nil || store.Entries["openai-proxy"] != nil || store.Entries["other"] == nil {
		t.Fatalf("catalogs = %v, want only the disabled instances' deleted", store.Entries)
	}
}

func TestDisconnectAPIKeyProviderRestoresTheKeyWhenTheConfigCannotBeSaved(t *testing.T) {
	path := connectFixture(t, nil)
	if err := auth.SetCredential("anthropic", &auth.AuthCredential{AccessToken: "kept-key"}); err != nil {
		t.Fatal(err)
	}
	deps := ConnectDeps{SaveConfig: func(string, *config.Config) error { return errors.New("disk full") }}
	if err := DisconnectAPIKeyProvider(path, "anthropic", deps); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("DisconnectAPIKeyProvider() error = %v", err)
	}
	if cred, err := auth.GetCredential("anthropic"); err != nil || cred == nil || cred.AccessToken != "kept-key" {
		t.Fatalf("credential = %#v, %v; want the key restored", cred, err)
	}
}

func TestDropTargetsLeavesUnrelatedSelectionsAlone(t *testing.T) {
	cfg := &config.Config{
		ActiveModels: []string{"gone/a", "not a target", "kept/b"},
		ModelRoutes: []*config.ModelRouteConfig{
			nil,
			{Name: "emptied", Targets: []string{"gone/a"}},
			{Name: "survives", Targets: []string{"gone/a", "kept/b"}},
		},
	}
	cfg.Agents.Defaults.ModelName = " gone/a "
	cfg.Agents.Defaults.ImageModel = "unknown-route"
	cfg.Agents.List = []config.AgentConfig{{ID: "a", Model: "emptied"}, {ID: "b", Model: "survives"}, {ID: "c"}}

	DropTargets(cfg, func(target config.ExactModelTarget) bool { return target.InstanceID != "gone" })
	DropTargets(nil, nil)
	DropTargets(cfg, nil)

	if !slices.Equal(cfg.ActiveModels, []string{"not a target", "kept/b"}) {
		t.Fatalf("active models = %q", cfg.ActiveModels)
	}
	if len(cfg.ModelRoutes) != 1 || cfg.ModelRoutes[0].Name != "survives" || !slices.Equal(cfg.ModelRoutes[0].Targets, []string{"kept/b"}) {
		t.Fatalf("routes = %#v", cfg.ModelRoutes)
	}
	if cfg.Agents.Defaults.ModelName != "" || cfg.Agents.List[0].Model != "" {
		t.Fatalf("stale selections remain: default=%q agent=%q", cfg.Agents.Defaults.ModelName, cfg.Agents.List[0].Model)
	}
	if cfg.Agents.Defaults.ImageModel != "unknown-route" || cfg.Agents.List[1].Model != "survives" || cfg.Agents.List[2].Model != "" {
		t.Fatalf("unrelated selections changed: %+v", cfg.Agents)
	}
}
