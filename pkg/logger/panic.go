package logger

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"time"
)

var (
	panicMu     sync.Mutex
	panicWriter io.WriteCloser
)

// InitPanic opens the panic log at filePath, owner only, and sends the
// process's stderr there. It returns the function a deferred call runs to
// record a panic and exit, or an error when the log cannot be opened.
func InitPanic(filePath string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
		return nil, fmt.Errorf("failed to create log directory: %w", err)
	}
	writer, err := initPanicFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open panic log %s: %w", filePath, err)
	}
	panicMu.Lock()
	if panicWriter != nil {
		_ = panicWriter.Close()
	}
	panicWriter = writer
	panicMu.Unlock()
	return func() {
		defer func() {
			panicMu.Lock()
			_ = writer.Close()
			if panicWriter == writer {
				panicWriter = nil
			}
			panicMu.Unlock()
		}()
		if err := recover(); err != nil {
			RecoverPanicNoExit(err)

			os.Exit(1)
		}
	}, nil
}

func RecoverPanicNoExit(err any) {
	panicMu.Lock()
	defer panicMu.Unlock()
	if panicWriter == nil {
		Errorf("panicWriter is nil, should not happen")
		return
	}
	now := time.Now().Format("2006-01-02 15:04:05")
	stack := debug.Stack()
	// The panic value may quote a secret, as any log line may.
	logMsg := "\n\n====================\n[" + now + "] PANIC OCCURRED: " + Redact(fmt.Sprintf("%v", err)) +
		"\n" + string(stack)

	_, _ = panicWriter.Write([]byte(logMsg))
}
