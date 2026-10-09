package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/xibodev/compa/v4/pkg/config"
	"github.com/xibodev/compa/v4/pkg/isolation"
	"github.com/xibodev/compa/v4/pkg/logger"
	"github.com/xibodev/compa/v4/pkg/media"
	"github.com/xibodev/compa/v4/pkg/memory"
	"github.com/xibodev/compa/v4/pkg/providers"
	"github.com/xibodev/compa/v4/pkg/routing"
	"github.com/xibodev/compa/v4/pkg/session"
	"github.com/xibodev/compa/v4/pkg/skills"
	"github.com/xibodev/compa/v4/pkg/tools"
)

// AgentInstance represents a fully configured agent with its own workspace,
// session manager, context builder, and tool registry.
type AgentInstance struct {
	modelMu *sync.RWMutex
	ID      string
	Name    string
	// Model is the agent's model selection — an exact target
	// "instance-id/model-id" or a model route name — taken from its AGENT.md
	// frontmatter, its agents.list entry or agents.defaults.model_name, in
	// that order. Empty when none is set.
	Model     string
	Workspace string
	// ModuleSummaries are one-line capability summaries for each enabled
	// module, composed into the system prompt so the agent knows what its
	// installed modules can do without loading their full documentation.
	ModuleSummaries []string
	// ModuleKnowledge composes a selected module's overlay and skills for one
	// turn. It is set at init, where module discovery already happens, so a
	// turn never pays to re-describe every installed module.
	//
	// nil when no module is installed, and it returns empty for a turn with no
	// selection -- an installed module costs one line of capability summary
	// until someone points the agent at it.
	ModuleKnowledge func(moduleID string) (overlays string, skills string, warnings []string)
	MaxIterations   int
	MaxTokens       int
	Temperature     float64
	// ThinkingLevel is the extended-thinking level of the agent's first
	// target, from its instance's runtime settings.
	ThinkingLevel             ThinkingLevel
	ThinkingLevelConfigured   bool
	ContextWindow             int
	SummarizeMessageThreshold int
	SummarizeTokenPercent     int
	// Provider is the provider of Candidates[0]; nil when the agent has no
	// model.
	Provider           providers.LLMProvider
	Sessions           session.SessionStore
	ContextBuilder     *ContextBuilder
	Tools              *tools.ToolRegistry
	Definition         AgentContextDefinition
	Subagents          *config.SubagentsConfig
	SkillsFilter       []string
	MCPServerAllowlist map[string]struct{}
	// Candidates are the targets Model resolved to, in failover order: one
	// for an exact target, the route's targets for a route. Empty when the
	// agent has no model.
	Candidates []providers.FallbackCandidate
	// ImageCandidates are the targets of agents.defaults.image_model, which
	// turns carrying images are rerouted to.
	ImageCandidates []providers.FallbackCandidate

	// Router is non-nil when model routing is configured and the light model
	// resolved. It scores each incoming message and decides whether to route
	// to LightCandidates or stay with Candidates.
	Router *routing.Router
	// LightCandidates are the targets of the routing light model selection,
	// resolved at agent creation.
	LightCandidates []providers.FallbackCandidate
	// LightProvider is the provider of LightCandidates[0]. It is only used
	// when routing selects the light tier for a turn.
	LightProvider providers.LLMProvider
	// CandidateProviders and CandidateConfigs hold the provider and the
	// request spec of every candidate above, keyed by
	// FallbackCandidate.StableKey(). A request spec carries the target
	// instance's runtime settings: thinking level, streaming, RPM, timeouts.
	CandidateProviders map[string]providers.LLMProvider
	CandidateConfigs   map[string]*providers.CallSpec

	// modelErr is why Model has no candidates; nil when it resolved.
	modelErr error
	// status mirrors the agent's primary model for readers outside a turn.
	status *agentModelStatus
	// models counts the turns running on the agent's current model
	// providers; see turnSnapshot.
	models *modelGeneration
}

// modelGeneration counts the turns running on one set of an agent's model
// providers. A turn runs on a snapshot of the agent's model and holds no
// lock, so the providers a /switch or Close replaces are closed only once
// the last turn started on them ends.
type modelGeneration struct {
	mu      sync.Mutex
	turns   int
	retired bool
	stale   []providers.LLMProvider
}

// acquire counts one more turn on the generation and reports whether its
// providers are still open: they are not once it was retired with no turn
// left on it.
func (g *modelGeneration) acquire() bool {
	if g == nil {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.retired && g.turns == 0 {
		return false
	}
	g.turns++
	return true
}

func (g *modelGeneration) release() {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.turns--
	var stale []providers.LLMProvider
	if g.turns == 0 && g.retired {
		stale, g.stale = g.stale, nil
	}
	g.mu.Unlock()
	closeUniqueStatefulProviders(stale...)
}

// retire marks the generation replaced: stale closes now when no turn runs
// on it, and otherwise when its last turn ends.
func (g *modelGeneration) retire(stale []providers.LLMProvider) {
	if g == nil {
		closeUniqueStatefulProviders(stale...)
		return
	}
	g.mu.Lock()
	g.retired = true
	g.stale = append(g.stale, stale...)
	if g.turns > 0 {
		g.mu.Unlock()
		return
	}
	stale, g.stale = g.stale, nil
	g.mu.Unlock()
	closeUniqueStatefulProviders(stale...)
}

// turnSnapshot returns a copy of the agent with its model as it is now, for
// one turn to run on, and the release the turn calls when it ends. The turn
// holds no lock while it runs: a /switch model never waits for it (nor for
// the sub-agents it runs) and takes effect on the next turn.
//
// current is false only for a snapshot of a snapshot whose turns all ended
// after a /switch closed its providers; its copy must not be run on.
func (a *AgentInstance) turnSnapshot() (snapshot *AgentInstance, release func(), current bool) {
	modelMu := a.modelStateMutex()
	modelMu.RLock()
	copied := *a
	generation := a.models
	current = generation.acquire()
	modelMu.RUnlock()
	if !current {
		return &copied, func() {}, false
	}
	var once sync.Once
	return &copied, func() { once.Do(generation.release) }, true
}

// agentModelStatus is the model state read outside a turn — by background
// work (summaries, seahorse, evolution) and /show model — without the model
// mutex. A shallow copy of an agent (a turn's snapshot) shares it.
type agentModelStatus struct {
	mu       sync.Mutex
	provider providers.LLMProvider
	model    string
	err      error
	served   string
}

func (s *agentModelStatus) set(provider providers.LLMProvider, model string, err error) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.provider = provider
	s.model = model
	s.err = err
	s.served = ""
}

func (s *agentModelStatus) primary() (providers.LLMProvider, string, error) {
	if s == nil {
		return nil, "", &noModelError{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.provider == nil {
		if s.err != nil {
			return nil, "", s.err
		}
		return nil, "", &noModelError{}
	}
	return s.provider, s.model, nil
}

func (s *agentModelStatus) noteServed(target string) {
	if s == nil || strings.TrimSpace(target) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.served = target
}

func (s *agentModelStatus) servedTarget() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.served
}

var fallbackAgentModelMu sync.RWMutex

// NewAgentInstance creates an agent instance from config. The agent runs on
// its model selection (see AgentInstance.Model), resolved through resolve;
// when provider is not nil it serves the default selection instead, with
// no resolution. A nil resolve uses the default model resolver.
func NewAgentInstance(
	agentCfg *config.AgentConfig,
	defaults *config.AgentDefaults,
	cfg *config.Config,
	provider providers.LLMProvider,
	resolve ModelResolver,
) *AgentInstance {
	if cfg != nil {
		// Keep the subprocess isolation runtime aligned with the latest loaded config
		// before any tools or providers start spawning child processes.
		isolation.Configure(cfg)
	}
	if resolve == nil {
		resolve = defaultModelResolver()
	}

	workspace := resolveAgentWorkspace(agentCfg, defaults)
	os.MkdirAll(workspace, 0o755)

	definition := loadAgentDefinition(workspace)

	selection := resolveAgentModel(agentCfg, defaults, definition)

	restrict := defaults.RestrictToWorkspace
	readRestrict := restrict && !defaults.AllowReadOutsideWorkspace

	// Compile path whitelist patterns from config.
	allowReadPaths := buildAllowReadPatterns(cfg)
	allowWritePaths := compilePatterns(cfg.Tools.AllowWritePaths)
	agentToolAllowlist := resolveAgentToolAllowlist(definition)
	agentMCPServerAllowlist := resolveAgentMCPServerAllowlist(definition)

	toolsRegistry := tools.NewToolRegistry()
	toolsRegistry.SetAllowlist(agentToolAllowlist)

	if cfg.Tools.IsToolEnabled("read_file") {
		maxReadFileSize := cfg.Tools.ReadFile.MaxReadFileSize
		switch cfg.Tools.ReadFile.EffectiveMode() {
		case config.ReadFileModeLines:
			toolsRegistry.Register(tools.NewReadFileLinesTool(workspace, readRestrict, maxReadFileSize, allowReadPaths))
		default:
			toolsRegistry.Register(tools.NewReadFileBytesTool(workspace, readRestrict, maxReadFileSize, allowReadPaths))
		}
	}
	if cfg.Tools.IsToolEnabled("edit_file") {
		toolsRegistry.Register(tools.NewEditFileTool(workspace, restrict, allowWritePaths))
	}
	if cfg.Tools.IsToolEnabled("append_file") {
		toolsRegistry.Register(tools.NewAppendFileTool(workspace, restrict, allowWritePaths))
	}
	// Build write_file's copy from the registered editors so it steers the agent
	// to edit_file/append_file only when those tools are actually available.
	if cfg.Tools.IsToolEnabled("write_file") {
		writeTool := tools.NewWriteFileTool(workspace, restrict, allowWritePaths)
		var altTools []string
		if toolsRegistry.HasRegistered("append_file") {
			altTools = append(altTools, "append_file")
		}
		if toolsRegistry.HasRegistered("edit_file") {
			altTools = append(altTools, "edit_file")
		}
		writeTool.SetAlternativeTools(altTools)
		toolsRegistry.Register(writeTool)
	}
	if cfg.Tools.IsToolEnabled("list_dir") {
		toolsRegistry.Register(tools.NewListDirTool(workspace, readRestrict, allowReadPaths))
	}
	if cfg.Tools.IsToolEnabled("exec") {
		execTool, err := tools.NewExecToolWithConfig(workspace, restrict, cfg, allowReadPaths)
		if err != nil {
			logger.ErrorCF("agent", "Failed to initialize exec tool; continuing without exec",
				map[string]any{"error": err.Error()})
		} else {
			toolsRegistry.Register(execTool)
		}
	}

	sessionsDir := filepath.Join(workspace, "sessions")
	sessions := initSessionStore(sessionsDir)

	mcpDiscoveryActive := agentHasDiscoverableMCPServers(cfg, agentMCPServerAllowlist)
	contextBuilder := NewContextBuilder(workspace).
		WithToolDiscovery(
			mcpDiscoveryActive && cfg.Tools.MCP.Discovery.UseBM25,
			mcpDiscoveryActive && cfg.Tools.MCP.Discovery.UseRegex,
		).
		WithSplitOnMarker(cfg.Agents.Defaults.SplitOnMarker).
		withSpokenReplies(spokenRepliesConfigured(cfg))

	agentID := routing.DefaultAgentID
	agentName := ""
	var subagents *config.SubagentsConfig
	var skillsFilter []string

	if agentCfg != nil {
		agentID = routing.NormalizeAgentID(agentCfg.ID)
		agentName = agentCfg.Name
		if definition.Agent != nil && strings.TrimSpace(definition.Agent.Frontmatter.Name) != "" {
			agentName = strings.TrimSpace(definition.Agent.Frontmatter.Name)
		}
		subagents = agentCfg.Subagents
		skillsFilter = resolveAgentSkillsFilter(agentCfg, definition)
	}
	warnOnUnknownAgentMCPServerDeclarations(agentID, workspace, cfg, definition)
	warnOnInvalidAgentFrontmatterFlags(agentID, workspace, definition)

	maxIter := defaults.MaxToolIterations
	if maxIter == 0 {
		maxIter = 20
	}

	maxTokens := defaults.MaxTokens
	if maxTokens == 0 {
		maxTokens = 8192
	}

	contextWindow := defaults.ContextWindow
	if contextWindow == 0 {
		// Default heuristic: 4x the output token limit.
		// Most models have context windows well above their output limits
		// (e.g., GPT-4o 128k ctx / 16k out, Claude 200k ctx / 8k out).
		// 4x is a conservative lower bound that avoids premature
		// summarization while remaining safe — the reactive
		// forceCompression handles any overshoot.
		contextWindow = maxTokens * 4
	}
	if maxTokens >= contextWindow {
		// The reply must fit the window with the prompt; otherwise every
		// request is over budget and compaction empties the history.
		clamped := max(contextWindow/2, 1)
		logger.WarnCF("agent", "max_tokens is not below context_window; capping it at half the window",
			map[string]any{
				"agent_id":       agentID,
				"max_tokens":     maxTokens,
				"context_window": contextWindow,
				"capped_to":      clamped,
			})
		maxTokens = clamped
	}

	temperature := 0.7
	if defaults.Temperature != nil {
		temperature = *defaults.Temperature
	}

	summarizeMessageThreshold := defaults.SummarizeMessageThreshold
	if summarizeMessageThreshold == 0 {
		summarizeMessageThreshold = 20
	}

	summarizeTokenPercent := defaults.SummarizeTokenPercent
	if summarizeTokenPercent == 0 {
		summarizeTokenPercent = 75
	}

	agent := &AgentInstance{
		modelMu:                   &sync.RWMutex{},
		ID:                        agentID,
		Name:                      agentName,
		Workspace:                 workspace,
		MaxIterations:             maxIter,
		MaxTokens:                 maxTokens,
		Temperature:               temperature,
		ContextWindow:             contextWindow,
		SummarizeMessageThreshold: summarizeMessageThreshold,
		SummarizeTokenPercent:     summarizeTokenPercent,
		Sessions:                  sessions,
		ContextBuilder:            contextBuilder,
		Tools:                     toolsRegistry,
		Definition:                definition,
		Subagents:                 subagents,
		SkillsFilter:              skillsFilter,
		MCPServerAllowlist:        agentMCPServerAllowlist,
		CandidateProviders:        make(map[string]providers.LLMProvider),
		CandidateConfigs:          make(map[string]*providers.CallSpec),
		status:                    &agentModelStatus{},
		models:                    &modelGeneration{},
	}

	models, err := modelsForSelection(cfg, provider, resolve, selection)
	if err != nil {
		agent.setModelError(selection, err)
	} else {
		agent.setModels(models)
	}

	if imageSelection := strings.TrimSpace(defaults.ImageModel); imageSelection != "" {
		imageModels, err := modelsForSelection(cfg, provider, resolve, imageSelection)
		if err != nil {
			logger.WarnCF("agent", "Image model selection did not resolve; turns carrying images stay on the agent's model",
				map[string]any{"agent_id": agentID, "image_model": imageSelection, "error": err.Error()})
		} else {
			agent.ImageCandidates = agent.addCandidates(imageModels)
		}
	}

	// Model routing: resolve the light model once here rather than per message.
	if rc := defaults.Routing; rc != nil && rc.Enabled && strings.TrimSpace(rc.LightModel) != "" {
		lightModels, err := modelsForSelection(cfg, provider, resolve, rc.LightModel)
		if err != nil {
			logger.WarnCF("agent", "Routing light model selection did not resolve; routing disabled",
				map[string]any{"agent_id": agentID, "light_model": rc.LightModel, "error": err.Error()})
		} else {
			agent.LightCandidates = agent.addCandidates(lightModels)
			agent.LightProvider = agent.CandidateProviders[agent.LightCandidates[0].StableKey()]
			agent.Router = routing.New(routing.RouterConfig{
				LightModel: lightModels.selection,
				Threshold:  rc.Threshold,
			})
		}
	}

	return agent
}

// setModels makes models the agent's model: its selection, candidates,
// primary provider and thinking level. The image and light candidates are
// kept, and so are their providers.
func (a *AgentInstance) setModels(models agentModels) {
	previous := a.CandidateProviders
	nextProviders := make(map[string]providers.LLMProvider, len(models.providers)+len(previous))
	nextConfigs := make(map[string]*providers.CallSpec, len(models.configs)+len(a.CandidateConfigs))
	for _, candidates := range [][]providers.FallbackCandidate{a.ImageCandidates, a.LightCandidates} {
		for _, candidate := range candidates {
			key := candidate.StableKey()
			nextProviders[key] = previous[key]
			nextConfigs[key] = a.CandidateConfigs[key]
		}
	}
	a.CandidateProviders = nextProviders
	a.CandidateConfigs = nextConfigs

	a.Model = models.selection
	a.Candidates = a.addCandidates(models)
	a.modelErr = nil
	primary := a.Candidates[0]
	a.Provider = a.CandidateProviders[primary.StableKey()]
	level := ""
	if modelCfg := a.CandidateConfigs[primary.StableKey()]; modelCfg != nil {
		level = modelCfg.ThinkingLevel
	}
	a.ThinkingLevel = parseThinkingLevel(level)
	a.ThinkingLevelConfigured = isConfiguredThinkingLevel(level)
	a.status.set(a.Provider, primary.Model, nil)

	// Turns still running on the previous model keep its providers.
	retiring := a.models
	a.models = &modelGeneration{}
	retiring.retire(unreferencedProviders(previous, a.CandidateProviders))
}

// setModelError records that selection gave the agent no model: every turn
// without a per-message selection then fails with a no-model error.
func (a *AgentInstance) setModelError(selection string, err error) {
	selection = strings.TrimSpace(selection)
	a.Model = selection
	a.Candidates = nil
	a.Provider = nil
	a.ThinkingLevel = ThinkingOff
	a.ThinkingLevelConfigured = false
	a.modelErr = &noModelError{}
	if !errors.Is(err, errNoSelection) {
		a.modelErr = &noModelError{selection: selection, cause: err}
	}
	a.status.set(nil, "", a.modelErr)

	fields := map[string]any{"agent_id": a.ID}
	if selection == "" {
		logger.WarnCF("agent", "No model selected; turns without a per-message model selection will fail", fields)
		return
	}
	fields["selection"] = selection
	fields["error"] = err.Error()
	logger.WarnCF("agent", "Model selection did not resolve; turns without a per-message model selection will fail", fields)
}

// addCandidates adds the providers and request specs of models to the
// agent's candidate maps and returns its candidates. A target the agent
// already runs on keeps its provider; the duplicate is closed.
func (a *AgentInstance) addCandidates(models agentModels) []providers.FallbackCandidate {
	for _, candidate := range models.candidates {
		key := candidate.StableKey()
		provider := models.providers[key]
		if existing := a.CandidateProviders[key]; existing != nil {
			if existing != provider {
				closeUniqueStatefulProviders(provider)
			}
			continue
		}
		a.CandidateProviders[key] = provider
		a.CandidateConfigs[key] = models.configs[key]
	}
	return models.candidates
}

// hasModel reports whether the agent has candidates to run a turn on.
func (a *AgentInstance) hasModel() bool {
	return a != nil && len(a.Candidates) > 0 && a.Provider != nil
}

// noModelError returns why the agent has no model to run a turn on.
func (a *AgentInstance) noModelError() error {
	if a != nil && a.modelErr != nil {
		return a.modelErr
	}
	return &noModelError{}
}

// providerForCandidate returns the provider of one of the agent's candidates.
func (a *AgentInstance) providerForCandidate(candidate providers.FallbackCandidate) (providers.LLMProvider, error) {
	if a != nil {
		if provider := a.CandidateProviders[candidate.StableKey()]; provider != nil {
			return provider, nil
		}
	}
	return nil, fmt.Errorf("model %q has no provider on this agent", candidate.DisplayName)
}

// configForCandidate returns the request spec of one of the agent's
// candidates, or nil.
func (a *AgentInstance) configForCandidate(candidate providers.FallbackCandidate) *providers.CallSpec {
	if a == nil {
		return nil
	}
	return a.CandidateConfigs[candidate.StableKey()]
}

// primaryModel returns the provider and model ID background work runs on:
// the agent's first target, or why it has none. It takes no model mutex, so
// it is safe inside and outside a turn.
func (a *AgentInstance) primaryModel() (providers.LLMProvider, string, error) {
	if a == nil {
		return nil, "", &noModelError{}
	}
	return a.status.primary()
}

// noteServed records the target that answered a turn on the agent's model.
func (a *AgentInstance) noteServed(target string) {
	if a != nil {
		a.status.noteServed(target)
	}
}

// resolveAgentWorkspace determines the workspace directory for an agent.
func resolveAgentWorkspace(agentCfg *config.AgentConfig, defaults *config.AgentDefaults) string {
	if agentCfg != nil && strings.TrimSpace(agentCfg.Workspace) != "" {
		return expandHome(strings.TrimSpace(agentCfg.Workspace))
	}
	// Use the configured default workspace (respects COMPA_HOME)
	if agentCfg == nil || agentCfg.Default || agentCfg.ID == "" ||
		routing.NormalizeAgentID(agentCfg.ID) == "main" {
		return expandHome(defaults.Workspace)
	}
	// For named agents without explicit workspace, use default workspace with agent ID suffix
	id := routing.NormalizeAgentID(agentCfg.ID)
	return filepath.Join(expandHome(defaults.Workspace), "..", "workspace-"+id)
}

// resolveAgentModel returns an agent's model selection: its AGENT.md
// frontmatter model, its agents.list model, or the default model, in that
// order.
func resolveAgentModel(
	agentCfg *config.AgentConfig,
	defaults *config.AgentDefaults,
	definition AgentContextDefinition,
) string {
	if definition.Agent != nil && strings.TrimSpace(definition.Agent.Frontmatter.Model) != "" {
		return strings.TrimSpace(definition.Agent.Frontmatter.Model)
	}
	if agentCfg != nil && strings.TrimSpace(agentCfg.Model) != "" {
		return strings.TrimSpace(agentCfg.Model)
	}
	return strings.TrimSpace(defaults.GetModelName())
}

func resolveAgentSkillsFilter(
	agentCfg *config.AgentConfig,
	definition AgentContextDefinition,
) []string {
	if definition.Agent != nil && definition.Agent.Frontmatter.Skills != nil {
		return append([]string(nil), definition.Agent.Frontmatter.Skills...)
	}
	if agentCfg == nil || agentCfg.Skills == nil {
		return nil
	}
	return append([]string(nil), agentCfg.Skills...)
}

// spokenRepliesConfigured reports whether the user can turn on spoken
// replies: voice is enabled with a text-to-speech target, as the web
// client's voice controls require.
func spokenRepliesConfigured(cfg *config.Config) bool {
	return cfg != nil && cfg.Voice.Enabled && strings.TrimSpace(cfg.Voice.TTSTarget) != ""
}

func (a *AgentInstance) AllowsMCPServer(serverName string) bool {
	if a == nil || a.MCPServerAllowlist == nil {
		return true
	}
	_, ok := a.MCPServerAllowlist[strings.ToLower(strings.TrimSpace(serverName))]
	return ok
}

func compilePatterns(patterns []string) []*regexp.Regexp {
	compiled := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			logger.WarnCF("agent", "invalid path pattern in compilePatterns", map[string]any{
				"pattern": p,
				"error":   err.Error(),
			})
			continue
		}
		compiled = append(compiled, re)
	}
	return compiled
}

// buildAllowReadPatterns compiles tools.allow_read_paths and adds the folders
// outside the workspace that the agent is pointed to: the media folder, and
// the global and built-in skill folders, whose SKILL.md files the skill
// catalog tells the model to read.
func buildAllowReadPatterns(cfg *config.Config) []*regexp.Regexp {
	var configured []string
	if cfg != nil {
		configured = cfg.Tools.AllowReadPaths
	}

	compiled := compilePatterns(configured)
	for _, dir := range []string{media.TempDir(), globalSkillsDir(), skills.BuiltinDir()} {
		compiled = appendFolderPattern(compiled, dir)
	}
	return compiled
}

// appendFolderPattern adds the pattern of dir and everything in it, unless
// there is no dir or the pattern is there already.
func appendFolderPattern(patterns []*regexp.Regexp, dir string) []*regexp.Regexp {
	if dir == "" {
		return patterns
	}
	sep := regexp.QuoteMeta(string(os.PathSeparator))
	pattern := "^" + regexp.QuoteMeta(filepath.Clean(dir)) + "(?:" + sep + "|$)"
	for _, existing := range patterns {
		if existing.String() == pattern {
			return patterns
		}
	}
	return append(patterns, regexp.MustCompile(pattern))
}

// Close releases resources held by the agent's providers and session store.
// Providers a running turn still uses close when that turn ends.
func (a *AgentInstance) Close() error {
	modelMu := a.modelStateMutex()
	modelMu.Lock()
	held := a.providerList()
	retiring := a.models
	a.models = &modelGeneration{}
	modelMu.Unlock()
	retiring.retire(held)
	if a.Sessions != nil {
		return a.Sessions.Close()
	}
	return nil
}

// providerList returns every provider the agent holds.
func (a *AgentInstance) providerList() []providers.LLMProvider {
	providerList := make([]providers.LLMProvider, 0, 2+len(a.CandidateProviders))
	providerList = append(providerList, a.Provider, a.LightProvider)
	for _, provider := range a.CandidateProviders {
		providerList = append(providerList, provider)
	}
	return providerList
}

func (a *AgentInstance) modelStateMutex() *sync.RWMutex {
	if a.modelMu == nil {
		return &fallbackAgentModelMu
	}
	return a.modelMu
}

func closeUniqueStatefulProviders(providerList ...providers.LLMProvider) {
	seen := make(map[string]struct{})
	for _, provider := range providerList {
		stateful, ok := provider.(providers.StatefulProvider)
		if !ok || stateful == nil {
			continue
		}
		key := fmt.Sprintf("%T:%p", stateful, stateful)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		stateful.Close()
	}
}

// closeUnreferencedStatefulProviders closes the stateful providers of
// previous that neither current nor retained still hold.
func closeUnreferencedStatefulProviders(
	previous map[string]providers.LLMProvider,
	current map[string]providers.LLMProvider,
	retained ...providers.LLMProvider,
) {
	closeUniqueStatefulProviders(unreferencedProviders(previous, current, retained...)...)
}

// unreferencedProviders returns the providers of previous that neither
// current nor retained hold.
func unreferencedProviders(
	previous map[string]providers.LLMProvider,
	current map[string]providers.LLMProvider,
	retained ...providers.LLMProvider,
) []providers.LLMProvider {
	retainedKeys := make(map[string]struct{}, len(current)+len(retained))
	for _, provider := range current {
		retainedKeys[fmt.Sprintf("%T:%p", provider, provider)] = struct{}{}
	}
	for _, provider := range retained {
		retainedKeys[fmt.Sprintf("%T:%p", provider, provider)] = struct{}{}
	}

	removed := make([]providers.LLMProvider, 0, len(previous))
	for _, provider := range previous {
		if _, exists := retainedKeys[fmt.Sprintf("%T:%p", provider, provider)]; !exists {
			removed = append(removed, provider)
		}
	}
	return removed
}

// initSessionStore creates the JSONL session store rooted at dir.
func initSessionStore(dir string) session.SessionStore {
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		// The store stays usable; its writes fail (and are logged by the
		// backend) until the directory can be created.
		logger.ErrorCF("agent", "Session directory unavailable; conversation history will not persist",
			map[string]any{"dir": dir, "error": err.Error()})
	}
	return session.NewJSONLBackend(store)
}

// expandHome expands a leading "~" the way the config does, "~\x" included:
// a path it does not name never becomes the bare home directory.
func expandHome(path string) string {
	return config.ExpandHome(path)
}
