package auth

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xibodev/compa/pkg/config"
	"github.com/xibodev/compa/pkg/modelservice"
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

	var err error
	output := captureAuthStdout(t, func() { err = connectProvider("openai", "sk-test") })
	if err != nil {
		t.Fatalf("connectProvider() error = %v", err)
	}
	if !strings.Contains(output, `provider instance "openai" (2 models available)`) ||
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

	if err := authLogoutCmd("openai"); err != nil {
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

func TestAuthLoginRejectsUnknownProvider(t *testing.T) {
	if err := authLoginCmd("unknown"); err == nil || !strings.Contains(err.Error(), "unsupported provider") {
		t.Fatalf("authLoginCmd() error = %v", err)
	}
}
