package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// WithFileLock runs fn while holding an exclusive OS lock tied to path, so the
// processes that write path (the launcher, the CLI, onboarding and the kernel)
// take turns. The lock is on a sidecar file, path+".flock", because path is
// replaced by rename, which would drop a lock held on it. The OS releases the
// lock if its holder dies. fn must not take the same lock again.
func WithFileLock(path string, fn func() error) error {
	// Some platforms grant the OS lock per process, so goroutines take turns
	// on a mutex first.
	mu, _ := fileLocks.LoadOrStore(filepath.Clean(path), &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()

	lockPath := path + ".flock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return fmt.Errorf("create lock directory: %w", err)
	}
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("open lock %s: %w", lockPath, err)
	}
	defer f.Close()
	if err := lockFile(f); err != nil {
		return fmt.Errorf("lock %s: %w", lockPath, err)
	}
	defer func() { _ = unlockFile(f) }()
	return fn()
}

var fileLocks sync.Map

// renameFile is os.Rename; tests replace it to make a rename fail.
var renameFile = os.Rename

// writeFilePair replaces two files that must stay consistent. Both contents
// are written to temporary files first, so a failed write changes neither
// file; then first and second are renamed into place. If the second rename
// fails, first gets its previous content back, or is removed when it did not
// exist.
func writeFilePair(firstPath string, firstData []byte, secondPath string, secondData []byte) error {
	previous, readErr := os.ReadFile(firstPath)
	existed := readErr == nil
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return fmt.Errorf("read %s: %w", filepath.Base(firstPath), readErr)
	}

	firstTmp, err := writeTempBeside(firstPath, firstData)
	if err != nil {
		return err
	}
	defer os.Remove(firstTmp)
	secondTmp, err := writeTempBeside(secondPath, secondData)
	if err != nil {
		return err
	}
	defer os.Remove(secondTmp)

	if err := renameWithRetry(firstTmp, firstPath); err != nil {
		return fmt.Errorf("replace %s: %w", filepath.Base(firstPath), err)
	}
	if err := renameWithRetry(secondTmp, secondPath); err != nil {
		if restoreErr := restoreFile(firstPath, previous, existed); restoreErr != nil {
			return fmt.Errorf("replace %s: %w (and %s could not be restored: %v)",
				filepath.Base(secondPath), err, filepath.Base(firstPath), restoreErr)
		}
		return fmt.Errorf("replace %s: %w", filepath.Base(secondPath), err)
	}
	syncDir(filepath.Dir(secondPath))
	return nil
}

// restoreFile puts back the content path had before a pair write.
func restoreFile(path string, previous []byte, existed bool) error {
	if !existed {
		return os.Remove(path)
	}
	tmp, err := writeTempBeside(path, previous)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	return renameWithRetry(tmp, path)
}

// writeTempBeside writes data to a new 0600 temporary file in path's folder,
// synced to disk, and returns its name.
func writeTempBeside(path string, data []byte) (string, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create directory: %w", err)
	}
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	name := f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Chmod(name, 0o600)
	}
	if err != nil {
		_ = os.Remove(name)
		return "", fmt.Errorf("write temp file for %s: %w", filepath.Base(path), err)
	}
	return name, nil
}

// renameWithRetry renames src onto dst. On Windows a reader holding dst open
// makes the rename fail with "Access is denied" for a few milliseconds.
func renameWithRetry(src, dst string) error {
	var err error
	for attempt := 0; attempt < 40; attempt++ {
		if err = renameFile(src, dst); err == nil {
			return nil
		}
		if errors.Is(err, os.ErrNotExist) {
			return err
		}
		time.Sleep(time.Duration(attempt+1) * time.Millisecond)
	}
	return err
}

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}
