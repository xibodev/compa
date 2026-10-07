package utils

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// THE PRODUCTION PATH: no env override, resolution by scanning the directory
// the running executable sits in.
//
// The env-override test above proves only that an explicit path is honoured.
// This is the branch a real install takes, and the one that was broken for two
// days when the name had no hyphen.
func TestScansItsOwnDirectoryForTheKernel(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("cannot locate the test binary: %v", err)
	}
	dir := filepath.Dir(exe)

	name := "compa-kernel"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	planted := filepath.Join(dir, name)
	if err := os.WriteFile(planted, []byte("x"), 0o755); err != nil {
		t.Skipf("cannot plant a binary beside the test executable: %v", err)
	}
	defer os.Remove(planted)

	os.Unsetenv("COMPA_BINARY")

	got := FindKernelBinary()
	if !strings.Contains(filepath.Base(got), "compa-kernel") {
		t.Fatalf("with a kernel planted beside the executable, resolution"+
			" returned %q. The directory scan is the branch a real install"+
			" takes; the env override is not.", got)
	}
}
