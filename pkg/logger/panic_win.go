//go:build windows
// +build windows

package logger

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/windows"
)

func initPanicFile(panicFile string) (io.WriteCloser, error) {
	file, err := os.OpenFile(panicFile, os.O_WRONLY|os.O_CREATE|os.O_SYNC|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	err = windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(file.Fd()))
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("redirect stderr: %w", err)
	}
	os.Stderr = file
	return file, nil
}
