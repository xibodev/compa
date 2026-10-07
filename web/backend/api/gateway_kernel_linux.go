//go:build linux

package api

import (
	"os/exec"
	"syscall"
)

// applyKernelProcAttrs has the kernel receive SIGTERM, its graceful
// shutdown, when the launcher dies, so it never outlives it.
func applyKernelProcAttrs(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Pdeathsig = syscall.SIGTERM
}
