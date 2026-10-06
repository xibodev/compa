package module

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/xibodev/compa/v2/pkg/modproto"
)

// The physical half of confinement -- the EvalSymlinks pass that catches what a
// lexical check cannot -- had no test. The lexical half did, which is the more
// dangerous arrangement: the covered branch is the one an attacker does not
// need, and a mutation to the symlink check killed nothing.
//
// A directory symlink needs SeCreateSymbolicLinkPrivilege on Windows, so this
// uses a junction there (mklink /J), which any user may create and which
// EvalSymlinks resolves the same way.
func TestSymlinkedDirectoryCannotSmuggleAPathOutOfItsRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("not yours"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}

	link := filepath.Join(root, "escape")
	linkDir(t, link, outside)

	// Lexically this is inside the root: no "..", nothing absolute.
	_, err := resolveInRoot(root, "escape/secret.txt")

	if err == nil {
		t.Fatal("a link inside the root reached a file outside it, and the" +
			" host would have served that file")
	}
	if !strings.Contains(err.Error(), "via a link") {
		t.Fatalf("refused for the wrong reason, so the physical check may not"+
			" be what refused it: %v", err)
	}
}

// The same link, reached through the artifact path a module actually reports.
func TestArtifactCannotEscapeThroughALink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	linkDir(t, filepath.Join(root, "escape"), outside)

	req := &modproto.Request{Roots: map[string]modproto.Root{
		"project_root": {Path: root, Mode: "rw"},
	}}

	_, err := ResolveArtifact(req, modproto.Artifact{
		Root: "project_root", Path: "escape/secret.txt",
	})
	if err == nil {
		t.Fatal("a module escaped its root by reporting an artifact through a link")
	}
}

// A link that stays INSIDE the root is legitimate and must still resolve: a
// confinement check that refuses everything is not a confinement check.
func TestALinkThatStaysInsideTheRootIsAllowed(t *testing.T) {
	root := t.TempDir()

	real := filepath.Join(root, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(real, "ok.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	linkDir(t, filepath.Join(root, "alias"), real)

	if _, err := resolveInRoot(root, "alias/ok.txt"); err != nil {
		t.Fatalf("a link within the root was refused: %v", err)
	}
}

// A ROOT that is itself a link must still serve the files inside it.
//
// The root was resolved with filepath.EvalSymlinks, which stopped following
// junctions in Go 1.23, while the path inside it went through pathlink. The
// root resolved to the junction and the path to its target, so every artifact
// under a junctioned workspace was refused as escaping it.
func TestAnArtifactUnderALinkedRootResolves(t *testing.T) {
	real := t.TempDir()
	if err := os.MkdirAll(filepath.Join(real, "out"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "out", "render.mp4"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "workspace")
	linkDir(t, root, real)

	req := &modproto.Request{Roots: map[string]modproto.Root{"workspace": {Path: root, Mode: "rw"}}}
	got, err := ResolveArtifact(req, modproto.Artifact{Root: "workspace", Path: "out/render.mp4"})
	if err != nil {
		t.Fatalf("an artifact inside a linked root was refused: %v", err)
	}
	if got != filepath.Join(root, "out", "render.mp4") {
		t.Fatalf("resolved %q", got)
	}
}

// linkDir makes name a directory link to target, skipping the test when the
// platform will not allow one without elevation.
func linkDir(t *testing.T, name, target string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		// A junction needs no privilege; a symlink needs
		// SeCreateSymbolicLinkPrivilege, which a developer shell rarely holds.
		if err := runMklinkJunction(name, target); err != nil {
			t.Skipf("cannot create a directory junction here: %v", err)
		}
		return
	}
	if err := os.Symlink(target, name); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}
}
