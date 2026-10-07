//go:build !windows

package api

import (
	"os/exec"
	"syscall"

	ppid "github.com/xibodev/compa/v3/pkg/pid"
)

// requestGatewayStop sends SIGTERM, the kernel's graceful shutdown.
func requestGatewayStop(cmd *exec.Cmd, _ *ppid.PidFileData) bool {
	if cmd == nil || cmd.Process == nil {
		return false
	}
	return cmd.Process.Signal(syscall.SIGTERM) == nil
}

// bindKernelLifetime has nothing to do here: on Linux the parent-death signal
// set by applyKernelProcAttrs ties the kernel to the launcher.
func bindKernelLifetime(*exec.Cmd) error { return nil }
