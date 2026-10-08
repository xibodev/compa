package auth

import (
	"github.com/spf13/cobra"

	"github.com/xibodev/compa/v3/cmd/compa-kernel/internal/jsonout"
)

func newLoginCommand() *cobra.Command {
	var provider string

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Store an API key for a provider",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			login, err := authLoginCmd(provider, jsonout.Progress(cmd))
			if err != nil {
				return err
			}
			if jsonout.Requested(cmd) {
				return jsonout.Write(cmd.OutOrStdout(), login)
			}
			printLogin(cmd.OutOrStdout(), login)
			return nil
		},
	}

	cmd.Flags().StringVarP(&provider, "provider", "p", "", "Provider to login with (openai, anthropic)")
	_ = cmd.MarkFlagRequired("provider")
	cmd.Flags().Bool(jsonout.Flag, false, jsonout.Usage)

	return cmd
}
