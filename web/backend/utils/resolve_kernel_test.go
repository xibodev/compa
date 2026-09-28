package utils

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The shell must resolve a kernel named compa-kernel sitting beside it.
func TestResolvesTheKernelBesideTheShell(t *testing.T) {
	dir := t.TempDir()
	name := "compa-kernel"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COMPA_BINARY", filepath.Join(dir, name))

	got := FindKernelBinary()
	if !strings.Contains(got, "compa-kernel") {
		t.Fatalf("resolved %q, which is not the kernel beside the shell", got)
	}
}
