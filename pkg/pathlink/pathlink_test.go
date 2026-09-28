package pathlink

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// linkDir makes name a link to target, skipping when the platform will not
// allow one without elevation.
func linkDir(t *testing.T, name, target string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		// A junction needs no privilege; a symlink needs
		// SeCreateSymbolicLinkPrivilege, which a developer shell rarely holds.
		if out, err := exec.Command("cmd", "/c", "mklink", "/J", name, target).
			CombinedOutput(); err != nil {
			t.Skipf("cannot create a junction here: %v: %s", err, out)
		}
		return
	}
	if err := os.Symlink(target, name); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}
}

// The case the whole package exists for: a file reached THROUGH a link must
// resolve to where it really is, not to where the path says it is.
func TestResolveFollowsALinkToItsRealLocation(t *testing.T) {
	inside := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	linkDir(t, filepath.Join(inside, "escape"), outside)

	got, err := Resolve(filepath.Join(inside, "escape", "secret.txt"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	real, _ := filepath.EvalSymlinks(outside)
	if !strings.HasPrefix(strings.ToLower(got), strings.ToLower(real)) {
		t.Fatalf("Resolve(%s) = %q, which does not point at %q -- a caller"+
			" comparing this against a root would allow the escape",
			"escape/secret.txt", got, real)
	}
}

// A path with no links resolves to itself, so an ordinary file is unaffected.
func TestResolveLeavesAnOrdinaryPathAlone(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "plain.txt")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := Resolve(p)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	real, _ := filepath.EvalSymlinks(p)
	if !strings.EqualFold(got, real) {
		t.Fatalf("Resolve(%q) = %q, want %q", p, got, real)
	}
}

// A path that does not exist is legitimate -- a caller naming a file it is
// about to write -- and must resolve rather than error, so the caller's own
// lexical check governs it.
func TestResolveToleratesAnAbsentPath(t *testing.T) {
	dir := t.TempDir()

	got, err := Resolve(filepath.Join(dir, "not", "here", "yet.txt"))
	if err != nil {
		t.Fatalf("an absent path must not be an error: %v", err)
	}
	if got == "" {
		t.Fatal("an absent path resolved to nothing")
	}
}

// A link cycle must terminate. Without a hop bound this hangs the host.
func TestResolveDoesNotSpinOnALinkCycle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mklink /J refuses to create a cycle")
	}
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	if err := os.Symlink(b, a); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}
	if err := os.Symlink(a, b); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}

	if _, err := Resolve(filepath.Join(a, "x")); err == nil {
		t.Fatal("a link cycle resolved instead of erroring")
	}
}

// IsLink must see a junction, which reports neither ModeSymlink nor ModeDir.
func TestIsLinkSeesAJunction(t *testing.T) {
	dir := t.TempDir()
	target := t.TempDir()
	link := filepath.Join(dir, "link")
	linkDir(t, link, target)

	if !IsLink(link) {
		t.Fatal("a link was not recognised as one, so callers will treat it as" +
			" an ordinary directory")
	}
	if IsLink(target) {
		t.Fatal("an ordinary directory was reported as a link")
	}
}
