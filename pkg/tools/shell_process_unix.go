//go:build !windows

package tools

import (
	"os/exec"
	"syscall"

	"github.com/xibodev/compa/v4/pkg/isolation"
)

func prepareCommandForTermination(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateProcessTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}

	pid := cmd.Process.Pid
	if pid <= 0 {
		return nil
	}

	// Kill the entire process group spawned by the shell command.
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	// Fallback kill on the shell process itself.
	_ = cmd.Process.Kill()
	return nil
}

// processTree is a started command together with every process it starts.
// On Unix it is the command's process group.
type processTree struct {
	cmd *exec.Cmd
}

// startProcessTree starts cmd, through isolation, in a process group of its
// own, so that kill reaches the processes the command starts as well.
func startProcessTree(cmd *exec.Cmd) (*processTree, error) {
	prepareCommandForTermination(cmd)
	if err := isolation.Start(cmd); err != nil {
		return nil, err
	}
	return &processTree{cmd: cmd}, nil
}

// kill kills every process of the tree.
func (t *processTree) kill() error {
	return terminateProcessTree(t.cmd)
}

// release lets go of the tree once the command is done. Processes the
// command left running in the background keep running.
func (t *processTree) release() {}
