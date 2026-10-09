package auth

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xibodev/compa/v4/pkg/auth"
	"github.com/xibodev/compa/v4/pkg/config"
)

// runAuth runs an auth subcommand with args and returns what it wrote to
// its stdout and stderr. Nothing may reach the process's stdout past them.
func runAuth(t *testing.T, cmd *cobra.Command, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd.SetArgs(append([]string{}, args...))
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	printed := captureAuthStdout(t, func() { err = cmd.Execute() })
	require.Empty(t, printed, "written to the process's stdout directly")
	return out.String(), errOut.String(), err
}

// withStdin pipes input to the key prompt.
func withStdin(t *testing.T, input string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stdin")
	require.NoError(t, os.WriteFile(path, []byte(input), 0o600))
	f, err := os.Open(path)
	require.NoError(t, err)
	previousStdin, previousIsTerminal := os.Stdin, stdinIsTerminal
	os.Stdin = f
	stdinIsTerminal = func(*os.File) bool { return false }
	t.Cleanup(func() {
		os.Stdin, stdinIsTerminal = previousStdin, previousIsTerminal
		_ = f.Close()
	})
}

func TestAuthCommandsTakeJSON(t *testing.T) {
	for _, cmd := range []*cobra.Command{newLoginCommand(), newLogoutCommand(), newStatusCommand()} {
		assert.NotNil(t, cmd.Flags().Lookup("json"), cmd.Name())
	}
}

func TestAuthLoginJSON(t *testing.T) {
	withConnectEnv(t, []string{"model-a", "model-b"})
	withStdin(t, "sk-test\n")

	stdout, stderr, err := runAuth(t, newLoginCommand(), "--provider", "openai", "--json")
	require.NoError(t, err)
	assert.JSONEq(t, `{"status": "ok", "provider": "openai", "instance_id": "openai",
		"model_count": 2, "default_model": ""}`, stdout)
	// The prompt goes to stderr, and the key is printed nowhere.
	assert.Contains(t, stderr, "Paste your API key")
	assert.NotContains(t, stdout+stderr, "sk-test")
}

func TestAuthLoginJSONReportsTheDefaultModel(t *testing.T) {
	configPath := withConnectEnv(t, []string{"model-a"})
	cfg, err := config.LoadConfig(configPath)
	require.NoError(t, err)
	cfg.Agents.Defaults.ModelName = "everyday"
	require.NoError(t, config.SaveConfig(configPath, cfg))
	withStdin(t, "sk-test\n")

	stdout, _, err := runAuth(t, newLoginCommand(), "--provider", "openai", "--json")
	require.NoError(t, err)
	assert.JSONEq(t, `{"status": "ok", "provider": "openai", "instance_id": "openai",
		"model_count": 1, "default_model": "everyday"}`, stdout)
}

func TestAuthLoginText(t *testing.T) {
	withConnectEnv(t, []string{"model-a"})
	withStdin(t, "sk-test\n")

	stdout, stderr, err := runAuth(t, newLoginCommand(), "--provider", "openai")
	require.NoError(t, err)
	assert.Empty(t, stderr)
	assert.Equal(t, "Paste your API key from platform.openai.com:\n> "+
		"Connected openai as provider instance \"openai\" (1 models available).\n"+
		"Choose a default model with: compa-kernel model openai/<model-id>\n", stdout)
}

// A failed command prints no document of its own: main prints the error one.
func TestAuthLoginJSONFailurePrintsNothing(t *testing.T) {
	withConnectEnv(t, nil)

	stdout, _, err := runAuth(t, newLoginCommand(), "--provider", "bogus", "--json")
	require.ErrorContains(t, err, "unsupported provider")
	assert.Empty(t, stdout)
}

func TestAuthLogoutJSON(t *testing.T) {
	withConnectEnv(t, []string{"model-a"})
	_, err := connectProvider("openai", "sk-test")
	require.NoError(t, err)

	stdout, _, err := runAuth(t, newLogoutCommand(), "--provider", "openai", "--json")
	require.NoError(t, err)
	assert.JSONEq(t, `{"status": "ok", "providers": ["openai"]}`, stdout)

	stdout, _, err = runAuth(t, newLogoutCommand(), "--json")
	require.NoError(t, err)
	assert.JSONEq(t, `{"status": "ok", "providers": ["openai", "anthropic"]}`, stdout)

	stdout, _, err = runAuth(t, newLogoutCommand(), "--provider", "bogus", "--json")
	require.ErrorContains(t, err, "unsupported provider")
	assert.Empty(t, stdout)
}

// When one provider fails, text still reports the others; JSON leaves the
// failure to main's error document.
func TestAuthLogoutPartialFailure(t *testing.T) {
	withConnectEnv(t, nil)
	connectDeps.DeleteCredential = func(key string) error {
		if key == "anthropic" {
			return errors.New("store locked")
		}
		return auth.DeleteCredential(key)
	}

	stdout, _, err := runAuth(t, newLogoutCommand())
	require.ErrorContains(t, err, "anthropic: deleting credential: store locked")
	assert.Equal(t, "Logged out from openai\n", stdout)

	stdout, _, err = runAuth(t, newLogoutCommand(), "--json")
	require.ErrorContains(t, err, "store locked")
	assert.Empty(t, stdout)
}

func TestAuthStatusJSON(t *testing.T) {
	setAuthStatusTestHome(t)
	expired := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	soon := time.Now().Add(2 * time.Minute).UTC().Truncate(time.Second)
	require.NoError(t, auth.SetCredential("openai", &auth.AuthCredential{
		AccessToken: "sk-secret", AuthMethod: "api_key",
	}))
	require.NoError(t, auth.SetCredential("extension-signin:acme", &auth.AuthCredential{
		AccessToken: "access-secret", RefreshToken: "refresh-secret", AccountID: "acct-1",
		ExpiresAt: expired, AuthMethod: "oauth",
	}))
	require.NoError(t, auth.SetCredential("soon", &auth.AuthCredential{
		AccessToken: "access-secret", ExpiresAt: soon, AuthMethod: "oauth",
	}))

	stdout, _, err := runAuth(t, newStatusCommand(), "--json")
	require.NoError(t, err)
	assert.JSONEq(t, fmt.Sprintf(`{"providers": [
		{"provider": "extension-signin:acme", "auth_method": "oauth", "status": "expired",
			"account_id": "acct-1", "expires_at": "2020-01-02T03:04:05Z"},
		{"provider": "openai", "auth_method": "api_key", "status": "active"},
		{"provider": "soon", "auth_method": "oauth", "status": "needs_refresh", "expires_at": %q}
	], "total": 3}`, soon.Format(time.RFC3339)), stdout)
	assert.NotContains(t, stdout, "secret")
}

// A null entry in a hand-edited auth.json is skipped, not dereferenced.
func TestAuthStatusSkipsNullCredential(t *testing.T) {
	setAuthStatusTestHome(t)
	require.NoError(t, auth.SetCredential("openai", &auth.AuthCredential{AccessToken: "sk-secret", AuthMethod: "api_key"}))
	path := filepath.Join(config.GetHome(), "auth.json")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	edited := bytes.Replace(data, []byte(`"credentials": {`), []byte(`"credentials": {"broken": null, `), 1)
	require.NotEqual(t, data, edited, "auth.json layout changed: %s", data)
	require.NoError(t, os.WriteFile(path, edited, 0o600))

	stdout, _, err := runAuth(t, newStatusCommand(), "--json")
	require.NoError(t, err)
	assert.JSONEq(t, `{"providers": [{"provider": "openai", "auth_method": "api_key", "status": "active"}], "total": 1}`, stdout)
}
