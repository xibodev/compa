package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xibodev/compa/v2/pkg/config"
)

func TestNewMCPCommand(t *testing.T) {
	cmd := NewMCPCommand()

	require.NotNil(t, cmd)

	assert.Equal(t, "mcp", cmd.Use)
	assert.Equal(t, "Manage MCP server configuration", cmd.Short)
	assert.True(t, cmd.HasSubCommands())

	allowedCommands := []string{
		"add",
		"remove",
		"list",
		"edit",
		"test",
		"show",
	}

	subcommands := cmd.Commands()
	assert.Len(t, subcommands, len(allowedCommands))

	for _, subcmd := range subcommands {
		found := slices.Contains(allowedCommands, subcmd.Name())
		assert.True(t, found, "unexpected subcommand %q", subcmd.Name())
		assert.False(t, subcmd.Hidden)
	}
}

func TestMCPAddAddsGenericStdioServer(t *testing.T) {
	configPath := setupMCPConfigEnv(t)

	cmd := NewMCPCommand()
	output, err := executeCommand(cmd, []string{
		"add",
		"sqlite",
		"npx",
		"-y",
		"@modelcontextprotocol/server-sqlite",
		"--db",
		"./mydb.db",
	}, "")
	require.NoError(t, err)
	assert.Contains(t, output, `MCP server "sqlite" saved`)

	cfg := readMCPConfig(t, configPath)
	require.True(t, cfg.Tools.MCP.Enabled)

	server, ok := cfg.Tools.MCP.Servers["sqlite"]
	require.True(t, ok)
	assert.True(t, server.Enabled)
	assert.Equal(t, "stdio", server.Type)
	assert.Equal(t, "npx", server.Command)
	assert.Equal(t, []string{"-y", "@modelcontextprotocol/server-sqlite", "--db", "./mydb.db"}, server.Args)
}

func TestMCPAddSupportsHeadersAfterURL(t *testing.T) {
	configPath := setupMCPConfigEnv(t)

	cmd := NewMCPCommand()
	_, err := executeCommand(cmd, []string{
		"add",
		"apify",
		"https://mcp.apify.com/",
		"-t",
		"http",
		"--header",
		"Authorization: Bearer OMITTED",
	}, "")
	require.NoError(t, err)

	cfg := readMCPConfig(t, configPath)
	server := cfg.Tools.MCP.Servers["apify"]
	assert.Equal(t, "http", server.Type)
	assert.Equal(t, "https://mcp.apify.com/", server.URL)
	assert.Equal(t, map[string]string{"Authorization": "Bearer OMITTED"}, server.Headers)
}

func TestMCPAddSupportsTransportBeforeName(t *testing.T) {
	configPath := setupMCPConfigEnv(t)

	cmd := NewMCPCommand()
	_, err := executeCommand(cmd, []string{
		"add",
		"--transport",
		"sse",
		"fiscal-ai",
		"https://api.fiscal.ai/mcp/sse",
	}, "")
	require.NoError(t, err)

	cfg := readMCPConfig(t, configPath)
	server := cfg.Tools.MCP.Servers["fiscal-ai"]
	assert.Equal(t, "sse", server.Type)
	assert.Equal(t, "https://api.fiscal.ai/mcp/sse", server.URL)
}

func TestMCPAddSupportsExplicitStdioCommandAfterSeparator(t *testing.T) {
	configPath := setupMCPConfigEnv(t)

	cmd := NewMCPCommand()
	_, err := executeCommand(cmd, []string{
		"add",
		"--transport",
		"stdio",
		"--env",
		"AIRTABLE_API_KEY=YOUR_KEY",
		"airtable",
		"--",
		"npx",
		"-y",
		"airtable-mcp-server",
	}, "")
	require.NoError(t, err)

	cfg := readMCPConfig(t, configPath)
	server := cfg.Tools.MCP.Servers["airtable"]
	assert.Equal(t, "stdio", server.Type)
	assert.Equal(t, "npx", server.Command)
	assert.Equal(t, []string{"-y", "airtable-mcp-server"}, server.Args)
	assert.Equal(t, map[string]string{"AIRTABLE_API_KEY": "YOUR_KEY"}, server.Env)
}

func TestMCPAddSupportsEnvFileForStdio(t *testing.T) {
	configPath := setupMCPConfigEnv(t)

	cmd := NewMCPCommand()
	_, err := executeCommand(cmd, []string{
		"add",
		"--env-file",
		".env.mcp",
		"filesystem",
		"npx",
		"-y",
		"@modelcontextprotocol/server-filesystem",
	}, "")
	require.NoError(t, err)

	cfg := readMCPConfig(t, configPath)
	server := cfg.Tools.MCP.Servers["filesystem"]
	assert.Equal(t, "stdio", server.Type)
	assert.Equal(t, "npx", server.Command)
	assert.Equal(t, []string{"-y", "@modelcontextprotocol/server-filesystem"}, server.Args)
	assert.Equal(t, ".env.mcp", server.EnvFile)
}

func TestMCPAddRejectsEnvFileForHTTP(t *testing.T) {
	setupMCPConfigEnv(t)

	cmd := NewMCPCommand()
	_, err := executeCommand(cmd, []string{
		"add",
		"--transport",
		"http",
		"--env-file",
		".env.mcp",
		"context7",
		"https://mcp.context7.com/mcp",
	}, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--env-file can only be used with stdio transport")
}

func TestMCPAddRejectsNonExecutableLocalCommand(t *testing.T) {
	// Windows has no execute bit, and the check under test says so itself
	// (`runtime.GOOS != "windows" && ...`). Asserting the rejection there tested
	// a rule the code deliberately does not apply.
	if runtime.GOOS == "windows" {
		t.Skip("no execute bit on Windows; validateLocalCommand skips this check")
	}

	setupMCPConfigEnv(t)

	tmpDir := t.TempDir()
	localCmd := filepath.Join(tmpDir, "server.sh")
	require.NoError(t, os.WriteFile(localCmd, []byte("#!/bin/sh\nexit 0\n"), 0o644))

	cmd := NewMCPCommand()
	_, err := executeCommand(cmd, []string{"add", "local", localCmd}, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not executable")
}

func TestMCPAddExpandsHomeInSavedLocalCommand(t *testing.T) {
	configPath := setupMCPConfigEnv(t)

	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)

	localCmd := filepath.Join(homeDir, "bin", "my-mcp")
	require.NoError(t, os.MkdirAll(filepath.Dir(localCmd), 0o755))
	require.NoError(t, os.WriteFile(localCmd, []byte("#!/bin/sh\nexit 0\n"), 0o755))

	tildeCmd := "~" + string(os.PathSeparator) + filepath.Join("bin", "my-mcp")

	cmd := NewMCPCommand()
	_, err := executeCommand(cmd, []string{"add", "local-home", tildeCmd}, "")
	require.NoError(t, err)

	cfg := readMCPConfig(t, configPath)
	server := cfg.Tools.MCP.Servers["local-home"]
	assert.Equal(t, localCmd, server.Command)
}

func TestMCPAddShowsClearErrorForRemoteURLWithoutTransport(t *testing.T) {
	setupMCPConfigEnv(t)

	cmd := NewMCPCommand()
	_, err := executeCommand(cmd, []string{"add", "apify", "https://mcp.apify.com/"}, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `looks like a remote MCP URL`)
	assert.Contains(t, err.Error(), `Use --transport http or --transport sse`)
}

func TestMCPAddOverwritePromptDecline(t *testing.T) {
	configPath := setupMCPConfigEnv(t)
	writeMCPConfig(t, configPath, &config.Config{
		Tools: config.ToolsConfig{
			MCP: config.MCPConfig{
				ToolConfig: config.ToolConfig{Enabled: true},
				Servers: map[string]config.MCPServerConfig{
					"filesystem": {
						Enabled: true,
						Type:    "stdio",
						Command: "old",
					},
				},
			},
		},
	})

	cmd := NewMCPCommand()
	output, err := executeCommand(cmd, []string{"add", "filesystem", "new-command"}, "n\n")
	require.Error(t, err)
	assert.Contains(t, output, `Overwrite? [y/N]:`)
	assert.Contains(t, err.Error(), "aborted")

	cfg := readMCPConfig(t, configPath)
	assert.Equal(t, "old", cfg.Tools.MCP.Servers["filesystem"].Command)
}

func TestMCPAddOverwriteWithConfirmation(t *testing.T) {
	configPath := setupMCPConfigEnv(t)
	writeMCPConfig(t, configPath, &config.Config{
		Tools: config.ToolsConfig{
			MCP: config.MCPConfig{
				ToolConfig: config.ToolConfig{Enabled: true},
				Servers: map[string]config.MCPServerConfig{
					"filesystem": {
						Enabled: true,
						Type:    "stdio",
						Command: "old",
					},
				},
			},
		},
	})

	cmd := NewMCPCommand()
	_, err := executeCommand(cmd, []string{"add", "filesystem", "new-command"}, "y\n")
	require.NoError(t, err)

	cfg := readMCPConfig(t, configPath)
	assert.Equal(t, "new-command", cfg.Tools.MCP.Servers["filesystem"].Command)
}

func TestMCPAddHTTPServer(t *testing.T) {
	configPath := setupMCPConfigEnv(t)

	cmd := NewMCPCommand()
	_, err := executeCommand(cmd, []string{
		"add",
		"context7",
		"--transport",
		"http",
		"https://mcp.context7.com/mcp",
	}, "")
	require.NoError(t, err)

	cfg := readMCPConfig(t, configPath)
	server := cfg.Tools.MCP.Servers["context7"]
	assert.Equal(t, "http", server.Type)
	assert.Equal(t, "https://mcp.context7.com/mcp", server.URL)
	assert.Empty(t, server.Command)
}

func TestMCPAddSupportsStreamableHTTPAlias(t *testing.T) {
	configPath := setupMCPConfigEnv(t)

	cmd := NewMCPCommand()
	_, err := executeCommand(cmd, []string{
		"add",
		"context7",
		"--transport",
		"streamable-http",
		"https://mcp.context7.com/mcp",
	}, "")
	require.NoError(t, err)

	cfg := readMCPConfig(t, configPath)
	server := cfg.Tools.MCP.Servers["context7"]
	assert.Equal(t, "http", server.Type)
	assert.Equal(t, "https://mcp.context7.com/mcp", server.URL)
}

func TestSaveValidatedConfigNormalizesStreamableHTTPAlias(t *testing.T) {
	configPath := setupMCPConfigEnv(t)

	cfg := config.DefaultConfig()
	cfg.Tools.MCP.Enabled = true
	cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
		"context7": {
			Enabled: true,
			Type:    "streamable-http",
			URL:     "https://mcp.context7.com/mcp",
		},
	}

	require.NoError(t, saveValidatedConfig(cfg))

	saved := readMCPConfig(t, configPath)
	server := saved.Tools.MCP.Servers["context7"]
	assert.Equal(t, "http", server.Type)
	assert.Equal(t, "https://mcp.context7.com/mcp", server.URL)
	assert.Equal(t, "streamable-http", cfg.Tools.MCP.Servers["context7"].Type)
}

func TestMCPRemoveRemovesLastServerAndDisablesMCP(t *testing.T) {
	configPath := setupMCPConfigEnv(t)
	writeMCPConfig(t, configPath, &config.Config{
		Tools: config.ToolsConfig{
			MCP: config.MCPConfig{
				ToolConfig: config.ToolConfig{Enabled: true},
				Servers: map[string]config.MCPServerConfig{
					"filesystem": {
						Enabled: true,
						Type:    "stdio",
						Command: "npx",
					},
				},
			},
		},
	})

	cmd := NewMCPCommand()
	output, err := executeCommand(cmd, []string{"remove", "filesystem"}, "")
	require.NoError(t, err)
	assert.Contains(t, output, `MCP server "filesystem" removed`)

	cfg := readMCPConfig(t, configPath)
	assert.False(t, cfg.Tools.MCP.Enabled)
	assert.Empty(t, cfg.Tools.MCP.Servers)
}

func TestMCPListPrintsTable(t *testing.T) {
	configPath := setupMCPConfigEnv(t)
	writeMCPConfig(t, configPath, &config.Config{
		Tools: config.ToolsConfig{
			MCP: config.MCPConfig{
				ToolConfig: config.ToolConfig{Enabled: true},
				Servers: map[string]config.MCPServerConfig{
					"context7": {
						Enabled: true,
						Type:    "http",
						URL:     "https://mcp.context7.com/mcp",
					},
					"filesystem": {
						Enabled: false,
						Type:    "stdio",
						Command: "npx",
						Args:    []string{"-y", "@modelcontextprotocol/server-filesystem", "/tmp"},
					},
				},
			},
		},
	})

	cmd := NewMCPCommand()
	output, err := executeCommand(cmd, []string{"list"}, "")
	require.NoError(t, err)
	assert.Contains(t, output, "| Name")
	assert.Contains(t, output, "context7")
	assert.Contains(t, output, "filesystem")
	assert.Contains(t, output, "https://mcp.context7.com/mcp")
	assert.Contains(t, output, "disabled")
}

func TestMCPListWithStatusUsesProbe(t *testing.T) {
	configPath := setupMCPConfigEnv(t)
	writeMCPConfig(t, configPath, &config.Config{
		Tools: config.ToolsConfig{
			MCP: config.MCPConfig{
				ToolConfig: config.ToolConfig{Enabled: true},
				Servers: map[string]config.MCPServerConfig{
					"filesystem": {
						Enabled: true,
						Type:    "stdio",
						Command: "npx",
					},
				},
			},
		},
	})

	originalProbe := serverProbe
	defer func() { serverProbe = originalProbe }()
	serverProbe = func(_ context.Context, name string, server config.MCPServerConfig, workspacePath string) (probeResult, error) {
		assert.Equal(t, "filesystem", name)
		assert.Equal(t, readMCPConfig(t, configPath).WorkspacePath(), workspacePath)
		assert.Equal(t, "npx", server.Command)
		return probeResult{ToolCount: 3}, nil
	}

	cmd := NewMCPCommand()
	output, err := executeCommand(cmd, []string{"list", "--status"}, "")
	require.NoError(t, err)
	assert.Contains(t, output, "ok (3 tools)")
}

func TestMCPEditUsesEditor(t *testing.T) {
	configPath := setupMCPConfigEnv(t)

	originalEditor := editorCommand
	defer func() { editorCommand = originalEditor }()

	var gotName string
	var gotArgs []string
	editorCommand = func(name string, args ...string) *exec.Cmd {
		gotName = name
		gotArgs = append([]string(nil), args...)
		return noopCommand()
	}

	t.Setenv("EDITOR", `dummy-editor --wait`)

	cmd := NewMCPCommand()
	output, err := executeCommand(cmd, []string{"edit"}, "")
	require.NoError(t, err)

	assert.Equal(t, "dummy-editor", gotName)
	require.Len(t, gotArgs, 2)
	assert.Equal(t, "--wait", gotArgs[0])
	assert.Equal(t, filepath.Dir(configPath), filepath.Dir(gotArgs[1]))
	assert.Contains(t, output, "No changes.")
	// Nothing is written when the editor changed nothing, and the copy is gone.
	_, statErr := os.Stat(configPath)
	assert.True(t, os.IsNotExist(statErr), "config must not be created without changes")
	_, statErr = os.Stat(gotArgs[1])
	assert.True(t, os.IsNotExist(statErr), "edit copy must be removed")
}

func TestMCPEditWritesOnlyAfterEditorAndKeepsEnvOverridesOut(t *testing.T) {
	configPath := setupMCPConfigEnv(t)
	writeMCPConfig(t, configPath, nil)
	before, err := os.ReadFile(configPath)
	require.NoError(t, err)
	t.Setenv("COMPA_AGENTS_DEFAULTS_MODEL_NAME", "env-only-model")

	originalEditor := editorCommand
	defer func() { editorCommand = originalEditor }()
	editorCommand = func(name string, args ...string) *exec.Cmd {
		draft := args[len(args)-1]
		// The editor sees the file as stored, and the real config is
		// untouched while it runs.
		data, err := os.ReadFile(draft)
		require.NoError(t, err)
		assert.Equal(t, string(before), string(data))
		current, err := os.ReadFile(configPath)
		require.NoError(t, err)
		assert.Equal(t, string(before), string(current))

		var doc map[string]any
		require.NoError(t, json.Unmarshal(data, &doc))
		doc["tools"].(map[string]any)["mcp"].(map[string]any)["servers"] = map[string]any{
			"files": map[string]any{"enabled": true, "command": "npx"},
		}
		edited, err := json.MarshalIndent(doc, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(draft, edited, 0o600))
		return noopCommand()
	}
	t.Setenv("EDITOR", "dummy-editor")

	output, err := executeCommand(NewMCPCommand(), []string{"edit"}, "")
	require.NoError(t, err)
	assert.Contains(t, output, "Config saved.")

	saved, err := os.ReadFile(configPath)
	require.NoError(t, err)
	assert.Contains(t, string(saved), `"files"`)
	assert.NotContains(t, string(saved), "env-only-model")
}

func TestMCPEditRejectsInvalidEdit(t *testing.T) {
	configPath := setupMCPConfigEnv(t)
	writeMCPConfig(t, configPath, nil)
	before, err := os.ReadFile(configPath)
	require.NoError(t, err)

	originalEditor := editorCommand
	defer func() { editorCommand = originalEditor }()
	editorCommand = func(name string, args ...string) *exec.Cmd {
		require.NoError(t, os.WriteFile(args[len(args)-1], []byte(`{"tools": {"mcp": {"servers": {"x": {"enabled": "yes"}}}}`), 0o600))
		return noopCommand()
	}
	t.Setenv("EDITOR", "dummy-editor")

	_, err = executeCommand(NewMCPCommand(), []string{"edit"}, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config not saved")

	after, err := os.ReadFile(configPath)
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after))
}

// The edit is loaded the way the kernel loads config.json: a key it does not
// know, such as a typo, is refused instead of saved.
func TestMCPEditRejectsUnknownKey(t *testing.T) {
	configPath := setupMCPConfigEnv(t)
	writeMCPConfig(t, configPath, nil)
	before, err := os.ReadFile(configPath)
	require.NoError(t, err)

	originalEditor := editorCommand
	defer func() { editorCommand = originalEditor }()
	editorCommand = func(name string, args ...string) *exec.Cmd {
		draft := args[len(args)-1]
		var doc map[string]any
		data, err := os.ReadFile(draft)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(data, &doc))
		doc["tools"].(map[string]any)["mcp"].(map[string]any)["servers"] = map[string]any{
			"files": map[string]any{"enabled": true, "command": "npx", "trustd": true},
		}
		edited, err := json.MarshalIndent(doc, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(draft, edited, 0o600))
		return noopCommand()
	}
	t.Setenv("EDITOR", "dummy-editor")

	_, err = executeCommand(NewMCPCommand(), []string{"edit"}, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config not saved")
	assert.Contains(t, err.Error(), "trustd")

	after, err := os.ReadFile(configPath)
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after))
}

func noopCommand() *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.Command("cmd", "/c", "exit 0")
	}
	return exec.Command("sh", "-c", "exit 0")
}

func TestMCPEditRequiresEditor(t *testing.T) {
	setupMCPConfigEnv(t)
	t.Setenv("EDITOR", "")

	cmd := NewMCPCommand()
	_, err := executeCommand(cmd, []string{"edit"}, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "$EDITOR is not set")
}

func TestMCPTestUsesProbe(t *testing.T) {
	configPath := setupMCPConfigEnv(t)
	writeMCPConfig(t, configPath, &config.Config{
		Tools: config.ToolsConfig{
			MCP: config.MCPConfig{
				ToolConfig: config.ToolConfig{Enabled: true},
				Servers: map[string]config.MCPServerConfig{
					"filesystem": {
						Enabled: false,
						Type:    "stdio",
						Command: "npx",
					},
				},
			},
		},
	})

	originalProbe := serverProbe
	defer func() { serverProbe = originalProbe }()
	serverProbe = func(_ context.Context, name string, _ config.MCPServerConfig, workspacePath string) (probeResult, error) {
		assert.Equal(t, "filesystem", name)
		assert.Equal(t, readMCPConfig(t, configPath).WorkspacePath(), workspacePath)
		return probeResult{ToolCount: 2}, nil
	}

	cmd := NewMCPCommand()
	output, err := executeCommand(cmd, []string{"test", "filesystem"}, "")
	require.NoError(t, err)
	assert.Contains(t, output, `MCP server "filesystem" reachable (2 tools)`)
}

func TestMCPAddDeferredFlag(t *testing.T) {
	configPath := setupMCPConfigEnv(t)

	cmd := NewMCPCommand()
	_, err := executeCommand(cmd, []string{"add", "--deferred", "myserver", "npx", "my-mcp"}, "")
	require.NoError(t, err)

	cfg := readMCPConfig(t, configPath)
	server := cfg.Tools.MCP.Servers["myserver"]
	require.NotNil(t, server.Deferred)
	assert.True(t, *server.Deferred)
}

func TestMCPAddNoDeferredFlag(t *testing.T) {
	configPath := setupMCPConfigEnv(t)

	cmd := NewMCPCommand()
	_, err := executeCommand(cmd, []string{"add", "--no-deferred", "myserver", "npx", "my-mcp"}, "")
	require.NoError(t, err)

	cfg := readMCPConfig(t, configPath)
	server := cfg.Tools.MCP.Servers["myserver"]
	require.NotNil(t, server.Deferred)
	assert.False(t, *server.Deferred)
}

func TestMCPAddNoDeferredByDefault(t *testing.T) {
	configPath := setupMCPConfigEnv(t)

	cmd := NewMCPCommand()
	_, err := executeCommand(cmd, []string{"add", "myserver", "npx", "my-mcp"}, "")
	require.NoError(t, err)

	cfg := readMCPConfig(t, configPath)
	server := cfg.Tools.MCP.Servers["myserver"]
	assert.Nil(t, server.Deferred)
	assert.False(t, server.Trusted)
	assert.Empty(t, server.Cwd)
}

// trusted and cwd are saved, and a config that has them still validates
// when another server is added.
func TestMCPAddTrustedAndCwd(t *testing.T) {
	configPath := setupMCPConfigEnv(t)

	_, err := executeCommand(NewMCPCommand(), []string{
		"add", "--trusted", "--cwd", "~/projects", "files", "npx", "server-files",
	}, "")
	require.NoError(t, err)
	_, err = executeCommand(NewMCPCommand(), []string{"add", "other", "npx", "other-mcp"}, "")
	require.NoError(t, err)

	cfg := readMCPConfig(t, configPath)
	server := cfg.Tools.MCP.Servers["files"]
	assert.True(t, server.Trusted)
	assert.Equal(t, "~/projects", server.Cwd)
	assert.Contains(t, cfg.Tools.MCP.Servers, "other")

	_, err = executeCommand(NewMCPCommand(), []string{
		"add", "--cwd", "/tmp", "-t", "http", "remote", "https://mcp.example.com/mcp",
	}, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--cwd can only be used with stdio transport")
}

func TestMCPShowNotFound(t *testing.T) {
	configPath := setupMCPConfigEnv(t)
	writeMCPConfig(t, configPath, nil)

	cmd := NewMCPCommand()
	_, err := executeCommand(cmd, []string{"show", "missing"}, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"missing" not found`)
}

func TestMCPShowDisabledServer(t *testing.T) {
	configPath := setupMCPConfigEnv(t)
	writeMCPConfig(t, configPath, &config.Config{
		Tools: config.ToolsConfig{
			MCP: config.MCPConfig{
				ToolConfig: config.ToolConfig{Enabled: true},
				Servers: map[string]config.MCPServerConfig{
					"myserver": {
						Enabled: false,
						Type:    "stdio",
						Command: "npx",
						Trusted: true,
						Cwd:     "~/work",
					},
				},
			},
		},
	})

	cmd := NewMCPCommand()
	output, err := executeCommand(cmd, []string{"show", "myserver"}, "")
	require.NoError(t, err)
	assert.Contains(t, output, "myserver")
	assert.Contains(t, output, "disabled")
	assert.Regexp(t, `Trusted\S*\s+yes`, output)
	assert.Contains(t, output, "~/work")
}

func TestMCPShowUsesProbe(t *testing.T) {
	configPath := setupMCPConfigEnv(t)
	writeMCPConfig(t, configPath, &config.Config{
		Tools: config.ToolsConfig{
			MCP: config.MCPConfig{
				ToolConfig: config.ToolConfig{Enabled: true},
				Servers: map[string]config.MCPServerConfig{
					"myserver": {
						Enabled: true,
						Type:    "stdio",
						Command: "npx",
					},
				},
			},
		},
	})

	original := serverShowProbe
	defer func() { serverShowProbe = original }()
	serverShowProbe = func(_ context.Context, name string, _ config.MCPServerConfig, _ string) ([]toolDetail, error) {
		assert.Equal(t, "myserver", name)
		return []toolDetail{
			{
				Name:        "read_file",
				Description: "Read a file from the filesystem",
				Parameters: []paramDetail{
					{Name: "path", Type: "string", Description: "File path", Required: true},
					{Name: "encoding", Type: "string", Description: "Character encoding", Required: false},
				},
			},
			{
				Name:        "list_dir",
				Description: "List directory contents",
				Parameters:  nil,
			},
		}, nil
	}

	cmd := NewMCPCommand()
	output, err := executeCommand(cmd, []string{"show", "myserver"}, "")
	require.NoError(t, err)
	assert.Contains(t, output, "myserver")
	assert.Contains(t, output, "read_file")
	assert.Contains(t, output, "Read a file from the filesystem")
	assert.Contains(t, output, "path")
	assert.Contains(t, output, "string")
	assert.Contains(t, output, "required")
	assert.Contains(t, output, "list_dir")
	assert.Contains(t, output, "none")
}

func setupMCPConfigEnv(t *testing.T) string {
	t.Helper()

	configPath := filepath.Join(t.TempDir(), "config.json")
	t.Setenv(config.EnvConfig, configPath)
	t.Setenv(config.EnvHome, filepath.Dir(configPath))
	return configPath
}

func writeMCPConfig(t *testing.T, path string, cfg *config.Config) {
	t.Helper()

	if cfg == nil {
		cfg = config.DefaultConfig()
	}

	require.NoError(t, config.SaveConfig(path, cfg))
}

func readMCPConfig(t *testing.T, path string) *config.Config {
	t.Helper()

	cfg, err := config.LoadConfig(path)
	require.NoError(t, err)
	return cfg
}

func executeCommand(cmd *cobra.Command, args []string, stdin string) (string, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	cmd.SetArgs(args)
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetIn(strings.NewReader(stdin))

	err := cmd.Execute()
	return stdout.String() + stderr.String(), err
}
