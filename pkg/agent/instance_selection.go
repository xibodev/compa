package agent

import (
	"context"
	"fmt"

	"github.com/xibodev/compa/pkg/providers"
)

type InstanceSelectionResult struct {
	Response       *providers.LLMResponse
	ServedIdentity string
	ExactTarget    string
}

// ProcessInstanceSelection invokes one explicit instance target or named
// route with the given messages, outside any turn or session: the selection
// resolves against the loop's current config, its targets run in order
// through core's candidate executor (providers.Failover), and a streaming
// target that fails after visible output does not fall over.
func (al *AgentLoop) ProcessInstanceSelection(
	ctx context.Context,
	selection string,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	options map[string]any,
	onChunk func(string),
) (*InstanceSelectionResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resolved, err := al.resolveTurnSelection(selection)
	if err != nil {
		return nil, err
	}
	owned := make([]providers.LLMProvider, 0, len(resolved.Candidates))
	defer func() { closeUniqueStatefulProviders(owned...) }()

	result, err := al.currentFailover().Execute(ctx, resolved.Candidates, func(
		ctx context.Context,
		candidate providers.FallbackCandidate,
	) (*providers.LLMResponse, error) {
		provider, providerErr := resolved.ProviderForCandidate(candidate)
		if providerErr != nil {
			return nil, providerErr
		}
		if provider == nil {
			return nil, fmt.Errorf("provider missing for %s", candidate.StableKey())
		}
		owned = append(owned, provider)
		if onChunk == nil {
			return provider.Chat(ctx, messages, tools, candidate.Model, options)
		}
		streaming, ok := provider.(providers.StreamingProvider)
		if !ok {
			return provider.Chat(ctx, messages, tools, candidate.Model, options)
		}
		visible := false
		response, streamErr := streaming.ChatStream(ctx, messages, tools, candidate.Model, options, func(chunk string) {
			if chunk != "" {
				visible = true
			}
			onChunk(chunk)
		})
		if streamErr != nil && visible {
			return nil, afterOutputError(streamErr)
		}
		return response, streamErr
	})
	if err != nil {
		return nil, err
	}
	return &InstanceSelectionResult{
		Response:       result.Value,
		ServedIdentity: result.Candidate.StableKey(),
		ExactTarget:    result.Candidate.DisplayName,
	}, nil
}
