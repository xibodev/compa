package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// committedFixtures is testdata/fixtures, seen from this package's folder.
var committedFixtures = filepath.Join("..", "..", fixtureDir)

// The committed fixtures are exactly what genfixtures writes: a change to
// pkg/modproto or to this generator that alters them fails here until they
// are regenerated (go run ./cmd/genfixtures) and committed.
func TestCommittedFixturesAreCurrent(t *testing.T) {
	fresh := t.TempDir()
	if err := generate(fresh); err != nil {
		t.Fatalf("generate: %v", err)
	}
	want := readTree(t, fresh)
	got := readTree(t, committedFixtures)
	for name, data := range want {
		committed, ok := got[name]
		if !ok {
			t.Errorf("%s is generated but not committed; run go run ./cmd/genfixtures", name)
			continue
		}
		if !bytes.Equal(committed, data) {
			t.Errorf("%s differs from what genfixtures writes; run go run ./cmd/genfixtures", name)
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("%s is committed but genfixtures no longer writes it", name)
		}
	}
}

// Two runs write the same bytes, so a regenerated fixture with no semantic
// change produces no diff.
func TestGenerateIsDeterministic(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	if err := generate(a); err != nil {
		t.Fatal(err)
	}
	if err := generate(b); err != nil {
		t.Fatal(err)
	}
	first, second := readTree(t, a), readTree(t, b)
	if len(first) != len(second) {
		t.Fatalf("runs wrote %d and %d files", len(first), len(second))
	}
	for name, data := range first {
		if !bytes.Equal(second[name], data) {
			t.Errorf("%s differs between two runs", name)
		}
	}
}

func readTree(t *testing.T, root string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = data
		return nil
	})
	if err != nil {
		t.Fatalf("read %s: %v", root, err)
	}
	return files
}
