package skills

import (
	"github.com/spf13/cobra"

	"github.com/xibodev/compa/v4/pkg/skills"
)

func newShowCommand(loaderFn func() (*skills.SkillsLoader, error)) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "show <name>",
		Short:   "Show skill details",
		Args:    cobra.ExactArgs(1),
		Example: `compa-kernel skills show weather`,
		RunE: func(_ *cobra.Command, args []string) error {
			loader, err := loaderFn()
			if err != nil {
				return err
			}
			return skillsShowCmd(loader, args[0])
		},
	}

	return cmd
}
