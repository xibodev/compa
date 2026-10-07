package seahorse

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// Every pooled connection gets busy_timeout, synchronous and foreign_keys,
// not just the one that happened to run a PRAGMA (EV-19).
func TestEnginePragmasApplyToEveryConnection(t *testing.T) {
	eng, err := NewEngine(Config{DBPath: filepath.Join(t.TempDir(), "seahorse.db")}, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	defer eng.Close()

	ctx := context.Background()
	db := eng.store.db
	// Hold two connections at once so the second is a fresh pool member.
	c1, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}
	defer c1.Close()
	c2, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}
	defer c2.Close()

	for name, want := range map[string]int{"busy_timeout": 5000, "synchronous": 1, "foreign_keys": 1} {
		for i, conn := range []*sql.Conn{c1, c2} {
			var got int
			if err := conn.QueryRowContext(ctx, "PRAGMA "+name).Scan(&got); err != nil {
				t.Fatalf("conn %d PRAGMA %s: %v", i, name, err)
			}
			if got != want {
				t.Errorf("conn %d PRAGMA %s = %d, want %d", i, name, got, want)
			}
		}
	}

	// Foreign keys are enforced: a message needs an existing conversation.
	if _, err := eng.store.AddMessage(ctx, 424242, "user", "orphan", 1); err == nil {
		t.Error("AddMessage for a missing conversation succeeded; foreign keys are not enforced")
	}
}

// A summary placed by resequencing stays where the replaced items were,
// not after newer messages (EV-21).
func TestReplaceContextItemsKeepsSummaryPositionOnResequence(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	conv, _ := s.GetOrCreateConversation(ctx, "test:reseq-position")
	convID := conv.ConversationID

	mk := func(content string) *Summary {
		sum, err := s.CreateSummary(ctx, CreateSummaryInput{
			ConversationID: convID, Kind: SummaryKindLeaf, Content: content, TokenCount: 5,
		})
		if err != nil {
			t.Fatalf("CreateSummary: %v", err)
		}
		return sum
	}
	a, b := mk("first summary"), mk("second summary")
	m1, _ := s.AddMessage(ctx, convID, "user", "between", 2)
	m2, _ := s.AddMessage(ctx, convID, "user", "newest", 2)

	// a, message, b are not contiguous candidates and the midpoint (101)
	// is taken by the message in between, which forces a resequence.
	if err := s.UpsertContextItems(ctx, convID, []ContextItem{
		{Ordinal: 100, ItemType: "summary", SummaryID: a.SummaryID, TokenCount: 5},
		{Ordinal: 101, ItemType: "message", MessageID: m1.ID, TokenCount: 2},
		{Ordinal: 102, ItemType: "summary", SummaryID: b.SummaryID, TokenCount: 5},
		{Ordinal: 200, ItemType: "message", MessageID: m2.ID, TokenCount: 2},
	}); err != nil {
		t.Fatalf("UpsertContextItems: %v", err)
	}
	merged := mk("merged summary")
	if err := s.ReplaceContextItemsWithSummary(ctx, convID,
		[]string{a.SummaryID, b.SummaryID}, merged.SummaryID); err != nil {
		t.Fatalf("ReplaceContextItemsWithSummary: %v", err)
	}

	items, err := s.GetContextItems(ctx, convID)
	if err != nil {
		t.Fatalf("GetContextItems: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("items = %d, want 3", len(items))
	}
	last := items[len(items)-1]
	if last.ItemType != "message" || last.MessageID != m2.ID {
		t.Fatalf("newest item = %+v, want the newest message (summary must not move after it)", last)
	}
	if items[0].SummaryID != merged.SummaryID {
		t.Fatalf("first item = %+v, want the merged summary", items[0])
	}
}

// A chunk that changed while its summary was generated is not replaced (EV-21).
func TestReplaceContextChunkRejectsChangedItems(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	conv, _ := s.GetOrCreateConversation(ctx, "test:chunk-verify")
	convID := conv.ConversationID

	var ids []int64
	for _, c := range []string{"one", "two", "three"} {
		m, _ := s.AddMessage(ctx, convID, "user", c, 2)
		ids = append(ids, m.ID)
	}
	if err := s.AppendContextMessages(ctx, convID, ids); err != nil {
		t.Fatalf("AppendContextMessages: %v", err)
	}
	chunk, _ := s.GetContextItems(ctx, convID)
	chunk = chunk[:2]

	// Meanwhile another compaction replaced the first two messages.
	other, _ := s.CreateSummary(ctx, CreateSummaryInput{
		ConversationID: convID, Kind: SummaryKindLeaf, Content: "other", TokenCount: 1,
	})
	if err := s.ReplaceContextRangeWithSummary(ctx, convID, chunk[0].Ordinal, chunk[1].Ordinal, other.SummaryID); err != nil {
		t.Fatalf("ReplaceContextRangeWithSummary: %v", err)
	}

	late, _ := s.CreateSummary(ctx, CreateSummaryInput{
		ConversationID: convID, Kind: SummaryKindLeaf, Content: "late", TokenCount: 1,
	})
	err := s.ReplaceContextChunkWithSummary(ctx, convID, chunk, late.SummaryID)
	if !errors.Is(err, ErrContextChanged) {
		t.Fatalf("err = %v, want ErrContextChanged", err)
	}
	items, _ := s.GetContextItems(ctx, convID)
	if len(items) != 2 || items[0].SummaryID != other.SummaryID {
		t.Fatalf("items = %+v, want the other compaction's result untouched", items)
	}

	// The unused summary can be removed; one in the context is kept.
	if err := s.DeleteSummary(ctx, late.SummaryID); err != nil {
		t.Fatalf("DeleteSummary: %v", err)
	}
	if _, err := s.GetSummary(ctx, late.SummaryID); err == nil {
		t.Error("unused summary still exists")
	}
	if err := s.DeleteSummary(ctx, other.SummaryID); err != nil {
		t.Fatalf("DeleteSummary: %v", err)
	}
	if _, err := s.GetSummary(ctx, other.SummaryID); err != nil {
		t.Errorf("summary in context was deleted: %v", err)
	}
}

// Close waits for background compaction goroutines (EV-21).
func TestCompactionCloseWaitsForBackground(t *testing.T) {
	ce := &CompactionEngine{}
	ce.shutdownCtx, ce.shutdownCancel = context.WithCancel(context.Background())

	finished := make(chan struct{})
	if !ce.goBackground(func() {
		<-ce.shutdownCtx.Done()
		time.Sleep(50 * time.Millisecond)
		close(finished)
	}) {
		t.Fatal("goBackground refused before Close")
	}
	ce.Close()
	select {
	case <-finished:
	default:
		t.Fatal("Close returned before the background goroutine finished")
	}
	if ce.goBackground(func() {}) {
		t.Fatal("goBackground started a goroutine after Close")
	}
}
