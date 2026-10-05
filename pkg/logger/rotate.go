package logger

import (
	"fmt"
	"os"
	"runtime"
	"sync"
)

// Rotation defaults: a log file rotates at 10 MB, and five files are kept.
const (
	defaultMaxSizeMB = 10
	defaultMaxFiles  = 5
)

var (
	rotationMaxSize  int64 = defaultMaxSizeMB << 20 // guarded by mu
	rotationMaxFiles       = defaultMaxFiles        // guarded by mu
)

// SetRotation sets when the log file rotates: once it would grow past
// maxSizeMB megabytes, it becomes <file>.1, an older <file>.1 becomes
// <file>.2 and so on, keeping maxFiles files in all, the current one
// included. A value of zero or less selects the default (10 MB, 5 files).
// It applies to the file being written at once.
func SetRotation(maxSizeMB, maxFiles int) {
	if maxSizeMB <= 0 {
		maxSizeMB = defaultMaxSizeMB
	}
	if maxFiles <= 0 {
		maxFiles = defaultMaxFiles
	}
	mu.Lock()
	rotationMaxSize, rotationMaxFiles = int64(maxSizeMB)<<20, maxFiles
	file := logFile
	mu.Unlock()
	if file != nil {
		file.setLimits(int64(maxSizeMB)<<20, maxFiles)
	}
}

func rotationLimits() (int64, int) {
	mu.Lock()
	defer mu.Unlock()
	return rotationMaxSize, rotationMaxFiles
}

// rotatingFile is a log file rotated by size (see SetRotation). Writes after
// Close are dropped: a logger swapped out by EnableFileLogging may still
// hold it for a moment.
type rotatingFile struct {
	mu       sync.Mutex
	path     string
	file     *os.File
	size     int64
	maxSize  int64
	maxFiles int
	closed   bool
}

func openRotatingFile(path string, maxSize int64, maxFiles int) (*rotatingFile, error) {
	file, err := openLogFile(path)
	if err != nil {
		return nil, err
	}
	var size int64
	if info, err := file.Stat(); err == nil {
		size = info.Size()
	}
	return &rotatingFile{path: path, file: file, size: size, maxSize: maxSize, maxFiles: maxFiles}, nil
}

// openLogFile opens path for appending, readable by its owner only. A file
// an earlier version created with wider permissions is narrowed too.
func openLogFile(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	if runtime.GOOS != "windows" {
		_ = file.Chmod(0o600)
	}
	return file, nil
}

func (r *rotatingFile) setLimits(maxSize int64, maxFiles int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.maxSize, r.maxFiles = maxSize, maxFiles
}

func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return len(p), nil
	}
	if r.maxSize > 0 && r.size > 0 && r.size+int64(len(p)) > r.maxSize {
		r.rotateLocked()
	}
	if r.file == nil {
		// Reopening after a rotation failed; try again.
		file, err := openLogFile(r.path)
		if err != nil {
			return len(p), nil
		}
		r.file, r.size = file, 0
	}
	n, err := r.file.Write(p)
	r.size += int64(n)
	return n, err
}

// rotateLocked moves the current file aside and starts a new one. The file
// is closed first, since Windows cannot rename an open file. When it cannot
// be moved, logging goes on in the same file and the next rotation is tried
// after another maxSize bytes.
func (r *rotatingFile) rotateLocked() {
	_ = r.file.Close()
	r.file = nil
	if backups := r.maxFiles - 1; backups > 0 {
		_ = os.Remove(backupName(r.path, backups))
		for i := backups - 1; i >= 1; i-- {
			_ = os.Rename(backupName(r.path, i), backupName(r.path, i+1))
		}
		_ = os.Rename(r.path, backupName(r.path, 1))
	} else {
		_ = os.Remove(r.path)
	}
	file, err := openLogFile(r.path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "logger: reopening %s after rotation failed: %v\n", r.path, err)
		return
	}
	// Counting from zero even when the move failed and the old content is
	// still there, so the next attempt waits for another maxSize bytes.
	r.file, r.size = file, 0
}

func backupName(path string, index int) string {
	return fmt.Sprintf("%s.%d", path, index)
}

func (r *rotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}
