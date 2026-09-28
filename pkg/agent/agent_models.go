package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/xibodev/compa/pkg/commands"
	"github.com/xibodev/compa/pkg/config"
	"github.com/xibodev/compa/pkg/logger"
	"github.com/xibodev/compa/pkg/providers"
)

// modelResolver returns the resolver the loop resolves selections with.
func (al *AgentLoop) modelResolver() ModelResolver {
	if al == nil || al.resolveModel == nil {
		return defaultModelResolver()
	}
	return al.resolveModel
}

// currentRateLimits returns the limiter registration of the current fallback
// chain.
func (al *AgentLoop) currentRateLimits() *candidateRateLimits {
	al.mu.RLock()
	defer al.mu.RUnlock()
	return al.rateLimits
}

// registerRateLimits registers the RPM limits of candidates the loop is about
// to run on.
func (al *AgentLoop) registerRateLimits(candidates []providers.FallbackCandidate) {
	al.currentRateLimits().register(candidates)
}

// resolveTurnSelection resolves a selection one turn runs on — a message's
// own selection or a hook's rewrite — against the loop's current config.
// The turn builds, and closes, the resolution's providers.
func (al *AgentLoop) resolveTurnSelection(selection string) (*providers.InstanceResolution, error) {
	selection = strings.TrimSpace(selection)
	if selection == "" {
		return nil, errNoSelection
	}
	if err := config.ValidateSelectionSyntax(selection); err != nil {
		return nil, err
	}
	resolution, err := resolveSelection(al.GetConfig(), al.modelResolver(), selection)
	if err != nil {
		return nil, err
	}
	al.registerRateLimits(resolution.Candidates)
	return resolution, nil
}

// switchModel makes selection agent's model until the next reload: /switch
// model. The selection must resolve; the agent's previous model stays when
// it does not.
func (al *AgentLoop) switchModel(agent *AgentInstance, value string) (string, error) {
	if agent == nil {
		return "", errors.New("no agent to switch")
	}
	selection := strings.TrimSpace(value)
	if selection == "" {
		return "", errors.New("a model is required: an exact target instance-id/model-id or a model route name")
	}
	if err := config.ValidateSelectionSyntax(selection); err != nil {
		return "", err
	}

	al.mu.RLock()
	cfg, injected := al.cfg, al.provider
	al.mu.RUnlock()
	models, err := modelsForSelection(cfg, injected, al.modelResolver(), selection)
	if err != nil {
		return "", fmt.Errorf("model %q is not available: %w", selection, err)
	}

	modelMu := agent.modelStateMutex()
	modelMu.Lock()
	previous := agent.Model
	agent.setModels(models)
	modelMu.Unlock()

	al.registerRateLimits(models.candidates)
	logger.InfoCF("agent", "Switched agent model", map[string]any{
		"agent_id": agent.ID,
		"from":     previous,
		"to":       selection,
	})
	return previous, nil
}

// DefaultModelStatus reports the default agent's model selection and, when
// the agent has no model to run on, why: every turn without a per-message
// selection then fails with that error.
func (al *AgentLoop) DefaultModelStatus() (string, error) {
	agent := al.GetRegistry().GetDefaultAgent()
	if agent == nil {
		return "", errors.New("no default agent")
	}
	modelMu := agent.modelStateMutex()
	modelMu.RLock()
	defer modelMu.RUnlock()
	if agent.hasModel() {
		return agent.Model, nil
	}
	return agent.Model, agent.noModelError()
}

// modelInfo describes the agent's model for /show model and /list models.
func (a *AgentInstance) modelInfo() commands.ModelInfo {
	modelMu := a.modelStateMutex()
	modelMu.RLock()
	defer modelMu.RUnlock()

	info := commands.ModelInfo{
		Selection:    a.Model,
		ServedTarget: a.status.servedTarget(),
	}
	if info.Selection == "" && len(a.Candidates) > 0 {
		// An injected provider serving an empty default selection.
		info.Selection = a.Candidates[0].DisplayName
	}
	if !a.hasModel() && info.Selection != "" {
		info.Unavailable = a.noModelError().Error()
		if cause := errors.Unwrap(a.noModelError()); cause != nil {
			info.Unavailable = cause.Error()
		}
	}
	return info
}

// primaryModelProvider runs background work — summaries, seahorse
// compaction and evolution's clustering, drafting and judging — on an
// agent's first target as it is when the work runs, so the work follows
// /switch model. The model its callers pass is ignored: they cannot know the
// target's model ID. Without a model its default model is empty, which
// background work treats as having no LLM, and its calls fail with the
// no-model error.
type primaryModelProvider struct {
	agent func() *AgentInstance
}

// registryModelProvider serves the default agent of registry.
func registryModelProvider(registry *AgentRegistry) providers.LLMProvider {
	return primaryModelProvider{agent: func() *AgentInstance {
		if registry == nil {
			return nil
		}
		return registry.GetDefaultAgent()
	}}
}

func (p primaryModelProvider) current() (providers.LLMProvider, string, error) {
	var agent *AgentInstance
	if p.agent != nil {
		agent = p.agent()
	}
	if agent == nil {
		return nil, "", errors.New("no default agent")
	}
	return agent.primaryModel()
}

func (p primaryModelProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	_ string,
	options map[string]any,
) (*providers.LLMResponse, error) {
	provider, model, err := p.current()
	if err != nil {
		logger.InfoCF("agent", "Background model call skipped: no model", map[string]any{"reason": err.Error()})
		return nil, err
	}
	return provider.Chat(ctx, messages, tools, model, options)
}

func (p primaryModelProvider) GetDefaultModel() string {
	_, model, err := p.current()
	if err != nil {
		logger.DebugCF("agent", "Background work has no model", map[string]any{"reason": err.Error()})
		return ""
	}
	return model
}
