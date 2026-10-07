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
	"github.com/xibodev/compa/v3/pkg/providers"
	"github.com/xibodev/compa/v3/pkg/session"
)

type fullTurnInstanceProvider struct {
	mu        sync.Mutex
	calls     int
	messages  [][]providers.Message
	closed    atomic.Int32
	block     bool
	failFirst bool
	direct    bool
}

func (p *fullTurnInstanceProvider) Chat(ctx context.Context, messages []providers.Message, tools []providers.ToolDefinition, model string, options map[string]any) (*providers.LLMResponse, error) {
	p.mu.Lock()
	p.calls++
	p.messages = append(p.messages, append([]providers.Message(nil), messages...))
	call := p.calls
	p.mu.Unlock()
	if p.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if p.failFirst && call == 1 {
		return nil, transientProviderError("fixture unavailable")
	}
	if p.direct {
		return &providers.LLMResponse{Content: "selected final"}, nil
	}
	if call == 1 {
		return &providers.LLMResponse{ToolCalls: []providers.ToolCall{{
			ID: "call-1", Type: "function", Name: "mock_custom", Arguments: map[string]any{},
		}}}, nil
	}
	return &providers.LLMResponse{Content: "selected final"}, nil
}

func TestInstanceSelectionChangesWithinOneSession(t *testing.T) {
	providerByTarget := map[string]*fullTurnInstanceProvider{
		"first/model":  {direct: true},
		"second/model": {direct: true},
	}
	resolver := func(_ *config.Config, selection string) (*providers.InstanceResolution, error) {
		return fullTurnResolution(t, selection, []string{selection}, func(instance *config.ProviderInstanceConfig, model string, _ string) (providers.LLMProvider, error) {
			return providerByTarget[instance.ID+"/"+model], nil
		}), nil
	}
	al, _ := newFullTurnSelectionLoop(t, resolver)
	for _, selection := range []string{"first/model", "second/model"} {
		if _, err := al.processMessage(context.Background(), selectedWebMessage("same-session", selection, selection)); err != nil {
			t.Fatal(err)
		}
	}
	history := al.registry.GetDefaultAgent().Sessions.GetHistory(session.BuildOpaqueSessionKey("same-session"))
	if len(history) != 4 || history[0].RequestedSelection != "first/model" || history[1].ServedTarget != "first/model" || history[2].RequestedSelection != "second/model" || history[3].ServedTarget != "second/model" {
		t.Fatalf("history selection continuity = %#v", history)
	}
}

func TestInstanceSelectionIncludesPriorTurnHistory(t *testing.T) {
	provider := &fullTurnInstanceProvider{direct: true}
	selection := "first/model"
	resolution := fullTurnResolution(t, selection, []string{selection}, func(*config.ProviderInstanceConfig, string, string) (providers.LLMProvider, error) {
		return provider, nil
	})
	al, _ := newFullTurnSelectionLoop(t, func(*config.Config, string) (*providers.InstanceResolution, error) { return resolution, nil })

	for _, content := range []string{"first turn marker", "second turn marker"} {
		if _, err := al.processMessage(context.Background(), selectedWebMessage("history-session", selection, content)); err != nil {
			t.Fatal(err)
		}
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.messages) != 2 {
		t.Fatalf("provider calls = %d, want 2", len(provider.messages))
	}
	found := false
	for _, message := range provider.messages[1] {
		if strings.Contains(message.Content, "first turn marker") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("second turn messages omit first turn history: %#v", provider.messages[1])
	}
}

func TestInstanceSelectionRejectsHookRewrite(t *testing.T) {
	provider := &fullTurnInstanceProvider{direct: true}
	selection := "first/model"
	resolution := fullTurnResolution(t, selection, []string{selection}, func(*config.ProviderInstanceConfig, string, string) (providers.LLMProvider, error) {
		return provider, nil
	})
	al, _ := newFullTurnSelectionLoop(t, func(*config.Config, string) (*providers.InstanceResolution, error) { return resolution, nil })
	if err := al.MountHook(NamedHook("instance-rewrite", modelRewriteHook{model: "other/model"})); err != nil {
		t.Fatal(err)
	}
	_, err := al.processMessage(context.Background(), selectedWebMessage("hook-session", selection, "rewrite"))
	if err == nil ||
		!strings.Contains(err.Error(), `a before_llm hook changed the model to "other/model"`) ||
		!strings.Contains(err.Error(), `the model it selected ("first/model")`) {
		t.Fatalf("hook rewrite error = %v", err)
	}
	if provider.calls != 0 {
		t.Fatalf("provider calls = %d, want none after the rejected rewrite", provider.calls)
	}
}

// A message that selected its model runs on it with media too: the image
// model reroutes only turns on the agent's own model.
func TestInstanceSelectionRunsMediaTurnOnSelectedTarget(t *testing.T) {
	provider := &fullTurnInstanceProvider{direct: true}
	visionProvider := &fullTurnInstanceProvider{direct: true}
	selection := "first/model"
	resolution := fullTurnResolution(t, selection, []string{selection}, func(*config.ProviderInstanceConfig, string, string) (providers.LLMProvider, error) {
		return provider, nil
	})
	vision := fullTurnResolution(t, "vision/model", []string{"vision/model"}, func(*config.ProviderInstanceConfig, string, string) (providers.LLMProvider, error) {
		return visionProvider, nil
	})
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ModelName = "default-model"
	cfg.Agents.Defaults.ImageModel = "vision/model"
	al := NewAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{}, WithModelResolver(
		func(_ *config.Config, requested string) (*providers.InstanceResolution, error) {
			if requested == "vision/model" {
				return vision, nil
			}
			return resolution, nil
		},
	))
	if images := al.registry.GetDefaultAgent().ImageCandidates; len(images) != 1 {
		t.Fatalf("image candidates = %#v, want the image model", images)
	}

	mediaMessage := selectedWebMessage("media-session", selection, "inspect")
	mediaMessage.Media = []string{"data:image/png;base64,iVBORw0KGgo="}
	response, err := al.processMessage(context.Background(), mediaMessage)
	if err != nil {
		t.Fatalf("media turn error = %v", err)
	}
	if response != "selected final" {
		t.Fatalf("response = %q, want the selected target's answer", response)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.calls != 1 || visionProvider.calls != 0 {
		t.Fatalf("calls = selected %d, image model %d; want the selected target only", provider.calls, visionProvider.calls)
	}
	sawMedia := false
	for _, message := range provider.messages[0] {
		if len(message.Media) > 0 {
			sawMedia = true
		}
	}
	if !sawMedia {
		t.Fatalf("selected target got no media: %#v", provider.messages[0])
	}
	history := al.registry.GetDefaultAgent().Sessions.GetHistory(session.BuildOpaqueSessionKey("media-session"))
	final := history[len(history)-1]
	if final.ServedTarget != selection {
		t.Fatalf("served target = %q, want %q", final.ServedTarget, selection)
	}
}

func (p *fullTurnInstanceProvider) GetDefaultModel() string { return "fixture" }
func (p *fullTurnInstanceProvider) Close()                  { p.closed.Add(1) }

func fullTurnResolution(t *testing.T, selection string, targets []string, factory providers.InstanceProviderFactory) *providers.InstanceResolution {
	t.Helper()
	cfg := &config.Config{}
	catalogs := make(map[string]providers.InstanceCatalog, len(targets))
	for _, raw := range targets {
		target, err := config.ParseExactModelTarget(raw)
		if err != nil {
			t.Fatal(err)
		}
		cfg.ProviderInstances = append(cfg.ProviderInstances, &config.ProviderInstanceConfig{
			ID: target.InstanceID, ProviderKind: "openai", Adapter: "fixture", Protocol: "openai",
			State:   config.ProviderInstanceStateEnabled,
			Runtime: &config.ProviderInstanceRuntime{Streaming: boolPtr(true)},
		})
		catalogs[target.InstanceID] = providers.InstanceCatalog{InstanceID: target.InstanceID, Models: []string{target.ModelID}}
	}
	if len(targets) > 1 {
		cfg.ModelRoutes = []*config.ModelRouteConfig{{Name: selection, Targets: targets}}
	}
	resolved, err := providers.ResolveInstanceTargetOrRoute(cfg, catalogs, selection, nil, factory)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func newFullTurnSelectionLoop(t *testing.T, resolver ModelResolver) (*AgentLoop, *bus.MessageBus) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ModelName = "default-model"
	cfg.Agents.Defaults.MaxToolIterations = 4
	msgBus := bus.NewMessageBus()
	// The injected provider serves the default model; per-message
	// selections resolve through resolver.
	al := NewAgentLoop(cfg, msgBus, &mockProvider{}, WithModelResolver(resolver))
	al.registry.GetDefaultAgent().Tools.Register(&mockCustomTool{})
	return al, msgBus
}

// selectedWebMessage builds a web chat message for the named session; the session
// key it carries is the opaque key derived from the name.
func selectedWebMessage(sessionName, selection, content string) bus.InboundMessage {
	return bus.InboundMessage{
		Context: bus.InboundContext{
			Channel: "web", ChatID: "web:" + sessionName, ChatType: "direct", SenderID: "web-user",
			Raw: map[string]string{bus.MetadataKeyModelSelection: selection},
		},
		Content: content, SessionKey: session.BuildOpaqueSessionKey(sessionName),
	}
}

func TestInstanceSelectionWebFullTurnToolLoopHistoryAndOutbound(t *testing.T) {
	provider := &fullTurnInstanceProvider{}
	selection := "first/model"
	resolution := fullTurnResolution(t, selection, []string{selection}, func(*config.ProviderInstanceConfig, string, string) (providers.LLMProvider, error) {
		return provider, nil
	})
	al, msgBus := newFullTurnSelectionLoop(t, func(*config.Config, string) (*providers.InstanceResolution, error) { return resolution, nil })

	al.runTurnWithSteering(context.Background(), selectedWebMessage("session-1", selection, "use the tool"))
	if provider.calls != 2 || provider.closed.Load() != 1 {
		t.Fatalf("provider calls=%d closed=%d", provider.calls, provider.closed.Load())
	}
	history := al.registry.GetDefaultAgent().Sessions.GetHistory(session.BuildOpaqueSessionKey("session-1"))
	if len(history) < 4 {
		t.Fatalf("history = %#v", history)
	}
	if history[0].RequestedSelection != selection {
		t.Fatalf("user requested selection = %q", history[0].RequestedSelection)
	}
	final := history[len(history)-1]
	if final.ModelName != selection || final.RequestedSelection != selection || final.ServedTarget != selection || final.ServedIdentity == "" {
		t.Fatalf("final history identity = %#v", final)
	}

	deadline := time.After(time.Second)
	for {
		select {
		case outbound := <-msgBus.OutboundChan():
			if outbound.Content != "selected final" {
				continue
			}
			if outbound.Context.Raw[bus.MetadataKeyModelSelection] != selection || outbound.Context.Raw[bus.MetadataKeyServedTarget] != selection || outbound.Context.Raw[bus.MetadataKeyServedIdentity] == "" {
				t.Fatalf("outbound identity = %#v", outbound.Context.Raw)
			}
			return
		case <-deadline:
			t.Fatal("final web chat outbound not published")
		}
	}
}

func TestInstanceSelectionOrderedFallbackIsLazyAndOwned(t *testing.T) {
	primary := &fullTurnInstanceProvider{failFirst: true}
	fallback := &fullTurnInstanceProvider{calls: 1}
	created := make(map[string]int)
	resolution := fullTurnResolution(t, "route", []string{"first/model", "second/model"}, func(instance *config.ProviderInstanceConfig, _ string, _ string) (providers.LLMProvider, error) {
		created[instance.ID]++
		if instance.ID == "first" {
			return primary, nil
		}
		return fallback, nil
	})
	al, _ := newFullTurnSelectionLoop(t, func(*config.Config, string) (*providers.InstanceResolution, error) { return resolution, nil })

	response, err := al.processMessage(context.Background(), selectedWebMessage("fallback-session", "route", "answer"))
	if err != nil || response != "selected final" {
		t.Fatalf("response=%q error=%v", response, err)
	}
	if created["first"] != 1 || created["second"] != 1 || primary.closed.Load() != 1 || fallback.closed.Load() != 1 {
		t.Fatalf("created=%v closed=%d/%d", created, primary.closed.Load(), fallback.closed.Load())
	}
	history := al.registry.GetDefaultAgent().Sessions.GetHistory(session.BuildOpaqueSessionKey("fallback-session"))
	final := history[len(history)-1]
	if final.RequestedSelection != "route" || final.ServedTarget != "second/model" || final.ServedIdentity == "" {
		t.Fatalf("fallback identity = %#v", final)
	}
}

func TestInstanceSelectionSuccessfulPrimaryDoesNotConstructFallback(t *testing.T) {
	primary := &fullTurnInstanceProvider{direct: true}
	created := make(map[string]int)
	resolution := fullTurnResolution(t, "route", []string{"first/model", "second/model"}, func(instance *config.ProviderInstanceConfig, _ string, _ string) (providers.LLMProvider, error) {
		created[instance.ID]++
		return primary, nil
	})
	al, _ := newFullTurnSelectionLoop(t, func(*config.Config, string) (*providers.InstanceResolution, error) { return resolution, nil })
	if _, err := al.processMessage(context.Background(), selectedWebMessage("lazy-session", "route", "answer")); err != nil {
		t.Fatal(err)
	}
	if created["first"] != 1 || created["second"] != 0 || primary.closed.Load() != 1 {
		t.Fatalf("created=%v primary closed=%d", created, primary.closed.Load())
	}
}

func TestInstanceSelectionOverlappingTurnsOwnProvidersAndCancel(t *testing.T) {
	var createdMu sync.Mutex
	var created []*fullTurnInstanceProvider
	resolver := func(*config.Config, string) (*providers.InstanceResolution, error) {
		return fullTurnResolution(t, "first/model", []string{"first/model"}, func(*config.ProviderInstanceConfig, string, string) (providers.LLMProvider, error) {
			provider := &fullTurnInstanceProvider{block: true}
			createdMu.Lock()
			created = append(created, provider)
			createdMu.Unlock()
			return provider, nil
		}), nil
	}
	al, _ := newFullTurnSelectionLoop(t, resolver)
	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, cancel2 := context.WithCancel(context.Background())
	errs := make(chan error, 2)
	go func() {
		_, err := al.processMessage(ctx1, selectedWebMessage("one", "first/model", "one"))
		errs <- err
	}()
	go func() {
		_, err := al.processMessage(ctx2, selectedWebMessage("two", "first/model", "two"))
		errs <- err
	}()
	deadline := time.Now().Add(time.Second)
	for {
		createdMu.Lock()
		count := len(created)
		createdMu.Unlock()
		if count == 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel1()
	cancel2()
	for range 2 {
		if err := <-errs; !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v", err)
		}
	}
	createdMu.Lock()
	defer createdMu.Unlock()
	if len(created) != 2 || created[0] == created[1] || created[0].closed.Load() != 1 || created[1].closed.Load() != 1 {
		t.Fatalf("providers = %#v closed=%d/%d", created, created[0].closed.Load(), created[1].closed.Load())
	}
}
