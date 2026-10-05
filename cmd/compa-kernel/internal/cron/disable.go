package cron

import "github.com/spf13/cobra"

func newDisableCommand(open openStore) *cobra.Command {
	return &cobra.Command{
		Use:     "disable <job-id>",
		Short:   "Disable a job",
		Args:    cobra.ExactArgs(1),
		Example: `compa-kernel cron disable ` + exampleJobID,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cronSetJobEnabled(cmd.OutOrStdout(), open, args[0], false)
		},
	}
}
