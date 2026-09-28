package moduletools

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/xibodev/compa/pkg/modproto"
)

func installedModule(t *testing.T, home, id string) *modproto.Descriptor {
	t.Helper()
	dir := filepath.Join(ModulesDir(home), id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	return &modproto.Descriptor{Module: id}
}

// A module that declares a bundle root is handed its own install directory --
// the only party that knows where the host copied its runtime content is the
// host.
func TestBundleRootIsGrantedWhenDeclared(t *testing.T) {
	home := t.TempDir()
	d := installedModule(t, home, "test.module")
	d.Permissions.FilesystemRead = []string{"test_bundle"}

	roots := GrantRoots(d, home, filepath.Join(home, "workspace"), nil)

	root, ok := roots["test_bundle"]
	if !ok {
		t.Fatal("declared bundle root was not granted")
	}
	if root.Mode != "ro" {
		t.Fatalf("bundle root mode = %q, want ro: content verified at install"+
			" time must not be writable afterwards", root.Mode)
	}
	want := filepath.Join(ModulesDir(home), "test.module")
	if root.Path != want {
		t.Fatalf("bundle root = %q, want %q", root.Path, want)
	}
}

// A root the module never declared is never supplied, bundle or otherwise.
func TestBundleRootNotGrantedWhenUndeclared(t *testing.T) {
	home := t.TempDir()
	d := installedModule(t, home, "test.module")

	for name, r := range GrantRoots(d, home, filepath.Join(home, "workspace"), nil) {
		if isBundleRootName(name) {
			t.Fatalf("granted undeclared bundle root %q -> %s", name, r.Path)
		}
	}
}

// A module that is not installed gets no bundle root rather than a path that
// does not exist -- "absent" and "present but broken" must stay distinguishable.
func TestBundleRootAbsentWhenNotInstalled(t *testing.T) {
	home := t.TempDir()
	d := &modproto.Descriptor{Module: "never.installed"}
	d.Permissions.FilesystemRead = []string{"never_bundle"}

	if _, ok := GrantRoots(d, home, "", nil)["never_bundle"]; ok {
		t.Fatal("granted a bundle root for a module with no install directory")
	}
}

func TestIsBundleRootName(t *testing.T) {
	for _, name := range []string{"bundle", "app_bundle", "x_bundle"} {
		if !isBundleRootName(name) {
			t.Errorf("%q should be a bundle root name", name)
		}
	}
	// Source roots and state roots must not be mistaken for bundle roots, or a
	// read-only install directory would shadow a module's writable state.
	for _, name := range []string{"workspace", "project_root", "notes_store", "bundled"} {
		if isBundleRootName(name) {
			t.Errorf("%q should NOT be a bundle root name", name)
		}
	}
}
