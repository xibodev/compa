package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ModulesConfig configures the host for detached modules.
type ModulesConfig struct {
	// SourceRoots are the named locations the host may lend a module to read:
	// root name -> absolute path of an existing directory or file.
	//
	// A module asks for a root by NAME in the filesystem_read list of its
	// descriptor, and the host grants it read-only only when that name is
	// configured here (or supplied for one invocation with
	// `module-invoke --source-root name=path`). Compa itself defines no names
	// and no default paths. Roots the host supplies on its own -- the
	// workspace, a module's bundle and its writable state -- are never taken
	// from here.
	//
	// Loading checks the SHAPE of each entry only. Whether a path exists is
	// checked each time a module is granted it, so a detached drive never
	// makes the whole config unloadable.
	SourceRoots map[string]string `json:"source_roots,omitempty"`
}

// sourceRootNamePattern is what a source root may be called: a name a module
// can declare, and one a command line can pass unambiguously as name=path.
var sourceRootNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ValidateSourceRoot checks the shape of one source root: a usable name and an
// absolute path. It does not touch the filesystem. The error states only what
// is wrong; the caller says where the entry came from.
func ValidateSourceRoot(name, path string) error {
	if !sourceRootNamePattern.MatchString(name) {
		return fmt.Errorf("name %q must start with a letter or digit and use"+
			" only letters, digits, '.', '_' or '-'", name)
	}
	if strings.TrimSpace(path) == "" {
		return errors.New("no path is given")
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("path %q is not absolute", path)
	}
	return nil
}

// ValidateModules checks the module host settings. Source roots are checked for
// shape only; see ModulesConfig.SourceRoots.
func (c *Config) ValidateModules() error {
	if c == nil {
		return nil
	}
	names := make([]string, 0, len(c.Modules.SourceRoots))
	for name := range c.Modules.SourceRoots {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := ValidateSourceRoot(name, c.Modules.SourceRoots[name]); err != nil {
			return fmt.Errorf("modules.source_roots[%q]: %w", name, err)
		}
	}
	return nil
}
