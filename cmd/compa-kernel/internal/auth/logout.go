package auth

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/xibodev/compa/v3/cmd/compa-kernel/internal/jsonout"
)

func newLogoutCommand() *cobra.Command {
	var provider string

	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Remove stored credentials",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			loggedOut, err := authLogoutCmd(provider)
			if jsonout.Requested(cmd) {
				if err != nil {
					return err
				}
				return jsonout.Write(cmd.OutOrStdout(), logoutResult{Status: statusOK, Providers: loggedOut})
			}
			for _, name := range loggedOut {
				fmt.Fprintf(cmd.OutOrStdout(), "Logged out from %s\n", name)
			}
			return err
		},
	}

	cmd.Flags().StringVarP(&provider, "provider", "p", "", "Provider to logout from (openai, anthropic); empty = all")
	cmd.Flags().Bool(jsonout.Flag, false, jsonout.Usage)

	return cmd
}
