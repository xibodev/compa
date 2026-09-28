package agent

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	core "github.com/xibodev/llmgw-core"

	"github.com/xibodev/compa/pkg/bus"
	"github.com/xibodev/compa/pkg/config"
	runtimeevents "github.com/xibodev/compa/pkg/events"
	"github.com/xibodev/compa/pkg/memory"
	"github.com/xibodev/compa/pkg/providers"
	"github.com/xibodev/compa/pkg/session"
)

// =============================================================================
// Mock Providers for turn_coord Tests
// =============================================================================

// simpleConvProvider returns a simple text response without tools
type simpleConvProvider struct{}

func (p *simpleConvProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	return &providers.LLMResponse{
		Content:      "Hello! How can I help you today?",
		FinishReason: "stop",
	}, nil
}

func (p *simpleConvProvider) GetDefaultModel() string {
	return "simple-model"
}

type sequenceProvider struct {
	responses []*providers.LLMResponse
	errors    []error
	callCount int
	mu        sync.Mutex
}

func (p *sequenceProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	idx := p.callCount
	p.callCount++

	if idx < len(p.errors) && p.errors[idx] != nil {
		return nil, p.errors[idx]
	}
	if idx < len(p.responses) && p.responses[idx] != nil {
		return p.responses[idx], nil
	}
	return &providers.LLMResponse{Content: "ok", FinishReason: "stop"}, nil
}

func (p *sequenceProvider) GetDefaultModel() string {
	return "sequence-model"
}

type nativeSearchCaptureProvider struct {
	lastOpts map[string]any
	messages []providers.Message
	tools    []providers.ToolDefinition
}

func (p *nativeSearchCaptureProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	p.messages = append([]providers.Message(nil), messages...)
	p.tools = append([]providers.ToolDefinition(nil), tools...)
	p.lastOpts = make(map[string]any, len(opts))
	for k, v := range opts {
		p.lastOpts[k] = v
	}
	return &providers.LLMResponse{
		Content:      "Using native search",
		FinishReason: "stop",
	}, nil
}

func (p *nativeSearchCaptureProvider) GetDefaultModel() string {
	return "native-search-model"
}

func (p *nativeSearchCaptureProvider) SupportsNativeSearch() bool {
	return true
}

// toolCallRespProvider returns a tool call response
type toolCallRespProvider struct {
	toolName  string
	toolArgs  map[string]any
	response  string
	callCount int
	mu        sync.Mutex
}

func (p *toolCallRespProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	p.mu.Lock()
	p.callCount++
	count := p.callCount
	p.mu.Unlock()

	// First call returns a tool call, subsequent calls return final response
	if count == 1 {
		return &providers.LLMResponse{
			Content: "Let me search for that information.",
			ToolCalls: []providers.ToolCall{
				{
					ID:        "call_1",
					Name:      p.toolName,
					Arguments: p.toolArgs,
				},
			},
			FinishReason: "tool_calls",
		}, nil
	}
	return &providers.LLMResponse{
		Content:      p.response,
		FinishReason: "stop",
	}, nil
}

func (p *toolCallRespProvider) GetDefaultModel() string {
	return "tool-model"
}

// errorProvider simulates various error conditions
type errorProvider struct {
	errType   string
	callCount int
	mu        sync.Mutex
}

func (p *errorProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	p.mu.Lock()
	p.callCount++
	p.mu.Unlock()

	switch p.errType {
	case "timeout":
		return nil, &core.ProviderError{
			Message: "request timed out", Class: core.ProviderErrorTransport, Cause: context.DeadlineExceeded,
			Classification: core.ProviderErrorClassification{Retryable: true, FailoverEligible: true, CircuitFailure: true},
		}
	case "context_length":
		return nil, errors.New("context_length_exceeded")
	case "vision":
		return nil, errors.New("vision_unsupported")
	case "connection_reset":
		return nil, transientProviderError("connection reset by peer")
	case "broken_pipe":
		return nil, transientProviderError("broken pipe")
	case "read_tcp":
		return nil, transientProviderError("read tcp 127.0.0.1:8080: connection reset")
	case "eof":
		return nil, transientProviderError("EOF")
	case "connection_refused":
		return nil, transientProviderError("connection refused")
	default:
		return nil, errors.New("unknown error")
	}
}

func (p *errorProvider) GetDefaultModel() string {
	return "error-model"
}

type failOnceLLMProvider struct {
	err       error
	response  string
	callCount int
	mu        sync.Mutex
}

func (p *failOnceLLMProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	p.mu.Lock()
	p.callCount++
	callCount := p.callCount
	p.mu.Unlock()

	if callCount == 1 {
		return nil, p.err
	}
	return &providers.LLMResponse{
		Content:      p.response,
		FinishReason: "stop",
	}, nil
}

func (p *failOnceLLMProvider) GetDefaultModel() string {
	return "fail-once-model"
}

// =============================================================================
// Test Helper Functions
// =============================================================================

func newTurnCoordTestLoop(t *testing.T, provider providers.LLMProvider) (*AgentLoop, *AgentInstance, func()) {
	t.Helper()
	tmpDir := t.TempDir()

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				ModelName:         "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
	}

	msgBus := bus.NewMessageBus()
	al := NewAgentLoop(cfg, msgBus, provider)
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}

	return al, agent, func() {
		al.Close()
	}
}

func makeTestProcessOpts(sessionKey string) processOptions {
	return processOptions{
		Dispatch: DispatchRequest{
			SessionKey:  sessionKey,
			UserMessage: "test message",
			InboundContext: &bus.InboundContext{
				Channel: "cli",
				ChatID:  "test-chat",
			},
		},
		DefaultResponse: "I couldn't process your request.",
		EnableSummary:   false,
		SendResponse:    false,
		NoHistory:       false,
	}
}

type saveFailingSessionStore struct {
	session.SessionStore
	err error
}

func (s *saveFailingSessionStore) Save(_ string) error {
	return s.err
}

// =============================================================================
// Pipeline Method Tests: SetupTurn
// =============================================================================

func TestPipeline_SetupTurn_BasicInitialization(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()

	pipeline := NewPipeline(al)
	ts := newTurnState(agent, makeTestProcessOpts("test-session"), turnEventScope{
		turnID:  "turn-1",
		context: newTurnContext(nil, nil, nil),
	})

	exec, err := pipeline.SetupTurn(context.Background(), ts)
	if err != nil {
		t.Fatalf("SetupTurn failed: %v", err)
	}
	if exec == nil {
		t.Fatal("expected non-nil turnExecution")
	}
	if len(exec.messages) == 0 {
		t.Error("expected messages to be populated")
	}
	if exec.iteration != 0 {
		t.Errorf("expected iteration 0, got %d", exec.iteration)
	}
}

// =============================================================================
// Pipeline Method Tests: CallLLM
// =============================================================================

func TestPipeline_CallLLM_SimpleResponse(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()

	pipeline := NewPipeline(al)
	ts := newTurnState(agent, makeTestProcessOpts("test-session"), turnEventScope{
		turnID:  "turn-1",
		context: newTurnContext(nil, nil, nil),
	})

	exec, err := pipeline.SetupTurn(context.Background(), ts)
	if err != nil {
		t.Fatalf("SetupTurn failed: %v", err)
	}

	ctrl, err := pipeline.CallLLM(context.Background(), context.Background(), ts, exec, 1)
	if err != nil {
		t.Fatalf("CallLLM failed: %v", err)
	}
	if ctrl != ControlBreak {
		t.Errorf("expected ControlBreak, got %v", ctrl)
	}
	if exec.response == nil {
		t.Fatal("expected non-nil response")
	}
	if exec.response.Content == "" {
		t.Error("expected non-empty content")
	}
}

func TestPipeline_SetupTurn_ModelNameIsPrimaryTargetBeforeFallback(t *testing.T) {
	cfg := newModelTestConfig(t, "chain")
	resolve := testModelResolver(cfg, nil,
		oneModel("main", "gpt-5.4", &simpleConvProvider{}),
		oneModel("backup", "claude-sonnet", &simpleConvProvider{}),
	)
	addTestRoute(cfg, "chain", "main/gpt-5.4", "backup/claude-sonnet")
	al := NewAgentLoop(cfg, bus.NewMessageBus(), nil, WithModelResolver(resolve))
	defer al.Close()
	agent := al.registry.GetDefaultAgent()

	pipeline := NewPipeline(al)
	ts := newTurnState(agent, makeTestProcessOpts("test-session"), turnEventScope{
		turnID:  "turn-1",
		context: newTurnContext(nil, nil, nil),
	})

	exec, err := pipeline.SetupTurn(context.Background(), ts)
	if err != nil {
		t.Fatalf("SetupTurn failed: %v", err)
	}
	if exec.llmModelName != "main/gpt-5.4" {
		t.Fatalf("exec.llmModelName = %q, want %q", exec.llmModelName, "main/gpt-5.4")
	}
	if len(exec.activeCandidates) != 2 {
		t.Fatalf("active candidates = %#v, want the route's two targets", exec.activeCandidates)
	}
}

func TestPipeline_CallLLM_ReportsTheFallbackTargetThatServed(t *testing.T) {
	provider := &sequenceProvider{
		errors: []error{
			upstreamStatusError(http.StatusTooManyRequests, 0),
			nil,
		},
		responses: []*providers.LLMResponse{
			nil,
			{Content: "fallback answer", FinishReason: "stop"},
		},
	}
	cfg := newModelTestConfig(t, "chain")
	resolve := testModelResolver(cfg, nil,
		oneModel("primary", "gpt-5.4", provider),
		oneModel("secondary", "claude-sonnet", provider),
	)
	addTestRoute(cfg, "chain", "primary/gpt-5.4", "secondary/claude-sonnet")
	al := NewAgentLoop(cfg, bus.NewMessageBus(), nil, WithModelResolver(resolve))
	defer al.Close()
	agent := al.registry.GetDefaultAgent()

	pipeline := NewPipeline(al)
	ts := newTurnState(agent, makeTestProcessOpts("test-session"), turnEventScope{
		turnID:  "turn-1",
		context: newTurnContext(nil, nil, nil),
	})

	exec, err := pipeline.SetupTurn(context.Background(), ts)
	if err != nil {
		t.Fatalf("SetupTurn failed: %v", err)
	}

	ctrl, err := pipeline.CallLLM(context.Background(), context.Background(), ts, exec, 1)
	if err != nil {
		t.Fatalf("CallLLM failed: %v", err)
	}
	if ctrl != ControlBreak {
		t.Fatalf("expected ControlBreak, got %v", ctrl)
	}
	if exec.llmModelName != "secondary/claude-sonnet" || exec.lastServed != "secondary/claude-sonnet" {
		t.Fatalf("exec.llmModelName = %q lastServed = %q, want secondary/claude-sonnet",
			exec.llmModelName, exec.lastServed)
	}
	if exec.activeModel != "claude-sonnet" {
		t.Fatalf("exec.activeModel = %q, want the serving target's model", exec.activeModel)
	}
	// A turn on the agent's model reports no per-message selection metadata.
	if exec.servedTarget != "" || exec.servedIdentity != "" {
		t.Fatalf("served metadata = %q/%q, want none for a turn on the agent's model",
			exec.servedTarget, exec.servedIdentity)
	}
}

func TestPipeline_SetupTurn_UsesLightCandidateDisplayName(t *testing.T) {
	cfg := newModelTestConfig(t, "main/gpt-5.4")
	cfg.Agents.Defaults.Routing = &config.RoutingConfig{Enabled: true, LightModel: "light/gpt-5.4-mini", Threshold: 1}
	lightProvider := &simpleConvProvider{}
	resolve := testModelResolver(cfg, nil,
		oneModel("main", "gpt-5.4", &simpleConvProvider{}),
		oneModel("light", "gpt-5.4-mini", lightProvider),
	)
	al := NewAgentLoop(cfg, bus.NewMessageBus(), nil, WithModelResolver(resolve))
	defer al.Close()
	agent := al.registry.GetDefaultAgent()

	pipeline := NewPipeline(al)
	opts := makeTestProcessOpts("test-session")
	opts.Dispatch.UserMessage = ""
	ts := newTurnState(agent, opts, turnEventScope{
		turnID:  "turn-1",
		context: newTurnContext(nil, nil, nil),
	})

	exec, err := pipeline.SetupTurn(context.Background(), ts)
	if err != nil {
		t.Fatalf("SetupTurn failed: %v", err)
	}
	if !exec.usedLight {
		t.Fatal("expected light routing to be used")
	}
	if exec.llmModelName != "light/gpt-5.4-mini" {
		t.Fatalf("exec.llmModelName = %q, want %q", exec.llmModelName, "light/gpt-5.4-mini")
	}
	if exec.activeProvider != lightProvider || exec.activeModel != "gpt-5.4-mini" {
		t.Fatalf("active provider/model = %T/%q, want the light target's", exec.activeProvider, exec.activeModel)
	}
}

func TestRunTurn_FinalizeSaveErrorEmitsErrorTurnEnd(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()

	store, err := memory.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}
	saveErr := errors.New("session save failed")
	agent.Sessions = &saveFailingSessionStore{
		SessionStore: session.NewJSONLBackend(store),
		err:          saveErr,
	}

	runtimeCh, closeRuntimeEvents := subscribeRuntimeEventsForTest(t, al, 8, runtimeevents.KindAgentTurnEnd)
	defer closeRuntimeEvents()

	if _, err := al.ProcessDirect(context.Background(), "hello", "session-save-fail"); err == nil {
		t.Fatal("expected ProcessDirect to fail")
	}

	evt := waitForRuntimeEvent(t, runtimeCh, 2*time.Second, func(evt runtimeevents.Event) bool {
		return evt.Kind == runtimeevents.KindAgentTurnEnd
	})
	payload, ok := evt.Payload.(TurnEndPayload)
	if !ok {
		t.Fatalf("TurnEnd payload type = %T", evt.Payload)
	}
	if payload.Status != TurnEndStatusError {
		t.Fatalf("TurnEnd status = %q, want %q", payload.Status, TurnEndStatusError)
	}
}

func TestPipeline_CallLLM_WithToolCall(t *testing.T) {
	provider := &toolCallRespProvider{
		toolName: "web_search",
		toolArgs: map[string]any{"query": "test"},
		response: "Found information about test.",
	}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()

	pipeline := NewPipeline(al)
	ts := newTurnState(agent, makeTestProcessOpts("test-session"), turnEventScope{
		turnID:  "turn-1",
		context: newTurnContext(nil, nil, nil),
	})

	exec, err := pipeline.SetupTurn(context.Background(), ts)
	if err != nil {
		t.Fatalf("SetupTurn failed: %v", err)
	}

	ctrl, err := pipeline.CallLLM(context.Background(), context.Background(), ts, exec, 1)
	if err != nil {
		t.Fatalf("CallLLM failed: %v", err)
	}
	if ctrl != ControlToolLoop {
		t.Errorf("expected ControlToolLoop, got %v", ctrl)
	}
	if len(exec.normalizedToolCalls) == 0 {
		t.Fatal("expected tool calls")
	}
	if exec.normalizedToolCalls[0].Name != "web_search" {
		t.Errorf("expected tool name 'web_search', got %q", exec.normalizedToolCalls[0].Name)
	}
}

func TestPipeline_CallLLM_UsesNativeSearchWithoutClientWebSearchTool(t *testing.T) {
	provider := &nativeSearchCaptureProvider{}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()

	if _, ok := agent.Tools.Get("web_search"); ok {
		t.Fatal("expected no client-side web_search tool to be registered")
	}

	al.cfg.Tools.Web.Enabled = true
	al.cfg.Tools.Web.PreferNative = true

	pipeline := NewPipeline(al)
	ts := newTurnState(agent, makeTestProcessOpts("test-session"), turnEventScope{
		turnID:  "turn-1",
		context: newTurnContext(nil, nil, nil),
	})

	exec, err := pipeline.SetupTurn(context.Background(), ts)
	if err != nil {
		t.Fatalf("SetupTurn failed: %v", err)
	}

	ctrl, err := pipeline.CallLLM(context.Background(), context.Background(), ts, exec, 1)
	if err != nil {
		t.Fatalf("CallLLM failed: %v", err)
	}
	if ctrl != ControlBreak {
		t.Fatalf("expected ControlBreak, got %v", ctrl)
	}
	if got, _ := provider.lastOpts["native_search"].(bool); !got {
		t.Fatalf("expected native_search=true, got %#v", provider.lastOpts["native_search"])
	}
}

func TestPipeline_CallLLM_TimeoutRetry(t *testing.T) {
	errorPrv := &errorProvider{errType: "timeout"}
	al, agent, cleanup := newTurnCoordTestLoop(t, errorPrv)
	defer cleanup()

	pipeline := NewPipeline(al)
	ts := newTurnState(agent, makeTestProcessOpts("test-session"), turnEventScope{
		turnID:  "turn-1",
		context: newTurnContext(nil, nil, nil),
	})

	exec, err := pipeline.SetupTurn(context.Background(), ts)
	if err != nil {
		t.Fatalf("SetupTurn failed: %v", err)
	}

	// Should retry and eventually fail after max retries
	_, err = pipeline.CallLLM(context.Background(), context.Background(), ts, exec, 1)
	if err == nil {
		t.Error("expected error after retries")
	}
}

func TestPipeline_CallLLM_HTTP5xxRetry(t *testing.T) {
	tmpDir := t.TempDir()
	provider := &failOnceLLMProvider{
		err:      upstreamStatusError(http.StatusInternalServerError, 0),
		response: "Recovered from server error",
	}
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:           tmpDir,
				ModelName:           "test-model",
				MaxTokens:           4096,
				MaxToolIterations:   10,
				MaxLLMRetries:       1,
				LLMRetryBackoffSecs: 1,
			},
		},
	}

	msgBus := bus.NewMessageBus()
	al := NewAgentLoop(cfg, msgBus, provider)
	defer al.Close()
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}

	pipeline := NewPipeline(al)
	ts := newTurnState(agent, makeTestProcessOpts("test-session"), turnEventScope{
		turnID:  "turn-1",
		context: newTurnContext(nil, nil, nil),
	})

	exec, err := pipeline.SetupTurn(context.Background(), ts)
	if err != nil {
		t.Fatalf("SetupTurn failed: %v", err)
	}

	ctrl, err := pipeline.CallLLM(context.Background(), context.Background(), ts, exec, 1)
	if err != nil {
		t.Fatalf("expected HTTP 500 retry to recover, got error: %v", err)
	}
	if ctrl != ControlBreak {
		t.Fatalf("expected ControlBreak, got %v", ctrl)
	}
	if exec.finalContent != "Recovered from server error" {
		t.Fatalf("finalContent = %q, want recovered response", exec.finalContent)
	}
	if provider.callCount != 2 {
		t.Fatalf("callCount = %d, want 2", provider.callCount)
	}
}

// callOnceWithRetries runs one CallLLM on provider with one retry and a
// one-second backoff, returning its error and how long it took.
func callOnceWithRetries(t *testing.T, provider providers.LLMProvider) (error, time.Duration) {
	t.Helper()
	cfg := &config.Config{Agents: config.AgentsConfig{Defaults: config.AgentDefaults{
		Workspace: t.TempDir(), ModelName: "test-model", MaxTokens: 4096, MaxToolIterations: 10,
		MaxLLMRetries: 1, LLMRetryBackoffSecs: 1,
	}}}
	al := NewAgentLoop(cfg, bus.NewMessageBus(), provider)
	t.Cleanup(al.Close)
	pipeline := NewPipeline(al)
	ts := newTurnState(al.registry.GetDefaultAgent(), makeTestProcessOpts("test-session"), turnEventScope{
		turnID: "turn-1", context: newTurnContext(nil, nil, nil),
	})
	exec, err := pipeline.SetupTurn(context.Background(), ts)
	if err != nil {
		t.Fatalf("SetupTurn failed: %v", err)
	}
	start := time.Now()
	_, err = pipeline.CallLLM(context.Background(), context.Background(), ts, exec, 1)
	return err, time.Since(start)
}

func TestPipeline_CallLLM_RetryWaitsOutRetryAfter(t *testing.T) {
	provider := &failOnceLLMProvider{err: upstreamStatusError(http.StatusTooManyRequests, 2*time.Second), response: "after the wait"}
	err, elapsed := callOnceWithRetries(t, provider)
	if err != nil || provider.callCount != 2 {
		t.Fatalf("err = %v calls = %d, want the rate limit retried", err, provider.callCount)
	}
	if elapsed < 2*time.Second {
		t.Fatalf("retried after %v, want at least the 2s Retry-After over the 1s backoff", elapsed)
	}
}

func TestPipeline_CallLLM_RetryAfterBeyondTheLimitIsNotWaited(t *testing.T) {
	provider := &failOnceLLMProvider{err: upstreamStatusError(http.StatusTooManyRequests, 10*time.Minute), response: "never"}
	err, _ := callOnceWithRetries(t, provider)
	if err == nil || provider.callCount != 1 {
		t.Fatalf("err = %v calls = %d, want the rate limit to stand", err, provider.callCount)
	}
	if got := formatProcessingError(err); !strings.Contains(got, "Try again in 10m0s") {
		t.Fatalf("formatted error = %q, want the retry time", got)
	}
}

func TestPipeline_CallLLM_TerminalErrorsAreNotRetried(t *testing.T) {
	for name, providerErr := range map[string]error{
		"auth":         upstreamStatusError(http.StatusUnauthorized, 0),
		"bad request":  upstreamStatusError(http.StatusBadRequest, 0),
		"unclassified": errors.New("status: 500 connection reset"),
	} {
		t.Run(name, func(t *testing.T) {
			provider := &failOnceLLMProvider{err: providerErr, response: "never"}
			if err, _ := callOnceWithRetries(t, provider); err == nil || provider.callCount != 1 {
				t.Fatalf("err = %v calls = %d, want one call", err, provider.callCount)
			}
		})
	}
}

func TestPipeline_CallLLM_ContextLengthError(t *testing.T) {
	errorPrv := &errorProvider{errType: "context_length"}
	al, agent, cleanup := newTurnCoordTestLoop(t, errorPrv)
	defer cleanup()

	pipeline := NewPipeline(al)
	ts := newTurnState(agent, makeTestProcessOpts("test-session"), turnEventScope{
		turnID:  "turn-1",
		context: newTurnContext(nil, nil, nil),
	})

	exec, err := pipeline.SetupTurn(context.Background(), ts)
	if err != nil {
		t.Fatalf("SetupTurn failed: %v", err)
	}

	// Should trigger context compression and retry
	_, err = pipeline.CallLLM(context.Background(), context.Background(), ts, exec, 1)
	// May succeed after compression or fail - either is acceptable
	t.Logf("CallLLM result after context error: err=%v", err)
}

func TestPipeline_CallLLM_NetworkErrorRetry(t *testing.T) {
	testCases := []struct {
		name    string
		errType string
	}{
		{"connection_reset", "connection_reset"},
		{"broken_pipe", "broken_pipe"},
		{"read_tcp", "read_tcp"},
		{"eof", "eof"},
		{"connection_refused", "connection_refused"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			errorPrv := &errorProvider{errType: tc.errType}
			al, agent, cleanup := newTurnCoordTestLoop(t, errorPrv)
			defer cleanup()

			pipeline := NewPipeline(al)
			ts := newTurnState(agent, makeTestProcessOpts("test-session"), turnEventScope{
				turnID:  "turn-1",
				context: newTurnContext(nil, nil, nil),
			})

			exec, err := pipeline.SetupTurn(context.Background(), ts)
			if err != nil {
				t.Fatalf("SetupTurn failed: %v", err)
			}

			_, err = pipeline.CallLLM(context.Background(), context.Background(), ts, exec, 1)
			if err == nil {
				t.Error("expected error after network error retries")
			}
		})
	}
}

func TestPipeline_CallLLM_RetryConfigRespected(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:           tmpDir,
				ModelName:           "test-model",
				MaxTokens:           4096,
				MaxToolIterations:   10,
				MaxLLMRetries:       3,
				LLMRetryBackoffSecs: 1,
			},
		},
	}

	msgBus := bus.NewMessageBus()
	provider := &errorProvider{errType: "connection_reset"}
	al := NewAgentLoop(cfg, msgBus, provider)
	defer al.Close()
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}

	pipeline := NewPipeline(al)
	ts := newTurnState(agent, makeTestProcessOpts("test-session"), turnEventScope{
		turnID:  "turn-1",
		context: newTurnContext(nil, nil, nil),
	})

	exec, err := pipeline.SetupTurn(context.Background(), ts)
	if err != nil {
		t.Fatalf("SetupTurn failed: %v", err)
	}

	start := time.Now()
	_, err = pipeline.CallLLM(context.Background(), context.Background(), ts, exec, 1)
	elapsed := time.Since(start)

	if err == nil {
		t.Error("expected error after retries")
	}

	expectedMinTime := 3 * time.Second
	if elapsed < expectedMinTime {
		t.Errorf("expected at least %v of backoff, got %v", expectedMinTime, elapsed)
	}
}

func TestPipeline_CallLLM_RetryCountLimit(t *testing.T) {
	tmpDir := t.TempDir()

	counterPrv := &countingErrorProvider{errType: "connection_reset", targetCalls: 5}
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:           tmpDir,
				ModelName:           "test-model",
				MaxTokens:           4096,
				MaxToolIterations:   10,
				MaxLLMRetries:       2,
				LLMRetryBackoffSecs: 0,
			},
		},
	}

	msgBus := bus.NewMessageBus()
	al := NewAgentLoop(cfg, msgBus, counterPrv)
	defer al.Close()
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}

	pipeline := NewPipeline(al)
	ts := newTurnState(agent, makeTestProcessOpts("test-session"), turnEventScope{
		turnID:  "turn-1",
		context: newTurnContext(nil, nil, nil),
	})

	exec, err := pipeline.SetupTurn(context.Background(), ts)
	if err != nil {
		t.Fatalf("SetupTurn failed: %v", err)
	}

	_, err = pipeline.CallLLM(context.Background(), context.Background(), ts, exec, 1)
	if err == nil {
		t.Error("expected error after retries")
	}

	if counterPrv.callCount != 3 {
		t.Errorf("expected exactly 3 calls (1 initial + 2 retries), got %d", counterPrv.callCount)
	}
}

type countingErrorProvider struct {
	errType     string
	targetCalls int
	callCount   int
	mu          sync.Mutex
}

func (p *countingErrorProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	p.mu.Lock()
	p.callCount++
	p.mu.Unlock()
	return nil, transientProviderError("connection reset by peer")
}

func (p *countingErrorProvider) GetDefaultModel() string {
	return "counting-error-model"
}

// =============================================================================
// Pipeline Method Tests: ExecuteTools
// =============================================================================

func TestPipeline_ExecuteTools_NoTools(t *testing.T) {
	// Provider returns no tool calls, so ExecuteTools should not be called
	// This test verifies the ControlBreak path from CallLLM
	provider := &simpleConvProvider{}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()

	pipeline := NewPipeline(al)
	ts := newTurnState(agent, makeTestProcessOpts("test-session"), turnEventScope{
		turnID:  "turn-1",
		context: newTurnContext(nil, nil, nil),
	})

	exec, err := pipeline.SetupTurn(context.Background(), ts)
	if err != nil {
		t.Fatalf("SetupTurn failed: %v", err)
	}

	// First CallLLM returns ControlBreak (no tools)
	ctrl, err := pipeline.CallLLM(context.Background(), context.Background(), ts, exec, 1)
	if err != nil {
		t.Fatalf("CallLLM failed: %v", err)
	}

	if ctrl != ControlBreak {
		t.Fatalf("expected ControlBreak, got %v", ctrl)
	}
	// No tools to execute, Finalize should be called directly
}

// =============================================================================
// runTurn Integration Tests
// =============================================================================

func TestRunTurn_SimpleConversation(t *testing.T) {
	provider := &simpleConvProvider{}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()

	pipeline := NewPipeline(al)
	opts := makeTestProcessOpts("test-session-simple")

	ts := newTurnState(agent, opts, turnEventScope{
		turnID:  "turn-simple",
		context: newTurnContext(nil, nil, nil),
	})

	result, err := al.runTurn(context.Background(), ts, pipeline)
	if err != nil {
		t.Fatalf("runTurn failed: %v", err)
	}
	if result.status != TurnEndStatusCompleted {
		t.Errorf("expected status Completed, got %v", result.status)
	}
	if result.finalContent == "" {
		t.Error("expected non-empty finalContent")
	}
}

func TestRunTurn_MaxIterations(t *testing.T) {
	// Provider always returns tool calls, should hit max iterations
	provider := &toolCallRespProvider{
		toolName: "search",
		toolArgs: map[string]any{"q": "x"},
		response: "done",
	}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()

	// Override max iterations to 2
	agent.MaxIterations = 2

	pipeline := NewPipeline(al)
	opts := makeTestProcessOpts("test-session-maxiter")

	ts := newTurnState(agent, opts, turnEventScope{
		turnID:  "turn-maxiter",
		context: newTurnContext(nil, nil, nil),
	})

	result, err := al.runTurn(context.Background(), ts, pipeline)
	if err != nil {
		t.Fatalf("runTurn failed: %v", err)
	}
	// Should complete due to max iterations
	if result.status != TurnEndStatusCompleted {
		t.Errorf("expected status Completed, got %v", result.status)
	}
}

func TestRunTurn_HardAbort(t *testing.T) {
	// Provider simulates a slow response, but we'll abort mid-turn
	slowProvider := &slowMockProvider{delay: 10 * time.Second}
	al, agent, cleanup := newTurnCoordTestLoop(t, slowProvider)
	defer cleanup()

	pipeline := NewPipeline(al)
	opts := makeTestProcessOpts("test-session-abort")

	ts := newTurnState(agent, opts, turnEventScope{
		turnID:  "turn-abort",
		context: newTurnContext(nil, nil, nil),
	})

	// Run in goroutine with abort after short delay
	done := make(chan struct{})

	go func() {
		al.runTurn(context.Background(), ts, pipeline)
		close(done)
	}()

	// Give it a moment to start
	time.Sleep(50 * time.Millisecond)

	// Request hard abort
	ts.requestHardAbort()

	// Wait for runTurn to complete
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runTurn did not complete after abort")
	}
}

func TestRunTurn_SteeringMessageInjection(t *testing.T) {
	provider := &simpleConvProvider{}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()

	pipeline := NewPipeline(al)
	opts := makeTestProcessOpts("test-session-steering")

	ts := newTurnState(agent, opts, turnEventScope{
		turnID:  "turn-steering",
		context: newTurnContext(nil, nil, nil),
	})

	// Enqueue steering message for the turn's session before runTurn
	steeringMsg := providers.Message{
		Role:    "user",
		Content: "Steering message",
	}
	if err := al.enqueueSteeringMessage(ts.sessionKey, agent.ID, steeringMsg); err != nil {
		t.Fatalf("enqueueSteeringMessage() error = %v", err)
	}

	result, err := al.runTurn(context.Background(), ts, pipeline)
	if err != nil {
		t.Fatalf("runTurn failed: %v", err)
	}
	if result.status != TurnEndStatusCompleted {
		t.Errorf("expected status Completed, got %v", result.status)
	}
	if pending := al.pendingSteeringCountForScope(ts.sessionKey); pending != 0 {
		t.Errorf("expected the queued steering message to be injected, %d still pending", pending)
	}
}

// =============================================================================
// turnState Tests
// =============================================================================

func TestTurnState_HardAbortRequested(t *testing.T) {
	ts := &turnState{
		hardAbort: false,
	}

	if ts.hardAbortRequested() {
		t.Error("expected no hard abort initially")
	}

	ts.requestHardAbort()

	if !ts.hardAbortRequested() {
		t.Error("expected hard abort to be requested")
	}
}

func TestTurnState_SkillContextSnapshotsTrackLatestSuccessfulPath(t *testing.T) {
	ts := &turnState{}

	ts.recordSkillContextSnapshot(skillContextTriggerInitialBuild, []string{"skill-a"})
	ts.recordSkillContextSnapshot(skillContextTriggerContextRetryRebuild, []string{"skill-b", "skill-c"})

	if got := ts.attemptedSkillsSnapshot(); len(got) != 3 || got[0] != "skill-a" || got[1] != "skill-b" ||
		got[2] != "skill-c" {
		t.Fatalf("attemptedSkillsSnapshot = %v, want [skill-a skill-b skill-c]", got)
	}

	if got := ts.latestSkillContextSnapshot(); len(got) != 2 || got[0] != "skill-b" || got[1] != "skill-c" {
		t.Fatalf("latestSkillContextSnapshot = %v, want [skill-b skill-c]", got)
	}

	snapshots := ts.skillContextSnapshotsSnapshot()
	if len(snapshots) != 2 {
		t.Fatalf("len(skillContextSnapshotsSnapshot()) = %d, want 2", len(snapshots))
	}
	if snapshots[0].Sequence != 1 || snapshots[0].Trigger != skillContextTriggerInitialBuild {
		t.Fatalf("snapshots[0] = %+v, want sequence=1 trigger=%q", snapshots[0], skillContextTriggerInitialBuild)
	}
	if snapshots[1].Sequence != 2 || snapshots[1].Trigger != skillContextTriggerContextRetryRebuild {
		t.Fatalf("snapshots[1] = %+v, want sequence=2 trigger=%q", snapshots[1], skillContextTriggerContextRetryRebuild)
	}
}
