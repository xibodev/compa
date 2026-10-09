package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xibodev/compa/v4/pkg/memory"
	"github.com/xibodev/compa/v4/pkg/providers"
	"github.com/xibodev/compa/v4/pkg/session"
	"github.com/xibodev/compa/v4/pkg/session/history"
)

// writeWebChatSession stores the web chat session sessionID in dir as the
// gateway does: under the scope it records for the chat "web:<id>".
func writeWebChatSession(t *testing.T, dir, sessionID string, messages ...providers.Message) {
	t.Helper()
	scope := session.SessionScope{
		Version:    session.ScopeVersionV1,
		AgentID:    "main",
		Channel:    "web",
		Account:    "default",
		Dimensions: []string{"chat"},
		Values:     map[string]string{"chat": "direct:web:" + sessionID},
	}
	data, err := json.Marshal(scope)
	if err != nil {
		t.Fatal(err)
	}
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}
	t.Cleanup(func() { store.Close() })
	key := session.BuildSessionKey(scope)
	if err := store.UpsertSessionMeta(context.Background(), key, data); err != nil {
		t.Fatalf("UpsertSessionMeta() error = %v", err)
	}
	for _, msg := range messages {
		if err := store.AddFullMessage(context.Background(), key, msg); err != nil {
			t.Fatalf("AddFullMessage() error = %v", err)
		}
	}
}

// The gateway serves the web chat's session history to a client with the
// web chat token, with the JSON the launcher's /api/sessions answers.
func TestSessionHistoryRoutes(t *testing.T) {
	ch := newTestWebChannel(t)
	if err := ch.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer ch.Stop(context.Background())

	dir := t.TempDir()
	writeWebChatSession(t, dir, "sess-1",
		providers.Message{Role: "user", Content: "hello"},
		providers.Message{Role: "assistant", Content: "hi there"},
	)

	serve := func(method, path, token string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		ch.ServeHTTP(rec, req)
		return rec
	}

	if rec := serve(http.MethodGet, "/web/sessions", "test-token"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("without a reader: status = %d, want 503", rec.Code)
	}
	ch.SetSessionHistory(func() history.Reader { return history.Reader{Dir: dir} })

	for _, token := range []string{"", "wrong-token"} {
		if rec := serve(http.MethodGet, "/web/sessions", token); rec.Code != http.StatusUnauthorized {
			t.Fatalf("token %q: status = %d, want 401", token, rec.Code)
		}
		if rec := serve(http.MethodGet, "/web/sessions/sess-1", token); rec.Code != http.StatusUnauthorized {
			t.Fatalf("token %q: detail status = %d, want 401", token, rec.Code)
		}
	}
	if rec := serve(http.MethodPost, "/web/sessions", "test-token"); rec.Code != http.StatusMethodNotAllowed ||
		rec.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("POST: status = %d, Allow = %q; want 405 and GET, HEAD", rec.Code, rec.Header().Get("Allow"))
	}

	rec := serve(http.MethodGet, "/web/sessions", "test-token")
	var list []history.Summary
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &list) != nil {
		t.Fatalf("list: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(list) != 1 || list[0].ID != "sess-1" || list[0].Title != "hello" || list[0].MessageCount != 2 {
		t.Fatalf("list = %+v, want sess-1 with its two messages", list)
	}

	rec = serve(http.MethodGet, "/web/sessions/sess-1", "test-token")
	var detail history.Detail
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &detail) != nil {
		t.Fatalf("detail: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if detail.ID != "sess-1" || len(detail.Messages) != 2 ||
		detail.Messages[0].Content != "hello" || detail.Messages[1].Content != "hi there" {
		t.Fatalf("detail = %+v, want the two messages", detail)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}

	if rec := serve(http.MethodGet, "/web/sessions/unknown", "test-token"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown session: status = %d, want 404", rec.Code)
	}
}
