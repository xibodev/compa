package moduletools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xibodev/compa/v2/internal/module"
)

// disabledMarker is the file that makes an installed module inert.
//
// A marker file rather than a registry entry, for the same reason installation
// is a file on disk and not a registry row: there is nothing to drift out of
// sync with what is actually installed. Removing a module directory removes its
// state with it, and the marker is visible to anyone who looks.
const disabledMarker = module.DisabledMarker

// Disabled reports whether the module installed at dir has been turned off.
//
// A module directory that cannot be read is treated as ENABLED: refusing to run
// a module because the host could not stat a marker would be a silent
// capability loss, and the failure to read is itself surfaced elsewhere.
func Disabled(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, disabledMarker))
	return err == nil
}

// SetDisabled turns a module off or on.
//
// Disabling does NOT uninstall: the binary, its state and its declared content
// stay exactly where they are, so re-enabling restores what the user had rather
// than re-fetching it.
func SetDisabled(dir string, off bool) error {
	marker := filepath.Join(dir, disabledMarker)
	if !off {
		if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("could not enable: %w", err)
		}
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// The content explains itself to whoever finds the file, since a bare
	// dotfile in a module directory is otherwise a mystery.
	const note = "This module is disabled in Compa.\n" +
		"Its capabilities are not offered to the agent while this file exists.\n" +
		"Delete this file, or re-enable it on the Modules page, to turn it back on.\n"
	if err := os.WriteFile(marker, []byte(note), 0o644); err != nil {
		return fmt.Errorf("could not disable: %w", err)
	}
	return nil
}

// ModuleDir is the directory an installed module occupies.
//
// It resolves from the BINARY rather than from the module id, because a module
// may be installed as <modules>/<id>/<binary> or as a bare binary directly in
// <modules>/ -- and a bare binary has no directory of its own to disable.
func ModuleDir(home, binary string) (string, bool) {
	root, err := filepath.Abs(ModulesDir(home))
	if err != nil {
		return "", false
	}
	abs, err := filepath.Abs(binary)
	if err != nil {
		return "", false
	}
	dir := filepath.Dir(abs)
	if strings.EqualFold(filepath.Clean(dir), filepath.Clean(root)) {
		// Installed as a bare binary directly in <modules>/: there is no
		// per-module directory, so a marker there would disable EVERY module.
		return "", false
	}
	return dir, true
}
