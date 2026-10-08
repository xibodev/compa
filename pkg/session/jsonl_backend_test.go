package session_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/xibodev/compa/v3/pkg/memory"
	"github.com/xibodev/compa/v3/pkg/providers"
	"github.com/xibodev/compa/v3/pkg/session"
)

// Compile-time interface satisfaction checks.
var (
	_ session.SessionStore              = (*session.JSONLBackend)(nil)
	_ session.MetadataAwareSessionStore = (*session.JSONLBackend)(nil)
)

func newBackend(t *testing.T) *session.JSONLBackend {
	t.Helper()
	store, err := memory.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return session.NewJSONLBackend(store)
}

// failingAppendStore is a memory.Store whose appends fail.
type failingAppendStore struct {
	memory.Store
	err error
}

func (s failingAppendStore) AddMessage(context.Context, string, string, string) error { return s.err }

func (s failingAppendStore) AddFullMessage(context.Context, string, providers.Message) error {
	return s.err
}

// The backend returns a failed append's error, so the turn can report it.
func TestJSONLBackend_AddReturnsTheStoreError(t *testing.T) {
	want := errors.New("no space left on device")
	b := session.NewJSONLBackend(failingAppendStore{err: want})
	if err := b.AddMessage("s1", "user", "hello"); !errors.Is(err, want) {
		t.Errorf("AddMessage() error = %v, want %v", err, want)
	}
	if err := b.AddFullMessage("s1", providers.Message{Role: "assistant", Content: "hi"}); !errors.Is(err, want) {
		t.Errorf("AddFullMessage() error = %v, want %v", err, want)
	}
}

func TestJSONLBackend_AddAndGetHistory(t *testing.T) {
	b := newBackend(t)

	b.AddMessage("s1", "user", "hello")
	b.AddMessage("s1", "assistant", "hi")

	history := b.GetHistory("s1")
	if len(history) != 2 {
		t.Fatalf("got %d messages, want 2", len(history))
	}
	if history[0].Role != "user" || history[0].Content != "hello" {
		t.Errorf("msg[0] = %+v", history[0])
	}
	if history[1].Role != "assistant" || history[1].Content != "hi" {
		t.Errorf("msg[1] = %+v", history[1])
	}
}

func TestJSONLBackend_AddFullMessage(t *testing.T) {
	b := newBackend(t)

	msg := providers.Message{
		Role:    "assistant",
		Content: "done",
		ToolCalls: []providers.ToolCall{
			{ID: "tc1", Function: &providers.FunctionCall{Name: "read_file", Arguments: `{"path":"x"}`}},
		},
	}
	b.AddFullMessage("s1", msg)

	history := b.GetHistory("s1")
	if len(history) != 1 {
		t.Fatalf("got %d, want 1", len(history))
	}
	if len(history[0].ToolCalls) != 1 || history[0].ToolCalls[0].ID != "tc1" {
		t.Errorf("tool calls = %+v", history[0].ToolCalls)
	}
}

func TestJSONLBackend_AddFullMessage_PreservesModelName(t *testing.T) {
	b := newBackend(t)

	msg := providers.Message{
		Role:      "assistant",
		Content:   "done",
		ModelName: "gpt-5.4-mini",
	}
	b.AddFullMessage("s1", msg)

	history := b.GetHistory("s1")
	if len(history) != 1 {
		t.Fatalf("got %d, want 1", len(history))
	}
	if history[0].ModelName != "gpt-5.4-mini" {
		t.Fatalf("ModelName = %q, want %q", history[0].ModelName, "gpt-5.4-mini")
	}
}

func TestJSONLBackend_Summary(t *testing.T) {
	b := newBackend(t)

	if got := b.GetSummary("s1"); got != "" {
		t.Errorf("got %q, want empty", got)
	}

	b.SetSummary("s1", "test summary")
	if got := b.GetSummary("s1"); got != "test summary" {
		t.Errorf("got %q, want %q", got, "test summary")
	}
}

func TestJSONLBackend_TruncateAndSave(t *testing.T) {
	b := newBackend(t)

	for i := 0; i < 10; i++ {
		b.AddMessage("s1", "user", fmt.Sprintf("msg %d", i))
	}
	b.TruncateHistory("s1", 3)

	history := b.GetHistory("s1")
	if len(history) != 3 {
		t.Fatalf("got %d, want 3", len(history))
	}
	if history[0].Content != "msg 7" {
		t.Errorf("got %q, want %q", history[0].Content, "msg 7")
	}

	// Save triggers compaction.
	if err := b.Save("s1"); err != nil {
		t.Fatal(err)
	}

	// Messages still accessible after compaction.
	history = b.GetHistory("s1")
	if len(history) != 3 {
		t.Fatalf("after save: got %d, want 3", len(history))
	}
}

func TestJSONLBackend_SetHistory(t *testing.T) {
	b := newBackend(t)
	b.AddMessage("s1", "user", "old")

	b.SetHistory("s1", []providers.Message{
		{Role: "user", Content: "new1"},
		{Role: "assistant", Content: "new2"},
	})

	history := b.GetHistory("s1")
	if len(history) != 2 {
		t.Fatalf("got %d, want 2", len(history))
	}
	if history[0].Content != "new1" {
		t.Errorf("got %q, want %q", history[0].Content, "new1")
	}
}

func TestJSONLBackend_EmptySession(t *testing.T) {
	b := newBackend(t)

	history := b.GetHistory("nonexistent")
	if history == nil {
		t.Fatal("got nil, want empty slice")
	}
	if len(history) != 0 {
		t.Errorf("got %d, want 0", len(history))
	}
}

func TestJSONLBackend_SessionIsolation(t *testing.T) {
	b := newBackend(t)
	b.AddMessage("s1", "user", "session1")
	b.AddMessage("s2", "user", "session2")

	h1 := b.GetHistory("s1")
	h2 := b.GetHistory("s2")

	if len(h1) != 1 || h1[0].Content != "session1" {
		t.Errorf("s1: %+v", h1)
	}
	if len(h2) != 1 || h2[0].Content != "session2" {
		t.Errorf("s2: %+v", h2)
	}
}

func TestJSONLBackend_SummarizeFlow(t *testing.T) {
	// Simulates the real summarization flow in the agent loop:
	// SetSummary → TruncateHistory → Save
	b := newBackend(t)

	for i := 0; i < 20; i++ {
		b.AddMessage("s1", "user", fmt.Sprintf("msg %d", i))
	}

	b.SetSummary("s1", "conversation about testing")
	b.TruncateHistory("s1", 4)
	if err := b.Save("s1"); err != nil {
		t.Fatal(err)
	}

	if got := b.GetSummary("s1"); got != "conversation about testing" {
		t.Errorf("summary = %q", got)
	}
	history := b.GetHistory("s1")
	if len(history) != 4 {
		t.Fatalf("got %d messages, want 4", len(history))
	}
	if history[0].Content != "msg 16" {
		t.Errorf("first message = %q, want %q", history[0].Content, "msg 16")
	}
}

func TestJSONLBackend_PersistsSessionScope(t *testing.T) {
	b := newBackend(t)

	scope := &session.SessionScope{
		Version:    session.ScopeVersionV1,
		AgentID:    "main",
		Channel:    "telegram",
		Account:    "default",
		Dimensions: []string{"chat"},
		Values: map[string]string{
			"chat": "group:c1",
		},
	}
	key := session.BuildSessionKey(*scope)
	b.EnsureSessionMetadata(key, scope)
	b.AddMessage(key, "user", "hello")

	resolvedScope := b.GetSessionScope(key)
	if resolvedScope == nil {
		t.Fatal("GetSessionScope() returned nil")
	}
	if resolvedScope.AgentID != scope.AgentID || resolvedScope.Values["chat"] != scope.Values["chat"] {
		t.Fatalf("GetSessionScope() = %+v, want %+v", resolvedScope, scope)
	}
	if history := b.GetHistory(key); len(history) != 1 || history[0].Content != "hello" {
		t.Fatalf("history = %+v, want the single persisted message", history)
	}
}

func TestJSONLBackend_GetSessionScopeWithoutMetadata(t *testing.T) {
	b := newBackend(t)
	b.AddMessage("s1", "user", "hello")

	if scope := b.GetSessionScope("s1"); scope != nil {
		t.Fatalf("GetSessionScope() = %+v, want nil for a session without scope", scope)
	}
}

func TestCommitSummaryDropsOnlyTheSummarizedPrefix(t *testing.T) {
	stores := map[string]session.SessionStore{
		"jsonl backend":  newBackend(t),
		"store in steps": &stepStore{SessionStore: newBackend(t)},
	}
	for name, store := range stores {
		t.Run(name, func(t *testing.T) {
			for _, content := range []string{"q1", "a1", "q2", "a2"} {
				store.AddMessage("s", "user", content)
			}
			summarized := store.GetHistory("s")[:2]
			store.AddMessage("s", "user", "appended while summarizing")

			if !session.CommitSummary(store, "s", summarized, "q1 and a1") {
				t.Fatal("CommitSummary refused an unchanged prefix")
			}
			history := store.GetHistory("s")
			if len(history) != 3 || history[0].Content != "q2" || history[2].Content != "appended while summarizing" {
				t.Fatalf("history = %+v", history)
			}
			if got := store.GetSummary("s"); got != "q1 and a1" {
				t.Fatalf("summary = %q", got)
			}

			store.SetHistory("s", []providers.Message{{Role: "user", Content: "cleared"}})
			if session.CommitSummary(store, "s", history[:1], "stale") {
				t.Fatal("CommitSummary accepted a prefix that is gone")
			}
		})
	}
}

// stepStore hides the backend's one-step commit, so CommitSummary takes the
// generic path.
type stepStore struct {
	session.SessionStore
}
