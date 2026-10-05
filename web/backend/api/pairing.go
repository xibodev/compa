package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/xibodev/compa/pkg/config"
	"github.com/xibodev/compa/pkg/logger"
	"github.com/xibodev/compa/pkg/pairing"
)

// pairingRequestView is one pending pairing request as the dashboard reads
// it.
type pairingRequestView struct {
	SenderID    string `json:"sender_id"`
	PlatformID  string `json:"platform_id"`
	DisplayName string `json:"display_name"`
	FirstSeen   string `json:"first_seen"`
	LastSeen    string `json:"last_seen"`
	Count       int    `json:"count"`
}

// pairingDecision is the body of an approve or deny request.
type pairingDecision struct {
	SenderID string `json:"sender_id"`
}

// errPairingChannelMissing reports an approval for a channel that is no
// longer in channel_list.
var errPairingChannelMissing = errors.New("the channel is not configured")

// pairingHome is the Compa home whose pairing.json the channels write; tests
// replace it.
var pairingHome = config.GetHome

func (h *Handler) registerPairingRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/channels/{name}/pairing", h.handleListPairingRequests)
	mux.HandleFunc("POST /api/channels/{name}/pairing/approve", h.handleApprovePairingRequest)
	mux.HandleFunc("POST /api/channels/{name}/pairing/deny", h.handleDenyPairingRequest)
}

// pairingChannelKey resolves the {name} of a pairing route to the
// channel_list key the channel records its requests under. A catalog name
// maps to its config key, as on the channel config page (whatsapp_native is
// channel_list.whatsapp); another name must be a channel_list key itself.
func (h *Handler) pairingChannelKey(name string) (string, bool) {
	if item, ok := findChannelCatalogItem(name); ok {
		return item.ConfigKey, true
	}
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil || cfg.Channels.Get(name) == nil {
		return "", false
	}
	return name, true
}

// handleListPairingRequests lists the senders a channel holds for approval,
// most recently seen first.
//
//	GET /api/channels/{name}/pairing
func (h *Handler) handleListPairingRequests(w http.ResponseWriter, r *http.Request) {
	key, ok := h.pairingChannelKey(r.PathValue("name"))
	if !ok {
		writeJSONError(w, http.StatusNotFound, "channel not found")
		return
	}
	requests, err := pairing.List(pairingHome(), key)
	if err != nil {
		logger.ErrorCF("pairing", "Failed to read pairing requests", map[string]any{"channel": key, "error": err.Error()})
		writeJSONError(w, http.StatusInternalServerError, "failed to read the pairing requests")
		return
	}
	views := make([]pairingRequestView, 0, len(requests))
	for _, request := range requests {
		views = append(views, pairingRequestView{
			SenderID:    request.SenderID,
			PlatformID:  request.PlatformID,
			DisplayName: request.DisplayName,
			FirstSeen:   request.FirstSeen.UTC().Format(time.RFC3339),
			LastSeen:    request.LastSeen.UTC().Format(time.RFC3339),
			Count:       request.Count,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": views})
}

// handleApprovePairingRequest admits a held sender: it appends the sender to
// the channel's allow_from, removes the request, and applies the change to
// the running gateway through its reload (see applyLiveConfig), so the
// sender is admitted without a restart.
//
//	POST /api/channels/{name}/pairing/approve
func (h *Handler) handleApprovePairingRequest(w http.ResponseWriter, r *http.Request) {
	key, senderID, ok := h.readPairingDecision(w, r)
	if !ok {
		return
	}
	home := pairingHome()
	requests, err := pairing.List(home, key)
	if err != nil {
		logger.ErrorCF("pairing", "Failed to read pairing requests", map[string]any{"channel": key, "error": err.Error()})
		writeJSONError(w, http.StatusInternalServerError, "failed to read the pairing requests")
		return
	}
	if !hasPairingRequest(requests, senderID) {
		writeJSONError(w, http.StatusNotFound, "no pairing request from this sender")
		return
	}

	_, err = h.updateConfig(func(cfg *config.Config) error {
		bc := cfg.Channels.Get(key)
		if bc == nil {
			return errPairingChannelMissing
		}
		for _, entry := range bc.AllowFrom {
			if strings.TrimSpace(entry) == senderID {
				return errConfigUnchanged
			}
		}
		bc.AllowFrom = append(bc.AllowFrom, senderID)
		return nil
	})
	switch {
	case errors.Is(err, errPairingChannelMissing):
		writeJSONError(w, http.StatusNotFound, "channel not found")
		return
	case err != nil:
		logger.ErrorCF("pairing", "Failed to approve a pairing request", map[string]any{"channel": key, "error": err.Error()})
		writeJSONError(w, http.StatusInternalServerError, "failed to save the config")
		return
	}

	if err := pairing.Remove(home, key, senderID); err != nil {
		// The sender is admitted; approving again removes the request.
		logger.ErrorCF("pairing", "Failed to remove an approved pairing request", map[string]any{"channel": key, "error": err.Error()})
		writeJSONError(w, http.StatusInternalServerError, "the sender was approved, but the request could not be removed")
		return
	}
	logger.InfoCF("pairing", "Pairing request approved", map[string]any{"channel": key, "sender_id": senderID})
	h.scheduleLiveApply()
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleDenyPairingRequest drops a held sender's request. The sender stays
// out of allow_from; a later message records a new request.
//
//	POST /api/channels/{name}/pairing/deny
func (h *Handler) handleDenyPairingRequest(w http.ResponseWriter, r *http.Request) {
	key, senderID, ok := h.readPairingDecision(w, r)
	if !ok {
		return
	}
	if err := pairing.Remove(pairingHome(), key, senderID); err != nil {
		logger.ErrorCF("pairing", "Failed to remove a pairing request", map[string]any{"channel": key, "error": err.Error()})
		writeJSONError(w, http.StatusInternalServerError, "failed to remove the pairing request")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readPairingDecision resolves the channel and reads the sender_id of an
// approve or deny request, answering the request itself when either is
// missing.
func (h *Handler) readPairingDecision(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	key, ok := h.pairingChannelKey(r.PathValue("name"))
	if !ok {
		writeJSONError(w, http.StatusNotFound, "channel not found")
		return "", "", false
	}
	var body pairingDecision
	if err := decodeJSONBody(r, &body); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return "", "", false
	}
	senderID := strings.TrimSpace(body.SenderID)
	if senderID == "" {
		writeJSONError(w, http.StatusBadRequest, "sender_id is required")
		return "", "", false
	}
	return key, senderID, true
}

func hasPairingRequest(requests []pairing.Request, senderID string) bool {
	for _, request := range requests {
		if request.SenderID == senderID {
			return true
		}
	}
	return false
}
