package cron

import "github.com/spf13/cobra"

func newRemoveCommand(open openStore) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "remove <job-id>",
		Short:   "Remove a job by ID",
		Args:    cobra.ExactArgs(1),
		Example: `compa-kernel cron remove ` + exampleJobID,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cronRemoveCmd(cmd.OutOrStdout(), open, args[0])
		},
	}

	return cmd
}
