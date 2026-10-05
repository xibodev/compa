//go:build linux

package pid

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
)

// ProcessImagePath returns the path of the program pid runs, read from
// /proc/<pid>/exe. It returns ErrProcessNotFound when no process has the PID.
func ProcessImagePath(pid int) (string, error) {
	if pid <= 0 {
		return "", ErrProcessNotFound
	}
	path, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrProcessNotFound
	}
	return path, err
}

// ProcessCommandLine returns the arguments pid was started with, read from
// /proc/<pid>/cmdline. A zombie has none. It returns ErrProcessNotFound when
// no process has the PID.
func ProcessCommandLine(pid int) ([]string, error) {
	if pid <= 0 {
		return nil, ErrProcessNotFound
	}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrProcessNotFound
	}
	if err != nil {
		return nil, err
	}
	raw = bytes.TrimRight(raw, "\x00")
	if len(raw) == 0 {
		return []string{}, nil
	}
	return strings.Split(string(raw), "\x00"), nil
}

// ProcessStartFingerprint returns the start time of pid in clock ticks since
// boot (field 22 of /proc/<pid>/stat), which tells a reused PID from the
// process that had it before. It returns "" when it cannot be read.
func ProcessStartFingerprint(pid int) string {
	if pid <= 0 {
		return ""
	}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ""
	}
	// The command name in field 2 may hold spaces and parentheses; the
	// fields after it start behind its last ')'.
	end := bytes.LastIndexByte(raw, ')')
	if end < 0 {
		return ""
	}
	fields := strings.Fields(string(raw[end+1:]))
	// fields[0] is field 3 (state), so field 22 is fields[19].
	if len(fields) < 20 {
		return ""
	}
	return fields[19]
}
