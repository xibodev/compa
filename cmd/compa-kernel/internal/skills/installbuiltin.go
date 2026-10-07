package skills

import "github.com/spf13/cobra"

func newInstallBuiltinCommand(workspaceFn func() (string, error)) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "install-builtin",
		Short:   "Install all builtin skills to workspace",
		Example: `compa-kernel skills install-builtin`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			workspace, err := workspaceFn()
			if err != nil {
				return err
			}
			fsys, err := builtinSkillsFS()
			if err != nil {
				return err
			}
			return installBuiltinSkills(cmd.OutOrStdout(), fsys, workspace)
		},
	}

	return cmd
}
