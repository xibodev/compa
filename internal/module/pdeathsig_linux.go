//go:build linux

package module

import "syscall"

// setParentDeathSignal has the kernel kill the module if the host dies first,
// so stopping the gateway does not leave modules running.
func setParentDeathSignal(attr *syscall.SysProcAttr) {
	attr.Pdeathsig = syscall.SIGKILL
}
