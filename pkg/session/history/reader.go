package history

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/xibodev/compa/v4/pkg/config"
)

// ErrNotFound reports that no web chat session has the asked id, or that it
// has nothing to show.
var ErrNotFound = errors.New("session not found")

// Reader reads the web chat's sessions in a sessions directory.
type Reader struct {
	// Dir is the workspace's sessions directory (see SessionsDir).
	Dir string
	// MaxArgsLength cuts the tool call arguments a transcript shows; 0 uses
	// the default.
	MaxArgsLength int
}

// SessionsDir returns the sessions directory of workspace: its "sessions"
// folder, or that of the default workspace when workspace is empty.
func SessionsDir(workspace string) string {
	if workspace == "" {
		workspace = filepath.Join(config.GetHome(), "workspace")
	}
	return filepath.Join(config.ExpandHome(workspace), "sessions")
}

// FileBase returns the name a session's files have in the sessions
// directory, before their extension.
func FileBase(key string) string {
	return sanitizeSessionKey(key)
}

func (r Reader) maxArgsLength() int {
	if r.MaxArgsLength > 0 {
		return r.MaxArgsLength
	}
	return defaultToolFeedbackMaxArgsLength()
}

// List returns one page of sessions, most recently updated first. A session
// with nothing to show gives its place on the page to the next one.
func (r Reader) List(offset, limit int) []Summary {
	return listWebChatSessions(r.Dir, offset, limit, r.maxArgsLength())
}

// Detail is a session's transcript.
type Detail struct {
	ID       string    `json:"id"`
	Messages []Message `json:"messages"`
	Summary  string    `json:"summary"`
	Created  string    `json:"created"`
	Updated  string    `json:"updated"`
}

// Get returns the transcript of the session id, including the model's
// thoughts, or ErrNotFound.
func (r Reader) Get(id string) (Detail, error) {
	ref, err := findWebChatJSONLSession(r.Dir, id)
	var sess sessionFile
	if err == nil {
		sess, err = readJSONLSession(r.Dir, ref.Key)
	}
	if err == nil && isEmptySession(sess) {
		err = os.ErrNotExist
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Detail{}, ErrNotFound
		}
		return Detail{}, err
	}

	for i := range sess.Messages {
		if sess.Messages[i].CreatedAt == nil {
			sess.Messages[i].CreatedAt = &sess.Updated
		}
	}
	return Detail{
		ID:       id,
		Messages: detailSessionMessages(sess.Messages, r.maxArgsLength()),
		Summary:  sess.Summary,
		Created:  sess.Created.Format(time.RFC3339),
		Updated:  sess.Updated.Format(time.RFC3339),
	}, nil
}

// Delete removes the files of the session id. It returns ErrNotFound when
// there were none.
func (r Reader) Delete(id string) error {
	ref, err := findWebChatJSONLSession(r.Dir, id)
	if err != nil {
		return ErrNotFound
	}
	removed := false
	base := filepath.Join(r.Dir, sanitizeSessionKey(ref.Key))
	for _, path := range []string{base + ".jsonl", base + ".meta.json"} {
		if err := os.Remove(path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		removed = true
	}
	if !removed {
		return ErrNotFound
	}
	return nil
}

// ServeList answers a request for one page of sessions: offset (0 by
// default) and limit (20 by default) come from the query.
func ServeList(w http.ResponseWriter, r *http.Request, reader Reader) {
	offset, limit := 0, 20
	if val, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && val >= 0 {
		offset = val
	}
	if val, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && val > 0 {
		limit = val
	}
	writeJSON(w, reader.List(offset, limit))
}

// ServeDetail answers a request for the transcript of the session id: 404
// when it has none.
func ServeDetail(w http.ResponseWriter, reader Reader, id string) {
	detail, err := reader.Get(id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			http.Error(w, "session not found", http.StatusNotFound)
		} else {
			http.Error(w, "failed to parse session", http.StatusInternalServerError)
		}
		return
	}
	writeJSON(w, detail)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}
