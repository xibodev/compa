package auth

import (
	"github.com/spf13/cobra"

	"github.com/xibodev/compa/v3/cmd/compa-kernel/internal/jsonout"
)

func newStatusCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show current auth status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			statuses, err := authStatusCmd()
			if err != nil {
				return err
			}
			if jsonout.Requested(cmd) {
				return jsonout.Write(cmd.OutOrStdout(), statusResult{Providers: statuses, Total: len(statuses)})
			}
			printStatus(cmd.OutOrStdout(), statuses)
			return nil
		},
	}
	cmd.Flags().Bool(jsonout.Flag, false, jsonout.Usage)

	return cmd
}
