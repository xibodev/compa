package modelservice

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xibodev/compa/v3/pkg/auth"
	"github.com/xibodev/compa/v3/pkg/config"
	"github.com/xibodev/compa/v3/pkg/providers"
)

func resolverInstance(id string) *config.ProviderInstanceConfig {
	return &config.ProviderInstanceConfig{
		ID:                id,
		ProviderKind:      "openai",
		Adapter:           config.ProviderAdapterOpenAICompatible,
		Protocol:          "openai",
		Endpoint:          "https://" + id + ".example.test/v1",
		AuthConnectionRef: "credential:" + id + "-auth",
		Headers:           map[string]string{"X-Instance": id},
		State:             config.ProviderInstanceStateEnabled,
	}
}

func saveCatalogFixture(t *testing.T, instance *config.ProviderInstanceConfig, models ...string) {
	t.Helper()
	catalog := make([]CatalogModel, 0, len(models))
	for _, model := range models {
		catalog = append(catalog, CatalogModel{ID: model})
	}
	if err := SaveProviderInstanceCatalog(instance, catalog); err != nil {
		t.Fatalf("SaveProviderInstanceCatalog() error = %v", err)
	}
}

func TestResolverResolvesSavedCatalogAndBuildsProvider(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	instance := resolverInstance("fixture")
	cfg := &config.Config{ProviderInstances: []*config.ProviderInstanceConfig{instance}}
	saveCatalogFixture(t, instance, "org/model")
	if err := auth.SetCredential("fixture-auth", &auth.AuthCredential{AccessToken: "fixture-secret", AuthMethod: "api_key"}); err != nil {
		t.Fatal(err)
	}

	resolved, err := NewResolver().Resolve(cfg, "fixture/org/model")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if len(resolved.Candidates) != 1 || resolved.Candidates[0].DisplayName != "fixture/org/model" {
		t.Fatalf("candidates = %#v", resolved.Candidates)
	}
	provider, err := resolved.ProviderForCandidate(resolved.Candidates[0])
	if err != nil || provider == nil {
		t.Fatalf("ProviderForCandidate() = %v, %v; want the instance's provider", provider, err)
	}
}

func TestResolverSeesACatalogSavedAfterItWasCreated(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	instance := resolverInstance("late")
	cfg := &config.Config{ProviderInstances: []*config.ProviderInstanceConfig{instance}}
	resolver := NewResolver()

	if _, err := resolver.Resolve(cfg, "late/model"); err == nil || !strings.Contains(err.Error(), "catalog") {
		t.Fatalf("Resolve() before the catalog sync error = %v, want the missing catalog", err)
	}
	saveCatalogFixture(t, instance, "model")
	if _, err := resolver.Resolve(cfg, "late/model"); err != nil {
		t.Fatalf("Resolve() after the catalog sync error = %v", err)
	}
}

func TestResolverResolvesRoutesInOrder(t *testing.T) {
	first, second := resolverInstance("first"), resolverInstance("second")
	cfg := &config.Config{
		ProviderInstances: []*config.ProviderInstanceConfig{first, second},
		ModelRoutes:       []*config.ModelRouteConfig{{Name: "chat", Targets: []string{"second/m", "first/m"}}},
	}
	resolver := NewResolver(WithCatalogLoader(func(*config.Config) (map[string]providers.InstanceCatalog, error) {
		return map[string]providers.InstanceCatalog{
			"first":  {InstanceID: "first", Models: []string{"m"}},
			"second": {InstanceID: "second", Models: []string{"m"}},
		}, nil
	}))
	resolved, err := resolver.Resolve(cfg, "chat")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	var order []string
	for _, candidate := range resolved.Candidates {
		order = append(order, candidate.DisplayName)
	}
	if !slices.Equal(order, []string{"second/m", "first/m"}) {
		t.Fatalf("candidates = %v, want the route's order", order)
	}
}

func TestResolverOptionsReplaceDefaults(t *testing.T) {
	instance := resolverInstance("owned")
	cfg := &config.Config{ProviderInstances: []*config.ProviderInstanceConfig{instance}}
	var gotRef, gotSecret, gotModel string
	resolver := NewResolver(
		WithCatalogLoader(func(got *config.Config) (map[string]providers.InstanceCatalog, error) {
			if got != cfg {
				t.Fatal("catalog loader did not receive the given config")
			}
			return map[string]providers.InstanceCatalog{"owned": {InstanceID: "owned", Models: []string{"m"}}}, nil
		}),
		WithCredentialResolver(func(ref string) (string, error) {
			gotRef = ref
			return "stub-secret", nil
		}),
		WithProviderFactory(func(instance *config.ProviderInstanceConfig, modelID, secret string) (providers.LLMProvider, error) {
			gotModel, gotSecret = modelID, secret
			return stubProvider{}, nil
		}),
		// Nil options keep the defaults already replaced above.
		WithCatalogLoader(nil), WithCredentialResolver(nil), WithProviderFactory(nil), nil,
	)
	resolved, err := resolver.Resolve(cfg, "owned/m")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if _, err := resolved.ProviderForCandidate(resolved.Candidates[0]); err != nil {
		t.Fatalf("ProviderForCandidate() error = %v", err)
	}
	if gotRef != "credential:owned-auth" || gotSecret != "stub-secret" || gotModel != "m" {
		t.Fatalf("ref=%q secret=%q model=%q", gotRef, gotSecret, gotModel)
	}
}

type stubProvider struct{ providers.LLMProvider }

func TestResolverResolveReportsCatalogLoadFailure(t *testing.T) {
	resolver := NewResolver(WithCatalogLoader(func(*config.Config) (map[string]providers.InstanceCatalog, error) {
		return nil, errors.New("disk on fire")
	}))
	cfg := &config.Config{ProviderInstances: []*config.ProviderInstanceConfig{resolverInstance("x")}}
	if _, err := resolver.Resolve(cfg, "x/m"); err == nil || !strings.Contains(err.Error(), "disk on fire") {
		t.Fatalf("Resolve() error = %v", err)
	}
	if err := resolver.Check(cfg, "x/m"); err == nil || !strings.Contains(err.Error(), "disk on fire") {
		t.Fatalf("Check() error = %v", err)
	}
	if _, err := resolver.Resolve(nil, "x/m"); err == nil {
		t.Fatal("Resolve(nil config) error = nil")
	}
}

// TestResolverCheckNeverResolvesCredentialsOrBuildsProviders pins that
// validating a selection when it is set touches neither secrets nor
// providers.
func TestResolverCheckNeverResolvesCredentialsOrBuildsProviders(t *testing.T) {
	enabled := resolverInstance("enabled")
	disabled := resolverInstance("disabled")
	disabled.State = config.ProviderInstanceStateDisabled
	cfg := &config.Config{
		ProviderInstances: []*config.ProviderInstanceConfig{enabled, disabled},
		ModelRoutes:       []*config.ModelRouteConfig{{Name: "chat", Targets: []string{"enabled/m"}}},
	}
	resolver := NewResolver(
		WithCatalogLoader(func(*config.Config) (map[string]providers.InstanceCatalog, error) {
			return map[string]providers.InstanceCatalog{
				"enabled":  {InstanceID: "enabled", Models: []string{"m"}},
				"disabled": {InstanceID: "disabled", Models: []string{"m"}},
			}, nil
		}),
		WithCredentialResolver(func(string) (string, error) {
			t.Fatal("Check resolved a credential")
			return "", nil
		}),
		WithProviderFactory(func(*config.ProviderInstanceConfig, string, string) (providers.LLMProvider, error) {
			t.Fatal("Check built a provider")
			return nil, nil
		}),
	)

	for _, selection := range []string{"", "  ", "enabled/m", "chat"} {
		if err := resolver.Check(cfg, selection); err != nil {
			t.Errorf("Check(%q) error = %v", selection, err)
		}
	}
	for selection, wantErr := range map[string]string{
		"Bad Selection": "must be an exact target",
		"missing/m":     `provider instance "missing" not found`,
		"disabled/m":    "is disabled",
		"enabled/other": "not found in provider instance",
		"no-such-route": "route was not found",
	} {
		if err := resolver.Check(cfg, selection); err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Errorf("Check(%q) error = %v, want containing %q", selection, err, wantErr)
		}
	}
}

func TestInstanceCatalogsKeepOnlyValidEntries(t *testing.T) {
	owned := resolverInstance("owned")
	recreated := resolverInstance("recreated")
	recreated.ProviderKind = "anthropic"
	cfg := &config.Config{ProviderInstances: []*config.ProviderInstanceConfig{owned, recreated, nil}}
	store := &CatalogStore{Entries: map[string]*CatalogEntry{
		"owned":                                {ID: "owned", InstanceID: "owned", Provider: "openai", Models: []CatalogModel{{ID: " model-a "}, {ID: ""}, {ID: "model-b"}}},
		"recreated":                            {ID: "recreated", InstanceID: "recreated", Provider: "openai", Models: []CatalogModel{{ID: "stale"}}},
		"orphan":                               {ID: "orphan", InstanceID: "orphan", Provider: "openai", Models: []CatalogModel{{ID: "x"}}},
		"openai|https://api.openai.com/v1|abc": {ID: "openai|https://api.openai.com/v1|abc", Provider: "openai"},
	}}

	catalogs := InstanceCatalogs(cfg, store)
	if len(catalogs) != 1 {
		t.Fatalf("catalogs = %#v, want only the owned instance's", catalogs)
	}
	if got := catalogs["owned"]; got.InstanceID != "owned" || !slices.Equal(got.Models, []string{"model-a", "model-b"}) {
		t.Fatalf("owned catalog = %#v", got)
	}
	if !ValidInstanceCatalog("owned", store.Entries["owned"], owned) || ValidInstanceCatalog("recreated", store.Entries["recreated"], recreated) ||
		ValidInstanceCatalog("owned", nil, owned) || ValidInstanceCatalog("owned", store.Entries["owned"], nil) {
		t.Fatal("ValidInstanceCatalog() disagrees with the catalog ownership rule")
	}
	if got := InstanceCatalogs(nil, store); len(got) != 0 {
		t.Fatalf("InstanceCatalogs(nil config) = %#v", got)
	}
}

func TestResolveCredentialReference(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	if err := auth.SetCredential("present", &auth.AuthCredential{AccessToken: " sk-present "}); err != nil {
		t.Fatal(err)
	}
	if err := auth.SetCredential("expired", &auth.AuthCredential{AccessToken: "sk-old", ExpiresAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := auth.SetCredential("empty", &auth.AuthCredential{AccessToken: "  "}); err != nil {
		t.Fatal(err)
	}

	if got, err := ResolveCredentialReference(" credential:present "); err != nil || got != "sk-present" {
		t.Fatalf("ResolveCredentialReference(present) = %q, %v", got, err)
	}
	for ref, wantErr := range map[string]string{
		"":                   "credential:<store-key>",
		"present":            "credential:<store-key>",
		"env:OPENAI_API_KEY": "credential:<store-key>",
		"credential:":        "credential:<store-key>",
		"credential:missing": "has no access token",
		"credential:empty":   "has no access token",
		"credential:expired": "is expired",
	} {
		if _, err := ResolveCredentialReference(ref); err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Errorf("ResolveCredentialReference(%q) error = %v, want containing %q", ref, err, wantErr)
		}
	}
}
