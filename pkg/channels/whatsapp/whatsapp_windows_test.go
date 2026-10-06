//go:build windows

package whatsapp

import "testing"

// A media path on another computer or a device is refused before Compa
// resolves it: resolving \\host\share\x would already reach that host.
func TestBridgeMediaPathRefusesNetworkAndDevicePaths(t *testing.T) {
	for _, p := range []string{
		`\\compa-no-such-host.invalid\share\media.jpg`,
		`\\.\pipe\compa-no-such-pipe`,
		`\\?\C:\compa-no-such-dir\media.jpg`,
	} {
		if _, err := bridgeMediaPath(p, 0); err == nil || err.Error() != "outside the temp directory" {
			t.Errorf("bridgeMediaPath(%q) error = %v, want it refused as outside the temp directory", p, err)
		}
	}

	// A temp folder that is itself on a share keeps its media.
	if otherNetworkVolume(`\\server\share\Temp\media.jpg`, `\\server\share\Temp`) {
		t.Error("a file in a temp folder on a share counts as on another volume")
	}
	if otherNetworkVolume(`C:\Temp\media.jpg`, `C:\Temp`) {
		t.Error("a local path counts as on a network volume")
	}
}
