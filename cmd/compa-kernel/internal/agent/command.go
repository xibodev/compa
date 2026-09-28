package agent

import (
	"github.com/spf13/cobra"
)

func NewAgentCommand() *cobra.Command {
	var (
		message    string
		sessionKey string
		model      string
		dir        string
		debug      bool
	)

	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Interact with the agent directly",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return agentCmd(message, sessionKey, model, dir, debug)
		},
	}

	cmd.Flags().BoolVarP(&debug, "debug", "d", false, "Enable debug logging")
	cmd.Flags().StringVarP(&message, "message", "m", "", "Send a single message (non-interactive mode)")
	cmd.Flags().StringVarP(&sessionKey, "session", "s", "cli:default", "Session key")
	cmd.Flags().StringVarP(&model, "model", "", "",
		"Model for this run: an exact target instance-id/model-id or a model route name")
	cmd.Flags().StringVarP(&dir, "dir", "C", "", "Working directory / workspace")
	cmd.Flags().StringVarP(&dir, "workspace", "w", "", "Working directory / workspace")

	return cmd
}
