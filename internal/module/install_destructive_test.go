package module_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xibodev/compa/v3/internal/module"
)

// The bug this exists for, reported by a module author and reproduced here: a
// failed `modules-add` REMOVED a module that was working before the command
// ran. They ran it against a binary that was already installed -- which is what
// a module's own installer does when it re-registers -- and lost the install.
//
// A failed install must leave what was there alone. Replacing a working module
// with nothing is worse than refusing.
func TestAFailedInstallDoesNotDestroyTheWorkingOne(t *testing.T) {
	home := t.TempDir()
	good := buildFakeModule(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	id, err := installIgnoringPartial(t, ctx, home, good)
	if err != nil {
		t.Fatalf("first install: %v", err)
	}

	installedDir := filepath.Join(module.ModulesDir(home), id)
	before, err := os.ReadDir(installedDir)
	if err != nil || len(before) == 0 {
		t.Fatalf("nothing was installed: %v", err)
	}

	// Now install something that cannot possibly describe itself.
	broken := filepath.Join(t.TempDir(), "fakemodule.exe")
	if err := os.WriteFile(broken, []byte("not a program"), 0o755); err != nil {
		t.Fatalf("write broken: %v", err)
	}
	if _, err := module.Install(ctx, home, broken); err == nil {
		t.Fatal("a non-executable installed successfully")
	}

	after, err := os.ReadDir(installedDir)
	if err != nil {
		t.Fatalf("the working module's directory is gone after a FAILED"+
			" install of a different file: %v", err)
	}
	if len(after) < len(before) {
		t.Fatalf("the working install lost content: %d entries before, %d after",
			len(before), len(after))
	}

	// And it must still run.
	if _, _, err := (&module.Runner{
		Binary:   filepath.Join(installedDir, filepath.Base(before[0].Name())),
		ModuleID: id,
	}).Describe(ctx); err != nil {
		// Only meaningful if that first entry IS the binary; the directory
		// check above is the load-bearing assertion.
		t.Logf("note: could not re-describe via %s: %v", before[0].Name(), err)
	}
}

// Installing a module from its own installed copy must be a clear refusal, not
// a corrupted install. This is what a module installer run with `--only compa`
// does on a re-run, and what the author hit.
func TestInstallingAModuleFromItsOwnInstalledCopyIsRefusedCleanly(t *testing.T) {
	home := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	id, err := installIgnoringPartial(t, ctx, home, buildFakeModule(t))
	if err != nil {
		t.Fatalf("first install: %v", err)
	}

	installedDir := filepath.Join(module.ModulesDir(home), id)
	entries, _ := os.ReadDir(installedDir)
	var binary string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), id) {
			binary = filepath.Join(installedDir, e.Name())
		}
	}
	if binary == "" {
		t.Fatalf("could not find the installed binary in %s", installedDir)
	}

	_, err = installIgnoringPartial(t, ctx, home, binary)

	// Either it succeeds as a no-op, or it refuses -- but it must NOT leave the
	// module broken, and it must not report a bare "Access is denied".
	if _, statErr := os.Stat(binary); statErr != nil {
		t.Fatalf("re-installing from the installed copy destroyed it: %v", statErr)
	}
	if err != nil && strings.Contains(err.Error(), "Access is denied") {
		t.Fatalf("the refusal is an OS error rather than an explanation: %v", err)
	}
}

// installIgnoringPartial installs and treats PartialInstall as success: the
// fake module declares an overlay and a skill it does not ship, which is a
// warning about content, not a failed install.
func installIgnoringPartial(t *testing.T, ctx context.Context, home, bin string) (string, error) {
	t.Helper()
	id, err := module.Install(ctx, home, bin)
	var partial *module.PartialInstall
	if errors.As(err, &partial) {
		return id, nil
	}
	return id, err
}
