//go:build windows

package whatsapp

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A media path elsewhere is refused before Compa resolves it: resolving
// \\host\share\x would already reach that host.
func TestBridgeMediaPathRefusesNetworkAndDevicePaths(t *testing.T) {
	paths := []string{
		`\\compa-no-such-host.invalid\share\media.jpg`,
		`\\.\pipe\compa-no-such-pipe`,
		`\\?\C:\compa-no-such-dir\media.jpg`,
	}
	// Beside the temp folder, on its volume: refused as written, not as
	// missing.
	temp, err := filepath.Abs(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if parent := filepath.Dir(temp); parent != temp {
		paths = append(paths, filepath.Join(parent, fmt.Sprintf("compa-no-such-dir-%d", time.Now().UnixNano()), "media.jpg"))
	}
	for _, p := range paths {
		if _, err := bridgeMediaPath(p, 0); err == nil || err.Error() != "outside the temp directory" {
			t.Errorf("bridgeMediaPath(%q) error = %v, want it refused as outside the temp directory", p, err)
		}
	}
}
