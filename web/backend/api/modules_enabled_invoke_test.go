package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xibodev/compa/v4/internal/module"
	"github.com/xibodev/compa/v4/pkg/modproto"
)

func TestDisabledModuleAPICannotInvokeAndReenableRestoresInvocation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMPA_HOME", home)
	installAPIFakeModule(t, home)

	configPath := filepath.Join(home, "config.json")
	handler := NewHandler(configPath)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	originalInvoke := moduleInvokeRunner
	invocations := 0
	moduleInvokeRunner = func(ctx context.Context, runner *module.Runner, descriptor *modproto.Descriptor, request *modproto.Request) (*module.Result, error) {
		invocations++
		return originalInvoke(ctx, runner, descriptor, request)
	}
	t.Cleanup(func() { moduleInvokeRunner = originalInvoke })

	setEnabled := func(mux *http.ServeMux, enabled bool) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]bool{"enabled": enabled})
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(
			http.MethodPost, "/api/modules/fake/enabled", bytes.NewReader(body),
		))
		return recorder
	}
	invoke := func(mux *http.ServeMux) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(
			http.MethodPost,
			"/api/modules/fake/invoke",
			strings.NewReader(`{"capability":"fake.echo","input":{"name":"hello"},"approved":true}`),
		))
		return recorder
	}

	if recorder := setEnabled(mux, false); recorder.Code != http.StatusOK {
		t.Fatalf("disable status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	// New handler/mux models a backend restart reading the persisted marker.
	restarted := NewHandler(configPath)
	restartedMux := http.NewServeMux()
	restarted.RegisterRoutes(restartedMux)
	workspace := filepath.Join(home, "workspace")
	recorder := invoke(restartedMux)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("disabled invoke status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var refusal map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &refusal); err != nil {
		t.Fatalf("decode disabled refusal: %v: %s", err, recorder.Body.String())
	}
	if !strings.Contains(refusal["error"], `module "fake" is disabled`) || !strings.Contains(refusal["error"], "re-enable") {
		t.Fatalf("disabled refusal is not actionable: %q", refusal["error"])
	}
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatalf("disabled invoke reached workspace/root preparation: stat error=%v", err)
	}
	if invocations != 0 {
		t.Fatalf("disabled invoke reached subprocess runner %d time(s)", invocations)
	}

	if recorder = setEnabled(restartedMux, true); recorder.Code != http.StatusOK {
		t.Fatalf("re-enable status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	recorder = invoke(restartedMux)
	if recorder.Code != http.StatusOK {
		t.Fatalf("enabled invoke status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var result InvokeResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode invoke result: %v: %s", err, recorder.Body.String())
	}
	if !result.OK || result.Module != "fake" || result.Capability != "fake.echo" {
		t.Fatalf("enabled invocation not restored: %#v", result)
	}
	if invocations != 1 {
		t.Fatalf("re-enabled invocation count=%d, want 1", invocations)
	}
}
