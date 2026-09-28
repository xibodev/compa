//go:build windows

package module

import (
	"fmt"
	"os/exec"
)

// runMklinkJunction creates a directory junction, which requires no special
// privilege, unlike a symlink.
func runMklinkJunction(name, target string) error {
	out, err := exec.Command("cmd", "/c", "mklink", "/J", name, target).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, out)
	}
	return nil
}
