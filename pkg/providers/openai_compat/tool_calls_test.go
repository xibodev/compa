package openai_compat

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	core "github.com/xibodev/llmgw-core"
)

// toolCallUpstream is an OpenAI-compatible upstream that answers a streamed
// request with frames, each one SSE data record, and any other with body.
func toolCallUpstream(t *testing.T, frames []string, body string) *Provider {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, frame := range frames {
				_, _ = io.WriteString(w, "data: "+frame+"\n\n")
			}
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	return newTestProvider("key", server.URL)
}

func streamAnswer(t *testing.T, frames ...string) *LLMResponse {
	t.Helper()
	out, err := toolCallUpstream(t, frames, "").ChatStream(t.Context(), []Message{{Role: "user", Content: "list"}}, nil, "m", nil, nil)
	if err != nil {
		t.Fatalf("ChatStream() error = %v", err)
	}
	return out
}

func chatAnswer(t *testing.T, body string) *LLMResponse {
	t.Helper()
	out, err := toolCallUpstream(t, nil, body).Chat(t.Context(), []Message{{Role: "user", Content: "list"}}, nil, "m", nil)
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	return out
}

type wantCall struct {
	id, name string
	args     map[string]any
}

func assertToolCalls(t *testing.T, out *LLMResponse, want ...wantCall) {
	t.Helper()
	if len(out.ToolCalls) != len(want) {
		t.Fatalf("tool calls = %#v, want %d", out.ToolCalls, len(want))
	}
	for i, call := range out.ToolCalls {
		if want[i].id != "" && call.ID != want[i].id {
			t.Errorf("tool call %d id = %q, want %q", i, call.ID, want[i].id)
		}
		if call.ID == "" {
			t.Errorf("tool call %d has no id", i)
		}
		if call.Name != want[i].name {
			t.Errorf("tool call %d name = %q, want %q", i, call.Name, want[i].name)
		}
		if len(call.Arguments) != len(want[i].args) {
			t.Errorf("tool call %d arguments = %#v, want %#v", i, call.Arguments, want[i].args)
			continue
		}
		for key, value := range want[i].args {
			if call.Arguments[key] != value {
				t.Errorf("tool call %d argument %s = %#v, want %#v", i, key, call.Arguments[key], value)
			}
		}
	}
	if out.FinishReason != "tool_calls" {
		t.Errorf("FinishReason = %q, want tool_calls", out.FinishReason)
	}
}

// One chunk carries the whole call - id, name and complete arguments - and
// the finish reason, as some upstreams stream a call they did not stream.
func TestStreamToolCallCompleteInTheFinishingChunk(t *testing.T) {
	out := streamAnswer(t,
		`{"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"list_dir","arguments":"{\"path\":\".\"}"}}]},"finish_reason":"tool_calls"}]}`,
	)
	assertToolCalls(t, out, wantCall{id: "call_1", name: "list_dir", args: map[string]any{"path": "."}})
	if out.Content != "" {
		t.Errorf("Content = %q, want none", out.Content)
	}
}

func TestStreamToolCallWithStopFinishReason(t *testing.T) {
	out := streamAnswer(t,
		`{"choices":[{"index":0,"delta":{"role":"assistant","content":"Looking."}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"list_dir","arguments":"{\"path\":\"src\"}"}}]},"finish_reason":"stop"}]}`,
	)
	assertToolCalls(t, out, wantCall{id: "call_1", name: "list_dir", args: map[string]any{"path": "src"}})
	if out.Content != "Looking." {
		t.Errorf("Content = %q", out.Content)
	}
}

func TestChatToolCallWithStopFinishReason(t *testing.T) {
	out := chatAnswer(t, `{"id":"c","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"list_dir","arguments":"{\"path\":\".\"}"}}]},"finish_reason":"stop"}]}`)
	assertToolCalls(t, out, wantCall{id: "call_1", name: "list_dir", args: map[string]any{"path": "."}})
}

// The classic OpenAI stream: id and name first, arguments in fragments, the
// finish reason in a chunk of its own. A fragment that arrives with or after
// the finishing chunk still belongs to the call.
func TestStreamToolCallSplitAcrossDeltas(t *testing.T) {
	out := streamAnswer(t,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":""}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"pa"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\"a"}}]},"finish_reason":"tool_calls"}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":".txt\"}"}}]}}]}`,
	)
	assertToolCalls(t, out, wantCall{id: "call_1", name: "read_file", args: map[string]any{"path": "a.txt"}})
}

func TestStreamToolCallShapes(t *testing.T) {
	cases := map[string]struct {
		frames []string
		want   []wantCall
	}{
		"two calls without index or id in one chunk": {
			frames: []string{`{"choices":[{"delta":{"tool_calls":[{"type":"function","function":{"name":"a","arguments":"{\"x\":1}"}},{"type":"function","function":{"name":"b","arguments":"{\"y\":2}"}}]},"finish_reason":"tool_calls"}]}`},
			want:   []wantCall{{name: "a", args: map[string]any{"x": float64(1)}}, {name: "b", args: map[string]any{"y": float64(2)}}},
		},
		"index that does not start at zero": {
			frames: []string{
				`{"choices":[{"index":0,"delta":{"content":"ok"}}]}`,
				`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_b","function":{"name":"b","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
			},
			want: []wantCall{{id: "call_b", name: "b", args: map[string]any{}}},
		},
		"distinct calls reusing one index": {
			frames: []string{
				`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"a","arguments":"{\"x\":1}"}}]}}]}`,
				`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_b","function":{"name":"b","arguments":"{\"y\":2}"}}]},"finish_reason":"tool_calls"}]}`,
			},
			want: []wantCall{{id: "call_a", name: "a", args: map[string]any{"x": float64(1)}}, {id: "call_b", name: "b", args: map[string]any{"y": float64(2)}}},
		},
		"another tool reusing a complete call's index without ids": {
			frames: []string{
				`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"a","arguments":"{\"x\":1}"}}]}}]}`,
				`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"b","arguments":"{\"y\":2}"}}]},"finish_reason":"tool_calls"}]}`,
			},
			want: []wantCall{{name: "a", args: map[string]any{"x": float64(1)}}, {name: "b", args: map[string]any{"y": float64(2)}}},
		},
		"arguments as an object": {
			frames: []string{`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"a","arguments":{"x":true}}}]},"finish_reason":"tool_calls"}]}`},
			want:   []wantCall{{id: "call_1", name: "a", args: map[string]any{"x": true}}},
		},
		"complete arguments sent again with the finish reason": {
			frames: []string{
				`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"a","arguments":"{\"x\":"}}]}}]}`,
				`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]}}]}`,
				`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"a","arguments":"{\"x\":1}"}}]},"finish_reason":"tool_calls"}]}`,
			},
			want: []wantCall{{id: "call_1", name: "a", args: map[string]any{"x": float64(1)}}},
		},
		"text and tool calls in separate choices": {
			frames: []string{
				`{"choices":[{"index":0,"delta":{"content":"Let me look."}}]}`,
				`{"choices":[{"index":1,"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"a","arguments":"{}"}}]},"finish_reason":"tool_calls"},{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			},
			want: []wantCall{{id: "call_1", name: "a", args: map[string]any{}}},
		},
		"call without an id": {
			frames: []string{`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"a","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`},
			want:   []wantCall{{name: "a", args: map[string]any{}}},
		},
		"complete message in the last chunk": {
			frames: []string{`{"choices":[{"index":0,"delta":{},"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"a","arguments":"{\"x\":1}"}}]},"finish_reason":"stop"}]}`},
			want:   []wantCall{{id: "call_1", name: "a", args: map[string]any{"x": float64(1)}}},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assertToolCalls(t, streamAnswer(t, tc.frames...), tc.want...)
		})
	}
}

func TestStreamToolCallKeepsTheThoughtSignature(t *testing.T) {
	out := streamAnswer(t,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"a","arguments":"{}"},"extra_content":{"google":{"thought_signature":"sig-1"}}}]},"finish_reason":"tool_calls"}]}`,
	)
	assertToolCalls(t, out, wantCall{id: "call_1", name: "a", args: map[string]any{}})
	call := out.ToolCalls[0]
	if call.ThoughtSignature != "sig-1" || call.ExtraContent == nil || call.ExtraContent.Google == nil || call.ExtraContent.Google.ThoughtSignature != "sig-1" {
		t.Fatalf("thought signature = %q, extra = %#v", call.ThoughtSignature, call.ExtraContent)
	}
}

func TestChatToolCallsAcrossChoices(t *testing.T) {
	out := chatAnswer(t, `{"choices":[`+
		`{"index":0,"message":{"role":"assistant","content":"Let me look."},"finish_reason":"stop"},`+
		`{"index":1,"message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"a","arguments":"{\"x\":1}"}}]},"finish_reason":"tool_calls"}]}`)
	assertToolCalls(t, out, wantCall{id: "call_1", name: "a", args: map[string]any{"x": float64(1)}})
	if out.Content != "Let me look." {
		t.Fatalf("Content = %q", out.Content)
	}
}

func TestChatContentAsParts(t *testing.T) {
	out := chatAnswer(t, `{"choices":[{"index":0,"message":{"role":"assistant","content":[{"type":"text","text":"hel"},{"type":"text","text":"lo"}]},"finish_reason":"stop"}]}`)
	if out.Content != "hello" || out.FinishReason != "stop" || len(out.ToolCalls) != 0 {
		t.Fatalf("response = %#v", out)
	}
}

// A stream the upstream ends with an error chunk fails; it does not pass
// for an empty answer.
func TestStreamErrorChunkFails(t *testing.T) {
	_, err := toolCallUpstream(t, []string{`{"error":{"message":"model overloaded","code":529}}`}, "").
		ChatStream(t.Context(), []Message{{Role: "user", Content: "hi"}}, nil, "m", nil, nil)
	var providerErr *core.ProviderError
	if err == nil || !errors.As(err, &providerErr) || !strings.Contains(err.Error(), "model overloaded") {
		t.Fatalf("ChatStream() error = %v, want the upstream's error", err)
	}
}

func TestStreamFinishReasonLengthIsTruncated(t *testing.T) {
	out := streamAnswer(t, `{"choices":[{"index":0,"delta":{"content":"cut"},"finish_reason":"length"}]}`)
	if out.FinishReason != "truncated" || out.Content != "cut" {
		t.Fatalf("response = %#v", out)
	}
}

// An upstream whose plain answer lacks the tool call its stream carries, as
// an extension daemon's can: with WithStreamedChat, Chat streams and keeps
// the call. The stream is the daemon's as captured: the call's arguments end
// in the finishing chunk, each delta names the speaker, and a record follows
// [DONE].
func TestStreamedChatKeepsToolCallsAPlainAnswerDrops(t *testing.T) {
	plain := `{"choices":[{"finish_reason":"stop","index":0,"message":{"content":"I'll create that file.","role":"assistant"}}]}`
	stream := "data: " + `{"choices":[{"index":0,"delta":{"role":"assistant","content":"I'll create that file.","name":"Helper"}}]}` + "\n\n" +
		"data: " + `{"choices":[{"index":0,"delta":{"role":"assistant","name":"Helper","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"write_file","arguments":"{\"path\": \"hola.txt\", \"content\": \"hola\""}}]}}]}` + "\n\n" +
		"data: " + `{"choices":[{"index":0,"finish_reason":"tool_calls","delta":{"role":"assistant","name":"Helper","tool_calls":[{"index":0,"function":{"arguments":"}"}}]}}]}` + "\n\n" +
		"data: " + `{"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":7,"total_tokens":12}}` + "\n\n" +
		"data: [DONE]\n\n" +
		"data: " + `{"choices":[],"cost":"0"}` + "\n\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, stream)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, plain)
	}))
	t.Cleanup(server.Close)
	messages := []Message{{Role: "user", Content: "make hola.txt"}}

	out, err := newTestProvider("", server.URL).Chat(t.Context(), messages, nil, "m", nil)
	if err != nil || len(out.ToolCalls) != 0 {
		t.Fatalf("plain Chat() = %#v, %v; want the upstream's answer without its call", out, err)
	}
	out, err = newTestProvider("", server.URL, WithStreamedChat()).Chat(t.Context(), messages, nil, "m", nil)
	if err != nil {
		t.Fatalf("streamed Chat() error = %v", err)
	}
	assertToolCalls(t, out, wantCall{id: "call_1", name: "write_file", args: map[string]any{"path": "hola.txt", "content": "hola"}})
	if out.Content != "I'll create that file." || out.FinishReason != "tool_calls" {
		t.Fatalf("streamed Chat() = %#v", out)
	}
}
