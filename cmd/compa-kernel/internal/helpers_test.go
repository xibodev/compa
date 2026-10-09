package internal

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xibodev/compa/v4/pkg/config"
)

func TestGetConfigPath(t *testing.T) {
	// os.UserHomeDir reads USERPROFILE on Windows and ignores HOME, so setting
	// HOME isolated nothing and the test read the developer's real home. The
	// sibling test below already used config.EnvHome, which works everywhere.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	got := GetConfigPath()
	want := filepath.Join(home, ".compa", "config.json")

	assert.Equal(t, want, got)
}

func TestGetConfigPath_WithCOMPA_HOME(t *testing.T) {
	t.Setenv(config.EnvHome, "/custom/compa")
	t.Setenv("HOME", "/tmp/home")

	got := GetConfigPath()
	want := filepath.Join("/custom/compa", "config.json")

	assert.Equal(t, want, got)
}

func TestGetConfigPath_WithCOMPA_CONFIG(t *testing.T) {
	t.Setenv("COMPA_CONFIG", "/custom/config.json")
	t.Setenv(config.EnvHome, "/custom/compa")
	t.Setenv("HOME", "/tmp/home")

	got := GetConfigPath()
	want := "/custom/config.json"

	assert.Equal(t, want, got)
}

func TestGetConfigPath_Windows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-specific HOME behavior varies; run on windows")
	}

	testUserProfilePath := `C:\Users\Test`
	t.Setenv("USERPROFILE", testUserProfilePath)

	got := GetConfigPath()
	want := filepath.Join(testUserProfilePath, ".compa", "config.json")

	require.True(t, strings.EqualFold(got, want), "GetConfigPath() = %q, want %q", got, want)
}
