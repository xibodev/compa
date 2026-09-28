package moduletools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xibodev/compa/pkg/config"
	"github.com/xibodev/compa/pkg/modproto"
)

// sourceRootDescriptor declares two source roots: a store kept as a directory
// and one kept as a single database file.
func sourceRootDescriptor() *modproto.Descriptor {
	return &modproto.Descriptor{
		Module: "source.fixture",
		Permissions: modproto.Permissions{
			FilesystemRead: []string{"notes_store", "sessions_db"},
		},
	}
}

// sourceRootFixtures creates a directory root and a file root on disk.
func sourceRootFixtures(t *testing.T) (notesDir, sessionsFile string) {
	t.Helper()
	notesDir = filepath.Join(t.TempDir(), "notes")
	if err := os.MkdirAll(notesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionsFile = filepath.Join(t.TempDir(), "sessions.db")
	if err := os.WriteFile(sessionsFile, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	return notesDir, sessionsFile
}

// Only the names the host cannot fill itself are source roots: not the
// workspace, not the module's bundle, and not a root the module also writes,
// which is its private state.
func TestSourceRootNamesAreWhatOnlyTheOperatorCanSupply(t *testing.T) {
	d := &modproto.Descriptor{Permissions: modproto.Permissions{
		FilesystemRead:  []string{"workspace", "notes_store", "app_bundle", "cache", "sessions_db", "notes_store"},
		FilesystemWrite: []string{"workspace", "cache"},
	}}
	got := SourceRootNames(d)
	if strings.Join(got, ",") != "notes_store,sessions_db" {
		t.Fatalf("SourceRootNames = %v, want [notes_store sessions_db]", got)
	}
	if SourceRootNames(nil) != nil {
		t.Fatal("a nil descriptor declared source roots")
	}
}

// A configured root is granted read-only, whether it is a directory or a
// single file: the host hands it over as-is and the module decides what to do
// with it.
func TestGrantRootsGrantsConfiguredDirectoriesAndFilesReadOnly(t *testing.T) {
	notes, sessions := sourceRootFixtures(t)
	roots := GrantRoots(sourceRootDescriptor(), t.TempDir(), "", map[string]string{
		"notes_store": notes + string(filepath.Separator),
		"sessions_db": sessions,
	})
	for name, want := range map[string]string{"notes_store": notes, "sessions_db": sessions} {
		root, ok := roots[name]
		if !ok || root.Path != filepath.Clean(want) || root.Mode != "ro" {
			t.Fatalf("root %s = %#v, want %q read-only", name, root, filepath.Clean(want))
		}
	}
}

// A root that does not resolve is left out, never replaced with something
// else, and a configured name the module never declared is never supplied.
func TestGrantRootsLeavesOutWhatDoesNotResolve(t *testing.T) {
	notes, _ := sourceRootFixtures(t)
	roots := GrantRoots(sourceRootDescriptor(), t.TempDir(), "", map[string]string{
		"notes_store":  "relative/notes",
		"sessions_db":  filepath.Join(t.TempDir(), "missing.db"),
		"undeclared_x": notes,
	})
	for _, name := range []string{"notes_store", "sessions_db", "undeclared_x"} {
		if root, ok := roots[name]; ok {
			t.Fatalf("root %s was granted as %#v", name, root)
		}
	}
}

// Compa ships no source roots. A module declaring a name gets nothing for it
// until the operator configures one -- however the user's home happens to look.
func TestGrantRootsHasNoBuiltInSourceRoots(t *testing.T) {
	ambient := t.TempDir()
	t.Setenv("HOME", ambient)
	t.Setenv("USERPROFILE", ambient)
	for _, dir := range []string{"notes_store", ".notes_store", "sessions_db", ".sessions_db"} {
		if err := os.MkdirAll(filepath.Join(ambient, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	roots := GrantRoots(sourceRootDescriptor(), t.TempDir(), "", nil)
	for _, name := range []string{"notes_store", "sessions_db"} {
		if root, ok := roots[name]; ok {
			t.Fatalf("%s was granted without being configured: %#v", name, root)
		}
	}
}

// The host's own roots always win. A source root named like one of them must
// not turn the workspace read-only, hide the bundle, or shadow private state.
func TestASourceRootNeverReplacesAHostRoot(t *testing.T) {
	home := t.TempDir()
	d := installedModule(t, home, "test.module")
	d.Permissions.FilesystemRead = []string{"workspace", "test_bundle", "cache"}
	d.Permissions.FilesystemWrite = []string{"cache"}
	elsewhere, _ := sourceRootFixtures(t)
	workspace := filepath.Join(home, "workspace")

	roots := GrantRoots(d, home, workspace, map[string]string{
		"workspace":   elsewhere,
		"test_bundle": elsewhere,
		"cache":       elsewhere,
	})
	for name, root := range roots {
		if root.Path == elsewhere {
			t.Errorf("source root replaced the host's %q root: %#v", name, root)
		}
	}
	if roots["workspace"].Mode != "rw" || roots["cache"].Mode != "rw" || roots["test_bundle"].Mode != "ro" {
		t.Fatalf("host roots changed: %#v", roots)
	}
}

func TestResolveSourceRoot(t *testing.T) {
	notes, sessions := sourceRootFixtures(t)
	for name, path := range map[string]string{"notes_store": notes, "sessions_db": sessions} {
		got, err := ResolveSourceRoot(name, path)
		if err != nil || got != filepath.Clean(path) {
			t.Errorf("ResolveSourceRoot(%q, %q) = %q, %v", name, path, got, err)
		}
	}

	missing := filepath.Join(t.TempDir(), "missing")
	for _, tc := range []struct {
		name, path, want string
	}{
		{"notes_store", "relative/notes", "not absolute"},
		{"notes_store", "", "no path"},
		{"notes_store", missing, "does not exist"},
		{"notes store", notes, "name"},
		{"workspace", notes, "host supplies"},
		{"app_bundle", notes, "host supplies"},
	} {
		got, err := ResolveSourceRoot(tc.name, tc.path)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("ResolveSourceRoot(%q, %q) = %q, %v; want an error mentioning %q",
				tc.name, tc.path, got, err, tc.want)
		}
	}
}

// A root the host will not grant is reported in terms the operator can act on:
// which name, why, and exactly what to configure.
func TestSourceRootWarningsAreActionable(t *testing.T) {
	notes, _ := sourceRootFixtures(t)
	missing := filepath.Join(t.TempDir(), "missing.db")

	warnings := SourceRootWarnings(sourceRootDescriptor(), map[string]string{
		"sessions_db": missing,
	})
	if len(warnings) != 2 {
		t.Fatalf("got %d warnings, want one per ungranted root: %v", len(warnings), warnings)
	}
	unconfigured, invalid := warnings[0], warnings[1]
	for _, want := range []string{`"notes_store"`, "not configured", `"modules": {"source_roots": {"notes_store"`, "--source-root notes_store="} {
		if !strings.Contains(unconfigured, want) {
			t.Errorf("unconfigured warning does not say %q: %s", want, unconfigured)
		}
	}
	for _, want := range []string{`"sessions_db"`, "not granted", "does not exist", "modules.source_roots.sessions_db"} {
		if !strings.Contains(invalid, want) {
			t.Errorf("invalid-path warning does not say %q: %s", want, invalid)
		}
	}

	if w := SourceRootWarnings(sourceRootDescriptor(), map[string]string{
		"notes_store": notes, "sessions_db": notes,
	}); len(w) != 0 {
		t.Errorf("fully configured roots were warned about: %v", w)
	}
	if w := SourceRootWarnings(&modproto.Descriptor{}, nil); len(w) != 0 {
		t.Errorf("a module declaring no source roots was warned about: %v", w)
	}
}

// The agent is handed only the state root, so it reads the operator's roots
// from the host config: COMPA_CONFIG when set, config.json under the home
// otherwise. A missing file configures nothing.
func TestHostSourceRootsReadTheHostConfig(t *testing.T) {
	notes, sessions := sourceRootFixtures(t)
	home := t.TempDir()
	t.Setenv(config.EnvConfig, "")

	if got, err := hostSourceRoots(home); err != nil || got != nil {
		t.Fatalf("with no config file: %v, %v; want nothing configured", got, err)
	}

	writeSourceRootConfig(t, filepath.Join(home, "config.json"), map[string]string{"notes_store": notes})
	got, err := hostSourceRoots(home)
	if err != nil || got["notes_store"] != notes {
		t.Fatalf("config.json under home: %v, %v", got, err)
	}

	explicit := filepath.Join(t.TempDir(), "explicit.json")
	writeSourceRootConfig(t, explicit, map[string]string{"sessions_db": sessions})
	t.Setenv(config.EnvConfig, explicit)
	got, err = hostSourceRoots(home)
	if err != nil || got["sessions_db"] != sessions || got["notes_store"] != "" {
		t.Fatalf("COMPA_CONFIG did not win: %v, %v", got, err)
	}
}

// The agent's config is consulted only when an enabled module declares a source
// root, so a host whose modules ask for none never depends on it.
func TestAgentSourceRootsAreReadOnlyWhenAModuleNeedsOne(t *testing.T) {
	notes, _ := sourceRootFixtures(t)
	home := t.TempDir()
	t.Setenv(config.EnvConfig, "")
	writeSourceRootConfig(t, filepath.Join(home, "config.json"), map[string]string{"notes_store": notes})

	plain := Installed{Descriptor: &modproto.Descriptor{Module: "plain"}}
	disabled := Installed{Descriptor: sourceRootDescriptor(), Disabled: true}
	if got := agentSourceRoots(home, []Installed{plain, disabled}); got != nil {
		t.Fatalf("read source roots although no enabled module declares one: %v", got)
	}

	needs := Installed{Descriptor: sourceRootDescriptor()}
	if got := agentSourceRoots(home, []Installed{plain, needs}); got["notes_store"] != notes {
		t.Fatalf("agent source roots = %v, want the configured notes_store", got)
	}
}

// The agent's tools carry the operator's roots, so what a capability is granted
// is what the operator configured, and the path hint shows the same grant.
func TestAgentToolsGrantTheConfiguredSourceRoots(t *testing.T) {
	notes, sessions := sourceRootFixtures(t)
	home := t.TempDir()
	d := sourceRootDescriptor()
	d.Capabilities = []modproto.Capability{{ID: "notes.read"}}

	tools := New(nil, d, home, filepath.Join(home, "workspace"), map[string]string{
		"notes_store": notes, "sessions_db": sessions,
	})
	if len(tools) != 1 {
		t.Fatalf("New built %d tools, want 1", len(tools))
	}
	hint := tools[0].(*CapabilityTool).rootHint()
	for _, want := range []string{"notes_store (ro): " + notes, "sessions_db (ro): " + sessions} {
		if !strings.Contains(hint, want) {
			t.Errorf("root hint does not show %q:\n%s", want, hint)
		}
	}
}

// A failure the agent cannot explain on its own gets the one fact only the host
// knows: which declared source roots were missing, and what to configure.
func TestFailureGuidanceNamesAnUngrantedSourceRoot(t *testing.T) {
	notes, _ := sourceRootFixtures(t)
	home := t.TempDir()
	tool := &CapabilityTool{
		descriptor:  sourceRootDescriptor(),
		capability:  modproto.Capability{ID: "notes.read"},
		home:        home,
		workspace:   filepath.Join(home, "workspace"),
		sourceRoots: map[string]string{"notes_store": notes},
	}

	got := tool.failureGuidance("command_failed", "no sessions found", "")
	for _, want := range []string{`"sessions_db"`, "WITHOUT", `"modules": {"source_roots"`, "do not search for the data yourself"} {
		if !strings.Contains(got, want) {
			t.Errorf("guidance does not say %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, `"notes_store"`) {
		t.Errorf("guidance reported a root that WAS granted:\n%s", got)
	}
	if !strings.Contains(got, "Do NOT attempt this task with shell commands") {
		t.Errorf("lost the do-not-improvise instruction:\n%s", got)
	}

	// A consent refusal never ran, so it must not claim the call went without
	// anything.
	if consent := tool.failureGuidance("consent_required", "needs consent", ""); strings.Contains(consent, "WITHOUT") {
		t.Errorf("a consent refusal carried the source root note:\n%s", consent)
	}

	tool.sourceRoots["sessions_db"] = notes
	if full := tool.failureGuidance("command_failed", "no sessions found", ""); strings.Contains(full, "WITHOUT") {
		t.Errorf("guidance reported missing roots when all were granted:\n%s", full)
	}
}

func writeSourceRootConfig(t *testing.T, path string, roots map[string]string) {
	t.Helper()
	blob, err := json.Marshal(map[string]any{"modules": map[string]any{"source_roots": roots}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, blob, 0o600); err != nil {
		t.Fatal(err)
	}
}
