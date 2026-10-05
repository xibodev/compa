package channels

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// deletingChannel records the messages deleted through it.
type deletingChannel struct {
	mockChannel
	deleted chan string
}

func (c *deletingChannel) DeleteMessage(_ context.Context, _ string, messageID string) error {
	c.deleted <- messageID
	return nil
}

func TestRecordPlaceholderDeletesTheOneItReplaces(t *testing.T) {
	m := newTestManager()
	ch := &deletingChannel{deleted: make(chan string, 4)}
	m.channels["chat_app"] = ch

	m.RecordPlaceholder("chat_app", "chat-1", "ph-1")
	m.RecordPlaceholder("chat_app", "chat-1", "ph-2")

	select {
	case id := <-ch.deleted:
		if id != "ph-1" {
			t.Fatalf("deleted %q, want the replaced placeholder ph-1", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the replaced placeholder was left behind")
	}
	entry, ok := m.placeholders.Load("chat_app:chat-1")
	if !ok || entry.(placeholderEntry).id != "ph-2" {
		t.Fatalf("tracked placeholder = %+v, want ph-2", entry)
	}
}

func TestRecordReactionUndoUndoesTheOneItReplaces(t *testing.T) {
	m := newTestManager()
	var first, second atomic.Int32
	m.RecordReactionUndo("chat_app", "chat-1", func() { first.Add(1) })
	m.RecordReactionUndo("chat_app", "chat-1", func() { second.Add(1) })
	if first.Load() != 1 {
		t.Fatal("the replaced reaction was not undone")
	}
	if second.Load() != 0 {
		t.Fatal("the current reaction was undone early")
	}
}
