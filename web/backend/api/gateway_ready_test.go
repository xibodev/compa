package api

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/xibodev/compa/pkg/config"
)

// serveKernelReadiness answers the tracked test gateway's probes: /health
// as a live kernel, /ready with readyStatus and readyBody.
func serveKernelReadiness(readyStatus int, readyBody string) {
	gatewayHealthGet = func(url string, _ time.Duration) (*http.Response, error) {
		if strings.HasSuffix(url, "/ready") {
			return &http.Response{StatusCode: readyStatus, Body: io.NopCloser(strings.NewReader(readyBody))}, nil
		}
		return mockGatewayHealthResponse(http.StatusOK, os.Getpid()), nil
	}
}

func TestGatewayStatusReportsAFailedReadinessCheck(t *testing.T) {
	resetGatewayTestState(t)
	configPath := gatewayModelTestConfig(t, func(*config.Config) {})
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	runAsTrackedGateway(t, cfg)
	serveKernelReadiness(http.StatusServiceUnavailable, `{"status":"not ready","checks":{`+
		`"agent_loop":{"name":"agent_loop","status":"fail","message":"agent loop stopped: mcp setup failed"},`+
		`"channels":{"name":"channels","status":"ok"}}}`)

	body := gatewayStatusBody(t, NewHandler(configPath))
	if got := body["gateway_status"]; got != "error" {
		t.Fatalf("gateway_status = %#v, want error", got)
	}
	if got := body["gateway_error"]; got != "agent loop stopped: mcp setup failed" {
		t.Fatalf("gateway_error = %#v, want the check's message", got)
	}
	if got := body["gateway_error_check"]; got != "agent_loop" {
		t.Fatalf("gateway_error_check = %#v, want agent_loop", got)
	}
	if got := body["pid"]; got != float64(os.Getpid()) {
		t.Fatalf("pid = %#v, want the running kernel's", got)
	}
	if !gatewayRestartsOnConfigChange(body) {
		t.Fatal("a kernel that runs without its agent loop is not restarted by a reset or binding")
	}
}

func TestGatewayStatusStaysRunningWhenReadinessPasses(t *testing.T) {
	for name, probe := range map[string]struct {
		status int
		body   string
	}{
		"ready":    {http.StatusOK, `{"status":"ready","checks":{"agent_loop":{"name":"agent_loop","status":"ok"}}}`},
		"starting": {http.StatusServiceUnavailable, `{"status":"not ready"}`},
		"garbled":  {http.StatusServiceUnavailable, `<html>`},
	} {
		t.Run(name, func(t *testing.T) {
			resetGatewayTestState(t)
			configPath := gatewayModelTestConfig(t, func(*config.Config) {})
			cfg, err := config.LoadConfig(configPath)
			if err != nil {
				t.Fatalf("LoadConfig() error = %v", err)
			}
			runAsTrackedGateway(t, cfg)
			serveKernelReadiness(probe.status, probe.body)

			body := gatewayStatusBody(t, NewHandler(configPath))
			if got := body["gateway_status"]; got != "running" {
				t.Fatalf("gateway_status = %#v, want running", got)
			}
			if _, ok := body["gateway_error"]; ok {
				t.Fatalf("gateway_error = %#v, want none", body["gateway_error"])
			}
		})
	}
}

func TestGatewayRestartsOnConfigChange(t *testing.T) {
	for _, tc := range []struct {
		status map[string]any
		want   bool
	}{
		{map[string]any{"gateway_status": "running"}, true},
		{map[string]any{"gateway_status": "error", "gateway_error_check": "agent_loop"}, true},
		{map[string]any{"gateway_status": "error"}, false},
		{map[string]any{"gateway_status": "stopped"}, false},
		{map[string]any{"gateway_status": "starting"}, false},
	} {
		if got := gatewayRestartsOnConfigChange(tc.status); got != tc.want {
			t.Errorf("gatewayRestartsOnConfigChange(%v) = %v, want %v", tc.status, got, tc.want)
		}
	}
}
