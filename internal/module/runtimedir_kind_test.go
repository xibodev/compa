package module

import (
	"os"
	"path/filepath"
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
	if warnings := copyDeclaredRuntimeDirs(srcRoot, destRoot, descriptor); len(warnings) != 0 {
		t.Fatalf("copy runtime warnings = %v", warnings)
	}
	for _, rel := range []string{"package.json", filepath.Join("src", "Root.tsx")} {
		if _, err := os.Stat(filepath.Join(destRoot, "remotion-composer", rel)); err != nil {
			t.Fatalf("runtime file %s was not copied: %v", rel, err)
		}
	}
}
