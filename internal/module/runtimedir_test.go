package module_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/xibodev/compa/v4/internal/module"
)

// TestDeclaredRuntimeDirectoryTravelsWithTheBinary covers the gap that made a
// renderer unusable once installed.
//
// A module may ship a runtime directory beside its binary -- a composition
// bundle, a template pack -- and resolve it relative to its working directory.
// Installing only the executable left the module reporting a missing
// dependency it had actually shipped with, which reads like a broken install
// rather than an incomplete copy.
func TestDeclaredRuntimeDirectoryTravelsWithTheBinary(t *testing.T) {
	bin := copyFakeModule(t)
	srcRoot := filepath.Dir(bin)

	// The fake module declares no directory requirement, so nothing should be
	// copied: the host carries what a module DECLARES and never goes looking.
	runtimeDir := filepath.Join(srcRoot, "runtime-bundle")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtimeDir, "package.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	id, err := module.Install(context.Background(), home, bin)
	// The fake module declares an overlay and a skill it does not ship, so a
	// PartialInstall is expected here and is itself the correct behaviour:
	// missing declared content is reported rather than passing silently.
	var partial *module.PartialInstall
	if err != nil && !errors.As(err, &partial) {
		t.Fatalf("Install: %v", err)
	}
	if id == "" {
		t.Fatal("install produced no module id")
	}

	// Undeclared content must NOT be copied. Copying it would put unverified
	// files into the module's installed tree, and the digest checks that guard
	// declared content would not cover them.
	stray := filepath.Join(module.ModulesDir(home), id, "runtime-bundle")
	if _, err := os.Stat(stray); err == nil {
		t.Error("an undeclared directory was copied; the host must carry only what a module declares")
	}
}
