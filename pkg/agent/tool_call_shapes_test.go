package agent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/xibodev/compa/pkg/bus"
	"github.com/xibodev/compa/pkg/config"
	"github.com/xibodev/compa/pkg/providers"
	"github.com/xibodev/compa/pkg/tools"
)

// countingTool counts its runs and records the arguments of the last one.
type countingTool struct {
	runs atomic.Int32
	path atomic.Value
}

func (t *countingTool) Name() string        { return "list_dir" }
func (t *countingTool) Description() string { return "Lists a directory" }
func (t *countingTool) Parameters() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{"path": map[string]any{"type": "string"}},
	}
}

func (t *countingTool) Execute(_ context.Context, args map[string]any) *tools.ToolResult {
	t.runs.Add(1)
	path, _ := args["path"].(string)
	t.path.Store(path)
	return tools.SilentResult("a.txt")
}

// A daemon provider answers a tool call in its stream, in one chunk carrying
// the whole call and the finish reason, while its answer to a request that
// does not stream lacks the call, as a real daemon's can. The tool runs
// whether the instance streams or not - the turn calls the tool, sends its
// result back, and ends with the model's next answer - because every chat
// call to a daemon streams.
func TestExtensionToolCallShapesRunTheTool(t *testing.T) {
	original := providers.ExtensionDaemonSecret
	providers.ExtensionDaemonSecret = func() (string, error) { return "daemon-secret", nil }
	t.Cleanup(func() { providers.ExtensionDaemonSecret = original })

	const (
		streamedCall = `data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"list_dir","arguments":"{\"path\":\"src\"}"}}]},"finish_reason":"tool_calls"}]}` + "\n\ndata: [DONE]\n\n"
		streamedDone = `data: {"choices":[{"index":0,"delta":{"content":"src holds a.txt"},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"
		invokedCall  = `{"id":"c","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"I'll list it."},"finish_reason":"stop"}]}`
		invokedDone  = `{"id":"c","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"src holds a.txt"},"finish_reason":"stop"}]}`
	)
	for name, streaming := range map[string]bool{"streamed": true, "not streamed": false} {
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			var operations []string
			var sawResult atomic.Bool
			daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				mu.Lock()
				operations = append(operations, r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:])
				mu.Unlock()
				answered := strings.Contains(string(body), `"tool_call_id":"call_1"`)
				if answered {
					sawResult.Store(strings.Contains(string(body), "a.txt"))
				}
				if strings.HasSuffix(r.URL.Path, "/stream") {
					w.Header().Set("Content-Type", "text/event-stream")
					if answered {
						_, _ = io.WriteString(w, streamedDone)
					} else {
						_, _ = io.WriteString(w, streamedCall)
					}
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if answered {
					_, _ = io.WriteString(w, invokedDone)
				} else {
					_, _ = io.WriteString(w, invokedCall)
				}
			}))
			defer daemon.Close()

			cfg := newConfiguredStreamingTestConfig(t, true, streaming, nil)
			instance := &config.ProviderInstanceConfig{
				ID: "main", ProviderKind: "extension", Adapter: config.ProviderAdapterExtension,
				Protocol: config.ExtensionSurfaceChatCompletions, Endpoint: daemon.URL,
				Settings: map[string]any{
					config.ExtensionProviderSetting:   "acme",
					config.ExtensionCredentialSetting: "none",
				},
				Runtime: &config.ProviderInstanceRuntime{Streaming: &streaming},
				State:   config.ProviderInstanceStateEnabled,
			}
			provider, err := providers.CreateProviderFromInstance(instance, "test-model", "")
			if err != nil {
				t.Fatalf("CreateProviderFromInstance() error = %v", err)
			}
			msgBus := bus.NewMessageBus()
			msgBus.SetStreamDelegate(configuredStreamingDelegate{streamer: &recordingStreamer{}})
			al := newConfiguredStreamingLoop(cfg, msgBus, provider)
			tool := &countingTool{}
			al.GetRegistry().GetDefaultAgent().Tools.Register(tool)

			got := runConfiguredStreamingTurn(t, al, "web")
			mu.Lock()
			defer mu.Unlock()

			if tool.runs.Load() != 1 {
				t.Fatalf("tool runs = %d, want 1 (daemon operations %v)", tool.runs.Load(), operations)
			}
			if path, _ := tool.path.Load().(string); path != "src" {
				t.Fatalf("tool arguments path = %q, want src", path)
			}
			if !sawResult.Load() {
				t.Fatalf("the tool result never reached the model (daemon operations %v)", operations)
			}
			if got != "src holds a.txt" {
				t.Fatalf("response = %q, want the answer after the tool ran", got)
			}
			for _, operation := range operations {
				if operation != "stream" {
					t.Fatalf("daemon operations = %v, want only stream", operations)
				}
			}
		})
	}
}
