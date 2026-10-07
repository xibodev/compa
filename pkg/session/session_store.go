package session

import (
	"bytes"
	"encoding/json"

	"github.com/xibodev/compa/v3/pkg/providers"
)

// SessionStore defines the persistence operations used by the agent loop.
// JSONLBackend is the durable implementation; the agent loop also runs
// sub-turns on an in-memory store behind the same interface.
//
// Write methods (Add*, Set*, Truncate*) are fire-and-forget: they do not
// return errors. Implementations should log failures internally.
type SessionStore interface {
	// AddMessage appends a simple role/content message to the session.
	AddMessage(sessionKey, role, content string)
	// AddFullMessage appends a complete message including tool calls.
	AddFullMessage(sessionKey string, msg providers.Message)
	// GetHistory returns the full message history for the session.
	GetHistory(key string) []providers.Message
	// GetSummary returns the conversation summary, or "" if none.
	GetSummary(key string) string
	// SetSummary replaces the conversation summary.
	SetSummary(key, summary string)
	// SetHistory replaces the full message history.
	SetHistory(key string, history []providers.Message)
	// TruncateHistory keeps only the last keepLast messages.
	TruncateHistory(key string, keepLast int)
	// Save persists any pending state to durable storage.
	Save(key string) error
	// ListSessions returns all known session keys.
	ListSessions() []string
	// Close releases resources held by the store.
	Close() error
}

// SummaryCommitter is implemented by session stores that replace the oldest
// messages of a session with a summary in one step (see CommitSummary).
type SummaryCommitter interface {
	CommitSummary(key string, summarized []providers.Message, summary string) bool
}

// CommitSummary makes summary the session's summary and drops summarized,
// which a summarizer read as the oldest messages of the session, from its
// history — by position, so messages appended while the summary was being
// written are kept. It changes nothing and reports false when summarized are
// no longer the session's oldest messages (the history was cleared, replaced
// or compacted meanwhile). A store that cannot do this in one step gets a
// check, a summary write and a truncation.
func CommitSummary(store SessionStore, key string, summarized []providers.Message, summary string) bool {
	if store == nil {
		return false
	}
	if committer, ok := store.(SummaryCommitter); ok {
		return committer.CommitSummary(key, summarized, summary)
	}
	return commitSummaryInSteps(store, key, summarized, summary)
}

// commitSummaryInSteps does what CommitSummary does for a store that cannot
// do it in one step.
func commitSummaryInSteps(store SessionStore, key string, summarized []providers.Message, summary string) bool {
	current := store.GetHistory(key)
	if len(current) < len(summarized) {
		return false
	}
	for i := range summarized {
		if !sameMessage(current[i], summarized[i]) {
			return false
		}
	}
	store.SetSummary(key, summary)
	store.TruncateHistory(key, len(current)-len(summarized))
	return true
}

// sameMessage reports whether two messages encode alike.
func sameMessage(a, b providers.Message) bool {
	encodedA, errA := json.Marshal(a)
	encodedB, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(encodedA, encodedB)
}
