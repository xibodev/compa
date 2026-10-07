package memory

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/xibodev/compa/v3/pkg/fileutil"
	"github.com/xibodev/compa/v3/pkg/providers"
	"github.com/xibodev/compa/v3/pkg/providers/messageutil"
)

const (
	// numLockShards is the fixed number of mutexes used to serialize
	// per-session access. Using a sharded array instead of a map keeps
	// memory bounded regardless of how many sessions are created over
	// the lifetime of the process — important for a long-running daemon.
	numLockShards = 64

	// maxLineSize is the maximum size of a single JSON line in a .jsonl
	// file. Tool results (read_file, web search, etc.) can be large, so
	// we set a generous limit. A longer line is skipped when reading, not
	// loaded: one oversized message must not hide the rest of a session.
	maxLineSize = 10 * 1024 * 1024 // 10 MB
)

var (
	dirLocksMu sync.Mutex
	dirLocks   = make(map[string]*[numLockShards]sync.Mutex)
)

// locksForDir returns the session locks of dir, shared by every store opened
// on it: a config reload opens new stores on the same directories while
// running turns and background summaries still write through the old ones.
func locksForDir(dir string) *[numLockShards]sync.Mutex {
	key := filepath.Clean(dir)
	if abs, err := filepath.Abs(key); err == nil {
		key = abs
	}
	dirLocksMu.Lock()
	defer dirLocksMu.Unlock()
	if locks, ok := dirLocks[key]; ok {
		return locks
	}
	locks := new([numLockShards]sync.Mutex)
	dirLocks[key] = locks
	return locks
}

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
	locks *[numLockShards]sync.Mutex
}

// NewJSONLStore creates a new JSONL-backed store rooted at dir, creating the
// directory when needed. If the directory cannot be created the error is
// returned together with a store that is still safe to use: it reports no
// sessions and its writes fail until the directory becomes available.
func NewJSONLStore(dir string) (*JSONLStore, error) {
	store := &JSONLStore{dir: dir, locks: locksForDir(dir)}
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

// eachLine calls fn with every non-empty line of r, numbered from 1, without
// its line ending. A line longer than maxLineSize is passed as nil: it still
// counts, so the numbers stay those of the file, but it is not read into
// memory.
func eachLine(r io.Reader, fn func(lineNum int, line []byte)) error {
	reader := bufio.NewReaderSize(r, 64*1024)
	var line []byte
	oversize := false
	lineNum := 0
	for {
		chunk, err := reader.ReadSlice('\n')
		if !oversize {
			// +2 leaves room for the line ending beyond the content limit.
			if len(line)+len(chunk) > maxLineSize+2 {
				oversize = true
				line = line[:0]
			} else {
				line = append(line, chunk...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue // the same line goes on
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		content := bytes.TrimSuffix(bytes.TrimSuffix(line, []byte("\n")), []byte("\r"))
		if oversize || len(content) > 0 {
			lineNum++
			if oversize {
				fn(lineNum, nil)
			} else {
				fn(lineNum, content)
			}
		}
		line = line[:0]
		oversize = false
		if err != nil {
			return nil // io.EOF
		}
	}
}

// storedMessage is a message read from a .jsonl file with its line number.
type storedMessage struct {
	line int
	msg  providers.Message
}

// readStoredMessages reads the messages of a .jsonl file after its first
// skip lines, which it does not decode, and returns them with the file's
// line count. A corrupt line (a write cut short by a crash) or an oversized
// one is skipped and logged rather than failing the whole history.
func readStoredMessages(path string, skip int) ([]storedMessage, int, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("memory: open jsonl: %w", err)
	}
	defer f.Close()

	var stored []storedMessage
	total := 0
	err = eachLine(f, func(lineNum int, line []byte) {
		total = lineNum
		if lineNum <= skip {
			return
		}
		if line == nil {
			log.Printf("memory: skipping line %d in %s: longer than %d bytes",
				lineNum, filepath.Base(path), maxLineSize)
			return
		}
		var msg providers.Message
		if err := json.Unmarshal(line, &msg); err != nil {
			log.Printf("memory: skipping corrupt line %d in %s: %v",
				lineNum, filepath.Base(path), err)
			return
		}
		if messageutil.IsTransientAssistantThoughtMessage(msg) {
			return
		}
		stored = append(stored, storedMessage{line: lineNum, msg: msg})
	})
	if err != nil {
		return nil, 0, fmt.Errorf("memory: read jsonl: %w", err)
	}
	return stored, total, nil
}

// readMessages reads the messages of a .jsonl file after its first skip
// lines (see readStoredMessages).
func readMessages(path string, skip int) ([]providers.Message, error) {
	stored, _, err := readStoredMessages(path, skip)
	if err != nil {
		return nil, err
	}
	msgs := make([]providers.Message, len(stored))
	for i := range stored {
		msgs[i] = stored[i].msg
	}
	return msgs, nil
}

// endsWithoutNewline reports whether f has content whose last byte is not a
// newline: a line a crash cut short.
func endsWithoutNewline(f *os.File) (bool, error) {
	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return false, err
	}
	last := make([]byte, 1)
	if _, err := f.ReadAt(last, info.Size()-1); err != nil {
		return false, err
	}
	return last[0] != '\n', nil
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
		os.O_CREATE|os.O_RDWR|os.O_APPEND,
		0o644,
	)
	if err != nil {
		return fmt.Errorf("memory: open jsonl for append: %w", err)
	}
	// A write a crash cut short leaves a line without its newline; this
	// message must start a line of its own rather than be glued onto it.
	if torn, tornErr := endsWithoutNewline(f); tornErr != nil {
		f.Close()
		return fmt.Errorf("memory: check jsonl end: %w", tornErr)
	} else if torn {
		line = append([]byte{'\n'}, line...)
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

	active, rawCount, readErr := readStoredMessages(s.jsonlPath(sessionKey), meta.Skip)
	if readErr != nil {
		return readErr
	}
	meta.Count = rawCount
	if meta.Skip > meta.Count {
		meta.Skip = meta.Count
	}

	switch {
	case keepLast <= 0 || len(active) == 0:
		meta.Skip = meta.Count
	case keepLast < len(active):
		meta.Skip = active[len(active)-keepLast-1].line
	}
	meta.UpdatedAt = time.Now()

	return s.writeMeta(sessionKey, meta)
}

// CommitSummary stores summary as the session's summary and drops
// summarized from the start of its history, in one metadata write, provided
// summarized are still the session's oldest messages. When they are not —
// the history was cleared, replaced or compacted after they were read — it
// changes nothing and reports false. Messages appended meanwhile are kept.
func (s *JSONLStore) CommitSummary(
	_ context.Context,
	sessionKey string,
	summarized []providers.Message,
	summary string,
) (bool, error) {
	l := s.sessionLock(sessionKey)
	l.Lock()
	defer l.Unlock()

	meta, err := s.readMeta(sessionKey)
	if err != nil {
		return false, err
	}
	active, rawCount, err := readStoredMessages(s.jsonlPath(sessionKey), meta.Skip)
	if err != nil {
		return false, err
	}
	if len(active) < len(summarized) {
		return false, nil
	}
	for i := range summarized {
		if !sameStoredMessage(active[i].msg, summarized[i]) {
			return false, nil
		}
	}

	now := time.Now()
	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = now
	}
	meta.Count = rawCount
	if len(summarized) > 0 {
		meta.Skip = active[len(summarized)-1].line
	}
	meta.Summary = summary
	meta.UpdatedAt = now
	return true, s.writeMeta(sessionKey, meta)
}

// sameStoredMessage reports whether two messages read from a session file
// are the same message: they encode alike.
func sameStoredMessage(a, b providers.Message) bool {
	encodedA, errA := json.Marshal(a)
	encodedB, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(encodedA, encodedB)
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
