package openai_compat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGenericProviderDoesNotStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["stream"] == true {
			t.Fatalf("generic provider unexpectedly streamed: %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	provider := newTestProvider("key", server.URL, WithProviderName("custom"))
	result, err := provider.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, "model", map[string]any{})
	if err != nil || result.Content != "ok" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}
