package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"go.mau.fi/util/shlex"

	"github.com/xibodev/compa/v2/cmd/compa-kernel/internal"
	"github.com/xibodev/compa/v2/pkg/config"
	"github.com/xibodev/compa/v2/pkg/fileutil"
)

func newEditCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "edit",
		Short: "Open the Compa config in $EDITOR",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			editor := strings.TrimSpace(os.Getenv("EDITOR"))
			if editor == "" {
				return fmt.Errorf("$EDITOR is not set")
			}

			editorArgs, err := shlex.Split(editor)
			if err != nil {
				return fmt.Errorf("failed to parse $EDITOR: %w", err)
			}
			if len(editorArgs) == 0 {
				return fmt.Errorf("$EDITOR is empty")
			}

			// The editor works on a copy of the file as stored. Nothing is
			// written before it exits: loading would merge environment
			// overrides into the file. The copy goes back only when it changed
			// and still validates.
			configPath := internal.GetConfigPath()
			original, err := os.ReadFile(configPath)
			if os.IsNotExist(err) {
				original, err = json.MarshalIndent(config.DefaultConfig(), "", "  ")
			}
			if err != nil {
				return fmt.Errorf("failed to read config: %w", err)
			}

			dir := filepath.Dir(configPath)
			if err = os.MkdirAll(dir, 0o700); err != nil {
				return fmt.Errorf("failed to create config directory: %w", err)
			}
			draft, err := os.CreateTemp(dir, ".config-edit-*.json")
			if err != nil {
				return fmt.Errorf("failed to create edit copy: %w", err)
			}
			draftPath := draft.Name()
			_, err = draft.Write(original)
			if closeErr := draft.Close(); err == nil {
				err = closeErr
			}
			if err != nil {
				_ = os.Remove(draftPath)
				return fmt.Errorf("failed to write edit copy: %w", err)
			}

			editorArgs = append(editorArgs, draftPath)
			process := editorCommand(editorArgs[0], editorArgs[1:]...)
			process.Stdin = cmd.InOrStdin()
			process.Stdout = cmd.OutOrStdout()
			process.Stderr = cmd.ErrOrStderr()

			if err := process.Run(); err != nil {
				_ = os.Remove(draftPath)
				return fmt.Errorf("failed to start editor: %w", err)
			}

			edited, err := os.ReadFile(draftPath)
			if err != nil {
				return fmt.Errorf("failed to read edited config: %w", err)
			}
			if bytes.Equal(edited, original) {
				_ = os.Remove(draftPath)
				fmt.Fprintln(cmd.OutOrStdout(), "No changes.")
				return nil
			}
			if err := validateEditedConfig(draftPath); err != nil {
				return fmt.Errorf("config not saved: %w (your edits are in %s)", err, draftPath)
			}
			if err := fileutil.WriteFileAtomic(configPath, edited, 0o600); err != nil {
				return fmt.Errorf("failed to save config: %w (your edits are in %s)", err, draftPath)
			}
			_ = os.Remove(draftPath)
			fmt.Fprintln(cmd.OutOrStdout(), "Config saved.")
			return nil
		},
	}
}

// validateEditedConfig checks the edited copy at path, which lies beside
// config.json, the way the kernel loads config.json: strictly, so that an
// unknown key or an invalid value is refused (LoadConfig never writes). Its
// MCP servers must also pass the checks the mcp commands apply.
func validateEditedConfig(path string) error {
	cfg, err := config.LoadConfig(path)
	if err != nil {
		return err
	}
	normalized, err := json.Marshal(normalizedConfigForSave(cfg))
	if err != nil {
		return fmt.Errorf("failed to serialize config: %w", err)
	}
	return validateConfigDocument(normalized)
}
