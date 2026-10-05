package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFileLinesTool_RegistryValidationSupportsMaxLinesAndRejectsLimit(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "registry_lines.txt")

	err := os.WriteFile(testFile, []byte("line 1\nline 2\nline 3\n"), 0o644)
	if err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	reg := NewToolRegistry()
	reg.Register(NewReadFileLinesTool(tmpDir, false, MaxReadFileSize))

	result := reg.Execute(context.Background(), "read_file", map[string]any{
		"path":       testFile,
		"start_line": 1,
		"max_lines":  1,
	})
	if result.IsError {
		t.Fatalf("expected max_lines to pass registry validation, got: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "1|line 1\n") {
		t.Fatalf("expected first line via max_lines, got: %s", result.ForLLM)
	}

	// The schema does not set additionalProperties, so under JSON Schema the
	// registry lets limit through; the tool rejects it itself, naming the
	// argument to use instead.
	result = reg.Execute(context.Background(), "read_file", map[string]any{
		"path":       testFile,
		"start_line": 2,
		"limit":      1,
	})
	if !result.IsError {
		t.Fatalf("expected limit to be rejected, got success: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "limit is not supported in line mode; use max_lines") {
		t.Fatalf("expected the tool to reject limit, got: %s", result.ForLLM)
	}
}
