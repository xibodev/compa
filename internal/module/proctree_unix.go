//go:build !windows

package module

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// processTree is one module process and everything it starts, held in its own
// process group.
//
// Negating the PID targets the process GROUP, which is why prepare sets
// Setpgid: without it the child shares the host's group and a signal to the
// group would reach the host itself.
type processTree struct {
	mu    sync.Mutex
	pid   int
	timer *time.Timer
	done  bool
}

func newProcessTree() *processTree { return &processTree{} }

// prepare puts the child in its own process group so the whole tree can be
// signalled with one call, and on Linux asks the kernel to kill it if the host
// dies first.
func (t *processTree) prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	setParentDeathSignal(cmd.SysProcAttr)
}

func (t *processTree) started(p *os.Process) error {
	t.mu.Lock()
	t.pid = p.Pid
	t.mu.Unlock()
	return nil
}

// interrupt asks the tree to stop with SIGTERM and stops it by force after
// killGrace.
func (t *processTree) interrupt() error {
	t.mu.Lock()
	pid, done := t.pid, t.done
	t.mu.Unlock()
	if done || pid <= 0 {
		return os.ErrProcessDone
	}
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		t.kill()
		return nil
	}
	t.mu.Lock()
	if !t.done && t.timer == nil {
		t.timer = time.AfterFunc(killGrace, t.kill)
	}
	t.mu.Unlock()
	return nil
}

// kill ends every process in the group.
func (t *processTree) kill() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done || t.pid <= 0 {
		return
	}
	_ = syscall.Kill(-t.pid, syscall.SIGKILL)
}

// finish releases the tree once the module has been waited for. stopTree ends
// whatever is still running in the group; after a normal exit nothing is
// killed, because a capability that returns a job handle leaves its worker
// running on purpose.
func (t *processTree) finish(stopTree bool) {
	if stopTree {
		t.kill()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.done = true
	if t.timer != nil {
		t.timer.Stop()
	}
}
