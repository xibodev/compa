package fstools

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// A junction is a link the workspace check must follow, and it is the one an
// unprivileged process can actually create on Windows: mklink /J needs no
// SeCreateSymbolicLinkPrivilege, while a symlink does. If the workspace
// boundary follows symlinks but not junctions, the boundary is advisory on the
// platform where it is easiest to cross.
func TestJunctionCannotReachOutsideTheWorkspace(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("junctions are a Windows construct")
	}

	ws := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("not yours"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}

	link := filepath.Join(ws, "escape")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, outside).CombinedOutput(); err != nil {
		t.Skipf("cannot create a junction here: %v: %s", err, out)
	}

	// Lexically inside the workspace: no "..", nothing absolute.
	_, err := validatePathWithAllowPaths(
		filepath.Join(ws, "escape", "secret.txt"), ws, true, nil)

	if err == nil {
		t.Fatal("a junction inside the workspace reached a file outside it")
	}
}

// A junction that stays INSIDE the workspace must still be usable: a boundary
// that refuses everything is not a boundary.
func TestJunctionWithinTheWorkspaceIsAllowed(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("junctions are a Windows construct")
	}

	ws := t.TempDir()
	real := filepath.Join(ws, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(real, "ok.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if out, err := exec.Command("cmd", "/c", "mklink", "/J",
		filepath.Join(ws, "alias"), real).CombinedOutput(); err != nil {
		t.Skipf("cannot create a junction here: %v: %s", err, out)
	}

	if _, err := validatePathWithAllowPaths(
		filepath.Join(ws, "alias", "ok.txt"), ws, true, nil); err != nil {
		t.Fatalf("a junction within the workspace was refused: %v", err)
	}
}
