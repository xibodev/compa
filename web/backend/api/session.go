package api

import (
	"errors"
	"net/http"

	"github.com/xibodev/compa/v4/pkg/config"
	"github.com/xibodev/compa/v4/pkg/session/history"
)

// registerSessionRoutes binds session list and detail endpoints to the ServeMux.
func (h *Handler) registerSessionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/sessions", h.handleListSessions)
	mux.HandleFunc("GET /api/sessions/{id}", h.handleGetSession)
	mux.HandleFunc("DELETE /api/sessions/{id}", h.handleDeleteSession)
}

// sessionReader reads the web chat sessions of the saved config's workspace.
func (h *Handler) sessionReader() (history.Reader, error) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		return history.Reader{}, err
	}
	return history.Reader{
		Dir:           history.SessionsDir(cfg.Agents.Defaults.Workspace),
		MaxArgsLength: cfg.Agents.Defaults.GetToolFeedbackMaxArgsLength(),
	}, nil
}

// handleListSessions returns a list of web chat session summaries.
//
//	GET /api/sessions
func (h *Handler) handleListSessions(w http.ResponseWriter, r *http.Request) {
	reader, err := h.sessionReader()
	if err != nil {
		http.Error(w, "failed to resolve sessions directory", http.StatusInternalServerError)
		return
	}
	history.ServeList(w, r, reader)
}

// handleGetSession returns the full message history for a specific session.
//
//	GET /api/sessions/{id}
func (h *Handler) handleGetSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if sessionID == "" {
		http.Error(w, "missing session id", http.StatusBadRequest)
		return
	}
	reader, err := h.sessionReader()
	if err != nil {
		http.Error(w, "failed to resolve sessions directory", http.StatusInternalServerError)
		return
	}
	history.ServeDetail(w, reader, sessionID)
}

// handleDeleteSession deletes a specific session.
//
//	DELETE /api/sessions/{id}
func (h *Handler) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if sessionID == "" {
		http.Error(w, "missing session id", http.StatusBadRequest)
		return
	}
	reader, err := h.sessionReader()
	if err != nil {
		http.Error(w, "failed to resolve sessions directory", http.StatusInternalServerError)
		return
	}
	switch err := reader.Delete(sessionID); {
	case errors.Is(err, history.ErrNotFound):
		http.Error(w, "session not found", http.StatusNotFound)
	case err != nil:
		http.Error(w, "failed to delete session", http.StatusInternalServerError)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
