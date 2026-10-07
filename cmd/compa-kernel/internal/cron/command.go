package cron

import (
	"github.com/spf13/cobra"

	"github.com/xibodev/compa/v3/cmd/compa-kernel/internal"
	"github.com/xibodev/compa/v3/pkg/cron"
)

// exampleJobID looks like a real job ID: 16 hex characters.
const exampleJobID = "3f9a1c2b7d4e8f60"

// openStore opens the cron store; it fails on a store that can't be loaded,
// so a command never writes over a corrupt one.
type openStore func() (*cron.CronService, error)

func NewCronCommand() *cobra.Command {
	var storePath string

	cmd := &cobra.Command{
		Use:     "cron",
		Aliases: []string{"c"},
		Short:   "Manage scheduled tasks",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
		// The gateway's store, shared by all subcommands.
		PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
			storePath = cron.DefaultStorePath(internal.GetHome())
			return nil
		},
	}

	open := func() (*cron.CronService, error) {
		return cron.OpenCronService(storePath, nil)
	}

	cmd.AddCommand(
		newListCommand(open),
		newAddCommand(open),
		newRemoveCommand(open),
		newEnableCommand(open),
		newDisableCommand(open),
	)

	return cmd
}
