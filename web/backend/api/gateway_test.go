package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xibodev/compa/pkg/config"
	ppid "github.com/xibodev/compa/pkg/pid"
	"github.com/xibodev/compa/web/backend/utils"
)

func startLongRunningProcess(t *testing.T) *exec.Cmd {
	t.Helper()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("powershell", "-NoProfile", "-Command", "Start-Sleep -Seconds 30")
	} else {
		cmd = exec.Command("sleep", "30")
	}

	if err := cmd.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	return cmd
}

func startGatewayLikeProcess(t *testing.T) *exec.Cmd {
	t.Helper()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		t.Skip("gateway-like process commandline check is not deterministic on Windows tests")
	}
	cmd = exec.Command("sh", "-c", "sleep 30 # compa-kernel gateway")

	if err := cmd.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	return cmd
}

func writeTestPidFile(t *testing.T, data ppid.PidFileData) string {
	t.Helper()

	path := filepath.Join(globalConfigDir(), ".compa.pid")
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		t.Fatalf("marshal pid file: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write pid file: %v", err)
	}
	return path
}

func mockGatewayHealthResponse(statusCode, pid int) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Body: io.NopCloser(strings.NewReader(
			`{"status":"ok","uptime":"1s","pid":` + strconv.Itoa(pid) + `}`,
		)),
	}
}

func startIgnoringTermProcess(t *testing.T) *exec.Cmd {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("TERM handling differs on Windows")
	}

	cmd := exec.Command("sh", "-c", "trap '' TERM; sleep 30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	return cmd
}

func resetGatewayTestState(t *testing.T) {
	t.Helper()

	originalHealthGet := gatewayHealthGet
	originalProcessMatcher := gatewayProcessMatcher
	originalExecCommand := gatewayExecCommand
	originalStartProcess := gatewayStartProcess
	originalRestartGracePeriod := gatewayRestartGracePeriod
	originalRestartForceKillWindow := gatewayRestartForceKillWindow
	originalRestartPollInterval := gatewayRestartPollInterval
	t.Setenv("COMPA_HOME", t.TempDir())
	t.Cleanup(func() {
		gatewayHealthGet = originalHealthGet
		gatewayProcessMatcher = originalProcessMatcher
		gatewayExecCommand = originalExecCommand
		gatewayStartProcess = originalStartProcess
		gatewayRestartGracePeriod = originalRestartGracePeriod
		gatewayRestartForceKillWindow = originalRestartForceKillWindow
		gatewayRestartPollInterval = originalRestartPollInterval

		gateway.mu.Lock()
		gateway.cmd = nil
		gateway.pidData = nil
		gateway.owned = false
		gateway.bootDefaultModel = ""
		gateway.bootConfig = nil
		setGatewayRuntimeStatusLocked("stopped")
		gateway.mu.Unlock()
	})
}

func TestWebChatGatewayProtocol(t *testing.T) {
	resetGatewayTestState(t)

	gateway.mu.Lock()
	gateway.webChatToken = "ui-token"
	gateway.mu.Unlock()

	if got := webChatGatewayProtocol(); got != tokenPrefix+"ui-token" {
		t.Fatalf("webChatGatewayProtocol() = %q, want %q", got, tokenPrefix+"ui-token")
	}
}

type gatewayStartEnvSnapshot struct {
	GatewayHost    string `json:"gateway_host"`
	GatewayHostSet bool   `json:"gateway_host_set"`
	ConfigPath     string `json:"config_path"`
}

// TestGatewayStartHelperProcess is the child the start tests spawn: it idles
// until killed, so the gateway's exit watcher never races the test.
func TestGatewayStartHelperProcess(t *testing.T) {
	helper := false
	for i, arg := range os.Args {
		if arg == "--" && i+1 < len(os.Args) && os.Args[i+1] == "gateway-idle-helper" {
			helper = true
			break
		}
	}
	if !helper {
		t.Skip("helper process")
	}
	time.Sleep(60 * time.Second)
	os.Exit(0)
}

// gatewayStartCapture records the command startGatewayLocked hands to the
// process starter. The environment is read from the prepared command itself,
// so nothing depends on child-process timing.
type gatewayStartCapture struct {
	mu   sync.Mutex
	cmds []*exec.Cmd
}

func (c *gatewayStartCapture) snapshot(t *testing.T) gatewayStartEnvSnapshot {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.cmds) == 0 {
		t.Fatal("gateway process was never started")
	}
	env := c.cmds[len(c.cmds)-1].Env
	var snapshot gatewayStartEnvSnapshot
	// Later entries win, matching how the OS resolves duplicate keys.
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		switch key {
		case config.EnvGatewayHost:
			snapshot.GatewayHost, snapshot.GatewayHostSet = value, true
		case config.EnvConfig:
			snapshot.ConfigPath = value
		}
	}
	return snapshot
}

func (c *gatewayStartCapture) started() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.cmds)
}

// captureGatewayStarts swaps in an idle child process and records every start.
// Cleanup detaches the child from the gateway state before killing it, so the
// exit watcher goroutine finds nothing to update.
func captureGatewayStarts(t *testing.T) *gatewayStartCapture {
	t.Helper()
	capture := &gatewayStartCapture{}
	gatewayExecCommand = func(_ string, _ ...string) *exec.Cmd {
		return exec.Command(os.Args[0], "-test.run=^TestGatewayStartHelperProcess$", "--", "gateway-idle-helper")
	}
	gatewayStartProcess = func(cmd *exec.Cmd) error {
		if err := cmd.Start(); err != nil {
			return err
		}
		capture.mu.Lock()
		capture.cmds = append(capture.cmds, cmd)
		capture.mu.Unlock()
		return nil
	}
	t.Cleanup(func() {
		capture.mu.Lock()
		cmds := append([]*exec.Cmd(nil), capture.cmds...)
		capture.mu.Unlock()
		gateway.mu.Lock()
		for _, cmd := range cmds {
			if gateway.cmd == cmd {
				gateway.cmd = nil
				gateway.bootDefaultModel = ""
				gateway.bootConfig = nil
			}
		}
		gateway.mu.Unlock()
		for _, cmd := range cmds {
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		}
	})
	return capture
}

func unsetGatewayStartEnvForTest(t *testing.T, key string) {
	t.Helper()

	prev, hadPrev := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("Unsetenv(%q) error = %v", key, err)
	}
	t.Cleanup(func() {
		if hadPrev {
			_ = os.Setenv(key, prev)
			return
		}
		_ = os.Unsetenv(key)
	})
}

func newGatewayStartTestHandler(t *testing.T) *Handler {
	t.Helper()
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	h.SetServerOptions(18800, false, false, nil)
	return h
}

func startGatewayAndCaptureCmd(t *testing.T, h *Handler) (gatewayStartEnvSnapshot, *exec.Cmd) {
	t.Helper()

	unsetGatewayStartEnvForTest(t, config.EnvGatewayHost)

	capture := captureGatewayStarts(t)

	gateway.mu.Lock()
	pid, err := h.startGatewayLocked("starting", 0)
	gateway.mu.Unlock()
	if err != nil {
		t.Fatalf("startGatewayLocked() error = %v", err)
	}
	if pid <= 0 {
		t.Fatalf("startGatewayLocked() pid = %d, want > 0", pid)
	}
	capture.mu.Lock()
	cmd := capture.cmds[len(capture.cmds)-1]
	capture.mu.Unlock()
	return capture.snapshot(t), cmd
}

// The launcher's listen host is the dashboard's, LAN mode included: the
// kernel keeps the host its own config names (loopback by default), so its
// port never opens to the network, and the dashboard proxies what browsers
// need. The kernel runs in the Compa home, wherever the launcher started.
func TestStartGatewayLocked_KeepsTheKernelOnItsConfiguredHost(t *testing.T) {
	for _, tc := range []struct {
		name   string
		launch func(h *Handler)
	}{
		{name: "explicit multi host", launch: func(h *Handler) { h.SetServerBindHost("127.0.0.1,::1", true) }},
		{name: "host from the environment", launch: func(h *Handler) { h.SetServerBindHost("::", true) }},
		{name: "public launcher", launch: func(h *Handler) { h.SetServerOptions(18800, true, true, nil) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newGatewayStartTestHandler(t)
			tc.launch(h)

			snapshot, cmd := startGatewayAndCaptureCmd(t, h)
			if snapshot.GatewayHostSet {
				t.Fatalf("gateway host env = %q, want none: the kernel binds its config's host", snapshot.GatewayHost)
			}
			if snapshot.ConfigPath != h.configPath {
				t.Fatalf("config env = %q, want %q", snapshot.ConfigPath, h.configPath)
			}
			if cmd.Dir != globalConfigDir() {
				t.Fatalf("kernel working directory = %q, want the Compa home %q", cmd.Dir, globalConfigDir())
			}
		})
	}
}

func TestStartGatewayLocked_UsesReloadedConfigForBootSignature(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	delete(cfg.Channels, "web")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	h.SetServerOptions(18800, false, false, nil)
	captureGatewayStarts(t)

	originalSignature := computeConfigSignature(cfg)
	pid, err := h.startGatewayLocked("starting", 0)
	if err != nil {
		t.Fatalf("startGatewayLocked() error = %v", err)
	}
	if pid <= 0 {
		t.Fatalf("startGatewayLocked() pid = %d, want > 0", pid)
	}

	gateway.mu.Lock()
	bootSignature := gateway.bootConfig
	gateway.mu.Unlock()

	updatedCfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	expectedSignature := computeConfigSignature(updatedCfg)
	if expectedSignature.equal(originalSignature) {
		t.Fatal("expected EnsureWebChatChannel() to change the config signature during gateway start")
	}
	if !bootSignature.equal(expectedSignature) {
		t.Fatalf("bootConfig = %q, want %q", bootSignature, expectedSignature)
	}
}

func TestGatewayStartConfigAllowsStartWithoutDefaultModel(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)

	cfg, err := h.gatewayStartConfig()
	if err != nil {
		t.Fatalf("gatewayStartConfig() error = %v, want a start without a default model", err)
	}
	if state, reason := h.defaultModelState(cfg); state != defaultModelNone || reason != "" {
		t.Fatalf("defaultModelState() = %q, %q, want %q", state, reason, defaultModelNone)
	}
}

func TestGatewayStartConfigRefusesConfigThatDoesNotLoad(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(`{"agents": `), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	h := NewHandler(configPath)

	if _, err := h.gatewayStartConfig(); err == nil || !strings.Contains(err.Error(), "failed to load config") {
		t.Fatalf("gatewayStartConfig() error = %v, want a config load failure", err)
	}
}

// gatewayModelTestConfig saves, under a fresh home, a config whose provider
// instance "owned" serves "chat" and "vision", instance "off" is disabled,
// and route "fast" targets owned/chat.
func gatewayModelTestConfig(t *testing.T, change func(cfg *config.Config)) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("COMPA_HOME", home)
	configPath := filepath.Join(home, "config.json")
	cfg := config.DefaultConfig()
	off := providerInstanceFixture("off", "https://off.example.test/v1")
	off.State = config.ProviderInstanceStateDisabled
	cfg.ProviderInstances = []*config.ProviderInstanceConfig{
		providerInstanceFixture("owned", "https://owned.example.test/v1"), off,
	}
	cfg.ModelRoutes = []*config.ModelRouteConfig{{Name: "fast", Targets: []string{"owned/chat"}}}
	if change != nil {
		change(cfg)
	}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	if err := saveCatalogs(&CatalogStore{Entries: map[string]*CatalogEntry{
		"owned": {ID: "owned", InstanceID: "owned", Provider: "openai", Models: []CatalogModel{{ID: "chat"}, {ID: "vision"}}},
		"off":   {ID: "off", InstanceID: "off", Provider: "openai", Models: []CatalogModel{{ID: "chat"}}},
	}}); err != nil {
		t.Fatalf("saveCatalogs() error = %v", err)
	}
	return configPath
}

func TestDefaultModelStateChecksTheSelectionResolves(t *testing.T) {
	tests := []struct {
		selection  string
		wantState  string
		wantReason string
	}{
		{selection: "", wantState: defaultModelNone},
		{selection: "owned/chat", wantState: defaultModelOK},
		{selection: "fast", wantState: defaultModelOK},
		{selection: "owned/missing", wantState: defaultModelUnavailable, wantReason: `model "missing" not found`},
		{selection: "ghost/chat", wantState: defaultModelUnavailable, wantReason: `provider instance "ghost" not found`},
		{selection: "off/chat", wantState: defaultModelUnavailable, wantReason: `provider instance "off" is disabled`},
		{selection: "no-such-route", wantState: defaultModelUnavailable, wantReason: "route was not found"},
	}
	for _, tc := range tests {
		t.Run(tc.selection, func(t *testing.T) {
			configPath := gatewayModelTestConfig(t, func(cfg *config.Config) {
				cfg.Agents.Defaults.ModelName = tc.selection
			})
			h := NewHandler(configPath)
			cfg, err := h.gatewayStartConfig()
			if err != nil {
				t.Fatalf("gatewayStartConfig() error = %v; the model must never block a start", err)
			}
			state, reason := h.defaultModelState(cfg)
			if state != tc.wantState || !strings.Contains(reason, tc.wantReason) || (tc.wantReason == "" && reason != "") {
				t.Fatalf("defaultModelState() = %q, %q, want %q with reason containing %q", state, reason, tc.wantState, tc.wantReason)
			}
		})
	}
}

func TestLooksLikeGatewayCommandLine(t *testing.T) {
	cases := []struct {
		name    string
		cmdline string
		want    bool
	}{
		{
			name:    "default compa-kernel gateway",
			cmdline: "/usr/local/bin/compa-kernel gateway -E",
			want:    true,
		},
		{
			name:    "renamed binary with gateway subcommand",
			cmdline: "/opt/bin/custom-claw gateway -E -d",
			want:    true,
		},
		{
			name:    "standalone gateway binary path",
			cmdline: "/opt/bin/gateway -E",
			want:    true,
		},
		{
			name:    "non gateway process",
			cmdline: "/bin/sleep 30",
			want:    false,
		},
		{
			name:    "gateway substring only",
			cmdline: "/opt/bin/gatewayd --serve",
			want:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := looksLikeGatewayCommandLine(tc.cmdline)
			if got != tc.want {
				t.Fatalf("looksLikeGatewayCommandLine(%q) = %v, want %v", tc.cmdline, got, tc.want)
			}
		})
	}
}

func TestValidateGatewayPidDataAcceptsHealthWhenMatcherInconclusive(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)

	const testPID = 34567
	pidData := &ppid.PidFileData{
		PID:  testPID,
		Host: "127.0.0.1",
		Port: 18790,
	}

	gatewayProcessMatcher = func(int) (bool, bool) { return false, false }
	gatewayHealthGet = func(string, time.Duration) (*http.Response, error) {
		return mockGatewayHealthResponse(http.StatusOK, testPID), nil
	}

	ok, decisive, reason := h.validateGatewayPidData(pidData, nil)
	if !ok {
		t.Fatalf("validateGatewayPidData() ok = false, want true (reason=%q)", reason)
	}
	if !decisive {
		t.Fatalf("validateGatewayPidData() decisive = false, want true")
	}
}

func TestValidateGatewayPidDataRejectsHealthPidMismatchWhenMatcherInconclusive(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)

	pidData := &ppid.PidFileData{
		PID:  34567,
		Host: "127.0.0.1",
		Port: 18790,
	}

	gatewayProcessMatcher = func(int) (bool, bool) { return false, false }
	gatewayHealthGet = func(string, time.Duration) (*http.Response, error) {
		return mockGatewayHealthResponse(http.StatusOK, 99999), nil
	}

	ok, decisive, reason := h.validateGatewayPidData(pidData, nil)
	if ok {
		t.Fatalf("validateGatewayPidData() ok = true, want false")
	}
	if !decisive {
		t.Fatalf("validateGatewayPidData() decisive = false, want true")
	}
	if !strings.Contains(reason, "health pid mismatch") {
		t.Fatalf("validateGatewayPidData() reason = %q, want contains %q", reason, "health pid mismatch")
	}
}

func TestGatewayCommandArgsIncludesDebugFlagWhenEnabled(t *testing.T) {
	h := NewHandler(filepath.Join(t.TempDir(), "config.json"))
	h.SetDebug(true)

	args := h.gatewayCommandArgs()
	want := []string{"gateway", "-E", "-d"}
	if strings.Join(args, " ") != strings.Join(want, " ") {
		t.Fatalf("gatewayCommandArgs() = %v, want %v", args, want)
	}
}

func gatewayStatusBody(t *testing.T, h *Handler) map[string]any {
	t.Helper()
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	return body
}

func TestGatewayStatusAllowsStartWithoutDefaultModel(t *testing.T) {
	resetGatewayTestState(t)
	configPath := filepath.Join(t.TempDir(), "config.json")
	body := gatewayStatusBody(t, NewHandler(configPath))

	if allowed, ok := body["gateway_start_allowed"].(bool); !ok || !allowed {
		t.Fatalf("gateway_start_allowed = %#v, want true without a default model", body["gateway_start_allowed"])
	}
	if _, exists := body["gateway_start_reason"]; exists {
		t.Fatalf("gateway_start_reason should be omitted when startup is allowed: %#v", body["gateway_start_reason"])
	}
	if _, exists := body["config_default_model"]; exists {
		t.Fatalf("config_default_model should be omitted without a default model: %#v", body["config_default_model"])
	}
	if got := body["config_default_model_status"]; got != defaultModelNone {
		t.Fatalf("config_default_model_status = %#v, want %q", got, defaultModelNone)
	}
	if _, exists := body["config_default_model_error"]; exists {
		t.Fatalf("config_default_model_error should be omitted: %#v", body["config_default_model_error"])
	}
}

func TestGatewayStatusReportsUnavailableDefaultModelWithoutBlockingStart(t *testing.T) {
	resetGatewayTestState(t)
	configPath := gatewayModelTestConfig(t, func(cfg *config.Config) {
		cfg.Agents.Defaults.ModelName = "owned/retired"
	})
	body := gatewayStatusBody(t, NewHandler(configPath))

	if allowed, ok := body["gateway_start_allowed"].(bool); !ok || !allowed {
		t.Fatalf("gateway_start_allowed = %#v, want true: the model never blocks a start", body["gateway_start_allowed"])
	}
	if got := body["config_default_model"]; got != "owned/retired" {
		t.Fatalf("config_default_model = %#v, want %q", got, "owned/retired")
	}
	if got := body["config_default_model_status"]; got != defaultModelUnavailable {
		t.Fatalf("config_default_model_status = %#v, want %q", got, defaultModelUnavailable)
	}
	if reason, _ := body["config_default_model_error"].(string); !strings.Contains(reason, `model "retired" not found`) {
		t.Fatalf("config_default_model_error = %#v, want the resolution error", body["config_default_model_error"])
	}
}

func TestGatewayStatusReportsResolvingDefaultModel(t *testing.T) {
	resetGatewayTestState(t)
	configPath := gatewayModelTestConfig(t, func(cfg *config.Config) {
		cfg.Agents.Defaults.ModelName = "fast"
	})
	body := gatewayStatusBody(t, NewHandler(configPath))

	if got := body["config_default_model"]; got != "fast" {
		t.Fatalf("config_default_model = %#v, want %q", got, "fast")
	}
	if got := body["config_default_model_status"]; got != defaultModelOK {
		t.Fatalf("config_default_model_status = %#v, want %q", got, defaultModelOK)
	}
}

func TestGatewayStatusDisallowsStartWhenConfigDoesNotLoad(t *testing.T) {
	resetGatewayTestState(t)
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(`{"agents": `), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	body := gatewayStatusBody(t, NewHandler(configPath))

	if allowed, ok := body["gateway_start_allowed"].(bool); !ok || allowed {
		t.Fatalf("gateway_start_allowed = %#v, want false", body["gateway_start_allowed"])
	}
	if reason, _ := body["gateway_start_reason"].(string); !strings.Contains(reason, "failed to load config") {
		t.Fatalf("gateway_start_reason = %#v, want the config load failure", body["gateway_start_reason"])
	}
	if _, exists := body["config_default_model_status"]; exists {
		t.Fatalf("config_default_model_status should be omitted without a config: %#v", body["config_default_model_status"])
	}
}

func TestGatewayStartStartsWithoutAUsableDefaultModel(t *testing.T) {
	h := newGatewayStartTestHandler(t)
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.Agents.Defaults.ModelName = "ghost/model"
	if err := config.SaveConfig(h.configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	unsetGatewayStartEnvForTest(t, config.EnvGatewayHost)
	capture := captureGatewayStarts(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/gateway/start", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("start status = %d, body = %s; the model must never block a start", rec.Code, rec.Body.String())
	}
	if capture.started() != 1 {
		t.Fatalf("gateway starts = %d, want 1", capture.started())
	}
}

func TestGatewayStatusKeepsRunningWhenHealthProbeFailsAfterRunning(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	cmd := startLongRunningProcess(t)
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})

	gateway.mu.Lock()
	gateway.cmd = cmd
	gateway.bootDefaultModel = "existing-model"
	// Simulate a process that has already reached the running state.
	setGatewayRuntimeStatusLocked("running")
	gateway.mu.Unlock()

	gatewayHealthGet = func(string, time.Duration) (*http.Response, error) {
		return nil, errors.New("probe failed")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if got := body["gateway_status"]; got != "running" {
		t.Fatalf("gateway_status = %#v, want %q", got, "running")
	}
}

func TestGatewayStatusKeepsPidDataWhileTrackedProcessAliveWhenPidFileUnavailable(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	cmd := startLongRunningProcess(t)
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})

	gateway.mu.Lock()
	gateway.cmd = cmd
	gateway.pidData = &ppid.PidFileData{
		PID:   cmd.Process.Pid,
		Token: "existing-token",
	}
	setGatewayRuntimeStatusLocked("running")
	gateway.mu.Unlock()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	if gateway.pidData == nil {
		t.Fatal("gateway.pidData was cleared while runtime status remained running")
	}
}

func TestGatewayStatusDowngradesRunningWhenTrackedProcessExitedAndPidFileMissing(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	cmd := startLongRunningProcess(t)
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	_ = cmd.Wait()

	gateway.mu.Lock()
	gateway.cmd = cmd
	gateway.pidData = &ppid.PidFileData{
		PID:   cmd.Process.Pid,
		Token: "stale-token",
	}
	setGatewayRuntimeStatusLocked("running")
	gateway.mu.Unlock()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got := body["gateway_status"]; got != "stopped" {
		t.Fatalf("gateway_status = %#v, want %q", got, "stopped")
	}

	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	if gateway.pidData != nil {
		t.Fatal("gateway.pidData should be cleared when tracked process has exited")
	}
}

func TestGatewayStatusIgnoresAndRemovesPidFileForNonGatewayProcess(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	cmd := startLongRunningProcess(t)
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})

	pidPath := writeTestPidFile(t, ppid.PidFileData{
		PID:   cmd.Process.Pid,
		Token: "stale-token",
		Host:  "127.0.0.1",
		Port:  18790,
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got := body["gateway_status"]; got != "stopped" {
		t.Fatalf("gateway_status = %#v, want %q", got, "stopped")
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Fatal("stale pid file should be removed for non-gateway process")
	}
}

func TestGatewayStopRefusesNonGatewayAttachedProcess(t *testing.T) {
	resetGatewayTestState(t)
	if runtime.GOOS == "windows" {
		t.Skip("commandline-based process type check is best-effort on Windows")
	}

	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	cmd := startLongRunningProcess(t)
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})

	gateway.mu.Lock()
	gateway.cmd = cmd
	gateway.owned = false
	setGatewayRuntimeStatusLocked("running")
	gateway.mu.Unlock()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/gateway/stop", nil)
	mux.ServeHTTP(rec, req)

	// Not a gateway: the conflict is reported and the process left alone.
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
	if !isCmdProcessAliveLocked(cmd) {
		t.Fatal("non-gateway process should not be terminated by /api/gateway/stop")
	}
}

func TestGatewayStatusReportsRunningFromPidProbe(t *testing.T) {
	resetGatewayTestState(t)
	gatewayProcessMatcher = func(int) (bool, bool) { return true, true }

	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	cmd := startGatewayLikeProcess(t)
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})

	gateway.mu.Lock()
	setGatewayRuntimeStatusLocked("stopped")
	gateway.mu.Unlock()

	gatewayHealthGet = func(string, time.Duration) (*http.Response, error) {
		return mockGatewayHealthResponse(http.StatusOK, cmd.Process.Pid), nil
	}

	writeTestPidFile(t, ppid.PidFileData{
		PID:   cmd.Process.Pid,
		Token: "test-token",
		Host:  "127.0.0.1",
		Port:  18790,
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if got := body["gateway_status"]; got != "running" {
		t.Fatalf("gateway_status = %#v, want %q", got, "running")
	}
	if got := body["gateway_restart_required"]; got != false {
		t.Fatalf("gateway_restart_required = %#v, want false", got)
	}
}

// runAsTrackedGateway makes the test process the gateway the launcher tracks,
// booted with cfg, and reports it through the PID file.
func runAsTrackedGateway(t *testing.T, cfg *config.Config) {
	t.Helper()
	gatewayProcessMatcher = func(int) (bool, bool) { return true, true }
	gatewayHealthGet = func(string, time.Duration) (*http.Response, error) {
		return mockGatewayHealthResponse(http.StatusOK, os.Getpid()), nil
	}
	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess() error = %v", err)
	}
	writeTestPidFile(t, ppid.PidFileData{PID: os.Getpid(), Token: "test-token", Host: "127.0.0.1", Port: 18790})
	gateway.mu.Lock()
	gateway.cmd = &exec.Cmd{Process: process}
	gateway.bootDefaultModel = strings.TrimSpace(cfg.Agents.Defaults.GetModelName())
	gateway.bootConfig = computeConfigSignature(cfg)
	setGatewayRuntimeStatusLocked("running")
	gateway.mu.Unlock()
}

func TestGatewayStatusRequiresRestartAfterDefaultModelChange(t *testing.T) {
	resetGatewayTestState(t)
	configPath := gatewayModelTestConfig(t, func(cfg *config.Config) {
		cfg.Agents.Defaults.ModelName = "owned/chat"
	})
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	runAsTrackedGateway(t, cfg)

	cfg.Agents.Defaults.ModelName = "fast"
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	body := gatewayStatusBody(t, NewHandler(configPath))

	if got := body["gateway_status"]; got != "running" {
		t.Fatalf("gateway_status = %#v, want %q", got, "running")
	}
	if got := body["boot_default_model"]; got != "owned/chat" {
		t.Fatalf("boot_default_model = %#v, want %q", got, "owned/chat")
	}
	if got := body["config_default_model"]; got != "fast" {
		t.Fatalf("config_default_model = %#v, want %q", got, "fast")
	}
	if got := body["config_default_model_status"]; got != defaultModelOK {
		t.Fatalf("config_default_model_status = %#v, want %q", got, defaultModelOK)
	}
	if got := body["gateway_restart_required"]; got != true {
		t.Fatalf("gateway_restart_required = %#v, want true", got)
	}
}

func TestGatewayStatusNeedsNoRestartForProviderChanges(t *testing.T) {
	resetGatewayTestState(t)
	configPath := gatewayModelTestConfig(t, func(cfg *config.Config) {
		cfg.Agents.Defaults.ModelName = "fast"
	})
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	runAsTrackedGateway(t, cfg)

	// The gateway resolves the unchanged selection per turn, so what it
	// resolves to may change under it: the instance's runtime settings, the
	// route's targets, the active models.
	streaming := true
	cfg.ProviderInstances[0].Runtime = &config.ProviderInstanceRuntime{Streaming: &streaming, RequestTimeout: 30}
	cfg.ModelRoutes[0].Targets = []string{"owned/vision", "owned/chat"}
	cfg.ActiveModels = []string{"owned/chat"}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	body := gatewayStatusBody(t, NewHandler(configPath))

	if got := body["gateway_status"]; got != "running" {
		t.Fatalf("gateway_status = %#v, want %q", got, "running")
	}
	if got := body["gateway_restart_required"]; got != false {
		t.Fatalf("gateway_restart_required = %#v, want false", got)
	}
}

func TestConfigSignatureTracksModelSelections(t *testing.T) {
	base := func() *config.Config {
		cfg := config.DefaultConfig()
		cfg.Agents.Defaults.ModelName = "owned/chat"
		cfg.Agents.Defaults.ImageModel = "owned/vision"
		cfg.Agents.Defaults.Routing = &config.RoutingConfig{Enabled: true, LightModel: "owned/mini", Threshold: 0.4}
		cfg.Agents.List = []config.AgentConfig{{ID: "helper", Model: "fast"}}
		return cfg
	}
	boot := computeConfigSignature(base())

	changes := map[string]func(cfg *config.Config){
		"default model":   func(cfg *config.Config) { cfg.Agents.Defaults.ModelName = "fast" },
		"cleared default": func(cfg *config.Config) { cfg.Agents.Defaults.ModelName = "" },
		"image model":     func(cfg *config.Config) { cfg.Agents.Defaults.ImageModel = "" },
		"light model":     func(cfg *config.Config) { cfg.Agents.Defaults.Routing.LightModel = "owned/chat" },
		"routing off":     func(cfg *config.Config) { cfg.Agents.Defaults.Routing.Enabled = false },
		"threshold":       func(cfg *config.Config) { cfg.Agents.Defaults.Routing.Threshold = 0.6 },
		"agent model":     func(cfg *config.Config) { cfg.Agents.List[0].Model = "owned/chat" },
	}
	for name, change := range changes {
		cfg := base()
		change(cfg)
		if computeConfigSignature(cfg).equal(boot) {
			t.Errorf("changing the %s left the config signature unchanged", name)
		}
	}

	same := base()
	same.Agents.Defaults.ModelName = "  owned/chat  "
	same.ProviderInstances = []*config.ProviderInstanceConfig{providerInstanceFixture("owned", "https://owned.example.test/v1")}
	same.ModelRoutes = []*config.ModelRouteConfig{{Name: "fast", Targets: []string{"owned/chat"}}}
	if !computeConfigSignature(same).equal(boot) {
		t.Error("provider instances, routes or selection whitespace changed the config signature")
	}
}

// A channel's secrets are in its signature; its access lists and policies
// are its live part, and nothing else.
func TestConfigSignatureTracksChannelSecretsAndAccess(t *testing.T) {
	withToken := func(token string) *config.Config {
		cfg := config.DefaultConfig()
		decoded, err := cfg.Channels["web"].GetDecoded()
		if err != nil {
			t.Fatalf("GetDecoded() error = %v", err)
		}
		decoded.(*config.WebChatSettings).Token = *config.NewSecureString(token)
		return cfg
	}
	boot := computeConfigSignature(withToken("old-token"))

	if computeConfigSignature(withToken("new-token")).equalBesidesLive(boot) {
		t.Error("changing a channel's secret left the config signature unchanged")
	}

	access := withToken("old-token")
	access.Channels["web"].AllowFrom = config.FlexibleStringSlice{"web:1"}
	if signature := computeConfigSignature(access); signature.liveEqual(boot) || !signature.equalBesidesLive(boot) {
		t.Error("changing a channel's allow_from changed other than the live part of the config signature")
	}
}

func TestGatewayStatusRequiresRestartAfterToolChange(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ModelName = "owned/chat"
	cfg.Tools.WriteFile.Enabled = true
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess() error = %v", err)
	}

	bootSignature := computeConfigSignature(cfg)
	gateway.mu.Lock()
	gateway.cmd = &exec.Cmd{Process: process}
	gateway.bootDefaultModel = cfg.Agents.Defaults.ModelName
	gateway.bootConfig = bootSignature
	setGatewayRuntimeStatusLocked("running")
	gateway.mu.Unlock()

	updatedCfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	updatedCfg.Tools.WriteFile.Enabled = false
	if err := config.SaveConfig(configPath, updatedCfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	gatewayHealthGet = func(string, time.Duration) (*http.Response, error) {
		return mockGatewayHealthResponse(http.StatusOK, os.Getpid()), nil
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if got := body["gateway_status"]; got != "running" {
		t.Fatalf("gateway_status = %#v, want %q", got, "running")
	}
	if got := body["gateway_restart_required"]; got != true {
		t.Fatalf("gateway_restart_required = %#v, want true", got)
	}
}

func TestGatewayStatusRequiresRestartAfterChannelChange(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ModelName = "owned/chat"
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess() error = %v", err)
	}

	bootSignature := computeConfigSignature(cfg)
	gateway.mu.Lock()
	gateway.cmd = &exec.Cmd{Process: process}
	gateway.bootDefaultModel = cfg.Agents.Defaults.ModelName
	gateway.bootConfig = bootSignature
	setGatewayRuntimeStatusLocked("running")
	gateway.mu.Unlock()

	updatedCfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	telegram := updatedCfg.Channels.Get("telegram")
	if telegram == nil {
		t.Fatalf("expected default telegram channel config")
	}
	telegram.Enabled = !telegram.Enabled
	if err := config.SaveConfig(configPath, updatedCfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	gatewayHealthGet = func(string, time.Duration) (*http.Response, error) {
		return mockGatewayHealthResponse(http.StatusOK, os.Getpid()), nil
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if got := body["gateway_status"]; got != "running" {
		t.Fatalf("gateway_status = %#v, want %q", got, "running")
	}
	if got := body["gateway_restart_required"]; got != true {
		t.Fatalf("gateway_restart_required = %#v, want true", got)
	}
}

func TestGatewayStatusRequiresRestartAfterWebSearchConfigChange(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ModelName = "owned/chat"
	cfg.Tools.Web.Enabled = true
	cfg.Tools.Web.Provider = "sogou"
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess() error = %v", err)
	}

	bootSignature := computeConfigSignature(cfg)
	gateway.mu.Lock()
	gateway.cmd = &exec.Cmd{Process: process}
	gateway.bootDefaultModel = cfg.Agents.Defaults.ModelName
	gateway.bootConfig = bootSignature
	setGatewayRuntimeStatusLocked("running")
	gateway.mu.Unlock()

	updatedCfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	updatedCfg.Tools.Web.Provider = "duckduckgo"
	if err := config.SaveConfig(configPath, updatedCfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	gatewayHealthGet = func(string, time.Duration) (*http.Response, error) {
		return mockGatewayHealthResponse(http.StatusOK, os.Getpid()), nil
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if got := body["gateway_status"]; got != "running" {
		t.Fatalf("gateway_status = %#v, want %q", got, "running")
	}
	if got := body["gateway_restart_required"]; got != true {
		t.Fatalf("gateway_restart_required = %#v, want true", got)
	}
}

func TestGatewayStatusNoRestartRequiredForNonSensitiveChanges(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ModelName = "owned/chat"
	cfg.Agents.Defaults.MaxTokens = 1000
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess() error = %v", err)
	}

	bootSignature := computeConfigSignature(cfg)
	gateway.mu.Lock()
	gateway.cmd = &exec.Cmd{Process: process}
	gateway.bootDefaultModel = cfg.Agents.Defaults.ModelName
	gateway.bootConfig = bootSignature
	setGatewayRuntimeStatusLocked("running")
	gateway.mu.Unlock()

	updatedCfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	updatedCfg.Agents.Defaults.MaxTokens = 2000
	if err := config.SaveConfig(configPath, updatedCfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	gatewayHealthGet = func(string, time.Duration) (*http.Response, error) {
		return mockGatewayHealthResponse(http.StatusOK, os.Getpid()), nil
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if got := body["gateway_status"]; got != "running" {
		t.Fatalf("gateway_status = %#v, want %q", got, "running")
	}
	if got := body["gateway_restart_required"]; got != false {
		t.Fatalf("gateway_restart_required = %#v, want false", got)
	}
}

func TestGatewayStatusNoRestartRequiredWhenNotRunning(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ModelName = "owned/chat"
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	gateway.mu.Lock()
	gateway.cmd = nil
	gateway.bootDefaultModel = ""
	gateway.bootConfig = nil
	setGatewayRuntimeStatusLocked("stopped")
	gateway.mu.Unlock()

	updatedCfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	updatedCfg.Agents.Defaults.ModelName = "owned/other"
	if err := config.SaveConfig(configPath, updatedCfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	gatewayHealthGet = func(string, time.Duration) (*http.Response, error) {
		return nil, errors.New("no gateway running")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if got := body["gateway_status"]; got != "stopped" {
		t.Fatalf("gateway_status = %#v, want %q", got, "stopped")
	}
	if got := body["gateway_restart_required"]; got != false {
		t.Fatalf("gateway_restart_required = %#v, want false", got)
	}
}

func TestGatewayStatusReturnsErrorAfterStartupWindowExpires(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	cmd := startLongRunningProcess(t)
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})

	gateway.mu.Lock()
	gateway.cmd = cmd
	gateway.bootDefaultModel = "existing-model"
	setGatewayRuntimeStatusLocked("starting")
	gateway.startupDeadline = time.Now().Add(-time.Second)
	gateway.mu.Unlock()

	gatewayHealthGet = func(string, time.Duration) (*http.Response, error) {
		return nil, errors.New("probe failed")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if got := body["gateway_status"]; got != "error" {
		t.Fatalf("gateway_status = %#v, want %q", got, "error")
	}
}

func TestGatewayStatusReturnsRestartingDuringRestartGap(t *testing.T) {
	resetGatewayTestState(t)

	// Mock health check to return error, so it won't override our "restarting" status
	gatewayHealthGet = func(url string, timeout time.Duration) (*http.Response, error) {
		return nil, errors.New("mock health check error")
	}

	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	gateway.mu.Lock()
	setGatewayRuntimeStatusLocked("restarting")
	gateway.mu.Unlock()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if got := body["gateway_status"]; got != "restarting" {
		t.Fatalf("gateway_status = %#v, want %q", got, "restarting")
	}
}

func TestGatewayRestartKeepsRunningProcessWhenConfigDoesNotLoad(t *testing.T) {
	resetGatewayTestState(t)
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(`{"agents": `), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	cmd := startLongRunningProcess(t)
	t.Cleanup(func() {
		gateway.mu.Lock()
		if gateway.cmd == cmd {
			gateway.cmd = nil
			gateway.bootDefaultModel = ""
		}
		gateway.mu.Unlock()

		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})

	gateway.mu.Lock()
	gateway.cmd = cmd
	gateway.bootDefaultModel = "owned/chat"
	gateway.mu.Unlock()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/gateway/restart", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "failed to load config") {
		t.Fatalf("status = %d, body = %s, want %d with the config load failure", rec.Code, rec.Body.String(), http.StatusInternalServerError)
	}

	gateway.mu.Lock()
	stillRunning := gateway.cmd == cmd && isCmdProcessAliveLocked(cmd)
	gateway.mu.Unlock()

	if !stillRunning {
		t.Fatalf("gateway process was stopped although the config it would restart with does not load")
	}
}

func TestGatewayRestartProceedsWithoutAUsableDefaultModel(t *testing.T) {
	resetGatewayTestState(t)
	configPath := gatewayModelTestConfig(t, func(cfg *config.Config) {
		cfg.Agents.Defaults.ModelName = "off/chat"
	})
	h := NewHandler(configPath)
	h.SetServerOptions(18800, false, false, nil)
	unsetGatewayStartEnvForTest(t, config.EnvGatewayHost)
	capture := captureGatewayStarts(t)

	pid, err := h.RestartGateway()
	if err != nil || pid <= 0 {
		t.Fatalf("RestartGateway() = %d, %v; the model must never block a restart", pid, err)
	}
	if capture.started() != 1 {
		t.Fatalf("gateway starts = %d, want 1", capture.started())
	}
}

func TestGatewayRestartKeepsOldProcessWhenItDoesNotExitInTime(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ModelName = "owned/chat"
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	cmd := startIgnoringTermProcess(t)
	t.Cleanup(func() {
		gateway.mu.Lock()
		if gateway.cmd == cmd {
			gateway.cmd = nil
			gateway.bootDefaultModel = ""
		}
		gateway.mu.Unlock()

		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})

	gatewayRestartGracePeriod = 150 * time.Millisecond
	gatewayRestartForceKillWindow = 150 * time.Millisecond
	gatewayRestartPollInterval = 10 * time.Millisecond

	gateway.mu.Lock()
	gateway.cmd = cmd
	gateway.bootDefaultModel = "existing-model"
	setGatewayRuntimeStatusLocked("running")
	gateway.mu.Unlock()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/gateway/restart", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}

	gateway.mu.Lock()
	stillRunning := gateway.cmd == cmd && isCmdProcessAliveLocked(cmd)
	status := gateway.runtimeStatus
	gateway.mu.Unlock()

	if !stillRunning {
		t.Fatalf("gateway process was replaced before the old process exited")
	}
	if status != "running" {
		t.Fatalf("runtimeStatus = %q, want %q", status, "running")
	}
}

func TestGatewayRestartReturnsErrorStatusWhenReplacementFailsToStart(t *testing.T) {
	resetGatewayTestState(t)

	// Mock health check to return error, so it won't override our "error" status
	gatewayHealthGet = func(url string, timeout time.Duration) (*http.Response, error) {
		return nil, errors.New("mock health check error")
	}

	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ModelName = "owned/chat"
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	invalidBinaryPath := filepath.Join(t.TempDir(), "fake-compa")
	if err := os.WriteFile(invalidBinaryPath, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	t.Setenv("COMPA_BINARY", invalidBinaryPath)

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/gateway/restart", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("restart status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}

	statusRec := httptest.NewRecorder()
	statusReq := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(statusRec, statusReq)

	if statusRec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", statusRec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(statusRec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if got := body["gateway_status"]; got != "error" {
		t.Fatalf("gateway_status = %#v, want %q", got, "error")
	}
}

func TestGatewayStatusExcludesLogsFields(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if _, ok := body["logs"]; ok {
		t.Fatalf("logs unexpectedly present in status response: %#v", body["logs"])
	}
	if _, ok := body["log_total"]; ok {
		t.Fatalf("log_total unexpectedly present in status response: %#v", body["log_total"])
	}
	if _, ok := body["log_run_id"]; ok {
		t.Fatalf("log_run_id unexpectedly present in status response: %#v", body["log_run_id"])
	}
}

func TestGatewayLogsReturnsIncrementalHistory(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	gateway.logs.Clear()
	gateway.logs.Append("first line")
	gateway.logs.Append("second line")
	runID := gateway.logs.RunID()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodGet,
		"/api/gateway/logs?log_offset=1&log_run_id="+strconv.Itoa(runID),
		nil,
	)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("logs status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal logs response: %v", err)
	}

	logs, ok := body["logs"].([]any)
	if !ok {
		t.Fatalf("logs missing or not array: %#v", body["logs"])
	}
	if len(logs) != 1 || logs[0] != "second line" {
		t.Fatalf("logs = %#v, want [\"second line\"]", logs)
	}
	if got := body["log_total"]; got != float64(2) {
		t.Fatalf("log_total = %#v, want 2", got)
	}
	if got := body["log_run_id"]; got != float64(runID) {
		t.Fatalf("log_run_id = %#v, want %d", got, runID)
	}
}

func TestGatewayClearLogsResetsBufferedHistory(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	gateway.logs.Clear()
	gateway.logs.Append("first line")
	gateway.logs.Append("second line")
	previousRunID := gateway.logs.RunID()

	clearRec := httptest.NewRecorder()
	clearReq := httptest.NewRequest(http.MethodPost, "/api/gateway/logs/clear", nil)
	mux.ServeHTTP(clearRec, clearReq)

	if clearRec.Code != http.StatusOK {
		t.Fatalf("clear status = %d, want %d", clearRec.Code, http.StatusOK)
	}

	var clearBody map[string]any
	if err := json.Unmarshal(clearRec.Body.Bytes(), &clearBody); err != nil {
		t.Fatalf("unmarshal clear response: %v", err)
	}

	if got := clearBody["status"]; got != "cleared" {
		t.Fatalf("clear status body = %#v, want %q", got, "cleared")
	}

	clearRunID, ok := clearBody["log_run_id"].(float64)
	if !ok {
		t.Fatalf("log_run_id missing or not number: %#v", clearBody["log_run_id"])
	}
	if int(clearRunID) <= previousRunID {
		t.Fatalf("log_run_id = %d, want > %d", int(clearRunID), previousRunID)
	}

	logsRec := httptest.NewRecorder()
	logsReq := httptest.NewRequest(
		http.MethodGet,
		"/api/gateway/logs?log_offset=0&log_run_id="+strconv.Itoa(previousRunID),
		nil,
	)
	mux.ServeHTTP(logsRec, logsReq)

	if logsRec.Code != http.StatusOK {
		t.Fatalf("logs code = %d, want %d", logsRec.Code, http.StatusOK)
	}

	var logsBody map[string]any
	if err := json.Unmarshal(logsRec.Body.Bytes(), &logsBody); err != nil {
		t.Fatalf("unmarshal logs response: %v", err)
	}

	logs, ok := logsBody["logs"].([]any)
	if !ok {
		t.Fatalf("logs missing or not array: %#v", logsBody["logs"])
	}
	if len(logs) != 0 {
		t.Fatalf("logs len = %d, want 0", len(logs))
	}
	if got := logsBody["log_total"]; got != float64(0) {
		t.Fatalf("log_total = %#v, want 0", got)
	}
	if got := logsBody["log_run_id"]; got != clearBody["log_run_id"] {
		t.Fatalf("log_run_id = %#v, want %#v", got, clearBody["log_run_id"])
	}
}

func TestFindKernelBinary_EnvOverride(t *testing.T) {
	// Create a temporary file to act as the mock binary
	tmpDir := t.TempDir()
	mockBinary := filepath.Join(tmpDir, "compa-mock")
	if err := os.WriteFile(mockBinary, []byte("mock"), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	t.Setenv("COMPA_BINARY", mockBinary)

	got := utils.FindKernelBinary()
	if got != mockBinary {
		t.Errorf("FindKernelBinary() = %q, want %q", got, mockBinary)
	}
}

func TestFindKernelBinary_EnvOverride_InvalidPath(t *testing.T) {
	// When COMPA_BINARY points to a non-existent path, fall through to next strategy
	t.Setenv("COMPA_BINARY", "/nonexistent/compa-binary")

	got := utils.FindKernelBinary()
	// Should not return the invalid path; falls back to "compa-kernel" or another found path
	if got == "/nonexistent/compa-binary" {
		t.Errorf("FindKernelBinary() returned invalid env path %q, expected fallback", got)
	}
}
