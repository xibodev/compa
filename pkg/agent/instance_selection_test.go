package agent

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/xibodev/compa/pkg/config"
	"github.com/xibodev/compa/pkg/providers"
)

type instanceSelectionTestProvider struct {
	chatCalls   int
	streamCalls int
	response    *providers.LLMResponse
	err         error
	chunks      []string
}

func (p *instanceSelectionTestProvider) Chat(context.Context, []providers.Message, []providers.ToolDefinition, string, map[string]any) (*providers.LLMResponse, error) {
	p.chatCalls++
	return p.response, p.err
}

func (p *instanceSelectionTestProvider) ChatStream(_ context.Context, _ []providers.Message, _ []providers.ToolDefinition, _ string, _ map[string]any, onChunk func(string)) (*providers.LLMResponse, error) {
	p.streamCalls++
	for _, chunk := range p.chunks {
		onChunk(chunk)
	}
	return p.response, p.err
}

func (p *instanceSelectionTestProvider) GetDefaultModel() string { return "fixture" }

func newInstanceSelectionTestLoop(resolution *providers.InstanceResolution) *AgentLoop {
	return &AgentLoop{
		failover: providers.NewFailover(providers.NewRateLimiterRegistry()),
		resolveModel: func(*config.Config, string) (*providers.InstanceResolution, error) {
			return resolution, nil
		},
	}
}

func instanceSelectionResolution(candidates []providers.FallbackCandidate, providerList ...providers.LLMProvider) *providers.InstanceResolution {
	cfg := &config.Config{}
	catalogs := make(map[string]providers.InstanceCatalog, len(candidates))
	providerByInstance := make(map[string]providers.LLMProvider, len(candidates))
	for index, candidate := range candidates {
		instanceID := strings.TrimPrefix(candidate.IdentityKey, "provider_instance:")
		cfg.ProviderInstances = append(cfg.ProviderInstances, &config.ProviderInstanceConfig{
			ID: instanceID, ProviderKind: candidate.Provider, Adapter: "fixture",
			Protocol: candidate.Provider, State: config.ProviderInstanceStateEnabled,
		})
		catalogs[instanceID] = providers.InstanceCatalog{InstanceID: instanceID, Models: []string{candidate.Model}}
		providerByInstance[instanceID] = providerList[index]
	}
	selection := candidates[0].DisplayName
	if len(candidates) > 1 {
		selection = "fixture-route"
		cfg.ModelRoutes = []*config.ModelRouteConfig{{Name: selection}}
		for _, candidate := range candidates {
			cfg.ModelRoutes[0].Targets = append(cfg.ModelRoutes[0].Targets, candidate.DisplayName)
		}
	}
	resolved, err := providers.ResolveInstanceTargetOrRoute(
		cfg, catalogs, selection, nil,
		func(instance *config.ProviderInstanceConfig, _ string, _ string) (providers.LLMProvider, error) {
			return providerByInstance[instance.ID], nil
		},
	)
	if err != nil {
		panic(err)
	}
	return resolved
}

func TestProcessInstanceSelectionOrderedPreOutputFallback(t *testing.T) {
	primary := &instanceSelectionTestProvider{err: transientProviderError("fixture unavailable")}
	fallback := &instanceSelectionTestProvider{response: &providers.LLMResponse{Content: "fallback response"}}
	candidates := []providers.FallbackCandidate{
		{Provider: "openai", Model: "shared", DisplayName: "first/shared", IdentityKey: "provider_instance:first", ConfigKey: "instance_target:first/shared"},
		{Provider: "openai", Model: "shared", DisplayName: "second/shared", IdentityKey: "provider_instance:second", ConfigKey: "instance_target:second/shared"},
	}
	loop := newInstanceSelectionTestLoop(instanceSelectionResolution(candidates, primary, fallback))

	result, err := loop.ProcessInstanceSelection(context.Background(), "route", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("ProcessInstanceSelection() error = %v", err)
	}
	if primary.chatCalls != 1 || fallback.chatCalls != 1 {
		t.Fatalf("calls = primary %d, fallback %d", primary.chatCalls, fallback.chatCalls)
	}
	if result.Response.Content != "fallback response" || result.ExactTarget != "second/shared" || result.ServedIdentity != candidates[1].StableKey() {
		t.Fatalf("result = %#v", result)
	}
}

func TestProcessInstanceSelectionDoesNotFallbackAfterVisibleChunk(t *testing.T) {
	primary := &instanceSelectionTestProvider{
		chunks: []string{"first byte"},
		// Retryable on its own: only the visible chunk stops the failover.
		err: transientProviderError("stream interrupted"),
	}
	fallback := &instanceSelectionTestProvider{response: &providers.LLMResponse{Content: "must not run"}}
	candidates := []providers.FallbackCandidate{
		{Provider: "openai", Model: "one", DisplayName: "first/one", IdentityKey: "provider_instance:first", ConfigKey: "instance_target:first/one"},
		{Provider: "openai", Model: "two", DisplayName: "second/two", IdentityKey: "provider_instance:second", ConfigKey: "instance_target:second/two"},
	}
	loop := newInstanceSelectionTestLoop(instanceSelectionResolution(candidates, primary, fallback))
	var chunks []string

	_, err := loop.ProcessInstanceSelection(context.Background(), "route", nil, nil, nil, func(chunk string) {
		chunks = append(chunks, chunk)
	})
	if err == nil || len(chunks) != 1 || chunks[0] != "first byte" {
		t.Fatalf("error = %v, chunks = %#v", err, chunks)
	}
	if primary.streamCalls != 1 || fallback.streamCalls != 0 || fallback.chatCalls != 0 {
		t.Fatalf("calls = primary stream %d, fallback stream %d chat %d", primary.streamCalls, fallback.streamCalls, fallback.chatCalls)
	}
}

func TestProcessInstanceSelectionFallsBackBeforeVisibleChunk(t *testing.T) {
	primary := &instanceSelectionTestProvider{err: transientProviderError("connection reset before output")}
	fallback := &instanceSelectionTestProvider{response: &providers.LLMResponse{Content: "stream fallback"}}
	candidates := []providers.FallbackCandidate{
		{Provider: "openai", Model: "one", DisplayName: "first/one", IdentityKey: "provider_instance:first", ConfigKey: "instance_target:first/one"},
		{Provider: "openai", Model: "two", DisplayName: "second/two", IdentityKey: "provider_instance:second", ConfigKey: "instance_target:second/two"},
	}
	loop := newInstanceSelectionTestLoop(instanceSelectionResolution(candidates, primary, fallback))

	result, err := loop.ProcessInstanceSelection(context.Background(), "route", nil, nil, nil, func(string) {})
	if err != nil {
		t.Fatalf("ProcessInstanceSelection() error = %v", err)
	}
	if primary.streamCalls != 1 || fallback.streamCalls != 1 || result.ExactTarget != "second/two" {
		t.Fatalf("calls/result = %d, %d, %#v", primary.streamCalls, fallback.streamCalls, result)
	}
}

func TestProcessInstanceSelectionSkipsAnInstanceWhoseCircuitIsOpen(t *testing.T) {
	primary := &instanceSelectionTestProvider{err: transientProviderError("fixture unavailable")}
	fallback := &instanceSelectionTestProvider{response: &providers.LLMResponse{Content: "fallback response"}}
	candidates := []providers.FallbackCandidate{
		{Provider: "openai", Model: "shared", DisplayName: "first/shared", IdentityKey: "provider_instance:first", ConfigKey: "instance_target:first/shared"},
		{Provider: "openai", Model: "shared", DisplayName: "second/shared", IdentityKey: "provider_instance:second", ConfigKey: "instance_target:second/shared"},
	}
	loop := newInstanceSelectionTestLoop(instanceSelectionResolution(candidates, primary, fallback))
	for range 2 {
		if _, err := loop.ProcessInstanceSelection(context.Background(), "route", nil, nil, nil, nil); err != nil {
			t.Fatalf("ProcessInstanceSelection() error = %v", err)
		}
	}
	if primary.chatCalls != 1 || fallback.chatCalls != 2 {
		t.Fatalf("calls = primary %d, fallback %d; want the failed instance skipped while its circuit is open", primary.chatCalls, fallback.chatCalls)
	}
}

func TestProcessInstanceSelectionTerminalErrorDoesNotFallBack(t *testing.T) {
	primary := &instanceSelectionTestProvider{err: upstreamStatusError(http.StatusBadRequest, 0)}
	fallback := &instanceSelectionTestProvider{response: &providers.LLMResponse{Content: "must not run"}}
	candidates := []providers.FallbackCandidate{
		{Provider: "openai", Model: "shared", DisplayName: "first/shared", IdentityKey: "provider_instance:first", ConfigKey: "instance_target:first/shared"},
		{Provider: "openai", Model: "shared", DisplayName: "second/shared", IdentityKey: "provider_instance:second", ConfigKey: "instance_target:second/shared"},
	}
	loop := newInstanceSelectionTestLoop(instanceSelectionResolution(candidates, primary, fallback))
	if _, err := loop.ProcessInstanceSelection(context.Background(), "route", nil, nil, nil, nil); err == nil || fallback.chatCalls != 0 {
		t.Fatalf("err = %v fallback calls = %d, want the invalid request to end the call", err, fallback.chatCalls)
	}
}
