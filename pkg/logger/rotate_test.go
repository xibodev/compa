package logger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestRotatingFileKeepsMaxFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.log")
	file, err := openRotatingFile(path, 100, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	line := strings.Repeat("x", 59) + "\n" // 61 bytes with its letter: one per 100-byte file
	for i := range 9 {
		if _, err := file.Write([]byte(string(rune('a'+i)) + line)); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	// Nine lines, each in a file of its own: the newest three files remain.
	if got := readFile(t, path); !strings.HasPrefix(got, "i") || len(got) != 61 {
		t.Fatalf("current file = %q", got)
	}
	if got := readFile(t, path+".1"); !strings.HasPrefix(got, "h") {
		t.Fatalf("%s.1 = %q", path, got)
	}
	if got := readFile(t, path+".2"); !strings.HasPrefix(got, "g") {
		t.Fatalf("%s.2 = %q", path, got)
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Fatalf("%s.3 exists (err = %v), want at most 3 files", path, err)
	}
}

func TestRotatingFileWithOneFileStartsOver(t *testing.T) {
	path := filepath.Join(t.TempDir(), "one.log")
	file, err := openRotatingFile(path, 10, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	_, _ = file.Write([]byte("first-line\n"))
	_, _ = file.Write([]byte("second\n"))
	if got := readFile(t, path); got != "second\n" {
		t.Fatalf("file = %q", got)
	}
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Fatalf("a backup was kept with max files 1")
	}
}

func TestRotatingFileCountsWhatTheFileAlreadyHolds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.log")
	if err := os.WriteFile(path, []byte(strings.Repeat("o", 90)), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := openRotatingFile(path, 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	_, _ = file.Write([]byte("new line that does not fit\n"))
	if got := readFile(t, path); got != "new line that does not fit\n" {
		t.Fatalf("current file = %q", got)
	}
	if got := readFile(t, path+".1"); got != strings.Repeat("o", 90) {
		t.Fatalf("backup = %q", got)
	}
}

func TestSetRotationAppliesToTheOpenFile(t *testing.T) {
	t.Cleanup(func() { SetRotation(0, 0) })
	path, _ := useLogFile(t)
	SetRotation(1, 2)
	mu.Lock()
	file := logFile
	mu.Unlock()
	file.mu.Lock()
	maxSize, maxFiles := file.maxSize, file.maxFiles
	file.mu.Unlock()
	if maxSize != 1<<20 || maxFiles != 2 {
		t.Fatalf("limits of %s = %d bytes, %d files", path, maxSize, maxFiles)
	}
	SetRotation(0, -1)
	if size, files := rotationLimits(); size != defaultMaxSizeMB<<20 || files != defaultMaxFiles {
		t.Fatalf("defaults = %d bytes, %d files", size, files)
	}
}

func TestWritesAfterCloseAreDropped(t *testing.T) {
	file, err := openRotatingFile(filepath.Join(t.TempDir(), "closed.log"), 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	if n, err := file.Write([]byte("late")); n != 4 || err != nil {
		t.Fatalf("Write() after Close = %d, %v", n, err)
	}
}
