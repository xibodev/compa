package onboard

import (
	"github.com/spf13/cobra"

	"github.com/xibodev/compa/v2"
)

var embeddedFiles = compa.OnboardWorkspace

func NewOnboardCommand() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:     "onboard",
		Aliases: []string{"o"},
		Short:   "Initialize Compa configuration and workspace",
		// Run without subcommands: the original onboard flow.
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return cmd.Help()
			}
			return onboard(force)
		},
	}

	cmd.Flags().BoolVar(&force, "force", false,
		"Replace workspace files you changed with the defaults (each is backed up first)")

	return cmd
}
