package skills

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/xibodev/compa/pkg/config"
)

func TestBuiltinDirNeverUsesWorkingDirectory(t *testing.T) {
	t.Setenv(config.EnvBuiltinSkills, "")
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, "skills", "planted"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwd)

	if got := BuiltinDir(); got == filepath.Join(cwd, "skills") || got == "skills" {
		t.Fatalf("BuiltinDir() = %q, the working directory's skills folder", got)
	}
}

func TestBuiltinDirHonorsOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(config.EnvBuiltinSkills, dir)
	if got := BuiltinDir(); got != dir {
		t.Fatalf("BuiltinDir() = %q, want %q", got, dir)
	}
}

func TestInstalledBuiltinDir(t *testing.T) {
	exeDir := t.TempDir()
	if got := installedBuiltinDir(exeDir); got != "" {
		t.Fatalf("installedBuiltinDir() without skills = %q, want empty", got)
	}
	if err := os.MkdirAll(filepath.Join(exeDir, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := installedBuiltinDir(exeDir); got != filepath.Join(exeDir, "skills") {
		t.Fatalf("installedBuiltinDir() = %q", got)
	}
}
