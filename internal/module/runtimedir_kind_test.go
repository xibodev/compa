package module

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xibodev/compa/pkg/modproto"
)

func TestRuntimeKindDirectoryTravelsWithTheBinary(t *testing.T) {
	srcRoot := t.TempDir()
	destRoot := t.TempDir()
	runtimeDir := filepath.Join(srcRoot, "remotion-composer")
	if err := os.MkdirAll(filepath.Join(runtimeDir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtimeDir, "package.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtimeDir, "src", "Root.tsx"), []byte("export {};"), 0o644); err != nil {
		t.Fatal(err)
	}

	descriptor := &modproto.Descriptor{Requirements: []modproto.Requirement{{
		Name: "remotion-composer", Kind: "runtime",
	}}}
	if warnings := copyDeclaredRuntimeDirs(srcRoot, destRoot, descriptor, ""); len(warnings) != 0 {
		t.Fatalf("copy runtime warnings = %v", warnings)
	}
	for _, rel := range []string{"package.json", filepath.Join("src", "Root.tsx")} {
		if _, err := os.Stat(filepath.Join(destRoot, "remotion-composer", rel)); err != nil {
			t.Fatalf("runtime file %s was not copied: %v", rel, err)
		}
	}
}

// A runtime directory of "." names the folder the binary sits in, and copying
// it took whatever else was there -- a download folder, a source tree.
func TestARuntimeDirectoryOfTheBinarysOwnFolderIsRefused(t *testing.T) {
	srcRoot := t.TempDir()
	destRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcRoot, "unrelated.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{".", "./", "sub/.."} {
		d := &modproto.Descriptor{Requirements: []modproto.Requirement{{Name: name, Kind: "runtime"}}}
		warnings := copyDeclaredRuntimeDirs(srcRoot, destRoot, d, "")
		if len(warnings) == 0 {
			t.Errorf("requirement %q was copied without a warning", name)
		}
	}
	if _, err := os.Stat(filepath.Join(destRoot, "unrelated.txt")); err == nil {
		t.Fatal("the binary's whole folder was copied into the install")
	}
}

// A declared directory holding the host's modules directory is not copied: it
// would copy every installed module, including the staging directory the copy
// is being written into.
func TestARuntimeDirectoryContainingTheModulesDirectoryIsRefused(t *testing.T) {
	srcRoot := t.TempDir()
	modules := filepath.Join(srcRoot, "home", "modules")
	if err := os.MkdirAll(modules, 0o755); err != nil {
		t.Fatal(err)
	}
	d := &modproto.Descriptor{Requirements: []modproto.Requirement{{Name: "home", Kind: "directory"}}}

	warnings := copyDeclaredRuntimeDirs(srcRoot, t.TempDir(), d, modules)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "modules directory") {
		t.Fatalf("warnings = %v, want the modules directory named", warnings)
	}
}

// A swap that cannot complete puts the previous install back, so a failed
// upgrade leaves the working module as it was.
func TestAFailedSwapRestoresThePreviousInstall(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "fake")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "fake.bin"), []byte("working"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := swapInto(dest, filepath.Join(root, ".install-missing")); err == nil {
		t.Fatal("a swap from a missing staging directory succeeded")
	}
	blob, err := os.ReadFile(filepath.Join(dest, "fake.bin"))
	if err != nil || string(blob) != "working" {
		t.Fatalf("the previous install was not restored: %q, %v", blob, err)
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 1 {
		t.Fatalf("the failed swap left %d entries behind", len(entries))
	}
}

func TestASwapReplacesTheInstallWhole(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "fake")
	stage := filepath.Join(root, ".install-fake")
	for dir, body := range map[string]string{dest: "old", stage: "new"} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "fake.bin"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dest, "stale.md"), []byte("old only"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := swapInto(dest, stage); err != nil {
		t.Fatalf("swap: %v", err)
	}
	if blob, _ := os.ReadFile(filepath.Join(dest, "fake.bin")); string(blob) != "new" {
		t.Fatalf("the new install is not in place: %q", blob)
	}
	if _, err := os.Stat(filepath.Join(dest, "stale.md")); err == nil {
		t.Fatal("a file from the previous install survived the upgrade")
	}
	if entries, _ := os.ReadDir(root); len(entries) != 1 {
		t.Fatalf("the swap left %d entries behind", len(entries))
	}
}
