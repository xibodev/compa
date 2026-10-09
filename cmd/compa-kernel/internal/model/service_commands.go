package model

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/xibodev/compa/v4/cmd/compa-kernel/internal"
	"github.com/xibodev/compa/v4/cmd/compa-kernel/internal/jsonout"
	"github.com/xibodev/compa/v4/pkg/config"
	"github.com/xibodev/compa/v4/pkg/modelservice"
)

// autoConnectFree connects the free providers that need no key; tests
// replace it.
var autoConnectFree = func(cmd *cobra.Command, configPath string) (*modelservice.AutoConnectResult, error) {
	return modelservice.AutoConnectFreeAndSave(cmd.Context(), configPath, nil)
}

// autoFreeResult is what auto-connecting found, with the fields of the
// launcher's POST /api/provider-instances/auto-connect-free answer.
type autoFreeResult struct {
	OK                bool                                    `json:"ok"`
	Total             int                                     `json:"total"`
	CatalogDiscovered int                                     `json:"catalog_discovered"`
	Verified          int                                     `json:"verified"`
	Instances         []string                                `json:"instances"`
	Outcomes          []modelservice.AnonymousProviderOutcome `json:"outcomes"`
	DefaultModel      string                                  `json:"default_model"`
}

func newAutoFreeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "auto-free",
		Short: "Discover and configure working zero-key free model providers",
		RunE: func(cmd *cobra.Command, args []string) error {
			configPath := internal.GetConfigPath()
			asJSON := jsonout.Requested(cmd)
			fmt.Fprintln(jsonout.Progress(cmd), "Probing the free providers that need no key...")
			res, err := autoConnectFree(cmd, configPath)
			if err != nil {
				return fmt.Errorf("auto-connect free failed: %w", err)
			}
			if asJSON {
				return jsonout.Write(cmd.OutOrStdout(), autoFreeResult{
					OK:                res.OK,
					Total:             res.Total,
					CatalogDiscovered: res.CatalogDiscovered,
					Verified:          res.Verified,
					Instances:         append([]string{}, res.Instances...),
					Outcomes:          append([]modelservice.AnonymousProviderOutcome{}, res.Outcomes...),
					DefaultModel:      res.DefaultModel,
				})
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Catalogs discovered: %d | Inference verified: %d\n", res.CatalogDiscovered, res.Verified)
			for _, outcome := range res.Outcomes {
				fmt.Fprintf(out, "- %s: %s", outcome.ProviderID, outcome.Status)
				if outcome.ErrorClass != "" {
					fmt.Fprintf(out, " (%s)", outcome.ErrorClass)
				}
				if outcome.Error != "" {
					fmt.Fprintf(out, " - %s", outcome.Error)
				}
				fmt.Fprintln(out)
			}
			for _, inst := range res.Instances {
				fmt.Fprintf(out, "  + %s\n", inst)
			}
			return nil
		},
	}
}

// pingResults is what `model ping --json` prints: one launcher
// POST /api/provider-instances/{id}/ping answer per instance pinged.
type pingResults struct {
	Results []modelservice.PingResult `json:"results"`
	Total   int                       `json:"total"`
}

// pingInstance probes one provider instance; tests replace it.
var pingInstance = func(ctx context.Context, inst *config.ProviderInstanceConfig, secret string) modelservice.PingResult {
	return modelservice.Ping(ctx, inst, secret, nil)
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

			asJSON := jsonout.Requested(cmd)
			out := cmd.OutOrStdout()
			results := []modelservice.PingResult{}
			for _, inst := range cfg.ProviderInstances {
				if inst == nil || (targetID != "" && !strings.EqualFold(inst.ID, targetID)) {
					continue
				}
				secret := ""
				if inst.AuthConnectionRef != "" {
					secret, _ = modelservice.ResolveCredentialReference(inst.AuthConnectionRef)
				}
				res := pingInstance(cmd.Context(), inst, secret)
				results = append(results, res)
				// Text shows each instance as soon as its ping returns.
				if !asJSON {
					printPing(out, res)
				}
			}
			if targetID != "" && len(results) == 0 {
				return fmt.Errorf("provider instance %q not found", targetID)
			}

			if asJSON {
				return jsonout.Write(out, pingResults{Results: results, Total: len(results)})
			}
			if len(results) == 0 {
				fmt.Fprintln(out, "No provider instances configured.")
			}
			return nil
		},
	}
}

func printPing(out io.Writer, res modelservice.PingResult) {
	if res.OK {
		modelsInfo := ""
		if res.ModelCount > 0 {
			modelsInfo = fmt.Sprintf(" (%d models)", res.ModelCount)
		}
		fmt.Fprintf(out, "  [OK] %-20s %4dms%s [%s]\n", res.InstanceID, res.LatencyMS, modelsInfo, res.Status)
		return
	}
	errStr := res.Error
	if errStr == "" {
		errStr = res.Status
	}
	fmt.Fprintf(out, "  [FAIL] %-18s %s\n", res.InstanceID, errStr)
}

// rosterResult lists the providers, as the launcher's GET /api/provider-roster
// does.
type rosterResult struct {
	Providers []modelservice.ProviderRosterItem `json:"providers"`
	Total     int                               `json:"total"`
}

func newRosterCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "roster",
		Short: "List all curated providers supported by Compa",
		RunE: func(cmd *cobra.Command, args []string) error {
			configPath := internal.GetConfigPath()
			cfg, _ := config.LoadConfig(configPath)
			roster := modelservice.ListRoster(cfg)

			if jsonout.Requested(cmd) {
				return jsonout.Write(cmd.OutOrStdout(), rosterResult{
					Providers: append([]modelservice.ProviderRosterItem{}, roster...),
					Total:     len(roster),
				})
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
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
