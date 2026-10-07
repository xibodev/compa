package config

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func sourceRootsConfig(t *testing.T, roots map[string]string) string {
	t.Helper()
	blob, err := json.Marshal(map[string]any{"modules": map[string]any{"source_roots": roots}})
	if err != nil {
		t.Fatal(err)
	}
	return string(blob)
}

func TestValidateSourceRoot(t *testing.T) {
	abs := t.TempDir() // absolute on every platform
	for _, name := range []string{"notes_store", "sessions_db", "a", "Notes.v2", "x-1"} {
		if err := ValidateSourceRoot(name, abs); err != nil {
			t.Errorf("ValidateSourceRoot(%q, %q) error = %v", name, abs, err)
		}
	}

	for _, tc := range []struct {
		name, path, want string
	}{
		{"", abs, "name"},
		{"_notes", abs, "name"},          // must start with a letter or digit
		{"notes store", abs, "name"},     // no spaces
		{"notes=store", abs, "name"},     // would not survive name=path
		{"notes/store", abs, "name"},     // a name, not a path
		{"notes_store", "", "no path"},   // a name with nowhere to point
		{"notes_store", "  ", "no path"}, // whitespace is not a path either
		{"notes_store", "relative/notes", "not absolute"},
		{"notes_store", "~/notes", "not absolute"}, // ~ is not expanded
	} {
		err := ValidateSourceRoot(tc.name, tc.path)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("ValidateSourceRoot(%q, %q) error = %v, want one mentioning %q",
				tc.name, tc.path, err, tc.want)
		}
	}
}

func TestLoadConfigReadsModuleSourceRoots(t *testing.T) {
	notes := t.TempDir()
	// A file root is as valid as a directory, and loading does not require it
	// to exist yet -- existence is checked when a module is granted it.
	sessions := filepath.Join(t.TempDir(), "not-yet", "sessions.db")

	cfg, err := LoadConfig(writeTestConfig(t, sourceRootsConfig(t, map[string]string{
		"notes_store": notes,
		"sessions_db": sessions,
	})))
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	got := cfg.Modules.SourceRoots
	if len(got) != 2 || got["notes_store"] != notes || got["sessions_db"] != sessions {
		t.Fatalf("modules.source_roots = %#v", got)
	}
}

func TestLoadConfigRejectsMalformedSourceRoots(t *testing.T) {
	abs := t.TempDir()
	for name, tc := range map[string]struct {
		roots map[string]string
		want  string
	}{
		"relative path": {map[string]string{"notes_store": "relative/notes"}, `modules.source_roots["notes_store"]`},
		"empty path":    {map[string]string{"notes_store": ""}, `modules.source_roots["notes_store"]`},
		"bad name":      {map[string]string{"notes store": abs}, `modules.source_roots["notes store"]`},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := LoadConfig(writeTestConfig(t, sourceRootsConfig(t, tc.roots)))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("LoadConfig() error = %v, want the malformed entry %s named", err, tc.want)
			}
		})
	}
}

func TestSourceRootsSurviveASaveAndAreOmittedWhenUnset(t *testing.T) {
	notes := t.TempDir()
	cfg, err := LoadConfig(writeTestConfig(t, sourceRootsConfig(t, map[string]string{"notes_store": notes})))
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	saved := filepath.Join(t.TempDir(), "config.json")
	if err := SaveConfig(saved, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	reloaded, err := LoadConfig(saved)
	if err != nil {
		t.Fatalf("LoadConfig(saved) error = %v", err)
	}
	if reloaded.Modules.SourceRoots["notes_store"] != notes {
		t.Fatalf("source roots after a save = %#v", reloaded.Modules.SourceRoots)
	}

	blob, err := json.Marshal(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), `"modules"`) {
		t.Fatalf("a config with no modules settings serialized a modules key: %s", blob)
	}
}
