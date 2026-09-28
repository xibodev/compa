package api

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/xibodev/compa/pkg/config"
	ppid "github.com/xibodev/compa/pkg/pid"
)

// fakeGatewayReload is a gateway that answers POST /reload with status and
// counts the reloads it was asked for with the right token.
func fakeGatewayReload(t *testing.T, status int, body string) (*ppid.PidFileData, *atomic.Int32) {
	t.Helper()
	var reloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/reload" || r.Header.Get("Authorization") != "Bearer gateway-token" {
			http.Error(w, "unexpected request", http.StatusTeapot)
			return
		}
		reloads.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
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
	gateway.bootConfigSignature = computeConfigSignature(cfg)
	gateway.bootModelSignature = modelSelectionSignature(cfg)
	setGatewayRuntimeStatusLocked("running")
}

// applyingDefaultModelHandler serves the default-model API the way the
// launcher does, with ApplyModelChanges around it.
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
	return h.ApplyModelChanges(mux), configPath
}

func putDefaultModel(handler http.Handler, selection string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/api/default-model", strings.NewReader(`{"selection":"`+selection+`"}`))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, request)
	return recorder
}

// A new default model takes effect in the running gateway before the
// request is answered: the gateway reloads, and no restart is left to do.
func TestApplyModelChangesReloadsTheRunningGateway(t *testing.T) {
	handler, configPath := applyingDefaultModelHandler(t)
	pidData, reloads := fakeGatewayReload(t, http.StatusOK, `{"status":"reload triggered"}`)
	runningGateway(t, pidData, loadSavedConfig(t, configPath))

	if recorder := putDefaultModel(handler, "owned/gpt-owned"); recorder.Code != http.StatusOK {
		t.Fatalf("PUT /api/default-model = %d %s", recorder.Code, recorder.Body.String())
	}
	if reloads.Load() != 1 {
		t.Fatalf("gateway reloads = %d, want 1", reloads.Load())
	}
	gateway.mu.Lock()
	bootDefault, bootSignature := gateway.bootDefaultModel, gateway.bootConfigSignature
	gateway.mu.Unlock()
	if bootDefault != "owned/gpt-owned" {
		t.Fatalf("boot default = %q, want the applied default", bootDefault)
	}
	if gatewayRestartRequiredBySignature(bootSignature, computeConfigSignature(loadSavedConfig(t, configPath)), "running") {
		t.Fatal("a restart is still required after the gateway applied the change")
	}

	// Saving the same default again applies nothing.
	putDefaultModel(handler, "owned/gpt-owned")
	if reloads.Load() != 1 {
		t.Fatalf("gateway reloads = %d, want no reload for an unchanged selection", reloads.Load())
	}
}

// A reload the gateway refuses leaves the restart to do.
func TestApplyModelChangesKeepsARefusedChangeForARestart(t *testing.T) {
	handler, configPath := applyingDefaultModelHandler(t)
	pidData, reloads := fakeGatewayReload(t, http.StatusInternalServerError, `{"error":"reload already in progress"}`)
	booted := loadSavedConfig(t, configPath)
	runningGateway(t, pidData, booted)

	putDefaultModel(handler, "owned/gpt-owned")
	if reloads.Load() != 1 {
		t.Fatalf("gateway reloads = %d, want 1", reloads.Load())
	}
	gateway.mu.Lock()
	applied := gateway.bootModelSignature
	gateway.mu.Unlock()
	if applied != modelSelectionSignature(booted) {
		t.Fatalf("applied model signature = %q, want the booted one kept", applied)
	}
}

// Nothing is reloaded without a running gateway, for a read, or for a
// request that drives the gateway itself.
func TestApplyModelChangesOnlyAppliesSavedChangesToARunningGateway(t *testing.T) {
	handler, configPath := applyingDefaultModelHandler(t)
	pidData, reloads := fakeGatewayReload(t, http.StatusOK, `{"status":"reload triggered"}`)

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
