package module_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xibodev/compa/v2/internal/module"
	"github.com/xibodev/compa/v2/pkg/modproto"
)

func TestValidID(t *testing.T) {
	for _, id := range []string{"fake", "archive", "example.render", "some-module_2", "a", strings.Repeat("a", 64)} {
		if !module.ValidID(id) {
			t.Errorf("ValidID(%q) = false, want a usable module ID", id)
		}
	}
	for _, id := range []string{
		"", ".", "..", "../x", `..\x`, "a/b", `a\b`, "Fake", ".hidden", "-x", "_x",
		"fake.", "nul", "con.module", "a b", strings.Repeat("a", 65),
	} {
		if module.ValidID(id) {
			t.Errorf("ValidID(%q) = true; it must not name a module directory", id)
		}
	}
}

// The bug this exists for: Remove("..") deleted the whole Compa home, and
// Remove(".") every installed module. The check compared the ID with
// filepath.Base, which accepts both.
func TestRemoveRefusesAnIDThatIsNotAModule(t *testing.T) {
	home := t.TempDir()
	config := filepath.Join(home, "config.json")
	if err := os.WriteFile(config, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(module.ModulesDir(home), "keep")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"..", ".", "", "../keep", `..\keep`, "keep/..", "Keep", "keep."} {
		if err := module.Remove(home, id); err == nil {
			t.Errorf("Remove(%q) was accepted", id)
		}
	}
	for _, p := range []string{config, other} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("a refused Remove deleted %s: %v", p, err)
		}
	}

	if err := module.Remove(home, "keep"); err != nil {
		t.Fatalf("a real module could not be removed: %v", err)
	}
	if _, err := os.Stat(other); !os.IsNotExist(err) {
		t.Fatalf("the module was not removed: %v", err)
	}
	if _, err := os.Stat(config); err != nil {
		t.Fatalf("removing a module touched the home: %v", err)
	}
}

// A link where a module directory belongs is removed as a link: what it points
// at is not the host's to delete.
func TestRemoveDeletesALinkNotItsTarget(t *testing.T) {
	home := t.TempDir()
	target := t.TempDir()
	keep := filepath.Join(target, "precious.txt")
	if err := os.WriteFile(keep, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(module.ModulesDir(home), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(module.ModulesDir(home), "linked")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}

	if err := module.Remove(home, "linked"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("removing a linked module deleted the link's target: %v", err)
	}
}

// Install records what it verified, and a binary changed afterwards is not run
// until it is installed again.
func TestAChangedBinaryIsNotRunUntilReinstalled(t *testing.T) {
	home := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	id, err := installIgnoringPartial(t, ctx, home, buildFakeModule(t))
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	dir, err := module.InstallDir(home, id)
	if err != nil {
		t.Fatal(err)
	}
	m, err := module.ReadManifest(dir)
	if err != nil || m == nil {
		t.Fatalf("no install manifest: %v", err)
	}
	bin := filepath.Join(dir, module.BinaryName(id))
	digest, err := module.FileDigest(bin)
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != id || m.Dir != id || m.SHA256 != digest {
		t.Fatalf("manifest = %+v, want id/dir %q and the binary's digest %s", m, id, digest)
	}

	r, err := module.NewRunner(home, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Describe(ctx); err != nil {
		t.Fatalf("the installed module does not run: %v", err)
	}

	f, err := os.OpenFile(bin, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("changed after install")); err != nil {
		t.Fatal(err)
	}
	f.Close()

	before := module.ProcessStarts()
	if _, _, err := r.Describe(ctx); err == nil || !strings.Contains(err.Error(), modproto.ErrHostModuleChanged) {
		t.Fatalf("a binary changed after install was run: %v", err)
	}
	if module.ProcessStarts() != before {
		t.Fatal("the changed binary was started")
	}

	if _, err := installIgnoringPartial(t, ctx, home, buildFakeModule(t)); err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	r, _ = module.NewRunner(home, id)
	if _, _, err := r.Describe(ctx); err != nil {
		t.Fatalf("a reinstalled module does not run: %v", err)
	}
}

// Installs of one module are serialised: they used to share one temporary
// file name, so two at once could corrupt each other.
func TestConcurrentInstallsOfOneModuleDoNotCollide(t *testing.T) {
	home := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	errs := make([]error, 3)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = installIgnoringPartial(t, ctx, home, buildFakeModule(t))
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("install %d: %v", i, err)
		}
	}

	entries, err := os.ReadDir(module.ModulesDir(home))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "fake" && e.Name() != ".locks" {
			t.Errorf("an install left %q behind", e.Name())
		}
	}
	r, err := module.NewRunner(home, "fake")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Describe(ctx); err != nil {
		t.Fatalf("the module does not run after concurrent installs: %v", err)
	}
}

// A module the user turned off stays off when it is upgraded.
func TestAnUpgradeKeepsAModuleDisabled(t *testing.T) {
	home := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	id, err := installIgnoringPartial(t, ctx, home, buildFakeModule(t))
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := module.InstallDir(home, id)
	marker := filepath.Join(dir, module.DisabledMarker)
	if err := os.WriteFile(marker, []byte("off"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := installIgnoringPartial(t, ctx, home, buildFakeModule(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("an upgrade turned a disabled module back on: %v", err)
	}
}
