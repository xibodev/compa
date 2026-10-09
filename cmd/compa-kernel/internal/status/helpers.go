package status

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/xibodev/compa/v4/cmd/compa-kernel/internal"
	"github.com/xibodev/compa/v4/cmd/compa-kernel/internal/cliui"
	"github.com/xibodev/compa/v4/pkg/auth"
	"github.com/xibodev/compa/v4/pkg/config"
	"github.com/xibodev/compa/v4/pkg/modelservice"
)

func statusCmd() {
	cfg, err := internal.LoadConfig()
	if err != nil {
		fmt.Printf("Error loading config: %v\n", err)
		return
	}

	configPath := internal.GetConfigPath()
	build, _ := config.FormatBuildInfo()

	_, configStatErr := os.Stat(configPath)
	configOK := configStatErr == nil

	workspace := cfg.WorkspacePath()
	_, wsErr := os.Stat(workspace)
	wsOK := wsErr == nil

	report := cliui.StatusReport{
		Logo:          internal.Logo,
		Version:       config.FormatVersion(),
		Build:         build,
		ConfigPath:    configPath,
		ConfigOK:      configOK,
		WorkspacePath: workspace,
		WorkspaceOK:   wsOK,
		Model:         cfg.Agents.Defaults.GetModelName(),
	}

	if configOK {
		report.Providers = providerRows(cfg)

		store, _ := auth.LoadStore()
		if store != nil && len(store.Credentials) > 0 {
			for provider, cred := range store.Credentials {
				st := "authenticated"
				if cred.IsExpired() {
					st = "expired"
				} else if cred.NeedsRefresh() {
					st = "needs refresh"
				}
				report.CredentialLines = append(report.CredentialLines,
					fmt.Sprintf("%s (%s): %s", provider, cred.AuthMethod, st))
			}
		}
	}

	cliui.PrintStatus(report)
}

// providerRows describes each provider instance: whether it is enabled, where
// it points, whether its credential is stored, and how many models it serves.
// Secrets are never printed.
func providerRows(cfg *config.Config) []cliui.ProviderRow {
	catalogs, _ := modelservice.LoadInstanceCatalogs(cfg)
	rows := make([]cliui.ProviderRow, 0, len(cfg.ProviderInstances))
	for _, instance := range cfg.ProviderInstances {
		if instance == nil {
			continue
		}
		if instance.State != config.ProviderInstanceStateEnabled {
			rows = append(rows, cliui.ProviderRow{Name: instance.ID, Val: "disabled"})
			continue
		}
		parts := []string{"✓"}
		if host := endpointHost(instance.Endpoint); host != "" {
			parts = append(parts, host)
		}
		if ref := strings.TrimSpace(instance.AuthConnectionRef); ref != "" {
			if _, err := modelservice.ResolveCredentialReference(ref); err != nil {
				parts = append(parts, "credential missing")
			}
		}
		parts = append(parts, fmt.Sprintf("%d models", len(catalogs[instance.ID].Models)))
		rows = append(rows, cliui.ProviderRow{Name: instance.ID, Val: strings.Join(parts, " · ")})
	}
	return rows
}

func endpointHost(endpoint string) string {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return ""
	}
	return parsed.Host
}
