package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/xibodev/compa/pkg/logger"
	"github.com/xibodev/compa/pkg/updater"
)

// POST /api/update installs a release of Compa from github.com/xibodev/compa:
// compa-kernel and then compa, both from the same archive for this OS and
// architecture, verified against the release's SHA256SUMS. The running
// launcher and kernel keep the old version until Compa restarts.
//
// Every request field is optional:
//
//	{"version": "v1.2.3"}  the release to install; default: the latest
//	{"url": "https://github.com/xibodev/compa/releases/tag/v1.2.3"}
//	{"binary": "compa"}    accepted from older clients and ignored; both
//	                       programs are always updated together
//
// The answer is {"status": "ok"|"error", "message": "...", "version": "v1.2.3"}.

const (
	updateTimeout        = 15 * time.Minute
	updateRestartMessage = "restart Compa to use the new version"
)

var (
	// applyUpdate installs a release; tests point it at a fake one.
	applyUpdate = updater.Update

	// updateMu is held for a whole update, so two cannot replace the same
	// files at once.
	updateMu sync.Mutex
	// updateInstalled is the release this process installed, which takes
	// effect on the next restart. Guarded by updateMu.
	updateInstalled string
)

// registerUpdateRoutes registers the self-update endpoint.
func (h *Handler) registerUpdateRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/update", h.handleUpdate)
}

type updateRequest struct {
	Version string `json:"version,omitempty"`
	URL     string `json:"url,omitempty"`
	Binary  string `json:"binary,omitempty"`
}

type updateResponse struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
	Version string `json:"version,omitempty"`
}

func (h *Handler) handleUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeUpdateError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req updateRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeUpdateError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	tag, err := requestedUpdateTag(req)
	if err != nil {
		writeUpdateError(w, http.StatusBadRequest, err.Error())
		return
	}

	if !updateMu.TryLock() {
		writeUpdateError(w, http.StatusConflict, "an update is already running")
		return
	}
	defer updateMu.Unlock()

	if updateInstalled != "" {
		// A second update before the restart would replace programs that are
		// still running, which Windows refuses; the first one is what counts.
		writeUpdateOK(w, fmt.Sprintf("compa and compa-kernel %s are already installed; %s",
			updateInstalled, updateRestartMessage), updateInstalled)
		return
	}

	// Detached from the request: closing the page must not stop an update
	// halfway through replacing the programs.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), updateTimeout)
	defer cancel()
	res, err := applyUpdate(ctx, updater.Options{Tag: tag})
	if err != nil {
		logger.ErrorC("update", fmt.Sprintf("Update failed: %v", err))
		writeUpdateError(w, http.StatusInternalServerError, err.Error())
		return
	}
	updateInstalled = res.Tag
	logger.InfoC("update", fmt.Sprintf("Installed compa and compa-kernel %s in %s from %s",
		res.Tag, res.Dir, res.Archive))
	writeUpdateOK(w, fmt.Sprintf("updated compa and compa-kernel to %s; %s", res.Tag, updateRestartMessage), res.Tag)
}

// requestedUpdateTag is the release a request asks for; "" is the latest.
func requestedUpdateTag(req updateRequest) (string, error) {
	tag, err := updater.NormalizeTag(req.Version)
	if err != nil {
		return "", err
	}
	fromURL, err := updater.TagFromURL(req.URL)
	if err != nil {
		return "", err
	}
	if tag != "" && req.URL != "" && fromURL != tag {
		return "", errors.New("version and url name different releases")
	}
	if tag == "" {
		tag = fromURL
	}
	return tag, nil
}

func writeUpdateOK(w http.ResponseWriter, message, version string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(updateResponse{Status: "ok", Message: message, Version: version})
}

func writeUpdateError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(updateResponse{Status: "error", Message: message})
}
