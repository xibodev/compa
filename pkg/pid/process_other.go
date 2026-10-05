//go:build !windows && !linux

package pid

import "errors"

// ProcessImagePath is not available without /proc; callers fall back to ps.
func ProcessImagePath(int) (string, error) {
	return "", errors.ErrUnsupported
}

// ProcessCommandLine is not available without /proc; callers fall back to ps.
func ProcessCommandLine(int) ([]string, error) {
	return nil, errors.ErrUnsupported
}

// ProcessStartFingerprint is not available without /proc.
func ProcessStartFingerprint(int) string {
	return ""
}
