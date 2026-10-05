//go:build !windows && !linux

package api

import "os/exec"

// applyKernelProcAttrs has no parent-death signal to set on this system.
func applyKernelProcAttrs(*exec.Cmd) {}
