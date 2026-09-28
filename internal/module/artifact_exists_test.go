package module

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xibodev/compa/pkg/modproto"
)

// The bug this exists for: a tool was given a relative output_path, wrote the
// file into the HOST's repository root -- outside every granted root -- and
// then reported an artifact claiming the file sat in its granted root, with a
// matching byte count.
//
// Every shape check passed. The path was relative, it named a supplied root, it
// resolved inside that root, and the digest was well formed. The record was
// simply false, and the cockpit would have offered a download link and a
// provenance digest for a file that was not there.
//
// Resolving a path proves where it WOULD be, not that anything is there.
func TestArtifactMustExistWhereItClaims(t *testing.T) {
	root := t.TempDir()
	req := &modproto.Request{
		Roots: map[string]modproto.Root{"project_root": {Path: root, Mode: "rw"}},
	}

	// Well-formed and in-root, but nothing was written there.
	missing := modproto.Artifact{
		ID: "ghost.mp4", Root: "project_root", Path: "ghost.mp4",
		Digest: "sha256:" + strings.Repeat("a", 64),
	}

	if _, err := ResolveArtifact(req, missing); err != nil {
		t.Fatalf("path resolution failed, so this test would pass for the wrong"+
			" reason: %v", err)
	}

	if _, err := os.Stat(filepath.Join(root, "ghost.mp4")); err == nil {
		t.Fatal("test setup wrote the file; it must be absent")
	}
}

// The positive half: a file that IS where it claims resolves and exists, so the
// check cannot be passing merely by rejecting everything.
func TestArtifactThatExistsResolves(t *testing.T) {
	root := t.TempDir()
	req := &modproto.Request{
		Roots: map[string]modproto.Root{"project_root": {Path: root, Mode: "rw"}},
	}

	real := filepath.Join(root, "real.mp4")
	if err := os.WriteFile(real, []byte("bytes"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	abs, err := ResolveArtifact(req, modproto.Artifact{
		ID: "real.mp4", Root: "project_root", Path: "real.mp4",
	})
	if err != nil {
		t.Fatalf("ResolveArtifact on a real file: %v", err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("resolved path does not exist: %v", err)
	}
}

// A path that escapes its root is still refused before existence is consulted:
// a file that exists somewhere outside the root must not become admissible by
// being real.
func TestEscapingArtifactRefusedEvenWhenFileExists(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.mp4")
	if err := os.WriteFile(outside, []byte("bytes"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	req := &modproto.Request{
		Roots: map[string]modproto.Root{"project_root": {Path: root, Mode: "rw"}},
	}

	if _, err := ResolveArtifact(req, modproto.Artifact{
		ID: "outside.mp4", Root: "project_root", Path: "../outside.mp4",
	}); err == nil {
		t.Fatal("an escaping path was accepted because the file happened to exist")
	}
}

// A module removed while an agent still holds its tools must say so.
//
// Windows reports "the directory name is invalid" for a missing install
// directory, which reads like a broken module rather than an absent one. That
// distinction decides what an agent does next: "broken" invites working around
// it, "uninstalled" is a fact to report.
func TestRemovedModuleSaysItIsUninstalled(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "vanished", "vanished.exe")
	r := &Runner{Binary: gone, ModuleID: "vanished"}

	_, _, err := r.Describe(context.Background())
	if err == nil {
		t.Fatal("describing a module that is not installed succeeded")
	}
	msg := err.Error()
	if !strings.Contains(msg, "no longer installed") {
		t.Fatalf("error does not name the cause:\n%s", msg)
	}
	if !strings.Contains(msg, "vanished.exe") {
		t.Fatalf("error does not name the missing binary:\n%s", msg)
	}
}

// Content whose bytes do not match its declared digest is REFUSED when loaded
// into agent context -- correctly, since a module's documentation is untrusted
// input. But that refusal is silent from the user's point of view: the module
// installs cleanly and its overlay simply never appears in any turn.
//
// Observed: a module edited its overlay without rebuilding, so the declared
// digest described content it no longer shipped. Install reported zero
// warnings; the overlay stopped loading with no visible reason.
func TestInstallWarnsOnStaleDigest(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()

	overlay := filepath.Join(src, "agents")
	if err := os.MkdirAll(overlay, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(overlay, "o.md"), []byte("actual content"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	d := &modproto.Descriptor{Module: "test.module"}
	d.AgentOverlays = []modproto.Overlay{{
		ID: "test.overlay", Path: "agents/o.md",
		// A digest for content this module no longer ships.
		Digest: "sha256:" + strings.Repeat("0", 64),
	}}

	warnings := copyDeclaredContent(src, dest, d)

	if len(warnings) == 0 {
		t.Fatal("stale digest installed with no warning: the overlay will be" +
			" refused at load time and nothing will say why")
	}
	joined := strings.Join(warnings, " ")
	for _, want := range []string{"does not match its declared digest", "agents/o.md", "REFUSED"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warning does not contain %q:\n%s", want, joined)
		}
	}
	// The file is still copied: a stale digest is a mistake in the module, not
	// a reason to withhold its capabilities.
	if _, err := os.Stat(filepath.Join(dest, "agents", "o.md")); err != nil {
		t.Fatalf("content was not copied despite the warning: %v", err)
	}
}

// Matching content installs silently. A warning on every install would train
// the reader to ignore warnings.
func TestInstallSilentWhenDigestMatches(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()

	body := []byte("actual content")
	if err := os.MkdirAll(filepath.Join(src, "agents"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "agents", "o.md"), body, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	d := &modproto.Descriptor{Module: "test.module"}
	d.AgentOverlays = []modproto.Overlay{{
		ID: "test.overlay", Path: "agents/o.md", Digest: modproto.DigestSHA256(body),
	}}

	if warnings := copyDeclaredContent(src, dest, d); len(warnings) != 0 {
		t.Fatalf("matching content warned: %v", warnings)
	}
}
