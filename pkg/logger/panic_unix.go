//go:build !windows

package logger

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

func initPanicFile(panicFile string) (io.WriteCloser, error) {
	file, err := os.OpenFile(panicFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND|os.O_SYNC, 0o600)
	if err != nil {
		return nil, err
	}
	_ = file.Chmod(0o600)
	if err = unix.Dup2(int(file.Fd()), int(os.Stderr.Fd())); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("redirect stderr: %w", err)
	}
	return file, nil
}
