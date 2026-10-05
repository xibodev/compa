// Package credential resolves secret values in the config, such as channel tokens and tool API keys.
//
// Supported formats:
//
//   - Plaintext:   "sk-abc123"          → returned as-is
//   - File ref:    "file://filename.key" → content read from configDir/filename.key
//   - Empty:       ""                   → returned as-is (a provider that needs no key)
package credential

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FileScheme prefixes a reference to a file, beside the config, that holds
// the value.
const FileScheme = "file://"

// Resolver resolves raw credential strings from config secret fields.
// File references are resolved relative to the directory of the config file.
type Resolver struct {
	configDir         string
	resolvedConfigDir string // symlink-resolved form of configDir
}

// NewResolver returns a Resolver that resolves file:// references relative to
// configDir (typically filepath.Dir of the config file path).
func NewResolver(configDir string) *Resolver {
	resolved := configDir
	if configDir != "" {
		if linkedPath, err := filepath.EvalSymlinks(configDir); err == nil {
			resolved = linkedPath
		}
	}
	return &Resolver{configDir: configDir, resolvedConfigDir: resolved}
}

// Resolve returns the actual credential value for raw:
//
//   - ""                → "" (no error; a provider that needs no key)
//   - "file://name.key" → trimmed content of configDir/name.key
//   - anything else     → raw unchanged (plaintext credential)
func (r *Resolver) Resolve(raw string) (string, error) {
	if !strings.HasPrefix(raw, FileScheme) {
		return raw, nil
	}

	fileName := strings.TrimSpace(strings.TrimPrefix(raw, FileScheme))
	if fileName == "" {
		return "", fmt.Errorf("credential: file:// reference has no filename")
	}

	baseDir := r.resolvedConfigDir
	if baseDir == "" {
		baseDir = r.configDir
	}
	keyPath := filepath.Join(baseDir, fileName)
	// Resolve symlinks before enforcing containment to prevent escaping via symlinks.
	realKeyPath, err := filepath.EvalSymlinks(keyPath)
	if err != nil {
		return "", fmt.Errorf("credential: failed to resolve credential file path %q: %w", keyPath, err)
	}
	if !isWithinDir(realKeyPath, baseDir) {
		return "", fmt.Errorf("credential: file:// path escapes config directory")
	}
	data, err := os.ReadFile(realKeyPath)
	if err != nil {
		return "", fmt.Errorf("credential: failed to read credential file %q: %w", realKeyPath, err)
	}

	value := strings.TrimSpace(string(data))
	if value == "" {
		return "", fmt.Errorf("credential: credential file %q is empty", realKeyPath)
	}
	return value, nil
}

// isWithinDir reports whether path is contained within (or equal to) dir.
// Uses filepath.IsLocal on the relative path for robust cross-platform traversal detection.
func isWithinDir(path, dir string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	return err == nil && filepath.IsLocal(rel)
}
