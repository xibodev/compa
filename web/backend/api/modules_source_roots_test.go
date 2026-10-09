package api

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xibodev/compa/v4/pkg/modproto"
)

// The Modules page reads the SAME config file the launcher was started with,
// so what it grants is what the gateway it supervises grants.
func TestModulesReadSourceRootsFromTheLauncherConfig(t *testing.T) {
	notes := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "launcher.json")
	blob, err := json.Marshal(map[string]any{
		"modules": map[string]any{"source_roots": map[string]string{"notes_store": notes}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, blob, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := NewHandler(configPath).configuredSourceRoots()
	if err != nil || got["notes_store"] != notes {
		t.Fatalf("configuredSourceRoots() = %v, %v; want notes_store from %s", got, err, configPath)
	}
}

// A declared source root the host will not grant is a host warning on the
// module, including when the config holding the roots could not be read.
func TestSourceRootHostWarnings(t *testing.T) {
	d := &modproto.Descriptor{
		Module:      "source.fixture",
		Permissions: modproto.Permissions{FilesystemRead: []string{"notes_store", "sessions_db"}},
	}

	unconfigured := sourceRootHostWarnings(d, map[string]string{"notes_store": t.TempDir()}, nil)
	if len(unconfigured) != 1 || !strings.Contains(unconfigured[0], `"sessions_db"`) ||
		!strings.Contains(unconfigured[0], "not configured") {
		t.Fatalf("warnings = %v, want one naming the unconfigured sessions_db", unconfigured)
	}

	unreadable := sourceRootHostWarnings(d, nil, errors.New("could not read source roots from config.json"))
	if len(unreadable) != 1 || !strings.Contains(unreadable[0], "notes_store, sessions_db") ||
		!strings.Contains(unreadable[0], "could not read source roots") {
		t.Fatalf("warnings = %v, want one naming the roots and the unreadable config", unreadable)
	}

	if w := sourceRootHostWarnings(&modproto.Descriptor{}, nil, errors.New("ignored")); w != nil {
		t.Fatalf("a module declaring no source roots was warned about: %v", w)
	}
}
