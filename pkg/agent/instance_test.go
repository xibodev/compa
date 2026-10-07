package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xibodev/compa/v3/pkg/config"
	"github.com/xibodev/compa/v3/pkg/media"
	"github.com/xibodev/compa/v3/pkg/providers"
	"github.com/xibodev/compa/v3/pkg/tools"
)

type countingStatefulProvider struct {
	closeCount int
}

func (p *countingStatefulProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	options map[string]any,
) (*providers.LLMResponse, error) {
	return &providers.LLMResponse{Content: "ok"}, nil
}

func (p *countingStatefulProvider) GetDefaultModel() string { return "test-model" }

func (p *countingStatefulProvider) Close() { p.closeCount++ }

func TestAgentInstanceCloseClosesSharedStatefulProviderOnce(t *testing.T) {
	provider := &countingStatefulProvider{}
	agent := &AgentInstance{
		Provider:      provider,
		LightProvider: provider,
		CandidateProviders: map[string]providers.LLMProvider{
			"candidate": provider,
			"runtime":   provider,
		},
	}

	if err := agent.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if provider.closeCount != 1 {
		t.Fatalf("close count = %d, want 1", provider.closeCount)
	}
}

func TestNewAgentInstance_UsesDefaultsTemperatureAndMaxTokens(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-instance-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				ModelName:         "test-model",
				MaxTokens:         1234,
				MaxToolIterations: 5,
			},
		},
	}

	configuredTemp := 1.0
	cfg.Agents.Defaults.Temperature = &configuredTemp

	provider := &mockProvider{}
	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, provider, nil)

	if agent.MaxTokens != 1234 {
		t.Fatalf("MaxTokens = %d, want %d", agent.MaxTokens, 1234)
	}
	if agent.Temperature != 1.0 {
		t.Fatalf("Temperature = %f, want %f", agent.Temperature, 1.0)
	}
}

func TestNewAgentInstance_DefaultsTemperatureWhenZero(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-instance-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				ModelName:         "test-model",
				MaxTokens:         1234,
				MaxToolIterations: 5,
			},
		},
	}

	configuredTemp := 0.0
	cfg.Agents.Defaults.Temperature = &configuredTemp

	provider := &mockProvider{}
	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, provider, nil)

	if agent.Temperature != 0.0 {
		t.Fatalf("Temperature = %f, want %f", agent.Temperature, 0.0)
	}
}

func TestNewAgentInstance_DefaultsTemperatureWhenUnset(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-instance-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				ModelName:         "test-model",
				MaxTokens:         1234,
				MaxToolIterations: 5,
			},
		},
	}

	provider := &mockProvider{}
	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, provider, nil)

	if agent.Temperature != 0.7 {
		t.Fatalf("Temperature = %f, want %f", agent.Temperature, 0.7)
	}
}

func TestNewAgentInstance_ResolvesExactTargetOnItsInstance(t *testing.T) {
	cfg := newModelTestConfig(t, "openrouter/stepfun/step-3.5-flash:free")
	provider := &mockProvider{}
	resolve := testModelResolver(cfg, nil, oneModel("openrouter", "stepfun/step-3.5-flash:free", provider).
		withRuntime(config.ProviderInstanceRuntime{ThinkingLevel: "medium", RPM: 30}))

	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, nil, resolve)

	if agent.Model != "openrouter/stepfun/step-3.5-flash:free" {
		t.Fatalf("agent.Model = %q, want the selection", agent.Model)
	}
	if len(agent.Candidates) != 1 {
		t.Fatalf("len(Candidates) = %d, want 1", len(agent.Candidates))
	}
	candidate := agent.Candidates[0]
	if candidate.Model != "stepfun/step-3.5-flash:free" || candidate.DisplayName != agent.Model || candidate.RPM != 30 {
		t.Fatalf("candidate = %#v, want the target's model ID, name and instance RPM", candidate)
	}
	if agent.Provider != provider || agent.CandidateProviders[candidate.StableKey()] != provider {
		t.Fatal("the agent does not run on its target's provider")
	}
	if agent.ThinkingLevel != ThinkingMedium || !agent.ThinkingLevelConfigured {
		t.Fatalf("thinking = %q configured=%v, want the instance runtime's medium", agent.ThinkingLevel, agent.ThinkingLevelConfigured)
	}
}

// Two instances serving the same model ID stay distinct targets: each has
// its own identity, provider and RPM, so a route fails over and rate-limits
// between them.
func TestNewAgentInstance_RouteTargetsOnTheSameModelKeepDistinctIdentities(t *testing.T) {
	cfg := newModelTestConfig(t, "glm")
	first, second := &mockProvider{}, &mockProvider{}
	resolve := testModelResolver(cfg, nil,
		oneModel("zhipu-a", "glm-4.7", first).withRuntime(config.ProviderInstanceRuntime{RPM: 1}),
		oneModel("zhipu-b", "glm-4.7", second).withRuntime(config.ProviderInstanceRuntime{RPM: 3}),
	)
	addTestRoute(cfg, "glm", "zhipu-a/glm-4.7", "zhipu-b/glm-4.7")

	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, nil, resolve)
	if len(agent.Candidates) != 2 {
		t.Fatalf("len(Candidates) = %d, want 2", len(agent.Candidates))
	}
	a, b := agent.Candidates[0], agent.Candidates[1]
	if a.Model != "glm-4.7" || b.Model != "glm-4.7" {
		t.Fatalf("candidate models = %q/%q, want glm-4.7 on both", a.Model, b.Model)
	}
	if a.StableKey() == b.StableKey() {
		t.Fatalf("both targets share the identity %q", a.StableKey())
	}
	if a.RPM != 1 || b.RPM != 3 {
		t.Fatalf("RPMs = %d/%d, want each instance's own", a.RPM, b.RPM)
	}
	if agent.CandidateProviders[a.StableKey()] != first || agent.CandidateProviders[b.StableKey()] != second {
		t.Fatal("route targets do not run on their own instances' providers")
	}
}

func TestNewAgentInstance_WithoutAModelHasNoCandidates(t *testing.T) {
	t.Run("empty selection", func(t *testing.T) {
		cfg := newModelTestConfig(t, "")
		agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, nil, testModelResolver(cfg, nil))
		if agent.hasModel() || len(agent.Candidates) != 0 || agent.Provider != nil {
			t.Fatalf("agent has a model: candidates=%#v provider=%T", agent.Candidates, agent.Provider)
		}
		if got := agent.noModelError().Error(); got != noModelSelectedMessage {
			t.Fatalf("no-model error = %q", got)
		}
	})

	t.Run("selection that does not resolve", func(t *testing.T) {
		cfg := newModelTestConfig(t, "gone/model")
		agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, nil, testModelResolver(cfg, nil))
		if agent.hasModel() {
			t.Fatal("agent has a model for a selection that does not resolve")
		}
		if agent.Model != "gone/model" {
			t.Fatalf("agent.Model = %q, want the configured selection kept", agent.Model)
		}
		if got := agent.noModelError().Error(); !strings.Contains(got, `Model "gone/model" is not available`) ||
			!strings.Contains(got, `provider instance "gone" not found`) {
			t.Fatalf("no-model error = %q", got)
		}
	})
}

// The injected provider serves only the default selection; an agent whose
// own selection differs resolves it.
func TestNewAgentInstance_InjectedProviderServesOnlyTheDefaultSelection(t *testing.T) {
	cfg := newModelTestConfig(t, "test-model")
	injected := &mockProvider{}
	own := &mockProvider{}
	resolve := testModelResolver(cfg, nil, oneModel("own", "model", own))

	defaulted := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, injected, resolve)
	if defaulted.Provider != injected || len(defaulted.Candidates) != 1 ||
		defaulted.Candidates[0].DisplayName != "test-model" || defaulted.Candidates[0].Model != "test-model" {
		t.Fatalf("default agent candidates = %#v provider = %T, want the injected provider", defaulted.Candidates, defaulted.Provider)
	}

	custom := NewAgentInstance(&config.AgentConfig{ID: "custom", Workspace: t.TempDir(), Model: "own/model"},
		&cfg.Agents.Defaults, cfg, injected, resolve)
	if custom.Provider != own {
		t.Fatalf("custom agent provider = %T, want its own target's", custom.Provider)
	}
}

func TestNewAgentInstance_ImageAndLightModelsResolveThroughTheResolver(t *testing.T) {
	cfg := newModelTestConfig(t, "main/model")
	cfg.Agents.Defaults.ImageModel = "vision/model"
	cfg.Agents.Defaults.Routing = &config.RoutingConfig{Enabled: true, LightModel: "fast/model", Threshold: 0.4}
	mainProvider, visionProvider, fastProvider := &mockProvider{}, &mockProvider{}, &mockProvider{}
	resolve := testModelResolver(cfg, nil,
		oneModel("main", "model", mainProvider),
		oneModel("vision", "model", visionProvider),
		oneModel("fast", "model", fastProvider),
	)

	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, nil, resolve)

	if len(agent.ImageCandidates) != 1 || agent.ImageCandidates[0].DisplayName != "vision/model" {
		t.Fatalf("image candidates = %#v", agent.ImageCandidates)
	}
	if agent.CandidateProviders[agent.ImageCandidates[0].StableKey()] != visionProvider {
		t.Fatal("the image model does not run on its own provider")
	}
	if agent.Router == nil || agent.Router.LightModel() != "fast/model" {
		t.Fatalf("router = %#v, want routing to the light model selection", agent.Router)
	}
	if len(agent.LightCandidates) != 1 || agent.LightProvider != fastProvider {
		t.Fatalf("light candidates = %#v provider = %T", agent.LightCandidates, agent.LightProvider)
	}
	if agent.Provider != mainProvider {
		t.Fatalf("primary provider = %T, want the agent model's", agent.Provider)
	}
}

func TestNewAgentInstance_LightModelThatDoesNotResolveDisablesRouting(t *testing.T) {
	cfg := newModelTestConfig(t, "main/model")
	cfg.Agents.Defaults.Routing = &config.RoutingConfig{Enabled: true, LightModel: "missing-route", Threshold: 0.4}
	resolve := testModelResolver(cfg, nil, oneModel("main", "model", &mockProvider{}))

	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, nil, resolve)
	if agent.Router != nil || len(agent.LightCandidates) != 0 {
		t.Fatalf("router = %#v light = %#v, want routing disabled", agent.Router, agent.LightCandidates)
	}
	if !agent.hasModel() {
		t.Fatal("the agent lost its own model")
	}
}

func TestNewAgentInstance_AllowsMediaTempDirForReadListAndExec(t *testing.T) {
	workspace := t.TempDir()
	mediaDir := media.TempDir()
	if err := os.MkdirAll(mediaDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(mediaDir) error = %v", err)
	}

	mediaFile, err := os.CreateTemp(mediaDir, "instance-tool-*.txt")
	if err != nil {
		t.Fatalf("CreateTemp(mediaDir) error = %v", err)
	}
	mediaPath := mediaFile.Name()
	if _, err := mediaFile.WriteString("attachment content"); err != nil {
		mediaFile.Close()
		t.Fatalf("WriteString(mediaFile) error = %v", err)
	}
	if err := mediaFile.Close(); err != nil {
		t.Fatalf("Close(mediaFile) error = %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(mediaPath) })

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:           workspace,
				ModelName:           "test-model",
				RestrictToWorkspace: true,
			},
		},
		Tools: config.ToolsConfig{
			ReadFile: config.ReadFileToolConfig{Enabled: true},
			ListDir:  config.ToolConfig{Enabled: true},
			Exec: config.ExecConfig{
				ToolConfig:         config.ToolConfig{Enabled: true},
				EnableDenyPatterns: true,
				AllowRemote:        true,
			},
		},
	}

	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{}, nil)

	readTool, ok := agent.Tools.Get("read_file")
	if !ok {
		t.Fatal("read_file tool not registered")
	}
	readResult := readTool.Execute(context.Background(), map[string]any{"path": mediaPath})
	if readResult.IsError {
		t.Fatalf("read_file should allow media temp dir, got: %s", readResult.ForLLM)
	}
	if !strings.Contains(readResult.ForLLM, "attachment content") {
		t.Fatalf("read_file output missing media content: %s", readResult.ForLLM)
	}

	listTool, ok := agent.Tools.Get("list_dir")
	if !ok {
		t.Fatal("list_dir tool not registered")
	}
	listResult := listTool.Execute(context.Background(), map[string]any{"path": mediaDir})
	if listResult.IsError {
		t.Fatalf("list_dir should allow media temp dir, got: %s", listResult.ForLLM)
	}
	if !strings.Contains(listResult.ForLLM, filepath.Base(mediaPath)) {
		t.Fatalf("list_dir output missing media file: %s", listResult.ForLLM)
	}

	execTool, ok := agent.Tools.Get("exec")
	if !ok {
		t.Fatal("exec tool not registered")
	}
	execResult := execTool.Execute(context.Background(), map[string]any{
		"action":  "run",
		"command": "cat " + filepath.Base(mediaPath),
		"cwd":     mediaDir,
	})
	if execResult.IsError {
		t.Fatalf("exec should allow media temp dir, got: %s", execResult.ForLLM)
	}
	if !strings.Contains(execResult.ForLLM, "attachment content") {
		t.Fatalf("exec output missing media content: %s", execResult.ForLLM)
	}
}

// A route across instances runs each target on its own instance's provider,
// never on the primary's, so a fallback request reaches its own endpoint.
func TestNewAgentInstance_RouteTargetsRunOnTheirOwnProviders(t *testing.T) {
	cfg := newModelTestConfig(t, "cross")
	openrouter, gemini := &mockProvider{}, &mockProvider{}
	resolve := testModelResolver(cfg, nil,
		oneModel("openrouter", "mistralai/mistral-small-3.1-24b-instruct:free", openrouter),
		testInstance{id: "gemini", models: map[string]providers.LLMProvider{
			"gemma-3-27b-it":        gemini,
			"gemini-2.5-flash-lite": gemini,
		}},
	)
	addTestRoute(cfg, "cross",
		"openrouter/mistralai/mistral-small-3.1-24b-instruct:free",
		"gemini/gemma-3-27b-it",
		"gemini/gemini-2.5-flash-lite",
	)

	// No injected provider: one would serve the default selection itself.
	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, nil, resolve)
	if len(agent.Candidates) != 3 {
		t.Fatalf("len(Candidates) = %d, want 3", len(agent.Candidates))
	}
	if agent.Provider != openrouter {
		t.Fatalf("primary provider = %T, want the first target's", agent.Provider)
	}
	for _, candidate := range agent.Candidates[1:] {
		if provider := agent.CandidateProviders[candidate.StableKey()]; provider != gemini {
			t.Fatalf("CandidateProviders[%s] = %T, want the gemini instance's provider", candidate.DisplayName, provider)
		}
	}
}

func TestAgentInstanceSetModelsKeepsImageAndLightModels(t *testing.T) {
	cfg := newModelTestConfig(t, "main/model")
	cfg.Agents.Defaults.ImageModel = "vision/model"
	cfg.Agents.Defaults.Routing = &config.RoutingConfig{Enabled: true, LightModel: "fast/model"}
	oldPrimary := &countingStatefulProvider{}
	vision, fast, next := &countingStatefulProvider{}, &countingStatefulProvider{}, &countingStatefulProvider{}
	resolve := testModelResolver(cfg, nil,
		oneModel("main", "model", oldPrimary),
		oneModel("vision", "model", vision),
		oneModel("fast", "model", fast),
		oneModel("next", "model", next),
	)
	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, nil, resolve)

	models, err := resolveModels(cfg, resolve, "next/model")
	if err != nil {
		t.Fatalf("resolveModels() error = %v", err)
	}
	agent.setModels(models)

	if agent.Model != "next/model" || agent.Provider != next {
		t.Fatalf("model = %q provider = %T, want next/model", agent.Model, agent.Provider)
	}
	if agent.CandidateProviders[agent.ImageCandidates[0].StableKey()] != vision || agent.LightProvider != fast {
		t.Fatal("switching the model dropped the image or light model")
	}
	if oldPrimary.closeCount != 1 {
		t.Fatalf("previous primary closed %d times, want 1", oldPrimary.closeCount)
	}
	if vision.closeCount != 0 || fast.closeCount != 0 || next.closeCount != 0 {
		t.Fatal("switching the model closed a provider still in use")
	}
	if provider, model, err := agent.primaryModel(); err != nil || provider != next || model != "model" {
		t.Fatalf("primaryModel() = %T/%q/%v, want the new target", provider, model, err)
	}
}

// AG-08: a turn runs on a snapshot of the model. /switch model neither waits
// for the turn nor for its nested sub-agents, the turn keeps its model, the
// replaced provider closes when the turn ends, and the next turn switches.
func TestSwitchModelDuringTurnTakesEffectOnTheNextTurn(t *testing.T) {
	cfg := newModelTestConfig(t, "main/model")
	oldPrimary, next := &countingStatefulProvider{}, &countingStatefulProvider{}
	resolve := testModelResolver(cfg, nil,
		oneModel("main", "model", oldPrimary),
		oneModel("next", "model", next),
	)
	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, nil, resolve)
	al := &AgentLoop{cfg: cfg, resolveModel: resolve, rateLimits: newCandidateRateLimits()}

	turn, releaseTurn, _ := agent.turnSnapshot()
	child, releaseChild, _ := turn.turnSnapshot() // a synchronous sub-agent

	switched := make(chan error, 1)
	go func() {
		_, err := al.switchModel(agent, "next/model")
		switched <- err
	}()
	select {
	case err := <-switched:
		if err != nil {
			t.Fatalf("switchModel() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("/switch model waited for the running turn")
	}

	// A sub-agent the turn starts after the switch still runs on its model.
	nested, releaseNested, current := child.turnSnapshot()
	if !current {
		t.Fatal("the running turn's model was closed")
	}
	for name, snapshot := range map[string]*AgentInstance{"turn": turn, "child": child, "nested": nested} {
		if snapshot.Provider != oldPrimary || snapshot.Model != "main/model" {
			t.Fatalf("%s runs on %q, want the model it started on", name, snapshot.Model)
		}
	}
	releaseNested()
	releaseChild()
	if oldPrimary.closeCount != 0 {
		t.Fatal("/switch model closed the provider of a running turn")
	}
	releaseTurn()
	if oldPrimary.closeCount != 1 {
		t.Fatalf("replaced provider closed %d times after the turn ended, want 1", oldPrimary.closeCount)
	}

	nextTurn, releaseNext, _ := agent.turnSnapshot()
	defer releaseNext()
	if nextTurn.Provider != next || nextTurn.Model != "next/model" {
		t.Fatalf("next turn runs on %q, want next/model", nextTurn.Model)
	}
	if _, _, current := turn.turnSnapshot(); current {
		t.Fatal("a snapshot of the ended turn reported its closed model as current")
	}
}

// A before_llm hook rewrites the model to a selection; the turn resolves it
// and runs on its target.
func TestApplyBeforeLLMModelRewriteSwitchesToTheHookSelection(t *testing.T) {
	cfg := newModelTestConfig(t, "main/model")
	replacement := &mockProvider{}
	resolve := testModelResolver(cfg, nil,
		oneModel("main", "model", &mockProvider{}),
		oneModel("anthropic", "claude-sonnet", replacement).withRuntime(config.ProviderInstanceRuntime{ThinkingLevel: "off"}),
	)
	al := NewAgentLoop(cfg, nil, nil, WithModelResolver(resolve))
	defer al.Close()
	agent := al.GetRegistry().GetDefaultAgent()
	exec := newTurnExecution(agent, processOptions{}, nil, "", nil)
	exec.activeCandidates = agent.Candidates
	exec.activeProvider = agent.Provider
	exec.llmModel = "anthropic/claude-sonnet"
	pipeline := NewPipeline(al)

	if err := pipeline.applyBeforeLLMModelRewrite(&turnState{agent: agent}, exec); err != nil {
		t.Fatalf("applyBeforeLLMModelRewrite() error = %v", err)
	}
	if exec.activeProvider != replacement {
		t.Fatalf("active provider = %T, want the hook-selected target's provider", exec.activeProvider)
	}
	if exec.activeModel != "claude-sonnet" || exec.llmModel != "claude-sonnet" || exec.llmModelName != "anthropic/claude-sonnet" {
		t.Fatalf("active model = %q llm model = %q name = %q", exec.activeModel, exec.llmModel, exec.llmModelName)
	}
	if exec.activeCallSpec == nil || exec.activeCallSpec.ThinkingLevel != "off" {
		t.Fatalf("active request spec = %#v, want the hook target's runtime", exec.activeCallSpec)
	}

	exec.llmModel = "missing-route"
	if err := pipeline.applyBeforeLLMModelRewrite(&turnState{agent: agent}, exec); err == nil ||
		!strings.Contains(err.Error(), `hook-selected model "missing-route" is not available`) {
		t.Fatalf("unresolvable hook selection error = %v", err)
	}
}

func TestNewAgentInstance_ReadFileModeSelectsSchema(t *testing.T) {
	workspace := t.TempDir()

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace: workspace,
				ModelName: "test-model",
			},
		},
		Tools: config.ToolsConfig{
			ReadFile: config.ReadFileToolConfig{
				Enabled:         true,
				Mode:            config.ReadFileModeLines,
				MaxReadFileSize: 4096,
			},
		},
	}

	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{}, nil)
	readTool, ok := agent.Tools.Get("read_file")
	if !ok {
		t.Fatal("read_file tool not registered")
	}

	params := readTool.Parameters()
	props, _ := params["properties"].(map[string]any)
	if _, ok := props["start_line"]; !ok {
		t.Fatalf("expected line-mode schema to expose start_line, got %#v", props)
	}
	if _, ok := props["max_lines"]; !ok {
		t.Fatalf("expected line-mode schema to expose max_lines, got %#v", props)
	}
	if _, ok := props["offset"]; ok {
		t.Fatalf("did not expect line-mode schema to expose offset, got %#v", props)
	}
	if _, ok := props["length"]; ok {
		t.Fatalf("did not expect line-mode schema to expose length, got %#v", props)
	}
}

// write_file copy names append_file/edit_file only when they are registered.
func TestNewAgentInstance_WriteFileCopyReflectsAvailableAltTools(t *testing.T) {
	newCfg := func(editEnabled, appendEnabled bool) *config.Config {
		return &config.Config{
			Agents: config.AgentsConfig{
				Defaults: config.AgentDefaults{
					Workspace: t.TempDir(),
					ModelName: "test-model",
				},
			},
			Tools: config.ToolsConfig{
				WriteFile:  config.ToolConfig{Enabled: true},
				EditFile:   config.ToolConfig{Enabled: editEnabled},
				AppendFile: config.ToolConfig{Enabled: appendEnabled},
			},
		}
	}

	writeToolDesc := func(cfg *config.Config) string {
		agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{}, nil)
		writeTool, ok := agent.Tools.Get("write_file")
		if !ok {
			t.Fatal("write_file tool not registered")
		}
		return writeTool.Description()
	}

	t.Run("only write_file exposed", func(t *testing.T) {
		desc := writeToolDesc(newCfg(false, false))
		if strings.Contains(desc, "append_file") || strings.Contains(desc, "edit_file") {
			t.Fatalf("write_file must not reference unavailable tools, got: %q", desc)
		}
	})

	t.Run("only append_file exposed", func(t *testing.T) {
		desc := writeToolDesc(newCfg(false, true))
		if !strings.Contains(desc, "append_file") {
			t.Fatalf("expected write_file to reference append_file, got: %q", desc)
		}
		if strings.Contains(desc, "edit_file") {
			t.Fatalf("write_file must not reference disabled edit_file, got: %q", desc)
		}
	})

	t.Run("both exposed", func(t *testing.T) {
		desc := writeToolDesc(newCfg(true, true))
		if !strings.Contains(desc, "append_file") || !strings.Contains(desc, "edit_file") {
			t.Fatalf("expected write_file to reference both alternatives, got: %q", desc)
		}
	})
}

// Availability follows the per-agent allowlist, not just the enable flag:
// editors enabled globally but hidden by frontmatter must not be named.
func TestNewAgentInstance_WriteFileCopyExcludesAllowlistHiddenAltTools(t *testing.T) {
	workspace := setupWorkspace(t, map[string]string{
		"AGENT.md": "---\ntools: [write_file]\n---\n# Agent\n",
	})
	defer cleanupWorkspace(t, workspace)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace: workspace,
				ModelName: "test-model",
			},
		},
		Tools: config.ToolsConfig{
			WriteFile:  config.ToolConfig{Enabled: true},
			EditFile:   config.ToolConfig{Enabled: true},
			AppendFile: config.ToolConfig{Enabled: true},
		},
	}

	agent := NewAgentInstance(&config.AgentConfig{
		ID:        "restricted",
		Workspace: workspace,
	}, &cfg.Agents.Defaults, cfg, &mockProvider{}, nil)

	if _, ok := agent.Tools.Get("edit_file"); ok {
		t.Fatal("edit_file should be blocked by the allowlist")
	}
	if _, ok := agent.Tools.Get("append_file"); ok {
		t.Fatal("append_file should be blocked by the allowlist")
	}

	writeTool, ok := agent.Tools.Get("write_file")
	if !ok {
		t.Fatal("write_file tool not registered")
	}
	if desc := writeTool.Description(); strings.Contains(desc, "append_file") ||
		strings.Contains(desc, "edit_file") {
		t.Fatalf("write_file must not name allowlist-hidden tools, got: %q", desc)
	}
}

func TestNewAgentInstance_InvalidExecConfigDoesNotExit(t *testing.T) {
	workspace := t.TempDir()

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace: workspace,
				ModelName: "test-model",
			},
		},
		Tools: config.ToolsConfig{
			ReadFile: config.ReadFileToolConfig{Enabled: true},
			Exec: config.ExecConfig{
				ToolConfig:         config.ToolConfig{Enabled: true},
				EnableDenyPatterns: true,
				CustomDenyPatterns: []string{"[invalid-regex"},
			},
		},
	}

	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{}, nil)
	if agent == nil {
		t.Fatal("expected agent instance, got nil")
	}

	if _, ok := agent.Tools.Get("exec"); ok {
		t.Fatal("exec tool should not be registered when exec config is invalid")
	}

	if _, ok := agent.Tools.Get("read_file"); !ok {
		t.Fatal("read_file tool should still be registered")
	}
}

func TestNewAgentInstance_UsesFrontmatterModelAndSkills(t *testing.T) {
	workspace := setupWorkspace(t, map[string]string{
		"AGENT.md": `---
model: frontmatter-model
skills: [frontmatter-skill]
mcpServers: [GitHub, filesystem]
---
# Agent

Use frontmatter identity.
`,
	})
	defer cleanupWorkspace(t, workspace)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace: workspace,
				ModelName: "default-model",
			},
		},
	}

	agent := NewAgentInstance(&config.AgentConfig{
		ID:        "research",
		Workspace: workspace,
		Model:     "config-model",
		Skills:    []string{"config-skill"},
	}, &cfg.Agents.Defaults, cfg, &mockProvider{}, testModelResolver(cfg, nil))

	if agent.Model != "frontmatter-model" {
		t.Fatalf("agent.Model = %q, want frontmatter-model", agent.Model)
	}
	if len(agent.SkillsFilter) != 1 || agent.SkillsFilter[0] != "frontmatter-skill" {
		t.Fatalf("agent.SkillsFilter = %v, want [frontmatter-skill]", agent.SkillsFilter)
	}
	if !agent.AllowsMCPServer("github") {
		t.Fatal("expected github MCP server to be allowed from frontmatter")
	}
	if !agent.AllowsMCPServer("FILESYSTEM") {
		t.Fatal("expected filesystem MCP server matching to be case-insensitive")
	}
	if agent.AllowsMCPServer("slack") {
		t.Fatal("expected slack MCP server to be blocked by frontmatter allowlist")
	}
}

// A frontmatter model is the agent's selection even when an injected
// provider serves the default one: it resolves to its own target.
func TestNewAgentInstance_UsesResolvedProviderForFrontmatterModel(t *testing.T) {
	workspace := setupWorkspace(t, map[string]string{
		"AGENT.md": `---
model: anthropic/claude-3-7-sonnet
---
# Agent
`,
	})
	defer cleanupWorkspace(t, workspace)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace: workspace,
				ModelName: "default-model",
			},
		},
	}
	claude := &mockProvider{}
	resolve := testModelResolver(cfg, nil, oneModel("anthropic", "claude-3-7-sonnet", claude))

	defaultProvider := &mockProvider{}
	agent := NewAgentInstance(&config.AgentConfig{
		ID:        "research",
		Workspace: workspace,
	}, &cfg.Agents.Defaults, cfg, defaultProvider, resolve)

	if agent.Model != "anthropic/claude-3-7-sonnet" {
		t.Fatalf("agent.Model = %q, want the frontmatter selection", agent.Model)
	}
	if len(agent.Candidates) != 1 || agent.Candidates[0].Model != "claude-3-7-sonnet" {
		t.Fatalf("candidates = %#v, want the frontmatter target", agent.Candidates)
	}
	if agent.Provider != claude {
		t.Fatal("expected the frontmatter target's provider instead of the injected default provider")
	}
}

func TestNewAgentInstance_SuppressesToolDiscoveryPromptWhenNoMCPServersSelected(t *testing.T) {
	workspace := setupWorkspace(t, map[string]string{
		"AGENT.md": `---
mcpServers: []
---
# Agent
`,
	})
	defer cleanupWorkspace(t, workspace)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace: workspace,
				ModelName: "default-model",
			},
		},
		Tools: config.ToolsConfig{
			MCP: config.MCPConfig{
				ToolConfig: config.ToolConfig{Enabled: true},
				Discovery: config.ToolDiscoveryConfig{
					Enabled:  true,
					UseBM25:  true,
					UseRegex: false,
				},
				Servers: map[string]config.MCPServerConfig{
					"github": {Enabled: true},
				},
			},
		},
	}

	agent := NewAgentInstance(&config.AgentConfig{
		ID:        "research",
		Workspace: workspace,
	}, &cfg.Agents.Defaults, cfg, &mockProvider{}, nil)

	if agent.AllowsMCPServer("github") {
		t.Fatal("expected empty mcpServers allowlist to deny all servers")
	}
	messages := agent.ContextBuilder.BuildMessagesFromPrompt(PromptBuildRequest{CurrentMessage: "hello"})
	if prompt := messages[0].Content; strings.Contains(prompt, tools.BM25SearchToolName) {
		t.Fatalf("expected no tool discovery prompt when no MCP servers are selected, got %q", prompt)
	}
}

func TestNewAgentInstance_IncludesToolDiscoveryPromptWhenDiscoverableMCPServerSelected(t *testing.T) {
	workspace := setupWorkspace(t, map[string]string{
		"AGENT.md": `---
mcpServers: [github]
---
# Agent
`,
	})
	defer cleanupWorkspace(t, workspace)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace: workspace,
				ModelName: "default-model",
			},
		},
		Tools: config.ToolsConfig{
			MCP: config.MCPConfig{
				ToolConfig: config.ToolConfig{Enabled: true},
				Discovery: config.ToolDiscoveryConfig{
					Enabled:  true,
					UseBM25:  true,
					UseRegex: false,
				},
				Servers: map[string]config.MCPServerConfig{
					"github": {Enabled: true},
				},
			},
		},
	}

	agent := NewAgentInstance(&config.AgentConfig{
		ID:        "research",
		Workspace: workspace,
	}, &cfg.Agents.Defaults, cfg, &mockProvider{}, nil)

	messages := agent.ContextBuilder.BuildMessagesFromPrompt(PromptBuildRequest{CurrentMessage: "hello"})
	if prompt := messages[0].Content; !strings.Contains(prompt, tools.BM25SearchToolName) {
		t.Fatalf("expected tool discovery prompt when a discoverable MCP server is selected, got %q", prompt)
	}
}

func TestNewAgentInstance_InvalidFrontmatterFailsClosedForToolsAndMCPServers(t *testing.T) {
	workspace := setupWorkspace(t, map[string]string{
		"AGENT.md": `---
tools: [read_file
mcpServers: [github]
---
# Agent
`,
	})
	defer cleanupWorkspace(t, workspace)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace: workspace,
				ModelName: "default-model",
			},
		},
		Tools: config.ToolsConfig{
			ReadFile: config.ReadFileToolConfig{Enabled: true},
		},
	}

	agent := NewAgentInstance(&config.AgentConfig{
		ID:        "research",
		Workspace: workspace,
	}, &cfg.Agents.Defaults, cfg, &mockProvider{}, nil)

	if _, ok := agent.Tools.Get("read_file"); ok {
		t.Fatal("expected malformed frontmatter to fail closed and block read_file")
	}
	if agent.AllowsMCPServer("github") {
		t.Fatal("expected malformed frontmatter to fail closed for MCP servers")
	}
}

func TestNewAgentInstance_ExplicitEmptyToolsFieldBlocksAllTools(t *testing.T) {
	tests := []struct {
		name         string
		toolsSnippet string
	}{
		{
			name:         "empty list",
			toolsSnippet: "tools: []",
		},
		{
			name:         "blank field",
			toolsSnippet: "tools:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspace := setupWorkspace(t, map[string]string{
				"AGENT.md": `---
` + tt.toolsSnippet + `
---
# Agent
`,
			})
			defer cleanupWorkspace(t, workspace)

			cfg := &config.Config{
				Agents: config.AgentsConfig{
					Defaults: config.AgentDefaults{
						Workspace: workspace,
						ModelName: "default-model",
					},
				},
				Tools: config.ToolsConfig{
					ReadFile: config.ReadFileToolConfig{Enabled: true},
					ListDir:  config.ToolConfig{Enabled: true},
				},
			}

			agent := NewAgentInstance(&config.AgentConfig{
				ID:        "research",
				Workspace: workspace,
			}, &cfg.Agents.Defaults, cfg, &mockProvider{}, nil)

			if got := agent.Tools.List(); len(got) != 0 {
				t.Fatalf("agent tools = %v, want no registered tools", got)
			}
			if _, ok := agent.Tools.Get("read_file"); ok {
				t.Fatal("expected read_file to be blocked by explicit empty tools field")
			}
			if _, ok := agent.Tools.Get("list_dir"); ok {
				t.Fatal("expected list_dir to be blocked by explicit empty tools field")
			}
		})
	}
}
