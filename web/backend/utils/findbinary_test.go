package utils

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// The launcher must find the binary the build produces: the kernel, which
// ships as compa-kernel(.exe) whether it sits beside the shell or stands alone.
func TestKernelBinaryIsFound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, kernelNameForOS())
	if err := os.WriteFile(path, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	// EnvBinary is the explicit override and takes precedence, which is
	// the mechanism this test can use to point the lookup at a
	// controlled directory without moving the test executable.
	t.Setenv("COMPA_BINARY", path)

	got := FindKernelBinary()
	if got != path {
		t.Fatalf("FindKernelBinary() = %q, want %q. The build"+
			" produces compa-kernel(.exe); a lookup that misses it"+
			" leaves the gateway unstartable", got, path)
	}
}

// The kernel has exactly one name, and it is not the shell's.
//
// Pinned literally: the Makefile, the installers, and the UAT harness all
// build compa-kernel. A second candidate such as "compa" would resolve to the
// shell itself when both sit in one directory, and the shell would then try
// to supervise itself as the gateway.
func TestKernelBinaryNameIsPinned(t *testing.T) {
	if got, want := KernelBinaryName(), kernelNameForOS(); got != want {
		t.Fatalf("KernelBinaryName() = %q, want %q", got, want)
	}
}

// An absent binary returns the kernel name rather than an empty string.
//
// Returning "" would make exec fail with a message about an empty path, which
// is less useful than a name a person can search for.
func TestAnAbsentBinaryFallsBackToAName(t *testing.T) {
	t.Setenv("COMPA_BINARY", "")
	got := FindKernelBinary()
	if got == "" {
		t.Fatal("returned an empty path; exec would then fail with a message" +
			" naming nothing at all")
	}
}

func kernelNameForOS() string {
	if runtime.GOOS == "windows" {
		return "compa-kernel.exe"
	}
	return "compa-kernel"
}
