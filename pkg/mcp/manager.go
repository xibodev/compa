package mcp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/xibodev/compa/v2/pkg/config"
	runtimeevents "github.com/xibodev/compa/v2/pkg/events"
	"github.com/xibodev/compa/v2/pkg/logger"
)

// ErrSessionLost is the error of a tool call during which the server lost
// its session. The call is not sent again: the tool may already have run.
var ErrSessionLost = errors.New("the server lost its session during this call; the tool may or may not have run")

// headerTransport is an http.RoundTripper that adds custom headers to requests
type headerTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

func expandHomeCommandPath(command string) string {
	if command == "" || command[0] != '~' {
		return command
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return command
	}
	if command == "~" {
		return home
	}
	if strings.HasPrefix(command, "~/") || strings.HasPrefix(command, "~\\") {
		return filepath.Join(home, command[2:])
	}
	return command
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone the request to avoid modifying the original
	req = req.Clone(req.Context())

	// Add custom headers
	for key, value := range t.headers {
		req.Header.Set(key, value)
	}

	// Use the base transport
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

// loadEnvFile loads environment variables from a file in .env format
// Each line should be in the format: KEY=value
// Lines starting with # are comments
// Empty lines are ignored
func loadEnvFile(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open env file: %w", err)
	}
	defer file.Close()

	envVars := make(map[string]string)
	scanner := bufio.NewScanner(file)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Parse KEY=value
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid format at line %d: %s", lineNum, line)
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		if key == "" {
			return nil, fmt.Errorf("invalid format at line %d: empty key", lineNum)
		}

		// Remove surrounding quotes if present
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') ||
				(value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}

		envVars[key] = value
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading env file: %w", err)
	}

	return envVars, nil
}

// ServerConnection represents a connection to an MCP server
type ServerConnection struct {
	Name        string
	Config      config.MCPServerConfig
	Client      *mcp.Client
	Session     *mcp.ClientSession
	Tools       []*mcp.Tool
	reconnectMu sync.Mutex

	// stderr keeps the end of a stdio server's error output; nil for other
	// transports.
	stderr *stderrTail
	// lost is set once the session has ended: the server exited or the
	// connection broke. The next call starts the server again.
	lost atomic.Bool
	// closing is set before Compa itself closes the session, so its end is
	// not reported as a failure.
	closing atomic.Bool
	// reconnectFailures and nextReconnect space out restarts of a server
	// that keeps failing. Guarded by reconnectMu.
	reconnectFailures int
	nextReconnect     time.Time
}

// stderrSuffix returns the end of the server's error output, for an error
// message, or "".
func (c *ServerConnection) stderrSuffix() string {
	if c == nil {
		return ""
	}
	return c.stderr.suffix()
}

const (
	// DefaultConnectTimeout bounds starting a server, the initialize
	// handshake and listing its tools, so a server that never answers cannot
	// hold up its caller.
	DefaultConnectTimeout = 30 * time.Second
	// DefaultCallTimeout bounds one tool call to a server, unless the
	// manager (WithCallTimeout) or the server (call_timeout_seconds) sets
	// another bound.
	DefaultCallTimeout = 5 * time.Minute
	// defaultCloseTimeout bounds each step of Close: waiting for in-flight
	// calls, then closing the sessions.
	defaultCloseTimeout = 10 * time.Second
	// minReconnectBackoff and maxReconnectBackoff bound the wait between
	// restarts of a server that keeps failing.
	minReconnectBackoff = time.Second
	maxReconnectBackoff = time.Minute
)

// Manager manages multiple MCP server connections
type Manager struct {
	servers       map[string]*ServerConnection
	runtimeEvents runtimeevents.Bus
	mu            sync.RWMutex
	closed        atomic.Bool    // changed from bool to atomic.Bool to avoid TOCTOU race
	wg            sync.WaitGroup // tracks in-flight CallTool calls

	connectTimeout time.Duration
	callTimeout    time.Duration
	closeTimeout   time.Duration

	// toolsChanged is told a server's tools when they may have changed: the
	// server announced that its tool list changed, or was reconnected. nil
	// when nobody listens.
	toolsChanged ToolsChangedFunc

	// roots are the roots every client advertises. They are set once: a
	// reload replaces the manager, so its servers start with the new ones.
	roots []*mcp.Root

	// progress maps the progress token of each call in flight to the
	// function its progress notifications go to.
	progressMu     sync.Mutex
	progress       map[string]ProgressFunc
	progressTokens atomic.Uint64
}

// ProgressFunc receives the progress notifications of one tool call.
type ProgressFunc func(*mcp.ProgressNotificationParams)

type progressKey struct{}

// WithProgress returns a context whose tool calls send the server's progress
// notifications to fn.
func WithProgress(ctx context.Context, fn ProgressFunc) context.Context {
	return context.WithValue(ctx, progressKey{}, fn)
}

func progressFuncFrom(ctx context.Context) ProgressFunc {
	fn, _ := ctx.Value(progressKey{}).(ProgressFunc)
	return fn
}

// WithRoots has the servers told dirs as the client's roots: the folders
// they may work in.
func WithRoots(dirs ...string) ManagerOption {
	return func(m *Manager) {
		m.roots = folderRoots(dirs)
	}
}

// ToolsChangedFunc receives the tools of a server listed again: after it
// announced its tool list changed, or after it was reconnected.
type ToolsChangedFunc func(server string, tools []*mcp.Tool)

// WithToolsChangedHandler has handler told the tools of a server that
// announces its tool list changed (notifications/tools/list_changed), and of
// a server reconnected, which may list other tools than before.
func WithToolsChangedHandler(handler ToolsChangedFunc) ManagerOption {
	return func(m *Manager) {
		m.toolsChanged = handler
	}
}

var connectServerFunc = connectServer

// ManagerOption configures an MCP manager.
type ManagerOption func(*Manager)

// WithRuntimeEvents injects the runtime event bus used for MCP observations.
func WithRuntimeEvents(eventBus runtimeevents.Bus) ManagerOption {
	return func(m *Manager) {
		m.runtimeEvents = eventBus
	}
}

// WithConnectTimeout bounds connecting to one server: starting it, the
// initialize handshake and listing its tools. 0 or less keeps
// DefaultConnectTimeout.
func WithConnectTimeout(timeout time.Duration) ManagerOption {
	return func(m *Manager) {
		if timeout > 0 {
			m.connectTimeout = timeout
		}
	}
}

// WithCallTimeout bounds one tool call to a server without a
// call_timeout_seconds of its own. 0 or less keeps DefaultCallTimeout.
func WithCallTimeout(timeout time.Duration) ManagerOption {
	return func(m *Manager) {
		if timeout > 0 {
			m.callTimeout = timeout
		}
	}
}

// ServerEventPayload describes MCP server connection events.
type ServerEventPayload struct {
	Server    string `json:"server"`
	Type      string `json:"type,omitempty"`
	URL       string `json:"url,omitempty"`
	Command   string `json:"command,omitempty"`
	Tool      string `json:"tool,omitempty"`
	ToolCount int    `json:"tool_count,omitempty"`
	Error     string `json:"error,omitempty"`
}

// NewManager creates a new MCP manager
func NewManager(opts ...ManagerOption) *Manager {
	m := &Manager{
		servers:        make(map[string]*ServerConnection),
		connectTimeout: DefaultConnectTimeout,
		callTimeout:    DefaultCallTimeout,
		closeTimeout:   defaultCloseTimeout,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(m)
		}
	}
	return m
}

// LoadFromConfig loads MCP servers from configuration
func (m *Manager) LoadFromConfig(ctx context.Context, cfg *config.Config) error {
	return m.LoadFromMCPConfig(ctx, cfg.Tools.MCP, cfg.WorkspacePath())
}

// LoadFromMCPConfig loads MCP servers from MCP configuration and workspace path.
// This is the minimal dependency version that doesn't require the full Config object.
// Relative env_file and cwd paths are taken from workspacePath, and a stdio
// server without a cwd starts in it.
func (m *Manager) LoadFromMCPConfig(
	ctx context.Context,
	mcpCfg config.MCPConfig,
	workspacePath string,
) error {
	if !mcpCfg.Enabled {
		logger.InfoCF("mcp", "MCP integration is disabled", nil)
		return nil
	}

	if len(mcpCfg.Servers) == 0 {
		logger.InfoCF("mcp", "No MCP servers configured", nil)
		return nil
	}

	logger.InfoCF("mcp", "Initializing MCP servers",
		map[string]any{
			"count": len(mcpCfg.Servers),
		})

	var wg sync.WaitGroup
	errs := make(chan error, len(mcpCfg.Servers))
	enabledCount := 0

	for name, serverCfg := range mcpCfg.Servers {
		if !serverCfg.Enabled {
			logger.DebugCF("mcp", "Skipping disabled server",
				map[string]any{
					"server": name,
				})
			continue
		}

		enabledCount++
		wg.Add(1)
		go func(name string, serverCfg config.MCPServerConfig, workspace string) {
			defer wg.Done()

			// Resolve relative envFile paths relative to workspace
			if serverCfg.EnvFile != "" && !filepath.IsAbs(serverCfg.EnvFile) {
				if workspace == "" {
					err := fmt.Errorf(
						"workspace path is empty while resolving relative envFile %q for server %s",
						serverCfg.EnvFile,
						name,
					)
					logger.ErrorCF("mcp", "Invalid MCP server configuration",
						map[string]any{
							"server":   name,
							"env_file": serverCfg.EnvFile,
							"error":    err.Error(),
						})
					errs <- err
					return
				}
				serverCfg.EnvFile = filepath.Join(workspace, serverCfg.EnvFile)
			}
			serverCfg.Cwd = stdioWorkDir(serverCfg.Cwd, workspace)

			if err := m.ConnectServer(ctx, name, serverCfg); err != nil {
				logger.ErrorCF("mcp", "Failed to connect to MCP server",
					map[string]any{
						"server": name,
						"error":  err.Error(),
					})
				errs <- fmt.Errorf("failed to connect to server %s: %w", name, err)
			}
		}(name, serverCfg, workspacePath)
	}

	wg.Wait()
	close(errs)

	// Collect errors
	var allErrors []error
	for err := range errs {
		allErrors = append(allErrors, err)
	}

	connectedCount := len(m.GetServers())

	// If all enabled servers failed to connect, return aggregated error
	if enabledCount > 0 && connectedCount == 0 {
		logger.ErrorCF("mcp", "All MCP servers failed to connect",
			map[string]any{
				"failed": len(allErrors),
				"total":  enabledCount,
			})
		return errors.Join(allErrors...)
	}

	if len(allErrors) > 0 {
		logger.WarnCF("mcp", "Some MCP servers failed to connect",
			map[string]any{
				"failed":    len(allErrors),
				"connected": connectedCount,
				"total":     enabledCount,
			})
		// Don't fail completely if some servers successfully connected
	}

	logger.InfoCF("mcp", "MCP server initialization complete",
		map[string]any{
			"connected": connectedCount,
			"total":     enabledCount,
		})

	return nil
}

// ConnectServer connects to a single MCP server. ctx bounds connecting
// only: the server, once connected, runs until Close, whatever happens to
// ctx -- callers such as a config reload pass contexts that end as soon as
// they return.
func (m *Manager) ConnectServer(
	ctx context.Context,
	name string,
	cfg config.MCPServerConfig,
) error {
	m.publishServerEvent(runtimeevents.KindMCPServerConnecting, name, cfg, 0, nil)
	conn, err := m.connect(ctx, name, cfg)
	if err != nil {
		m.publishServerEvent(runtimeevents.KindMCPServerFailed, name, cfg, 0, err)
		return err
	}

	m.mu.Lock()
	if m.closed.Load() {
		m.mu.Unlock()
		closeConnection(conn)
		m.publishServerEvent(runtimeevents.KindMCPServerFailed, name, cfg, 0, fmt.Errorf("manager is closed"))
		return fmt.Errorf("manager is closed")
	}
	previous := m.servers[name]
	m.servers[name] = conn
	tools := conn.Tools
	m.mu.Unlock()

	if previous != nil {
		closeConnection(previous)
	}
	for _, tool := range tools {
		toolName := ""
		if tool != nil {
			toolName = tool.Name
		}
		m.publishToolDiscovered(name, cfg, toolName)
	}
	m.publishServerEvent(runtimeevents.KindMCPServerConnected, name, cfg, len(tools), nil)
	return nil
}

// refreshServerTools lists again the tools of a server whose session
// announced its tool list changed, and tells the tools-changed handler. A
// session that is not (or no longer) the server's current one is ignored: a
// connection still being set up lists its tools itself.
func (m *Manager) refreshServerTools(name string, session *mcp.ClientSession) {
	if session == nil || m.closed.Load() {
		return
	}
	m.mu.RLock()
	conn, ok := m.servers[name]
	m.mu.RUnlock()
	if !ok || conn.Session != session {
		return
	}

	timeout := m.connectTimeout
	if timeout <= 0 {
		timeout = DefaultConnectTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	tools, err := listServerTools(ctx, name, session, session.InitializeResult())
	if err != nil {
		logger.WarnCF("mcp", "Failed to list the changed tools of an MCP server",
			map[string]any{"server": name, "error": err.Error()})
		return
	}

	m.mu.Lock()
	if m.closed.Load() || m.servers[name] != conn {
		m.mu.Unlock()
		return
	}
	conn.Tools = tools
	handler := m.toolsChanged
	m.mu.Unlock()

	logger.InfoCF("mcp", "MCP server tool list changed",
		map[string]any{"server": name, "tool_count": len(tools)})
	if handler != nil {
		handler(name, tools)
	}
}

// ServerTools returns the tools a connected server lists.
func (m *Manager) ServerTools(name string) []*mcp.Tool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	conn, ok := m.servers[name]
	if !ok {
		return nil
	}
	return append([]*mcp.Tool(nil), conn.Tools...)
}

// connect connects to a server within the connect timeout.
func (m *Manager) connect(
	ctx context.Context,
	name string,
	cfg config.MCPServerConfig,
) (*ServerConnection, error) {
	timeout := m.connectTimeout
	if timeout <= 0 {
		timeout = DefaultConnectTimeout
	}
	connectCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	opts := connectOptions{
		// A server that announces its tool list changed has it listed
		// again; listing waits for an answer the session reads, so not in
		// the handler.
		onToolListChanged: func(session *mcp.ClientSession) {
			go m.refreshServerTools(name, session)
		},
		onProgress: m.routeProgress,
		roots:      m.roots,
	}
	conn, err := connectServerFunc(connectCtx, name, cfg, opts)
	if err != nil && errors.Is(connectCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		return nil, fmt.Errorf("server %s did not finish connecting within %v: %w", name, timeout, err)
	}
	return conn, err
}

// connectOptions is what connecting a server takes from its manager.
type connectOptions struct {
	onToolListChanged func(session *mcp.ClientSession)
	onProgress        func(params *mcp.ProgressNotificationParams)
	// roots are the roots the client advertises from the start.
	roots []*mcp.Root
}

// closeConnection closes a connection Compa no longer uses.
func closeConnection(conn *ServerConnection) error {
	if conn == nil || conn.Session == nil {
		return nil
	}
	conn.closing.Store(true)
	return conn.Session.Close()
}

func connectServer(
	ctx context.Context,
	name string,
	cfg config.MCPServerConfig,
	opts connectOptions,
) (*ServerConnection, error) {
	logger.InfoCF("mcp", "Connecting to MCP server",
		map[string]any{
			"server":     name,
			"command":    cfg.Command,
			"args_count": len(cfg.Args),
		})

	// Create client
	client := mcp.NewClient(&mcp.Implementation{
		Name:    "compa",
		Version: "1.0.0",
	}, &mcp.ClientOptions{
		ToolListChangedHandler: func(_ context.Context, req *mcp.ToolListChangedRequest) {
			if req != nil && opts.onToolListChanged != nil {
				opts.onToolListChanged(req.Session)
			}
		},
		ProgressNotificationHandler: func(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
			if req != nil && req.Params != nil && opts.onProgress != nil {
				opts.onProgress(req.Params)
			}
		},
	})
	// Before connecting, so the server finds them at once; with no session
	// yet, no change is announced.
	client.AddRoots(opts.roots...)

	// Create transport based on configuration
	// Auto-detect transport type if not explicitly specified
	transportType := config.EffectiveMCPTransportType(cfg)
	if transportType == "" {
		return nil, fmt.Errorf("either URL or command must be provided")
	}

	var (
		session *mcp.ClientSession
		stderr  *stderrTail
		err     error
	)
	switch transportType {
	case "sse", "http":
		if cfg.URL == "" {
			return nil, fmt.Errorf("URL is required for SSE/HTTP transport")
		}
		session, err = connectHTTPServer(ctx, client, name, cfg, transportType)
		if err != nil {
			return nil, err
		}
	case "stdio":
		if cfg.Command == "" {
			return nil, fmt.Errorf("command is required for stdio transport")
		}
		logger.DebugCF("mcp", "Using stdio transport",
			map[string]any{
				"server":  name,
				"command": cfg.Command,
			})
		// The server's lifetime is not tied to ctx, which only bounds
		// connecting: closing the session stops the server.
		cmd := exec.Command(expandHomeCommandPath(cfg.Command), cfg.Args...)
		cmd.Dir = config.ExpandHome(cfg.Cwd)
		env, envErr := stdioServerEnv(name, cfg, cmd.Environ())
		if envErr != nil {
			return nil, envErr
		}
		cmd.Env = env
		stderr = newStderrTail(name)
		cmd.Stderr = stderr
		// A process the server started that keeps the pipes open must not
		// keep stopping the server waiting.
		cmd.WaitDelay = stdioWaitDelay

		transport := &isolatedCommandTransport{Command: cmd}
		session, err = client.Connect(ctx, transport, nil)
		if err != nil {
			// The SDK does not close every connection it gives up on (an
			// unsupported protocol version, for one): stop the server here.
			transport.abort()
			return nil, fmt.Errorf("failed to connect: %w%s", err, stderr.suffix())
		}
	default:
		return nil, fmt.Errorf(
			"unsupported transport type: %s (supported: stdio, sse, http, streamable-http)",
			transportType,
		)
	}

	// Get server info
	initResult := session.InitializeResult()
	logger.InfoCF("mcp", "Connected to MCP server",
		map[string]any{
			"server":        name,
			"serverName":    initResult.ServerInfo.Name,
			"serverVersion": initResult.ServerInfo.Version,
			"protocol":      initResult.ProtocolVersion,
		})

	// List available tools if supported
	tools, err := listServerTools(ctx, name, session, initResult)
	if err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("%w%s", err, stderr.suffix())
	}

	conn := &ServerConnection{
		Name:    name,
		Config:  cfg,
		Client:  client,
		Session: session,
		Tools:   tools,
		stderr:  stderr,
	}
	go watchSession(conn)
	return conn, nil
}

// watchSession marks a connection lost when its session ends, and reports
// an end Compa did not ask for.
func watchSession(conn *ServerConnection) {
	err := conn.Session.Wait()
	conn.lost.Store(true)
	if conn.closing.Load() {
		return
	}
	fields := map[string]any{"server": conn.Name}
	if err != nil {
		fields["error"] = err.Error()
	}
	if tail := conn.stderr.String(); tail != "" {
		fields["stderr"] = tail
	}
	logger.WarnCF("mcp", "MCP server connection ended; the next tool call reconnects", fields)
}

// connectHTTPServer connects over Streamable HTTP. A server configured as
// "sse" that does not speak it is tried with the SSE transport of the
// 2024-11-05 specification, as the specification advises clients to.
func connectHTTPServer(
	ctx context.Context,
	client *mcp.Client,
	name string,
	cfg config.MCPServerConfig,
	transportType string,
) (*mcp.ClientSession, error) {
	// Configure DisableStandaloneSSE based on transport type.
	// - "http": Streamable HTTP request-response mode. Disable the standalone
	//   SSE stream to avoid compatibility issues with servers that don't
	//   support the optional GET listener.
	// - "sse": Bidirectional mode. Enable the standalone SSE stream to receive
	//   server-initiated notifications (e.g., ToolListChangedNotification).
	// - Empty or auto-detected: Defaults to "sse" behavior (standalone SSE enabled).
	disableStandaloneSSE := transportType == "http"

	logger.DebugCF("mcp", "Using SSE/HTTP transport",
		map[string]any{
			"server":               name,
			"disableStandaloneSSE": disableStandaloneSSE,
		})

	httpClient := http.DefaultClient
	// Add custom headers if provided
	if len(cfg.Headers) > 0 {
		// Create a custom HTTP client with header-injecting transport
		httpClient = &http.Client{
			Transport: &headerTransport{
				base:    http.DefaultTransport,
				headers: cfg.Headers,
			},
		}
		logger.DebugCF("mcp", "Added custom HTTP headers",
			map[string]any{
				"server":       name,
				"header_count": len(cfg.Headers),
			})
	}

	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             cfg.URL,
		DisableStandaloneSSE: disableStandaloneSSE,
		HTTPClient:           httpClient,
	}, nil)
	if err == nil {
		return session, nil
	}
	if transportType != "sse" || ctx.Err() != nil {
		return nil, fmt.Errorf("failed to connect: %w", err)
	}

	logger.DebugCF("mcp", "Streamable HTTP failed; trying the SSE transport",
		map[string]any{"server": name, "error": err.Error()})
	session, sseErr := client.Connect(ctx, &legacySSETransport{
		inner: &mcp.SSEClientTransport{Endpoint: cfg.URL, HTTPClient: httpClient},
	}, nil)
	if sseErr != nil {
		return nil, fmt.Errorf("failed to connect: %w (SSE transport: %v)", err, sseErr)
	}
	return session, nil
}

// legacySSETransport connects with the SSE transport of the 2024-11-05
// specification. That transport reads its event stream with the context it
// connects with; this keeps the stream open after connecting, while
// connecting stays bounded by the caller's context.
type legacySSETransport struct {
	inner *mcp.SSEClientTransport
}

func (t *legacySSETransport) Connect(ctx context.Context) (mcp.Connection, error) {
	streamCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(ctx, cancel)
	conn, err := t.inner.Connect(streamCtx)
	if !stop() {
		// ctx ended while connecting, which cancelled the stream.
		if err == nil {
			_ = conn.Close()
			err = ctx.Err()
		}
	}
	if err != nil {
		cancel()
		return nil, err
	}
	return &cancelOnCloseConnection{Connection: conn, cancel: cancel}, nil
}

// cancelOnCloseConnection ends the event stream's context once the
// connection is closed.
type cancelOnCloseConnection struct {
	mcp.Connection
	cancel context.CancelFunc
}

func (c *cancelOnCloseConnection) Close() error {
	defer c.cancel()
	return c.Connection.Close()
}

// stdioServerEnv returns the environment of a stdio server: environ, the
// command's own, then the env file, then the configured variables. A later
// value of a variable wins, as it does in exec.Cmd.Env.
func stdioServerEnv(name string, cfg config.MCPServerConfig, environ []string) ([]string, error) {
	env := environ
	if cfg.EnvFile != "" {
		envVars, err := loadEnvFile(cfg.EnvFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load env file %s: %w", cfg.EnvFile, err)
		}
		env = appendEnv(env, envVars)
		logger.DebugCF("mcp", "Loaded environment variables from file",
			map[string]any{
				"server":    name,
				"envFile":   cfg.EnvFile,
				"var_count": len(envVars),
			})
	}
	return appendEnv(env, cfg.Env), nil
}

func appendEnv(env []string, vars map[string]string) []string {
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, k+"="+vars[k])
	}
	return env
}

// stdioWorkDir returns the folder a stdio server starts in: its cwd, a
// relative one taken from the workspace, or else the workspace itself when it
// exists.
func stdioWorkDir(cwd, workspace string) string {
	if cwd = config.ExpandHome(strings.TrimSpace(cwd)); cwd != "" {
		if !filepath.IsAbs(cwd) && workspace != "" {
			cwd = filepath.Join(workspace, cwd)
		}
		return cwd
	}
	if info, err := os.Stat(workspace); err == nil && info.IsDir() {
		return workspace
	}
	return ""
}

// folderRoots returns the roots for dirs: one file:// URI per folder.
func folderRoots(dirs []string) []*mcp.Root {
	var roots []*mcp.Root
	seen := make(map[string]bool)
	for _, dir := range dirs {
		dir = config.ExpandHome(strings.TrimSpace(dir))
		if dir == "" {
			continue
		}
		if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}
		uri := fileURI(dir)
		if seen[uri] {
			continue
		}
		seen[uri] = true
		roots = append(roots, &mcp.Root{URI: uri, Name: filepath.Base(dir)})
	}
	return roots
}

// fileURI returns the file:// URI of an absolute path.
func fileURI(path string) string {
	path = filepath.ToSlash(path)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path // a Windows drive: file:///C:/...
	}
	return (&url.URL{Scheme: "file", Path: path}).String()
}

// GetServers returns all connected servers
func (m *Manager) GetServers() map[string]*ServerConnection {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string]*ServerConnection, len(m.servers))
	for k, v := range m.servers {
		result[k] = v
	}
	return result
}

// GetServer returns a specific server connection
func (m *Manager) GetServer(name string) (*ServerConnection, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	conn, ok := m.servers[name]
	return conn, ok
}

// CallTool calls a tool on a specific server. Every call carries a progress
// token; the server's progress notifications go to the ProgressFunc of ctx
// (WithProgress).
//
// A call during which the server lost its session is not sent again, since
// the tool may have run: CallTool reconnects for the calls that follow and
// returns ErrSessionLost. Only a tool that a trusted server marks read-only
// or idempotent is called once more, on the new session.
func (m *Manager) CallTool(
	ctx context.Context,
	serverName, toolName string,
	arguments map[string]any,
) (*mcp.CallToolResult, error) {
	// Check if closed before acquiring lock (fast path)
	if m.closed.Load() {
		return nil, fmt.Errorf("manager is closed")
	}

	m.mu.RLock()
	// Double-check after acquiring lock to prevent TOCTOU race
	if m.closed.Load() {
		m.mu.RUnlock()
		return nil, fmt.Errorf("manager is closed")
	}
	conn, ok := m.servers[serverName]
	if ok {
		m.wg.Add(1) // Add to WaitGroup while holding the lock
	}
	m.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("server %s not found", serverName)
	}
	defer m.wg.Done()

	timeout := m.callTimeout
	if seconds := conn.Config.CallTimeoutSeconds; seconds > 0 {
		timeout = time.Duration(seconds) * time.Second
	}
	if timeout <= 0 {
		timeout = DefaultCallTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// A server whose session ended -- it exited, or its connection broke --
	// is started again before the call rather than failing every call.
	if conn.lost.Load() {
		logger.InfoCF("mcp", "MCP server session has ended; reconnecting",
			map[string]any{"server": serverName})
		freshConn, err := m.reconnectServer(callCtx, serverName, conn)
		if err != nil {
			return nil, fmt.Errorf("server %s is not connected and reconnecting failed: %w", serverName, err)
		}
		conn = freshConn
	}

	params := &mcp.CallToolParams{
		Name:      toolName,
		Arguments: arguments,
	}
	token := m.trackProgress(progressFuncFrom(ctx))
	defer m.untrackProgress(token)
	params.SetProgressToken(token)

	result, err := conn.Session.CallTool(callCtx, params)
	if err != nil && sessionLost(err) {
		retry := m.retriesAfterLostSession(conn, toolName)
		conn.lost.Store(true)
		freshConn, reconnectErr := m.reconnectServer(callCtx, serverName, conn)
		fields := map[string]any{"server": serverName, "tool": toolName, "error": err.Error()}
		if reconnectErr != nil {
			fields["reconnect_error"] = reconnectErr.Error()
		}
		if reconnectErr != nil || !retry {
			logger.WarnCF("mcp", "MCP server lost its session during a tool call; the call is not repeated", fields)
			return nil, ErrSessionLost
		}
		logger.InfoCF("mcp", "MCP server lost its session during a read-only or idempotent tool call; calling it again", fields)
		conn = freshConn
		result, err = conn.Session.CallTool(callCtx, params)
		if err != nil && sessionLost(err) {
			conn.lost.Store(true)
			return nil, ErrSessionLost
		}
	}
	if err != nil {
		if connectionBroken(err) {
			// The call may have run in part, so it is not repeated; the next
			// call reconnects. Once the session has ended, the server's
			// error output is complete and may say what happened.
			conn.lost.Store(true)
			awaitSessionEnd(conn, sessionEndGrace)
			return nil, fmt.Errorf("failed to call tool: %w%s", err, conn.stderrSuffix())
		}
		if errors.Is(callCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			return nil, fmt.Errorf("failed to call tool: no answer from server %s within %v", serverName, timeout)
		}
		return nil, fmt.Errorf("failed to call tool: %w", err)
	}

	return result, nil
}

// retriesAfterLostSession reports whether a call of toolName that the
// session lost may be sent again: the server is trusted and declares the
// tool read-only or idempotent. Annotations of other servers are not acted
// on.
func (m *Manager) retriesAfterLostSession(conn *ServerConnection, toolName string) bool {
	if !conn.Config.Trusted {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, tool := range conn.Tools {
		if tool != nil && tool.Name == toolName && tool.Annotations != nil {
			return tool.Annotations.ReadOnlyHint || tool.Annotations.IdempotentHint
		}
	}
	return false
}

// trackProgress returns a new progress token and routes its notifications
// to fn.
func (m *Manager) trackProgress(fn ProgressFunc) string {
	token := fmt.Sprintf("compa-%d", m.progressTokens.Add(1))
	if fn == nil {
		return token
	}
	m.progressMu.Lock()
	defer m.progressMu.Unlock()
	if m.progress == nil {
		m.progress = make(map[string]ProgressFunc)
	}
	m.progress[token] = fn
	return token
}

func (m *Manager) untrackProgress(token string) {
	m.progressMu.Lock()
	defer m.progressMu.Unlock()
	delete(m.progress, token)
}

// routeProgress hands a progress notification to the call it belongs to.
// Notifications of calls that ended, or with a token Compa did not send, are
// dropped.
func (m *Manager) routeProgress(params *mcp.ProgressNotificationParams) {
	token, ok := params.ProgressToken.(string)
	if !ok {
		return
	}
	m.progressMu.Lock()
	fn := m.progress[token]
	m.progressMu.Unlock()
	if fn != nil {
		fn(params)
	}
}

func listServerTools(
	ctx context.Context,
	name string,
	session *mcp.ClientSession,
	initResult *mcp.InitializeResult,
) ([]*mcp.Tool, error) {
	var tools []*mcp.Tool
	if initResult.Capabilities.Tools == nil {
		return tools, nil
	}

	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			logger.WarnCF("mcp", "Error listing tool",
				map[string]any{
					"server": name,
					"error":  err.Error(),
				})
			continue
		}
		tools = append(tools, tool)
	}

	logger.InfoCF("mcp", "Listed tools from MCP server",
		map[string]any{
			"server":    name,
			"toolCount": len(tools),
		})

	return tools, nil
}

// sessionLost reports whether a call failed because the server no longer
// knows the session, as after a restart. The SDK keeps only the text of
// ErrSessionMissing when the connection closes over it (ErrConnectionClosed
// wraps the cause with %v), so the text counts there and nowhere else: a
// tool's own JSON-RPC error may say "session not found" too.
func sessionLost(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, mcp.ErrSessionMissing) {
		return true
	}
	return errors.Is(err, mcp.ErrConnectionClosed) &&
		strings.Contains(strings.ToLower(err.Error()), mcp.ErrSessionMissing.Error())
}

// connectionBroken reports whether a call failed because the connection to
// the server ended, as when a stdio server exits.
func connectionBroken(err error) bool {
	return errors.Is(err, mcp.ErrConnectionClosed) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF)
}

// sessionEndGrace bounds the wait for a broken session to end.
const sessionEndGrace = 3 * time.Second

// awaitSessionEnd waits, at most timeout, for a connection's session to end.
func awaitSessionEnd(conn *ServerConnection, timeout time.Duration) {
	if conn == nil || conn.Session == nil {
		return
	}
	ended := make(chan struct{})
	go func() {
		_ = conn.Session.Wait()
		close(ended)
	}()
	select {
	case <-ended:
	case <-time.After(timeout):
	}
}

func (m *Manager) reconnectServer(
	ctx context.Context,
	serverName string,
	staleConn *ServerConnection,
) (*ServerConnection, error) {
	if staleConn == nil {
		return nil, fmt.Errorf("server %s not found", serverName)
	}

	staleConn.reconnectMu.Lock()
	defer staleConn.reconnectMu.Unlock()

	if m.closed.Load() {
		return nil, fmt.Errorf("manager is closed")
	}

	m.mu.RLock()
	currentConn, ok := m.servers[serverName]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("server %s not found", serverName)
	}
	if currentConn != staleConn {
		return currentConn, nil
	}

	// A server that keeps failing is not started on every call.
	if wait := time.Until(staleConn.nextReconnect); wait > 0 {
		return nil, fmt.Errorf("server %s failed to reconnect; next attempt in %v",
			serverName, wait.Round(time.Second))
	}

	freshConn, err := m.connect(ctx, serverName, staleConn.Config)
	if err != nil {
		// A call that was cancelled says nothing about the server.
		if ctx.Err() == nil || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			staleConn.reconnectFailures++
			staleConn.nextReconnect = time.Now().Add(reconnectBackoff(staleConn.reconnectFailures))
		}
		return nil, err
	}

	m.mu.Lock()
	if m.closed.Load() {
		m.mu.Unlock()
		_ = closeConnection(freshConn)
		return nil, fmt.Errorf("manager is closed")
	}

	currentConn, ok = m.servers[serverName]
	if !ok {
		m.mu.Unlock()
		_ = closeConnection(freshConn)
		return nil, fmt.Errorf("server %s not found", serverName)
	}

	if currentConn == staleConn {
		m.servers[serverName] = freshConn
		staleToClose := staleConn
		handler, tools := m.toolsChanged, freshConn.Tools
		m.mu.Unlock()
		_ = closeConnection(staleToClose)
		// The server started again may list other tools. The handler
		// registers them; not on this call's goroutine, whose tool runs.
		if handler != nil {
			go func() {
				if !m.closed.Load() {
					handler(serverName, tools)
				}
			}()
		}
		return freshConn, nil
	}

	m.mu.Unlock()
	_ = closeConnection(freshConn)
	return currentConn, nil
}

// reconnectBackoff is the wait after the given number of failed reconnects
// in a row: one second, doubling up to a minute.
func reconnectBackoff(failures int) time.Duration {
	if failures <= 0 {
		return 0
	}
	backoff := minReconnectBackoff
	for i := 1; i < failures && backoff < maxReconnectBackoff; i++ {
		backoff *= 2
	}
	return min(backoff, maxReconnectBackoff)
}

// Close closes all server connections. It waits a bounded time for calls in
// flight and for the servers to stop: a hung server cannot keep Compa from
// shutting down or reloading.
func (m *Manager) Close() error {
	// Use Swap to atomically set closed=true and get the previous value
	// This prevents TOCTOU race with CallTool's closed check
	if m.closed.Swap(true) {
		return nil // already closed
	}

	timeout := m.closeTimeout
	if timeout <= 0 {
		timeout = defaultCloseTimeout
	}

	// Wait for in-flight CallTool calls to finish before closing sessions.
	// After closed=true is set, no new CallTool can start (they check closed
	// first). Calls still running after the timeout end with their session.
	callsDone := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(callsDone)
	}()
	select {
	case <-callsDone:
	case <-time.After(timeout):
		logger.WarnCF("mcp", "MCP tool calls still running at close; closing their servers",
			map[string]any{"waited": timeout.String()})
	}

	m.mu.Lock()
	servers := m.servers
	m.servers = make(map[string]*ServerConnection)
	m.mu.Unlock()

	logger.InfoCF("mcp", "Closing all MCP server connections",
		map[string]any{
			"count": len(servers),
		})

	type closeResult struct {
		name string
		err  error
	}
	results := make(chan closeResult, len(servers))
	for name, conn := range servers {
		go func() {
			results <- closeResult{name: name, err: closeConnection(conn)}
		}()
	}

	var errs []error
	deadline := time.After(timeout)
collect:
	for remaining := len(servers); remaining > 0; remaining-- {
		select {
		case res := <-results:
			if res.err != nil {
				logger.ErrorCF("mcp", "Failed to close server connection",
					map[string]any{
						"server": res.name,
						"error":  res.err.Error(),
					})
				errs = append(errs, fmt.Errorf("server %s: %w", res.name, res.err))
			}
		case <-deadline:
			logger.ErrorCF("mcp", "MCP servers did not close in time",
				map[string]any{"remaining": remaining, "waited": timeout.String()})
			errs = append(errs, fmt.Errorf("%d server(s) did not close within %v", remaining, timeout))
			break collect
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("failed to close %d server(s): %w", len(errs), errors.Join(errs...))
	}

	return nil
}

// GetAllTools returns all tools from all connected servers
func (m *Manager) GetAllTools() map[string][]*mcp.Tool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string][]*mcp.Tool)
	for name, conn := range m.servers {
		if len(conn.Tools) > 0 {
			result[name] = conn.Tools
		}
	}
	return result
}
