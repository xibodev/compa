package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	core "github.com/xibodev/llmgw-core"

	"github.com/xibodev/compa/v4/pkg/bus"
	"github.com/xibodev/compa/v4/pkg/config"
	"github.com/xibodev/compa/v4/pkg/providers"
	"github.com/xibodev/compa/v4/pkg/session"
)

// scriptedProvider records every prompt and answers it with step.
type scriptedProvider struct {
	mu      sync.Mutex
	prompts [][]providers.Message
	step    func(call int, messages []providers.Message) (*providers.LLMResponse, error)
}

func (p *scriptedProvider) Chat(
	_ context.Context,
	messages []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	p.mu.Lock()
	p.prompts = append(p.prompts, append([]providers.Message(nil), messages...))
	call := len(p.prompts)
	p.mu.Unlock()
	return p.step(call, messages)
}

func (p *scriptedProvider) GetDefaultModel() string { return "scripted-model" }

// prompt returns the text of every message of call n (from 1).
func (p *scriptedProvider) prompt(n int) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if n < 1 || n > len(p.prompts) {
		return ""
	}
	var sb strings.Builder
	for _, msg := range p.prompts[n-1] {
		sb.WriteString(msg.Content)
		sb.WriteString("\n")
	}
	return sb.String()
}

func (p *scriptedProvider) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.prompts)
}

func alternatingHistory(pairs int) []providers.Message {
	history := make([]providers.Message, 0, pairs*2)
	for i := 1; i <= pairs; i++ {
		history = append(history,
			providers.Message{Role: "user", Content: fmt.Sprintf("q%d", i)},
			providers.Message{Role: "assistant", Content: fmt.Sprintf("a%d", i)},
		)
	}
	return history
}

func contents(messages []providers.Message) []string {
	out := make([]string, len(messages))
	for i, msg := range messages {
		out[i] = msg.Content
	}
	return out
}

func twoAgentConfig(t *testing.T) *config.Config {
	t.Helper()
	workspace := t.TempDir()
	return &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         filepath.Join(workspace, "default"),
				ModelName:         "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
			List: []config.AgentConfig{
				{ID: "main", Default: true, Workspace: filepath.Join(workspace, "main")},
				{ID: "support", Workspace: filepath.Join(workspace, "support")},
			},
		},
	}
}

// AG-02: a routed agent's turn reads its own store, not the default agent's.
func TestRoutedAgentTurnSeesItsOwnHistory(t *testing.T) {
	provider := &scriptedProvider{step: func(int, []providers.Message) (*providers.LLMResponse, error) {
		return &providers.LLMResponse{Content: "4711"}, nil
	}}
	al := NewAgentLoop(twoAgentConfig(t), bus.NewMessageBus(), provider)
	support, ok := al.GetRegistry().GetAgent("support")
	if !ok {
		t.Fatal("support agent missing")
	}
	key := session.BuildOpaqueSessionKey("routed-history")
	support.Sessions.SetHistory(key, []providers.Message{
		{Role: "user", Content: "remember the code 4711"},
		{Role: "assistant", Content: "noted"},
	})

	if _, err := al.runAgentLoop(context.Background(), support, processOptions{
		Dispatch:        DispatchRequest{SessionKey: key, UserMessage: "what was the code?"},
		DefaultResponse: defaultResponse,
	}); err != nil {
		t.Fatalf("runAgentLoop() error = %v", err)
	}
	if !strings.Contains(provider.prompt(1), "remember the code 4711") {
		t.Fatalf("routed agent's prompt lacks its history:\n%s", provider.prompt(1))
	}
	if got := contents(support.Sessions.GetHistory(key)); len(got) != 4 || got[3] != "4711" {
		t.Fatalf("support history = %q", got)
	}
	if main := al.GetRegistry().GetDefaultAgent(); len(main.Sessions.GetHistory(key)) != 0 {
		t.Fatal("the routed turn wrote into the default agent's store")
	}
}

// AG-02: the registry's default agent is routing's default, the agent marked
// default, even when an agent named "main" exists.
func TestRegistryDefaultAgentFollowsTheDefaultFlag(t *testing.T) {
	cfg := twoAgentConfig(t)
	cfg.Agents.List[0].Default = false
	cfg.Agents.List[1].Default = true
	al := NewAgentLoop(cfg, bus.NewMessageBus(), &simpleMockProvider{response: "ok"})
	if got := al.GetRegistry().GetDefaultAgent().ID; got != "support" {
		t.Fatalf("GetDefaultAgent() = %q, want support", got)
	}
}

// AG-03: a multi-batch summary keeps the session's earlier summary, both in
// the first batch and in the merge.
func TestSummarizeSessionKeepsEarlierSummaryAcrossBatches(t *testing.T) {
	provider := &scriptedProvider{step: func(call int, _ []providers.Message) (*providers.LLMResponse, error) {
		return &providers.LLMResponse{Content: fmt.Sprintf("summary-%d", call)}, nil
	}}
	al := NewAgentLoop(testConfig(t), bus.NewMessageBus(), provider)
	agent := al.GetRegistry().GetDefaultAgent()
	agent.Sessions.SetHistory("s", alternatingHistory(8))
	agent.Sessions.SetSummary("s", "EARLIER-FACTS")

	(&defaultContextManager{al: al}).summarizeSession(agent, "s")

	if provider.calls() != 3 {
		t.Fatalf("calls = %d, want two batches and a merge", provider.calls())
	}
	if !strings.Contains(provider.prompt(1), "EARLIER-FACTS") {
		t.Fatalf("first batch lost the earlier summary:\n%s", provider.prompt(1))
	}
	if !strings.Contains(provider.prompt(3), "EARLIER-FACTS") {
		t.Fatalf("merge lost the earlier summary:\n%s", provider.prompt(3))
	}
	if got := agent.Sessions.GetSummary("s"); got != "summary-3" {
		t.Fatalf("summary = %q, want the merge", got)
	}
	if got := len(agent.Sessions.GetHistory("s")); got != 4 {
		t.Fatalf("history length = %d, want the 4 kept messages", got)
	}
}

// AG-03: when the model fails, the history is not truncated behind an
// excerpt of itself.
func TestSummarizeSessionFailureKeepsHistory(t *testing.T) {
	provider := &scriptedProvider{step: func(int, []providers.Message) (*providers.LLMResponse, error) {
		return nil, errors.New("model unavailable")
	}}
	al := NewAgentLoop(testConfig(t), bus.NewMessageBus(), provider)
	agent := al.GetRegistry().GetDefaultAgent()
	agent.Sessions.SetHistory("s", alternatingHistory(4))
	agent.Sessions.SetSummary("s", "EARLIER-FACTS")

	(&defaultContextManager{al: al}).summarizeSession(agent, "s")

	if got := len(agent.Sessions.GetHistory("s")); got != 8 {
		t.Fatalf("history length = %d, want all 8 messages", got)
	}
	if got := agent.Sessions.GetSummary("s"); got != "EARLIER-FACTS" {
		t.Fatalf("summary = %q, want it unchanged", got)
	}
}

// AG-04: messages appended while the summary is written survive it.
func TestSummarizeSessionKeepsMessagesAppendedMeanwhile(t *testing.T) {
	var agent *AgentInstance
	provider := &scriptedProvider{step: func(int, []providers.Message) (*providers.LLMResponse, error) {
		agent.Sessions.AddMessage("s", "user", "appended during the summary")
		return &providers.LLMResponse{Content: "summary"}, nil
	}}
	al := NewAgentLoop(testConfig(t), bus.NewMessageBus(), provider)
	agent = al.GetRegistry().GetDefaultAgent()
	agent.Sessions.SetHistory("s", alternatingHistory(4))

	(&defaultContextManager{al: al}).summarizeSession(agent, "s")

	got := contents(agent.Sessions.GetHistory("s"))
	want := []string{"q3", "a3", "q4", "a4", "appended during the summary"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("history = %q, want %q", got, want)
	}
	if summary := agent.Sessions.GetSummary("s"); summary != "summary" {
		t.Fatalf("summary = %q", summary)
	}
}

// AG-07: steering that overtakes a final answer is injected once, after the
// answer, which is stored and delivered.
func TestSteeringAfterFinalAnswerKeepsTheAnswer(t *testing.T) {
	sessionKey := session.BuildOpaqueSessionKey("steer-after-answer")
	var al *AgentLoop
	provider := &scriptedProvider{step: func(call int, _ []providers.Message) (*providers.LLMResponse, error) {
		if call == 1 {
			if err := al.enqueueSteeringMessage(sessionKey, "main",
				providers.Message{Role: "user", Content: "steer now"}); err != nil {
				return nil, err
			}
			return &providers.LLMResponse{Content: "first answer"}, nil
		}
		return &providers.LLMResponse{Content: "second answer"}, nil
	}}
	msgBus := bus.NewMessageBus()
	al = NewAgentLoop(testConfig(t), msgBus, provider)

	resp, err := al.ProcessDirectWithChannel(context.Background(), "do something", sessionKey, "test", "chat1")
	if err != nil {
		t.Fatalf("ProcessDirectWithChannel() error = %v", err)
	}
	if resp != "second answer" {
		t.Fatalf("response = %q", resp)
	}
	if provider.calls() != 2 {
		t.Fatalf("calls = %d, want 2", provider.calls())
	}
	second := provider.prompt(2)
	if n := strings.Count(second, "steer now"); n != 1 {
		t.Fatalf("steering appears %d times in the second prompt:\n%s", n, second)
	}
	if strings.Index(second, "first answer") > strings.Index(second, "steer now") ||
		!strings.Contains(second, "first answer") {
		t.Fatalf("the first answer must precede the steering:\n%s", second)
	}
	history := contents(al.GetRegistry().GetDefaultAgent().Sessions.GetHistory(sessionKey))
	want := []string{"do something", "first answer", "steer now", "second answer"}
	if strings.Join(history, "|") != strings.Join(want, "|") {
		t.Fatalf("history = %q, want %q", history, want)
	}
	select {
	case outbound := <-msgBus.OutboundChan():
		if outbound.Content != "first answer" {
			t.Fatalf("outbound = %q, want the first answer", outbound.Content)
		}
	case <-time.After(time.Second):
		t.Fatal("the overtaken answer was not delivered")
	}
}

// AG-10: retry compaction keeps the active turn's own messages.
func TestForceCompressionKeepsTheActiveTurn(t *testing.T) {
	al := newCMTestAgentLoop(testConfig(t))
	agent := al.GetRegistry().GetDefaultAgent()
	activeTurn := []providers.Message{
		{Role: "user", Content: "current question"},
		{Role: "assistant", ToolCalls: []providers.ToolCall{{
			ID: "call-1", Type: "function", Name: "lookup",
			Function: &providers.FunctionCall{Name: "lookup", Arguments: "{}"},
		}}},
		{Role: "tool", Content: "lookup result", ToolCallID: "call-1"},
	}
	agent.Sessions.SetHistory("s", append(alternatingHistory(1), activeTurn...))
	ts := &turnState{agent: agent, sessionKey: "s", persistedMessages: activeTurn}
	al.registerActiveTurn(ts)
	defer al.clearActiveTurn(ts)

	if err := al.contextManager.Compact(context.Background(), &CompactRequest{
		SessionKey: "s",
		Reason:     ContextCompressReasonRetry,
		AgentID:    agent.ID,
	}); err != nil {
		t.Fatalf("Compact() error = %v", err)
	}
	got := contents(agent.Sessions.GetHistory("s"))
	want := []string{"current question", "", "lookup result"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("history = %q, want the active turn whole: %q", got, want)
	}
}

// AG-11: max_tokens at or above the context window is capped, so requests
// are not all over budget.
func TestMaxTokensIsCappedBelowTheContextWindow(t *testing.T) {
	cfg := testConfig(t)
	cfg.Agents.Defaults.MaxTokens = 4096
	cfg.Agents.Defaults.ContextWindow = 1000
	agent := NewAgentLoop(cfg, bus.NewMessageBus(), &simpleMockProvider{response: "ok"}).GetRegistry().GetDefaultAgent()
	if agent.MaxTokens != 500 || agent.ContextWindow != 1000 {
		t.Fatalf("max_tokens = %d, context_window = %d; want 500 and 1000", agent.MaxTokens, agent.ContextWindow)
	}
}

// writeMemoryFile writes a file of workspace's memory directory, today's
// daily note for name "today".
func writeMemoryFile(t *testing.T, workspace, name, content string) {
	t.Helper()
	path := filepath.Join(workspace, "memory", name)
	if name == "today" {
		path = dailyNotePaths(filepath.Join(workspace, "memory"), time.Now(), 1)[0]
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create memory directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// AG-12: injected memory is capped, with a marker saying what was left out.
func TestMemoryContextIsCapped(t *testing.T) {
	workspace := t.TempDir()
	store := NewMemoryStore(workspace)
	writeMemoryFile(t, workspace, "MEMORY.md", strings.Repeat("fact ", 10<<10))
	writeMemoryFile(t, workspace, "today", "today's note")
	got := store.GetMemoryContext()
	if len(got) > memoryContextLimit+512 {
		t.Fatalf("memory context is %d bytes, want about %d", len(got), memoryContextLimit)
	}
	if !strings.Contains(got, "more bytes of memory/MEMORY.md not shown") {
		t.Fatal("the truncation marker is missing")
	}
}

// AG-27: the cached system prompt picks up a daily note written after it
// was built.
func TestSystemPromptCacheSeesNewDailyNotes(t *testing.T) {
	workspace := t.TempDir()
	cb := NewContextBuilder(workspace)
	if strings.Contains(cb.BuildSystemPromptWithCache(), "fresh daily note") {
		t.Fatal("unexpected note before it was written")
	}
	writeMemoryFile(t, workspace, "today", "fresh daily note")
	if !strings.Contains(cb.BuildSystemPromptWithCache(), "fresh daily note") {
		t.Fatal("the cached prompt missed today's note")
	}
}

// AG-10: only a request the provider rejected is a context overflow.
func TestIsContextOverflowError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"unclassified context error", errors.New("context_length_exceeded"), true},
		{"dashscope wording", errors.New("InvalidParameter: Total tokens of image and text exceed max message tokens"), true},
		{"bad request", &core.ProviderError{
			Message:        "This model's maximum context length is 8192 tokens",
			Classification: core.ProviderErrorClassification{StatusCode: 400},
		}, true},
		{"max_tokens setting", errors.New("max_tokens is too large: 9000"), false},
		{"bare invalid parameter", errors.New("InvalidParameter: temperature out of range"), false},
		{"rate limit mentioning tokens", &core.ProviderError{
			Message:        "token limit per minute reached",
			Class:          core.ProviderErrorRateLimited,
			Classification: core.ProviderErrorClassification{StatusCode: 429},
		}, false},
		{"auth failure mentioning the context window", &core.ProviderError{
			Message:        "context window access denied",
			Classification: core.ProviderErrorClassification{StatusCode: 401},
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isContextOverflowError(tt.err); got != tt.want {
				t.Fatalf("isContextOverflowError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
