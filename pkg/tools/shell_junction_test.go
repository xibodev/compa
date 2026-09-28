package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// The shell guard's comment said "Check symlinks and junctions" above a bare
// filepath.EvalSymlinks, which does not see a junction. A junction needs no
// privilege to create, so it is the easiest way past the guard and was the one
// path it did not actually check.
func TestShellGuardFollowsAJunctionOutOfTheWorkspace(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("junctions are a Windows construct")
	}

	ws := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	link := filepath.Join(ws, "escape")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, outside).
		CombinedOutput(); err != nil {
		t.Skipf("cannot create a junction here: %v: %s", err, out)
	}

	tool := &ExecTool{workingDir: ws, restrictToWorkspace: true}

	// Lexically inside the workspace: no "..", nothing absolute.
	target := filepath.Join(ws, "escape", "secret.txt")
	if guard := tool.guardCommand("cat "+target, ws); guard == "" {
		t.Fatalf("the guard allowed %q, which reaches outside the workspace"+
			" through a junction", target)
	}
}

// A path inside the workspace must still run: a guard that blocks everything is
// not a guard, and this is the assertion that catches an over-broad fix.
//
// This was skipped on Windows for one cycle with the note that the guard
// already blocked legitimate in-workspace Windows paths. That defect is now
// fixed -- the path regex stopped at the second backslash, so only a drive-root
// prefix was ever validated -- so the skip is gone and the assertion stands.
func TestShellGuardStillAllowsAPathInsideTheWorkspace(t *testing.T) {
	ws := t.TempDir()
	inside := filepath.Join(ws, "ok.txt")
	if err := os.WriteFile(inside, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	tool := &ExecTool{workingDir: ws, restrictToWorkspace: true}

	if guard := tool.guardCommand("cat "+inside, ws); guard != "" {
		t.Fatalf("the guard blocked a file inside the workspace: %s", guard)
	}
}
