package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xibodev/compa/v3/pkg/bus"
	"github.com/xibodev/compa/v3/pkg/config"
	runtimeevents "github.com/xibodev/compa/v3/pkg/events"
	"github.com/xibodev/compa/v3/pkg/providers"
)

func TestRuntimeEventLoggerFiltering(t *testing.T) {
	cfg := config.DefaultConfig()
	eventLogger := newRuntimeEventLogger(cfg)
	if eventLogger == nil {
		t.Fatal("default runtime event logger is nil")
	}

	if !eventLogger.shouldLog(runtimeevents.Event{
		Kind:     runtimeevents.KindAgentTurnStart,
		Severity: runtimeevents.SeverityInfo,
	}) {
		t.Fatal("default config should log agent events")
	}
	if eventLogger.shouldLog(runtimeevents.Event{
		Kind:     runtimeevents.KindChannelLifecycleStarted,
		Severity: runtimeevents.SeverityInfo,
	}) {
		t.Fatal("default config should not log non-agent events")
	}

	cfg.Events.Logging.Include = []string{"*"}
	cfg.Events.Logging.Exclude = []string{"mcp.*"}
	eventLogger = newRuntimeEventLogger(cfg)
	if !eventLogger.shouldLog(runtimeevents.Event{
		Kind:     runtimeevents.KindGatewayReady,
		Severity: runtimeevents.SeverityInfo,
	}) {
		t.Fatal("include * should log gateway events")
	}
	if eventLogger.shouldLog(runtimeevents.Event{
		Kind:     runtimeevents.KindMCPServerConnected,
		Severity: runtimeevents.SeverityInfo,
	}) {
		t.Fatal("exclude mcp.* should suppress MCP events")
	}

	cfg.Events.Logging.Exclude = nil
	cfg.Events.Logging.MinSeverity = "warn"
	eventLogger = newRuntimeEventLogger(cfg)
	if eventLogger.shouldLog(runtimeevents.Event{
		Kind:     runtimeevents.KindGatewayReady,
		Severity: runtimeevents.SeverityInfo,
	}) {
		t.Fatal("min severity warn should suppress info events")
	}
	if !eventLogger.shouldLog(runtimeevents.Event{
		Kind:     runtimeevents.KindGatewayReloadFailed,
		Severity: runtimeevents.SeverityError,
	}) {
		t.Fatal("min severity warn should allow error events")
	}

	cfg.Events.Logging.Enabled = false
	if newRuntimeEventLogger(cfg) != nil {
		t.Fatal("disabled config should not create runtime event logger")
	}
}

func TestRuntimeEventLogFieldsSummarizeAgentPayload(t *testing.T) {
	fields := runtimeEventLogFields(runtimeevents.Event{
		ID:       "evt-test",
		Kind:     runtimeevents.KindAgentToolExecStart,
		Severity: runtimeevents.SeverityInfo,
		Source: runtimeevents.Source{
			Component: "agent",
			Name:      "main",
		},
		Scope: runtimeevents.Scope{
			AgentID:    "main",
			SessionKey: "session-1",
			TurnID:     "turn-1",
		},
		Payload: ToolExecStartPayload{
			Tool: "exec",
			Arguments: map[string]any{
				"secret": "should-not-be-logged-by-default",
			},
		},
	})

	if fields["event_id"] != "evt-test" || fields["source_component"] != "agent" {
		t.Fatalf("missing common event fields: %#v", fields)
	}
	if fields["tool"] != "exec" || fields["args_count"] != 1 {
		t.Fatalf("missing safe agent payload summary fields: %#v", fields)
	}
	if _, ok := fields["payload"]; ok {
		t.Fatalf("raw payload should not be included by runtimeEventLogFields: %#v", fields)
	}
}

// A failed turn's log line says why it failed, on one bounded line.
func TestRuntimeEventLogFieldsSayWhyATurnFailed(t *testing.T) {
	fields := runtimeEventLogFields(runtimeevents.Event{
		ID:       "evt-end",
		Kind:     runtimeevents.KindAgentTurnEnd,
		Severity: runtimeevents.SeverityError,
		Payload: TurnEndPayload{
			Status: TurnEndStatusError,
			Error:  "No model selected.\nConnect a provider " + strings.Repeat("x", 400),
		},
	})
	reason, _ := fields["error"].(string)
	if !strings.HasPrefix(reason, "No model selected. Connect a provider") || strings.Contains(reason, "\n") ||
		len([]rune(reason)) > maxRuntimeEventText+1 {
		t.Fatalf("error field = %q, want the reason on one bounded line", reason)
	}
	if _, ok := runtimeEventLogFields(runtimeevents.Event{Kind: runtimeevents.KindAgentTurnEnd, Payload: TurnEndPayload{Status: TurnEndStatusCompleted}})["error"]; ok {
		t.Fatal("a completed turn logged an error")
	}
}

func TestRuntimeEventLogFieldsIncludeSafeAttrs(t *testing.T) {
	fields := runtimeEventLogFields(runtimeevents.Event{
		ID:       "evt-gateway",
		Kind:     runtimeevents.KindGatewayReady,
		Severity: runtimeevents.SeverityInfo,
		Attrs: map[string]any{
			"duration_ms": 42,
			"error":       "startup failed",
			"event_kind":  "conflict",
		},
	})

	if fields["duration_ms"] != 42 || fields["error"] != "startup failed" {
		t.Fatalf("missing safe attrs: %#v", fields)
	}
	if fields["event_kind"] != runtimeevents.KindGatewayReady.String() {
		t.Fatalf("event_kind overwritten by attrs: %#v", fields)
	}
	if fields["attr_event_kind"] != "conflict" {
		t.Fatalf("conflicting attr not preserved with prefix: %#v", fields)
	}
	if _, ok := fields["payload"]; ok {
		t.Fatalf("raw payload should not be included by runtimeEventLogFields: %#v", fields)
	}
}

func runtimeEventLoggerStateForTest(
	al *AgentLoop,
) (*runtimeEventLogger, runtimeevents.Subscription) {
	al.runtimeEventLogMu.RLock()
	defer al.runtimeEventLogMu.RUnlock()
	return al.runtimeEventLogger, al.runtimeEventLogSub
}

func TestReloadProviderAndConfigRefreshesRuntimeEventLogger(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Events.Logging.Include = []string{"agent.*"}

	al := NewAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{})
	defer al.Close()

	eventLogger, logSub := runtimeEventLoggerStateForTest(al)
	if eventLogger == nil || logSub == nil {
		t.Fatal("expected initial runtime event logger subscription")
	}
	if eventLogger.shouldLog(runtimeevents.Event{
		Kind:     runtimeevents.KindGatewayReloadCompleted,
		Severity: runtimeevents.SeverityInfo,
	}) {
		t.Fatal("initial agent-only logging should not log gateway reload events")
	}

	reloaded := config.DefaultConfig()
	reloaded.Agents.Defaults.Workspace = cfg.Agents.Defaults.Workspace
	reloaded.Events.Logging.Include = []string{"gateway.*"}
	if err := al.ReloadProviderAndConfig(context.Background(), &mockProvider{}, reloaded); err != nil {
		t.Fatalf("ReloadProviderAndConfig() error = %v", err)
	}

	eventLogger, logSub = runtimeEventLoggerStateForTest(al)
	if eventLogger == nil || logSub == nil {
		t.Fatal("expected runtime event logger subscription after reload")
	}
	if !eventLogger.shouldLog(runtimeevents.Event{
		Kind:     runtimeevents.KindGatewayReloadCompleted,
		Severity: runtimeevents.SeverityInfo,
	}) {
		t.Fatal("reloaded gateway logging should log gateway reload events")
	}
	if eventLogger.shouldLog(runtimeevents.Event{
		Kind:     runtimeevents.KindAgentTurnStart,
		Severity: runtimeevents.SeverityInfo,
	}) {
		t.Fatal("reloaded gateway-only logging should not log agent events")
	}

	disabled := config.DefaultConfig()
	disabled.Agents.Defaults.Workspace = cfg.Agents.Defaults.Workspace
	disabled.Events.Logging.Enabled = false
	if err := al.ReloadProviderAndConfig(context.Background(), &mockProvider{}, disabled); err != nil {
		t.Fatalf("ReloadProviderAndConfig() with disabled logging error = %v", err)
	}
	eventLogger, logSub = runtimeEventLoggerStateForTest(al)
	if eventLogger != nil || logSub != nil {
		t.Fatal("expected runtime event logger to be disabled after reload")
	}
}

type reloadBlockingProvider struct {
	chatStarted chan struct{}
	releaseChat chan struct{}
	closeCalled chan struct{}
	// closeDelay is how long Close takes, like a provider ending a process.
	closeDelay time.Duration
}

func (p *reloadBlockingProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	options map[string]any,
) (*providers.LLMResponse, error) {
	select {
	case <-p.chatStarted:
	default:
		close(p.chatStarted)
	}

	select {
	case <-p.releaseChat:
		return &providers.LLMResponse{Content: "done"}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *reloadBlockingProvider) GetDefaultModel() string {
	return "reload-blocking"
}

func (p *reloadBlockingProvider) Close() {
	time.Sleep(p.closeDelay)
	select {
	case <-p.closeCalled:
	default:
		close(p.closeCalled)
	}
}

// A reload does not wait for the requests in flight: it returns while one
// runs on the previous provider, which closes once the request ended.
func TestReloadClosesTheReplacedProviderOnceItsRequestsEnd(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()

	oldProvider := &reloadBlockingProvider{
		chatStarted: make(chan struct{}),
		releaseChat: make(chan struct{}),
		closeCalled: make(chan struct{}),
	}
	al := NewAgentLoop(cfg, bus.NewMessageBus(), oldProvider)
	defer al.Close()

	msg := testInboundMessage(bus.InboundMessage{
		Channel:  "test",
		ChatID:   "reload-chat",
		SenderID: "user-1",
		Content:  "hold request open",
	})

	reqDone := make(chan error, 1)
	go func() {
		_, err := al.processMessage(context.Background(), msg)
		reqDone <- err
	}()

	select {
	case <-oldProvider.chatStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for in-flight provider request")
	}

	reloadDone := make(chan error, 1)
	go func() {
		reloaded := config.DefaultConfig()
		reloaded.Agents.Defaults.Workspace = cfg.Agents.Defaults.Workspace
		reloadDone <- al.ReloadProviderAndConfig(context.Background(), &mockProvider{}, reloaded)
	}()
	select {
	case err := <-reloadDone:
		if err != nil {
			t.Fatalf("ReloadProviderAndConfig() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the reload waited for the request in flight")
	}

	select {
	case <-oldProvider.closeCalled:
		t.Fatal("old provider closed before in-flight request completed")
	case <-time.After(150 * time.Millisecond):
	}

	close(oldProvider.releaseChat)

	select {
	case err := <-reqDone:
		if err != nil {
			t.Fatalf("processMessage() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for in-flight request to complete")
	}

	select {
	case <-oldProvider.closeCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for old provider close")
	}
}

// What a reload replaced closes once no request is in flight, and when the
// loop closes, at once.
func TestReplacedProvidersCloseOnceRequestsEndOrTheLoopCloses(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	al := NewAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{})

	first := &reloadBlockingProvider{closeCalled: make(chan struct{})}
	al.activeRequestsInc()
	al.closeReplaced(map[string]providers.LLMProvider{"first": first}, nil, nil)
	select {
	case <-first.closeCalled:
		t.Fatal("the replaced provider closed with a request in flight")
	case <-time.After(150 * time.Millisecond):
	}
	al.activeRequestsDec()
	select {
	case <-first.closeCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("the replaced provider did not close once the request ended")
	}

	second := &reloadBlockingProvider{closeCalled: make(chan struct{}), closeDelay: 300 * time.Millisecond}
	al.activeRequestsInc()
	defer al.activeRequestsDec()
	al.closeReplaced(map[string]providers.LLMProvider{"second": second}, nil, nil)
	closed := make(chan struct{})
	go func() {
		al.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close waited for the request in flight")
	}
	select {
	case <-second.closeCalled:
	default:
		t.Fatal("Close returned before closing what the reload replaced")
	}
}

func TestWaitForActiveRequestsHonorsContextCancellation(t *testing.T) {
	al := &AgentLoop{}
	al.activeReqCond = sync.NewCond(&al.activeReqMu)
	al.activeRequestsInc()
	defer al.activeRequestsDec()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if al.waitForActiveRequests(ctx, time.Second) {
		t.Fatal("waitForActiveRequests() = true, want false on canceled context")
	}
}

func TestReloadProviderAndConfigReturnsCanceledErrorWhenRegistryCreationPanics(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()

	al := NewAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{})
	defer al.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := al.ReloadProviderAndConfig(ctx, &panicProviderForReloadTest{}, cfg)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ReloadProviderAndConfig() error = %v, want context canceled", err)
	}
}

type panicProviderForReloadTest struct{}

func (p *panicProviderForReloadTest) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	options map[string]any,
) (*providers.LLMResponse, error) {
	return &providers.LLMResponse{Content: "unused"}, nil
}

func (p *panicProviderForReloadTest) GetDefaultModel() string {
	panic("boom")
}

func TestCloseRuntimeEventLoggerSubscriptionWaitsForDrain(t *testing.T) {
	eventBus := runtimeevents.NewBus()
	defer func() {
		if err := eventBus.Close(); err != nil {
			t.Fatalf("Close failed: %v", err)
		}
	}()

	var handled atomic.Uint64
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	sub, err := eventBus.Channel().Subscribe(
		context.Background(),
		runtimeevents.SubscribeOptions{
			Name:        "runtime-event-logger",
			Buffer:      2,
			Concurrency: runtimeevents.Locked,
		},
		func(context.Context, runtimeevents.Event) error {
			if handled.Add(1) == 1 {
				close(firstStarted)
				<-releaseFirst
			}
			return nil
		},
	)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	first := eventBus.Publish(context.Background(), runtimeevents.Event{Kind: runtimeevents.Kind("test.first")})
	if first.Delivered != 1 {
		t.Fatalf("first Publish = %+v, want one delivered event", first)
	}
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first handler to start")
	}
	second := eventBus.Publish(context.Background(), runtimeevents.Event{Kind: runtimeevents.Kind("test.second")})
	if second.Delivered != 1 {
		t.Fatalf("second Publish = %+v, want one delivered event", second)
	}

	closeReturned := make(chan struct{})
	go func() {
		closeRuntimeEventLoggerSubscription(sub)
		close(closeReturned)
	}()

	select {
	case <-closeReturned:
		t.Fatal("runtime event logger close returned before buffered events drained")
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseFirst)
	select {
	case <-closeReturned:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for runtime event logger close to return")
	}
	if got := handled.Load(); got != 2 {
		t.Fatalf("handled = %d, want 2", got)
	}
}
