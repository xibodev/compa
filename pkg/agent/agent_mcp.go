// Compa - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 Compa contributors

package agent

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/xibodev/compa/v3/pkg/config"
	"github.com/xibodev/compa/v3/pkg/logger"
	"github.com/xibodev/compa/v3/pkg/mcp"
	"github.com/xibodev/compa/v3/pkg/tools"
)

// mcpRuntime is the MCP state of the loop: the manager of the connected
// servers and how connecting them went. Each config has one initialization;
// a reload starts a new generation, and an initialization still running for
// an older one discards its result.
type mcpRuntime struct {
	mu sync.Mutex
	// gen counts reloads; an initialization records its result only while
	// gen is the one it started under.
	gen uint64
	// done is closed when the current generation's initialization ends;
	// nil until one starts.
	done    chan struct{}
	manager *mcp.Manager
	initErr error
}

// reset starts a new generation, returning the previous manager for the
// caller to close.
func (r *mcpRuntime) reset() *mcp.Manager {
	r.mu.Lock()
	manager := r.manager
	r.manager = nil
	r.initErr = nil
	r.gen++
	r.done = nil
	r.mu.Unlock()
	return manager
}

// begin claims the current generation's initialization. run is true for the
// one caller that runs it; everyone gets the generation and the channel
// closed when it ends.
func (r *mcpRuntime) begin() (gen uint64, done chan struct{}, run bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done != nil {
		return r.gen, r.done, false
	}
	r.done = make(chan struct{})
	return r.gen, r.done, true
}

// finish records generation gen's result and reports whether it was kept: a
// result of a generation a reload replaced is not, and its manager is the
// caller's to close.
func (r *mcpRuntime) finish(gen uint64, done chan struct{}, manager *mcp.Manager, err error) bool {
	r.mu.Lock()
	current := r.gen == gen
	if current {
		r.manager = manager
		r.initErr = err
	}
	r.mu.Unlock()
	close(done)
	return current
}

func (r *mcpRuntime) setManager(manager *mcp.Manager) {
	r.mu.Lock()
	r.manager = manager
	r.initErr = nil
	r.mu.Unlock()
}

func (r *mcpRuntime) setInitErr(err error) {
	r.mu.Lock()
	r.initErr = err
	r.mu.Unlock()
}

func (r *mcpRuntime) getInitErr() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.initErr
}

func (r *mcpRuntime) hasManager() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.manager != nil
}

func (r *mcpRuntime) getManager() *mcp.Manager {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.manager
}

// mcpConfigToLoad returns the MCP servers cfg has the loop connect, after
// the agents' mcpServers allowlists, and whether there is any.
func mcpConfigToLoad(cfg *config.Config, registry *AgentRegistry) (config.MCPConfig, bool) {
	if cfg == nil || registry == nil || !cfg.Tools.IsToolEnabled("mcp") {
		return config.MCPConfig{}, false
	}

	if len(cfg.Tools.MCP.Servers) == 0 {
		logger.WarnCF("agent", "MCP is enabled but no servers are configured, skipping MCP initialization", nil)
		return config.MCPConfig{}, false
	}

	mcpCfg := filterMCPConfigServers(cfg.Tools.MCP, registry.allowedMCPServers())
	if len(mcpCfg.Servers) == 0 {
		logger.InfoCF(
			"agent",
			"No MCP servers selected after applying per-agent mcpServers allowlists",
			nil,
		)
		return config.MCPConfig{}, false
	}

	for _, serverCfg := range mcpCfg.Servers {
		if serverCfg.Enabled {
			return mcpCfg, true
		}
	}
	logger.WarnCF("agent", "MCP is enabled but no valid servers are configured, skipping MCP initialization", nil)
	return config.MCPConfig{}, false
}

// ensureMCPInitialized connects the configured MCP servers and registers
// their tools, once per config, and returns how that went. A caller that
// arrives while the connection runs elsewhere waits for it as long as ctx
// allows. ctx never bounds the servers: they connect within the MCP
// manager's own timeouts and run for the loop's lifetime.
func (al *AgentLoop) ensureMCPInitialized(ctx context.Context) error {
	cfg, registry := al.GetConfig(), al.GetRegistry()
	mcpCfg, ok := mcpConfigToLoad(cfg, registry)
	if !ok {
		return nil
	}
	gen, done, run := al.mcp.begin()
	if run {
		return al.initializeMCP(gen, done, cfg, registry, mcpCfg)
	}
	select {
	case <-done:
		return al.mcp.getInitErr()
	default:
	}
	select {
	case <-done:
		return al.mcp.getInitErr()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// startMCPInitialization connects the MCP servers in the background; their
// tools register once connected. Run and reload use it, so a slow or broken
// server never holds up message processing.
func (al *AgentLoop) startMCPInitialization() {
	cfg, registry := al.GetConfig(), al.GetRegistry()
	mcpCfg, ok := mcpConfigToLoad(cfg, registry)
	if !ok {
		return
	}
	gen, done, run := al.mcp.begin()
	if !run {
		return
	}
	go func() {
		_ = al.initializeMCP(gen, done, cfg, registry, mcpCfg)
	}()
}

// initializeMCP runs generation gen's initialization, records its result and
// reports a failure. A result a reload made stale is dropped, its servers
// closed.
func (al *AgentLoop) initializeMCP(
	gen uint64,
	done chan struct{},
	cfg *config.Config,
	registry *AgentRegistry,
	mcpCfg config.MCPConfig,
) error {
	manager, err := al.connectMCP(cfg, registry, mcpCfg)
	if !al.mcp.finish(gen, done, manager, err) {
		if manager != nil {
			if closeErr := manager.Close(); closeErr != nil {
				logger.WarnCF("agent", "Failed to close MCP manager of a replaced config",
					map[string]any{"error": closeErr.Error()})
			}
		}
		return nil
	}
	al.reportInitError("MCP servers", err)
	return err
}

// connectMCP connects mcpCfg's servers and registers their tools on
// registry's agents. A server that announces its tool list changed, or is
// reconnected, has its tools registered again.
func (al *AgentLoop) connectMCP(
	cfg *config.Config,
	registry *AgentRegistry,
	mcpCfg config.MCPConfig,
) (*mcp.Manager, error) {
	defaultAgent := registry.GetDefaultAgent()
	workspacePath := cfg.WorkspacePath()
	if defaultAgent != nil && defaultAgent.Workspace != "" {
		workspacePath = defaultAgent.Workspace
	}

	index := &mcpToolIndex{}
	var mcpManager *mcp.Manager
	mcpManager = mcp.NewManager(
		mcp.WithRuntimeEvents(al.runtimeEvents),
		// The servers may work in the agents' workspaces.
		mcp.WithRoots(mcpWorkspaces(workspacePath, registry)...),
		mcp.WithCallTimeout(time.Duration(mcpCfg.CallTimeoutSeconds)*time.Second),
		mcp.WithToolsChangedHandler(func(serverName string, _ []*mcpsdk.Tool) {
			// Only the registry these servers serve: a reload replaced it
			// and its manager with them.
			if al.GetRegistry() != registry {
				return
			}
			al.registerMCPServerTools(mcpManager, cfg, registry, index, serverName)
		}),
	)

	discovery := cfg.Tools.MCP.Enabled && cfg.Tools.MCP.Discovery.Enabled
	useBM25 := cfg.Tools.MCP.Discovery.UseBM25
	useRegex := cfg.Tools.MCP.Discovery.UseRegex
	// Fail fast, before starting any server: discovery is enabled but no
	// search method is turned on.
	if discovery && !useBM25 && !useRegex {
		return nil, fmt.Errorf(
			"tool discovery is enabled but neither 'use_bm25' nor 'use_regex' is set to true in the configuration",
		)
	}

	if err := mcpManager.LoadFromMCPConfig(al.lifetimeContext(), mcpCfg, workspacePath); err != nil {
		logger.WarnCF("agent", "Failed to load MCP servers, MCP tools will not be available",
			map[string]any{
				"error": err.Error(),
			})
		if closeErr := mcpManager.Close(); closeErr != nil {
			logger.ErrorCF("agent", "Failed to close MCP manager",
				map[string]any{
					"error": closeErr.Error(),
				})
		}
		return nil, fmt.Errorf("failed to load MCP servers: %w", err)
	}

	// Register MCP tools for all agents, servers in name order: of two tools
	// whose names collide, the one registered first keeps the plain name.
	servers := mcpManager.GetServers()
	serverNames := make([]string, 0, len(servers))
	for serverName := range servers {
		serverNames = append(serverNames, serverName)
	}
	sort.Strings(serverNames)
	uniqueTools := 0
	totalRegistrations := 0
	agentCount := len(registry.ListAgentIDs())

	for _, serverName := range serverNames {
		uniqueTools += len(mcpManager.ServerTools(serverName))
		totalRegistrations += al.registerMCPServerTools(mcpManager, cfg, registry, index, serverName)
	}
	logger.InfoCF("agent", "MCP tools registered successfully",
		map[string]any{
			"server_count":        len(servers),
			"unique_tools":        uniqueTools,
			"total_registrations": totalRegistrations,
			"agent_count":         agentCount,
		})

	// Initializes Discovery Tools only if enabled by configuration
	if discovery {
		ttl := cfg.Tools.MCP.Discovery.TTL
		if ttl <= 0 {
			ttl = 5 // Default value
		}

		maxSearchResults := cfg.Tools.MCP.Discovery.MaxSearchResults
		if maxSearchResults <= 0 {
			maxSearchResults = 5 // Default value
		}

		logger.InfoCF("agent", "Initializing tool discovery", map[string]any{
			"bm25": useBM25, "regex": useRegex, "ttl": ttl, "max_results": maxSearchResults,
		})

		for _, agentID := range registry.ListAgentIDs() {
			agent, ok := registry.GetAgent(agentID)
			if !ok {
				continue
			}
			if !agentHasDiscoverableMCPServers(cfg, agent.MCPServerAllowlist) {
				continue
			}

			if useRegex {
				agent.Tools.Register(tools.NewRegexSearchTool(agent.Tools, ttl, maxSearchResults))
			}
			if useBM25 {
				agent.Tools.Register(tools.NewBM25SearchTool(agent.Tools, ttl, maxSearchResults))
			}
		}
	}

	return mcpManager, nil
}

// mcpToolIndex records the tools each MCP server registered on each agent,
// so a server's tools can be replaced when its tool list changes.
type mcpToolIndex struct {
	mu sync.Mutex
	// registered maps a server to the names of the tools it registered,
	// by agent ID.
	registered map[string]map[string][]string
}

// registerMCPServerTools registers the tools serverName lists now on the
// registry's agents that allow the server, in place of the ones it
// registered before, and returns how many registrations it made. Reading
// the list under the index lock keeps a late registration from restoring an
// older list.
func (al *AgentLoop) registerMCPServerTools(
	manager *mcp.Manager,
	cfg *config.Config,
	registry *AgentRegistry,
	index *mcpToolIndex,
	serverName string,
) int {
	index.mu.Lock()
	defer index.mu.Unlock()
	if index.registered == nil {
		index.registered = make(map[string]map[string][]string)
	}
	previous := index.registered[serverName]
	current := make(map[string][]string)
	serverTools := manager.ServerTools(serverName)

	// Per-server "deferred" takes precedence over the global Discovery.Enabled.
	registerAsHidden := serverIsDeferred(cfg.Tools.MCP.Discovery.Enabled, cfg.Tools.MCP.Servers[serverName])
	total := 0
	for _, agentID := range registry.ListAgentIDs() {
		agent, ok := registry.GetAgent(agentID)
		if !ok || agent.Tools == nil {
			continue
		}
		for _, name := range previous[agentID] {
			agent.Tools.Unregister(name)
		}
		if !agent.AllowsMCPServer(serverName) {
			logger.DebugCF("agent", "Skipped MCP tool registration by agent mcpServers allowlist",
				map[string]any{"agent_id": agentID, "server": serverName})
			continue
		}

		for _, tool := range serverTools {
			if tool == nil {
				continue
			}
			mcpTool := tools.NewMCPTool(manager, serverName, tool)
			mcpTool.SetTrusted(cfg.Tools.MCP.Servers[serverName].Trusted)
			mcpTool.SetWorkspace(agent.Workspace)
			mcpTool.SetMaxInlineTextRunes(cfg.Tools.MCP.GetMaxInlineTextChars())
			mcpTool.SetEventPublisher(al.runtimeEvents)

			// A name another tool holds stays that tool's: this one takes
			// its hashed name, and is refused when that is taken too.
			toolName := mcpTool.Name()
			if agent.Tools.HasRegistered(toolName) {
				mcpTool.UseHashedName()
				hashed := mcpTool.Name()
				if hashed == toolName || agent.Tools.HasRegistered(hashed) {
					logger.ErrorCF("agent", "MCP tool not registered: another tool has its name",
						map[string]any{
							"agent_id": agentID,
							"server":   serverName,
							"tool":     tool.Name,
							"name":     toolName,
						})
					continue
				}
				toolName = hashed
			}

			if registerAsHidden {
				agent.Tools.RegisterHidden(mcpTool)
			} else {
				agent.Tools.Register(mcpTool)
			}
			// The agent's tool allowlist may leave it out.
			if !agent.Tools.HasRegistered(toolName) {
				continue
			}

			current[agentID] = append(current[agentID], toolName)
			total++
			logger.DebugCF("agent", "Registered MCP tool",
				map[string]any{
					"agent_id": agentID,
					"server":   serverName,
					"tool":     tool.Name,
					"name":     toolName,
					"deferred": registerAsHidden,
				})
		}
		if len(current[agentID]) > 0 || len(previous[agentID]) > 0 {
			registerMCPServerPromptContributor(agentID, agent, serverName, len(current[agentID]), registerAsHidden)
		}
	}
	index.registered[serverName] = current
	if previous != nil {
		logger.InfoCF("agent", "MCP server tools registered again after its tool list changed",
			map[string]any{"server": serverName, "registrations": total})
	}
	return total
}

// mcpWorkspaces returns the folders the MCP servers are told as roots: the
// default workspace, then each agent's.
func mcpWorkspaces(defaultWorkspace string, registry *AgentRegistry) []string {
	dirs := []string{defaultWorkspace}
	agentIDs := registry.ListAgentIDs()
	sort.Strings(agentIDs)
	for _, agentID := range agentIDs {
		if agent, ok := registry.GetAgent(agentID); ok && agent != nil && agent.Workspace != "" {
			dirs = append(dirs, agent.Workspace)
		}
	}
	return dirs
}

func registerMCPServerPromptContributor(
	agentID string,
	agent *AgentInstance,
	serverName string,
	toolCount int,
	registerAsHidden bool,
) {
	if agent == nil || agent.ContextBuilder == nil {
		return
	}
	if err := agent.ContextBuilder.RegisterPromptContributor(mcpServerPromptContributor{
		serverName: serverName,
		toolCount:  toolCount,
		deferred:   registerAsHidden,
	}); err != nil {
		logger.WarnCF("agent", "Failed to register MCP prompt contributor",
			map[string]any{
				"agent_id": agentID,
				"server":   serverName,
				"error":    err.Error(),
			})
	}
}

func toolRegistryIncludes(registry *tools.ToolRegistry, name string) bool {
	if registry == nil {
		return false
	}
	return registry.HasRegistered(name)
}

func filterMCPConfigServers(
	mcpCfg config.MCPConfig,
	allowed map[string]struct{},
) config.MCPConfig {
	if allowed == nil {
		return mcpCfg
	}

	filtered := mcpCfg
	filtered.Servers = make(map[string]config.MCPServerConfig)
	normalizedAllowed := make(map[string]struct{}, len(allowed))
	for serverName := range allowed {
		name := normalizeMCPServerName(serverName)
		if name == "" {
			continue
		}
		normalizedAllowed[name] = struct{}{}
	}
	for serverName, serverCfg := range mcpCfg.Servers {
		if _, ok := normalizedAllowed[normalizeMCPServerName(serverName)]; ok {
			filtered.Servers[serverName] = serverCfg
		}
	}

	return filtered
}

func agentHasDiscoverableMCPServers(cfg *config.Config, allowed map[string]struct{}) bool {
	if cfg == nil || !cfg.Tools.MCP.Enabled || !cfg.Tools.MCP.Discovery.Enabled {
		return false
	}

	filtered := filterMCPConfigServers(cfg.Tools.MCP, allowed)
	for _, serverCfg := range filtered.Servers {
		if serverCfg.Enabled && serverIsDeferred(cfg.Tools.MCP.Discovery.Enabled, serverCfg) {
			return true
		}
	}

	return false
}

// serverIsDeferred reports whether an MCP server's tools should be registered
// as hidden (deferred/discovery mode).
//
// The per-server Deferred field takes precedence over the global discoveryEnabled
// default. When Deferred is nil, discoveryEnabled is used as the fallback.
func serverIsDeferred(discoveryEnabled bool, serverCfg config.MCPServerConfig) bool {
	if !discoveryEnabled {
		return false
	}
	if serverCfg.Deferred != nil {
		return *serverCfg.Deferred
	}
	return true
}
