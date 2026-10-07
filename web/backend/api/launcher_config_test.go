package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xibodev/compa/v3/web/backend/launcherconfig"
)

func TestGetLauncherConfigUsesRuntimeFallback(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	h.SetServerOptions(19999, true, false, []string{"192.168.1.0/24"})
	h.SetServerAccessOptions(false, []string{"10.0.0.0/8"})

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/system/launcher-config", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var got launcherConfigPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got.Port != 19999 || !got.Public {
		t.Fatalf("response = %+v, want port=19999 public=true", got)
	}
	if len(got.AllowedCIDRs) != 1 || got.AllowedCIDRs[0] != "192.168.1.0/24" {
		t.Fatalf("response allowed_cidrs = %v, want [192.168.1.0/24]", got.AllowedCIDRs)
	}
	if got.AllowLocalhostBypass {
		t.Fatalf("response allow_localhost_bypass = true, want false")
	}
	if len(got.TrustedProxyCIDRs) != 1 || got.TrustedProxyCIDRs[0] != "10.0.0.0/8" {
		t.Fatalf("response trusted_proxy_cidrs = %v, want [10.0.0.0/8]", got.TrustedProxyCIDRs)
	}
}

func TestPutLauncherConfigPersists(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	path := launcherconfig.PathForAppConfig(configPath)
	if err := os.WriteFile(
		path,
		[]byte(`{"port":18800,"public":false,"dashboard_password_hash":"saved-hash"}`),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	h := NewHandler(configPath)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPut,
		"/api/system/launcher-config",
		strings.NewReader(
			`{"port":18080,"public":true,"allowed_cidrs":["192.168.1.0/24"],"allow_localhost_bypass":false,"trusted_proxy_cidrs":["10.0.0.0/8"]}`,
		),
	)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	cfg, err := launcherconfig.Load(path, launcherconfig.Default())
	if err != nil {
		t.Fatalf("launcherconfig.Load() error = %v", err)
	}
	if cfg.Port != 18080 || !cfg.Public {
		t.Fatalf("saved config = %+v, want port=18080 public=true", cfg)
	}
	if cfg.DashboardPasswordHash != "saved-hash" {
		t.Fatalf("saved dashboard_password_hash = %q, want saved-hash", cfg.DashboardPasswordHash)
	}
	if len(cfg.AllowedCIDRs) != 1 || cfg.AllowedCIDRs[0] != "192.168.1.0/24" {
		t.Fatalf("saved config allowed_cidrs = %v, want [192.168.1.0/24]", cfg.AllowedCIDRs)
	}
	if cfg.AllowLocalhostBypass {
		t.Fatalf("saved config allow_localhost_bypass = true, want false")
	}
	if len(cfg.TrustedProxyCIDRs) != 1 || cfg.TrustedProxyCIDRs[0] != "10.0.0.0/8" {
		t.Fatalf("saved config trusted_proxy_cidrs = %v, want [10.0.0.0/8]", cfg.TrustedProxyCIDRs)
	}
}

func TestPutLauncherConfigUsesDirectAccessFields(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPut,
		"/api/system/launcher-config",
		strings.NewReader(
			`{"port":18080,"public":false,"allowed_cidrs":["192.168.1.0/24"],"allow_localhost_bypass":true,"trusted_proxy_cidrs":["10.0.0.0/8"]}`,
		),
	)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	cfg, err := launcherconfig.Load(launcherconfig.PathForAppConfig(configPath), launcherconfig.Default())
	if err != nil {
		t.Fatalf("launcherconfig.Load() error = %v", err)
	}
	if !cfg.AllowLocalhostBypass {
		t.Fatal("saved config allow_localhost_bypass = false, want true")
	}
	if len(cfg.TrustedProxyCIDRs) != 1 || cfg.TrustedProxyCIDRs[0] != "10.0.0.0/8" {
		t.Fatalf("saved config trusted_proxy_cidrs = %v, want [10.0.0.0/8]", cfg.TrustedProxyCIDRs)
	}
}

func TestPutLauncherConfigKeepsLocalhostBypassWhenOmitted(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	path := launcherconfig.PathForAppConfig(configPath)
	if err := os.WriteFile(
		path,
		[]byte(`{"port":18800,"public":false,"allow_localhost_bypass":true,"trusted_proxy_cidrs":["10.0.0.0/8"]}`),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	h := NewHandler(configPath)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPut,
		"/api/system/launcher-config",
		strings.NewReader(`{"port":18080,"public":true,"allowed_cidrs":["192.168.1.0/24"]}`),
	)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	cfg, err := launcherconfig.Load(path, launcherconfig.Default())
	if err != nil {
		t.Fatalf("launcherconfig.Load() error = %v", err)
	}
	if !cfg.AllowLocalhostBypass {
		t.Fatal("saved config allow_localhost_bypass = false, want true")
	}
}

func TestPutLauncherConfigRejectsInvalidPort(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPut,
		"/api/system/launcher-config",
		strings.NewReader(`{"port":70000,"public":false}`),
	)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestPutLauncherConfigRejectsInvalidCIDR(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPut,
		"/api/system/launcher-config",
		strings.NewReader(`{"port":18080,"public":false,"allowed_cidrs":["bad-cidr"]}`),
	)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestPutLauncherConfigRejectsInvalidTrustedProxyCIDR(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPut,
		"/api/system/launcher-config",
		strings.NewReader(`{"port":18080,"public":false,"trusted_proxy_cidrs":["bad-cidr"]}`),
	)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestLauncherConfigCarriesHostsLANAndRemoteImages(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	put := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/api/system/launcher-config", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		mux.ServeHTTP(rec, req)
		return rec
	}
	get := func() launcherConfigPayload {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/system/launcher-config", nil))
		var got launcherConfigPayload
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("unmarshal %s: %v", rec.Body.String(), err)
		}
		return got
	}

	if got := get(); got.RemoteImages != launcherconfig.RemoteImagesClick || got.AllowLANWithoutPassword || len(got.AllowedHosts) != 0 {
		t.Fatalf("defaults = %+v, want remote_images click, no LAN without password, no hosts", got)
	}
	if rec := put(`{"port":18800,"allowed_hosts":["Compa.Example.TEST:443"," "],"allow_lan_without_password":true,"remote_images":"always"}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d %s", rec.Code, rec.Body.String())
	}
	got := get()
	if len(got.AllowedHosts) != 1 || got.AllowedHosts[0] != "compa.example.test" || !got.AllowLANWithoutPassword || got.RemoteImages != launcherconfig.RemoteImagesAlways {
		t.Fatalf("after PUT = %+v", got)
	}

	// A client that does not know the fields keeps them.
	if rec := put(`{"port":18801}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT without the fields = %d %s", rec.Code, rec.Body.String())
	}
	if got := get(); len(got.AllowedHosts) != 1 || !got.AllowLANWithoutPassword || got.RemoteImages != launcherconfig.RemoteImagesAlways {
		t.Fatalf("a PUT without the fields changed them: %+v", got)
	}

	if rec := put(`{"port":18801,"remote_images":"sometimes"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT with an unknown remote_images = %d %s, want 400", rec.Code, rec.Body.String())
	}
}
