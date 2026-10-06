package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	core "github.com/xibodev/llmgw-core"

	"github.com/xibodev/compa/v2/pkg/config"
	"github.com/xibodev/compa/v2/pkg/modelservice"
	"github.com/xibodev/compa/v2/pkg/providers"
)

func TestResolveModels_BuildsEachTargetWithItsInstanceRuntime(t *testing.T) {
	cfg := newModelTestConfig(t, "chain")
	primary := &mockProvider{}
	backup := &mockProvider{}
	resolve := testModelResolver(cfg, nil,
		oneModel("primary", "gpt-4o", primary).withRuntime(config.ProviderInstanceRuntime{
			ThinkingLevel: "high", Streaming: boolPtr(true), RPM: 7,
		}),
		oneModel("backup", "claude", backup).withRuntime(config.ProviderInstanceRuntime{ThinkingLevel: "off", Streaming: boolPtr(false)}),
	)
	addTestRoute(cfg, "chain", "primary/gpt-4o", "backup/claude")

	models, err := resolveModels(cfg, resolve, "chain")
	if err != nil {
		t.Fatalf("resolveModels() error = %v", err)
	}
	if len(models.candidates) != 2 ||
		models.candidates[0].DisplayName != "primary/gpt-4o" || models.candidates[0].Model != "gpt-4o" ||
		models.candidates[1].DisplayName != "backup/claude" || models.candidates[1].Model != "claude" {
		t.Fatalf("candidates = %#v, want the route's targets in order", models.candidates)
	}
	if models.candidates[0].RPM != 7 {
		t.Fatalf("primary RPM = %d, want the instance runtime's 7", models.candidates[0].RPM)
	}
	first, second := models.candidates[0].StableKey(), models.candidates[1].StableKey()
	if models.providers[first] != primary || models.providers[second] != backup {
		t.Fatalf("providers = %#v, want each target on its own instance's provider", models.providers)
	}
	if cfg := models.configs[first]; cfg.ThinkingLevel != "high" || !cfg.Streaming || cfg.RPM != 7 {
		t.Fatalf("primary request spec = %#v, want its instance runtime", cfg)
	}
	if cfg := models.configs[second]; cfg.ThinkingLevel != "off" || cfg.Streaming {
		t.Fatalf("backup request spec = %#v, want its own instance runtime", cfg)
	}
}

func TestResolveModels_TargetWithoutProviderFailsOver(t *testing.T) {
	cfg := newModelTestConfig(t, "chain")
	resolve := modelservice.NewResolver(
		modelservice.WithCatalogLoader(func(*config.Config) (map[string]providers.InstanceCatalog, error) {
			return map[string]providers.InstanceCatalog{
				"broken": {InstanceID: "broken", Models: []string{"model"}},
			}, nil
		}),
		modelservice.WithProviderFactory(func(*config.ProviderInstanceConfig, string, string) (providers.LLMProvider, error) {
			return nil, errors.New("credential missing")
		}),
	).Resolve
	cfg.ProviderInstances = []*config.ProviderInstanceConfig{{
		ID: "broken", ProviderKind: "openai", Adapter: config.ProviderAdapterOpenAICompatible,
		Protocol: "openai", State: config.ProviderInstanceStateEnabled,
	}}

	models, err := resolveModels(cfg, resolve, "broken/model")
	if err != nil {
		t.Fatalf("resolveModels() error = %v", err)
	}
	provider := models.providers[models.candidates[0].StableKey()]
	_, err = provider.Chat(context.Background(), nil, nil, "model", nil)
	if err == nil || !strings.Contains(err.Error(), "credential missing") {
		t.Fatalf("unavailable provider error = %v, want the build error", err)
	}
	if failure := providers.DescribeFailure(err); failure.Disposition != core.DispositionFailover || failure.Class != core.ProviderErrorConfiguration {
		t.Fatalf("failure = %+v, want a configuration error that fails over", failure)
	}
}

func TestResolveModels_RejectsEmptyAndUnknownSelections(t *testing.T) {
	cfg := newModelTestConfig(t, "")
	resolve := testModelResolver(cfg, nil, oneModel("main", "model", &mockProvider{}))

	if _, err := resolveModels(cfg, resolve, " "); !errors.Is(err, errNoSelection) {
		t.Fatalf("empty selection error = %v, want errNoSelection", err)
	}
	if _, err := resolveModels(cfg, resolve, "missing-route"); err == nil ||
		!strings.Contains(err.Error(), "route was not found") {
		t.Fatalf("unknown route error = %v", err)
	}
	if _, err := resolveModels(cfg, resolve, "main/other"); err == nil ||
		!strings.Contains(err.Error(), `model "other" not found`) {
		t.Fatalf("unknown target error = %v", err)
	}
}

func TestInjectedModels_ServeTheSelectionOnTheInjectedProvider(t *testing.T) {
	provider := &mockProvider{}
	for _, tt := range []struct {
		selection   string
		wantDisplay string
		wantModel   string
	}{
		{selection: "openai-main/gpt-4o", wantDisplay: "openai-main/gpt-4o", wantModel: "gpt-4o"},
		{selection: "test-model", wantDisplay: "test-model", wantModel: "test-model"},
		{selection: "", wantDisplay: "default", wantModel: "mock-model"},
	} {
		models := injectedModels(tt.selection, provider)
		if len(models.candidates) != 1 {
			t.Fatalf("%q: candidates = %#v, want one", tt.selection, models.candidates)
		}
		candidate := models.candidates[0]
		if candidate.DisplayName != tt.wantDisplay || candidate.Model != tt.wantModel {
			t.Fatalf("%q: candidate = %#v, want %s calling %s", tt.selection, candidate, tt.wantDisplay, tt.wantModel)
		}
		if models.providers[candidate.StableKey()] != provider {
			t.Fatalf("%q: candidate does not run on the injected provider", tt.selection)
		}
	}
}

func TestNoModelErrorMessages(t *testing.T) {
	if got := (&noModelError{}).Error(); got != noModelSelectedMessage {
		t.Fatalf("empty selection message = %q", got)
	}
	cause := errors.New(`provider instance "gone" not found`)
	err := &noModelError{selection: "gone/model", cause: cause}
	if !strings.Contains(err.Error(), `Model "gone/model" is not available`) ||
		!strings.Contains(err.Error(), cause.Error()) ||
		!strings.Contains(err.Error(), "Connect a provider under Models") {
		t.Fatalf("unresolved selection message = %q", err.Error())
	}
	if !errors.Is(err, cause) {
		t.Fatal("noModelError does not unwrap to its cause")
	}
}

func TestCandidateRateLimits_RegisterOncePerInstanceAndRPM(t *testing.T) {
	limits := newCandidateRateLimits()
	candidate := providers.FallbackCandidate{
		Model: "model", DisplayName: "main/model", RPM: 1,
		IdentityKey: "provider_instance:main", ConfigKey: "instance_target:main/model",
	}
	key := candidate.InstanceKey()

	limits.register([]providers.FallbackCandidate{candidate})
	if !limits.registry.TryAcquire(key) {
		t.Fatal("first request was limited")
	}
	if limits.registry.TryAcquire(key) {
		t.Fatal("second request within the minute was not limited")
	}

	// Registering the same candidate again (a new turn's resolution) must not
	// refill its bucket.
	limits.register([]providers.FallbackCandidate{candidate})
	if limits.registry.TryAcquire(key) {
		t.Fatal("re-registering the same RPM refilled the limiter")
	}

	// A changed RPM takes effect.
	candidate.RPM = 5
	limits.register([]providers.FallbackCandidate{candidate})
	if !limits.registry.TryAcquire(key) {
		t.Fatal("a changed RPM was not registered")
	}
}
