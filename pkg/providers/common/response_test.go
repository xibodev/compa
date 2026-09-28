package common

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizeFinishReason(t *testing.T) {
	for _, tc := range []struct {
		reason    string
		toolCalls bool
		want      string
	}{
		{"stop", false, "stop"},
		{"", false, "stop"},
		{"length", false, "truncated"},
		{"stop", true, "tool_calls"},
		{"", true, "tool_calls"},
		{"tool_calls", true, "tool_calls"},
		{"length", true, "truncated"},
		{"content_filter", false, "content_filter"},
	} {
		if got := NormalizeFinishReason(tc.reason, tc.toolCalls); got != tc.want {
			t.Errorf("NormalizeFinishReason(%q, %v) = %q, want %q", tc.reason, tc.toolCalls, got, tc.want)
		}
	}
}

// A message's tool calls are the answer's whatever its finish reason says,
// and so are those of every choice.
func TestParseResponseHonorsToolCallsWhateverTheFinishReason(t *testing.T) {
	out, err := ParseResponse(strings.NewReader(`{"choices":[` +
		`{"index":0,"message":{"role":"assistant","content":"Looking."},"finish_reason":"stop"},` +
		`{"index":1,"message":{"role":"assistant","content":null,"tool_calls":[` +
		`{"id":"call_1","type":"function","function":{"name":"list_dir","arguments":"{\"path\":\".\"}"}},` +
		`{"type":"function","function":{"name":"read_file","arguments":{"path":"a"}}}]},"finish_reason":"stop"}]}`))
	if err != nil {
		t.Fatalf("ParseResponse() error = %v", err)
	}
	if out.Content != "Looking." || out.FinishReason != "tool_calls" || len(out.ToolCalls) != 2 {
		t.Fatalf("response = %#v", out)
	}
	if out.ToolCalls[0].ID != "call_1" || out.ToolCalls[0].Arguments["path"] != "." {
		t.Fatalf("first call = %#v", out.ToolCalls[0])
	}
	if out.ToolCalls[1].ID == "" || out.ToolCalls[1].Name != "read_file" || out.ToolCalls[1].Arguments["path"] != "a" {
		t.Fatalf("second call = %#v, want an id of its own", out.ToolCalls[1])
	}
}

func TestParseResponseReportsAnAnswerThatIsAnError(t *testing.T) {
	_, err := ParseResponse(strings.NewReader(`{"error":{"message":"quota exhausted"}}`))
	if err == nil || !strings.Contains(err.Error(), "quota exhausted") {
		t.Fatalf("ParseResponse() error = %v, want the answer's error", err)
	}
	out, err := ParseResponse(strings.NewReader(`{"choices":[],"error":null}`))
	if err != nil || out.FinishReason != "stop" {
		t.Fatalf("empty answer = %#v, %v", out, err)
	}
}

func TestContentText(t *testing.T) {
	for raw, want := range map[string]string{
		`"hello"`: "hello",
		`null`:    "",
		``:        "",
		`[{"type":"text","text":"a"},{"type":"image_url","image_url":{"url":"x"}},{"type":"text","text":"b"}]`: "ab",
		`{"unexpected":true}`: "",
	} {
		if got := ContentText(json.RawMessage(raw)); got != want {
			t.Errorf("ContentText(%s) = %q, want %q", raw, got, want)
		}
	}
}
