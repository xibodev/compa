package api

// The WhatsApp page links Compa to a WhatsApp account by QR code: Compa
// becomes a linked device of the account, its session saved where the
// WhatsApp channel reads it, and the channel is turned on.

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"sync"
	"time"

	"rsc.io/qr"

	"github.com/xibodev/compa/v3/pkg/channels/whatsapp"
	"github.com/xibodev/compa/v3/pkg/config"
	"github.com/xibodev/compa/v3/pkg/logger"
)

// whatsappLinkTimeout bounds one linking: the QR codes keep changing until
// one is scanned or this passes.
const whatsappLinkTimeout = 5 * time.Minute

// Linking and checking a session; tests replace them.
var (
	linkWhatsApp   = whatsapp.Link
	linkedWhatsApp = whatsapp.Linked
)

// whatsappLinkState is the WhatsApp linking in progress, or the last one.
type whatsappLinkState struct {
	mu      sync.Mutex
	running bool
	qr      string // data URI of the QR code to scan
	phone   string // the linked number, once linked
	err     string // why the last linking failed
}

// whatsappLinkResponse is the body of GET and POST /api/whatsapp/link.
// Status is "unlinked", "waiting" (a QR code to scan), "linked" or "failed".
type whatsappLinkResponse struct {
	Status    string `json:"status"`
	QRDataURI string `json:"qr_data_uri,omitempty"`
	Phone     string `json:"phone,omitempty"`
	Error     string `json:"error,omitempty"`
}

func (h *Handler) registerWhatsAppRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/whatsapp/link", h.handleGetWhatsAppLink)
	mux.HandleFunc("POST /api/whatsapp/link", h.handleStartWhatsAppLink)
}

// handleGetWhatsAppLink reports whether an account is linked, or the
// linking in progress.
//
//	GET /api/whatsapp/link
func (h *Handler) handleGetWhatsAppLink(w http.ResponseWriter, r *http.Request) {
	resp, err := h.whatsappLinkStatus(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleStartWhatsAppLink starts linking an account, unless one is linked or
// linking; poll GET for the QR code and the result.
//
//	POST /api/whatsapp/link
func (h *Handler) handleStartWhatsAppLink(w http.ResponseWriter, r *http.Request) {
	resp, err := h.whatsappLinkStatus(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if resp.Status == "linked" || resp.Status == "waiting" {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	dir, err := h.whatsappStorePath()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	link := &h.whatsappLink
	link.mu.Lock()
	link.running, link.qr, link.phone, link.err = true, "", "", ""
	link.mu.Unlock()

	go h.runWhatsAppLink(dir)
	writeJSON(w, http.StatusOK, whatsappLinkResponse{Status: "waiting"})
}

func (h *Handler) runWhatsAppLink(dir string) {
	ctx, cancel := context.WithTimeout(context.Background(), whatsappLinkTimeout)
	defer cancel()
	link := &h.whatsappLink
	phone, err := linkWhatsApp(ctx, dir, func(code string) {
		uri, err := generateQRDataURI(code)
		if err != nil {
			return
		}
		link.mu.Lock()
		link.qr = uri
		link.mu.Unlock()
	})
	if err == nil {
		err = h.enableWhatsApp()
	}

	link.mu.Lock()
	link.running, link.qr, link.phone = false, "", phone
	if err != nil {
		link.phone, link.err = "", err.Error()
		logger.WarnCF("whatsapp", "WhatsApp linking failed", map[string]any{"error": err.Error()})
	}
	link.mu.Unlock()
}

// whatsappLinkStatus is the linking in progress, the last one's failure, or
// whether an account is linked.
func (h *Handler) whatsappLinkStatus(ctx context.Context) (whatsappLinkResponse, error) {
	link := &h.whatsappLink
	link.mu.Lock()
	running, qrURI, linkErr := link.running, link.qr, link.err
	link.mu.Unlock()
	if running {
		return whatsappLinkResponse{Status: "waiting", QRDataURI: qrURI}, nil
	}

	dir, err := h.whatsappStorePath()
	if err != nil {
		return whatsappLinkResponse{}, err
	}
	phone, err := linkedWhatsApp(ctx, dir)
	if err != nil {
		return whatsappLinkResponse{}, fmt.Errorf("read the WhatsApp session: %w", err)
	}
	switch {
	case phone != "":
		return whatsappLinkResponse{Status: "linked", Phone: phone}, nil
	case linkErr != "":
		return whatsappLinkResponse{Status: "failed", Error: linkErr}, nil
	}
	return whatsappLinkResponse{Status: "unlinked"}, nil
}

func (h *Handler) whatsappStorePath() (string, error) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		return "", fmt.Errorf("load config: %w", err)
	}
	var settings *config.WhatsAppSettings
	if bc := cfg.Channels.Get(config.ChannelWhatsApp); bc != nil {
		if decoded, err := bc.GetDecoded(); err == nil {
			settings, _ = decoded.(*config.WhatsAppSettings)
		}
	}
	return whatsapp.StorePath(cfg, settings), nil
}

// enableWhatsApp turns the WhatsApp channel on once an account is linked,
// restarting a running gateway so it connects.
func (h *Handler) enableWhatsApp() error {
	_, err := h.updateConfig(func(cfg *config.Config) error {
		bc := cfg.Channels.Get(config.ChannelWhatsApp)
		if bc == nil {
			bc = &config.Channel{Type: config.ChannelWhatsApp}
			cfg.Channels[config.ChannelWhatsApp] = bc
		}
		if bc.Enabled {
			return errConfigUnchanged
		}
		bc.Enabled = true
		return nil
	})
	if err != nil {
		return err
	}
	if gatewayRestartsOnConfigChange(h.gatewayStatusData()) {
		if _, err := h.RestartGateway(); err != nil {
			logger.ErrorCF("whatsapp", "Failed to restart the gateway after linking WhatsApp", map[string]any{"error": err.Error()})
		}
	}
	return nil
}

// generateQRDataURI encodes content as a QR code PNG and returns a data URI.
func generateQRDataURI(content string) (string, error) {
	code, err := qr.Encode(content, qr.L)
	if err != nil {
		return "", fmt.Errorf("qr encode: %w", err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(code.PNG()), nil
}
