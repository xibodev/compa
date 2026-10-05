package pid

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestIsProcessRunningReportsSelfAndExitedChild(t *testing.T) {
	if !IsProcessRunning(os.Getpid()) {
		t.Fatal("IsProcessRunning(self) = false")
	}
	if IsProcessRunning(0) || IsProcessRunning(-1) {
		t.Fatal("IsProcessRunning reported a non-positive PID as running")
	}

	cmd := exitingChild(t)
	if err := cmd.Run(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if IsProcessRunning(cmd.Process.Pid) {
		t.Fatalf("IsProcessRunning(%d) = true after the child exited", cmd.Process.Pid)
	}
}

func TestProcessImagePathNamesThisExecutable(t *testing.T) {
	path, err := ProcessImagePath(os.Getpid())
	if errors.Is(err, errors.ErrUnsupported) {
		t.Skip("no image path on this platform")
	}
	if err != nil {
		t.Fatalf("ProcessImagePath(self) error = %v", err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(filepath.Base(path), filepath.Base(self)) {
		t.Fatalf("ProcessImagePath(self) = %q, want the test binary %q", path, self)
	}
}

func TestProcessImagePathOfAnExitedProcessIsNotFound(t *testing.T) {
	cmd := exitingChild(t)
	if err := cmd.Run(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := ProcessImagePath(cmd.Process.Pid); errors.Is(err, errors.ErrUnsupported) {
		t.Skip("no image path on this platform")
	} else if !errors.Is(err, ErrProcessNotFound) {
		t.Fatalf("ProcessImagePath(exited) error = %v, want ErrProcessNotFound", err)
	}
}

func TestProcessStartFingerprintIsStable(t *testing.T) {
	first := ProcessStartFingerprint(os.Getpid())
	if first == "" {
		if runtime.GOOS == "windows" || runtime.GOOS == "linux" {
			t.Fatal("ProcessStartFingerprint(self) is empty")
		}
		t.Skip("no start fingerprint on this platform")
	}
	if again := ProcessStartFingerprint(os.Getpid()); again != first {
		t.Fatalf("ProcessStartFingerprint changed: %q then %q", first, again)
	}
}

// exitingChild returns a command that exits at once.
func exitingChild(t *testing.T) *exec.Cmd {
	t.Helper()
	if runtime.GOOS == "windows" {
		return exec.Command("cmd", "/c", "exit", "0")
	}
	return exec.Command("true")
}
