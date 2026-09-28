package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The bug this exists for, reported by a module author who lost an install to
// it: Home() returned the literal ".local", resolved against whatever working
// directory the process happened to have.
//
// Run from anywhere but the repo root, `modules` reported "no modules
// installed" with modules sitting on disk -- a confident wrong answer rather
// than an error -- and `modules-add` copied into one directory, verified
// another, and reported a spawn failure on a path that had never been created.
func TestHomeDoesNotDependOnTheWorkingDirectory(t *testing.T) {
	t.Setenv("COMPA_HOME", "")

	from := Home()

	dir := t.TempDir()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer os.Chdir(prev)

	if got := Home(); got != from {
		t.Fatalf("Home() changed with the working directory:\n  before %q\n  after  %q",
			from, got)
	}
}

// It must also be absolute: a relative home is what allowed the two paths to
// diverge in the first place.
func TestHomeIsAbsolute(t *testing.T) {
	t.Setenv("COMPA_HOME", "")

	if got := Home(); !filepath.IsAbs(got) {
		t.Fatalf("Home() = %q, which is relative and will follow the cwd", got)
	}
}

// A relative COMPA_HOME must be anchored too -- the override is the
// documented way to point the host somewhere, and it had the same defect.
func TestARelativeHomeOverrideIsAnchored(t *testing.T) {
	t.Setenv("COMPA_HOME", "some-relative-home")

	got := Home()

	if !filepath.IsAbs(got) {
		t.Fatalf("a relative override stayed relative: %q", got)
	}
	if !strings.HasSuffix(got, "some-relative-home") {
		t.Fatalf("the override was not honoured: %q", got)
	}
}

// An absolute override is used exactly as given.
func TestAnAbsoluteHomeOverrideIsUsedAsIs(t *testing.T) {
	want := t.TempDir()
	t.Setenv("COMPA_HOME", want)

	if got := Home(); got != want {
		t.Fatalf("Home() = %q, want %q", got, want)
	}
}
