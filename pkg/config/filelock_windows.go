//go:build windows

package config

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockFile blocks until it holds an exclusive lock on the first byte of f.
// Windows releases the lock when the holder closes f or exits.
func lockFile(f *os.File) error {
	var overlapped windows.Overlapped
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &overlapped)
}

func unlockFile(f *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &overlapped)
}
