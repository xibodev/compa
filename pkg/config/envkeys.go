// Compa - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 Compa contributors

package config

import (
	"os"
	"path/filepath"

	"github.com/xibodev/compa/v2/pkg"
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
	// Default: a skills folder beside the executable, if there is one.
	EnvBuiltinSkills = "COMPA_BUILTIN_SKILLS"

	// EnvBinary overrides the path to the compa-kernel executable.
	// Used by the web shell when spawning the gateway subprocess.
	// Default: resolved from the same directory as the current executable.
	EnvBinary = "COMPA_BINARY"

	// EnvGatewayHost overrides the host address for the gateway server.
	// Default: "localhost"
	EnvGatewayHost = "COMPA_GATEWAY_HOST"
)

// GetHome returns the base directory for Compa's data: COMPA_HOME when set,
// otherwise ~/.compa. The launcher, gateway, agent and CLI all resolve it
// here, so they agree on where the data is.
func GetHome() string {
	if home := os.Getenv(EnvHome); home != "" {
		return home
	}
	if homePath, err := os.UserHomeDir(); err == nil && homePath != "" {
		return filepath.Join(homePath, pkg.DefaultHome)
	}
	return "."
}
