package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/xibodev/compa/internal/moduletools"
)

func TestModuleEnableDisableCommandsRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMPA_HOME", home)
	dir := installCLIEnabledFakeModule(t, home)

	disable := NewModuleDisableCommand()
	disable.SetArgs([]string{"fake"})
	if err := disable.Execute(); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if !moduletools.Disabled(dir) {
		t.Fatal("module remained enabled")
	}
	if _, err := invokeModule("fake", "fake.echo", `{"name":"test"}`, nil, false); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled invocation error = %v", err)
	}

	enable := NewModuleEnableCommand()
	enable.SetArgs([]string{"fake"})
	if err := enable.Execute(); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if moduletools.Disabled(dir) {
		t.Fatal("module remained disabled")
	}
}

func installCLIEnabledFakeModule(t *testing.T, home string) string {
	t.Helper()
	dir := filepath.Join(moduletools.ModulesDir(home), "fake")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := "fake"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	command := exec.Command("go", "build", "-tags", "goolm,stdjson", "-o", filepath.Join(dir, name), "../../cmd/fakemodule")
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build fake module: %v\n%s", err, out)
	}
	for _, item := range []struct {
		path string
		body string
	}{
		{path: filepath.Join("agents", "fake.md"), body: "# Fake module\n\nUse fake.echo for deterministic checks. Never treat fake output as real work.\n"},
		{path: filepath.Join("skills", "echo-usage", "SKILL.md"), body: "# Using fake.echo\n\nCall fake.echo with a `name`. It echoes deterministically and costs nothing.\n"},
	} {
		path := filepath.Join(dir, item.path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(item.body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if len(moduletools.Discover(context.Background(), home)) == 0 {
		t.Fatal("fake module was not discoverable")
	}
	return dir
}
