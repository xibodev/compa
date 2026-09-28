package session

import (
	"context"
	"encoding/json"
	"log"
	"strings"

	"github.com/xibodev/compa/pkg/memory"
	"github.com/xibodev/compa/pkg/providers"
)

// JSONLBackend adapts a memory.Store into the SessionStore interface.
// Write errors are logged rather than returned, matching the fire-and-forget
// SessionStore contract the agent loop relies on.
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
			log.Printf("session: encode session scope: %v", err)
			return
		}
		rawScope = data
	}
	if err := metaStore.UpsertSessionMeta(context.Background(), sessionKey, rawScope); err != nil {
		log.Printf("session: upsert session metadata: %v", err)
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
		log.Printf("session: get session metadata: %v", err)
		return nil
	}
	if len(meta.Scope) == 0 {
		return nil
	}
	var scope SessionScope
	if err := json.Unmarshal(meta.Scope, &scope); err != nil {
		log.Printf("session: decode session scope: %v", err)
		return nil
	}
	return CloneScope(&scope)
}

func (b *JSONLBackend) AddMessage(sessionKey, role, content string) {
	if err := b.store.AddMessage(context.Background(), sessionKey, role, content); err != nil {
		log.Printf("session: add message: %v", err)
	}
}

func (b *JSONLBackend) AddFullMessage(sessionKey string, msg providers.Message) {
	if err := b.store.AddFullMessage(context.Background(), sessionKey, msg); err != nil {
		log.Printf("session: add full message: %v", err)
	}
}

func (b *JSONLBackend) GetHistory(key string) []providers.Message {
	msgs, err := b.store.GetHistory(context.Background(), key)
	if err != nil {
		log.Printf("session: get history: %v", err)
		return []providers.Message{}
	}
	return msgs
}

func (b *JSONLBackend) GetSummary(key string) string {
	summary, err := b.store.GetSummary(context.Background(), key)
	if err != nil {
		log.Printf("session: get summary: %v", err)
		return ""
	}
	return summary
}

func (b *JSONLBackend) SetSummary(key, summary string) {
	if err := b.store.SetSummary(context.Background(), key, summary); err != nil {
		log.Printf("session: set summary: %v", err)
	}
}

func (b *JSONLBackend) SetHistory(key string, history []providers.Message) {
	if err := b.store.SetHistory(context.Background(), key, history); err != nil {
		log.Printf("session: set history: %v", err)
	}
}

func (b *JSONLBackend) TruncateHistory(key string, keepLast int) {
	if err := b.store.TruncateHistory(context.Background(), key, keepLast); err != nil {
		log.Printf("session: truncate history: %v", err)
	}
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
