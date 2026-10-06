package version

import (
	"github.com/spf13/cobra"

	"github.com/xibodev/compa/v2/cmd/compa-kernel/internal"
	"github.com/xibodev/compa/v2/cmd/compa-kernel/internal/cliui"
	"github.com/xibodev/compa/v2/pkg/config"
)

func NewVersionCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "version",
		Aliases: []string{"v"},
		Short:   "Show version information",
		Run: func(_ *cobra.Command, _ []string) {
			printVersion()
		},
	}

	return cmd
}

func printVersion() {
	build, goVer := config.FormatBuildInfo()
	cliui.PrintVersion(internal.Logo, "compa-kernel "+config.FormatVersion(), build, goVer)
}
