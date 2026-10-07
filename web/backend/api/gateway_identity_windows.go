//go:build windows

package api

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/xibodev/compa/v3/pkg/config"
	ppid "github.com/xibodev/compa/v3/pkg/pid"
	"github.com/xibodev/compa/v3/web/backend/utils"
)

// inspectGatewayProcess tells from the program a process runs, read with
// QueryFullProcessImageNameW, whether it is the kernel. A process this user
// may not inspect is inconclusive; the caller then asks the health endpoint.
func inspectGatewayProcess(pid int) (bool, bool) {
	path, err := ppid.ProcessImagePath(pid)
	if errors.Is(err, ppid.ErrProcessNotFound) {
		return false, true
	}
	if err != nil {
		return false, false
	}
	return isKernelImage(path), true
}

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
