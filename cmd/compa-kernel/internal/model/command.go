package model

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/xibodev/compa/v4/cmd/compa-kernel/internal"
	"github.com/xibodev/compa/v4/cmd/compa-kernel/internal/jsonout"
	"github.com/xibodev/compa/v4/pkg/config"
	"github.com/xibodev/compa/v4/pkg/modelservice"
)

// checkSelection validates a selection against the config; tests replace it.
var checkSelection = func(cfg *config.Config, selection string) error {
	return modelservice.NewResolver().Check(cfg, selection)
}

func NewModelCommand() *cobra.Command {
	var clearDefault bool
	cmd := &cobra.Command{
		Use:   "model [target-or-route]",
		Short: "Show or change the default model",
		Long: `Show or change the default model.

Models come from connected provider instances. The default model is either an
exact target, "<instance-id>/<model-id>", or the name of a model route.

Examples:
  compa-kernel model                        # Show the default model and choices
  compa-kernel model openai/gpt-5.4         # Use a model of the "openai" instance
  compa-kernel model everyday               # Use the "everyday" model route
  compa-kernel model --clear                # Clear the default model`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			configPath := internal.GetConfigPath()
			cfg, err := config.LoadConfig(configPath)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}
			asJSON := jsonout.Requested(cmd)
			switch {
			case clearDefault && len(args) > 0:
				return fmt.Errorf("use either a model or --clear, not both")
			case len(args) == 0 && !clearDefault:
				if asJSON {
					return jsonout.Write(cmd.OutOrStdout(), currentModelState(cfg))
				}
				showCurrentModel(cfg)
				return nil
			}
			selection := ""
			if !clearDefault {
				selection = args[0]
			}
			if !asJSON {
				return setDefaultModel(configPath, cfg, selection)
			}
			change, err := saveDefaultModel(configPath, cfg, selection)
			if err != nil {
				return err
			}
			return jsonout.Write(cmd.OutOrStdout(), change)
		},
	}
	cmd.Flags().BoolVar(&clearDefault, "clear", false, "Clear the default model")
	cmd.PersistentFlags().Bool(jsonout.Flag, false, jsonout.Usage)

	cmd.AddCommand(newAutoFreeCommand())
	cmd.AddCommand(newPingCommand())
	cmd.AddCommand(newRosterCommand())

	return cmd
}

// modelState is the default model and the choices for it, with the field
// names of the launcher's /api/default-model, /api/active-models and
// /api/model-routes.
type modelState struct {
	Selection    string                     `json:"selection"`
	ActiveModels []string                   `json:"active_models"`
	Routes       []*config.ModelRouteConfig `json:"routes"`
}

func currentModelState(cfg *config.Config) modelState {
	state := modelState{
		Selection:    cfg.Agents.Defaults.GetModelName(),
		ActiveModels: append([]string{}, cfg.ActiveModels...),
		Routes:       []*config.ModelRouteConfig{},
	}
	for _, route := range cfg.ModelRoutes {
		if route != nil {
			state.Routes = append(state.Routes, route)
		}
	}
	return state
}

func showCurrentModel(cfg *config.Config) {
	selection := cfg.Agents.Defaults.GetModelName()
	if selection == "" {
		fmt.Println("No default model is set.")
	} else {
		fmt.Printf("Default model: %s\n", selection)
	}
	listAvailableModels(cfg)
}

func listAvailableModels(cfg *config.Config) {
	state := currentModelState(cfg)
	marker := func(value string) string {
		if value == state.Selection {
			return "> "
		}
		return "  "
	}

	if len(state.ActiveModels) > 0 {
		fmt.Println("\nChat shortlist:")
		for _, target := range state.ActiveModels {
			fmt.Printf("%s%s\n", marker(target), target)
		}
	}
	if len(state.Routes) > 0 {
		fmt.Println("\nModel routes:")
		for _, route := range state.Routes {
			fmt.Printf("%s%s -> %s\n", marker(route.Name), route.Name, strings.Join(route.Targets, ", "))
		}
	}
	if len(state.ActiveModels) == 0 && len(state.Routes) == 0 {
		fmt.Println("\nNo models on the chat shortlist and no model routes.")
		fmt.Println("Connect a provider (compa-kernel auth login, or the Models page) and pick models.")
	}
}

// defaultModelChange is what setting or clearing the default model did:
// selection is the new default model, "" when cleared.
type defaultModelChange struct {
	Selection string `json:"selection"`
	Previous  string `json:"previous"`
}

// saveDefaultModel makes selection the default model, or clears it when
// selection is empty, and saves the config.
func saveDefaultModel(configPath string, cfg *config.Config, selection string) (defaultModelChange, error) {
	selection = strings.TrimSpace(selection)
	if selection != "" {
		if err := checkSelection(cfg, selection); err != nil {
			return defaultModelChange{}, fmt.Errorf("cannot use %q as the default model: %w", selection, err)
		}
	}

	previous := cfg.Agents.Defaults.GetModelName()
	cfg.Agents.Defaults.ModelName = selection
	if err := config.SaveConfig(configPath, cfg); err != nil {
		return defaultModelChange{}, fmt.Errorf("failed to save config: %w", err)
	}
	return defaultModelChange{Selection: selection, Previous: previous}, nil
}

func setDefaultModel(configPath string, cfg *config.Config, selection string) error {
	change, err := saveDefaultModel(configPath, cfg, selection)
	if err != nil {
		return err
	}
	printDefaultModelChange(os.Stdout, change)
	return nil
}

func printDefaultModelChange(w io.Writer, change defaultModelChange) {
	if change.Selection == "" {
		fmt.Fprintf(w, "✓ Default model cleared (was %s)\n", formatModelName(change.Previous))
		return
	}
	fmt.Fprintf(w, "✓ Default model changed from %s to %s\n", formatModelName(change.Previous), change.Selection)
}

func formatModelName(name string) string {
	if name == "" {
		return "(none)"
	}
	return name
}
