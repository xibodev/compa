package model

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/xibodev/compa/cmd/compa-kernel/internal"
	"github.com/xibodev/compa/pkg/config"
	"github.com/xibodev/compa/pkg/modelservice"
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
			switch {
			case clearDefault && len(args) > 0:
				return fmt.Errorf("use either a model or --clear, not both")
			case clearDefault:
				return setDefaultModel(configPath, cfg, "")
			case len(args) == 0:
				showCurrentModel(cfg)
				return nil
			}
			return setDefaultModel(configPath, cfg, args[0])
		},
	}
	cmd.Flags().BoolVar(&clearDefault, "clear", false, "Clear the default model")

	cmd.AddCommand(newAutoFreeCommand())
	cmd.AddCommand(newPingCommand())
	cmd.AddCommand(newRosterCommand())

	return cmd
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
	selection := cfg.Agents.Defaults.GetModelName()
	marker := func(value string) string {
		if value == selection {
			return "> "
		}
		return "  "
	}

	if len(cfg.ActiveModels) > 0 {
		fmt.Println("\nChat shortlist:")
		for _, target := range cfg.ActiveModels {
			fmt.Printf("%s%s\n", marker(target), target)
		}
	}
	var routes []*config.ModelRouteConfig
	for _, route := range cfg.ModelRoutes {
		if route != nil {
			routes = append(routes, route)
		}
	}
	if len(routes) > 0 {
		fmt.Println("\nModel routes:")
		for _, route := range routes {
			fmt.Printf("%s%s -> %s\n", marker(route.Name), route.Name, strings.Join(route.Targets, ", "))
		}
	}
	if len(cfg.ActiveModels) == 0 && len(routes) == 0 {
		fmt.Println("\nNo models on the chat shortlist and no model routes.")
		fmt.Println("Connect a provider (compa-kernel auth login, or the Models page) and pick models.")
	}
}

func setDefaultModel(configPath string, cfg *config.Config, selection string) error {
	selection = strings.TrimSpace(selection)
	if selection != "" {
		if err := checkSelection(cfg, selection); err != nil {
			return fmt.Errorf("cannot use %q as the default model: %w", selection, err)
		}
	}

	previous := cfg.Agents.Defaults.GetModelName()
	cfg.Agents.Defaults.ModelName = selection
	if err := config.SaveConfig(configPath, cfg); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	if selection == "" {
		fmt.Printf("✓ Default model cleared (was %s)\n", formatModelName(previous))
		return nil
	}
	fmt.Printf("✓ Default model changed from %s to %s\n", formatModelName(previous), selection)
	return nil
}

func formatModelName(name string) string {
	if name == "" {
		return "(none)"
	}
	return name
}
