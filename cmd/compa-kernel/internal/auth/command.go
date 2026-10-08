package auth

import "github.com/spf13/cobra"

// pausedChannelCommands makes the commands that set up paused channels; the
// files built with the paused_channels tag add them.
var pausedChannelCommands []func() *cobra.Command

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
	for _, newCommand := range pausedChannelCommands {
		cmd.AddCommand(newCommand())
	}

	return cmd
}
