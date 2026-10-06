package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xibodev/compa/v2/pkg/memory"
)

// writeWebChatTestHistory writes the web chat session sessionID straight to
// dir: its history lines and metadata recording updated as its last update.
func writeWebChatTestHistory(t *testing.T, dir, sessionID string, updated time.Time, lines ...string) string {
	t.Helper()

	sessionKey, scope := webChatTestSession(t, sessionID)
	base := filepath.Join(dir, sanitizeSessionKey(sessionKey))
	history := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(base+".jsonl", []byte(history), 0o644); err != nil {
		t.Fatalf("WriteFile(jsonl) error = %v", err)
	}
	meta, err := json.Marshal(memory.SessionMeta{
		Key:       sessionKey,
		Count:     len(lines),
		CreatedAt: updated,
		UpdatedAt: updated,
		Scope:     scope,
	})
	if err != nil {
		t.Fatalf("Marshal(meta) error = %v", err)
	}
	if err := os.WriteFile(base+".meta.json", meta, 0o644); err != nil {
		t.Fatalf("WriteFile(meta) error = %v", err)
	}
	return base + ".jsonl"
}

func listTestSessions(t *testing.T, configPath, query string) []sessionListItem {
	t.Helper()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sessions"+query, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var items []sessionListItem
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	return items
}

func TestHandleListSessions_ReadsOnlyTheHistoriesOnThePage(t *testing.T) {
	configPath := setupSessionTestEnv(t)
	dir := sessionsTestDir(t, configPath)

	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	histories := make(map[string]string)
	for i := range 5 {
		id := fmt.Sprintf("page-%d", i)
		histories[id] = writeWebChatTestHistory(t, dir, id, base.Add(time.Duration(i)*time.Minute),
			fmt.Sprintf(`{"role":"user","content":"question %d"}`, i),
			`{"role":"tool","content":"ignored"}`,
			fmt.Sprintf(`{"role":"assistant","content":"answer %d"}`, i),
		)
	}

	var read []string
	sessionHistoryRead = func(path string) { read = append(read, path) }
	t.Cleanup(func() { sessionHistoryRead = nil })

	items := listTestSessions(t, configPath, "?offset=1&limit=2")

	if len(items) != 2 || items[0].ID != "page-3" || items[1].ID != "page-2" {
		t.Fatalf("items = %+v, want page-3 then page-2", items)
	}
	for i, item := range items {
		n := 3 - i
		if want := fmt.Sprintf("question %d", n); item.Title != want || item.Preview != want {
			t.Fatalf("items[%d] title/preview = %q/%q, want %q", i, item.Title, item.Preview, want)
		}
		if item.MessageCount != 2 {
			t.Fatalf("items[%d].MessageCount = %d, want 2", i, item.MessageCount)
		}
		if want := base.Add(time.Duration(n) * time.Minute).Format(time.RFC3339); item.Updated != want {
			t.Fatalf("items[%d].Updated = %q, want %q", i, item.Updated, want)
		}
	}
	if len(read) != 2 || read[0] != histories["page-3"] || read[1] != histories["page-2"] {
		t.Fatalf("histories read = %v, want only those of page-3 and page-2", read)
	}

	if rest := listTestSessions(t, configPath, "?offset=4&limit=2"); len(rest) != 1 || rest[0].ID != "page-0" {
		t.Fatalf("last page = %+v, want page-0 alone", rest)
	}
	if past := listTestSessions(t, configPath, "?offset=5"); len(past) != 0 {
		t.Fatalf("page past the end = %+v, want none", past)
	}
}

func TestHandleListSessions_AHistoryWithNothingToShowGivesItsPlaceToTheNext(t *testing.T) {
	configPath := setupSessionTestEnv(t)
	dir := sessionsTestDir(t, configPath)

	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	writeWebChatTestHistory(t, dir, "newest", base.Add(2*time.Minute), `{"role":"user","content":"newest"}`)
	// Metadata alone can't tell this history holds no readable message.
	writeWebChatTestHistory(t, dir, "torn", base.Add(time.Minute), `{"role":"user","cont`)
	writeWebChatTestHistory(t, dir, "oldest", base, `{"role":"user","content":"oldest"}`)

	items := listTestSessions(t, configPath, "?limit=2")
	if len(items) != 2 || items[0].ID != "newest" || items[1].ID != "oldest" {
		t.Fatalf("items = %+v, want newest then oldest", items)
	}
}

func TestHandleListSessions_SkipsFullyTruncatedHistoriesWithoutReadingThem(t *testing.T) {
	configPath := setupSessionTestEnv(t)
	dir := sessionsTestDir(t, configPath)

	sessionKey, scope := webChatTestSession(t, "cleared")
	base := filepath.Join(dir, sanitizeSessionKey(sessionKey))
	if err := os.WriteFile(base+".jsonl", []byte(`{"role":"user","content":"gone"}`+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(jsonl) error = %v", err)
	}
	meta, err := json.Marshal(memory.SessionMeta{
		Key:       sessionKey,
		Count:     1,
		Skip:      1,
		UpdatedAt: time.Now().UTC(),
		Scope:     scope,
	})
	if err != nil {
		t.Fatalf("Marshal(meta) error = %v", err)
	}
	if err := os.WriteFile(base+".meta.json", meta, 0o644); err != nil {
		t.Fatalf("WriteFile(meta) error = %v", err)
	}

	var read []string
	sessionHistoryRead = func(path string) { read = append(read, path) }
	t.Cleanup(func() { sessionHistoryRead = nil })

	if items := listTestSessions(t, configPath, ""); len(items) != 0 {
		t.Fatalf("items = %+v, want none", items)
	}
	if len(read) != 0 {
		t.Fatalf("histories read = %v, want none", read)
	}
}
