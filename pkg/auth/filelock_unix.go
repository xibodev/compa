//go:build !windows

package auth

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

// tryLockFile takes the exclusive lock on f if no one holds it, reporting
// whether it did.
func tryLockFile(f *os.File) (bool, error) {
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		switch err {
		case nil:
			return true, nil
		case unix.EWOULDBLOCK:
			return false, nil
		case unix.EINTR:
			continue
		}
		return false, err
	}
}

func unlockFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}
