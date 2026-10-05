package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/xibodev/compa/internal/moduletools"
)

// The bug this exists for: `compa-kernel modules-remove ..` passed the
// filepath.Base check and deleted the whole Compa home; "." removed every
// module. The enable, disable and invoke commands take the same argument.
func TestModuleCommandsRefuseAnIDThatIsNotAModule(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMPA_HOME", home)
	keep := filepath.Join(home, "config.json")
	if err := os.WriteFile(keep, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(moduletools.ModulesDir(home), "keep")
	if err := os.MkdirAll(installed, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"..", ".", "../keep", `..\keep`} {
		for _, cmd := range []func() error{
			func() error { c := NewModuleRemoveCommand(); c.SetArgs([]string{id}); return c.Execute() },
			func() error { c := NewModuleDisableCommand(); c.SetArgs([]string{id}); return c.Execute() },
			func() error { c := NewModuleEnableCommand(); c.SetArgs([]string{id}); return c.Execute() },
			func() error { _, err := invokeModule(id, "x", "{}", nil, false); return err },
		} {
			if err := cmd(); err == nil {
				t.Errorf("a module command accepted the ID %q", id)
			}
		}
	}
	for _, p := range []string{keep, installed} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("a refused command deleted %s: %v", p, err)
		}
	}
}
