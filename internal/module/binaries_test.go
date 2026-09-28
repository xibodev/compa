package module_test

import (
	"runtime"
	"testing"

	"github.com/xibodev/compa/internal/module"
	"github.com/xibodev/compa/pkg/modproto"
)

// hostBinary is a binary that genuinely exists on every supported platform, so
// the test proves resolution works rather than only that failure works.
func hostBinary() string {
	if runtime.GOOS == "windows" {
		return "cmd"
	}
	return "sh"
}

func TestResolveBinariesSuppliesAbsolutePaths(t *testing.T) {
	resolved, missing := module.ResolveBinaries([]string{hostBinary()})

	path, ok := resolved[hostBinary()]
	if !ok {
		t.Fatalf("%q was not resolved (missing: %v)", hostBinary(), missing)
	}
	if path == "" {
		t.Fatal("a resolved binary must never map to an empty string; absence and emptiness must stay distinguishable")
	}
	if len(path) < 2 || (path[0] != '/' && path[1] != ':') {
		t.Errorf("resolved path %q is not absolute", path)
	}
}

// TestUnresolvableBinaryIsOmittedNotEmpty guards the null-vs-zero rule applied
// to paths: a declared binary the host could not find must be ABSENT, so a
// module cannot mistake "supplied as nothing" for "supplied".
func TestUnresolvableBinaryIsOmittedNotEmpty(t *testing.T) {
	resolved, missing := module.ResolveBinaries([]string{"definitely-not-a-real-binary-xyzzy"})

	if v, present := resolved["definitely-not-a-real-binary-xyzzy"]; present {
		t.Errorf("unresolvable binary was supplied as %q; it must be omitted entirely", v)
	}
	if len(missing) != 1 {
		t.Errorf("missing = %v, want exactly the unresolved name", missing)
	}
}

// TestUndeclaredBinaryIsNeverSupplied is the authority test: resolution happens
// only for names the module declared, so the descriptor governs what it can
// execute.
func TestUndeclaredBinaryIsNeverSupplied(t *testing.T) {
	d := &modproto.Descriptor{
		Permissions: modproto.Permissions{Subprocess: []string{}},
	}
	req := &modproto.Request{}
	module.GrantBinaries(d, req)

	if len(req.Binaries) != 0 {
		t.Errorf("a module declaring no subprocess binaries received %v", req.Binaries)
	}

	// Declaring one supplies exactly one, and nothing else leaks in.
	d.Permissions.Subprocess = []string{hostBinary()}
	module.GrantBinaries(d, req)
	if len(req.Binaries) != 1 {
		t.Errorf("got %d binaries, want exactly the one declared: %v", len(req.Binaries), req.Binaries)
	}
}

// TestDeclarationMustBeANameNotAPath stops a module nominating an arbitrary
// executable and having the host bless it with an absolute path.
func TestDeclarationMustBeANameNotAPath(t *testing.T) {
	for _, bad := range []string{
		"/usr/bin/evil",
		`C:\Windows\System32\evil.exe`,
		"../../evil",
		"sub/dir/tool",
		"",
	} {
		resolved, missing := module.ResolveBinaries([]string{bad})
		if len(resolved) != 0 {
			t.Errorf("declaration %q resolved to %v; a declaration must be a bare name", bad, resolved)
		}
		if len(missing) != 1 {
			t.Errorf("declaration %q should be reported as unresolved", bad)
		}
	}
}
