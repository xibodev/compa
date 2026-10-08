package auth

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xibodev/compa/v3/pkg/config"
	"github.com/xibodev/compa/v3/pkg/modelservice"
)

// withConnectEnv points the CLI at a temporary home and config, and replaces
// catalog discovery so no test reaches the network.
func withConnectEnv(t *testing.T, models []string) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), ".compa")
	t.Setenv(config.EnvHome, home)
	configPath := filepath.Join(home, "config.json")
	t.Setenv(config.EnvConfig, configPath)
	if err := config.SaveConfig(configPath, config.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	previous := connectDeps
	connectDeps = modelservice.ConnectDeps{
		SyncCatalog: func(context.Context, modelservice.ProviderCatalogSyncInput) ([]modelservice.CatalogModel, error) {
			catalog := make([]modelservice.CatalogModel, 0, len(models))
			for _, id := range models {
				catalog = append(catalog, modelservice.CatalogModel{ID: id})
			}
			return catalog, nil
		},
	}
	t.Cleanup(func() { connectDeps = previous })
	return configPath
}

func TestConnectProviderRecordsInstanceAndGuidesDefault(t *testing.T) {
	configPath := withConnectEnv(t, []string{"model-a", "model-b"})

	login, err := connectProvider("openai", "sk-test")
	if err != nil {
		t.Fatalf("connectProvider() error = %v", err)
	}
	want := loginResult{Status: "ok", Provider: "openai", InstanceID: "openai", ModelCount: 2}
	if login != want {
		t.Fatalf("connectProvider() = %+v, want %+v", login, want)
	}
	var out bytes.Buffer
	printLogin(&out, login)
	if output := out.String(); !strings.Contains(output, `provider instance "openai" (2 models available)`) ||
		!strings.Contains(output, "compa-kernel model openai/<model-id>") {
		t.Fatalf("output = %q", output)
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.ProviderInstances) != 1 || cfg.ProviderInstances[0].ID != "openai" ||
		cfg.ProviderInstances[0].State != config.ProviderInstanceStateEnabled {
		t.Fatalf("instances = %+v", cfg.ProviderInstances)
	}

	if _, err := authLogoutCmd("openai"); err != nil {
		t.Fatalf("authLogoutCmd() error = %v", err)
	}
	cfg, err = config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProviderInstances[0].State != config.ProviderInstanceStateDisabled {
		t.Fatalf("instance after logout = %+v", cfg.ProviderInstances[0])
	}
}

func TestPrintLoginNamesTheDefaultModel(t *testing.T) {
	var out bytes.Buffer
	printLogin(&out, loginResult{Provider: "openai", InstanceID: "openai", ModelCount: 1, DefaultModel: "openai/gpt-x"})
	if got := out.String(); got != "Connected openai as provider instance \"openai\" (1 models available).\nDefault model: openai/gpt-x\n" {
		t.Fatalf("output = %q", got)
	}
}

func TestAuthLoginRejectsUnknownProvider(t *testing.T) {
	if _, err := authLoginCmd("unknown", io.Discard); err == nil || !strings.Contains(err.Error(), "unsupported provider") {
		t.Fatalf("authLoginCmd() error = %v", err)
	}
}
