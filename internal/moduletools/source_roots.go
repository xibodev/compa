package moduletools

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/xibodev/compa/v4/pkg/config"
	"github.com/xibodev/compa/v4/pkg/modproto"
)

// Source roots are the locations the OPERATOR lends a module to read.
//
// A module names every root it wants in filesystem_read; it never names a path.
// The host fills some of those names on its own -- the workspace, the module's
// bundle root, and anything the module also declares writable, which is its
// private state. Every other name can only be supplied by the operator, who
// maps it to a path in the config (modules.source_roots) or for one invocation
// on the command line (module-invoke --source-root name=path).
//
// The host ships no names and no default paths. A module that wants a user's
// data says what it calls it, the user says where it is, and nothing in
// between guesses. An unconfigured name is simply not granted, and
// SourceRootWarnings says so in terms the operator can act on.

// SourceRootNames returns the read roots a module declares that only the
// operator can supply, in declaration order and without duplicates.
func SourceRootNames(d *modproto.Descriptor) []string {
	if d == nil {
		return nil
	}
	writable := make(map[string]bool, len(d.Permissions.FilesystemWrite))
	for _, name := range d.Permissions.FilesystemWrite {
		writable[name] = true
	}
	seen := map[string]bool{}
	var out []string
	for _, name := range d.Permissions.FilesystemRead {
		if seen[name] || writable[name] || isHostRootName(name) {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// isHostRootName reports whether the host supplies a read root of this name
// itself, for every module, so it can never come from the operator.
func isHostRootName(name string) bool {
	return name == "workspace" || isBundleRootName(name)
}

// ResolveSourceRoot checks one source root and returns the path to grant.
//
// The shape rules are the config's own (a usable name, an absolute path), so a
// command-line root is held to exactly what a configured one is. On top of that
// the path must EXIST, and be a directory or a regular file: a root is handed
// over as-is and the module decides what to do with it, so a store kept as a
// single database file is as good as a directory of sessions. Anything else --
// a socket, a device -- is refused rather than guessed at.
//
// Nothing is ever substituted. A root that fails here is not granted, and the
// caller says why; the module then fails closed instead of reading something
// the operator did not point it at.
func ResolveSourceRoot(name, path string) (string, error) {
	if err := config.ValidateSourceRoot(name, path); err != nil {
		return "", err
	}
	if isHostRootName(name) {
		return "", fmt.Errorf("the host supplies %q itself, so it cannot be a source root", name)
	}
	clean := filepath.Clean(path)
	info, err := os.Stat(clean)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("%s does not exist", clean)
	case err != nil:
		return "", fmt.Errorf("%s cannot be read: %w", clean, err)
	case !info.IsDir() && !info.Mode().IsRegular():
		return "", fmt.Errorf("%s is neither a directory nor a regular file", clean)
	}
	return clean, nil
}

// SourceRootWarnings reports every source root a module declares that the host
// will NOT grant from sources, and what the operator can do about it.
//
// A warning rather than a refusal, like a missing bundle root: a module may not
// need every root for every capability, and refusing it outright for one it
// might not use would be worse than showing what is missing. What must not
// happen is silence -- a module whose data root was never granted describes
// itself fine, lists its capabilities fine, and fails only when invoked, for a
// reason nothing on screen explains.
func SourceRootWarnings(d *modproto.Descriptor, sources map[string]string) []string {
	var out []string
	for _, name := range SourceRootNames(d) {
		path, configured := sources[name]
		if !configured {
			out = append(out, fmt.Sprintf(
				"declares source root %q, which is not configured, so it is not"+
					" granted. Set it in the Compa config file as"+
					" \"modules\": {\"source_roots\": {%q: \"<absolute path>\"}},"+
					" or pass --source-root %s=<absolute path> to module-invoke",
				name, name, name))
			continue
		}
		if _, err := ResolveSourceRoot(name, path); err != nil {
			out = append(out, fmt.Sprintf(
				"declares source root %q, which is not granted: %v. Point"+
					" modules.source_roots.%s at an existing absolute directory"+
					" or file", name, err, name))
		}
	}
	return out
}

// hostSourceRoots reads the operator's configured source roots for a caller
// that was handed no config of its own -- the agent, whose composition root
// passes the module host only its state root.
//
// It reads the file this process is configured from: COMPA_CONFIG when set,
// which is how the launcher points the gateway it supervises at its own config,
// and otherwise config.json under the state root. That is the rule the CLI and
// the launcher already apply, so the agent grants what the Modules page and
// module-invoke grant. A missing file configures nothing.
func hostSourceRoots(home string) (map[string]string, error) {
	path := os.Getenv(config.EnvConfig)
	if path == "" {
		path = filepath.Join(home, "config.json")
	}
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		return nil, fmt.Errorf("could not read source roots from %s: %w", path, err)
	}
	return cfg.Modules.SourceRoots, nil
}
