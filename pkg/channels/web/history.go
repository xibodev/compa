package web

import (
	"net/http"

	"github.com/xibodev/compa/v3/pkg/session/history"
)

// SetSessionHistory gives the channel the reader of the sessions it serves
// at /web/sessions; source is called for each request.
func (c *WebChannel) SetSessionHistory(source func() history.Reader) {
	if source == nil {
		c.sessionHistory.Store(nil)
		return
	}
	c.sessionHistory.Store(&source)
}

// handleSessionHistory serves the web chat's session history to a client
// with the web chat token, as the launcher's /api/sessions does:
//
//	GET /web/sessions?offset=&limit=  one page of sessions, newest first
//	GET /web/sessions/{session_id}     a session's transcript
//
// session_id is the id the client opens /web/ws?session_id= with.
func (c *WebChannel) handleSessionHistory(w http.ResponseWriter, r *http.Request, sessionID string, detail bool) {
	if !c.IsRunning() {
		http.Error(w, "channel not running", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !c.authenticate(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	source := c.sessionHistory.Load()
	if source == nil {
		http.Error(w, "session history unavailable", http.StatusServiceUnavailable)
		return
	}
	reader := (*source)()
	if !detail {
		history.ServeList(w, r, reader)
		return
	}
	if sessionID == "" {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	history.ServeDetail(w, reader, sessionID)
}
