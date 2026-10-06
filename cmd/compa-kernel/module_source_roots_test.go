package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xibodev/compa/v2/pkg/modproto"
)

func sourceRootModule() *modproto.Descriptor {
	return &modproto.Descriptor{
		Module: "source.fixture",
		Permissions: modproto.Permissions{
			FilesystemRead: []string{"notes_store", "sessions_db"},
		},
	}
}

// --source-root lends a location for ONE invocation. It must stay on
// module-invoke, repeatable, and never leak to the root command, where a
// browser-facing surface could turn it into a standing setting.
func TestModuleInvokeSourceRootFlagIsLocalAndRepeatable(t *testing.T) {
	cmd := NewModuleInvokeCommand()
	flag := cmd.Flags().Lookup("source-root")
	if flag == nil {
		t.Fatal("module-invoke has no --source-root flag")
	}
	// A string ARRAY, not a slice: a slice splits on commas, and a comma is a
	// legal character in a path.
	if flag.Value.Type() != "stringArray" {
		t.Fatalf("--source-root is a %s, want a repeatable stringArray", flag.Value.Type())
	}
	if err := cmd.Flags().Parse([]string{
		"--source-root", "notes_store=/a,b", "--source-root", "sessions_db=/c",
	}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got, _ := cmd.Flags().GetStringArray("source-root"); len(got) != 2 || got[0] != "notes_store=/a,b" {
		t.Fatalf("--source-root values = %q", got)
	}

	root := NewRootCommand()
	if root.PersistentFlags().Lookup("source-root") != nil {
		t.Fatal("--source-root leaked to global/browser-facing configuration")
	}
}

func TestParseSourceRootFlags(t *testing.T) {
	if got, err := parseSourceRootFlags(nil); err != nil || got != nil {
		t.Fatalf("no flags: %v, %v", got, err)
	}

	got, err := parseSourceRootFlags([]string{"notes_store=/data/notes", "sessions_db=/data/a=b.db"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// Only the FIRST '=' separates: a path may contain one.
	if len(got) != 2 || got["notes_store"] != "/data/notes" || got["sessions_db"] != "/data/a=b.db" {
		t.Fatalf("parsed %#v", got)
	}

	for _, bad := range [][]string{
		{"notes_store"}, // no '=' at all
		{"notes_store=/a", "notes_store=/b"},
	} {
		if _, err := parseSourceRootFlags(bad); err == nil {
			t.Errorf("parseSourceRootFlags(%q) accepted a malformed set", bad)
		}
	}
}

// A root the command line asks for must be usable, or nothing runs -- and the
// refusal comes BEFORE discovery, naming the flag and the reason.
func TestCmdInvokeWithSourceRootsRejectsInvalidRootBeforeDiscovery(t *testing.T) {
	t.Setenv("COMPA_HOME", t.TempDir())
	notes := t.TempDir()

	for _, tc := range []struct {
		overrides map[string]string
		want      []string
	}{
		{map[string]string{"sessions_db": filepath.Join(t.TempDir(), "missing", "sessions.db")},
			[]string{"invalid --source-root sessions_db=", "does not exist"}},
		{map[string]string{"notes_store": "relative/notes"},
			[]string{"invalid --source-root notes_store=", "not absolute"}},
		{map[string]string{"notes store": notes},
			[]string{"invalid --source-root notes store=", "name"}},
		{map[string]string{"workspace": notes},
			[]string{"invalid --source-root workspace=", "host supplies"}},
	} {
		_, err := invokeModule("module", "capability", "{}", tc.overrides, false)
		if err == nil {
			t.Fatalf("overrides %v were accepted", tc.overrides)
		}
		for _, want := range tc.want {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("overrides %v: error %q does not say %q", tc.overrides, err, want)
			}
		}
		if strings.Contains(err.Error(), "not installed") {
			t.Errorf("overrides %v reached discovery before being refused: %v", tc.overrides, err)
		}
	}
}

// A directory and a single file are both valid roots; with nothing wrong with
// them, the invocation proceeds to discovery.
func TestCmdInvokeWithSourceRootsAcceptsDirectoriesAndFiles(t *testing.T) {
	t.Setenv("COMPA_HOME", t.TempDir())
	notes := t.TempDir()
	sessions := filepath.Join(t.TempDir(), "sessions.db")
	if err := os.WriteFile(sessions, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := invokeModule("no.such.module", "capability", "{}", map[string]string{
		"notes_store": notes,
		"sessions_db": sessions,
	}, false)
	if err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("error = %v, want valid roots accepted and the missing module reported", err)
	}
}

// The command line overlays the config. The config is read only for a declared
// root the command line did not give, so a fully specified invocation cannot
// pick up the machine's configured roots.
func TestInvocationSourceRootsOverlayTheConfig(t *testing.T) {
	d := sourceRootModule()
	unreadable := func() (map[string]string, error) {
		t.Fatal("the config was read although every declared root was given")
		return nil, nil
	}
	got, err := invocationSourceRoots(d, map[string]string{
		"notes_store": "/cli/notes", "sessions_db": "/cli/sessions.db",
	}, unreadable)
	if err != nil || len(got) != 2 || got["notes_store"] != "/cli/notes" {
		t.Fatalf("fully specified invocation = %v, %v", got, err)
	}

	reads := 0
	configured := func() (map[string]string, error) {
		reads++
		return map[string]string{"notes_store": "/config/notes", "sessions_db": "/config/sessions.db"}, nil
	}
	got, err = invocationSourceRoots(d, map[string]string{"notes_store": "/cli/notes"}, configured)
	if err != nil {
		t.Fatal(err)
	}
	if reads != 1 || got["notes_store"] != "/cli/notes" || got["sessions_db"] != "/config/sessions.db" {
		t.Fatalf("overlay = %v after %d config reads; want the flag to win and the config to fill the rest", got, reads)
	}

	broken := func() (map[string]string, error) { return nil, errors.New("config is broken") }
	if _, err := invocationSourceRoots(d, nil, broken); err == nil || !strings.Contains(err.Error(), "config is broken") {
		t.Fatalf("a config that could not be read was not reported: %v", err)
	}
	if got, err := invocationSourceRoots(&modproto.Descriptor{}, nil, broken); err != nil || len(got) != 0 {
		t.Fatalf("a module with no source roots consulted the config: %v, %v", got, err)
	}
}

// A --source-root the module never declared is pointed out, so a typo does not
// silently grant nothing.
func TestUndeclaredSourceRootsAreNamed(t *testing.T) {
	got := undeclaredSourceRoots(sourceRootModule(), map[string]string{
		"notes_store": "/a", "notes_stor": "/b", "workspace": "/c",
	})
	if strings.Join(got, ",") != "notes_stor,workspace" {
		t.Fatalf("undeclared = %v, want [notes_stor workspace]", got)
	}
}
