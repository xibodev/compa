package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/xibodev/compa/v4/pkg/config"
)

func whatsappLinkRequest(t *testing.T, mux *http.ServeMux, method string) whatsappLinkResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, "/api/whatsapp/link", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("%s /api/whatsapp/link = %d %s", method, rec.Code, rec.Body.String())
	}
	var resp whatsappLinkResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	return resp
}

// Linking shows the QR code to scan, then turns the WhatsApp channel on.
func TestWhatsAppLinkShowsTheQRCodeThenTurnsTheChannelOn(t *testing.T) {
	resetGatewayTestState(t)
	configPath, cleanup := setupCredentialTestEnv(t)
	defer cleanup()

	linked := ""
	scanned := make(chan struct{})
	shown := make(chan struct{})
	previousLink, previousLinked := linkWhatsApp, linkedWhatsApp
	t.Cleanup(func() { linkWhatsApp, linkedWhatsApp = previousLink, previousLinked })
	linkedWhatsApp = func(context.Context, string) (string, error) { return linked, nil }
	linkWhatsApp = func(_ context.Context, _ string, onCode func(string)) (string, error) {
		onCode("code-1")
		close(shown)
		<-scanned
		linked = "15550001111"
		return linked, nil
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	if got := whatsappLinkRequest(t, mux, http.MethodGet); got.Status != "unlinked" {
		t.Fatalf("before linking: %+v", got)
	}
	if got := whatsappLinkRequest(t, mux, http.MethodPost); got.Status != "waiting" {
		t.Fatalf("start: %+v", got)
	}
	<-shown
	if got := whatsappLinkRequest(t, mux, http.MethodGet); got.Status != "waiting" || got.QRDataURI == "" {
		t.Fatalf("while waiting: %+v, want a QR code", got)
	}

	close(scanned)
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := whatsappLinkRequest(t, mux, http.MethodGet)
		if got.Status == "linked" && got.Phone == "15550001111" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after scanning: %+v", got)
		}
		time.Sleep(20 * time.Millisecond)
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if bc := cfg.Channels.Get(config.ChannelWhatsApp); bc == nil || !bc.Enabled {
		t.Fatalf("whatsapp channel = %+v, want it on", bc)
	}
}
