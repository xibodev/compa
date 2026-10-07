package auth

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xibodev/compa/v3/pkg/auth"
	"github.com/xibodev/compa/v3/pkg/config"
)

func captureAuthStdout(t *testing.T, fn func()) string {
	t.Helper()

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	t.Cleanup(func() {
		os.Stdout = oldStdout
	})

	fn()

	require.NoError(t, w.Close())
	os.Stdout = oldStdout

	var buf bytes.Buffer
	_, err = io.Copy(&buf, r)
	require.NoError(t, err)
	require.NoError(t, r.Close())
	return buf.String()
}

func setAuthStatusTestHome(t *testing.T) string {
	t.Helper()

	tmpDir := t.TempDir()
	t.Setenv(config.EnvHome, filepath.Join(tmpDir, ".compa"))
	return tmpDir
}

func TestNewStatusSubcommand(t *testing.T) {
	cmd := newStatusCommand()

	require.NotNil(t, cmd)

	assert.Equal(t, "Show current auth status", cmd.Short)

	assert.False(t, cmd.HasFlags())
}

func TestAuthStatusCmdReportsStoredCredentials(t *testing.T) {
	setAuthStatusTestHome(t)
	require.NoError(t, auth.SetCredential("openai", &auth.AuthCredential{
		AccessToken: "sk-test",
		AuthMethod:  "api_key",
	}))
	require.NoError(t, auth.SetCredential("extension-signin:acme", &auth.AuthCredential{
		AccessToken:  "access",
		RefreshToken: "refresh",
		AccountID:    "acct-1",
		ExpiresAt:    time.Now().Add(-time.Minute),
		AuthMethod:   "oauth",
	}))

	var err error
	output := captureAuthStdout(t, func() { err = authStatusCmd() })
	require.NoError(t, err)

	assert.Contains(t, output, "  openai:\n    Method: api_key\n    Status: active\n")
	assert.Contains(t, output, "  extension-signin:acme:\n    Method: oauth\n    Status: expired\n    Account: acct-1\n")
}
