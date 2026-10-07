//go:build windows

package module

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// processTree is one module process and everything it starts, held in a job
// object.
//
// taskkill /T used to do this, and it lost the race it was written for:
// CommandContext killed the module first, and a tree whose root is gone cannot
// be walked, so its grandchildren survived (reproduced 3 times of 3). A job
// object holds the whole tree from the first instruction, because the process
// starts suspended and only runs once it is in the job.
//
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE means the tree also dies with the host:
// when the gateway is stopped, even by Process.Kill, Windows closes the job
// handle and ends every process in it.
type processTree struct {
	mu    sync.Mutex
	pid   int
	job   windows.Handle // 0 when the process could not be put in a job
	timer *time.Timer
	done  bool
	// stopped is set by a stop asked for before started knew the process.
	stopped bool
}

func newProcessTree() *processTree { return &processTree{} }

// prepare starts the module suspended, in its own process group so a console
// break reaches it and nothing else, and without the console window the donor
// flashed on every dashboard page load.
func (t *processTree) prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_SUSPENDED,
	}
}

// started puts the suspended process in a kill-on-close job, then lets it run.
// A process asked to stop before this ends without running.
//
// A process that cannot join a job still runs: some hosts run inside a job
// that forbids nesting. Stopping it then falls back to taskkill /T.
func (t *processTree) started(p *os.Process) error {
	t.mu.Lock()
	t.pid = p.Pid
	if job, err := newKillOnCloseJob(); err == nil {
		if err := assignToJob(job, p.Pid); err == nil {
			t.job = job
		} else {
			_ = windows.CloseHandle(job)
		}
	}
	stopped := t.stopped
	t.mu.Unlock()

	if stopped {
		t.kill()
		return nil
	}
	if err := resumeProcess(uint32(p.Pid)); err != nil {
		t.kill()
		return fmt.Errorf("could not start the module after creating it: %w", err)
	}
	return nil
}

// interrupt asks the tree to stop and stops it by force after killGrace.
//
// CTRL_BREAK is the console's graceful stop. It only reaches a process sharing
// this host's console, so a host without one stops the tree at once. Before
// started knows the process there is nothing to signal: a break to process
// group 0 would reach every process on the console, this host included. The
// process is still suspended then, and started ends it instead of resuming it.
func (t *processTree) interrupt() error {
	t.mu.Lock()
	pid, done := t.pid, t.done
	if pid == 0 {
		t.stopped = true
	}
	t.mu.Unlock()
	if done || pid == 0 {
		return os.ErrProcessDone
	}
	if err := windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(pid)); err != nil {
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

// kill ends every process in the tree.
func (t *processTree) kill() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done || t.pid == 0 {
		return
	}
	if t.job != 0 {
		_ = windows.TerminateJobObject(t.job, 1)
		return
	}
	// /T kills the tree, /F forces it. Failure is tolerated: the process may
	// have exited in the meantime.
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(t.pid)).Run()
}

// finish releases the tree once the module has been waited for.
//
// stopTree ends whatever is still running: the module was stopped early, and
// its helpers go with it. After a normal exit the job is closed without
// killing anything, because a capability that returns a job handle leaves its
// worker running on purpose.
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
	if t.job != 0 {
		if !stopTree {
			_ = setJobLimits(t.job, 0)
		}
		_ = windows.CloseHandle(t.job)
		t.job = 0
	}
}

func newKillOnCloseJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	if err := setJobLimits(job, windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE); err != nil {
		_ = windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

func setJobLimits(job windows.Handle, flags uint32) error {
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = flags
	_, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	return err
}

func assignToJob(job windows.Handle, pid int) error {
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	return windows.AssignProcessToJobObject(job, h)
}

// resumeProcess resumes the threads of a process created suspended.
func resumeProcess(pid uint32) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snap)

	var entry windows.ThreadEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	resumed := 0
	for err = windows.Thread32First(snap, &entry); err == nil; err = windows.Thread32Next(snap, &entry) {
		if entry.OwnerProcessID != pid {
			continue
		}
		th, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if err != nil {
			return err
		}
		_, err = windows.ResumeThread(th)
		_ = windows.CloseHandle(th)
		if err != nil {
			return err
		}
		resumed++
	}
	if resumed == 0 {
		return fmt.Errorf("process %d has no thread to resume", pid)
	}
	return nil
}
