package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"runtime"
	"testing"
)

func setupVersionTestIsolation(t *testing.T) {
	t.Helper()

	originalGatewayState := currentGatewayVersionState
	originalFinder := findKernelBinaryForInfo
	originalRunner := runKernelVersionOutput
	originalFallback := launcherBuildInfoForVersion
	t.Cleanup(func() {
		currentGatewayVersionState = originalGatewayState
		findKernelBinaryForInfo = originalFinder
		runKernelVersionOutput = originalRunner
		launcherBuildInfoForVersion = originalFallback
		versionInfoCache.resetForTest()
	})

	currentGatewayVersionState = func() (int, bool) { return 0, false }
	versionInfoCache.resetForTest()
}

func TestGetSystemVersionUsesKernelBinaryInfo(t *testing.T) {
	setupVersionTestIsolation(t)

	launcherBuildInfoForVersion = func() systemVersionResponse {
		return systemVersionResponse{Version: "fallback", GoVersion: "go-fallback"}
	}

	findKernelBinaryForInfo = func() string { return "compa-kernel" }
	runKernelVersionOutput = func(_ context.Context, _ string) (string, error) {
		return "🦞 compa-kernel v1.2.3 (git: deadbeef)\n  Build: 2026-03-27T12:34:56Z\n  Go: go1.25.8\n", nil
	}

	h := NewHandler("")
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/system/version", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var got systemVersionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if got.Version != "v1.2.3" {
		t.Fatalf("version = %q, want %q", got.Version, "v1.2.3")
	}
	if got.GitCommit != "deadbeef" {
		t.Fatalf("git_commit = %q, want %q", got.GitCommit, "deadbeef")
	}
	if got.BuildTime != "2026-03-27T12:34:56Z" {
		t.Fatalf("build_time = %q, want %q", got.BuildTime, "2026-03-27T12:34:56Z")
	}
	if got.GoVersion != "go1.25.8" {
		t.Fatalf("go_version = %q, want %q", got.GoVersion, "go1.25.8")
	}
}

func TestGetSystemVersionFallsBackToLauncherInfoWhenCommandFails(t *testing.T) {
	setupVersionTestIsolation(t)

	expected := systemVersionResponse{
		Version:   "v9.9.9",
		GitCommit: "cafebabe",
		BuildTime: "2026-03-27T10:43:34+0000",
		GoVersion: "go1.25.8",
	}
	launcherBuildInfoForVersion = func() systemVersionResponse { return expected }

	findKernelBinaryForInfo = func() string { return "compa-kernel" }
	runKernelVersionOutput = func(_ context.Context, _ string) (string, error) {
		return "", errors.New("binary unavailable")
	}

	h := NewHandler("")
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/system/version", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var got systemVersionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if got.Version != expected.Version {
		t.Fatalf("version = %q, want %q", got.Version, expected.Version)
	}
	if got.GitCommit != expected.GitCommit {
		t.Fatalf("git_commit = %q, want %q", got.GitCommit, expected.GitCommit)
	}
	if got.BuildTime != expected.BuildTime {
		t.Fatalf("build_time = %q, want %q", got.BuildTime, expected.BuildTime)
	}
	if got.GoVersion != expected.GoVersion {
		t.Fatalf("go_version = %q, want %q", got.GoVersion, expected.GoVersion)
	}
}

func TestParseKernelVersionOutput(t *testing.T) {
	setupVersionTestIsolation(t)

	raw := "\u001b[1;31m████\u001b[0m\n🦞 compa-kernel 18ec263 (git: 18ec2631)\n  Build: 2026-03-27T10:43:34+0000\n  Go: go1.25.8\n"
	got, ok := parseKernelVersionOutput(raw)
	if !ok {
		t.Fatal("parseKernelVersionOutput() should parse valid output")
	}
	if got.Version != "18ec263" {
		t.Fatalf("version = %q, want %q", got.Version, "18ec263")
	}
	if got.GitCommit != "18ec2631" {
		t.Fatalf("git_commit = %q, want %q", got.GitCommit, "18ec2631")
	}
	if got.BuildTime != "2026-03-27T10:43:34+0000" {
		t.Fatalf("build_time = %q, want %q", got.BuildTime, "2026-03-27T10:43:34+0000")
	}
	if got.GoVersion != "go1.25.8" {
		t.Fatalf("go_version = %q, want %q", got.GoVersion, "go1.25.8")
	}
}

func TestParseKernelVersionOutputIgnoresUsageLine(t *testing.T) {
	setupVersionTestIsolation(t)

	raw := "Usage: compa-kernel version [flags]\n"
	got, ok := parseKernelVersionOutput(raw)
	if ok {
		t.Fatalf("parseKernelVersionOutput() parsed usage line unexpectedly: %#v", got)
	}
}

func TestParseKernelVersionOutputAcceptsLetterOnlyHashVersion(t *testing.T) {
	setupVersionTestIsolation(t)

	raw := "compa-kernel abcdefa (git: abcdefabcdefabcdefabcdefabcdefabcdefabcd)\n"
	got, ok := parseKernelVersionOutput(raw)
	if !ok {
		t.Fatal("parseKernelVersionOutput() should parse letter-only hash version")
	}
	if got.Version != "abcdefa" {
		t.Fatalf("version = %q, want %q", got.Version, "abcdefa")
	}
	if got.GitCommit != "abcdefabcdefabcdefabcdefabcdefabcdefabcd" {
		t.Fatalf("git_commit = %q, want %q", got.GitCommit, "abcdefabcdefabcdefabcdefabcdefabcdefabcd")
	}
}

func TestResolveSystemVersionInfoFallsBackRuntimeGoVersion(t *testing.T) {
	setupVersionTestIsolation(t)

	launcherBuildInfoForVersion = func() systemVersionResponse {
		return systemVersionResponse{Version: "dev", GoVersion: ""}
	}

	findKernelBinaryForInfo = func() string { return "compa-kernel" }
	runKernelVersionOutput = func(_ context.Context, _ string) (string, error) {
		return "compa-kernel v1.0.0\n", nil
	}

	h := NewHandler("")
	got := h.resolveSystemVersionInfo(context.Background())
	if got.GoVersion != runtime.Version() {
		t.Fatalf("go_version = %q, want runtime version %q", got.GoVersion, runtime.Version())
	}
}

func TestResolveSystemVersionInfoCachesWhileGatewayAlive(t *testing.T) {
	setupVersionTestIsolation(t)

	launcherBuildInfoForVersion = func() systemVersionResponse {
		return systemVersionResponse{Version: "dev", GoVersion: "go-fallback"}
	}
	findKernelBinaryForInfo = func() string { return "compa-kernel" }

	pid := 4321
	currentGatewayVersionState = func() (int, bool) { return pid, true }

	runCount := 0
	runKernelVersionOutput = func(_ context.Context, _ string) (string, error) {
		runCount++
		return fmt.Sprintf("compa-kernel v1.2.%d\n", runCount), nil
	}

	h := NewHandler("")
	first := h.resolveSystemVersionInfo(context.Background())
	second := h.resolveSystemVersionInfo(context.Background())

	if first.Version != "v1.2.1" {
		t.Fatalf("first version = %q, want %q", first.Version, "v1.2.1")
	}
	if second.Version != "v1.2.1" {
		t.Fatalf("second version = %q, want cached %q", second.Version, "v1.2.1")
	}
	if runCount != 1 {
		t.Fatalf("run count = %d, want %d", runCount, 1)
	}
}

func TestResolveSystemVersionInfoInvalidatesCacheWhenGatewayStops(t *testing.T) {
	setupVersionTestIsolation(t)

	launcherBuildInfoForVersion = func() systemVersionResponse {
		return systemVersionResponse{Version: "dev", GoVersion: "go-fallback"}
	}
	findKernelBinaryForInfo = func() string { return "compa-kernel" }

	alive := true
	pid := 9876
	currentGatewayVersionState = func() (int, bool) {
		if !alive {
			return 0, false
		}
		return pid, true
	}

	runCount := 0
	runKernelVersionOutput = func(_ context.Context, _ string) (string, error) {
		runCount++
		return fmt.Sprintf("compa-kernel v2.0.%d\n", runCount), nil
	}

	h := NewHandler("")
	first := h.resolveSystemVersionInfo(context.Background())
	second := h.resolveSystemVersionInfo(context.Background())

	if first.Version != "v2.0.1" || second.Version != "v2.0.1" {
		t.Fatalf("expected cached version v2.0.1, got first=%q second=%q", first.Version, second.Version)
	}
	if runCount != 1 {
		t.Fatalf("run count after cache hit = %d, want %d", runCount, 1)
	}

	alive = false
	third := h.resolveSystemVersionInfo(context.Background())
	if third.Version != "v2.0.2" {
		t.Fatalf("third version = %q, want refreshed %q", third.Version, "v2.0.2")
	}
	if runCount != 2 {
		t.Fatalf("run count after invalidation = %d, want %d", runCount, 2)
	}
}

func TestResolveSystemVersionInfoSkipsCommandWhenContextCanceled(t *testing.T) {
	setupVersionTestIsolation(t)

	launcherBuildInfoForVersion = func() systemVersionResponse {
		return systemVersionResponse{Version: "v3.0.0", GoVersion: "go-fallback"}
	}
	findKernelBinaryForInfo = func() string { return "compa-kernel" }

	runCount := 0
	runKernelVersionOutput = func(_ context.Context, _ string) (string, error) {
		runCount++
		return "compa-kernel v9.9.9\n", nil
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	h := NewHandler("")
	got := h.resolveSystemVersionInfo(canceledCtx)

	if runCount != 0 {
		t.Fatalf("run count = %d, want %d", runCount, 0)
	}
	if got.Version != "v3.0.0" {
		t.Fatalf("version = %q, want fallback %q", got.Version, "v3.0.0")
	}
}

func TestResolveGatewayBinaryForVersionInfoPrefersGatewayCommandPath(t *testing.T) {
	setupVersionTestIsolation(t)

	originalFinder := findKernelBinaryForInfo
	t.Cleanup(func() {
		findKernelBinaryForInfo = originalFinder
	})

	gateway.mu.Lock()
	originalCmd := gateway.cmd
	gateway.cmd = &exec.Cmd{Path: "/tmp/compa-from-gateway"}
	gateway.mu.Unlock()
	t.Cleanup(func() {
		gateway.mu.Lock()
		gateway.cmd = originalCmd
		gateway.mu.Unlock()
	})

	got := resolveGatewayBinaryForVersionInfo()
	if got != "/tmp/compa-from-gateway" {
		t.Fatalf("exec path = %q, want %q", got, "/tmp/compa-from-gateway")
	}
}
