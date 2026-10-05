//go:build !windows

package module_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// processAlive reports whether a process with this ID is still running. A
// zombie waiting to be reaped counts as gone.
func processAlive(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return false
	}
	return !isZombie(pid)
}

func isZombie(pid int) bool {
	if runtime.GOOS == "linux" {
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			return false
		}
		// The state follows the parenthesised command name.
		i := bytes.LastIndexByte(b, ')')
		return i >= 0 && i+2 < len(b) && b[i+2] == 'Z'
	}
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	return err == nil && strings.HasPrefix(strings.TrimSpace(string(out)), "Z")
}

// killProcess ends a process a test left behind.
func killProcess(pid int) {
	_ = syscall.Kill(pid, syscall.SIGKILL)
}
