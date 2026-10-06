package model

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xibodev/compa/v2/pkg/config"
)

func TestRosterCommand(t *testing.T) {
	cmd := newRosterCommand()
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("roster command failed: %v", err)
	}
}

func TestPingCommandRequiresConfig(t *testing.T) {
	cmd := newPingCommand()
	cmd.SetArgs([]string{"non-existent"})
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.json")
	cfg := config.DefaultConfig()
	if err := config.SaveConfig(cfgPath, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	t.Setenv(config.EnvConfig, cfgPath)

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not found error, got %v", err)
	}
}
