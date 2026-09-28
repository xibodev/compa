package memory

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xibodev/compa/pkg/fileutil"
	"github.com/xibodev/compa/pkg/providers"
	"github.com/xibodev/compa/pkg/providers/messageutil"
)

const (
	// numLockShards is the fixed number of mutexes used to serialize
	// per-session access. Using a sharded array instead of a map keeps
	// memory bounded regardless of how many sessions are created over
	// the lifetime of the process — important for a long-running daemon.
	numLockShards = 64

	// maxLineSize is the maximum size of a single JSON line in a .jsonl
	// file. Tool results (read_file, web search, etc.) can be large, so
	// we set a generous limit. The scanner starts at 64 KB and grows
	// only as needed up to this cap.
	maxLineSize = 10 * 1024 * 1024 // 10 MB
)

// SessionMeta holds per-session metadata stored in a .meta.json file.
//
// Scope is stored as raw JSON so pkg/memory can stay decoupled from the
// higher-level session package while still preserving structured scope data.
type SessionMeta struct {
	Key       string          `json:"key"`
	Summary   string          `json:"summary"`
	Skip      int             `json:"skip"`
	Count     int             `json:"count"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	Scope     json.RawMessage `json:"scope,omitempty"`
}

// JSONLStore implements Store using append-only JSONL files.
//
// Each session is stored as two files:
//
//	{sanitized_key}.jsonl      — one JSON-encoded message per line, append-only
//	{sanitized_key}.meta.json  — session metadata (summary, logical truncation offset)
//
// Messages are never physically deleted from the JSONL file. Instead,
// TruncateHistory records a "skip" offset in the metadata file and
// GetHistory ignores lines before that offset. This keeps all writes
// append-only, which is both fast and crash-safe.
type JSONLStore struct {
	dir   string
	locks [numLockShards]sync.Mutex
}

// NewJSONLStore creates a new JSONL-backed store rooted at dir, creating the
// directory when needed. If the directory cannot be created the error is
// returned together with a store that is still safe to use: it reports no
// sessions and its writes fail until the directory becomes available.
func NewJSONLStore(dir string) (*JSONLStore, error) {
	store := &JSONLStore{dir: dir}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return store, fmt.Errorf("memory: create directory: %w", err)
	}
	return store, nil
}

// sessionLock returns a mutex for the given session key.
// Keys are mapped to a fixed pool of shards via FNV hash, so
// memory usage is O(1) regardless of total session count.
func (s *JSONLStore) sessionLock(key string) *sync.Mutex {
	h := fnv.New32a()
	h.Write([]byte(key))
	return &s.locks[h.Sum32()%numLockShards]
}

func (s *JSONLStore) jsonlPath(key string) string {
	return filepath.Join(s.dir, sanitizeKey(key)+".jsonl")
}

func (s *JSONLStore) metaPath(key string) string {
	return filepath.Join(s.dir, sanitizeKey(key)+".meta.json")
}

// sanitizeKey converts a session key to a safe filename component.
// The session API in web/backend/api mirrors this mapping to locate session
// files on disk. Replaces ':' with '_' (session key separator) and '/' and '\' with '_'
// so composite IDs (e.g. Telegram forum "chatID/threadID", Slack "channel/thread_ts")
// do not create subdirectories or break on Windows.
func sanitizeKey(key string) string {
	s := strings.ReplaceAll(key, ":", "_")
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "\\", "_")
	return s
}

// readMeta loads the metadata file for a session.
// Returns a zero-value sessionMeta if the file does not exist.
func (s *JSONLStore) readMeta(key string) (SessionMeta, error) {
	data, err := os.ReadFile(s.metaPath(key))
	if os.IsNotExist(err) {
		return SessionMeta{Key: key}, nil
	}
	if err != nil {
		return SessionMeta{}, fmt.Errorf("memory: read meta: %w", err)
	}
	var meta SessionMeta
	err = json.Unmarshal(data, &meta)
	if err != nil {
		return SessionMeta{}, fmt.Errorf("memory: decode meta: %w", err)
	}
	if meta.Key == "" {
		meta.Key = key
	}
	return meta, nil
}

// writeMeta atomically writes the metadata file using the project's
// standard WriteFileAtomic (temp + fsync + rename).
func (s *JSONLStore) writeMeta(key string, meta SessionMeta) error {
	if strings.TrimSpace(meta.Key) == "" {
		meta.Key = key
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("memory: encode meta: %w", err)
	}
	return fileutil.WriteFileAtomic(s.metaPath(key), data, 0o644)
}

func cloneRawJSON(data json.RawMessage) json.RawMessage {
	if len(data) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), data...)
}

// GetSessionMeta returns the current metadata snapshot for sessionKey.
func (s *JSONLStore) GetSessionMeta(_ context.Context, sessionKey string) (SessionMeta, error) {
	l := s.sessionLock(sessionKey)
	l.Lock()
	defer l.Unlock()

	meta, err := s.readMeta(sessionKey)
	if err != nil {
		return SessionMeta{}, err
	}
	meta.Scope = cloneRawJSON(meta.Scope)
	return meta, nil
}

// UpsertSessionMeta stores structured session metadata while preserving
// summary/count/skip timestamps maintained by the core JSONL store.
func (s *JSONLStore) UpsertSessionMeta(
	_ context.Context,
	sessionKey string,
	scope json.RawMessage,
) error {
	l := s.sessionLock(sessionKey)
	l.Lock()
	defer l.Unlock()

	meta, err := s.readMeta(sessionKey)
	if err != nil {
		return err
	}
	meta.Scope = cloneRawJSON(scope)
	now := time.Now()
	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = now
	}
	meta.UpdatedAt = now

	return s.writeMeta(sessionKey, meta)
}

// readMessages reads valid JSON lines from a .jsonl file, skipping
// the first `skip` lines without unmarshaling them. This avoids the
// cost of json.Unmarshal on logically truncated messages.
// Malformed trailing lines (e.g. from a crash) are silently skipped.
func readMessages(path string, skip int) ([]providers.Message, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return []providers.Message{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("memory: open jsonl: %w", err)
	}
	defer f.Close()

	var msgs []providers.Message
	scanner := bufio.NewScanner(f)
	// Allow large lines for tool results (read_file, web search, etc.).
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineSize)

	lineNum := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		lineNum++
		if lineNum <= skip {
			continue
		}
		var msg providers.Message
		if err := json.Unmarshal(line, &msg); err != nil {
			// Corrupt line — likely a partial write from a crash.
			// Log so operators know data was skipped, but don't
			// fail the entire read; this is the standard JSONL
			// recovery pattern.
			log.Printf("memory: skipping corrupt line %d in %s: %v",
				lineNum, filepath.Base(path), err)
			continue
		}
		if messageutil.IsTransientAssistantThoughtMessage(msg) {
			continue
		}
		msgs = append(msgs, msg)
	}
	if scanner.Err() != nil {
		return nil, fmt.Errorf("memory: scan jsonl: %w", scanner.Err())
	}

	if msgs == nil {
		msgs = []providers.Message{}
	}
	return msgs, nil
}

// scanRetainedMessageLines returns the total number of non-empty raw JSONL
// lines plus the raw line numbers that survive readMessages filtering.
// TruncateHistory uses this to compute keepLast against retained messages
// while preserving the raw-line skip offset stored in metadata.
func scanRetainedMessageLines(path string) (int, []int, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return 0, []int{}, nil
	}
	if err != nil {
		return 0, nil, fmt.Errorf("memory: open jsonl: %w", err)
	}
	defer f.Close()

	rawCount := 0
	retained := make([]int, 0)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineSize)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		rawCount++

		var msg providers.Message
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		if messageutil.IsTransientAssistantThoughtMessage(msg) {
			continue
		}
		retained = append(retained, rawCount)
	}
	if err := scanner.Err(); err != nil {
		return 0, nil, err
	}
	return rawCount, retained, nil
}

func (s *JSONLStore) AddMessage(
	_ context.Context, sessionKey, role, content string,
) error {
	return s.addMsg(sessionKey, providers.Message{
		Role:    role,
		Content: content,
	})
}

func (s *JSONLStore) AddFullMessage(
	_ context.Context, sessionKey string, msg providers.Message,
) error {
	return s.addMsg(sessionKey, msg)
}

// addMsg is the shared implementation for AddMessage and AddFullMessage.
func (s *JSONLStore) addMsg(sessionKey string, msg providers.Message) error {
	if messageutil.IsTransientAssistantThoughtMessage(msg) {
		return nil
	}

	l := s.sessionLock(sessionKey)
	l.Lock()
	defer l.Unlock()

	now := time.Now()

	if msg.CreatedAt == nil {
		msg.CreatedAt = &now
	}

	// Append the message as a single JSON line.
	line, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("memory: marshal message: %w", err)
	}
	line = append(line, '\n')

	f, err := os.OpenFile(
		s.jsonlPath(sessionKey),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND,
		0o644,
	)
	if err != nil {
		return fmt.Errorf("memory: open jsonl for append: %w", err)
	}
	_, writeErr := f.Write(line)
	if writeErr != nil {
		f.Close()
		return fmt.Errorf("memory: append message: %w", writeErr)
	}
	// Flush to physical storage before closing. This matches the
	// durability guarantee of writeMeta and rewriteJSONL (which use
	// WriteFileAtomic with fsync). Without Sync, a power loss could
	// leave the append in the kernel page cache only — lost on reboot.
	if syncErr := f.Sync(); syncErr != nil {
		f.Close()
		return fmt.Errorf("memory: sync jsonl: %w", syncErr)
	}
	if closeErr := f.Close(); closeErr != nil {
		return fmt.Errorf("memory: close jsonl: %w", closeErr)
	}

	// Update metadata.
	meta, err := s.readMeta(sessionKey)
	if err != nil {
		return err
	}
	if meta.Count == 0 && meta.CreatedAt.IsZero() {
		meta.CreatedAt = now
	}
	meta.Count++
	meta.UpdatedAt = now

	return s.writeMeta(sessionKey, meta)
}

func (s *JSONLStore) GetHistory(
	_ context.Context, sessionKey string,
) ([]providers.Message, error) {
	l := s.sessionLock(sessionKey)
	l.Lock()
	defer l.Unlock()

	meta, err := s.readMeta(sessionKey)
	if err != nil {
		return nil, err
	}

	// Pass meta.Skip so readMessages skips those lines without
	// unmarshaling them — avoids wasted CPU on truncated messages.
	msgs, err := readMessages(s.jsonlPath(sessionKey), meta.Skip)
	if err != nil {
		return nil, err
	}

	return msgs, nil
}

func (s *JSONLStore) GetSummary(
	_ context.Context, sessionKey string,
) (string, error) {
	l := s.sessionLock(sessionKey)
	l.Lock()
	defer l.Unlock()

	meta, err := s.readMeta(sessionKey)
	if err != nil {
		return "", err
	}
	return meta.Summary, nil
}

func (s *JSONLStore) SetSummary(
	_ context.Context, sessionKey, summary string,
) error {
	l := s.sessionLock(sessionKey)
	l.Lock()
	defer l.Unlock()

	meta, err := s.readMeta(sessionKey)
	if err != nil {
		return err
	}
	now := time.Now()
	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = now
	}
	meta.Summary = summary
	meta.UpdatedAt = now

	return s.writeMeta(sessionKey, meta)
}

func (s *JSONLStore) TruncateHistory(
	_ context.Context, sessionKey string, keepLast int,
) error {
	l := s.sessionLock(sessionKey)
	l.Lock()
	defer l.Unlock()

	meta, err := s.readMeta(sessionKey)
	if err != nil {
		return err
	}

	rawCount, retainedRawLines, scanErr := scanRetainedMessageLines(s.jsonlPath(sessionKey))
	if scanErr != nil {
		return scanErr
	}
	meta.Count = rawCount
	if meta.Skip > meta.Count {
		meta.Skip = meta.Count
	}

	activeStart := sort.Search(len(retainedRawLines), func(i int) bool {
		return retainedRawLines[i] > meta.Skip
	})
	activeRetainedCount := len(retainedRawLines) - activeStart

	switch {
	case keepLast <= 0 || activeRetainedCount == 0:
		meta.Skip = meta.Count
	case keepLast < activeRetainedCount:
		activeRawLines := retainedRawLines[activeStart:]
		meta.Skip = activeRawLines[activeRetainedCount-keepLast-1]
	}
	meta.UpdatedAt = time.Now()

	return s.writeMeta(sessionKey, meta)
}

func (s *JSONLStore) SetHistory(
	_ context.Context,
	sessionKey string,
	history []providers.Message,
) error {
	history = messageutil.FilterInvalidHistoryMessages(history)

	l := s.sessionLock(sessionKey)
	l.Lock()
	defer l.Unlock()

	meta, err := s.readMeta(sessionKey)
	if err != nil {
		return err
	}
	now := time.Now()
	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = now
	}
	meta.Skip = 0
	meta.Count = len(history)
	meta.UpdatedAt = now

	for i := range history {
		if history[i].CreatedAt == nil {
			history[i].CreatedAt = &now
		}
	}

	// Write meta BEFORE rewriting the JSONL file. If we crash between
	// the two writes, meta has Skip=0 and the old file is still intact,
	// so GetHistory reads from line 1 — returning "too many" messages
	// rather than losing data. The next SetHistory call corrects this.
	err = s.writeMeta(sessionKey, meta)
	if err != nil {
		return err
	}

	return s.rewriteJSONL(sessionKey, history)
}

// Compact physically rewrites the JSONL file, dropping all logically
// skipped lines. This reclaims disk space that accumulates after
// repeated TruncateHistory calls.
//
// It is safe to call at any time; if there is nothing to compact
// (skip == 0) the method returns immediately.
func (s *JSONLStore) Compact(
	_ context.Context, sessionKey string,
) error {
	l := s.sessionLock(sessionKey)
	l.Lock()
	defer l.Unlock()

	meta, err := s.readMeta(sessionKey)
	if err != nil {
		return err
	}
	if meta.Skip == 0 {
		return nil
	}

	// Read only the active messages, skipping truncated lines
	// without unmarshaling them.
	active, err := readMessages(s.jsonlPath(sessionKey), meta.Skip)
	if err != nil {
		return err
	}

	// Write meta BEFORE rewriting the JSONL file. If the process
	// crashes between the two writes, meta has Skip=0 and the old
	// (uncompacted) file is still intact, so GetHistory reads from
	// line 1 — returning previously-truncated messages rather than
	// losing data. The next Compact or TruncateHistory corrects this.
	meta.Skip = 0
	meta.Count = len(active)
	meta.UpdatedAt = time.Now()

	err = s.writeMeta(sessionKey, meta)
	if err != nil {
		return err
	}

	return s.rewriteJSONL(sessionKey, active)
}

// rewriteJSONL atomically replaces the JSONL file with the given messages
// using the project's standard WriteFileAtomic (temp + fsync + rename).
func (s *JSONLStore) rewriteJSONL(
	sessionKey string, msgs []providers.Message,
) error {
	msgs = messageutil.FilterInvalidHistoryMessages(msgs)

	var buf bytes.Buffer
	for i, msg := range msgs {
		line, err := json.Marshal(msg)
		if err != nil {
			return fmt.Errorf("memory: marshal message %d: %w", i, err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return fileutil.WriteFileAtomic(s.jsonlPath(sessionKey), buf.Bytes(), 0o644)
}

// ListSessions returns all known session keys by reading .meta.json files.
func (s *JSONLStore) ListSessions() []string {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}
	var keys []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".meta.json") {
			continue
		}
		// Read the meta file to get the original key
		data, err := os.ReadFile(filepath.Join(s.dir, entry.Name()))
		if err != nil {
			continue
		}
		var meta SessionMeta
		if err := json.Unmarshal(data, &meta); err != nil {
			continue
		}
		if meta.Key != "" {
			keys = append(keys, meta.Key)
		}
	}
	return keys
}

func (s *JSONLStore) Close() error {
	return nil
}
