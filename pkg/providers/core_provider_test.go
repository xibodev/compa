package providers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	core "github.com/xibodev/llmgw-core"

	"github.com/xibodev/compa/v3/pkg/config"
)

const openaiCompatResponse = `{"id":"chatcmpl-1","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`

const anthropicResponse = `{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`

// anthropicStreamResponse is anthropicResponse as a Messages stream.
const anthropicStreamResponse = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"m\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
	"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

func coreInstance(id, kind, adapter, endpoint string) *config.ProviderInstanceConfig {
	return &config.ProviderInstanceConfig{
		ID: id, ProviderKind: kind, Adapter: adapter, Protocol: "openai", Endpoint: endpoint,
		State: config.ProviderInstanceStateEnabled,
	}
}

func TestInstanceRuntimeTypeTable(t *testing.T) {
	cases := []struct {
		kind, adapter, want string
	}{
		{"openrouter", config.ProviderAdapterOpenAICompatible, "openai_compatible"},
		{"custom", config.ProviderAdapterOpenAICompatible, "openai_compatible"},
		{"anthropic", config.ProviderAdapterAnthropicCompatible, "anthropic"},
		{"my-claude-proxy", config.ProviderAdapterAnthropicCompatible, "anthropic"},
		{"ai_studio", config.ProviderAdapterNative, "ai_studio"},
		{"vertex_ai", config.ProviderAdapterNative, "vertex_ai"},
		{"azure_openai", config.ProviderAdapterOpenAICompatible, "azure_openai"},
		{"bedrock", config.ProviderAdapterNative, "bedrock"},
		{"ollama", config.ProviderAdapterOpenAICompatible, "ollama"},
		{"acme", config.ProviderAdapterExtension, "extension"},
	}
	for _, tc := range cases {
		got, ok := InstanceRuntimeType(coreInstance("x", tc.kind, tc.adapter, "https://example.test"))
		if !ok || got != tc.want {
			t.Errorf("runtime of %s/%s = %q, %v; want %q", tc.kind, tc.adapter, got, ok, tc.want)
		}
	}
	if _, ok := InstanceRuntimeType(coreInstance("x", "unknown", config.ProviderAdapterNative, "")); ok {
		t.Error("a native instance of an unknown kind has a runtime")
	}
}

func TestOpenAICompatibleKeylessInstanceSendsNoAuthorization(t *testing.T) {
	server, requests := recordingEndpoint(t, openaiCompatResponse)
	provider, err := CreateProviderFromInstance(coreInstance("uat", "custom", config.ProviderAdapterOpenAICompatible, server.URL+"/v1"), "m", "")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := provider.Chat(t.Context(), []Message{{Role: "user", Content: "hi"}}, nil, "m", nil)
	if err != nil || resp.Content != "ok" {
		t.Fatalf("Chat() = %v, %v", resp, err)
	}
	got := requests()[0]
	if got.path != "/v1/chat/completions" || got.header.Get("Authorization") != "" {
		t.Fatalf("path %q authorization %q", got.path, got.header.Get("Authorization"))
	}
}

func TestOpenAICompatibleStreamWithToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, frame := range []string{
			`{"choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"thinking"}}]}`,
			`{"choices":[{"index":0,"delta":{"content":"hel"}}]}`,
			`{"choices":[{"index":0,"delta":{"content":"lo"}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"pa"}}]}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\"a\"}"}}]}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", frame)
			w.(http.Flusher).Flush()
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	provider, err := CreateProviderFromInstance(coreInstance("o", "openai", config.ProviderAdapterOpenAICompatible, server.URL+"/v1"), "gpt", "key")
	if err != nil {
		t.Fatal(err)
	}
	var chunks []string
	resp, err := provider.(StreamingProvider).ChatStream(t.Context(), []Message{{Role: "user", Content: "hi"}}, nil, "gpt", nil, func(accumulated string) {
		chunks = append(chunks, accumulated)
	})
	if err != nil {
		t.Fatalf("ChatStream() error = %v", err)
	}
	if resp.Content != "hello" || resp.ReasoningContent != "thinking" || len(chunks) == 0 {
		t.Fatalf("content %q reasoning %q chunks %v", resp.Content, resp.ReasoningContent, chunks)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "read_file" || resp.ToolCalls[0].Arguments["path"] != "a" {
		t.Fatalf("tool calls = %#v", resp.ToolCalls)
	}
}

func TestUpstreamErrorStatusReachesClassification(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"slow down"}}`)
	}))
	defer server.Close()
	provider, err := CreateProviderFromInstance(coreInstance("o", "openai", config.ProviderAdapterOpenAICompatible, server.URL+"/v1"), "gpt", "key")
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Chat(t.Context(), []Message{{Role: "user", Content: "hi"}}, nil, "gpt", nil)
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("Chat() error = %v, want the upstream's 429", err)
	}
	failure := DescribeFailure(err)
	if failure.Class != core.ProviderErrorRateLimited || failure.StatusCode != http.StatusTooManyRequests ||
		failure.RetryAfter != 3*time.Second || failure.Disposition != core.DispositionRetryable {
		t.Fatalf("failure = %+v, want a retryable rate limit with Retry-After 3s", failure)
	}
}

func TestAnthropicInstanceChatsOverMessages(t *testing.T) {
	server, requests := recordingEndpoint(t, `{"id":"msg_1","type":"message","role":"assistant","model":"claude","content":[{"type":"text","text":"let me look"},{"type":"tool_use","id":"toolu_1","name":"read_file","input":{"path":"a"}}],"stop_reason":"tool_use","usage":{"input_tokens":3,"output_tokens":4}}`)
	instance := coreInstance("anthropic", "anthropic", config.ProviderAdapterAnthropicCompatible, server.URL+"/v1")
	instance.Protocol = "anthropic-messages"
	provider, err := CreateProviderFromInstance(instance, "claude", "sk-ant")
	if err != nil {
		t.Fatal(err)
	}
	tools := []ToolDefinition{{Type: "function", Function: ToolFunctionDefinition{Name: "read_file", Parameters: map[string]any{"type": "object"}}}}
	resp, err := provider.Chat(t.Context(), []Message{{Role: "system", Content: "be brief"}, {Role: "user", Content: "hi"}}, tools, "claude", map[string]any{"max_tokens": 50})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if resp.Content != "let me look" || len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "read_file" || resp.FinishReason != "tool_calls" {
		t.Fatalf("response = %#v", resp)
	}
	got := requests()[0]
	if got.path != "/v1/messages" || got.header.Get("X-Api-Key") != "sk-ant" {
		t.Fatalf("path %q x-api-key %q", got.path, got.header.Get("X-Api-Key"))
	}
	if got.body["system"] != "be brief" || got.body["max_tokens"] != float64(50) || got.body["tools"] == nil {
		t.Fatalf("messages body = %v", got.body)
	}
}

func TestAnthropicInstanceStreamsOverMessages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range []string{
			`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hel"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"read_file","input":{}}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"a\"}"}}`,
			`{"type":"content_block_stop","index":1}`,
			`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`,
			`{"type":"message_stop"}`,
		} {
			var probe struct{ Type string }
			_ = json.Unmarshal([]byte(event), &probe)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", probe.Type, event)
		}
	}))
	defer server.Close()
	provider, err := CreateProviderFromInstance(coreInstance("anthropic", "anthropic", config.ProviderAdapterAnthropicCompatible, server.URL+"/v1"), "claude", "sk-ant")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := provider.(StreamingProvider).ChatStream(t.Context(), []Message{{Role: "user", Content: "hi"}}, nil, "claude", nil, func(string) {})
	if err != nil {
		t.Fatalf("ChatStream() error = %v", err)
	}
	if resp.Content != "hello" || len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Arguments["path"] != "a" {
		t.Fatalf("response = %#v", resp)
	}
}

func TestGoogleAIStudioInstanceChats(t *testing.T) {
	const answer = `{"candidates":[{"content":{"role":"model","parts":[{"text":"hi from gemini"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"totalTokenCount":3}}`
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Header.Get("X-Goog-Api-Key") != "g-key" {
			t.Errorf("key %q", r.Header.Get("X-Goog-Api-Key"))
		}
		if strings.HasSuffix(r.URL.Path, ":streamGenerateContent") {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: "+answer+"\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, answer)
	}))
	defer server.Close()
	provider, err := CreateProviderFromInstance(coreInstance("gemini", "ai_studio", config.ProviderAdapterNative, server.URL+"/v1beta"), "gemini-2.5-flash", "g-key")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := provider.Chat(t.Context(), []Message{{Role: "user", Content: "hi"}}, nil, "gemini-2.5-flash", nil)
	if err != nil || resp.Content != "hi from gemini" {
		t.Fatalf("Chat() = %#v, %v", resp, err)
	}
	streamed, err := provider.(StreamingProvider).ChatStream(t.Context(), []Message{{Role: "user", Content: "hi"}}, nil, "gemini-2.5-flash", nil, func(string) {})
	if err != nil || streamed.Content != "hi from gemini" {
		t.Fatalf("ChatStream() = %#v, %v", streamed, err)
	}
	if len(paths) != 2 || !strings.HasSuffix(paths[0], "/models/gemini-2.5-flash:generateContent") ||
		!strings.HasSuffix(paths[1], "/models/gemini-2.5-flash:streamGenerateContent") {
		t.Fatalf("paths = %q", paths)
	}
}

func TestInstanceProxyAndHeadersReachTheUpstream(t *testing.T) {
	proxied := make(chan string, 1)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied <- r.URL.String() + " " + r.Header.Get("X-Team")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, openaiCompatResponse)
	}))
	defer proxy.Close()
	instance := coreInstance("o", "custom", config.ProviderAdapterOpenAICompatible, "http://upstream.invalid/v1")
	instance.Headers = map[string]string{"X-Team": "core"}
	instance.Runtime = &config.ProviderInstanceRuntime{Proxy: proxy.URL}
	provider, err := CreateProviderFromInstance(instance, "m", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Chat(t.Context(), []Message{{Role: "user", Content: "hi"}}, nil, "m", nil); err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if got := <-proxied; got != "http://upstream.invalid/v1/chat/completions core" {
		t.Fatalf("proxied request = %q", got)
	}
}

func TestExtensionInstanceStreamsThroughDaemon(t *testing.T) {
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		for _, chunk := range []string{"hel", "lo"} {
			fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":%q}}]}\n\n", chunk)
		}
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	instance, _ := extensionDaemonFixture(t, openaiCompatResponse)
	instance.Endpoint = server.URL
	provider, err := CreateProviderFromInstance(instance, "acme-model", "")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := provider.(StreamingProvider).ChatStream(t.Context(), []Message{{Role: "user", Content: "hi"}}, nil, "acme-model", nil, func(string) {})
	if err != nil || resp.Content != "hello" {
		t.Fatalf("ChatStream() = %#v, %v", resp, err)
	}
	if !strings.HasSuffix(path, "/acme/stream") {
		t.Fatalf("path = %q, want the daemon's stream operation", path)
	}
}

// An extension instance's core provider serves every surface the daemon
// listed for its provider, not only its protocol.
func TestExtensionInstanceServesTheDaemonListedSurfaces(t *testing.T) {
	original := ExtensionDaemonSecret
	ExtensionDaemonSecret = func() (string, error) { return "secret", nil }
	t.Cleanup(func() { ExtensionDaemonSecret = original })
	instance := coreInstance("ext-voice", "extension", config.ProviderAdapterExtension, "http://127.0.0.1:1")
	instance.Protocol = string(core.ModelSurfaceAudioSpeech)
	instance.Settings = map[string]any{
		config.ExtensionProviderSetting: "voice",
		config.ExtensionSurfacesSetting: []any{"audio_speech", "audio_transcriptions"},
	}
	provider, err := NewCoreProvider(instance)
	if err != nil {
		t.Fatalf("NewCoreProvider() error = %v", err)
	}
	got := provider.NativeSurfaces("any")
	if len(got) != 2 || got[0] != core.ModelSurfaceAudioSpeech || got[1] != core.ModelSurfaceAudioTranscriptions {
		t.Fatalf("NativeSurfaces() = %v", got)
	}
}
