//go:build windows

package pid

import (
	"errors"
	"strconv"

	"golang.org/x/sys/windows"
)

// ProcessImagePath returns the full path of the program pid runs, read with
// QueryFullProcessImageNameW. It returns ErrProcessNotFound when no running
// process has the PID.
func ProcessImagePath(pid int) (string, error) {
	handle, err := openRunningProcess(pid)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)

	buf := make([]uint16, windows.MAX_LONG_PATH)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(handle, 0, &buf[0], &size); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buf[:size]), nil
}

// ProcessCommandLine is not read on Windows; the image path identifies the
// program there.
func ProcessCommandLine(int) ([]string, error) {
	return nil, errors.ErrUnsupported
}

// ProcessStartFingerprint returns the creation time of the process pid runs,
// which tells a reused PID from the process that had it before. It returns ""
// when the time cannot be read.
func ProcessStartFingerprint(pid int) string {
	handle, err := openRunningProcess(pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(handle)

	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return ""
	}
	return strconv.FormatInt(creation.Nanoseconds(), 10)
}

// openRunningProcess opens pid for queries. A process object that only
// lingers because someone still holds a handle to it counts as gone.
func openRunningProcess(pid int) (windows.Handle, error) {
	if pid <= 0 {
		return 0, ErrProcessNotFound
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return 0, ErrProcessNotFound
		}
		return 0, err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(handle, &code); err == nil && code != stillActive {
		windows.CloseHandle(handle)
		return 0, ErrProcessNotFound
	}
	return handle, nil
}
