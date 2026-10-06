package module_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/xibodev/compa/v2/internal/module"
)

// The bug this exists for: a module that describes itself in about a second
// took 11.8s once -- immediately after its build wrote the 13MB file, almost
// certainly the virus scanner reading it -- and the install refused with
// "does not speak the module protocol". It spoke it perfectly. The retry
// succeeded.
//
// A timeout and a protocol failure need OPPOSITE responses from the author:
// run it again, versus go and fix your module. Reporting one as the other
// sends them to rewrite something that was never broken.
func TestASlowModuleIsNotCalledAProtocolFailure(t *testing.T) {
	home := t.TempDir()

	// A binary that exists and is executable but never answers: the host's
	// deadline is what ends it, which is the case under test.
	sleeper := buildSleeper(t)

	_, err := module.Install(context.Background(), home, sleeper)

	if err == nil {
		t.Fatal("a module that never describes itself was installed")
	}
	msg := err.Error()
	if strings.Contains(msg, "does not speak the module protocol") {
		t.Fatalf("a timeout was reported as a protocol failure, which sends the"+
			" author to rewrite a working module:\n%s", msg)
	}
	for _, want := range []string{"timeout", "try again"} {
		if !strings.Contains(strings.ToLower(msg), want) {
			t.Errorf("the refusal does not say %q, so the author cannot tell it"+
				" is worth retrying:\n%s", want, msg)
		}
	}
}

// A binary that genuinely does not speak the protocol must still say so --
// otherwise every real failure reads as "try again" and nobody fixes anything.
func TestARealProtocolFailureIsStillReportedAsOne(t *testing.T) {
	home := t.TempDir()
	bad := filepath.Join(t.TempDir(), "notamodule.exe")
	if err := os.WriteFile(bad, []byte("not a program"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := module.Install(context.Background(), home, bad)

	if err == nil {
		t.Fatal("a non-executable was installed")
	}
	if !strings.Contains(err.Error(), "does not speak the module protocol") {
		t.Fatalf("a real protocol failure was not reported as one:\n%v", err)
	}
}

// buildSleeper compiles a program that outlives any describe deadline.
func buildSleeper(t *testing.T) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "main.go")
	body := "package main\n\nimport \"time\"\n\nfunc main() { time.Sleep(90 * time.Second) }\n"
	if err := os.WriteFile(src, []byte(body), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	bin := filepath.Join(t.TempDir(), "sleeper")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if out, err := buildGo(ctx, bin, src); err != nil {
		t.Fatalf("cannot build the sleeper: %v: %s", err, out)
	}
	return bin
}

func buildGo(ctx context.Context, out, src string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "go", "build", "-o", out, src)
	return cmd.CombinedOutput()
}

// The three callers -- CLI, agent, cockpit -- must allow a capability the SAME
// time to run.
//
// They each had their own number: the CLI 120s, the other two 180s. A render
// that worked through the browser failed through the CLI with
// "command_timeout: node was cancelled or timed out", which reads as a broken
// module rather than a shorter leash. Fourth divergence found between these
// same three paths, after grantRoots, ApplyGrants and the seed request shape.
func TestTheInvokeDeadlineIsSharedNotCopied(t *testing.T) {
	if module.DefaultInvokeDeadlineMS <= 0 {
		t.Fatal("no default invoke deadline")
	}
	// Video rendering sets the floor: a real Remotion render measured 1m55s
	// against a 120s budget and was killed mid-render.
	if module.DefaultInvokeDeadlineMS < 120000 {
		t.Fatalf("the default deadline is %dms, below the measured cost of a"+
			" real render", module.DefaultInvokeDeadlineMS)
	}
}
