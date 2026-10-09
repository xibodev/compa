package auth

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xibodev/compa/v4/pkg/auth"
	"github.com/xibodev/compa/v4/pkg/config"
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

	assert.NotNil(t, cmd.Flags().Lookup("json"))
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
	require.NoError(t, auth.SetCredential("soon", &auth.AuthCredential{
		AccessToken: "access",
		ExpiresAt:   time.Now().Add(2 * time.Minute),
		AuthMethod:  "oauth",
	}))

	output, _, err := runAuth(t, newStatusCommand())
	require.NoError(t, err)

	assert.Contains(t, output, "  openai:\n    Method: api_key\n    Status: active\n")
	assert.Contains(t, output, "  extension-signin:acme:\n    Method: oauth\n    Status: expired\n    Account: acct-1\n")
	assert.Contains(t, output, "  soon:\n    Method: oauth\n    Status: needs refresh\n    Expires: ")
	// Sorted by provider.
	first, second, third := strings.Index(output, "extension-signin:acme:"), strings.Index(output, "openai:"), strings.Index(output, "soon:")
	assert.True(t, first < second && second < third, "providers out of order:\n%s", output)
}

func TestAuthStatusWithoutCredentials(t *testing.T) {
	setAuthStatusTestHome(t)

	output, _, err := runAuth(t, newStatusCommand())
	require.NoError(t, err)
	assert.Equal(t, "No authenticated providers.\nRun: compa-kernel auth login --provider <name>\n", output)

	output, _, err = runAuth(t, newStatusCommand(), "--json")
	require.NoError(t, err)
	assert.JSONEq(t, `{"providers": [], "total": 0}`, output)
}
