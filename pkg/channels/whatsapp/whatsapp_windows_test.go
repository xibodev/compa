//go:build windows

package whatsapp

import (
	"os"
	"path/filepath"
	"testing"
)

// A media path elsewhere is refused before Compa resolves it: resolving
// \\host\share\x would already reach that host.
func TestBridgeMediaPathRefusesNetworkAndDevicePaths(t *testing.T) {
	for _, p := range []string{
		`\\compa-no-such-host.invalid\share\media.jpg`,
		`\\.\pipe\compa-no-such-pipe`,
		`\\?\C:\compa-no-such-dir\media.jpg`,
		// Same volume as the temp folder, outside it: refused as written,
		// not as missing.
		filepath.VolumeName(os.TempDir()) + `\compa-no-such-dir\media.jpg`,
	} {
		if _, err := bridgeMediaPath(p, 0); err == nil || err.Error() != "outside the temp directory" {
			t.Errorf("bridgeMediaPath(%q) error = %v, want it refused as outside the temp directory", p, err)
		}
	}
}
