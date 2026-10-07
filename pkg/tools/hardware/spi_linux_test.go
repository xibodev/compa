//go:build linux

package hardwaretools

import (
	"runtime"
	"testing"
	"unsafe"
)

func TestSPIIoctlNumbersFollowArchEncoding(t *testing.T) {
	if size := unsafe.Sizeof(spiTransfer{}); size != 32 {
		t.Fatalf("spi_ioc_transfer is %d bytes, want 32", size)
	}
	wantMode, wantMessage := uintptr(0x40016B01), uintptr(0x40206B00)
	switch runtime.GOARCH {
	case "mips", "mipsle", "mips64", "mips64le", "ppc", "ppc64", "ppc64le", "sparc64":
		wantMode, wantMessage = 0x80016B01, 0x80206B00
	}
	if spiIocWrMode != wantMode || spiIocMessage1 != wantMessage {
		t.Fatalf("SPI_IOC_WR_MODE = %#x, SPI_IOC_MESSAGE(1) = %#x; want %#x, %#x",
			spiIocWrMode, spiIocMessage1, wantMode, wantMessage)
	}
	if spiIocWrMaxSpeedHz != wantMode&^0x1FFFFFFF|4<<16|0x6B04 {
		t.Fatalf("SPI_IOC_WR_MAX_SPEED_HZ = %#x", spiIocWrMaxSpeedHz)
	}
}
