//go:build !windows

package module

import (
	"os"

	"golang.org/x/sys/unix"
)

// lockFile blocks until it holds an exclusive advisory lock on f. The kernel
// releases the lock when the holder closes f or exits.
func lockFile(f *os.File) error {
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX)
		if err != unix.EINTR {
			return err
		}
	}
}

func unlockFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}
