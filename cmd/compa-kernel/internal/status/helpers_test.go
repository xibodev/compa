package status

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xibodev/compa/v3/pkg/config"
)

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	os.Stdout = w

	fn()

	_ = w.Close()
	os.Stdout = oldStdout
	defer r.Close()

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("io.Copy() error = %v", err)
	}
	return buf.String()
}

func TestStatusCmdDescribesProviderInstances(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")
	workspace := filepath.Join(tmpDir, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("os.MkdirAll() error = %v", err)
	}

	t.Setenv(config.EnvConfig, configPath)
	t.Setenv(config.EnvHome, tmpDir)

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Agents.Defaults.ModelName = "openai/gpt-5.4"
	cfg.ProviderInstances = []*config.ProviderInstanceConfig{
		{
			ID: "openai", ProviderKind: "openai", Adapter: config.ProviderAdapterOpenAICompatible, Protocol: "openai",
			Endpoint: "https://api.openai.com/v1", AuthConnectionRef: "credential:openai", State: config.ProviderInstanceStateEnabled,
		},
		{
			ID: "spare", ProviderKind: "groq", Adapter: config.ProviderAdapterOpenAICompatible, Protocol: "groq",
			Endpoint: "https://api.groq.com/openai/v1", State: config.ProviderInstanceStateDisabled,
		},
	}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("config.SaveConfig() error = %v", err)
	}

	output := captureStdout(t, statusCmd)

	for _, want := range []string{"openai/gpt-5.4", "api.openai.com", "credential missing", "0 models", "spare", "disabled"} {
		if !strings.Contains(output, want) {
			t.Fatalf("status output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "sk-") {
		t.Fatalf("status output printed a secret:\n%s", output)
	}
}
