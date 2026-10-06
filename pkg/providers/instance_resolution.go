package providers

import (
	"fmt"
	"strings"
	"sync"

	"github.com/xibodev/compa/v2/pkg/config"
)

// InstanceCatalog is the model identity snapshot owned by one configured
// provider instance. Callers load this from their catalog persistence layer.
type InstanceCatalog struct {
	InstanceID string
	Models     []string
}

// InstanceProviderFactory creates a runtime provider from one exact target's
// owning instance. secret is resolved from that instance's auth reference.
type InstanceProviderFactory func(
	instance *config.ProviderInstanceConfig,
	modelID string,
	secret string,
) (LLMProvider, error)

// InstanceCredentialResolver resolves an instance auth connection reference.
type InstanceCredentialResolver func(ref string) (string, error)

// InstanceResolution contains ordered fallback candidates and their exact
// instance-owned providers, keyed by FallbackCandidate.StableKey().
type InstanceResolution struct {
	Candidates        []FallbackCandidate
	bindings          map[string]instanceTargetBinding
	resolveCredential InstanceCredentialResolver
	createProvider    InstanceProviderFactory
	mu                sync.Mutex
	providers         map[string]LLMProvider
}

type instanceTargetBinding struct {
	instance *config.ProviderInstanceConfig
	modelID  string
}

func (r *InstanceResolution) ProviderForCandidate(candidate FallbackCandidate) (LLMProvider, error) {
	if r == nil {
		return nil, fmt.Errorf("instance resolution is required")
	}
	key := candidate.StableKey()
	r.mu.Lock()
	defer r.mu.Unlock()
	if provider := r.providers[key]; provider != nil {
		return provider, nil
	}
	binding, ok := r.bindings[key]
	if !ok {
		return nil, fmt.Errorf("instance binding missing for %s", key)
	}
	var secret string
	if ref := strings.TrimSpace(binding.instance.AuthConnectionRef); ref != "" {
		if r.resolveCredential == nil {
			return nil, fmt.Errorf("credential resolver is required")
		}
		var err error
		secret, err = r.resolveCredential(ref)
		if err != nil {
			return nil, fmt.Errorf("resolve provider instance credential: %w", err)
		}
	}
	provider, err := r.createProvider(cloneProviderInstance(binding.instance), binding.modelID, secret)
	if err != nil {
		return nil, err
	}
	r.providers[key] = provider
	return provider, nil
}

// CallSpecForCandidate returns the call spec of candidate: its model on its
// own instance, with the instance's runtime settings but no credential.
func (r *InstanceResolution) CallSpecForCandidate(candidate FallbackCandidate) (*CallSpec, error) {
	if r == nil {
		return nil, fmt.Errorf("instance resolution is required")
	}
	binding, ok := r.bindings[candidate.StableKey()]
	if !ok {
		return nil, fmt.Errorf("instance binding missing for %s", candidate.StableKey())
	}
	return callSpecFromInstance(binding.instance, binding.modelID), nil
}

// ResolveInstanceTargetOrRoute resolves either an exact instance-id/model-id
// target or a named route. Every target runs on its own instance's endpoint,
// credential and runtime settings. Resolution checks each target's instance
// and catalog but neither resolves credentials nor creates providers; those
// happen on the first ProviderForCandidate call for a candidate.
func ResolveInstanceTargetOrRoute(
	cfg *config.Config,
	catalogs map[string]InstanceCatalog,
	selection string,
	resolveCredential InstanceCredentialResolver,
	createProvider InstanceProviderFactory,
) (*InstanceResolution, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	selection = strings.TrimSpace(selection)
	if selection == "" {
		return nil, fmt.Errorf("target or route is required")
	}
	if createProvider == nil {
		createProvider = CreateProviderFromInstance
	}

	targets := []string{selection}
	if _, err := config.ParseExactModelTarget(selection); err != nil {
		route := findModelRoute(cfg.ModelRoutes, selection)
		if route == nil {
			return nil, fmt.Errorf("target %q is invalid and route was not found", selection)
		}
		targets = route.Targets
	}

	instances := make(map[string]*config.ProviderInstanceConfig, len(cfg.ProviderInstances))
	for _, instance := range cfg.ProviderInstances {
		if instance != nil {
			instances[instance.ID] = instance
		}
	}
	resolution := &InstanceResolution{
		Candidates:        make([]FallbackCandidate, 0, len(targets)),
		bindings:          make(map[string]instanceTargetBinding, len(targets)),
		providers:         make(map[string]LLMProvider, len(targets)),
		resolveCredential: resolveCredential,
		createProvider:    createProvider,
	}
	for index, raw := range targets {
		target, err := config.ParseExactModelTarget(raw)
		if err != nil {
			return nil, fmt.Errorf("target[%d]: %w", index, err)
		}
		instance := instances[target.InstanceID]
		if instance == nil {
			return nil, fmt.Errorf("target[%d]: provider instance %q not found", index, target.InstanceID)
		}
		if instance.State != config.ProviderInstanceStateEnabled {
			return nil, fmt.Errorf("target[%d]: provider instance %q is disabled", index, target.InstanceID)
		}
		catalog, ok := catalogs[target.InstanceID]
		if !ok || catalog.InstanceID != target.InstanceID {
			return nil, fmt.Errorf("target[%d]: catalog for provider instance %q not found", index, target.InstanceID)
		}
		if !catalogContainsModel(catalog, target.ModelID) {
			return nil, fmt.Errorf("target[%d]: model %q not found in provider instance %q catalog", index, target.ModelID, target.InstanceID)
		}

		candidate := FallbackCandidate{
			Provider:    strings.TrimSpace(instance.Protocol),
			Model:       target.ModelID,
			DisplayName: raw,
			IdentityKey: "provider_instance:" + target.InstanceID,
			ConfigKey:   "instance_target:" + target.String(),
		}
		if instance.Runtime != nil {
			candidate.RPM = instance.Runtime.RPM
		}
		resolution.Candidates = append(resolution.Candidates, candidate)
		resolution.bindings[candidate.StableKey()] = instanceTargetBinding{
			instance: cloneProviderInstance(instance), modelID: target.ModelID,
		}
	}
	return resolution, nil
}

func findModelRoute(routes []*config.ModelRouteConfig, name string) *config.ModelRouteConfig {
	for _, route := range routes {
		if route != nil && route.Name == name {
			return route
		}
	}
	return nil
}

func catalogContainsModel(catalog InstanceCatalog, modelID string) bool {
	for _, candidate := range catalog.Models {
		if candidate == modelID {
			return true
		}
	}
	return false
}

func cloneProviderInstance(instance *config.ProviderInstanceConfig) *config.ProviderInstanceConfig {
	clone := *instance
	clone.Headers = cloneStringMap(instance.Headers)
	clone.Settings = cloneAnyMap(instance.Settings)
	if instance.Runtime != nil {
		runtime := *instance.Runtime
		if instance.Runtime.Streaming != nil {
			streaming := *instance.Runtime.Streaming
			runtime.Streaming = &streaming
		}
		runtime.ExtraBody = cloneAnyMap(instance.Runtime.ExtraBody)
		clone.Runtime = &runtime
	}
	return &clone
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	clone := make(map[string]string, len(source))
	for name, value := range source {
		clone[name] = value
	}
	return clone
}

func cloneAnyMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	clone := make(map[string]any, len(source))
	for name, value := range source {
		clone[name] = value
	}
	return clone
}
