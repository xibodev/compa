package mcp

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/xibodev/compa/pkg/config"
)

// stdioTestServerEnv makes the test binary run as a stdio MCP server, in the
// mode it names, instead of running the tests.
const stdioTestServerEnv = "COMPA_MCP_TEST_STDIO_SERVER"

func TestMain(m *testing.M) {
	switch os.Getenv(stdioTestServerEnv) {
	case "":
		os.Exit(m.Run())
	case "hang":
		// Never answers initialize; exits once its input is closed.
		fmt.Fprintln(os.Stderr, "hanging test server started")
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	default:
		runStdioTestServer()
		os.Exit(0)
	}
}

type stdioTestInput struct {
	Text string `json:"text,omitempty"`
	Name string `json:"name,omitempty"`
}

func runStdioTestServer() {
	fmt.Fprintln(os.Stderr, "stdio test server started")
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "stdio-test", Version: "1.0.0"}, nil)
	text := func(s string) *sdkmcp.CallToolResult {
		return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: s}}}
	}
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "echo", Description: "Echo the text"},
		func(_ context.Context, _ *sdkmcp.CallToolRequest, in stdioTestInput) (*sdkmcp.CallToolResult, any, error) {
			return text(in.Text), nil, nil
		})
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "env", Description: "Report an environment variable"},
		func(_ context.Context, _ *sdkmcp.CallToolRequest, in stdioTestInput) (*sdkmcp.CallToolResult, any, error) {
			value, ok := os.LookupEnv(in.Name)
			if !ok {
				return text("<unset>"), nil, nil
			}
			return text(value), nil, nil
		})
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "cwd", Description: "Report the working folder"},
		func(_ context.Context, _ *sdkmcp.CallToolRequest, _ stdioTestInput) (*sdkmcp.CallToolResult, any, error) {
			dir, err := os.Getwd()
			if err != nil {
				return nil, nil, err
			}
			return text(dir), nil, nil
		})
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "exit", Description: "Exit without answering"},
		func(_ context.Context, _ *sdkmcp.CallToolRequest, _ stdioTestInput) (*sdkmcp.CallToolResult, any, error) {
			fmt.Fprintln(os.Stderr, "test server exiting on request")
			os.Exit(3)
			return nil, nil, nil
		})
	_ = server.Run(context.Background(), &sdkmcp.StdioTransport{})
}

func stdioTestServerConfig(t *testing.T, mode string) config.MCPServerConfig {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	return config.MCPServerConfig{
		Enabled: true,
		Type:    "stdio",
		Command: exe,
		Env:     map[string]string{stdioTestServerEnv: mode},
	}
}

func callText(t *testing.T, mgr *Manager, server, tool string, args map[string]any) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := mgr.CallTool(ctx, server, tool, args)
	if err != nil {
		t.Fatalf("CallTool(%s) error = %v", tool, err)
	}
	return joinTextContent(result)
}

func joinTextContent(result *sdkmcp.CallToolResult) string {
	if result == nil {
		return ""
	}
	var parts []string
	for _, content := range result.Content {
		if text, ok := content.(*sdkmcp.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// TestStdioServerOutlivesConnectContext covers a config reload, which
// connects with a context that ends when the reload returns: the server
// must keep running.
func TestStdioServerOutlivesConnectContext(t *testing.T) {
	mgr := NewManager()
	t.Cleanup(func() { _ = mgr.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	if err := mgr.ConnectServer(ctx, "local", stdioTestServerConfig(t, "serve")); err != nil {
		cancel()
		t.Fatalf("ConnectServer() error = %v", err)
	}
	cancel()
	// Give a process tied to the context the time to be killed.
	time.Sleep(300 * time.Millisecond)

	if got := callText(t, mgr, "local", "echo", map[string]any{"text": "still here"}); got != "still here" {
		t.Fatalf("echo = %q, want %q", got, "still here")
	}
}

// A stdio server gets Compa's environment, then the env file's variables,
// then the configured ones, each overriding the one before.
func TestStdioServerEnvironment(t *testing.T) {
	t.Setenv("COMPA_MCP_TEST_INHERITED", "from-compa")
	t.Setenv("COMPA_MCP_TEST_FILE", "from-compa")
	t.Setenv("COMPA_MCP_TEST_CONFIG", "from-compa")
	envFile := filepath.Join(t.TempDir(), "server.env")
	if err := os.WriteFile(envFile, []byte("COMPA_MCP_TEST_FILE=from-file\nCOMPA_MCP_TEST_CONFIG=from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := stdioTestServerConfig(t, "serve")
	cfg.EnvFile = envFile
	cfg.Env["COMPA_MCP_TEST_CONFIG"] = "from-config"

	mgr := NewManager()
	t.Cleanup(func() { _ = mgr.Close() })
	if err := mgr.ConnectServer(context.Background(), "local", cfg); err != nil {
		t.Fatalf("ConnectServer() error = %v", err)
	}
	for name, want := range map[string]string{
		"COMPA_MCP_TEST_INHERITED": "from-compa",
		"COMPA_MCP_TEST_FILE":      "from-file",
		"COMPA_MCP_TEST_CONFIG":    "from-config",
	} {
		if got := callText(t, mgr, "local", "env", map[string]any{"name": name}); got != want {
			t.Errorf("server saw %s = %q, want %q", name, got, want)
		}
	}
}

// A stdio server starts in its cwd, a relative one taken from the
// workspace, and without one in the workspace.
func TestStdioServerWorkingFolder(t *testing.T) {
	workspace := t.TempDir()
	own := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		cwd  string
		want string
	}{
		"workspace":    {want: workspace},
		"absolute cwd": {cwd: own, want: own},
		"relative cwd": {cwd: "sub", want: filepath.Join(workspace, "sub")},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := stdioTestServerConfig(t, "serve")
			cfg.Cwd = tc.cwd
			mgr := NewManager()
			t.Cleanup(func() { _ = mgr.Close() })
			err := mgr.LoadFromMCPConfig(context.Background(), config.MCPConfig{
				ToolConfig: config.ToolConfig{Enabled: true},
				Servers:    map[string]config.MCPServerConfig{"local": cfg},
			}, workspace)
			if err != nil {
				t.Fatalf("LoadFromMCPConfig() error = %v", err)
			}
			got := callText(t, mgr, "local", "cwd", nil)
			if !sameFolder(t, got, tc.want) {
				t.Fatalf("server started in %q, want %q", got, tc.want)
			}
		})
	}
}

func sameFolder(t *testing.T, a, b string) bool {
	t.Helper()
	infoA, errA := os.Stat(a)
	infoB, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(infoA, infoB)
}

func TestStdioServerReconnectsAfterExit(t *testing.T) {
	mgr := NewManager()
	t.Cleanup(func() { _ = mgr.Close() })
	if err := mgr.ConnectServer(context.Background(), "local", stdioTestServerConfig(t, "serve")); err != nil {
		t.Fatalf("ConnectServer() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := mgr.CallTool(ctx, "local", "exit", nil)
	if err == nil {
		t.Fatal("expected the call whose server exited to fail")
	}
	if !strings.Contains(err.Error(), "test server exiting on request") {
		t.Fatalf("error does not quote the server's stderr: %v", err)
	}

	// The call is not repeated, but the next one starts the server again.
	if got := callText(t, mgr, "local", "echo", map[string]any{"text": "back"}); got != "back" {
		t.Fatalf("echo after restart = %q, want %q", got, "back")
	}
}

func TestStdioServerConnectTimeout(t *testing.T) {
	mgr := NewManager(WithConnectTimeout(500 * time.Millisecond))
	t.Cleanup(func() { _ = mgr.Close() })

	start := time.Now()
	err := mgr.ConnectServer(context.Background(), "hung", stdioTestServerConfig(t, "hang"))
	if err == nil {
		t.Fatal("expected a server that never answers initialize to fail to connect")
	}
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Fatalf("ConnectServer took %v", elapsed)
	}
	if !strings.Contains(err.Error(), "did not finish connecting within") {
		t.Fatalf("error does not name the timeout: %v", err)
	}
	if !strings.Contains(err.Error(), "hanging test server started") {
		t.Fatalf("error does not quote the server's stderr: %v", err)
	}
	if _, ok := mgr.GetServer("hung"); ok {
		t.Fatal("a server that failed to connect was registered")
	}
}

func TestReconnectBackoff(t *testing.T) {
	cases := map[int]time.Duration{
		0:  0,
		1:  time.Second,
		2:  2 * time.Second,
		3:  4 * time.Second,
		7:  time.Minute,
		50: time.Minute,
	}
	for failures, want := range cases {
		if got := reconnectBackoff(failures); got != want {
			t.Errorf("reconnectBackoff(%d) = %v, want %v", failures, got, want)
		}
	}
}

func TestStderrTailKeepsTheEnd(t *testing.T) {
	tail := newStderrTail("s")
	for i := range 2000 {
		fmt.Fprintf(tail, "line %d\n", i)
	}
	got := tail.String()
	if len(got) > maxStderrTailBytes || !strings.HasSuffix(got, "line 1999") {
		t.Fatalf("tail = %d bytes ending %q", len(got), got[max(0, len(got)-20):])
	}
	if suffix := tail.suffix(); len([]rune(suffix)) > maxStderrSuffixChars+len("; server stderr: ...") {
		t.Fatalf("suffix too long: %d", len(suffix))
	}
	var empty *stderrTail
	if empty.suffix() != "" {
		t.Fatal("a nil tail has a suffix")
	}
}
