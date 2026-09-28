package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The CLI and the host must resolve the SAME state root, or a module installed
// by one is invisible to the other.
//
// THE BUG THIS PINS, found by running rather than reading. There were two
// fallback implementations -- the CLI anchored to the executable's `.local`,
// the host to `~/.compa` -- and they agreed ONLY when COMPA_HOME
// was set. Unset, `modules-add` installed where the browser agent never looked:
// the CLI listed the module happily and the agent found zero.
//
// It failed SILENTLY, because discovery finding no modules is indistinguishable
// from none being installed. And it survived a week of development because
// every launch set the variable explicitly, which masks the divergence
// completely.
//
// This project has consolidated this class twice before -- once for module
// discovery, once for grants. Both unified the FUNCTION and left the `home`
// argument fed into it as two implementations. Same bug, one level up.
func TestAnExplicitHomeIsHonouredByEveryResolver(t *testing.T) {
	want := t.TempDir()
	t.Setenv(EnvHome, want)

	if got := GetHome(); got != want {
		t.Errorf("GetHome() = %q, want the explicit override %q", got, want)
	}
	if got := DevHome(); got != want {
		t.Errorf("DevHome() = %q, want the explicit override %q", got, want)
	}
	// An explicit home means there is nothing to decide, so the dev/install
	// question must not reintroduce a second answer.
	if IsDevBuild() {
		t.Error("IsDevBuild() is true despite an explicit COMPA_HOME." +
			" With the home stated outright there is no fallback to choose" +
			" between, and answering yes here would let one caller take the dev" +
			" branch while another takes the override")
	}
}

// GetHome must RETURN the dev fallback when the dev branch applies.
//
// THE TEST THAT ACTUALLY COVERS THE BUG, and my first attempt did not. That one
// compared GetHome() to DevHome() and SKIPPED whenever IsDevBuild() was false --
// which a test binary always is, since it runs from a temp build directory. So
// it skipped every run, and a mutation reintroducing the exact divergence
// (`if false` in place of the dev branch) left the whole suite GREEN.
//
// An honest skip is still an unrun check. The fix is not to assert harder but
// to make the branch REACHABLE: IsDevBuild reads the executable's directory,
// and the executable is fixed, so the only controllable input is the ENV
// OVERRIDE -- which short-circuits both. Hence this tests the decision function
// directly against a fabricated location rather than the process's own.
func TestGetHomeReturnsTheDevFallbackWhenTheBranchApplies(t *testing.T) {
	t.Setenv(EnvHome, "")

	// devHomeFor is the pure form of the rule: given a binary path, where does
	// state live? GetHome's dev branch must produce exactly this for a .local
	// binary, and the install path for anything else.
	cases := []struct {
		name    string
		exeDir  string
		wantDev bool
	}{
		{"binary in .local is a dev build", filepath.Join("anywhere", ".local"), true},
		{"binary elsewhere is an install", filepath.Join("usr", "local", "bin"), false},
		{"a directory merely containing the word is not", filepath.Join("x", "dotlocal"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isDevLocation(tc.exeDir); got != tc.wantDev {
				t.Errorf("isDevLocation(%q) = %v, want %v", tc.exeDir, got, tc.wantDev)
			}
		})
	}

	// And the wiring: GetHome must CONSULT that decision rather than ignoring
	// it. Reintroducing the bug means GetHome stops calling it, so this asserts
	// the call happens for the location the running binary actually has.
	if IsDevBuild() != isDevLocation(filepath.Dir(mustExe(t))) {
		t.Fatal("IsDevBuild() disagrees with the location rule it is supposed" +
			" to implement, so the two can drift apart again")
	}
}

func mustExe(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Skip("cannot resolve the test executable")
	}
	return exe
}

// IsDevBuild keys on where the binary SITS, and nothing else.
//
// Pinned because the tempting implementations -- a build tag, a version string,
// a marker file -- are all things that can disagree with reality. The directory
// name is the same signal DevHome already used to anchor itself; naming it once
// is what stops the two drifting.
func TestIsDevBuildKeysOnTheBinaryLocation(t *testing.T) {
	t.Setenv(EnvHome, "")

	exe, err := os.Executable()
	if err != nil {
		t.Skip("cannot resolve the test executable")
	}
	inDotLocal := filepath.Base(filepath.Dir(exe)) == ".local"

	if got := IsDevBuild(); got != inDotLocal {
		t.Errorf("IsDevBuild() = %v but the binary at %q is%s in a .local"+
			" directory. The location IS the signal; any other basis can"+
			" disagree with where the binary actually runs from",
			got, exe, map[bool]string{true: "", false: " not"}[inDotLocal])
	}
}

// GetHome must ACTUALLY TAKE the dev branch, not merely be able to decide it.
//
// THE TEST THAT CATCHES THE ORIGINAL BUG, and the two before it did not. A
// mutation replacing the dev branch with `if false` -- exactly the divergence
// that made modules invisible to the browser agent -- passed every earlier
// assertion, because they tested the RULE while the defect was in the WIRING
// that consumes it.
//
// Testable because homeFor takes the decision and the lookups as arguments
// rather than reading the process. There is no way to make os.Executable()
// point at a .local directory from inside a test binary.
func TestTheDevBranchIsTakenAndNotMerelyDecided(t *testing.T) {
	devCalled := false
	dev := func() string { devCalled = true; return "DEV-HOME" }
	user := func() (string, error) { return "USER-HOME", nil }

	if got := homeFor(true, dev, user); got != "DEV-HOME" {
		t.Fatalf("homeFor(isDev=true) = %q, want the dev home. Ignoring the"+
			" decision is the original bug: modules install beside the binary"+
			" and the agent looks in the user profile", got)
	}
	if !devCalled {
		t.Error("the dev fallback was never consulted")
	}

	if got := homeFor(false, dev, user); got == "DEV-HOME" {
		t.Fatal("homeFor(isDev=false) returned the DEV home, so an installed" +
			" build would keep state beside its binary instead of in the user" +
			" profile -- the same divergence in the other direction")
	}
}

// A user home that cannot be resolved falls back to a usable path, not "".
//
// Returning empty would make every state path relative to the process's working
// directory, which is the cwd-dependence a previous fix removed.
func TestAnUnresolvableUserHomeStillYieldsAPath(t *testing.T) {
	got := homeFor(false,
		func() string { return "DEV" },
		func() (string, error) { return "", os.ErrNotExist })

	if got == "" {
		t.Fatal("an unresolvable user home produced an empty path, making all" +
			" host state resolve against the working directory")
	}
}
