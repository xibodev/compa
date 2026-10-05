//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// attachParentProcess is ATTACH_PARENT_PROCESS.
const attachParentProcess = ^uint32(0)

var procAttachConsole = windows.NewLazySystemDLL("kernel32.dll").NewProc("AttachConsole")

// attachParentConsole gives the launcher, built as a GUI program so that no
// console window opens at login, the console of the terminal it was started
// from: for -console, -password and the usage text. Output redirected to a
// file or pipe is left alone. It reports whether it attached; the logger
// keeps the standard output it saw at start, so its console lines stay lost
// then.
func attachParentConsole() bool {
	if stdHandleUsable(windows.STD_OUTPUT_HANDLE) {
		return false
	}
	if ok, _, _ := procAttachConsole.Call(uintptr(attachParentProcess)); ok == 0 {
		return false
	}
	if out, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		os.Stdout = out
		_ = windows.SetStdHandle(windows.STD_OUTPUT_HANDLE, windows.Handle(out.Fd()))
		if !stdHandleUsable(windows.STD_ERROR_HANDLE) {
			os.Stderr = out
			_ = windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(out.Fd()))
		}
	}
	if !stdHandleUsable(windows.STD_INPUT_HANDLE) {
		if in, err := os.OpenFile("CONIN$", os.O_RDWR, 0); err == nil {
			os.Stdin = in
			_ = windows.SetStdHandle(windows.STD_INPUT_HANDLE, windows.Handle(in.Fd()))
		}
	}
	return true
}

func stdHandleUsable(which uint32) bool {
	handle, err := windows.GetStdHandle(which)
	if err != nil || handle == 0 || handle == windows.InvalidHandle {
		return false
	}
	kind, err := windows.GetFileType(handle)
	return err == nil && kind != windows.FILE_TYPE_UNKNOWN
}
