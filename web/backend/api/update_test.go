package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/xibodev/compa/v4/pkg/updater"
	"github.com/xibodev/compa/v4/pkg/updater/updatertest"
)

// useUpdater replaces what /api/update runs for the rest of the test.
func useUpdater(t *testing.T, fn func(context.Context, updater.Options) (*updater.Result, error)) {
	t.Helper()
	orig := applyUpdate
	applyUpdate = fn
	t.Cleanup(func() {
		applyUpdate = orig
		updateMu.Lock()
		updateInstalled = ""
		updateMu.Unlock()
	})
}

// useFakeRelease makes /api/update install from srv into dir, instead of from
// GitHub into the directory of the test binary. The fake programs are text,
// so the check that the new kernel runs is skipped.
func useFakeRelease(t *testing.T, srv *httptest.Server, dir string) {
	t.Helper()
	useUpdater(t, func(ctx context.Context, opts updater.Options) (*updater.Result, error) {
		opts.Dir = dir
		opts.APIBaseURL = srv.URL
		opts.HTTPClient = srv.Client()
		opts.SelfTest = func(context.Context, string) error { return nil }
		return updater.Update(ctx, opts)
	})
}

func exeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// installedCompa is an install directory holding both programs.
func installedCompa(t *testing.T, launcher, kernel string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{exeName("compa"): launcher, exeName("compa-kernel"): kernel} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func assertPrograms(t *testing.T, dir, launcher, kernel string) {
	t.Helper()
	for name, want := range map[string]string{exeName("compa"): launcher, exeName("compa-kernel"): kernel} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
}

func fakeRelease(t *testing.T, tag string) updatertest.Release {
	version := strings.TrimPrefix(tag, "v")
	return updatertest.Build(t, tag, runtime.GOOS, runtime.GOARCH, "launcher "+version, "kernel "+version)
}

func postUpdate(t *testing.T, method, body string) (*httptest.ResponseRecorder, updateResponse) {
	t.Helper()
	mux := http.NewServeMux()
	NewHandler("").RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, "/api/update", strings.NewReader(body)))
	var resp updateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response %q is not JSON: %v", rec.Body.String(), err)
	}
	return rec, resp
}

func TestUpdateInstallsBothProgramsFromTheLatestRelease(t *testing.T) {
	srv := updatertest.Server(t, fakeRelease(t, "v1.2.3"), fakeRelease(t, "v1.1.0"))
	dir := installedCompa(t, "launcher 1.0.0", "kernel 1.0.0")
	useFakeRelease(t, srv, dir)

	rec, resp := postUpdate(t, http.MethodPost, "")
	if rec.Code != http.StatusOK || resp.Status != "ok" {
		t.Fatalf("status = %d %+v, want 200 ok", rec.Code, resp)
	}
	if !strings.Contains(resp.Message, "restart Compa to use the new version") || resp.Version != "v1.2.3" {
		t.Fatalf("answer = %+v, want v1.2.3 and a request to restart Compa", resp)
	}
	assertPrograms(t, dir, "launcher 1.2.3", "kernel 1.2.3")
}

func TestUpdateInstallsTheRequestedRelease(t *testing.T) {
	srv := updatertest.Server(t, fakeRelease(t, "v1.2.3"), fakeRelease(t, "v1.1.0"))

	for _, body := range []string{
		`{"version":"1.1.0"}`,
		`{"url":"https://github.com/xibodev/compa/releases/tag/v1.1.0"}`,
	} {
		t.Run(body, func(t *testing.T) {
			dir := installedCompa(t, "launcher 1.0.0", "kernel 1.0.0")
			useFakeRelease(t, srv, dir)

			rec, resp := postUpdate(t, http.MethodPost, body)
			if rec.Code != http.StatusOK || resp.Version != "v1.1.0" {
				t.Fatalf("status = %d %+v, want 200 with v1.1.0", rec.Code, resp)
			}
			assertPrograms(t, dir, "launcher 1.1.0", "kernel 1.1.0")
		})
	}
}

func TestUpdateRefusesAnArchiveThatFailsItsChecksum(t *testing.T) {
	rel := fakeRelease(t, "v1.2.3")
	archive := &rel.Assets[0]
	archive.Digest = updatertest.NoDigest
	archive.Body = updatertest.Archive(t, runtime.GOOS,
		updatertest.Programs(runtime.GOOS, "tampered launcher", "tampered kernel"))
	srv := updatertest.Server(t, rel)
	dir := installedCompa(t, "launcher 1.0.0", "kernel 1.0.0")
	useFakeRelease(t, srv, dir)

	rec, resp := postUpdate(t, http.MethodPost, "{}")
	if rec.Code != http.StatusInternalServerError || resp.Status != "error" {
		t.Fatalf("status = %d %+v, want 500 error", rec.Code, resp)
	}
	if !strings.Contains(resp.Message, "SHA-256 mismatch") {
		t.Fatalf("message = %q, want a SHA-256 mismatch", resp.Message)
	}
	assertPrograms(t, dir, "launcher 1.0.0", "kernel 1.0.0")
}

func TestUpdateRefusesReleasesFromAnywhereElse(t *testing.T) {
	useUpdater(t, func(context.Context, updater.Options) (*updater.Result, error) {
		t.Fatal("an update ran for a foreign release")
		return nil, nil
	})

	for _, body := range []string{
		`{"url":"https://example.com/compa_1.2.3_linux_amd64.tar.gz"}`,
		`{"url":"https://github.com/someone/compa/releases/tag/v1.2.3"}`,
		`{"version":"latest"}`,
		`{"version":"v1.2.3","url":"https://github.com/xibodev/compa/releases/tag/v1.2.4"}`,
		`not json`,
	} {
		rec, resp := postUpdate(t, http.MethodPost, body)
		if rec.Code != http.StatusBadRequest || resp.Status != "error" {
			t.Errorf("%s: status = %d %+v, want 400 error", body, rec.Code, resp)
		}
	}
}

func TestUpdateAcceptsOnlyPost(t *testing.T) {
	rec, resp := postUpdate(t, http.MethodGet, "")
	if rec.Code != http.StatusMethodNotAllowed || resp.Status != "error" || rec.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("GET: status = %d %+v Allow=%q, want 405 and Allow: POST", rec.Code, resp, rec.Header().Get("Allow"))
	}
}

func TestUpdateReportsTheUpdaterError(t *testing.T) {
	useUpdater(t, func(context.Context, updater.Options) (*updater.Result, error) {
		return nil, errors.New("release v1.2.3 has no build for plan9/386")
	})

	rec, resp := postUpdate(t, http.MethodPost, "{}")
	if rec.Code != http.StatusInternalServerError || resp.Message != "release v1.2.3 has no build for plan9/386" {
		t.Fatalf("status = %d %+v, want 500 with the updater's error", rec.Code, resp)
	}
}

func TestUpdateWaitsForARestartBeforeUpdatingAgain(t *testing.T) {
	calls := 0
	useUpdater(t, func(context.Context, updater.Options) (*updater.Result, error) {
		calls++
		return &updater.Result{Tag: "v1.2.3", Version: "1.2.3", Dir: t.TempDir()}, nil
	})

	if rec, _ := postUpdate(t, http.MethodPost, "{}"); rec.Code != http.StatusOK {
		t.Fatalf("first update: status %d", rec.Code)
	}
	rec, resp := postUpdate(t, http.MethodPost, "{}")
	if rec.Code != http.StatusOK || !strings.Contains(resp.Message, "already installed") ||
		!strings.Contains(resp.Message, "restart Compa") {
		t.Fatalf("second update: status = %d %+v, want the pending restart reported", rec.Code, resp)
	}
	if calls != 1 {
		t.Fatalf("the updater ran %d times, want once", calls)
	}
}

func TestUpdateRunsOneAtATime(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	useUpdater(t, func(context.Context, updater.Options) (*updater.Result, error) {
		close(started)
		<-release
		return &updater.Result{Tag: "v1.2.3"}, nil
	})

	done := make(chan int)
	go func() {
		mux := http.NewServeMux()
		NewHandler("").RegisterRoutes(mux)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/update", strings.NewReader("{}")))
		done <- rec.Code
	}()
	<-started

	rec, resp := postUpdate(t, http.MethodPost, "{}")
	close(release)
	if rec.Code != http.StatusConflict || resp.Status != "error" {
		t.Fatalf("concurrent update: status = %d %+v, want 409", rec.Code, resp)
	}
	if code := <-done; code != http.StatusOK {
		t.Fatalf("first update: status %d, want 200", code)
	}
}
