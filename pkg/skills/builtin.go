package skills

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/xibodev/compa/v3/pkg/config"
)

// BuiltinDir returns the directory built-in skills are loaded from:
// $COMPA_BUILTIN_SKILLS when set, otherwise a skills folder installed next to
// the executable, otherwise "" (none). The bundled default skills reach the
// agent through onboarding, which copies them into the workspace.
//
// It never falls back to the working directory: launching Compa from a folder
// that happens to contain skills/ must not add those skills to the system
// prompt as built-ins.
func BuiltinDir() string {
	if dir := strings.TrimSpace(os.Getenv(config.EnvBuiltinSkills)); dir != "" {
		return dir
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return installedBuiltinDir(filepath.Dir(exe))
}

// installedBuiltinDir is the install-location rule, split from the process
// lookup so a test can reach it.
func installedBuiltinDir(exeDir string) string {
	if exeDir == "" {
		return ""
	}
	dir := filepath.Join(exeDir, "skills")
	if info, err := os.Stat(dir); err == nil && info.IsDir() {
		return dir
	}
	return ""
}
