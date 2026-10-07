package model

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/xibodev/compa/v3/cmd/compa-kernel/internal"
	"github.com/xibodev/compa/v3/pkg/config"
	"github.com/xibodev/compa/v3/pkg/modelservice"
)

func newAutoFreeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "auto-free",
		Short: "Discover and configure working zero-key free model providers",
		RunE: func(cmd *cobra.Command, args []string) error {
			configPath := internal.GetConfigPath()
			fmt.Println("Probing the free providers that need no key...")
			res, err := modelservice.AutoConnectFreeAndSave(cmd.Context(), configPath, nil)
			if err != nil {
				return fmt.Errorf("auto-connect free failed: %w", err)
			}
			fmt.Printf("Catalogs discovered: %d | Inference verified: %d\n", res.CatalogDiscovered, res.Verified)
			for _, outcome := range res.Outcomes {
				fmt.Printf("- %s: %s", outcome.ProviderID, outcome.Status)
				if outcome.ErrorClass != "" {
					fmt.Printf(" (%s)", outcome.ErrorClass)
				}
				if outcome.Error != "" {
					fmt.Printf(" - %s", outcome.Error)
				}
				fmt.Println()
			}
			for _, inst := range res.Instances {
				fmt.Printf("  + %s\n", inst)
			}
			return nil
		},
	}
}

func newPingCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "ping [instance-id]",
		Short: "Probe reachability and latency of provider instances",
		RunE: func(cmd *cobra.Command, args []string) error {
			configPath := internal.GetConfigPath()
			cfg, err := config.LoadConfig(configPath)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			targetID := ""
			if len(args) > 0 {
				targetID = strings.TrimSpace(args[0])
			}

			instances := cfg.ProviderInstances
			if len(instances) == 0 {
				if targetID != "" {
					return fmt.Errorf("provider instance %q not found", targetID)
				}
				fmt.Println("No provider instances configured.")
				return nil
			}

			found := false
			for _, inst := range instances {
				if inst == nil {
					continue
				}
				if targetID != "" && !strings.EqualFold(inst.ID, targetID) {
					continue
				}
				found = true
				secret := ""
				if inst.AuthConnectionRef != "" {
					secret, _ = modelservice.ResolveCredentialReference(inst.AuthConnectionRef)
				}
				res := modelservice.Ping(cmd.Context(), inst, secret, nil)
				if res.OK {
					modelsInfo := ""
					if res.ModelCount > 0 {
						modelsInfo = fmt.Sprintf(" (%d models)", res.ModelCount)
					}
					fmt.Printf("  [OK] %-20s %4dms%s [%s]\n", inst.ID, res.LatencyMS, modelsInfo, res.Status)
				} else {
					errStr := res.Error
					if errStr == "" {
						errStr = res.Status
					}
					fmt.Printf("  [FAIL] %-18s %s\n", inst.ID, errStr)
				}
			}

			if targetID != "" && !found {
				return fmt.Errorf("provider instance %q not found", targetID)
			}
			return nil
		},
	}
}

func newRosterCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "roster",
		Short: "List all curated providers supported by Compa",
		RunE: func(cmd *cobra.Command, args []string) error {
			configPath := internal.GetConfigPath()
			cfg, _ := config.LoadConfig(configPath)
			roster := modelservice.ListRoster(cfg)

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tDISPLAY NAME\tADAPTER\tSTATUS")
			fmt.Fprintln(w, "--\t------------\t-------\t------")
			for _, item := range roster {
				status := "available"
				if item.Configured {
					status = fmt.Sprintf("configured (%d)", item.InstanceCount)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", item.ID, item.DisplayName, item.Adapter, status)
			}
			return w.Flush()
		},
	}
}
