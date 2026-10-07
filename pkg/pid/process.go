package pid

import "errors"

// ErrProcessNotFound reports that no running process has the PID.
var ErrProcessNotFound = errors.New("process not found")

// IsProcessRunning reports whether a process with pid is alive: on Windows
// through OpenProcess and GetExitCodeProcess, elsewhere through signal 0.
func IsProcessRunning(pid int) bool {
	return isProcessRunning(pid)
}
