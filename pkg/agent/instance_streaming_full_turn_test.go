package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xibodev/compa/v3/pkg/bus"
	"github.com/xibodev/compa/v3/pkg/config"
	"github.com/xibodev/compa/v3/pkg/providers"
	"github.com/xibodev/compa/v3/pkg/session"
)

type instanceStreamingProvider struct {
	instanceID  string
	streamCalls atomic.Int32
	chatCalls   atomic.Int32
	closed      atomic.Int32
	chunks      []string
	response    *providers.LLMResponse
	err         error
	block       bool
	plans       []instanceStreamingPlan
}

type instanceStreamingPlan struct {
	chunks   []string
	response *providers.LLMResponse
	err      error
}

func (p *instanceStreamingProvider) Chat(context.Context, []providers.Message, []providers.ToolDefinition, string, map[string]any) (*providers.LLMResponse, error) {
	p.chatCalls.Add(1)
	return p.response, p.err
}

func (p *instanceStreamingProvider) ChatStream(ctx context.Context, _ []providers.Message, _ []providers.ToolDefinition, _ string, _ map[string]any, onChunk func(string)) (*providers.LLMResponse, error) {
	call := int(p.streamCalls.Add(1))
	if p.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	chunks := p.chunks
	response := p.response
	streamErr := p.err
	if call <= len(p.plans) {
		plan := p.plans[call-1]
		chunks, response, streamErr = plan.chunks, plan.response, plan.err
	}
	for _, chunk := range chunks {
		onChunk(chunk)
	}
	return response, streamErr
}

func (p *instanceStreamingProvider) GetDefaultModel() string { return "fixture" }
func (p *instanceStreamingProvider) Close()                  { p.closed.Add(1) }

type instanceStreamingRecorder struct {
	updates           []string
	finalized         []string
	canceled          int
	cleared           int
	modelNames        []string
	selectionMetadata [][3]string
}

func (s *instanceStreamingRecorder) Update(_ context.Context, content string) error {
	s.updates = append(s.updates, content)
	return nil
}
func (s *instanceStreamingRecorder) Finalize(_ context.Context, content string) error {
	s.finalized = append(s.finalized, content)
	return nil
}
func (s *instanceStreamingRecorder) Cancel(context.Context)      { s.canceled++ }
func (s *instanceStreamingRecorder) ClearFinalizedStreamMarker() { s.cleared++ }
func (s *instanceStreamingRecorder) SetModelName(name string) {
	s.modelNames = append(s.modelNames, name)
}
func (s *instanceStreamingRecorder) SetSelectionMetadata(requested, target, identity string) {
	s.selectionMetadata = append(s.selectionMetadata, [3]string{requested, target, identity})
}

type instanceStreamingDelegate struct{ streamer bus.Streamer }

func (d instanceStreamingDelegate) GetStreamer(context.Context, string, string, string) (bus.Streamer, bool) {
	return d.streamer, d.streamer != nil
}

func newInstanceStreamingLoop(
	t *testing.T,
	providersByInstance map[string]*instanceStreamingProvider,
	streamer bus.Streamer,
) (*AgentLoop, *bus.MessageBus) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ModelName = "default-model"
	cfg.Agents.Defaults.MaxToolIterations = 3
	cfg.Channels = config.ChannelsConfig{"web": newConfiguredStreamingWebChannel(t, true)}
	if err := config.InitChannelList(cfg.Channels); err != nil {
		t.Fatal(err)
	}
	resolver := func(_ *config.Config, selection string) (*providers.InstanceResolution, error) {
		targets := []string{"first/model", "second/model"}
		return fullTurnResolution(t, selection, targets, func(instance *config.ProviderInstanceConfig, _ string, _ string) (providers.LLMProvider, error) {
			return providersByInstance[instance.ID], nil
		}), nil
	}
	msgBus := bus.NewMessageBus()
	msgBus.SetStreamDelegate(instanceStreamingDelegate{streamer: streamer})
	// The injected provider serves the default model; the route resolves per
	// message through resolver.
	al := NewAgentLoop(cfg, msgBus, &mockProvider{}, WithModelResolver(resolver))
	al.registry.GetDefaultAgent().Tools.Register(&mockCustomTool{})
	return al, msgBus
}

func TestInstanceRouteStreamingFallsBackBeforeVisibleOutput(t *testing.T) {
	primary := &instanceStreamingProvider{instanceID: "first", err: transientProviderError("before output")}
	fallback := &instanceStreamingProvider{instanceID: "second", chunks: []string{"winning"}, response: &providers.LLMResponse{Content: "winning"}}
	streamer := &instanceStreamingRecorder{}
	providersByInstance := map[string]*instanceStreamingProvider{"first": primary, "second": fallback}
	al, _ := newInstanceStreamingLoop(t, providersByInstance, streamer)

	response, err := al.processMessage(context.Background(), selectedWebMessage("stream-fallback", "route", "answer"))
	if err != nil || response != "winning" {
		t.Fatalf("response=%q error=%v", response, err)
	}
	if primary.streamCalls.Load() != 1 || fallback.streamCalls.Load() != 1 || primary.chatCalls.Load() != 0 || fallback.chatCalls.Load() != 0 {
		t.Fatalf("stream/chat calls primary=%d/%d fallback=%d/%d", primary.streamCalls.Load(), primary.chatCalls.Load(), fallback.streamCalls.Load(), fallback.chatCalls.Load())
	}
	if streamer.canceled != 1 || streamer.cleared != 1 || len(streamer.finalized) != 1 || streamer.finalized[0] != "winning" {
		t.Fatalf("stream lifecycle canceled=%d cleared=%d finalized=%v", streamer.canceled, streamer.cleared, streamer.finalized)
	}
	if len(streamer.updates) != 1 || streamer.updates[0] != "winning" || streamer.modelNames[len(streamer.modelNames)-1] != "second/model" {
		t.Fatalf("stream updates=%v model names=%v", streamer.updates, streamer.modelNames)
	}
	if len(streamer.selectionMetadata) != 1 || streamer.selectionMetadata[0][0] != "route" || streamer.selectionMetadata[0][1] != "second/model" || streamer.selectionMetadata[0][2] == "" {
		t.Fatalf("stream selection metadata=%v", streamer.selectionMetadata)
	}
	history := al.registry.GetDefaultAgent().Sessions.GetHistory(session.BuildOpaqueSessionKey("stream-fallback"))
	final := history[len(history)-1]
	if final.ServedTarget != "second/model" || final.ServedIdentity == "" || final.RequestedSelection != "route" {
		t.Fatalf("winning metadata = %#v", final)
	}
	if primary.closed.Load() != 1 || fallback.closed.Load() != 1 {
		t.Fatalf("closed primary=%d fallback=%d", primary.closed.Load(), fallback.closed.Load())
	}
}

func TestInstanceRouteStreamingNeverFallsBackAfterVisibleOutput(t *testing.T) {
	primary := &instanceStreamingProvider{instanceID: "first", chunks: []string{"visible"}, err: errors.New("after output")}
	fallback := &instanceStreamingProvider{instanceID: "second", chunks: []string{"must not run"}, response: &providers.LLMResponse{Content: "must not run"}}
	streamer := &instanceStreamingRecorder{}
	al, _ := newInstanceStreamingLoop(t, map[string]*instanceStreamingProvider{"first": primary, "second": fallback}, streamer)

	_, err := al.processMessage(context.Background(), selectedWebMessage("stream-visible", "route", "answer"))
	if err == nil || !isAfterOutputError(err) {
		t.Fatalf("error=%v, want configured streaming visible error", err)
	}
	if fallback.streamCalls.Load() != 0 || fallback.chatCalls.Load() != 0 || fallback.closed.Load() != 0 {
		t.Fatalf("fallback calls=%d/%d closed=%d", fallback.streamCalls.Load(), fallback.chatCalls.Load(), fallback.closed.Load())
	}
	if len(streamer.updates) != 1 || streamer.updates[0] != "visible" || len(streamer.finalized) != 0 {
		t.Fatalf("updates=%v finalized=%v", streamer.updates, streamer.finalized)
	}
	if primary.closed.Load() != 1 {
		t.Fatalf("primary closed=%d", primary.closed.Load())
	}
}

func TestInstanceRouteStreamingCancellationStopsAndCleansPrimary(t *testing.T) {
	primary := &instanceStreamingProvider{instanceID: "first", block: true}
	fallback := &instanceStreamingProvider{instanceID: "second"}
	streamer := &instanceStreamingRecorder{}
	al, _ := newInstanceStreamingLoop(t, map[string]*instanceStreamingProvider{"first": primary, "second": fallback}, streamer)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := al.processMessage(ctx, selectedWebMessage("stream-cancel", "route", "answer"))
		done <- err
	}()
	deadline := time.Now().Add(time.Second)
	for primary.streamCalls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	if fallback.streamCalls.Load() != 0 || fallback.closed.Load() != 0 || primary.closed.Load() != 1 {
		t.Fatalf("primary closed=%d fallback calls=%d closed=%d", primary.closed.Load(), fallback.streamCalls.Load(), fallback.closed.Load())
	}
}

func TestInstanceRouteStreamingPreservesWinnerThroughToolLoop(t *testing.T) {
	primary := &instanceStreamingProvider{
		instanceID: "first",
		err:        transientProviderError("before output"),
	}
	fallback := &instanceStreamingProvider{
		instanceID: "second",
		plans: []instanceStreamingPlan{
			{response: &providers.LLMResponse{ToolCalls: []providers.ToolCall{{
				ID: "call-1", Type: "function", Name: "mock_custom", Arguments: map[string]any{},
			}}}},
			{chunks: []string{"tool result answer"}, response: &providers.LLMResponse{Content: "tool result answer"}},
		},
	}
	streamer := &instanceStreamingRecorder{}
	al, _ := newInstanceStreamingLoop(t, map[string]*instanceStreamingProvider{
		"first": primary, "second": fallback,
	}, streamer)

	response, err := al.processMessage(context.Background(), selectedWebMessage("stream-tool", "route", "use tool"))
	if err != nil || response != "tool result answer" {
		t.Fatalf("response=%q error=%v", response, err)
	}
	// One failure does not open the primary's circuit, so the tool loop's
	// second call tries it again before the fallback serves it.
	if primary.streamCalls.Load() != 2 || fallback.streamCalls.Load() != 2 {
		t.Fatalf("stream calls primary=%d fallback=%d", primary.streamCalls.Load(), fallback.streamCalls.Load())
	}
	// Each failed primary attempt cancels and clears its preview; the tool
	// call round cancels one more.
	if streamer.canceled != 3 || streamer.cleared != 2 || len(streamer.finalized) != 1 || streamer.finalized[0] != "tool result answer" {
		t.Fatalf("stream lifecycle canceled=%d cleared=%d finalized=%v", streamer.canceled, streamer.cleared, streamer.finalized)
	}
	history := al.registry.GetDefaultAgent().Sessions.GetHistory(session.BuildOpaqueSessionKey("stream-tool"))
	final := history[len(history)-1]
	if final.ServedTarget != "second/model" || final.RequestedSelection != "route" || final.ServedIdentity == "" {
		t.Fatalf("final identity=%#v", final)
	}
	if primary.closed.Load() != 1 || fallback.closed.Load() != 1 {
		t.Fatalf("closed primary=%d fallback=%d", primary.closed.Load(), fallback.closed.Load())
	}
}
