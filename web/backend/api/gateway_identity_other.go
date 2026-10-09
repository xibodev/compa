//go:build !windows

package api

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"time"

	ppid "github.com/xibodev/compa/v4/pkg/pid"
)

// gatewayInspectTimeout bounds the ps call on systems without /proc.
const gatewayInspectTimeout = 2 * time.Second

// inspectGatewayProcess tells from a process's command line whether it is
// "compa-kernel gateway": read from /proc where there is one, else from ps.
func inspectGatewayProcess(pid int) (bool, bool) {
	args, err := ppid.ProcessCommandLine(pid)
	switch {
	case err == nil:
		if len(args) == 0 {
			// A zombie: it has exited.
			return false, true
		}
		return looksLikeGatewayCommandLine(strings.Join(args, " ")), true
	case errors.Is(err, ppid.ErrProcessNotFound):
		return false, true
	case !errors.Is(err, errors.ErrUnsupported):
		return false, false
	}

	ctx, cancel := context.WithTimeout(context.Background(), gatewayInspectTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return false, false
	}
	cmdline := strings.TrimSpace(string(out))
	if cmdline == "" {
		return false, true
	}
	return looksLikeGatewayCommandLine(cmdline), true
}
