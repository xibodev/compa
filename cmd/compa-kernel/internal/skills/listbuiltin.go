package skills

import "github.com/spf13/cobra"

func newListBuiltinCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list-builtin",
		Short:   "List available builtin skills",
		Example: `compa-kernel skills list-builtin`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fsys, err := builtinSkillsFS()
			if err != nil {
				return err
			}
			return skillsListBuiltin(cmd.OutOrStdout(), fsys)
		},
	}

	return cmd
}
