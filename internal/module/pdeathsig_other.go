//go:build !linux && !windows

package module

import "syscall"

// setParentDeathSignal does nothing where the kernel offers no parent-death
// signal; such a host stops its modules through their contexts.
func setParentDeathSignal(*syscall.SysProcAttr) {}
