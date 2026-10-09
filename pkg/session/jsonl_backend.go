package session

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/xibodev/compa/v4/pkg/logger"
	"github.com/xibodev/compa/v4/pkg/memory"
	"github.com/xibodev/compa/v4/pkg/providers"
)

// JSONLBackend adapts a memory.Store into the SessionStore interface. Add*
// return the store's errors; the other writes log theirs through Compa's
// logger, as the SessionStore contract allows.
type JSONLBackend struct {
	store memory.Store
}

type metaAwareStore interface {
	GetSessionMeta(ctx context.Context, sessionKey string) (memory.SessionMeta, error)
	UpsertSessionMeta(ctx context.Context, sessionKey string, scope json.RawMessage) error
}

// MetadataAwareSessionStore exposes structured session metadata operations.
type MetadataAwareSessionStore interface {
	EnsureSessionMetadata(sessionKey string, scope *SessionScope)
	GetSessionScope(sessionKey string) *SessionScope
}

// NewJSONLBackend wraps a memory.Store for use as a SessionStore.
func NewJSONLBackend(store memory.Store) *JSONLBackend {
	return &JSONLBackend{store: store}
}

// logStoreError logs a failed store operation on a session.
func logStoreError(operation, sessionKey string, err error) {
	logger.ErrorCF("session", "Session store operation failed", map[string]any{
		"operation":   operation,
		"session_key": sessionKey,
		"error":       err.Error(),
	})
}

// EnsureSessionMetadata persists the structured scope of a session.
func (b *JSONLBackend) EnsureSessionMetadata(sessionKey string, scope *SessionScope) {
	metaStore, ok := b.store.(metaAwareStore)
	if !ok {
		return
	}
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		return
	}

	var rawScope json.RawMessage
	if scope != nil {
		data, err := json.Marshal(scope)
		if err != nil {
			logStoreError("encode session scope", sessionKey, err)
			return
		}
		rawScope = data
	}
	if err := metaStore.UpsertSessionMeta(context.Background(), sessionKey, rawScope); err != nil {
		logStoreError("upsert session metadata", sessionKey, err)
	}
}

// GetSessionScope reads the structured scope metadata of a session.
func (b *JSONLBackend) GetSessionScope(sessionKey string) *SessionScope {
	metaStore, ok := b.store.(metaAwareStore)
	if !ok {
		return nil
	}
	meta, err := metaStore.GetSessionMeta(context.Background(), sessionKey)
	if err != nil {
		logStoreError("get session metadata", sessionKey, err)
		return nil
	}
	if len(meta.Scope) == 0 {
		return nil
	}
	var scope SessionScope
	if err := json.Unmarshal(meta.Scope, &scope); err != nil {
		logStoreError("decode session scope", sessionKey, err)
		return nil
	}
	return CloneScope(&scope)
}

func (b *JSONLBackend) AddMessage(sessionKey, role, content string) error {
	return b.store.AddMessage(context.Background(), sessionKey, role, content)
}

func (b *JSONLBackend) AddFullMessage(sessionKey string, msg providers.Message) error {
	return b.store.AddFullMessage(context.Background(), sessionKey, msg)
}

func (b *JSONLBackend) GetHistory(key string) []providers.Message {
	msgs, err := b.store.GetHistory(context.Background(), key)
	if err != nil {
		logStoreError("get history", key, err)
		return []providers.Message{}
	}
	return msgs
}

func (b *JSONLBackend) GetSummary(key string) string {
	summary, err := b.store.GetSummary(context.Background(), key)
	if err != nil {
		logStoreError("get summary", key, err)
		return ""
	}
	return summary
}

func (b *JSONLBackend) SetSummary(key, summary string) {
	if err := b.store.SetSummary(context.Background(), key, summary); err != nil {
		logStoreError("set summary", key, err)
	}
}

func (b *JSONLBackend) SetHistory(key string, history []providers.Message) {
	if err := b.store.SetHistory(context.Background(), key, history); err != nil {
		logStoreError("set history", key, err)
	}
}

func (b *JSONLBackend) TruncateHistory(key string, keepLast int) {
	if err := b.store.TruncateHistory(context.Background(), key, keepLast); err != nil {
		logStoreError("truncate history", key, err)
	}
}

// CommitSummary replaces summarized, the oldest messages of the session,
// with summary (see session.CommitSummary): in one step when the underlying
// store can, otherwise by checking, then writing the summary and truncating.
func (b *JSONLBackend) CommitSummary(key string, summarized []providers.Message, summary string) bool {
	committer, ok := b.store.(memory.SummaryCommitter)
	if !ok {
		return commitSummaryInSteps(b, key, summarized, summary)
	}
	committed, err := committer.CommitSummary(context.Background(), key, summarized, summary)
	if err != nil {
		logStoreError("commit summary", key, err)
		return false
	}
	return committed
}

// Save persists session state. Since the JSONL store fsyncs every write
// immediately, the data is already durable. Save runs compaction to reclaim
// space from logically truncated messages (no-op when there are none).
func (b *JSONLBackend) Save(key string) error {
	return b.store.Compact(context.Background(), key)
}

// Close releases resources held by the underlying store.
func (b *JSONLBackend) Close() error {
	return b.store.Close()
}

// ListSessions returns all known session keys.
func (b *JSONLBackend) ListSessions() []string {
	return b.store.ListSessions()
}
