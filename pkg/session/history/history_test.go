package history

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xibodev/compa/v4/pkg/memory"
	"github.com/xibodev/compa/v4/pkg/session"
)

// webChatScope returns the session key and scope the gateway records for the
// web chat session sessionID: its chat ID, "web:<id>", in lower case.
func webChatScope(t *testing.T, sessionID string) (string, json.RawMessage) {
	t.Helper()
	scope := session.SessionScope{
		Version:    session.ScopeVersionV1,
		AgentID:    "main",
		Channel:    "web",
		Account:    "default",
		Dimensions: []string{"chat"},
		Values:     map[string]string{"chat": "direct:web:" + strings.ToLower(sessionID)},
	}
	data, err := json.Marshal(scope)
	if err != nil {
		t.Fatalf("Marshal(scope) error = %v", err)
	}
	return session.BuildSessionKey(scope), data
}

// writeTestHistory writes the web chat session sessionID to dir: its history
// lines and metadata recording updated as its last update. It returns the
// history file's path.
func writeTestHistory(t *testing.T, dir, sessionID string, updated time.Time, lines ...string) string {
	t.Helper()
	key, scope := webChatScope(t, sessionID)
	return writeTestFiles(t, dir, key, scope, updated, lines...)
}

func writeTestFiles(t *testing.T, dir, key string, scope json.RawMessage, updated time.Time, lines ...string) string {
	t.Helper()
	base := filepath.Join(dir, FileBase(key))
	if err := os.WriteFile(base+".jsonl", []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(jsonl) error = %v", err)
	}
	meta, err := json.Marshal(memory.SessionMeta{
		Key:       key,
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

func TestListReadsOnlyTheHistoriesOnThePage(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	histories := make(map[string]string)
	for i := range 5 {
		id := fmt.Sprintf("page-%d", i)
		histories[id] = writeTestHistory(t, dir, id, base.Add(time.Duration(i)*time.Minute),
			fmt.Sprintf(`{"role":"user","content":"question %d"}`, i),
			`{"role":"tool","content":"ignored"}`,
			fmt.Sprintf(`{"role":"assistant","content":"answer %d"}`, i),
		)
	}

	var read []string
	sessionHistoryRead = func(path string) { read = append(read, path) }
	t.Cleanup(func() { sessionHistoryRead = nil })

	reader := Reader{Dir: dir}
	items := reader.List(1, 2)
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

	if rest := reader.List(4, 2); len(rest) != 1 || rest[0].ID != "page-0" {
		t.Fatalf("last page = %+v, want page-0 alone", rest)
	}
	if past := reader.List(5, 20); len(past) != 0 {
		t.Fatalf("page past the end = %+v, want none", past)
	}
}

func TestListGivesAnEmptyHistorysPlaceToTheNext(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	writeTestHistory(t, dir, "newest", base.Add(2*time.Minute), `{"role":"user","content":"newest"}`)
	// Metadata alone can't tell this history holds no readable message.
	writeTestHistory(t, dir, "torn", base.Add(time.Minute), `{"role":"user","cont`)
	writeTestHistory(t, dir, "oldest", base, `{"role":"user","content":"oldest"}`)

	items := Reader{Dir: dir}.List(0, 2)
	if len(items) != 2 || items[0].ID != "newest" || items[1].ID != "oldest" {
		t.Fatalf("items = %+v, want newest then oldest", items)
	}
}

func TestListSkipsFullyTruncatedHistoriesWithoutReadingThem(t *testing.T) {
	dir := t.TempDir()
	key, scope := webChatScope(t, "cleared")
	base := filepath.Join(dir, FileBase(key))
	if err := os.WriteFile(base+".jsonl", []byte(`{"role":"user","content":"gone"}`+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(jsonl) error = %v", err)
	}
	meta, err := json.Marshal(memory.SessionMeta{Key: key, Count: 1, Skip: 1, UpdatedAt: time.Now().UTC(), Scope: scope})
	if err != nil {
		t.Fatalf("Marshal(meta) error = %v", err)
	}
	if err := os.WriteFile(base+".meta.json", meta, 0o644); err != nil {
		t.Fatalf("WriteFile(meta) error = %v", err)
	}

	var read []string
	sessionHistoryRead = func(path string) { read = append(read, path) }
	t.Cleanup(func() { sessionHistoryRead = nil })

	if items := (Reader{Dir: dir}).List(0, 20); len(items) != 0 {
		t.Fatalf("items = %+v, want none", items)
	}
	if len(read) != 0 {
		t.Fatalf("histories read = %v, want none", read)
	}
}

// The gateway records a session's id in lower case: the session is found
// by the id its client uses, in any case.
func TestGetFindsASessionInAnyCase(t *testing.T) {
	dir := t.TempDir()
	writeTestHistory(t, dir, "Sess-ABC", time.Now().UTC(),
		`{"role":"user","content":"hello"}`, `{"role":"assistant","content":"hi"}`)

	for _, id := range []string{"Sess-ABC", "sess-abc"} {
		detail, err := Reader{Dir: dir}.Get(id)
		if err != nil {
			t.Fatalf("Get(%q) error = %v", id, err)
		}
		if detail.ID != id || len(detail.Messages) != 2 || detail.Messages[1].Content != "hi" {
			t.Fatalf("Get(%q) = %+v", id, detail)
		}
	}
	if _, err := (Reader{Dir: dir}).Get("other"); err != ErrNotFound {
		t.Fatalf("Get(other) error = %v, want ErrNotFound", err)
	}
}

// When two files name the same session, the one updated last holds it.
func TestGetTakesTheNewestOfTwoFilesForOneSession(t *testing.T) {
	dir := t.TempDir()
	key, scope := webChatScope(t, "twice")
	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	// Directory order is by file name: the older file's name sorts first in
	// one run and second in the other.
	for _, newerFirst := range []bool{false, true} {
		dir := filepath.Join(dir, fmt.Sprint(newerFirst))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		olderKey, newerKey := "a-"+key, "b-"+key
		if newerFirst {
			olderKey, newerKey = newerKey, olderKey
		}
		writeTestFiles(t, dir, olderKey, scope, base, `{"role":"user","content":"older"}`)
		writeTestFiles(t, dir, newerKey, scope, base.Add(time.Hour), `{"role":"user","content":"newer"}`)

		detail, err := Reader{Dir: dir}.Get("twice")
		if err != nil || len(detail.Messages) != 1 || detail.Messages[0].Content != "newer" {
			t.Fatalf("newer first = %v: Get() = %+v, %v; want the newer file's", newerFirst, detail, err)
		}
	}
}

func TestSessionsDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("COMPA_HOME", filepath.Join(home, "compa-home"))

	if got, want := SessionsDir("~/work"), filepath.Join(home, "work", "sessions"); got != want {
		t.Fatalf("SessionsDir(~/work) = %q, want %q", got, want)
	}
	if got, want := SessionsDir(""), filepath.Join(home, "compa-home", "workspace", "sessions"); got != want {
		t.Fatalf("SessionsDir(\"\") = %q, want %q", got, want)
	}
}
