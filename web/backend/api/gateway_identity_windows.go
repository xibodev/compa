//go:build windows

package api

import (
	"errors"

	ppid "github.com/xibodev/compa/pkg/pid"
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
