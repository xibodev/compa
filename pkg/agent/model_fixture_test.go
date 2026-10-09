package agent

import (
	"fmt"
	"sort"
	"testing"

	"github.com/xibodev/compa/v4/pkg/config"
	"github.com/xibodev/compa/v4/pkg/modelservice"
	"github.com/xibodev/compa/v4/pkg/providers"
)

// testInstance is one provider instance of a test: its runtime settings and
// the provider each of its models runs on.
type testInstance struct {
	id      string
	runtime *config.ProviderInstanceRuntime
	models  map[string]providers.LLMProvider
}

// oneModel is an instance serving a single model.
func oneModel(instanceID, modelID string, provider providers.LLMProvider) testInstance {
	return testInstance{id: instanceID, models: map[string]providers.LLMProvider{modelID: provider}}
}

// withRuntime returns inst with runtime settings.
func (inst testInstance) withRuntime(runtime config.ProviderInstanceRuntime) testInstance {
	inst.runtime = &runtime
	return inst
}

// testModelResolver adds instances to cfg and returns the resolver a loop
// over cfg resolves selections with: the production resolver over static
// catalogs of the instances' models, building each target's provider from
// the table. The providers it builds are counted per target in created, when
// not nil.
func testModelResolver(cfg *config.Config, created map[string]int, instances ...testInstance) ModelResolver {
	catalogs := make(map[string]providers.InstanceCatalog, len(instances))
	targets := make(map[string]providers.LLMProvider)
	for _, inst := range instances {
		cfg.ProviderInstances = append(cfg.ProviderInstances, &config.ProviderInstanceConfig{
			ID:           inst.id,
			ProviderKind: "openai",
			Adapter:      config.ProviderAdapterOpenAICompatible,
			Protocol:     "openai",
			Endpoint:     "http://127.0.0.1:1/v1",
			Runtime:      inst.runtime,
			State:        config.ProviderInstanceStateEnabled,
		})
		catalog := providers.InstanceCatalog{InstanceID: inst.id}
		for modelID, provider := range inst.models {
			catalog.Models = append(catalog.Models, modelID)
			targets[inst.id+"/"+modelID] = provider
		}
		sort.Strings(catalog.Models)
		catalogs[inst.id] = catalog
	}
	return modelservice.NewResolver(
		modelservice.WithCatalogLoader(func(*config.Config) (map[string]providers.InstanceCatalog, error) {
			return catalogs, nil
		}),
		modelservice.WithCredentialResolver(func(string) (string, error) { return "", nil }),
		modelservice.WithProviderFactory(func(
			instance *config.ProviderInstanceConfig,
			modelID string,
			_ string,
		) (providers.LLMProvider, error) {
			target := instance.ID + "/" + modelID
			provider := targets[target]
			if provider == nil {
				return nil, fmt.Errorf("no test provider for %s", target)
			}
			if created != nil {
				created[target]++
			}
			return provider, nil
		}),
	).Resolve
}

// addTestRoute adds a model route over targets to cfg.
func addTestRoute(cfg *config.Config, name string, targets ...string) {
	cfg.ModelRoutes = append(cfg.ModelRoutes, &config.ModelRouteConfig{Name: name, Targets: targets})
}

// addHTTPInstance adds an OpenAI-compatible provider instance reaching
// endpoint, whose credential reference resolves to a test key.
func addHTTPInstance(cfg *config.Config, id, endpoint string, runtime *config.ProviderInstanceRuntime) {
	cfg.ProviderInstances = append(cfg.ProviderInstances, &config.ProviderInstanceConfig{
		ID:                id,
		ProviderKind:      "openai",
		Adapter:           config.ProviderAdapterOpenAICompatible,
		Protocol:          "openai",
		Endpoint:          endpoint,
		AuthConnectionRef: "credential:" + id,
		Runtime:           runtime,
		State:             config.ProviderInstanceStateEnabled,
	})
}

// httpModelResolver resolves against static catalogs (models by instance ID)
// and builds each target's real provider from its instance, as production
// does.
func httpModelResolver(catalogs map[string][]string) ModelResolver {
	return modelservice.NewResolver(
		modelservice.WithCatalogLoader(func(*config.Config) (map[string]providers.InstanceCatalog, error) {
			loaded := make(map[string]providers.InstanceCatalog, len(catalogs))
			for id, models := range catalogs {
				loaded[id] = providers.InstanceCatalog{InstanceID: id, Models: models}
			}
			return loaded, nil
		}),
		modelservice.WithCredentialResolver(func(ref string) (string, error) { return "test-key-" + ref, nil }),
	).Resolve
}

// newModelTestConfig returns a loop config whose default model is selection.
func newModelTestConfig(t *testing.T, selection string) *config.Config {
	t.Helper()
	return &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         t.TempDir(),
				ModelName:         selection,
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
	}
}
