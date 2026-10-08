package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xibodev/compa/v3/pkg/config"
	"github.com/xibodev/compa/v3/pkg/pairing"
)

// pairingTestHandler saves a config whose telegram and whatsapp channels
// have the given allow_from, in a fresh Compa home.
func pairingTestHandler(t *testing.T, allowFrom ...string) (*Handler, *http.ServeMux, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	configPath := filepath.Join(home, "config.json")
	cfg := config.DefaultConfig()
	cfg.Channels.Get("telegram").AllowFrom = append(config.FlexibleStringSlice(nil), allowFrom...)
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	h := NewHandler(configPath)
	t.Cleanup(h.waitForLiveApplies)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return h, mux, home
}

func recordPairingRequest(t *testing.T, home string, r pairing.Request) {
	t.Helper()
	if err := pairing.Record(home, r); err != nil {
		t.Fatalf("pairing.Record() error = %v", err)
	}
}

func pairingRequestJSON(t *testing.T, mux *http.ServeMux, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestPairingListReturnsRequestsNewestFirst(t *testing.T) {
	_, mux, home := pairingTestHandler(t)
	older := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second)
	newer := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	recordPairingRequest(t, home, pairing.Request{
		Channel: "telegram", SenderID: "telegram:1", PlatformID: "1", DisplayName: "Bo",
		FirstSeen: older, LastSeen: older,
	})
	recordPairingRequest(t, home, pairing.Request{
		Channel: "telegram", SenderID: "telegram:2", PlatformID: "2", DisplayName: "Ana",
		FirstSeen: older, LastSeen: newer, Count: 2,
	})
	recordPairingRequest(t, home, pairing.Request{Channel: "discord", SenderID: "discord:9"})

	rec := pairingRequestJSON(t, mux, http.MethodGet, "/api/channels/telegram/pairing", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Requests []pairingRequestView `json:"requests"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(body.Requests) != 2 {
		t.Fatalf("requests = %+v, want the two telegram requests", body.Requests)
	}
	want := pairingRequestView{
		SenderID: "telegram:2", PlatformID: "2", DisplayName: "Ana",
		FirstSeen: older.Format(time.RFC3339), LastSeen: newer.Format(time.RFC3339), Count: 2,
	}
	if body.Requests[0] != want {
		t.Fatalf("first request = %+v, want %+v", body.Requests[0], want)
	}
	if body.Requests[1].SenderID != "telegram:1" {
		t.Fatalf("second request = %+v, want telegram:1", body.Requests[1])
	}
}

func TestPairingListEmptyIsAnEmptyArray(t *testing.T) {
	_, mux, _ := pairingTestHandler(t)
	rec := pairingRequestJSON(t, mux, http.MethodGet, "/api/channels/slack/pairing", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"requests":[]}` {
		t.Fatalf("body = %s, want an empty requests array", got)
	}
}

func TestPairingUnknownChannelIsNotFound(t *testing.T) {
	_, mux, _ := pairingTestHandler(t)
	for _, path := range []string{
		"/api/channels/nope/pairing",
		"/api/channels/nope/pairing/approve",
		"/api/channels/nope/pairing/deny",
	} {
		method := http.MethodPost
		if strings.HasSuffix(path, "/pairing") {
			method = http.MethodGet
		}
		rec := pairingRequestJSON(t, mux, method, path, `{"sender_id":"x"}`)
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Header().Get("Content-Type"), "application/json") {
			t.Fatalf("%s %s: status = %d, content type %q, want a JSON 404", method, path, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
}

func TestPairingApproveAppendsToAllowFromAndRemovesTheRequest(t *testing.T) {
	h, mux, home := pairingTestHandler(t, "telegram:1")
	recordPairingRequest(t, home, pairing.Request{Channel: "telegram", SenderID: "telegram:123", PlatformID: "123"})
	recordPairingRequest(t, home, pairing.Request{Channel: "telegram", SenderID: "telegram:456"})

	rec := pairingRequestJSON(t, mux, http.MethodPost, "/api/channels/telegram/pairing/approve", `{"sender_id":" telegram:123 "}`)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"status":"ok"}` {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if got := []string(cfg.Channels.Get("telegram").AllowFrom); strings.Join(got, ",") != "telegram:1,telegram:123" {
		t.Fatalf("allow_from = %v, want the approved sender appended", got)
	}
	requests, err := pairing.List(home, "telegram")
	if err != nil {
		t.Fatalf("pairing.List() error = %v", err)
	}
	if len(requests) != 1 || requests[0].SenderID != "telegram:456" {
		t.Fatalf("pending = %+v, want only the other sender", requests)
	}

	// The request is gone, so approving it again finds nothing.
	rec = pairingRequestJSON(t, mux, http.MethodPost, "/api/channels/telegram/pairing/approve", `{"sender_id":"telegram:123"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("second approval status = %d, want 404", rec.Code)
	}
}

func TestPairingApproveDoesNotDuplicateAnAllowedSender(t *testing.T) {
	h, mux, home := pairingTestHandler(t, "telegram:123")
	recordPairingRequest(t, home, pairing.Request{Channel: "telegram", SenderID: "telegram:123"})

	rec := pairingRequestJSON(t, mux, http.MethodPost, "/api/channels/telegram/pairing/approve", `{"sender_id":"telegram:123"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if got := []string(cfg.Channels.Get("telegram").AllowFrom); len(got) != 1 {
		t.Fatalf("allow_from = %v, want the sender once", got)
	}
	if requests, _ := pairing.List(home, "telegram"); len(requests) != 0 {
		t.Fatalf("pending = %+v, want none", requests)
	}
}

func TestPairingApproveWithoutARequestIsNotFound(t *testing.T) {
	h, mux, _ := pairingTestHandler(t)
	rec := pairingRequestJSON(t, mux, http.MethodPost, "/api/channels/telegram/pairing/approve", `{"sender_id":"telegram:999"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if got := cfg.Channels.Get("telegram").AllowFrom; len(got) != 0 {
		t.Fatalf("allow_from = %v, want it unchanged", got)
	}
}

func TestPairingDecisionNeedsASenderID(t *testing.T) {
	_, mux, _ := pairingTestHandler(t)
	for _, body := range []string{`{}`, `{"sender_id":"  "}`, `not json`} {
		rec := pairingRequestJSON(t, mux, http.MethodPost, "/api/channels/telegram/pairing/deny", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %q: status = %d, want 400", body, rec.Code)
		}
		var answer map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil || answer["error"] == "" {
			t.Fatalf("body %q: answer = %s, want a JSON error", body, rec.Body.String())
		}
	}
}

func TestPairingDenyRemovesTheRequestOnly(t *testing.T) {
	h, mux, home := pairingTestHandler(t)
	recordPairingRequest(t, home, pairing.Request{Channel: "telegram", SenderID: "telegram:123"})

	rec := pairingRequestJSON(t, mux, http.MethodPost, "/api/channels/telegram/pairing/deny", `{"sender_id":"telegram:123"}`)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"status":"ok"}` {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if requests, _ := pairing.List(home, "telegram"); len(requests) != 0 {
		t.Fatalf("pending = %+v, want none", requests)
	}
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if got := cfg.Channels.Get("telegram").AllowFrom; len(got) != 0 {
		t.Fatalf("allow_from = %v, want the denied sender left out", got)
	}
}
