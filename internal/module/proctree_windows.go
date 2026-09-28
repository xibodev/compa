//go:build windows

package module

import (
	"context"
	"os/exec"
	"strconv"
	"syscall"
)

// configureProcessGroup puts the child in its own process group so the whole
// tree can be signalled, and hides the console window that would otherwise
// flash on every invocation.
//
// The donor spawned modules without this and flashed a console window on every
// dashboard page load.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}

// waitWithTreeKill waits for the module, killing its ENTIRE process tree if the
// deadline expires.
//
// exec.CommandContext kills only the direct child. A creative module spawns
// ffmpeg and provider CLIs, so killing just the child orphans those
// grandchildren -- they keep running, keep holding files, and on a paid
// provider may keep spending. The donor had exactly this defect while
// elsewhere using taskkill /T for the same reason, so the fix is known-good on
// this platform.
func waitWithTreeKill(ctx context.Context, cmd *exec.Cmd) error {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		if cmd.Process != nil {
			// /T kills the tree, /F forces it. Failure is tolerated: the
			// process may have exited between the deadline and this call.
			_ = exec.Command("taskkill", "/T", "/F", "/PID",
				strconv.Itoa(cmd.Process.Pid)).Run()
		}
		// Wait for the reaped child so ProcessState is populated for the
		// caller's exit-code reporting.
		return <-done
	}
}
