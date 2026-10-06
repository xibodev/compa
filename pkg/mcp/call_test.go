package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/xibodev/compa/v2/pkg/config"
)

// swapHandler lets the test replace the whole MCP endpoint, which is what a
// server restart looks like to a client: the new process knows no sessions.
type swapHandler struct {
	mu sync.Mutex
	h  http.Handler
}

func (s *swapHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	h := s.h
	s.mu.Unlock()
	h.ServeHTTP(w, r)
}

func (s *swapHandler) set(h http.Handler) {
	s.mu.Lock()
	s.h = h
	s.mu.Unlock()
}

// One Manager.CallTool must never execute a tool more than once.
//
// The server accepts tools/call, performs the side effect, and streams its
// response with resumable SSE (an EventStore, so the stream starts with a
// priming event that carries an event ID). The server then restarts before
// the response is delivered. The client resumes the stream with
// Last-Event-ID, the new server answers 404 for the unknown session, and the
// call fails with ErrSessionMissing. CallTool used to treat that as "session
// lost", reconnect and send tools/call again.
func TestOneCallToolExecutesTheToolOnce(t *testing.T) {
	var runs atomic.Int32
	started := make(chan struct{}, 4)
	release := make(chan struct{})

	newHandler := func() http.Handler {
		server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "side-effect", Version: "1"}, nil)
		server.AddTool(&sdkmcp.Tool{
			Name:        "charge",
			Description: "stands in for any non-idempotent operation",
			InputSchema: map[string]any{"type": "object"},
		}, func(ctx context.Context, _ *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			runs.Add(1)
			started <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
			case <-time.After(10 * time.Second):
			}
			return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "charged"}}}, nil
		})
		return sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return server },
			&sdkmcp.StreamableHTTPOptions{EventStore: sdkmcp.NewMemoryEventStore(nil)})
	}

	endpoint := &swapHandler{h: newHandler()}
	ts := httptest.NewServer(endpoint)
	defer ts.Close()

	manager := NewManager()
	defer manager.Close()
	if err := manager.ConnectServer(context.Background(), "payments",
		config.MCPServerConfig{Enabled: true, Type: "http", URL: ts.URL}); err != nil {
		t.Fatalf("connect: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	type outcome struct {
		result *sdkmcp.CallToolResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := manager.CallTool(ctx, "payments", "charge", map[string]any{})
		done <- outcome{result, err}
	}()

	select {
	case <-started: // the server accepted the call and ran the side effect
	case <-ctx.Done():
		t.Fatal("tool never started")
	}
	endpoint.set(newHandler())  // server restart: sessions are gone
	ts.CloseClientConnections() // the in-flight response stream drops
	close(release)

	var got outcome
	select {
	case got = <-done:
	case <-ctx.Done():
		t.Fatal("CallTool did not return")
	}
	if n := runs.Load(); n != 1 {
		t.Fatalf("one CallTool executed the tool %d times", n)
	}
	if !errors.Is(got.err, ErrSessionLost) || !strings.Contains(got.err.Error(), "may or may not have run") {
		t.Fatalf("CallTool() error = %v, want ErrSessionLost", got.err)
	}

	// The server was reconnected: the next call reaches the new session.
	result, err := manager.CallTool(ctx, "payments", "charge", map[string]any{})
	if err != nil {
		t.Fatalf("next CallTool() error = %v", err)
	}
	if text := joinTextContent(result); text != "charged" {
		t.Fatalf("next CallTool() text = %q", text)
	}
}

// Each call carries a progress token, and the server's progress
// notifications reach the call they belong to.
func TestCallToolRoutesProgressToItsCall(t *testing.T) {
	names := []string{"first", "second"}
	// A call's progress callback closes its channel on the third
	// notification, and the tool answers only then: a notification handled
	// after its call returned would be dropped.
	received := make(map[string]chan struct{}, len(names))
	for _, name := range names {
		received[name] = make(chan struct{})
	}
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "slow", Version: "1"}, nil)
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "work", Description: "reports progress"},
		func(ctx context.Context, req *sdkmcp.CallToolRequest, in stdioTestInput) (*sdkmcp.CallToolResult, any, error) {
			token := req.Params.GetProgressToken()
			if token == nil {
				return nil, nil, errors.New("no progress token")
			}
			for i := 1; i <= 3; i++ {
				if err := req.Session.NotifyProgress(ctx, &sdkmcp.ProgressNotificationParams{
					ProgressToken: token, Progress: float64(i), Total: 3, Message: in.Text,
				}); err != nil {
					return nil, nil, err
				}
			}
			select {
			case <-received[in.Text]:
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-time.After(10 * time.Second): // the call then misses notifications
			}
			return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: in.Text}}}, nil, nil
		})
	ts := httptest.NewServer(sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return server }, nil))
	defer ts.Close()

	mgr := NewManager()
	defer mgr.Close()
	if err := mgr.ConnectServer(context.Background(), "slow",
		config.MCPServerConfig{Enabled: true, Type: "http", URL: ts.URL}); err != nil {
		t.Fatalf("ConnectServer() error = %v", err)
	}

	var wg sync.WaitGroup
	for _, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var mu sync.Mutex
			var got []*sdkmcp.ProgressNotificationParams
			ctx := WithProgress(context.Background(), func(p *sdkmcp.ProgressNotificationParams) {
				mu.Lock()
				defer mu.Unlock()
				got = append(got, p)
				if len(got) == 3 {
					close(received[name])
				}
			})
			if _, err := mgr.CallTool(ctx, "slow", "work", map[string]any{"text": name}); err != nil {
				t.Errorf("CallTool(%s) error = %v", name, err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if len(got) != 3 {
				t.Errorf("call %s got %d progress notifications, want 3", name, len(got))
			}
			for _, p := range got {
				if p.Message != name || p.Total != 3 {
					t.Errorf("call %s got progress %+v", name, p)
				}
			}
		}()
	}
	wg.Wait()
}

// The servers are told the workspaces as roots, each folder once.
func TestServersAreToldTheWorkspacesAsRoots(t *testing.T) {
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "rooted", Version: "1"}, nil)
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "roots", Description: "lists the client's roots"},
		func(ctx context.Context, req *sdkmcp.CallToolRequest, _ stdioTestInput) (*sdkmcp.CallToolResult, any, error) {
			res, err := req.Session.ListRoots(ctx, nil)
			if err != nil {
				return nil, nil, err
			}
			var uris []string
			for _, root := range res.Roots {
				uris = append(uris, root.URI)
			}
			return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: strings.Join(uris, "\n")}}}, nil, nil
		})
	ts := httptest.NewServer(sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return server }, nil))
	defer ts.Close()

	first, second := t.TempDir(), t.TempDir()
	mgr := NewManager(WithRoots(first, second, first))
	defer mgr.Close()
	if err := mgr.ConnectServer(context.Background(), "rooted",
		config.MCPServerConfig{Enabled: true, Type: "http", URL: ts.URL}); err != nil {
		t.Fatalf("ConnectServer() error = %v", err)
	}
	listed := func() []string {
		return strings.Split(callText(t, mgr, "rooted", "roots", nil), "\n")
	}
	want := []string{fileURI(first), fileURI(second)}
	if got := listed(); !slices.Equal(got, want) {
		t.Fatalf("roots/list = %v, want %v", got, want)
	}
	if !strings.HasPrefix(want[0], "file:///") {
		t.Fatalf("root URI %q is not a file URI", want[0])
	}
}
