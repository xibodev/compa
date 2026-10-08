package auth

import "github.com/spf13/cobra"

func NewAuthCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage authentication (login, logout, status)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	cmd.AddCommand(
		newLoginCommand(),
		newLogoutCommand(),
		newStatusCommand(),
	)
	// auth weixin and auth wecom set up paused channels, in builds made with
	// the paused_channels build tag.
	cmd.AddCommand(pausedChannelCommands()...)

	return cmd
}
