//go:build !windows

package module

import "errors"

// runMklinkJunction is never called off Windows.
func runMklinkJunction(_, _ string) error {
	return errors.New("junctions are Windows-only")
}
