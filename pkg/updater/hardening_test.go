package updater

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/xibodev/compa/v3/pkg/config"
	"github.com/xibodev/compa/v3/pkg/updater/updatertest"
)

// TestMain keeps tests from running the fake programs they install.
func TestMain(m *testing.M) {
	selfTest = func(context.Context, string) error { return nil }
	os.Exit(m.Run())
}

func TestDownloadsRefusePlainHTTPOffThisMachine(t *testing.T) {
	for _, raw := range []string{"http://github.com/x", "http://192.168.1.2/x", "ftp://127.0.0.1/x"} {
		if _, err := get(context.Background(), http.DefaultClient, raw, "*/*"); err == nil || !strings.Contains(err.Error(), "https") {
			t.Errorf("get(%q) = %v, want it refused", raw, err)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"v1.2.3", "1.2.3", 0},
		{"v1.2.3", "v1.10.0", -1},
		{"v2.0.0", "v1.99.99", 1},
		{"v1.0.0-rc.1", "v1.0.0", -1},
		{"v1.0.0-rc.2", "v1.0.0-rc.10", -1},
		{"v1.0.0-alpha", "v1.0.0-1", 1},
		{"v1.0.0", "dev", 0},
		{"", "v1.0.0", 0},
	} {
		if got := CompareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestDowngradeNeedsToBeAllowed(t *testing.T) {
	original := config.Version
	config.Version = "2.0.0"
	t.Cleanup(func() { config.Version = original })

	srv := updatertest.Server(t, updatertest.Build(t, "v1.2.3", runtime.GOOS, runtime.GOARCH, "launcher 1.2.3", "kernel 1.2.3"))
	dir, files := installed(t, runtime.GOOS, "launcher 2.0.0", "kernel 2.0.0")
	opts := fakeOptions(srv, dir)
	opts.Tag = "v1.2.3"
	if _, err := update(context.Background(), opts, runtime.GOOS, runtime.GOARCH); err == nil || !strings.Contains(err.Error(), "downgrade") {
		t.Fatalf("update() to an older release = %v, want it refused", err)
	}
	assertDir(t, dir, files)

	opts.AllowDowngrade = true
	if _, err := update(context.Background(), opts, runtime.GOOS, runtime.GOARCH); err != nil {
		t.Fatalf("update() with AllowDowngrade = %v", err)
	}
}

func TestUpdateCommandDowngradesOnlyWithAllowDowngrade(t *testing.T) {
	original := config.Version
	config.Version = "2.0.0"
	t.Cleanup(func() { config.Version = original })

	srv := updatertest.Server(t, updatertest.Build(t, "v1.2.3", runtime.GOOS, runtime.GOARCH, "launcher 1.2.3", "kernel 1.2.3"))
	dir, files := installed(t, runtime.GOOS, "launcher 2.0.0", "kernel 2.0.0")
	originalRun := runUpdate
	runUpdate = func(ctx context.Context, opts Options) (*Result, error) {
		opts.Dir, opts.APIBaseURL, opts.HTTPClient = dir, srv.URL, srv.Client()
		return Update(ctx, opts)
	}
	t.Cleanup(func() { runUpdate = originalRun })
	run := func(args ...string) error {
		cmd := NewUpdateCommand("compa-kernel")
		cmd.SetArgs(args)
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		return cmd.Execute()
	}

	if err := run("--version", "v1.2.3"); err == nil || !strings.Contains(err.Error(), "--allow-downgrade") {
		t.Fatalf("update --version v1.2.3 = %v, want it refused, naming --allow-downgrade", err)
	}
	assertDir(t, dir, files)

	if err := run("--version", "v1.2.3", "--allow-downgrade"); err != nil {
		t.Fatalf("update --version v1.2.3 --allow-downgrade = %v", err)
	}
	for name := range files {
		files[name] = strings.Replace(files[name], "2.0.0", "1.2.3", 1)
	}
	assertDir(t, dir, files)
}

func TestAKernelThatDoesNotRunIsPutBack(t *testing.T) {
	original := selfTest
	selfTest = func(context.Context, string) error { return errors.New("exec format error") }
	t.Cleanup(func() { selfTest = original })

	srv := updatertest.Server(t, updatertest.Build(t, "v1.2.3", runtime.GOOS, runtime.GOARCH, "launcher 1.2.3", "kernel 1.2.3"))
	dir, files := installed(t, runtime.GOOS, "launcher 1.0.0", "kernel 1.0.0")
	_, err := update(context.Background(), fakeOptions(srv, dir), runtime.GOOS, runtime.GOARCH)
	if err == nil || !strings.Contains(err.Error(), "put back") {
		t.Fatalf("update() = %v, want the failed self-test to undo it", err)
	}
	assertDir(t, dir, files)
}

func TestOptionsSelfTestReplacesTheCheck(t *testing.T) {
	srv := updatertest.Server(t, updatertest.Build(t, "v1.2.3", runtime.GOOS, runtime.GOARCH, "launcher 1.2.3", "kernel 1.2.3"))
	dir, files := installed(t, runtime.GOOS, "launcher 1.0.0", "kernel 1.0.0")
	opts := fakeOptions(srv, dir)
	var tested string
	opts.SelfTest = func(_ context.Context, path string) error {
		tested = path
		return errors.New("exec format error")
	}
	_, err := update(context.Background(), opts, runtime.GOOS, runtime.GOARCH)
	if err == nil || !strings.Contains(err.Error(), "put back") {
		t.Fatalf("update() = %v, want Options.SelfTest's failure to undo it", err)
	}
	if !strings.HasPrefix(filepath.Base(tested), "compa-kernel") {
		t.Fatalf("self-test ran %q, want the new compa-kernel", tested)
	}
	assertDir(t, dir, files)
}
