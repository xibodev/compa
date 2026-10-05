package api

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/xibodev/compa/pkg/config"
	"github.com/xibodev/compa/web/backend/utils"
)

// isKernelImage reports whether path is the kernel's executable: the name it
// ships under, or the name of the program COMPA_BINARY points at.
func isKernelImage(path string) bool {
	base := filepath.Base(strings.TrimSpace(path))
	if strings.EqualFold(base, utils.KernelBinaryName()) {
		return true
	}
	if custom := strings.TrimSpace(os.Getenv(config.EnvBinary)); custom != "" {
		return strings.EqualFold(base, filepath.Base(custom))
	}
	return false
}
