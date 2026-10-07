package onboard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCopyEmbeddedToTargetWritesAgentDefinitionFiles(t *testing.T) {
	targetDir := t.TempDir()

	if _, err := copyEmbeddedToTarget(targetDir, false); err != nil {
		t.Fatalf("copyEmbeddedToTarget() error = %v", err)
	}

	agentPath := filepath.Join(targetDir, "AGENT.md")
	if _, err := os.Stat(agentPath); err != nil {
		t.Fatalf("expected %s to exist: %v", agentPath, err)
	}

	soulPath := filepath.Join(targetDir, "SOUL.md")
	if _, err := os.Stat(soulPath); err != nil {
		t.Fatalf("expected %s to exist: %v", soulPath, err)
	}

	userPath := filepath.Join(targetDir, "USER.md")
	if _, err := os.Stat(userPath); err != nil {
		t.Fatalf("expected %s to exist: %v", userPath, err)
	}
}

func TestCopyEmbeddedToTargetKeepsUserFilesUnlessForced(t *testing.T) {
	targetDir := t.TempDir()
	soulPath := filepath.Join(targetDir, "SOUL.md")
	userSoul := []byte("my own soul\n")
	if err := os.WriteFile(soulPath, userSoul, 0o644); err != nil {
		t.Fatal(err)
	}

	kept, err := copyEmbeddedToTarget(targetDir, false)
	if err != nil {
		t.Fatalf("copyEmbeddedToTarget() error = %v", err)
	}
	if len(kept) != 1 || kept[0] != soulPath {
		t.Fatalf("kept = %v, want only %s", kept, soulPath)
	}
	if got, _ := os.ReadFile(soulPath); string(got) != string(userSoul) {
		t.Fatalf("SOUL.md was overwritten without --force: %q", got)
	}
	if _, err := os.Stat(filepath.Join(targetDir, "AGENT.md")); err != nil {
		t.Fatalf("missing defaults were not added: %v", err)
	}

	// Running again with the defaults in place changes nothing.
	if kept, err := copyEmbeddedToTarget(targetDir, false); err != nil || len(kept) != 1 {
		t.Fatalf("second run: kept = %v, err = %v", kept, err)
	}

	kept, err = copyEmbeddedToTarget(targetDir, true)
	if err != nil || len(kept) != 0 {
		t.Fatalf("forced run: kept = %v, err = %v", kept, err)
	}
	if got, _ := os.ReadFile(soulPath); string(got) == string(userSoul) {
		t.Fatal("--force did not replace SOUL.md")
	}
	backups, _ := filepath.Glob(soulPath + ".bak-*")
	if len(backups) != 1 {
		t.Fatalf("backups = %v, want one", backups)
	}
	if got, _ := os.ReadFile(backups[0]); string(got) != string(userSoul) {
		t.Fatalf("backup content = %q", got)
	}
	others, _ := filepath.Glob(filepath.Join(targetDir, "*.bak-*"))
	for _, other := range others {
		if !strings.HasPrefix(filepath.Base(other), "SOUL.md") {
			t.Fatalf("unchanged file was backed up: %s", other)
		}
	}
}
