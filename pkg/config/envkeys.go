// Compa - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 Compa contributors

package config

import (
	"os"
	"path/filepath"

	"github.com/xibodev/compa/pkg"
)

// Runtime environment variable keys for Compa processes.
// These control the location of files and binaries at runtime and are read
// directly via os.Getenv / os.LookupEnv. All Compa-specific keys use the
// COMPA_ prefix. Reference these constants instead of inline string
// literals to keep all supported knobs visible in one place and to prevent
// typos.
const (
	// EnvHome overrides the base directory for all Compa data
	// (config, workspace, skills, auth store, …).
	// Default: ~/.compa
	EnvHome = "COMPA_HOME"

	// EnvConfig overrides the full path to the JSON config file.
	// Default: $COMPA_HOME/config.json
	EnvConfig = "COMPA_CONFIG"

	// EnvBuiltinSkills overrides the directory from which built-in
	// skills are loaded.
	// Default: <cwd>/skills
	EnvBuiltinSkills = "COMPA_BUILTIN_SKILLS"

	// EnvBinary overrides the path to the compa-kernel executable.
	// Used by the web shell when spawning the gateway subprocess.
	// Default: resolved from the same directory as the current executable.
	EnvBinary = "COMPA_BINARY"

	// EnvGatewayHost overrides the host address for the gateway server.
	// Default: "localhost"
	EnvGatewayHost = "COMPA_GATEWAY_HOST"
)

// GetHome is the ONE answer to "where is host state", and every part of the
// host must use it -- launcher, gateway, agent and CLI alike.
//
// WHY THIS COMMENT EXISTS. There was a second implementation in the CLI with a
// DIFFERENT fallback, and the two only agreed when COMPA_HOME was set.
// Unset, the CLI installed modules into an executable-anchored `.local` while
// the agent discovered from `~/.compa` -- so `modules-add` succeeded, the
// CLI listed the module, and THE BROWSER AGENT SAW NOTHING. Discovery finding
// no modules is indistinguishable from none being installed, so it failed
// silently.
//
// It went unnoticed because every launch during development set the variable
// explicitly, which masks the divergence completely.
//
// This project has consolidated this exact class twice before -- once for
// module discovery and once for grants, both recorded in cmd/compa-kernel.
// Those unified the FUNCTIONS and left the `home` argument fed into them as two
// implementations. Same bug, one level up.
func GetHome() string {
	if home := os.Getenv(EnvHome); home != "" {
		return home
	}
	// A dev build keeps state beside the binary. THE HOST HONOURS THIS TOO,
	// which is the whole point: if only the CLI did, `modules-add` would still
	// install where the agent never looks.
	return homeFor(IsDevBuild(), DevHome, os.UserHomeDir)
}

// homeFor is the fallback CHOICE, separated from the process it is asked about.
//
// SPLIT FOR THE SAME REASON isDevLocation IS. GetHome reads os.Executable() and
// os.UserHomeDir(), neither of which a test controls, so the dev branch was
// unreachable from a test binary -- and a mutation deleting that branch
// entirely, which is the ORIGINAL BUG, passed the whole suite. Testing the rule
// was not enough; the WIRING THAT CONSUMES IT is what broke.
func homeFor(isDev bool, devHome func() string, userHome func() (string, error)) string {
	if isDev {
		return devHome()
	}
	if homePath, err := userHome(); err == nil && homePath != "" {
		return filepath.Join(homePath, pkg.DefaultHome)
	}
	return "."
}

// IsDevBuild reports whether this binary is running from a development
// checkout rather than an installed location.
//
// THE SINGLE PLACE THAT DECIDES, so the CLI and the host cannot answer it
// differently. The test is where the binary SITS: a build placed in a `.local`
// directory is a dev build, anything else is an install. That is the same
// signal DevHome already used to anchor itself, named once rather than
// re-derived by each caller.
//
// An explicit COMPA_HOME wins over both paths, so this only decides the
// fallback -- which is the only thing the two implementations ever disagreed
// about.
func IsDevBuild() bool {
	if os.Getenv(EnvHome) != "" {
		return false // explicit wins; there is nothing to decide
	}
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return isDevLocation(filepath.Dir(exe))
}

// isDevLocation is the rule itself, separated from the process it is asked
// about so a test can reach it.
//
// SPLIT DELIBERATELY. IsDevBuild reads os.Executable(), which a test cannot
// control, so a test binary is never a dev build and every assertion about the
// dev branch skips. A mutation reintroducing the original divergence passed the
// whole suite because of exactly that. The decision is testable; the process
// lookup is not, so they are separate functions.
func isDevLocation(exeDir string) bool {
	return filepath.Base(exeDir) == ".local"
}

// DevHome is the fallback for a binary running from a development checkout,
// where writing into the user profile by surprise is the wrong behaviour.
//
// SEPARATE FROM GetHome ON PURPOSE. GetHome is where a real install keeps
// config, credentials, logs and workspace; changing ITS fallback would strand
// an existing install's state. This one answers a narrower question -- "am I a
// dev build, and if so where is my sandbox" -- and callers that want the
// developer behaviour ask for it explicitly.
//
// Anchored to the EXECUTABLE rather than the working directory. Returning a
// bare ".local" resolves against wherever the process happens to be: run from
// anywhere but the repo root and `modules` reported "no modules installed" with
// modules sitting on disk, while `modules-add` copied into one directory and
// verified another. A confident wrong answer, reported by an author who lost an
// install to it.
func DevHome() string {
	if home := os.Getenv(EnvHome); home != "" {
		return home
	}
	exe, err := os.Executable()
	if err != nil {
		return ".local"
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)
	// The binary is normally built INTO .local, so the home is the directory it
	// already sits in rather than one below.
	if filepath.Base(dir) == ".local" {
		return dir
	}
	return filepath.Join(dir, ".local")
}
