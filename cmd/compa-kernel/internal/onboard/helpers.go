package onboard

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/xibodev/compa/v3/cmd/compa-kernel/internal"
	"github.com/xibodev/compa/v3/cmd/compa-kernel/internal/cliui"
	"github.com/xibodev/compa/v3/pkg/config"
)

func onboard(force bool) error {
	configPath := internal.GetConfigPath()

	cfg := config.DefaultConfig()
	if _, err := os.Stat(configPath); err == nil {
		// An existing config is kept.
		if cfg, err = config.LoadConfig(configPath); err != nil {
			return fmt.Errorf("loading existing config: %w", err)
		}
	}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}

	workspace := cfg.WorkspacePath()
	if err := createWorkspaceTemplates(workspace, force); err != nil {
		return err
	}

	cliui.PrintOnboardComplete(internal.Logo, configPath)
	return nil
}

func createWorkspaceTemplates(workspace string, force bool) error {
	kept, err := copyEmbeddedToTarget(workspace, force)
	if err != nil {
		return fmt.Errorf("copying workspace templates: %w", err)
	}
	if len(kept) > 0 {
		fmt.Printf("Kept %d workspace file(s) you changed; run `compa-kernel onboard --force` to replace them with the defaults (each is backed up first).\n",
			len(kept))
	}
	return nil
}

// copyEmbeddedToTarget writes the bundled workspace files into targetDir. A
// file that already exists with other content is the user's: it is kept and
// returned in kept, unless force is set, which replaces it after copying it
// to <file>.bak-<time>.
func copyEmbeddedToTarget(targetDir string, force bool) (kept []string, err error) {
	// Ensure target directory exists
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return nil, fmt.Errorf("Failed to create target directory: %w", err)
	}
	stamp := time.Now().Format("20060102-150405")

	// Walk through all files in embed.FS
	err = fs.WalkDir(embeddedFiles, "workspace", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Skip directories
		if d.IsDir() {
			return nil
		}

		// Read embedded file
		data, err := embeddedFiles.ReadFile(path)
		if err != nil {
			return fmt.Errorf("Failed to read embedded file %s: %w", path, err)
		}

		new_path, err := filepath.Rel("workspace", path)
		if err != nil {
			return fmt.Errorf("Failed to get relative path for %s: %v\n", path, err)
		}

		// Build target file path
		targetPath := filepath.Join(targetDir, new_path)

		existing, readErr := os.ReadFile(targetPath)
		switch {
		case readErr == nil && bytes.Equal(existing, data):
			return nil
		case readErr == nil && !force:
			kept = append(kept, targetPath)
			return nil
		case readErr == nil:
			backup := targetPath + ".bak-" + stamp
			if err := os.WriteFile(backup, existing, 0o600); err != nil {
				return fmt.Errorf("Failed to back up %s: %w", targetPath, err)
			}
		case !os.IsNotExist(readErr):
			return fmt.Errorf("Failed to read %s: %w", targetPath, readErr)
		}

		// Ensure target file's directory exists
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return fmt.Errorf("Failed to create directory %s: %w", filepath.Dir(targetPath), err)
		}

		// Scripts (a skill's helpers) must stay executable.
		mode := os.FileMode(0o644)
		if bytes.HasPrefix(data, []byte("#!")) {
			mode = 0o755
		}
		if err := os.WriteFile(targetPath, data, mode); err != nil {
			return fmt.Errorf("Failed to write file %s: %w", targetPath, err)
		}

		return nil
	})

	return kept, err
}
