package fileutil

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// The bug this exists for: the temp file was named .tmp-<pid>-<UnixNano> and
// opened with O_EXCL. Windows advances UnixNano about every 15ms, so two
// concurrent writers get the identical name, the loser's O_EXCL open fails with
// "The file exists", and WriteFileAtomic RETURNS AN ERROR -- the write is lost.
//
// hostFs.WriteFile routes through this, so a concurrent tool write could fail
// for a reason that has nothing to do with the caller. Same clock-granularity
// class as the summary-id collision that was dropping summaries.
func TestConcurrentAtomicWritesAllSucceed(t *testing.T) {
	dir := t.TempDir()

	const writers = 16
	errs := make([]error, writers)

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // maximise the chance of one clock tick covering all of them
			errs[i] = WriteFileAtomic(
				filepath.Join(dir, "f"+string(rune('a'+i))+".txt"),
				[]byte("x"), 0o600)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d failed, so its write was silently lost: %v", i, err)
		}
	}
}

// The ordinary single write must still work and land the right bytes.
func TestAtomicWriteLandsItsContent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "one.txt")

	if err := WriteFileAtomic(p, []byte("hello"), 0o600); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}

	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("content = %q, want %q", got, "hello")
	}
}

// No temp file may survive a successful write: a directory slowly filling with
// .tmp- files is the other way this fails.
func TestNoTempFileIsLeftBehind(t *testing.T) {
	dir := t.TempDir()

	if err := WriteFileAtomic(filepath.Join(dir, "x.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if len(e.Name()) > 5 && e.Name()[:5] == ".tmp-" {
			t.Fatalf("a temp file survived the write: %s", e.Name())
		}
	}
}
