package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/xibodev/compa/pkg/config"
	ppid "github.com/xibodev/compa/pkg/pid"
)

func newWebChatProxyRequest(method, path string) *http.Request {
	req := httptest.NewRequest(method, "http://launcher.local:18800"+path, nil)
	req.Header.Set("Origin", "http://launcher.local:18800")
	return req
}

func TestEnsureWebChatChannel_FreshConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)

	changed, err := h.EnsureWebChatChannel()
	if err != nil {
		t.Fatalf("EnsureWebChatChannel() error = %v", err)
	}
	if !changed {
		t.Fatal("EnsureWebChatChannel() should report changed on a fresh config")
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	bc := cfg.Channels["web"]
	decoded, err := bc.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	webChatCfg := decoded.(*config.WebChatSettings)
	if !bc.Enabled {
		t.Error("expected the web channel to be enabled after setup")
	}
	if webChatCfg.Token.String() == "" {
		t.Error("expected a non-empty token after setup")
	}
}

func TestEnsureWebChatChannel_DoesNotEnableTokenQuery(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)

	if _, err := h.EnsureWebChatChannel(); err != nil {
		t.Fatalf("EnsureWebChatChannel() error = %v", err)
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	bc := cfg.Channels["web"]
	decoded, err := bc.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	webChatCfg := decoded.(*config.WebChatSettings)
	if webChatCfg.AllowTokenQuery {
		t.Error("setup must not enable allow_token_query by default")
	}
}

func TestEnsureWebChatChannel_LeavesAllowOriginsEmptyByDefault(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)

	if _, err := h.EnsureWebChatChannel(); err != nil {
		t.Fatalf("EnsureWebChatChannel() error = %v", err)
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	bc := cfg.Channels["web"]
	decoded, err := bc.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	webChatCfg := decoded.(*config.WebChatSettings)
	if len(webChatCfg.AllowOrigins) != 0 {
		t.Errorf("allow_origins = %v, want empty", webChatCfg.AllowOrigins)
	}
}

func TestEnsureWebChatChannel_NoOriginConfigurationRequired(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)

	if _, err := h.EnsureWebChatChannel(); err != nil {
		t.Fatalf("EnsureWebChatChannel() error = %v", err)
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	bc := cfg.Channels["web"]
	decoded, err := bc.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	webChatCfg := decoded.(*config.WebChatSettings)
	if len(webChatCfg.AllowOrigins) != 0 {
		t.Errorf("allow_origins = %v, want empty", webChatCfg.AllowOrigins)
	}
}

func TestEnsureWebChatChannel_PreservesUserSettings(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")

	// Pre-configure with custom user settings
	cfg := config.DefaultConfig()
	bc := cfg.Channels["web"]
	decoded, err := bc.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	webChatCfg := decoded.(*config.WebChatSettings)
	bc.Enabled = true
	webChatCfg.SetToken("user-custom-token")
	webChatCfg.AllowTokenQuery = true
	webChatCfg.AllowOrigins = []string{"https://myapp.example.com"}
	if err = config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)

	changed, err := h.EnsureWebChatChannel()
	if err != nil {
		t.Fatalf("EnsureWebChatChannel() error = %v", err)
	}
	if changed {
		t.Error("EnsureWebChatChannel() should not change a fully configured config")
	}

	cfg, err = config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	bc = cfg.Channels["web"]
	decoded, err = bc.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	webChatCfg = decoded.(*config.WebChatSettings)
	if webChatCfg.Token.String() != "user-custom-token" {
		t.Errorf("token = %q, want %q", webChatCfg.Token.String(), "user-custom-token")
	}
	if !webChatCfg.AllowTokenQuery {
		t.Error("user's allow_token_query=true must be preserved")
	}
	if len(webChatCfg.AllowOrigins) != 1 || webChatCfg.AllowOrigins[0] != "https://myapp.example.com" {
		t.Errorf("allow_origins = %v, want [https://myapp.example.com]", webChatCfg.AllowOrigins)
	}
}

func TestEnsureWebChatChannel_ExistingConfigWithoutSecurityFile(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")

	cfg := config.DefaultConfig()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if err = os.WriteFile(configPath, raw, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	h := NewHandler(configPath)

	changed, err := h.EnsureWebChatChannel()
	if err != nil {
		t.Fatalf("EnsureWebChatChannel() error = %v", err)
	}
	if !changed {
		t.Fatal("EnsureWebChatChannel() should report changed when web is missing")
	}

	cfg, err = config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	bc := cfg.Channels["web"]
	decoded, err := bc.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	webChatCfg := decoded.(*config.WebChatSettings)
	if !bc.Enabled {
		t.Error("expected the web channel to be enabled after setup")
	}
	if webChatCfg.Token.String() == "" {
		t.Error("expected a non-empty token after setup")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(configPath), config.SecurityConfigFile)); err != nil {
		t.Fatalf("expected .security.yml to be created: %v", err)
	}
}

func TestEnsureWebChatChannel_ConfiguresWebChatWithoutGateway(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ModelName = ""
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	if _, err := h.EnsureWebChatChannel(); err != nil {
		t.Fatalf("EnsureWebChatChannel() error = %v", err)
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	bc := cfg.Channels["web"]
	decoded, err := bc.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	webChatCfg := decoded.(*config.WebChatSettings)
	if !bc.Enabled {
		t.Error("expected the web channel to be enabled after launcher startup setup")
	}
	if webChatCfg.Token.String() == "" {
		t.Error("expected a non-empty token after launcher startup setup")
	}
}

func TestEnsureWebChatChannel_Idempotent(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)

	// First call sets things up
	if _, err := h.EnsureWebChatChannel(); err != nil {
		t.Fatalf("first EnsureWebChatChannel() error = %v", err)
	}

	cfg1, _ := config.LoadConfig(configPath)
	bc := cfg1.Channels["web"]
	decoded, err := bc.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	webChatCfg := decoded.(*config.WebChatSettings)
	token1 := webChatCfg.Token.String()

	// Second call should be a no-op
	changed, err := h.EnsureWebChatChannel()
	if err != nil {
		t.Fatalf("second EnsureWebChatChannel() error = %v", err)
	}
	if changed {
		t.Error("second EnsureWebChatChannel() should not report changed")
	}

	cfg2, _ := config.LoadConfig(configPath)
	bc = cfg2.Channels["web"]
	decoded, err = bc.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	webChatCfg = decoded.(*config.WebChatSettings)
	if webChatCfg.Token.String() != token1 {
		t.Error("token should not change on subsequent calls")
	}
}

func TestHandleWebSocketProxyReloadsGatewayTargetFromConfig(t *testing.T) {
	origMatcher := gatewayProcessMatcher
	gatewayProcessMatcher = func(int) (bool, bool) { return true, true }
	t.Cleanup(func() { gatewayProcessMatcher = origMatcher })

	home := t.TempDir()
	t.Setenv("COMPA_HOME", home)

	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	handler := h.handleWebSocketProxy()

	server1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/web/ws" {
			t.Fatalf("server1 path = %q, want %q", r.URL.Path, "/web/ws")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "server1")
	}))
	defer server1.Close()

	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/web/ws" {
			t.Fatalf("server2 path = %q, want %q", r.URL.Path, "/web/ws")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "server2")
	}))
	defer server2.Close()

	cfg := config.DefaultConfig()
	cfg.Gateway.Host = "127.0.0.1"
	cfg.Gateway.Port = mustGatewayTestPort(t, server1.URL)
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	cmd := startGatewayLikeProcess(t)
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})
	writeTestPidFile(t, ppid.PidFileData{
		PID:   cmd.Process.Pid,
		Token: "test-token",
		Host:  cfg.Gateway.Host,
		Port:  cfg.Gateway.Port,
	})
	origPidData := gateway.pidData
	origWebChatToken := gateway.webChatToken
	t.Cleanup(func() {
		ppid.RemovePidFile(globalConfigDir())
		gateway.pidData = origPidData
		gateway.webChatToken = origWebChatToken
	})

	gateway.pidData = &ppid.PidFileData{}
	gateway.webChatToken = "web"
	req1 := newWebChatProxyRequest(http.MethodGet, "/web/ws")
	rec1 := httptest.NewRecorder()
	handler(rec1, req1)

	if rec1.Code != http.StatusOK {
		t.Fatalf("first status = %d, want %d", rec1.Code, http.StatusOK)
	}
	if body := rec1.Body.String(); body != "server1" {
		t.Fatalf("first body = %q, want %q", body, "server1")
	}

	cfg.Gateway.Port = mustGatewayTestPort(t, server2.URL)
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	req2 := newWebChatProxyRequest(http.MethodGet, "/web/ws")
	rec2 := httptest.NewRecorder()
	handler(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("second status = %d, want %d", rec2.Code, http.StatusOK)
	}
	if body := rec2.Body.String(); body != "server2" {
		t.Fatalf("second body = %q, want %q", body, "server2")
	}
}

func TestHandleWebSocketProxyLoadsCachedWebChatTokenWhenMissing(t *testing.T) {
	origMatcher := gatewayProcessMatcher
	gatewayProcessMatcher = func(int) (bool, bool) { return true, true }
	t.Cleanup(func() { gatewayProcessMatcher = origMatcher })

	home := t.TempDir()
	t.Setenv("COMPA_HOME", home)

	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	handler := h.handleWebSocketProxy()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/web/ws" {
			t.Fatalf("path = %q, want %q", r.URL.Path, "/web/ws")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "proxied")
	}))
	defer server.Close()

	cfg := config.DefaultConfig()
	cfg.Gateway.Host = "127.0.0.1"
	cfg.Gateway.Port = mustGatewayTestPort(t, server.URL)
	bc := cfg.Channels["web"]
	decoded, err := bc.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	webChatCfg := decoded.(*config.WebChatSettings)
	bc.Enabled = true
	webChatCfg.SetToken("cached-token")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	cmd := startGatewayLikeProcess(t)
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})
	writeTestPidFile(t, ppid.PidFileData{
		PID:   cmd.Process.Pid,
		Token: "test-token",
		Host:  cfg.Gateway.Host,
		Port:  cfg.Gateway.Port,
	})
	t.Cleanup(func() {
		ppid.RemovePidFile(globalConfigDir())
	})

	origPidData := gateway.pidData
	origWebChatToken := gateway.webChatToken
	t.Cleanup(func() {
		gateway.pidData = origPidData
		gateway.webChatToken = origWebChatToken
	})

	gateway.pidData = &ppid.PidFileData{}
	gateway.webChatToken = ""

	req := newWebChatProxyRequest(http.MethodGet, "/web/ws?session_id=test-session")
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if body := rec.Body.String(); body != "proxied" {
		t.Fatalf("body = %q, want %q", body, "proxied")
	}
	if gateway.webChatToken != "cached-token" {
		t.Fatalf("gateway.webChatToken = %q, want %q", gateway.webChatToken, "cached-token")
	}
}

func TestHandleWebSocketProxyLoadsPidDataOnDemand(t *testing.T) {
	origMatcher := gatewayProcessMatcher
	gatewayProcessMatcher = func(int) (bool, bool) { return true, true }
	t.Cleanup(func() { gatewayProcessMatcher = origMatcher })

	home := t.TempDir()
	t.Setenv("COMPA_HOME", home)

	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	handler := h.handleWebSocketProxy()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/web/ws" {
			t.Fatalf("path = %q, want %q", r.URL.Path, "/web/ws")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, r.Header.Get(protocolKey))
	}))
	defer server.Close()

	cfg := config.DefaultConfig()
	cfg.Gateway.Host = "127.0.0.1"
	cfg.Gateway.Port = mustGatewayTestPort(t, server.URL)
	bc := cfg.Channels["web"]
	bc.Enabled = true
	decoded, err := bc.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	decoded.(*config.WebChatSettings).SetToken("ui-token")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	cmd := startGatewayLikeProcess(t)
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})
	pidData := ppid.PidFileData{
		PID:   cmd.Process.Pid,
		Token: "test-token",
		Host:  cfg.Gateway.Host,
		Port:  cfg.Gateway.Port,
	}
	writeTestPidFile(t, pidData)
	t.Cleanup(func() {
		ppid.RemovePidFile(globalConfigDir())
	})

	origPidData := gateway.pidData
	origWebChatToken := gateway.webChatToken
	origStatus := gateway.runtimeStatus
	t.Cleanup(func() {
		gateway.mu.Lock()
		gateway.pidData = origPidData
		gateway.webChatToken = origWebChatToken
		gateway.runtimeStatus = origStatus
		gateway.mu.Unlock()
	})

	gateway.mu.Lock()
	gateway.pidData = nil
	gateway.webChatToken = ""
	setGatewayRuntimeStatusLocked("stopped")
	gateway.mu.Unlock()

	req := newWebChatProxyRequest(http.MethodGet, "/web/ws?session_id=test-session")
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	expected := tokenPrefix + "ui-token"
	if got := rec.Body.String(); got != expected {
		t.Fatalf("forwarded protocol = %q, want %q", got, expected)
	}

	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	if gateway.pidData == nil {
		t.Fatal("gateway.pidData should be loaded from pid file")
	}
	if gateway.runtimeStatus != "running" {
		t.Fatalf("runtimeStatus = %q, want %q", gateway.runtimeStatus, "running")
	}
}

func TestCreateWebChatHTTPProxyInjectsGatewayAuth(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)

	cfg := config.DefaultConfig()
	cfg.Gateway.Host = "127.0.0.1"
	cfg.Gateway.Port = 18790
	bc := cfg.Channels["web"]
	bc.Enabled = true
	decoded, err := bc.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	decoded.(*config.WebChatSettings).SetToken("ui-token")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	proxy := h.createWebChatHTTPProxy("ui-token")
	var capturedPath string
	var capturedAuth string
	proxy.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		capturedPath = req.URL.Path
		capturedAuth = req.Header.Get("Authorization")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("proxied")),
			Request:    req,
		}, nil
	})

	req := httptest.NewRequest(http.MethodGet, "/web/media/attachment-1", nil)
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if capturedPath != "/web/media/attachment-1" {
		t.Fatalf("capturedPath = %q, want %q", capturedPath, "/web/media/attachment-1")
	}
	expected := "Bearer ui-token"
	if capturedAuth != expected {
		t.Fatalf("Authorization = %q, want %q", capturedAuth, expected)
	}
}

func TestHandleWebChatMediaProxyUsesRawBearerToken(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMPA_HOME", home)

	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	handler := h.handleWebChatMediaProxy()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/web/media/attachment-1" {
			t.Fatalf("path = %q, want %q", r.URL.Path, "/web/media/attachment-1")
		}
		if got := r.Header.Get("Authorization"); got != "Bearer ui-token" {
			t.Fatalf("Authorization = %q, want %q", got, "Bearer ui-token")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "proxied-media")
	}))
	defer server.Close()

	cfg := config.DefaultConfig()
	cfg.Gateway.Host = "127.0.0.1"
	cfg.Gateway.Port = mustGatewayTestPort(t, server.URL)
	bc := cfg.Channels["web"]
	bc.Enabled = true
	decoded, err := bc.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	decoded.(*config.WebChatSettings).SetToken("ui-token")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	cmd := startGatewayLikeProcess(t)
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})

	origPidData := gateway.pidData
	origWebChatToken := gateway.webChatToken
	origCmd := gateway.cmd
	t.Cleanup(func() {
		gateway.mu.Lock()
		gateway.pidData = origPidData
		gateway.webChatToken = origWebChatToken
		gateway.cmd = origCmd
		gateway.mu.Unlock()
	})

	gateway.mu.Lock()
	gateway.pidData = &ppid.PidFileData{PID: cmd.Process.Pid}
	gateway.webChatToken = "ui-token"
	gateway.cmd = cmd
	gateway.mu.Unlock()

	req := newWebChatProxyRequest(http.MethodGet, "/web/media/attachment-1")
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if body := rec.Body.String(); body != "proxied-media" {
		t.Fatalf("body = %q, want %q", body, "proxied-media")
	}
}

func TestHandleWebSocketProxyRejectsStalePidDataAfterProcessExit(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("COMPA_HOME", filepath.Join(tmpDir, ".compa"))

	configPath := filepath.Join(tmpDir, "config.json")
	h := NewHandler(configPath)
	handler := h.handleWebSocketProxy()

	cfg := config.DefaultConfig()
	bc := cfg.Channels["web"]
	bc.Enabled = true
	decoded, err := bc.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	decoded.(*config.WebChatSettings).SetToken("ui-token")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	cmd := startLongRunningProcess(t)
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	_ = cmd.Wait()

	origPidData := gateway.pidData
	origWebChatToken := gateway.webChatToken
	origCmd := gateway.cmd
	origStatus := gateway.runtimeStatus
	t.Cleanup(func() {
		gateway.mu.Lock()
		gateway.pidData = origPidData
		gateway.webChatToken = origWebChatToken
		gateway.cmd = origCmd
		gateway.runtimeStatus = origStatus
		gateway.mu.Unlock()
	})

	gateway.mu.Lock()
	gateway.pidData = &ppid.PidFileData{PID: cmd.Process.Pid, Token: "stale-token"}
	gateway.webChatToken = "ui-token"
	gateway.cmd = cmd
	setGatewayRuntimeStatusLocked("running")
	gateway.mu.Unlock()

	req := newWebChatProxyRequest(http.MethodGet, "/web/ws?session_id=test-session")
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	if gateway.pidData != nil {
		t.Fatal("gateway.pidData should be cleared after stale process exit is detected")
	}
}

func TestHandleWebSocketProxy_AllowsArbitraryOrigin(t *testing.T) {
	origMatcher := gatewayProcessMatcher
	gatewayProcessMatcher = func(int) (bool, bool) { return true, true }
	t.Cleanup(func() { gatewayProcessMatcher = origMatcher })

	home := t.TempDir()
	t.Setenv("COMPA_HOME", home)

	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	handler := h.handleWebSocketProxy()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/web/ws" {
			t.Fatalf("path = %q, want %q", r.URL.Path, "/web/ws")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "proxied")
	}))
	defer server.Close()

	cfg := config.DefaultConfig()
	cfg.Gateway.Host = "127.0.0.1"
	cfg.Gateway.Port = mustGatewayTestPort(t, server.URL)
	bc := cfg.Channels["web"]
	bc.Enabled = true
	decoded, err := bc.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	decoded.(*config.WebChatSettings).SetToken("ui-token")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	cmd := startGatewayLikeProcess(t)
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})
	writeTestPidFile(t, ppid.PidFileData{
		PID:   cmd.Process.Pid,
		Token: "test-token",
		Host:  cfg.Gateway.Host,
		Port:  cfg.Gateway.Port,
	})
	t.Cleanup(func() {
		ppid.RemovePidFile(globalConfigDir())
	})

	origPidData := gateway.pidData
	origWebChatToken := gateway.webChatToken
	t.Cleanup(func() {
		gateway.pidData = origPidData
		gateway.webChatToken = origWebChatToken
	})

	gateway.pidData = &ppid.PidFileData{}
	gateway.webChatToken = "ui-token"

	req := httptest.NewRequest(http.MethodGet, "http://launcher.local/web/ws?session_id=test-session", nil)
	req.Header.Set("Origin", "http://evil.example")
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func mustGatewayTestPort(t *testing.T, rawURL string) int {
	t.Helper()

	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}

	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatalf("Atoi(%q) error = %v", parsed.Port(), err)
	}

	return port
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}
