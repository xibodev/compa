package module

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// idPattern is the one grammar for a module ID and for a root name a module
// declares. Both become a single directory name under host state, so the
// grammar admits nothing a path could interpret: no separators, no leading
// dot, and only lowercase so two IDs cannot differ only in case on a
// case-insensitive filesystem.
var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// windowsDeviceNames are names Windows maps to devices whatever the extension,
// so "nul" or "con.module" would never name a directory there.
var windowsDeviceNames = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com0": true, "com1": true, "com2": true, "com3": true, "com4": true,
	"com5": true, "com6": true, "com7": true, "com8": true, "com9": true,
	"lpt0": true, "lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true,
	"lpt5": true, "lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// ValidID reports whether id is a usable module ID.
//
// The check that preceded this compared id with filepath.Base(id), which
// accepts "." and "..": Remove("..") deleted the whole Compa home. Every place
// that turns an ID into a path -- install, remove, enable, disable, the API and
// the CLI -- uses this one test.
//
// A trailing dot is refused because Windows drops it, so "fake." would name
// the directory of the module "fake".
func ValidID(id string) bool {
	if id == "." || id == ".." || !idPattern.MatchString(id) || strings.HasSuffix(id, ".") {
		return false
	}
	base, _, _ := strings.Cut(id, ".")
	return !windowsDeviceNames[base]
}

// CheckID returns an error naming the rule when id is not a usable module ID.
func CheckID(id string) error {
	if !ValidID(id) {
		return fmt.Errorf("invalid module ID %q: an ID is 1 to 64 lowercase letters,"+
			" digits, '.', '_' or '-', starting with a letter or digit", id)
	}
	return nil
}

// ValidRootName reports whether a root name a module declares can be used. A
// writable root becomes <home>/state/<module>/<name>, so it is held to the same
// grammar as a module ID.
func ValidRootName(name string) bool { return ValidID(name) }

// ModulesDir is where installed modules live under the host state root.
func ModulesDir(home string) string { return filepath.Join(home, "modules") }

// BinaryName is the file name of an installed module's executable: the module
// ID, with .exe on Windows. Discovery runs nothing else.
func BinaryName(id string) string {
	if runtime.GOOS == "windows" {
		return id + ".exe"
	}
	return id
}

// InstallDir is the directory module id is installed in, after checking the ID
// and that the result is a direct child of the modules directory.
func InstallDir(home, id string) (string, error) {
	if err := CheckID(id); err != nil {
		return "", err
	}
	root, err := filepath.Abs(ModulesDir(home))
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, id)
	if !isDirectChild(root, dir) {
		return "", fmt.Errorf("module ID %q does not name a directory inside %s", id, root)
	}
	return dir, nil
}

// BinaryPath is where an installed module's executable lives.
func BinaryPath(home, id string) (string, error) {
	dir, err := InstallDir(home, id)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, BinaryName(id)), nil
}

// StateDir is a module's own state directory. Removing a module leaves it in
// place, so reinstalling finds the user's work again.
func StateDir(home, id string) string { return filepath.Join(home, "state", id) }

// ScratchDir is the working directory a module runs in.
//
// A module used to run in its own install directory, beside the host's state,
// so a relative path it resolved landed among installed files it could then
// change. The name starts with a dot, which no declared root name can, so it
// never collides with a root under the same state directory.
func ScratchDir(home, id string) string { return filepath.Join(StateDir(home, id), ".scratch") }

// NewRunner returns a runner for the module installed as id: its binary, its
// identity, its scratch directory and the digest recorded when it was
// installed, if any.
func NewRunner(home, id string) (*Runner, error) {
	bin, err := BinaryPath(home, id)
	if err != nil {
		return nil, err
	}
	r := &Runner{Binary: bin, ModuleID: id, WorkDir: ScratchDir(home, id)}
	m, err := ReadManifest(filepath.Dir(bin))
	if err != nil {
		return nil, err
	}
	if m != nil {
		r.Digest = m.SHA256
	}
	return r, nil
}

// isDirectChild reports whether path is exactly one level below root.
func isDirectChild(root, path string) bool {
	root, path = filepath.Clean(root), filepath.Clean(path)
	return filepath.Dir(path) == root && path != root
}

// exists reports whether path names anything at all.
func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
