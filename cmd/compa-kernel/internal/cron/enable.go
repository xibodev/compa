package cron

import "github.com/spf13/cobra"

func newEnableCommand(open openStore) *cobra.Command {
	return &cobra.Command{
		Use:     "enable <job-id>",
		Short:   "Enable a job",
		Args:    cobra.ExactArgs(1),
		Example: `compa-kernel cron enable ` + exampleJobID,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cronSetJobEnabled(cmd.OutOrStdout(), open, args[0], true)
		},
	}
}
