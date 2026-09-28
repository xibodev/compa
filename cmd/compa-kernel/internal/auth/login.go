package auth

import "github.com/spf13/cobra"

func newLoginCommand() *cobra.Command {
	var provider string

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Store an API key for a provider",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return authLoginCmd(provider)
		},
	}

	cmd.Flags().StringVarP(&provider, "provider", "p", "", "Provider to login with (openai, anthropic)")
	_ = cmd.MarkFlagRequired("provider")

	return cmd
}
