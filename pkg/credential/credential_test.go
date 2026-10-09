package credential_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/xibodev/compa/v4/pkg/credential"
)

func TestResolve_PlainKey(t *testing.T) {
	r := credential.NewResolver(t.TempDir())
	got, err := r.Resolve("sk-plaintext-key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "sk-plaintext-key" {
		t.Fatalf("got %q, want %q", got, "sk-plaintext-key")
	}
}

func TestResolve_FileKey_Success(t *testing.T) {
	dir := t.TempDir()
	keyFile := "openai_plain.key"
	if err := os.WriteFile(filepath.Join(dir, keyFile), []byte("sk-from-file\n"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	r := credential.NewResolver(dir)
	got, err := r.Resolve("file://" + keyFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "sk-from-file" {
		t.Fatalf("got %q, want %q", got, "sk-from-file")
	}
}

func TestResolve_FileKey_NotFound(t *testing.T) {
	r := credential.NewResolver(t.TempDir())
	_, err := r.Resolve("file://missing.key")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestResolve_FileKey_Empty(t *testing.T) {
	dir := t.TempDir()
	keyFile := "empty.key"
	if err := os.WriteFile(filepath.Join(dir, keyFile), []byte("   \n"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	r := credential.NewResolver(dir)
	_, err := r.Resolve("file://" + keyFile)
	if err == nil {
		t.Fatal("expected error for empty credential file, got nil")
	}
}

// TestResolve_FileRef_PathTraversal verifies that file:// references cannot escape configDir
// via relative traversal ("../../etc/passwd") or absolute paths ("/abs/path").
func TestResolve_FileRef_PathTraversal(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	// Create a file outside configDir that the traversal would point to.
	outsideFile := filepath.Join(t.TempDir(), "secret.key")
	if err := os.WriteFile(outsideFile, []byte("stolen"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	r := credential.NewResolver(filepath.Dir(cfgPath))

	cases := []string{
		"file://../../secret.key",
		"file://../secret.key",
		"file://" + outsideFile, // absolute path
	}
	for _, raw := range cases {
		_, err := r.Resolve(raw)
		if err == nil {
			t.Errorf("Resolve(%q): expected path traversal error, got nil", raw)
		}
	}
}

// TestResolve_FileRef_withinConfigDir verifies that a legitimate relative file:// ref works.
func TestResolve_FileRef_withinConfigDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "my.key"), []byte("sk-valid\n"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	r := credential.NewResolver(dir)
	got, err := r.Resolve("file://my.key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "sk-valid" {
		t.Fatalf("got %q, want %q", got, "sk-valid")
	}
}
