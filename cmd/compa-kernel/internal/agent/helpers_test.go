package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCLIHistoryFileIsPrivateUnderHome(t *testing.T) {
	home := t.TempDir()

	path := cliHistoryFile(home)
	require.Equal(t, filepath.Join(home, "state", "cli_history"), path)
	info, err := os.Stat(path)
	require.NoError(t, err)
	if runtime.GOOS != "windows" {
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
	assert.NotEqual(t, os.TempDir(), filepath.Dir(path))

	assert.Empty(t, cliHistoryFile(""), "no home disables history instead of using a shared folder")
}
