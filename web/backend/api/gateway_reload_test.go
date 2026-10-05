package api

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xibodev/compa/pkg/approval"
	"github.com/xibodev/compa/pkg/config"
	"github.com/xibodev/compa/pkg/pairing"
	ppid "github.com/xibodev/compa/pkg/pid"
)

// fakeGatewayReload is a gateway that answers POST /reload with status and
// counts the reloads it was asked for with the right token. With a release
// channel, a reload is answered once release is closed (or the test ends).
func fakeGatewayReload(t *testing.T, status int, body string, release <-chan struct{}) (*ppid.PidFileData, *atomic.Int32) {
	t.Helper()
	var reloads atomic.Int32
	ended := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/reload" || r.Header.Get("Authorization") != "Bearer gateway-token" {
			http.Error(w, "unexpected request", http.StatusTeapot)
			return
		}
		reloads.Add(1)
		if release != nil {
			select {
			case <-release:
			case <-ended:
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	// Runs before server.Close, which waits for a held reload.
	t.Cleanup(func() { close(ended) })
	host, portText, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portText)
	return &ppid.PidFileData{PID: 4242, Host: host, Port: port, Token: "gateway-token"}, &reloads
}

// runningGateway records that a gateway described by pidData runs cfg.
func runningGateway(t *testing.T, pidData *ppid.PidFileData, cfg *config.Config) {
	t.Helper()
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	gateway.pidData = pidData
	gateway.bootDefaultModel = cfg.Agents.Defaults.GetModelName()
	gateway.bootConfig = computeConfigSignature(cfg)
	setGatewayRuntimeStatusLocked("running")
}

// trackedFakeGateway makes the test process the gateway the launcher tracks,
// booted with cfg and named by the PID file, with its /reload served by a
// fake that answers status once release, when given, is closed. The gateway
// status then reports on it.
func trackedFakeGateway(t *testing.T, cfg *config.Config, status int, release <-chan struct{}) *atomic.Int32 {
	t.Helper()
	pidData, reloads := fakeGatewayReload(t, status, `{"status":"reload triggered"}`, release)
	pidData.PID = os.Getpid()
	gatewayProcessMatcher = func(int) (bool, bool) { return true, true }
	gatewayHealthGet = func(string, time.Duration) (*http.Response, error) {
		return mockGatewayHealthResponse(http.StatusOK, os.Getpid()), nil
	}
	writeTestPidFile(t, *pidData)
	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess() error = %v", err)
	}
	gateway.mu.Lock()
	gateway.cmd = &exec.Cmd{Process: process}
	gateway.mu.Unlock()
	runningGateway(t, pidData, cfg)
	return reloads
}

// liveApplyingHandler serves the API the way the launcher does, with
// ApplyLiveChanges around it. ServeHTTP returns once the apply a request
// scheduled finished, so tests see its effect.
type liveApplyingHandler struct {
	h    *Handler
	next http.Handler
}

func (m liveApplyingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.next.ServeHTTP(w, r)
	m.h.waitForLiveApplies()
}

// applyingDefaultModelHandler serves the default-model API the way the
// launcher does, with ApplyLiveChanges around it.
func applyingDefaultModelHandler(t *testing.T) (http.Handler, string) {
	t.Helper()
	resetGatewayTestState(t)
	off := providerInstanceFixture("off", "https://off.example.test/v1")
	off.State = config.ProviderInstanceStateDisabled
	h, mux, configPath := providerInstanceTestHandler(t, providerInstanceFixture("owned", "https://owned.example.test/v1"), off)
	if err := saveCatalogs(&CatalogStore{Entries: map[string]*CatalogEntry{
		"owned": {ID: "owned", InstanceID: "owned", Provider: "openai", Models: []CatalogModel{{ID: "gpt-owned"}}},
	}}); err != nil {
		t.Fatal(err)
	}
	return liveApplyingHandler{h: h, next: h.ApplyLiveChanges(mux)}, configPath
}

func putDefaultModel(handler http.Handler, selection string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/api/default-model", strings.NewReader(`{"selection":"`+selection+`"}`))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, request)
	return recorder
}

func patchLiveConfig(t *testing.T, handler http.Handler, body string) {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPatch, "/api/config", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("PATCH /api/config %s = %d %s", body, recorder.Code, recorder.Body.String())
	}
}

// restartRequired reports what the gateway status of h says.
func restartRequired(t *testing.T, h *Handler) bool {
	t.Helper()
	body := gatewayStatusBody(t, h)
	if body["gateway_status"] != "running" {
		t.Fatalf("gateway_status = %#v, want running", body["gateway_status"])
	}
	required, _ := body["gateway_restart_required"].(bool)
	return required
}

// A new default model takes effect in the running gateway once the request
// was answered: the gateway reloads, and no restart is left to do.
func TestApplyLiveChangesReloadsTheRunningGateway(t *testing.T) {
	handler, configPath := applyingDefaultModelHandler(t)
	pidData, reloads := fakeGatewayReload(t, http.StatusOK, `{"status":"reload triggered"}`, nil)
	runningGateway(t, pidData, loadSavedConfig(t, configPath))

	if recorder := putDefaultModel(handler, "owned/gpt-owned"); recorder.Code != http.StatusOK {
		t.Fatalf("PUT /api/default-model = %d %s", recorder.Code, recorder.Body.String())
	}
	if reloads.Load() != 1 {
		t.Fatalf("gateway reloads = %d, want 1", reloads.Load())
	}
	gateway.mu.Lock()
	bootDefault, bootSignature := gateway.bootDefaultModel, gateway.bootConfig
	gateway.mu.Unlock()
	if bootDefault != "owned/gpt-owned" {
		t.Fatalf("boot default = %q, want the applied default", bootDefault)
	}
	if gatewayRestartRequiredBySignature(bootSignature, computeConfigSignature(loadSavedConfig(t, configPath)), "running", false) {
		t.Fatal("a restart is still required after the gateway applied the change")
	}

	// Saving the same default again applies nothing.
	putDefaultModel(handler, "owned/gpt-owned")
	if reloads.Load() != 1 {
		t.Fatalf("gateway reloads = %d, want no reload for an unchanged selection", reloads.Load())
	}
}

// A reload the gateway refuses leaves the restart to do.
func TestApplyLiveChangesKeepsARefusedChangeForARestart(t *testing.T) {
	handler, configPath := applyingDefaultModelHandler(t)
	pidData, reloads := fakeGatewayReload(t, http.StatusInternalServerError, `{"error":"reload already in progress"}`, nil)
	booted := loadSavedConfig(t, configPath)
	runningGateway(t, pidData, booted)

	putDefaultModel(handler, "owned/gpt-owned")
	if reloads.Load() != 1 {
		t.Fatalf("gateway reloads = %d, want 1", reloads.Load())
	}
	gateway.mu.Lock()
	applied := gateway.bootConfig
	gateway.mu.Unlock()
	if !applied.equal(computeConfigSignature(booted)) {
		t.Fatalf("applied signature = %q, want the booted one kept", applied)
	}
	if !gatewayRestartRequiredBySignature(applied, computeConfigSignature(loadSavedConfig(t, configPath)), "running", false) {
		t.Fatal("no restart is required after the gateway refused the change")
	}
}

// Nothing is reloaded without a running gateway, for a read, or for a
// request that drives the gateway itself.
func TestApplyLiveChangesOnlyAppliesSavedChangesToARunningGateway(t *testing.T) {
	handler, configPath := applyingDefaultModelHandler(t)
	pidData, reloads := fakeGatewayReload(t, http.StatusOK, `{"status":"reload triggered"}`, nil)

	putDefaultModel(handler, "owned/gpt-owned")
	if reloads.Load() != 0 {
		t.Fatalf("gateway reloads = %d with no gateway running, want 0", reloads.Load())
	}

	runningGateway(t, pidData, loadSavedConfig(t, configPath))
	editSavedConfig(t, configPath, func(cfg *config.Config) { cfg.Agents.Defaults.ModelName = "" })
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/default-model", nil),
		httptest.NewRequest(http.MethodPost, "/api/gateway/restart-not-a-route", nil),
	} {
		handler.ServeHTTP(httptest.NewRecorder(), request)
	}
	if reloads.Load() != 0 {
		t.Fatalf("gateway reloads = %d after a read and a gateway request, want 0", reloads.Load())
	}
}

// An approved sender is admitted by the running gateway: the approval
// reloads it, and the status asks for no restart, neither while the reload
// runs nor after it.
func TestPairingApprovalReloadsTheRunningGateway(t *testing.T) {
	resetGatewayTestState(t)
	h, mux, home := pairingTestHandler(t, "telegram:1")
	recordPairingRequest(t, home, pairing.Request{Channel: "telegram", SenderID: "telegram:123"})
	reloads := trackedFakeGateway(t, loadSavedConfig(t, h.configPath), http.StatusOK, nil)

	rec := pairingRequestJSON(t, mux, http.MethodPost, "/api/channels/telegram/pairing/approve", `{"sender_id":"telegram:123"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("approve = %d %s", rec.Code, rec.Body.String())
	}
	if restartRequired(t, h) {
		t.Fatal("gateway_restart_required right after the approval")
	}
	h.waitForLiveApplies()
	if reloads.Load() != 1 {
		t.Fatalf("gateway reloads = %d, want 1", reloads.Load())
	}
	if restartRequired(t, h) {
		t.Fatal("gateway_restart_required after the gateway applied the approval")
	}
}

// Without a reload, an approval leaves the restart to do.
func TestPairingApprovalThatFailsToReloadRequiresARestart(t *testing.T) {
	resetGatewayTestState(t)
	h, mux, home := pairingTestHandler(t)
	recordPairingRequest(t, home, pairing.Request{Channel: "telegram", SenderID: "telegram:123"})
	reloads := trackedFakeGateway(t, loadSavedConfig(t, h.configPath), http.StatusInternalServerError, nil)

	pairingRequestJSON(t, mux, http.MethodPost, "/api/channels/telegram/pairing/approve", `{"sender_id":"telegram:123"}`)
	h.waitForLiveApplies()
	if reloads.Load() != 1 {
		t.Fatalf("gateway reloads = %d, want 1", reloads.Load())
	}
	if !restartRequired(t, h) {
		t.Fatal("no restart is required after the gateway failed to reload")
	}
}

// approvalPolicyChange is a PATCH /api/config body that changes the approval
// policy, a live change.
const approvalPolicyChange = `{"tools":{"approval":{"default":"ask","rules":[{"tool":"exec","action":"deny"}]}}}`

// heldApprovalPolicyChange saves approvalPolicyChange through
// ApplyLiveChanges to a tracked gateway whose reload answers status once
// the returned function released it.
func heldApprovalPolicyChange(t *testing.T, status int) (*Handler, http.Handler, *atomic.Int32, func()) {
	t.Helper()
	resetGatewayTestState(t)
	h, mux, _ := pairingTestHandler(t)
	handler := h.ApplyLiveChanges(mux)
	release := make(chan struct{})
	reloads := trackedFakeGateway(t, loadSavedConfig(t, h.configPath), status, release)

	patchLiveConfig(t, handler, approvalPolicyChange)
	if got := loadSavedConfig(t, h.configPath).Tools.Approval.Default; got != approval.Ask {
		t.Fatalf("saved approval default = %q, want ask", got)
	}
	return h, handler, reloads, func() {
		close(release)
		h.waitForLiveApplies()
	}
}

// A saved approval policy takes effect in the running gateway. The status
// asks for no restart while the gateway reloads - the client reads it as
// soon as the save was answered - nor after.
func TestApprovalPolicyChangeNeedsNoRestart(t *testing.T) {
	h, _, reloads, release := heldApprovalPolicyChange(t, http.StatusOK)
	if restartRequired(t, h) {
		t.Fatal("gateway_restart_required while the gateway applies the approval policy")
	}

	release()
	if reloads.Load() != 1 {
		t.Fatalf("gateway reloads = %d, want 1", reloads.Load())
	}
	if restartRequired(t, h) {
		t.Fatal("gateway_restart_required after the gateway applied the approval policy")
	}
}

// A reload that fails leaves the restart to do once it finished.
func TestApprovalPolicyChangeThatFailsToReloadRequiresARestart(t *testing.T) {
	h, _, reloads, release := heldApprovalPolicyChange(t, http.StatusInternalServerError)
	if restartRequired(t, h) {
		t.Fatal("gateway_restart_required while the gateway applies the approval policy")
	}

	release()
	if reloads.Load() != 1 {
		t.Fatalf("gateway reloads = %d, want 1", reloads.Load())
	}
	if !restartRequired(t, h) {
		t.Fatal("no restart is required after the gateway failed to apply the approval policy")
	}
}

// A change a reload is not asked for, such as a tool turned off, asks for a
// restart at once, also while a live change is applied.
func TestNonLiveChangeRequiresARestartAtOnce(t *testing.T) {
	h, handler, reloads, release := heldApprovalPolicyChange(t, http.StatusOK)

	writeFile := loadSavedConfig(t, h.configPath).Tools.WriteFile.Enabled
	patchLiveConfig(t, handler, `{"tools":{"write_file":{"enabled":`+strconv.FormatBool(!writeFile)+`}}}`)
	if !restartRequired(t, h) {
		t.Fatal("no restart is required for a tool change the gateway does not apply")
	}
	release()
	if reloads.Load() != 1 {
		t.Fatalf("gateway reloads = %d, want 1 for the approval policy alone", reloads.Load())
	}
}
