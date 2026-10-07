package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/xibodev/compa/v3/pkg"
)

func TestGetHomeHonoursCompaHome(t *testing.T) {
	want := t.TempDir()
	t.Setenv(EnvHome, want)

	if got := GetHome(); got != want {
		t.Errorf("GetHome() = %q, want %q", got, want)
	}
}

// Without COMPA_HOME the data is in the user's profile, wherever the binary
// sits.
func TestGetHomeDefaultsToTheUserProfile(t *testing.T) {
	t.Setenv(EnvHome, "")
	userHome, err := os.UserHomeDir()
	if err != nil || userHome == "" {
		t.Skip("no user home directory")
	}

	if got, want := GetHome(), filepath.Join(userHome, pkg.DefaultHome); got != want {
		t.Errorf("GetHome() = %q, want %q", got, want)
	}
}
