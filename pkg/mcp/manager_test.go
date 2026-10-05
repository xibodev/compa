package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/xibodev/compa/pkg/config"
	runtimeevents "github.com/xibodev/compa/pkg/events"
)

func TestLoadEnvFile(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		expected  map[string]string
		expectErr bool
	}{
		{
			name: "basic env file",
			content: `API_KEY=secret123
DATABASE_URL=postgres://localhost/db
PORT=8080`,
			expected: map[string]string{
				"API_KEY":      "secret123",
				"DATABASE_URL": "postgres://localhost/db",
				"PORT":         "8080",
			},
			expectErr: false,
		},
		{
			name: "with comments and empty lines",
			content: `# This is a comment
API_KEY=secret123

# Another comment
DATABASE_URL=postgres://localhost/db

PORT=8080`,
			expected: map[string]string{
				"API_KEY":      "secret123",
				"DATABASE_URL": "postgres://localhost/db",
				"PORT":         "8080",
			},
			expectErr: false,
		},
		{
			name: "with quoted values",
			content: `API_KEY="secret with spaces"
NAME='single quoted'
PLAIN=no-quotes`,
			expected: map[string]string{
				"API_KEY": "secret with spaces",
				"NAME":    "single quoted",
				"PLAIN":   "no-quotes",
			},
			expectErr: false,
		},
		{
			name: "with spaces around equals",
			content: `API_KEY = secret123
DATABASE_URL= postgres://localhost/db
PORT =8080`,
			expected: map[string]string{
				"API_KEY":      "secret123",
				"DATABASE_URL": "postgres://localhost/db",
				"PORT":         "8080",
			},
			expectErr: false,
		},
		{
			name:      "invalid format - no equals",
			content:   `INVALID_LINE`,
			expectErr: true,
		},
		{
			name:      "empty file",
			content:   ``,
			expected:  map[string]string{},
			expectErr: false,
		},
		{
			name: "only comments",
			content: `# Comment 1
# Comment 2`,
			expected:  map[string]string{},
			expectErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			envFile := filepath.Join(tmpDir, ".env")

			if err := os.WriteFile(envFile, []byte(tt.content), 0o644); err != nil {
				t.Fatalf("Failed to create test file: %v", err)
			}

			result, err := loadEnvFile(envFile)

			if tt.expectErr {
				if err == nil {
					t.Errorf("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if len(result) != len(tt.expected) {
				t.Errorf("Expected %d variables, got %d", len(tt.expected), len(result))
			}

			for key, expectedValue := range tt.expected {
				if actualValue, ok := result[key]; !ok {
					t.Errorf("Expected key %s not found", key)
				} else if actualValue != expectedValue {
					t.Errorf("For key %s: expected %q, got %q", key, expectedValue, actualValue)
				}
			}
		})
	}
}

func TestLoadEnvFileNotFound(t *testing.T) {
	_, err := loadEnvFile("/nonexistent/file.env")
	if err == nil {
		t.Error("Expected error for nonexistent file")
	}
}

func TestExpandHomeCommandPath(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)

	want := filepath.Join(homeDir, "bin", "my-mcp")
	got := expandHomeCommandPath("~" + string(os.PathSeparator) + filepath.Join("bin", "my-mcp"))
	if got != want {
		t.Fatalf("expandHomeCommandPath() = %q, want %q", got, want)
	}

	if got := expandHomeCommandPath("npx"); got != "npx" {
		t.Fatalf("expandHomeCommandPath() should leave bare commands unchanged, got %q", got)
	}
}

func TestEnvFilePriority(t *testing.T) {
	// Create a temporary .env file
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, ".env")

	envContent := `API_KEY=from_file
DATABASE_URL=from_file
SHARED_VAR=from_file`

	if err := os.WriteFile(envFile, []byte(envContent), 0o644); err != nil {
		t.Fatalf("Failed to create .env file: %v", err)
	}

	// Load envFile
	envVars, err := loadEnvFile(envFile)
	if err != nil {
		t.Fatalf("Failed to load env file: %v", err)
	}

	// Verify envFile variables
	if envVars["API_KEY"] != "from_file" {
		t.Errorf("Expected API_KEY=from_file, got %s", envVars["API_KEY"])
	}

	// Simulate config.Env overriding envFile
	configEnv := map[string]string{
		"SHARED_VAR": "from_config",
		"NEW_VAR":    "from_config",
	}

	// Merge: envFile first, then config overrides
	merged := make(map[string]string)
	for k, v := range envVars {
		merged[k] = v
	}
	for k, v := range configEnv {
		merged[k] = v
	}

	// Verify priority: config.Env should override envFile
	if merged["SHARED_VAR"] != "from_config" {
		t.Errorf(
			"Expected SHARED_VAR=from_config (config should override file), got %s",
			merged["SHARED_VAR"],
		)
	}
	if merged["API_KEY"] != "from_file" {
		t.Errorf("Expected API_KEY=from_file, got %s", merged["API_KEY"])
	}
	if merged["NEW_VAR"] != "from_config" {
		t.Errorf("Expected NEW_VAR=from_config, got %s", merged["NEW_VAR"])
	}
}

func TestLoadFromMCPConfig_EmptyWorkspaceWithRelativeEnvFile(t *testing.T) {
	mgr := NewManager()

	mcpCfg := config.MCPConfig{
		ToolConfig: config.ToolConfig{
			Enabled: true,
		},
		Servers: map[string]config.MCPServerConfig{
			"test-server": {
				Enabled: true,
				Command: "echo",
				Args:    []string{"ok"},
				EnvFile: ".env",
			},
		},
	}

	err := mgr.LoadFromMCPConfig(context.Background(), mcpCfg, "")
	if err == nil {
		t.Fatal("expected error for relative env_file with empty workspace path, got nil")
	}

	if !strings.Contains(err.Error(), "workspace path is empty") {
		t.Fatalf("expected workspace path validation error, got: %v", err)
	}
}

func TestNewManager_InitialState(t *testing.T) {
	mgr := NewManager()
	if mgr == nil {
		t.Fatal("expected manager instance, got nil")
	}
	if len(mgr.GetServers()) != 0 {
		t.Fatalf("expected no servers on new manager, got %d", len(mgr.GetServers()))
	}
}

func TestConnectServerPublishesRuntimeEvents(t *testing.T) {
	originalConnectServerFunc := connectServerFunc
	t.Cleanup(func() {
		connectServerFunc = originalConnectServerFunc
	})

	eventBus := runtimeevents.NewBus()
	defer func() {
		if err := eventBus.Close(); err != nil {
			t.Errorf("event bus close failed: %v", err)
		}
	}()

	_, eventsCh, err := eventBus.Channel().OfKind(
		runtimeevents.KindMCPServerConnected,
		runtimeevents.KindMCPServerFailed,
	).SubscribeChan(t.Context(), runtimeevents.SubscribeOptions{Name: "mcp-events", Buffer: 2})
	if err != nil {
		t.Fatalf("SubscribeChan failed: %v", err)
	}

	connectServerFunc = func(
		_ context.Context,
		name string,
		cfg config.MCPServerConfig,
		_ connectOptions,
	) (*ServerConnection, error) {
		if name == "bad" {
			return nil, fmt.Errorf("connect failed")
		}
		return &ServerConnection{
			Name:   name,
			Config: cfg,
			Tools:  []*sdkmcp.Tool{{Name: "echo"}},
		}, nil
	}

	mgr := NewManager(WithRuntimeEvents(eventBus))
	err = mgr.ConnectServer(context.Background(), "good", config.MCPServerConfig{
		Type:    "stdio",
		Command: "echo",
	})
	if err != nil {
		t.Fatalf("ConnectServer(good) error = %v", err)
	}
	connected := receiveMCPRuntimeEvent(t, eventsCh)
	if connected.Kind != runtimeevents.KindMCPServerConnected ||
		connected.Source.Name != "good" ||
		connected.Severity != runtimeevents.SeverityInfo {
		t.Fatalf("connected event = %+v", connected)
	}
	if connected.Attrs["server"] != "good" ||
		connected.Attrs["type"] != "stdio" ||
		connected.Attrs["tool_count"] != 1 {
		t.Fatalf("connected attrs = %#v", connected.Attrs)
	}

	err = mgr.ConnectServer(context.Background(), "bad", config.MCPServerConfig{
		Type:    "stdio",
		Command: "echo",
	})
	if err == nil {
		t.Fatal("expected ConnectServer(bad) to fail")
	}
	failed := receiveMCPRuntimeEvent(t, eventsCh)
	if failed.Kind != runtimeevents.KindMCPServerFailed ||
		failed.Source.Name != "bad" ||
		failed.Severity != runtimeevents.SeverityError {
		t.Fatalf("failed event = %+v", failed)
	}
	if failed.Attrs["server"] != "bad" || failed.Attrs["error"] != "connect failed" {
		t.Fatalf("failed attrs = %#v", failed.Attrs)
	}
}

func receiveMCPRuntimeEvent(t *testing.T, ch <-chan runtimeevents.Event) runtimeevents.Event {
	t.Helper()

	select {
	case evt, ok := <-ch:
		if !ok {
			t.Fatal("runtime event channel closed before expected event")
		}
		return evt
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for runtime event")
		return runtimeevents.Event{}
	}
}

func TestLoadFromMCPConfig_DisabledOrEmptyServers(t *testing.T) {
	mgr := NewManager()

	err := mgr.LoadFromMCPConfig(
		context.Background(),
		config.MCPConfig{ToolConfig: config.ToolConfig{Enabled: false}},
		"/tmp",
	)
	if err != nil {
		t.Fatalf("expected nil error when MCP disabled, got: %v", err)
	}

	err = mgr.LoadFromMCPConfig(
		context.Background(),
		config.MCPConfig{ToolConfig: config.ToolConfig{Enabled: true}},
		"/tmp",
	)
	if err != nil {
		t.Fatalf("expected nil error when no servers configured, got: %v", err)
	}
}

func TestGetServers_ReturnsCopy(t *testing.T) {
	mgr := NewManager()
	mgr.servers["s1"] = &ServerConnection{Name: "s1"}

	servers := mgr.GetServers()
	delete(servers, "s1")

	if _, ok := mgr.GetServer("s1"); !ok {
		t.Fatal("expected internal manager state to remain unchanged")
	}
}

func TestGetAllTools_FiltersEmptyTools(t *testing.T) {
	mgr := NewManager()
	mgr.servers["empty"] = &ServerConnection{Name: "empty", Tools: nil}
	mgr.servers["with-tools"] = &ServerConnection{Name: "with-tools", Tools: []*sdkmcp.Tool{{}}}

	all := mgr.GetAllTools()
	if _, ok := all["empty"]; ok {
		t.Fatal("expected server without tools to be excluded")
	}
	if _, ok := all["with-tools"]; !ok {
		t.Fatal("expected server with tools to be included")
	}
}

func TestCallTool_ErrorsForClosedOrMissingServer(t *testing.T) {
	t.Run("manager closed", func(t *testing.T) {
		mgr := NewManager()
		mgr.closed.Store(true)

		_, err := mgr.CallTool(context.Background(), "s1", "tool", nil)
		if err == nil || !strings.Contains(err.Error(), "manager is closed") {
			t.Fatalf("expected manager closed error, got: %v", err)
		}
	})

	t.Run("server missing", func(t *testing.T) {
		mgr := NewManager()

		_, err := mgr.CallTool(context.Background(), "missing", "tool", nil)
		if err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("expected server not found error, got: %v", err)
		}
	})
}

func TestConnectServer_StreamableHTTPRequestResponseMode(t *testing.T) {
	t.Parallel()

	for _, transportType := range []string{"http", "streamable-http"} {
		t.Run(transportType, func(t *testing.T) {
			t.Parallel()

			server := sdkmcp.NewServer(&sdkmcp.Implementation{
				Name:    "streamable-test-server",
				Version: "1.0.0",
			}, nil)
			sdkmcp.AddTool(server, &sdkmcp.Tool{
				Name:        "echo",
				Description: "Echo test tool",
			}, func(ctx context.Context, req *sdkmcp.CallToolRequest, args map[string]any) (*sdkmcp.CallToolResult, any, error) {
				return &sdkmcp.CallToolResult{
					Content: []sdkmcp.Content{
						&sdkmcp.TextContent{Text: "ok"},
					},
				}, nil, nil
			})

			type observedRequest struct {
				Method        string
				SessionID     string
				Authorization string
			}

			var (
				mu       sync.Mutex
				observed []observedRequest
			)

			handler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server {
				return server
			}, nil)
			httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				observed = append(observed, observedRequest{
					Method:        r.Method,
					SessionID:     r.Header.Get("Mcp-Session-Id"),
					Authorization: r.Header.Get("Authorization"),
				})
				mu.Unlock()
				handler.ServeHTTP(w, r)
			}))
			defer httpServer.Close()

			conn, err := connectServer(context.Background(), "streamable", config.MCPServerConfig{
				Enabled: true,
				Type:    transportType,
				URL:     httpServer.URL,
				Headers: map[string]string{
					"Authorization": "Bearer test-token",
				},
			}, connectOptions{})
			if err != nil {
				t.Fatalf("connectServer(%q) error = %v", transportType, err)
			}
			if got := len(conn.Tools); got != 1 {
				t.Fatalf("len(conn.Tools) = %d, want 1", got)
			}
			if got := conn.Session.ID(); got == "" {
				t.Fatal("expected non-empty streamable session ID")
			}
			if err := conn.Session.Close(); err != nil {
				t.Fatalf("Session.Close() error = %v", err)
			}

			mu.Lock()
			defer mu.Unlock()

			var (
				getCount            int
				postCount           int
				deleteCount         int
				postWithSession     bool
				deleteWithSession   bool
				requestsWithAuth    int
				requestsWithoutAuth []string
			)

			for _, req := range observed {
				switch req.Method {
				case http.MethodGet:
					getCount++
				case http.MethodPost:
					postCount++
					if req.SessionID != "" {
						postWithSession = true
					}
				case http.MethodDelete:
					deleteCount++
					if req.SessionID != "" {
						deleteWithSession = true
					}
				}

				if req.Authorization == "Bearer test-token" {
					requestsWithAuth++
				} else {
					requestsWithoutAuth = append(requestsWithoutAuth, req.Method)
				}
			}

			if getCount != 0 {
				t.Fatalf("expected no standalone GET requests for %q transport, saw %d", transportType, getCount)
			}
			if postCount == 0 {
				t.Fatal("expected POST requests during streamable HTTP handshake")
			}
			if deleteCount != 1 {
				t.Fatalf("DELETE count = %d, want 1", deleteCount)
			}
			if !postWithSession {
				t.Fatal("expected at least one POST request with Mcp-Session-Id")
			}
			if !deleteWithSession {
				t.Fatal("expected DELETE request with Mcp-Session-Id")
			}
			if requestsWithAuth != len(observed) {
				t.Fatalf("Authorization header missing on requests: %v", requestsWithoutAuth)
			}
		})
	}
}

// A call whose session the server lost is sent again only for a tool that a
// trusted server declares read-only or idempotent; either way the server is
// reconnected for the calls that follow.
func TestCallToolAfterLostSession(t *testing.T) {
	cases := []struct {
		name        string
		trusted     bool
		annotations *sdkmcp.ToolAnnotations
		wantRetry   bool
	}{
		{name: "untrusted idempotent", annotations: &sdkmcp.ToolAnnotations{IdempotentHint: true}},
		{name: "trusted without hints", trusted: true},
		{name: "trusted destructive", trusted: true, annotations: &sdkmcp.ToolAnnotations{}},
		{name: "trusted read-only", trusted: true, annotations: &sdkmcp.ToolAnnotations{ReadOnlyHint: true}, wantRetry: true},
		{name: "trusted idempotent", trusted: true, annotations: &sdkmcp.ToolAnnotations{IdempotentHint: true}, wantRetry: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			originalConnectServerFunc := connectServerFunc
			t.Cleanup(func() { connectServerFunc = originalConnectServerFunc })

			staleConn, staleTransport, err := newScriptedServerConnection(
				"session-1",
				nil,
				fmt.Errorf(`sending "tools/call": failed to connect (session ID: session-1): %w`, sdkmcp.ErrSessionMissing),
			)
			if err != nil {
				t.Fatalf("newScriptedServerConnection(stale) error = %v", err)
			}
			staleConn.Config.Trusted = tc.trusted
			staleConn.Tools[0].Annotations = tc.annotations
			freshConn, freshTransport, err := newScriptedServerConnection(
				"session-2",
				&sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "reconnected"}}},
				nil,
			)
			if err != nil {
				t.Fatalf("newScriptedServerConnection(fresh) error = %v", err)
			}

			connectCalls := 0
			connectServerFunc = func(context.Context, string, config.MCPServerConfig, connectOptions) (*ServerConnection, error) {
				connectCalls++
				if connectCalls == 1 {
					return freshConn, nil
				}
				return nil, fmt.Errorf("unexpected reconnect attempt %d", connectCalls)
			}

			mgr := NewManager()
			mgr.servers["flaky"] = staleConn

			result, err := mgr.CallTool(context.Background(), "flaky", "echo", map[string]any{"query": "hello"})
			if tc.wantRetry {
				if err != nil {
					t.Fatalf("CallTool() error = %v", err)
				}
				if got := joinTextContent(result); got != "reconnected" {
					t.Fatalf("CallTool() text = %q, want %q", got, "reconnected")
				}
			} else if !errors.Is(err, ErrSessionLost) || !strings.Contains(err.Error(), "may or may not have run") {
				t.Fatalf("CallTool() error = %v, want ErrSessionLost", err)
			}
			if staleTransport.calls() != 1 {
				t.Fatalf("stale session got %d tools/call, want 1", staleTransport.calls())
			}
			wantFresh := 0
			if tc.wantRetry {
				wantFresh = 1
			}
			if freshTransport.calls() != wantFresh {
				t.Fatalf("fresh session got %d tools/call, want %d", freshTransport.calls(), wantFresh)
			}

			// The server was reconnected for the calls that follow.
			conn, ok := mgr.GetServer("flaky")
			if !ok || conn.Session.ID() != "session-2" || connectCalls != 1 {
				t.Fatalf("server after the lost session: ok=%v connects=%d", ok, connectCalls)
			}
			if _, err := mgr.CallTool(context.Background(), "flaky", "echo", nil); err != nil {
				t.Fatalf("next CallTool() error = %v", err)
			}
		})
	}
}

// A server reconnected after its session ended may list other tools: the
// manager's tools-changed handler is told them.
func TestReconnectedServerToolsReachTheToolsChangedHandler(t *testing.T) {
	originalConnectServerFunc := connectServerFunc
	t.Cleanup(func() { connectServerFunc = originalConnectServerFunc })

	staleConn, _, err := newScriptedServerConnection("session-1", nil, nil)
	if err != nil {
		t.Fatalf("newScriptedServerConnection(stale) error = %v", err)
	}
	staleConn.lost.Store(true)
	freshConn, _, err := newScriptedServerConnection("session-2",
		&sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "ok"}}}, nil)
	if err != nil {
		t.Fatalf("newScriptedServerConnection(fresh) error = %v", err)
	}
	freshConn.Tools = append(freshConn.Tools, &sdkmcp.Tool{Name: "added", InputSchema: map[string]any{"type": "object"}})
	connectServerFunc = func(context.Context, string, config.MCPServerConfig, connectOptions) (*ServerConnection, error) {
		return freshConn, nil
	}

	changed := make(chan []*sdkmcp.Tool, 4)
	mgr := NewManager(WithToolsChangedHandler(func(name string, tools []*sdkmcp.Tool) {
		if name == "flaky" {
			changed <- tools
		}
	}))
	mgr.servers["flaky"] = staleConn

	if _, err := mgr.CallTool(context.Background(), "flaky", "echo", nil); err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	select {
	case tools := <-changed:
		if len(tools) != 2 || tools[1].Name != "added" {
			t.Fatalf("handler told %d tools, want the reconnected server's 2", len(tools))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the tools-changed handler was not told the reconnected server's tools")
	}
}

// A tool whose own error says "session not found" did not lose the session:
// its error comes back as it is, and the server is neither reconnected nor
// called again, even for a trusted read-only tool.
func TestCallToolReturnsAToolErrorThatMentionsASession(t *testing.T) {
	originalConnectServerFunc := connectServerFunc
	t.Cleanup(func() { connectServerFunc = originalConnectServerFunc })

	conn, transport, err := newScriptedServerConnection("session-1", nil, nil)
	if err != nil {
		t.Fatalf("newScriptedServerConnection() error = %v", err)
	}
	const toolErr = "session not found: the cart expired"
	transport.toolCallRPCErr = &jsonrpc.Error{Code: -32000, Message: toolErr}
	conn.Config.Trusted = true
	conn.Tools[0].Annotations = &sdkmcp.ToolAnnotations{ReadOnlyHint: true}
	freshConn, freshTransport, err := newScriptedServerConnection("session-2",
		&sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "sent again"}}}, nil)
	if err != nil {
		t.Fatalf("newScriptedServerConnection(fresh) error = %v", err)
	}
	connects := 0
	connectServerFunc = func(context.Context, string, config.MCPServerConfig, connectOptions) (*ServerConnection, error) {
		connects++
		return freshConn, nil
	}
	mgr := NewManager()
	mgr.servers["flaky"] = conn

	_, err = mgr.CallTool(context.Background(), "flaky", "echo", map[string]any{"query": "hello"})
	var rpcErr *jsonrpc.Error
	if !errors.As(err, &rpcErr) || rpcErr.Message != toolErr {
		t.Fatalf("CallTool() error = %v, want the tool's error", err)
	}
	if errors.Is(err, ErrSessionLost) || strings.Contains(err.Error(), "may or may not have run") {
		t.Fatalf("CallTool() error = %v, want no lost session", err)
	}
	if transport.calls() != 1 || freshTransport.calls() != 0 || connects != 0 || conn.lost.Load() {
		t.Fatalf("tools/call sent %d+%d times, %d reconnects, lost = %v; want one call and no reconnect",
			transport.calls(), freshTransport.calls(), connects, conn.lost.Load())
	}
}

func TestClose_IdempotentOnEmptyManager(t *testing.T) {
	mgr := NewManager()

	if err := mgr.Close(); err != nil {
		t.Fatalf("first close should succeed, got: %v", err)
	}
	if err := mgr.Close(); err != nil {
		t.Fatalf("second close should be idempotent, got: %v", err)
	}
}

// TL-12: a server that announces its tool list changed has its tools listed
// again, and the manager's tools-changed handler told them.
func TestToolListChangedNotificationRefreshesServerTools(t *testing.T) {
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "dynamic", Version: "1.0.0"}, nil)
	echo := func(context.Context, *sdkmcp.CallToolRequest, map[string]any) (*sdkmcp.CallToolResult, any, error) {
		return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "ok"}}}, nil, nil
	}
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "first", Description: "first tool"}, echo)
	handler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return server }, nil)
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()

	changed := make(chan []*sdkmcp.Tool, 16)
	mgr := NewManager(WithToolsChangedHandler(func(name string, tools []*sdkmcp.Tool) {
		if name == "dynamic" {
			changed <- tools
		}
	}))
	defer mgr.Close()
	// "sse" keeps the stream open that server notifications arrive on.
	if err := mgr.ConnectServer(context.Background(), "dynamic", config.MCPServerConfig{
		Enabled: true,
		Type:    "sse",
		URL:     httpServer.URL,
	}); err != nil {
		t.Fatalf("ConnectServer() error = %v", err)
	}
	if got := len(mgr.ServerTools("dynamic")); got != 1 {
		t.Fatalf("ServerTools() has %d tools at connect, want 1", got)
	}

	// The notification stream opens after connecting; add tools until one
	// change is announced on it.
	deadline := time.After(15 * time.Second)
	for added := 1; ; added++ {
		sdkmcp.AddTool(server, &sdkmcp.Tool{Name: fmt.Sprintf("added_%d", added), Description: "added"}, echo)
		select {
		case tools := <-changed:
			if len(tools) < 2 {
				t.Fatalf("changed tool list has %d tools, want the added ones too", len(tools))
			}
			if got := len(mgr.ServerTools("dynamic")); got < 2 {
				t.Fatalf("ServerTools() has %d tools after the change, want the new list", got)
			}
			return
		case <-time.After(300 * time.Millisecond):
		case <-deadline:
			t.Fatal("the tool list change was never handled")
		}
	}
}

func newScriptedServerConnection(
	sessionID string,
	toolCallResult *sdkmcp.CallToolResult,
	toolCallErr error,
) (*ServerConnection, *scriptedTransport, error) {
	transport := &scriptedTransport{
		sessionID:      sessionID,
		toolCallResult: toolCallResult,
		toolCallErr:    toolCallErr,
	}

	client := sdkmcp.NewClient(&sdkmcp.Implementation{
		Name:    "compa-test",
		Version: "1.0.0",
	}, nil)
	session, err := client.Connect(context.Background(), transport, nil)
	if err != nil {
		return nil, nil, err
	}

	return &ServerConnection{
		Name:    "flaky",
		Config:  config.MCPServerConfig{Enabled: true, Type: "http", URL: "https://example.invalid/mcp"},
		Client:  client,
		Session: session,
		Tools: []*sdkmcp.Tool{
			{
				Name:        "echo",
				Description: "Echo test tool",
				InputSchema: map[string]any{"type": "object"},
			},
		},
	}, transport, nil
}

type scriptedTransport struct {
	sessionID      string
	toolCallResult *sdkmcp.CallToolResult
	toolCallErr    error
	// toolCallRPCErr, when set, is the server's JSON-RPC error answer to
	// tools/call.
	toolCallRPCErr *jsonrpc.Error

	mu            sync.Mutex
	toolCallCalls int
	closed        bool
	incoming      chan jsonrpc.Message
}

func (t *scriptedTransport) Connect(context.Context) (sdkmcp.Connection, error) {
	if t.incoming == nil {
		t.incoming = make(chan jsonrpc.Message, 4)
	}
	return t, nil
}

func (t *scriptedTransport) Read(ctx context.Context) (jsonrpc.Message, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case msg, ok := <-t.incoming:
		if !ok {
			return nil, io.EOF
		}
		return msg, nil
	}
}

func (t *scriptedTransport) Write(ctx context.Context, msg jsonrpc.Message) error {
	req, ok := msg.(*jsonrpc.Request)
	if !ok {
		return nil
	}

	switch req.Method {
	case "initialize":
		payload, err := json.Marshal(&sdkmcp.InitializeResult{
			ProtocolVersion: "2025-11-25",
			ServerInfo: &sdkmcp.Implementation{
				Name:    "scripted-test-server",
				Version: "1.0.0",
			},
			Capabilities: &sdkmcp.ServerCapabilities{
				Tools: &sdkmcp.ToolCapabilities{},
			},
		})
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case t.incoming <- &jsonrpc.Response{ID: req.ID, Result: payload}:
			return nil
		}

	case "notifications/initialized":
		return nil

	case "tools/call":
		t.mu.Lock()
		t.toolCallCalls++
		t.mu.Unlock()

		if t.toolCallErr != nil {
			return t.toolCallErr
		}
		if t.toolCallRPCErr != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case t.incoming <- &jsonrpc.Response{ID: req.ID, Error: t.toolCallRPCErr}:
				return nil
			}
		}

		payload, err := json.Marshal(t.toolCallResult)
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case t.incoming <- &jsonrpc.Response{ID: req.ID, Result: payload}:
			return nil
		}
	}

	return fmt.Errorf("unexpected method %q", req.Method)
}

func (t *scriptedTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true
	close(t.incoming)
	return nil
}

func (t *scriptedTransport) SessionID() string {
	return t.sessionID
}

func (t *scriptedTransport) calls() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.toolCallCalls
}
