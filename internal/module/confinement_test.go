package module

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xibodev/compa/v2/pkg/modproto"
)

// withinRoot is the confinement primitive: every artefact path the host accepts
// passes through it. It was exercised only indirectly, through capabilities and
// API handlers, which means a regression here would surface as a puzzling
// higher-level failure rather than as a broken boundary.
func TestWithinRoot(t *testing.T) {
	root := filepath.FromSlash("/srv/modules/acme")

	cases := []struct {
		path string
		want bool
		why  string
	}{
		{root, true, "the root itself is inside it"},
		{filepath.Join(root, "file.txt"), true, "a direct child"},
		{filepath.Join(root, "a", "b", "c.txt"), true, "a nested child"},
		{filepath.Join(root, "..", "sibling.txt"), false, "climbs out one level"},
		{filepath.Join(root, "..", "..", "etc", "passwd"), false, "climbs out two"},
		{filepath.FromSlash("/srv/modules/other/file.txt"), false, "a different module"},

		// The trap: a sibling whose name STARTS with the root's name. A naive
		// string-prefix check accepts this, and it is a different directory.
		{filepath.FromSlash("/srv/modules/acme-evil/file.txt"), false,
			"a sibling sharing the root's prefix is not inside it"},
		{filepath.FromSlash("/srv/modules/acmex"), false,
			"a sibling one character longer is not inside it"},
	}

	for _, c := range cases {
		if got := withinRoot(root, c.path); got != c.want {
			t.Errorf("withinRoot(%q, %q) = %v, want %v -- %s", root, c.path, got, c.want, c.why)
		}
	}
}

// resolveInRoot refuses an absolute path outright: a module reports paths
// RELATIVE to a root it was granted, and an absolute one cannot be checked
// against that root at all.
func TestResolveInRootRefusesAbsolutePaths(t *testing.T) {
	root := t.TempDir()

	for _, p := range []string{
		filepath.FromSlash("/etc/passwd"),
		filepath.FromSlash(`C:\Windows\System32\config\sam`),
		"/leading-slash.txt",
		`\leading-backslash.txt`,
	} {
		if _, err := resolveInRoot(root, p); err == nil {
			t.Errorf("resolveInRoot accepted the absolute path %q", p)
		}
	}
}

// A relative path inside the root resolves; one that climbs out does not.
func TestResolveInRootHonoursTheBoundary(t *testing.T) {
	root := t.TempDir()

	abs, err := resolveInRoot(root, "sub/file.txt")
	if err != nil {
		t.Fatalf("an in-root path was refused: %v", err)
	}
	if !withinRoot(filepath.Clean(root), abs) {
		t.Fatalf("resolved %q, which is not inside %q", abs, root)
	}

	if _, err := resolveInRoot(root, "../escape.txt"); err == nil {
		t.Error("resolveInRoot accepted a path that climbs out of its root")
	}
}

// tempOnlyEnv is what keeps a module from inheriting the host's environment.
//
// It matters concretely rather than abstractly: ELEVENLABS_API_KEY is set on
// the machine this was developed on, and a module that inherited it could reach
// a paid provider without ever being granted that credential. The module
// declares what it needs and the host supplies exactly that, per invocation.
//
// A temp directory is the one exception, because os.MkdirTemp fails with no
// environment at all -- which surfaced as "unable to create staging directory"
// and read like a permissions problem.
func TestTempOnlyEnvCarriesNothingButTemp(t *testing.T) {
	env := tempOnlyEnv()

	for _, entry := range env {
		name, _, found := strings.Cut(entry, "=")
		if !found {
			t.Errorf("malformed environment entry %q", entry)
			continue
		}
		switch name {
		case "TEMP", "TMP", "TMPDIR":
			// The deliberate exception.
		default:
			t.Errorf("module environment carries %q, which the host never granted", name)
		}
	}

	// And it must actually carry a temp dir, or staging fails in a way that
	// reads like a permissions problem.
	if os.TempDir() != "" && len(env) == 0 {
		t.Fatal("no temp directory supplied; module staging will fail obscurely")
	}
}

// No credential the host happens to hold reaches a module through the
// environment, whatever is set on the machine.
func TestNoHostCredentialLeaksIntoModuleEnv(t *testing.T) {
	t.Setenv("ELEVENLABS_API_KEY", "secret-should-not-leak")
	t.Setenv("OPENAI_API_KEY", "secret-should-not-leak")

	for _, entry := range tempOnlyEnv() {
		if strings.Contains(entry, "secret-should-not-leak") {
			t.Fatalf("a host credential reached the module environment: %q", entry)
		}
	}
}

// A path the host must reject cannot depend on where the host runs.
//
// Three call sites used filepath.IsAbs, which on Linux is literally
// HasPrefix("/") -- verified in Go's own source. A module emitting a
// drive-letter path would have been accepted on a Linux host and rejected on a
// Windows one, which is the worst shape for a security check: it passes every
// test on the developer's machine.
func TestAbsolutePathRejectionIsPlatformIndependent(t *testing.T) {
	mustReject := []string{
		`C:\Windows\System32\config\sam`,
		"C:/Windows/System32/config/sam",
		`D:\secrets.txt`,
		"/etc/passwd",
		`\server\share\file`,
	}
	for _, p := range mustReject {
		if !modproto.IsAbsolutePath(p) {
			t.Errorf("IsAbsolutePath(%q) = false; a module could name it on some host", p)
		}
	}

	mustAccept := []string{"agents/o.md", "skills/x/SKILL.md", "file.txt", ""}
	for _, p := range mustAccept {
		if modproto.IsAbsolutePath(p) {
			t.Errorf("IsAbsolutePath(%q) = true; a legitimate module-relative path was refused", p)
		}
	}

	// "c:" without a separator is a relative path named "c:", not a drive.
	if modproto.IsAbsolutePath("c:file.txt") {
		t.Error(`IsAbsolutePath("c:file.txt") = true; that is a relative name, not a drive path`)
	}
}
