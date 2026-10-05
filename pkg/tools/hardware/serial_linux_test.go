//go:build linux

package hardwaretools

import (
	"testing"

	"golang.org/x/sys/unix"
)

func TestSerialSetSpeedEncodesBaudInCflag(t *testing.T) {
	tio := &unix.Termios{Cflag: unix.CREAD | unix.CLOCAL | unix.CS8}
	for _, baud := range []int{115200, 9600} {
		speed, err := serialBaudToUnix(baud)
		if err != nil {
			t.Fatalf("serialBaudToUnix(%d): %v", baud, err)
		}
		if err := serialSetSpeed(tio, speed); err != nil {
			t.Fatalf("serialSetSpeed: %v", err)
		}
		// TCSETS reads the speed from these bits only.
		if tio.Cflag&unix.CBAUD != speed {
			t.Fatalf("baud %d: CBAUD bits = %#x, want %#x", baud, tio.Cflag&unix.CBAUD, speed)
		}
		if tio.Cflag&(unix.CREAD|unix.CLOCAL|unix.CS8) != unix.CREAD|unix.CLOCAL|unix.CS8 {
			t.Fatalf("other control flags were lost: %#x", tio.Cflag)
		}
	}
}
