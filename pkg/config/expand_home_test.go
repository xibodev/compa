package config

import (
	"path/filepath"
	"testing"
)

func TestExpandHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	tests := []struct {
		in, want string
	}{
		{"", ""},
		{"~", home},
		{"~/", home},
		{`~\`, home},
		{"~/workspace", filepath.Join(home, "workspace")},
		{`~\workspace`, filepath.Join(home, "workspace")},
		{"~/a/b", filepath.Join(home, "a", "b")},
		{"~user/workspace", "~user/workspace"},
		{"~workspace", "~workspace"},
		{"/srv/compa", "/srv/compa"},
		{"workspace/~", "workspace/~"},
	}
	for _, tt := range tests {
		if got := ExpandHome(tt.in); got != tt.want {
			t.Errorf("ExpandHome(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// A Windows-style "~\..." workspace used to expand to the bare home
// directory, which made the whole profile the workspace and restrict root.
func TestWorkspacePathExpandsBackslashTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	cfg := &Config{}
	cfg.Agents.Defaults.Workspace = `~\.compa\workspace`
	if got, want := cfg.WorkspacePath(), filepath.Join(home, `.compa\workspace`); got != want {
		t.Fatalf("WorkspacePath() = %q, want %q", got, want)
	}
	if cfg.WorkspacePath() == home {
		t.Fatal("WorkspacePath() is the bare home directory")
	}
}
