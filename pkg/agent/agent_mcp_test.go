// Compa - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 Compa contributors

package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/xibodev/compa/pkg/approval"
	"github.com/xibodev/compa/pkg/config"
	"github.com/xibodev/compa/pkg/mcp"
	agenttools "github.com/xibodev/compa/pkg/tools"
)

func boolPtr(b bool) *bool { return &b }

func TestMCPRuntimeResetClearsState(t *testing.T) {
	var rt mcpRuntime
	manager := mcp.NewManager()
	rt.setManager(manager)
	rt.setInitErr(errors.New("stale init error"))
	if _, _, run := rt.begin(); !run {
		t.Fatal("expected the first begin to run the initialization")
	}

	got := rt.reset()
	if got != manager {
		t.Fatalf("reset() manager = %p, want %p", got, manager)
	}
	if rt.hasManager() {
		t.Fatal("expected manager to be cleared after reset")
	}
	if err := rt.getInitErr(); err != nil {
		t.Fatalf("getInitErr() = %v, want nil", err)
	}
	if _, _, run := rt.begin(); !run {
		t.Fatal("expected a new initialization after reset")
	}
}

func TestMCPRuntimeDiscardsInitializationOfReplacedConfig(t *testing.T) {
	var rt mcpRuntime
	gen, done, run := rt.begin()
	if !run {
		t.Fatal("expected the first begin to run the initialization")
	}
	if _, _, again := rt.begin(); again {
		t.Fatal("a second caller must wait, not run the initialization again")
	}

	rt.reset() // a reload while the old config's servers connect
	if kept := rt.finish(gen, done, nil, errors.New("old config failed")); kept {
		t.Fatal("the replaced config's result must not be kept")
	}
	select {
	case <-done:
	default:
		t.Fatal("finish must release whoever waits for the old initialization")
	}
	if err := rt.getInitErr(); err != nil {
		t.Fatalf("getInitErr() = %v, want the new config's (none yet)", err)
	}
}

func TestReloadProviderAndConfig_ResetsMCPRuntime(t *testing.T) {
	al, cfg, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	defer al.Close()

	manager := mcp.NewManager()
	al.mcp.setManager(manager)
	al.mcp.setInitErr(errors.New("stale init error"))
	al.mcp.begin()

	if !al.mcp.hasManager() {
		t.Fatal("expected MCP manager to exist before reload")
	}

	// As the gateway's, the reload's context ends as soon as it returns.
	ctx, cancel := context.WithCancel(context.Background())
	err := al.ReloadProviderAndConfig(ctx, &mockProvider{}, cfg)
	cancel()
	if err != nil {
		t.Fatalf("ReloadProviderAndConfig() error = %v", err)
	}

	if al.mcp.hasManager() {
		t.Fatal("expected MCP manager to be cleared when reloaded config has MCP disabled")
	}
	if err := al.mcp.getInitErr(); err != nil {
		t.Fatalf("getInitErr() = %v, want nil", err)
	}
	if _, _, run := al.mcp.begin(); !run {
		t.Fatal("expected MCP initialization to be reset after reload")
	}
}

func TestServerIsDeferred(t *testing.T) {
	tests := []struct {
		name             string
		discoveryEnabled bool
		serverDeferred   *bool
		want             bool
	}{
		// --- global false always wins: per-server deferred is ignored ---
		{
			name:             "global false: per-server deferred=true is ignored",
			discoveryEnabled: false,
			serverDeferred:   boolPtr(true),
			want:             false,
		},
		{
			name:             "global false: per-server deferred=false stays false",
			discoveryEnabled: false,
			serverDeferred:   boolPtr(false),
			want:             false,
		},
		// --- global true: per-server override applies ---
		{
			name:             "global true: per-server deferred=false opts out",
			discoveryEnabled: true,
			serverDeferred:   boolPtr(false),
			want:             false,
		},
		{
			name:             "global true: per-server deferred=true stays true",
			discoveryEnabled: true,
			serverDeferred:   boolPtr(true),
			want:             true,
		},
		// --- no per-server override: fall back to global ---
		{
			name:             "no per-server field, global discovery enabled",
			discoveryEnabled: true,
			serverDeferred:   nil,
			want:             true,
		},
		{
			name:             "no per-server field, global discovery disabled",
			discoveryEnabled: false,
			serverDeferred:   nil,
			want:             false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			serverCfg := config.MCPServerConfig{Deferred: tt.serverDeferred}
			got := serverIsDeferred(tt.discoveryEnabled, serverCfg)
			if got != tt.want {
				t.Errorf("serverIsDeferred(discoveryEnabled=%v, deferred=%v) = %v, want %v",
					tt.discoveryEnabled, tt.serverDeferred, got, tt.want)
			}
		})
	}
}

func TestRegisterMCPServerPromptContributorUsesActualRegisteredToolCount(t *testing.T) {
	cb := NewContextBuilder(t.TempDir())
	agent := &AgentInstance{ContextBuilder: cb}

	registerMCPServerPromptContributor("research", agent, "github", 0, false)
	messages := cb.BuildMessagesFromPrompt(PromptBuildRequest{CurrentMessage: "hello"})
	if prompt := messages[0].Content; strings.Contains(prompt, "MCP server `github`") {
		t.Fatalf("expected no MCP prompt when no tools were registered, got %q", prompt)
	}

	registerMCPServerPromptContributor("research", agent, "github", 2, false)
	messages = cb.BuildMessagesFromPrompt(PromptBuildRequest{CurrentMessage: "hello"})
	prompt := messages[0].Content
	if !strings.Contains(prompt, "MCP server `github` is connected") {
		t.Fatalf("expected MCP prompt for registered tools, got %q", prompt)
	}
	if !strings.Contains(prompt, "It contributes 2 tool(s)") {
		t.Fatalf("expected actual registered tool count in prompt, got %q", prompt)
	}
}

func TestToolRegistryIncludesReportsOnlyRegisteredTools(t *testing.T) {
	registry := agenttools.NewToolRegistry()
	registry.SetAllowlist([]string{"mcp_github_search"})

	registry.RegisterHidden(&allowlistTestTool{name: "mcp_github_search"})
	registry.RegisterHidden(&allowlistTestTool{name: "mcp_github_create_issue"})

	if !toolRegistryIncludes(registry, "mcp_github_search") {
		t.Fatal("expected hidden registered MCP tool to be included")
	}
	if toolRegistryIncludes(registry, "mcp_github_create_issue") {
		t.Fatal("blocked MCP tool should not be included")
	}
}

func TestFilterMCPConfigServersCaseInsensitivePreservesOriginalKeys(t *testing.T) {
	mcpCfg := config.MCPConfig{
		Servers: map[string]config.MCPServerConfig{
			"GitHub":     {Enabled: true},
			"filesystem": {Enabled: true},
			"Slack":      {Enabled: true},
		},
	}
	allowed := map[string]struct{}{
		"github":     {},
		"FILESYSTEM": {},
	}

	filtered := filterMCPConfigServers(mcpCfg, allowed)

	if len(filtered.Servers) != 2 {
		t.Fatalf("filtered.Servers = %v, want 2 entries", filtered.Servers)
	}
	if _, ok := filtered.Servers["GitHub"]; !ok {
		t.Fatal("expected original GitHub config key to be preserved")
	}
	if _, ok := filtered.Servers["filesystem"]; !ok {
		t.Fatal("expected filesystem config key to be preserved")
	}
	if _, ok := filtered.Servers["github"]; ok {
		t.Fatal("did not expect normalized github key to replace original config key")
	}
	if _, ok := filtered.Servers["Slack"]; ok {
		t.Fatal("did not expect unallowed Slack server")
	}
}

func TestAgentHasDiscoverableMCPServers(t *testing.T) {
	deferredFalse := false
	cfg := &config.Config{
		Tools: config.ToolsConfig{
			MCP: config.MCPConfig{
				ToolConfig: config.ToolConfig{Enabled: true},
				Discovery: config.ToolDiscoveryConfig{
					Enabled:  true,
					UseBM25:  true,
					UseRegex: false,
				},
				Servers: map[string]config.MCPServerConfig{
					"github":     {Enabled: true},
					"filesystem": {Enabled: true, Deferred: &deferredFalse},
				},
			},
		},
	}

	tests := []struct {
		name    string
		allowed map[string]struct{}
		want    bool
	}{
		{
			name: "nil allowlist includes discoverable enabled server",
			want: true,
		},
		{
			name:    "empty allowlist denies all servers",
			allowed: map[string]struct{}{},
			want:    false,
		},
		{
			name: "selected server discoverable",
			allowed: map[string]struct{}{
				"github": {},
			},
			want: true,
		},
		{
			name: "selected server opted out of discovery",
			allowed: map[string]struct{}{
				"filesystem": {},
			},
			want: false,
		},
		{
			name: "unknown allowlist server matches nothing",
			allowed: map[string]struct{}{
				"slack": {},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := agentHasDiscoverableMCPServers(cfg, tt.allowed); got != tt.want {
				t.Fatalf("agentHasDiscoverableMCPServers() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEnsureMCPInitialized_LoadFailureSetsInitErr(t *testing.T) {
	al, cfg, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	defer al.Close()

	cfg.Tools = config.ToolsConfig{
		MCP: config.MCPConfig{
			ToolConfig: config.ToolConfig{Enabled: true},
			Servers: map[string]config.MCPServerConfig{
				"broken": {
					Enabled: true,
					Command: "compa-command-that-does-not-exist-for-mcp-tests",
				},
			},
		},
	}

	// The caller's context bounds only its own wait: the servers connect on
	// the loop's lifetime, so a caller whose context has ended still gets
	// the load failure, not a cancellation.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := al.ensureMCPInitialized(ctx)
	if err == nil {
		t.Fatal("ensureMCPInitialized() error = nil, want load failure")
	}
	if !strings.Contains(err.Error(), "failed to load MCP servers") {
		t.Fatalf("ensureMCPInitialized() error = %q, want wrapped load failure", err.Error())
	}

	initErr := al.mcp.getInitErr()
	if initErr == nil {
		t.Fatal("getInitErr() = nil, want cached load failure")
	}
	if !strings.Contains(initErr.Error(), "failed to load MCP servers") {
		t.Fatalf("getInitErr() = %q, want wrapped load failure", initErr.Error())
	}
	if al.mcp.getManager() != nil {
		t.Fatal("expected MCP manager to remain nil after load failure")
	}

	err = al.ensureMCPInitialized(ctx)
	if err == nil {
		t.Fatal("second ensureMCPInitialized() error = nil, want cached load failure")
	}
	if !strings.Contains(err.Error(), "failed to load MCP servers") {
		t.Fatalf("second ensureMCPInitialized() error = %q, want wrapped load failure", err.Error())
	}
}

// The MCP servers are told the agents' workspaces as roots, and of two tools
// whose names collide the second gets the hashed name instead of replacing
// the first.
func TestMCPServersGetWorkspaceRootsAndCollidingNamesStayApart(t *testing.T) {
	newServer := func(tool string) *httptest.Server {
		server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "srv", Version: "1.0.0"}, nil)
		sdkmcp.AddTool(server, &sdkmcp.Tool{Name: tool, Description: "lists roots"},
			func(ctx context.Context, req *sdkmcp.CallToolRequest, _ map[string]any) (*sdkmcp.CallToolResult, any, error) {
				res, err := req.Session.ListRoots(ctx, nil)
				if err != nil {
					return nil, nil, err
				}
				var uris []string
				for _, root := range res.Roots {
					uris = append(uris, root.URI)
				}
				return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{
					&sdkmcp.TextContent{Text: strings.Join(uris, "\n")},
				}}, nil, nil
			})
		return httptest.NewServer(sdkmcp.NewStreamableHTTPHandler(
			func(*http.Request) *sdkmcp.Server { return server }, nil))
	}
	first, second := newServer("b_c"), newServer("c")
	defer first.Close()
	defer second.Close()

	al, cfg, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	defer al.Close()
	cfg.Tools = config.ToolsConfig{
		MCP: config.MCPConfig{
			ToolConfig: config.ToolConfig{Enabled: true},
			Servers: map[string]config.MCPServerConfig{
				"a":   {Enabled: true, Type: "http", URL: first.URL, Trusted: true},
				"a_b": {Enabled: true, Type: "http", URL: second.URL},
			},
		},
	}
	if err := al.ensureMCPInitialized(context.Background()); err != nil {
		t.Fatalf("ensureMCPInitialized() error = %v", err)
	}
	agent := al.GetRegistry().GetDefaultAgent()

	plain, ok := agent.Tools.Get("mcp_a_b_c")
	if !ok {
		t.Fatal("the first server's tool lost its plain name")
	}
	var hashed agenttools.Tool
	for _, name := range agent.Tools.List() {
		if strings.HasPrefix(name, "mcp_a_b_c_") {
			hashed, _ = agent.Tools.Get(name)
		}
	}
	if hashed == nil {
		t.Fatalf("the colliding tool was not registered under its hashed name: %v", agent.Tools.List())
	}
	// Each tool tells the approval policy its server, and whether it is
	// trusted.
	if info := plain.(approval.Described).ApprovalInfo(); info.Source != "mcp:a" || !info.Trusted {
		t.Fatalf("first server's tool ApprovalInfo() = %+v, want trusted mcp:a", info)
	}
	if info := hashed.(approval.Described).ApprovalInfo(); info.Source != "mcp:a_b" || info.Trusted {
		t.Fatalf("second server's tool ApprovalInfo() = %+v, want untrusted mcp:a_b", info)
	}

	result := plain.Execute(context.Background(), nil)
	if result.IsError {
		t.Fatalf("roots tool failed: %s", result.ForLLM)
	}
	want := filepath.ToSlash(agent.Workspace)
	if !strings.Contains(result.ForLLM, "file://") || !strings.Contains(result.ForLLM, want) {
		t.Fatalf("roots = %q, want the workspace %q", result.ForLLM, want)
	}
}

// TL-12: when a server announces its tool list changed, the agents get its
// new tools and lose the ones it dropped.
func TestMCPToolListChangeReregistersTheServerTools(t *testing.T) {
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "dynamic", Version: "1.0.0"}, nil)
	echo := func(context.Context, *sdkmcp.CallToolRequest, map[string]any) (*sdkmcp.CallToolResult, any, error) {
		return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "ok"}}}, nil, nil
	}
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "first", Description: "first tool"}, echo)
	httpServer := httptest.NewServer(sdkmcp.NewStreamableHTTPHandler(
		func(*http.Request) *sdkmcp.Server { return server }, nil))
	defer httpServer.Close()

	al, cfg, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	defer al.Close()
	cfg.Tools = config.ToolsConfig{
		MCP: config.MCPConfig{
			ToolConfig: config.ToolConfig{Enabled: true},
			Servers: map[string]config.MCPServerConfig{
				// "sse" keeps the stream open that server notifications arrive on.
				"dyn": {Enabled: true, Type: "sse", URL: httpServer.URL},
			},
		},
	}
	if err := al.ensureMCPInitialized(context.Background()); err != nil {
		t.Fatalf("ensureMCPInitialized() error = %v", err)
	}
	agent := al.GetRegistry().GetDefaultAgent()
	if !toolRegistryIncludes(agent.Tools, "mcp_dyn_first") {
		t.Fatal("the server's tool was not registered at connect")
	}

	waitFor := func(what string, change func(), done func() bool) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for !done() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting until %s", what)
			}
			change()
			time.Sleep(200 * time.Millisecond)
		}
	}
	// The notification stream opens after connecting: announce until heard.
	added := 0
	waitFor("the added tool is registered", func() {
		added++
		sdkmcp.AddTool(server, &sdkmcp.Tool{Name: fmt.Sprintf("added_%d", added), Description: "added"}, echo)
	}, func() bool { return toolRegistryIncludes(agent.Tools, "mcp_dyn_added_1") })

	server.RemoveTools("first")
	waitFor("the removed tool is unregistered", func() {}, func() bool {
		return !toolRegistryIncludes(agent.Tools, "mcp_dyn_first")
	})
	if !toolRegistryIncludes(agent.Tools, "mcp_dyn_added_1") {
		t.Fatal("registering the changed tools again dropped a tool the server still has")
	}
}
