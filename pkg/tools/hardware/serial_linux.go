//go:build linux

package hardwaretools

import "golang.org/x/sys/unix"

func serialGetTermios(fd int) (*unix.Termios, error) {
	return unix.IoctlGetTermios(fd, unix.TCGETS)
}

// serialSetSpeed stores a B* speed constant. TCSETS takes the speed from the
// CBAUD bits of Cflag and ignores the Ispeed/Ospeed fields, so both are set.
func serialSetSpeed(tio *unix.Termios, speed uint32) error {
	tio.Cflag = tio.Cflag&^unix.CBAUD | speed&unix.CBAUD
	tio.Ispeed = speed
	tio.Ospeed = speed
	return nil
}

func serialSetTermios(fd int, tio *unix.Termios) error {
	return unix.IoctlSetTermios(fd, unix.TCSETS, tio)
}
