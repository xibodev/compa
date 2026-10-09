package main

import (
	"path/filepath"
	"testing"

	"github.com/xibodev/compa/v4/pkg/config"
	"github.com/xibodev/compa/v4/pkg/logger"
)

func TestApplyLauncherLoggingSettingsFollowsTheConfig(t *testing.T) {
	t.Cleanup(func() {
		logger.SetRedaction(true)
		logger.SetRotation(0, 0)
	})
	const line = "GET https://user:secret@example.com/x"

	cfg := config.DefaultConfig()
	cfg.Logging.RedactSecrets = false
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.SaveConfig(path, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	applyLauncherLoggingSettings(path)
	if got := logger.Redact(line); got != line {
		t.Fatalf("Redact() with redact_secrets off = %q, want the line unchanged", got)
	}

	// A config that doesn't load leaves the setting as it was.
	logger.SetRedaction(true)
	applyLauncherLoggingSettings(filepath.Join(t.TempDir(), "missing", "config.json"))
	if got := logger.Redact(line); got == line {
		t.Fatal("a config that doesn't load turned redaction off")
	}
}
