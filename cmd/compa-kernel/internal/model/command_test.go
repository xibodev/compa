package model

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xibodev/compa/v3/pkg/config"
)

// captureStdout captures stdout during the execution of fn and returns it.
func captureStdout(fn func()) string {
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	fn()

	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	io.Copy(&buf, r)
	return buf.String()
}

func testConfig(t *testing.T) (string, *config.Config) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv(config.EnvConfig, path)
	cfg := config.DefaultConfig()
	cfg.ProviderInstances = []*config.ProviderInstanceConfig{{
		ID: "local", ProviderKind: "ollama", Adapter: config.ProviderAdapterOpenAICompatible,
		Protocol: "ollama", Endpoint: "http://127.0.0.1:11434/v1", State: config.ProviderInstanceStateEnabled,
	}}
	cfg.ActiveModels = []string{"local/llama3", "local/qwen3"}
	cfg.ModelRoutes = []*config.ModelRouteConfig{{Name: "everyday", Targets: []string{"local/llama3", "local/qwen3"}}}
	require.NoError(t, config.SaveConfig(path, cfg))
	return path, cfg
}

// acceptSelections makes checkSelection accept the given selections only.
func acceptSelections(t *testing.T, valid ...string) {
	t.Helper()
	previous := checkSelection
	checkSelection = func(_ *config.Config, selection string) error {
		for _, v := range valid {
			if v == selection {
				return nil
			}
		}
		return errors.New("not found")
	}
	t.Cleanup(func() { checkSelection = previous })
}

func TestNewModelCommand(t *testing.T) {
	cmd := NewModelCommand()
	require.NotNil(t, cmd)
	assert.Equal(t, "model [target-or-route]", cmd.Use)
	assert.NotNil(t, cmd.Flags().Lookup("clear"))
	assert.NotNil(t, cmd.RunE)
	for _, name := range []string{"auto-free", "ping", "roster"} {
		found, _, err := cmd.Find([]string{name})
		require.NoError(t, err)
		assert.Equal(t, name, found.Name())
	}
}

func TestShowCurrentModelListsShortlistAndRoutes(t *testing.T) {
	_, cfg := testConfig(t)
	cfg.Agents.Defaults.ModelName = "everyday"

	output := captureStdout(func() { showCurrentModel(cfg) })

	assert.Contains(t, output, "Default model: everyday")
	assert.Contains(t, output, "  local/llama3")
	assert.Contains(t, output, "> everyday -> local/llama3, local/qwen3")
}

func TestShowCurrentModelWithoutModels(t *testing.T) {
	cfg := config.DefaultConfig()
	output := captureStdout(func() { showCurrentModel(cfg) })
	assert.Contains(t, output, "No default model is set.")
	assert.Contains(t, output, "Connect a provider")
}

func TestSetDefaultModelValidatesAndSaves(t *testing.T) {
	path, cfg := testConfig(t)
	acceptSelections(t, "local/qwen3")

	output := captureStdout(func() { require.NoError(t, setDefaultModel(path, cfg, "local/qwen3")) })
	assert.Contains(t, output, "from (none) to local/qwen3")

	saved, err := config.LoadConfig(path)
	require.NoError(t, err)
	assert.Equal(t, "local/qwen3", saved.Agents.Defaults.ModelName)
}

func TestSetDefaultModelRejectsUnknownSelection(t *testing.T) {
	path, cfg := testConfig(t)
	acceptSelections(t)

	err := setDefaultModel(path, cfg, "local/missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `cannot use "local/missing"`)

	saved, err := config.LoadConfig(path)
	require.NoError(t, err)
	assert.Empty(t, saved.Agents.Defaults.ModelName)
}

func TestModelCommandClear(t *testing.T) {
	path, cfg := testConfig(t)
	cfg.Agents.Defaults.ModelName = "everyday"
	require.NoError(t, config.SaveConfig(path, cfg))

	cmd := NewModelCommand()
	cmd.SetArgs([]string{"--clear"})
	output := captureStdout(func() { require.NoError(t, cmd.Execute()) })
	assert.Contains(t, output, "Default model cleared (was everyday)")

	saved, err := config.LoadConfig(path)
	require.NoError(t, err)
	assert.Empty(t, saved.Agents.Defaults.ModelName)
}

func TestModelCommandRejectsClearWithSelection(t *testing.T) {
	testConfig(t)
	cmd := NewModelCommand()
	cmd.SetArgs([]string{"--clear", "everyday"})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	assert.Error(t, cmd.Execute())
}

func TestModelCommandTooManyArgs(t *testing.T) {
	cmd := NewModelCommand()
	assert.Error(t, cmd.Args(cmd, []string{"a", "b"}))
}

func TestFormatModelName(t *testing.T) {
	assert.Equal(t, "(none)", formatModelName(""))
	assert.Equal(t, "everyday", formatModelName("everyday"))
}
