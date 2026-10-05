package auth

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// On a terminal the key is read without echo; piped input still works (PR-25).
func TestReadAPIKeyHidesTerminalInput(t *testing.T) {
	origTerm, origHidden := stdinIsTerminal, readHidden
	t.Cleanup(func() { stdinIsTerminal, readHidden = origTerm, origHidden })

	hiddenUsed := false
	stdinIsTerminal = func(*os.File) bool { return true }
	readHidden = func(*os.File) ([]byte, error) {
		hiddenUsed = true
		return []byte("  sk-secret  \n"), nil
	}
	var out bytes.Buffer
	key, err := readAPIKey("openai", os.Stdin, &out)
	require.NoError(t, err)
	assert.True(t, hiddenUsed)
	assert.Equal(t, "sk-secret", key)
	assert.NotContains(t, out.String(), "sk-secret")

	readHidden = func(*os.File) ([]byte, error) { return []byte("   "), nil }
	_, err = readAPIKey("openai", os.Stdin, &out)
	assert.Error(t, err)

	// Piped input.
	stdinIsTerminal = func(*os.File) bool { return false }
	path := filepath.Join(t.TempDir(), "key")
	require.NoError(t, os.WriteFile(path, []byte("sk-piped\n"), 0o600))
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	key, err = readAPIKey("openai", f, &out)
	require.NoError(t, err)
	assert.Equal(t, "sk-piped", strings.TrimSpace(key))
}
