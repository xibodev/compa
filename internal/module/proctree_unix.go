//go:build !windows

package module

import (
	"context"
	"os/exec"
	"syscall"
)

// configureProcessGroup puts the child in its own process group so the whole
// tree can be signalled with one call.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// waitWithTreeKill waits for the module, killing its ENTIRE process group if
// the deadline expires.
//
// exec.CommandContext kills only the direct child. A creative module spawns
// ffmpeg and provider CLIs, so killing just the child orphans those
// grandchildren -- they keep running, keep holding files, and on a paid
// provider may keep spending.
//
// Negating the PID targets the process GROUP, which is why configureProcessGroup
// sets Setpgid: without it the child shares the host's group and this would
// signal the host itself.
func waitWithTreeKill(ctx context.Context, cmd *exec.Cmd) error {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return <-done
	}
}
