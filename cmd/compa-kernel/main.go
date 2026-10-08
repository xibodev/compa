// Compa - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 Compa contributors

package main

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/xibodev/compa/v3/cmd/compa-kernel/internal"
	"github.com/xibodev/compa/v3/cmd/compa-kernel/internal/agent"
	"github.com/xibodev/compa/v3/cmd/compa-kernel/internal/auth"
	"github.com/xibodev/compa/v3/cmd/compa-kernel/internal/cliui"
	configcmd "github.com/xibodev/compa/v3/cmd/compa-kernel/internal/config"
	"github.com/xibodev/compa/v3/cmd/compa-kernel/internal/cron"
	evolutioncmd "github.com/xibodev/compa/v3/cmd/compa-kernel/internal/evolution"
	"github.com/xibodev/compa/v3/cmd/compa-kernel/internal/gateway"
	"github.com/xibodev/compa/v3/cmd/compa-kernel/internal/jsonout"
	"github.com/xibodev/compa/v3/cmd/compa-kernel/internal/mcp"
	"github.com/xibodev/compa/v3/cmd/compa-kernel/internal/model"
	"github.com/xibodev/compa/v3/cmd/compa-kernel/internal/onboard"
	"github.com/xibodev/compa/v3/cmd/compa-kernel/internal/skills"
	"github.com/xibodev/compa/v3/cmd/compa-kernel/internal/status"
	"github.com/xibodev/compa/v3/cmd/compa-kernel/internal/version"
	"github.com/xibodev/compa/v3/pkg/config"
	"github.com/xibodev/compa/v3/pkg/logger"
	"github.com/xibodev/compa/v3/pkg/updater"
)

var rootNoColor bool

// initTermuxSSL detects Termux environment and sets SSL_CERT_FILE if not already set.
// This fixes X509 certificate errors when running Compa inside Termux or termux-chroot.
func initTermuxSSL() {
	// Only applicable on Linux/Android
	if runtime.GOOS != "linux" && runtime.GOOS != "android" {
		return
	}

	// Skip if already set
	if os.Getenv("SSL_CERT_FILE") != "" {
		return
	}

	// Check for Termux prefix in PATH or HOME
	home := os.Getenv("HOME")
	path := os.Getenv("PATH")

	isTermux := strings.Contains(home, "com.termux") ||
		strings.Contains(path, "com.termux") ||
		strings.Contains(home, "/data/data/com.termux")

	if !isTermux {
		return
	}

	// Check common CA bundle locations in Termux
	caPaths := []string{
		"$PREFIX/etc/tls/cert.pem",
		os.Getenv("PREFIX") + "/etc/tls/cert.pem",
		"/data/data/com.termux/files/usr/etc/tls/cert.pem",
		"/usr/etc/tls/cert.pem",
	}

	for _, caPath := range caPaths {
		expanded := os.ExpandEnv(caPath)
		if _, err := os.Stat(expanded); err == nil {
			os.Setenv("SSL_CERT_FILE", expanded)
			return
		}
	}
}

func syncCliUIColor(root *cobra.Command) {
	no, _ := root.PersistentFlags().GetBool("no-color")
	cliui.Init(no || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb")
}

// earlyColorDisabled matches lipgloss/banner behavior from env and argv before Cobra parses flags.
func earlyColorDisabled() bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return true
	}
	for i := 1; i < len(os.Args); i++ {
		arg := os.Args[i]
		if arg == "--no-color" || arg == "--no-color=true" || arg == "--no-color=1" {
			return true
		}
	}
	return false
}

func NewRootCommand() *cobra.Command {
	short := fmt.Sprintf("%s Compa — agent host with detached modules", internal.Logo)
	long := fmt.Sprintf(`%s Compa is an agent host and web cockpit.

Capabilities come from detached local modules, discovered and invoked as
bounded separate processes rather than linked into this binary.

Version: %s`, internal.Logo, config.FormatVersion())

	cmd := &cobra.Command{
		Use:   "compa-kernel",
		Short: short,
		Long:  long,
		Example: `compa-kernel version
compa-kernel modules
compa-kernel gateway`,
		SilenceErrors: true,
		// Avoid plain UsageString() on stderr/stdout when a command fails; cliui
		// renders matching panels on stderr instead.
		SilenceUsage: true,
		PersistentPreRun: func(c *cobra.Command, _ []string) {
			syncCliUIColor(c.Root())
		},
	}

	cmd.PersistentFlags().BoolVar(&rootNoColor, "no-color", false,
		"Disable colors (boxed layout unchanged)")

	cmd.SetHelpFunc(func(c *cobra.Command, _ []string) {
		syncCliUIColor(c.Root())
		fmt.Fprint(c.OutOrStdout(), cliui.RenderCommandHelp(c))
	})

	cmd.AddCommand(
		configcmd.NewConfigCommand(),
		onboard.NewOnboardCommand(),
		agent.NewAgentCommand(),
		auth.NewAuthCommand(),
		gateway.NewGatewayCommand(),
		status.NewStatusCommand(),
		cron.NewCronCommand(),
		evolutioncmd.NewEvolutionCommand(),
		mcp.NewMCPCommand(),
		skills.NewSkillsCommand(),
		model.NewModelCommand(),
		updater.NewUpdateCommand("compa-kernel"),
		version.NewVersionCommand(),
		NewModulesCommand(),
		NewModuleInvokeCommand(),
		NewModuleAddCommand(),
		NewModuleEnableCommand(),
		NewModuleDisableCommand(),
		NewModuleRemoveCommand(),
	)

	return cmd
}

const (
	colorBlue = "\033[1;38;2;62;93;185m"
	wordmark  = " ██████╗ ██████╗ ███╗   ███╗██████╗  █████╗ \n" +
		"██╔════╝██╔═══██╗████╗ ████║██╔══██╗██╔══██╗\n" +
		"██║     ██║   ██║██╔████╔██║██████╔╝███████║\n" +
		"██║     ██║   ██║██║╚██╔╝██║██╔═══╝ ██╔══██║\n" +
		"╚██████╗╚██████╔╝██║ ╚═╝ ██║██║     ██║  ██║\n" +
		" ╚═════╝ ╚═════╝ ╚═╝     ╚═╝╚═╝     ╚═╝  ╚═╝\n "
	banner      = "\r\n" + colorBlue + wordmark + "\033[0m\r\n"
	plainBanner = "\r\n" + wordmark + "\r\n"
)

// startupBanner is the wordmark printed at startup: colored, or plain when
// colors are off, and only on an interactive terminal. A kernel whose output
// is captured prints none - such as the gateway the launcher runs, whose
// output becomes the dashboard's logs, where box glyphs and color codes are
// noise.
func startupBanner(interactive, noColor bool) string {
	switch {
	case !interactive:
		return ""
	case noColor:
		return plainBanner
	}
	return banner
}

func main() {
	// Initialize Termux SSL certificate detection before anything else
	initTermuxSSL()

	cliui.Init(earlyColorDisabled())

	// With --json, stdout carries only the command's JSON document: no
	// banner, and the logs go to stderr.
	jsonMode := jsonout.InArgs(os.Args[1:])
	if jsonMode {
		logger.SetConsoleOutput(os.Stderr)
	} else {
		fmt.Print(startupBanner(term.IsTerminal(int(os.Stdout.Fd())), earlyColorDisabled()))
	}

	// TZ selects the local time zone. Nothing is printed on success, so
	// commands whose stdout is parsed stay clean; a bad value is reported on
	// stderr and the system zone stays in use.
	if tzEnv := os.Getenv("TZ"); tzEnv != "" {
		loc, err := time.LoadLocation(tzEnv)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Ignoring TZ=%q: %v\n", tzEnv, err)
		} else {
			time.Local = loc //nolint:gosmopolitan // We intentionally set local timezone from TZ env
		}
	}

	cmd := NewRootCommand()
	last, err := cmd.ExecuteC()
	if err != nil {
		reportError(cmd, last, err, jsonMode, os.Stdout, os.Stderr)
		os.Exit(1)
	}
}

// reportError prints why a command failed: as the JSON document
// {"error": message} on stdout when it runs with --json, otherwise as a
// panel on stderr. jsonMode, --json found in the arguments, also counts
// when the command's flags failed to parse, for a command that has the flag.
func reportError(root, last *cobra.Command, err error, jsonMode bool, stdout, stderr io.Writer) {
	if jsonout.Requested(last) || (jsonMode && jsonout.Accepted(last)) {
		_ = jsonout.WriteError(stdout, err)
		return
	}
	syncCliUIColor(root)
	fmt.Fprint(stderr, cliui.FormatCLIError(err.Error(), last))
}
