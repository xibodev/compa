package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	core "github.com/xibodev/llmgw-core"

	"github.com/xibodev/compa/v3/pkg/config"
	"github.com/xibodev/compa/v3/pkg/logger"
	"github.com/xibodev/compa/v3/pkg/modelservice"
	"github.com/xibodev/compa/v3/pkg/providers"
)

// ModelResolver resolves a model selection — an exact target
// "instance-id/model-id" or a model route name — against cfg into ordered
// candidates, each running on its own provider instance. The loop resolves
// every selection through it: the agents' models, the image and light
// models, /switch model, hook rewrites and per-message selections.
type ModelResolver func(cfg *config.Config, selection string) (*providers.InstanceResolution, error)

// defaultModelResolver resolves against the saved catalogs and the auth
// store, building each target's provider from its instance.
func defaultModelResolver() ModelResolver {
	return modelservice.NewResolver().Resolve
}

// noModelSelectedMessage tells the user a turn had no model to run on.
const noModelSelectedMessage = "No model selected. Connect a provider under Models and choose a default model."

// errNoSelection is the resolution error of an empty selection.
var errNoSelection = errors.New("no model selection is set")

// noModelError is the error of a turn with no model to run on: the agent's
// selection is empty or did not resolve, and the message selected none.
type noModelError struct {
	selection string
	cause     error
}

func (e *noModelError) Error() string {
	if e.selection == "" {
		return noModelSelectedMessage
	}
	return fmt.Sprintf(
		"Model %q is not available (%v). Connect a provider under Models and choose a default model.",
		e.selection, e.cause,
	)
}

func (e *noModelError) Unwrap() error { return e.cause }

// agentModels is what one selection resolved to: its targets in failover
// order, each with the provider and the request spec it runs with.
type agentModels struct {
	selection  string
	candidates []providers.FallbackCandidate
	providers  map[string]providers.LLMProvider // by FallbackCandidate.StableKey()
	configs    map[string]*providers.CallSpec   // by FallbackCandidate.StableKey()
}

// modelsForSelection resolves selection through resolve, or serves it on
// injected when an embedder supplied a provider for the default selection.
func modelsForSelection(
	cfg *config.Config,
	injected providers.LLMProvider,
	resolve ModelResolver,
	selection string,
) (agentModels, error) {
	selection = strings.TrimSpace(selection)
	if injected != nil && cfg != nil && selection == strings.TrimSpace(cfg.Agents.Defaults.GetModelName()) {
		return injectedModels(selection, injected), nil
	}
	return resolveModels(cfg, resolve, selection)
}

// resolveModels resolves selection and builds the provider of each of its
// targets. A target whose provider cannot be built (a missing credential, an
// unsupported adapter) stays in the list on a provider that fails over, so a
// route still reaches its other targets.
func resolveModels(cfg *config.Config, resolve ModelResolver, selection string) (agentModels, error) {
	selection = strings.TrimSpace(selection)
	if selection == "" {
		return agentModels{}, errNoSelection
	}
	resolution, err := resolveSelection(cfg, resolve, selection)
	if err != nil {
		return agentModels{}, err
	}
	models := agentModels{
		selection:  selection,
		candidates: append([]providers.FallbackCandidate(nil), resolution.Candidates...),
		providers:  make(map[string]providers.LLMProvider, len(resolution.Candidates)),
		configs:    make(map[string]*providers.CallSpec, len(resolution.Candidates)),
	}
	for _, candidate := range models.candidates {
		key := candidate.StableKey()
		models.configs[key] = candidateCallSpec(resolution, candidate)
		provider, providerErr := resolution.ProviderForCandidate(candidate)
		if providerErr == nil && provider == nil {
			providerErr = fmt.Errorf("provider instance built no provider for %s", candidate.DisplayName)
		}
		if providerErr != nil {
			logger.WarnCF("agent", "Model target unavailable; its turns fail over to the next target",
				map[string]any{
					"selection": selection,
					"target":    candidate.DisplayName,
					"error":     providerErr.Error(),
				})
			provider = &unavailableLLMProvider{model: candidate.Model, err: providerErr}
		}
		models.providers[key] = provider
	}
	return models, nil
}

// resolveSelection resolves selection and checks it named at least one
// target.
func resolveSelection(
	cfg *config.Config,
	resolve ModelResolver,
	selection string,
) (*providers.InstanceResolution, error) {
	if resolve == nil {
		return nil, errors.New("model resolution is not configured")
	}
	resolution, err := resolve(cfg, selection)
	if err != nil {
		return nil, err
	}
	if resolution == nil || len(resolution.Candidates) == 0 {
		return nil, fmt.Errorf("selection %q resolved no targets", selection)
	}
	return resolution, nil
}

// candidateCallSpec returns candidate's request spec; a resolution always
// has one for its own candidates, so the bare fallback never carries runtime
// settings.
func candidateCallSpec(
	resolution *providers.InstanceResolution,
	candidate providers.FallbackCandidate,
) *providers.CallSpec {
	if modelCfg, err := resolution.CallSpecForCandidate(candidate); err == nil && modelCfg != nil {
		return modelCfg
	}
	return &providers.CallSpec{
		DisplayName: candidate.DisplayName,
		ModelID:     candidate.Model,
	}
}

// injectedModels serves selection on a provider an embedder or test
// supplied: one candidate, named after the selection ("default" when there
// is none), calling the selection's model ID — the provider's own default
// model when the selection is empty.
func injectedModels(selection string, provider providers.LLMProvider) agentModels {
	display := selection
	if display == "" {
		display = "default"
	}
	model := provider.GetDefaultModel()
	if target, err := config.ParseExactModelTarget(selection); err == nil {
		model = target.ModelID
	} else if selection != "" {
		model = selection
	}
	candidate := providers.FallbackCandidate{
		Model:       model,
		DisplayName: display,
		IdentityKey: "injected_provider",
		ConfigKey:   "selection:" + display,
	}
	key := candidate.StableKey()
	return agentModels{
		selection:  selection,
		candidates: []providers.FallbackCandidate{candidate},
		providers:  map[string]providers.LLMProvider{key: provider},
		configs:    map[string]*providers.CallSpec{key: {DisplayName: display, ModelID: model}},
	}
}

// unavailableLLMProvider stands in for a target whose provider could not be
// built. Its calls fail with a core configuration error, which permits
// failover to the next candidate but says nothing about instance health.
type unavailableLLMProvider struct {
	model string
	err   error
}

func (p *unavailableLLMProvider) Chat(
	context.Context,
	[]providers.Message,
	[]providers.ToolDefinition,
	string,
	map[string]any,
) (*providers.LLMResponse, error) {
	return nil, core.NewConfigurationError(fmt.Sprintf("provider for model %q is unavailable: %v", p.model, p.err), p.err)
}

func (p *unavailableLLMProvider) GetDefaultModel() string { return p.model }

// candidateRateLimits registers the RPM limit of each instance the loop runs
// on with the failover's rate limiter, keyed by the candidate's instance.
// Registering a key again refills its bucket, so a key is registered again
// only when its RPM changes.
type candidateRateLimits struct {
	mu       sync.Mutex
	registry *providers.RateLimiterRegistry
	rpm      map[string]int
}

func newCandidateRateLimits() *candidateRateLimits {
	return &candidateRateLimits{
		registry: providers.NewRateLimiterRegistry(),
		rpm:      make(map[string]int),
	}
}

func (r *candidateRateLimits) register(candidates []providers.FallbackCandidate) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, candidate := range candidates {
		if candidate.RPM <= 0 {
			continue
		}
		key := candidate.InstanceKey()
		if r.rpm[key] == candidate.RPM {
			continue
		}
		r.rpm[key] = candidate.RPM
		r.registry.Register(key, candidate.RPM)
	}
}

// primaryModelID returns the model ID of the first candidate, or fallback.
func primaryModelID(candidates []providers.FallbackCandidate, fallback string) string {
	if len(candidates) > 0 && strings.TrimSpace(candidates[0].Model) != "" {
		return candidates[0].Model
	}
	return fallback
}

// candidateIn reports whether candidates holds candidate.
func candidateIn(candidates []providers.FallbackCandidate, candidate providers.FallbackCandidate) bool {
	key := candidate.StableKey()
	for _, c := range candidates {
		if c.StableKey() == key {
			return true
		}
	}
	return false
}
