//go:build windows

package tools

import (
	"fmt"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/xibodev/compa/v3/pkg/isolation"
	"github.com/xibodev/compa/v3/pkg/logger"
)

func prepareCommandForTermination(cmd *exec.Cmd) {
	// no-op on Windows
}

func terminateProcessTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}

	pid := cmd.Process.Pid
	if pid <= 0 {
		return nil
	}

	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
	_ = cmd.Process.Kill()
	return nil
}

// processTree is a started command together with every process it starts.
// On Windows it is a Job Object: unlike taskkill /T, which walks parent links
// and loses the descendants of a shell that already exited, terminating the
// job reaches every process in it. The job kills its processes when its last
// handle closes, so a command does not outlive Compa itself.
type processTree struct {
	cmd *exec.Cmd

	mu  sync.Mutex
	job windows.Handle
}

// startProcessTree starts cmd, through isolation, inside a new job. The
// command starts suspended and runs only once it is in the job, so it cannot
// start a process the job would miss. If no job can be set up, the command
// runs anyway and kill falls back to taskkill.
func startProcessTree(cmd *exec.Cmd) (*processTree, error) {
	tree := &processTree{cmd: cmd}

	job, err := newKillOnCloseJob()
	if err != nil {
		logger.DebugCF("shell", "Cannot create a job object; killing the command falls back to taskkill",
			map[string]any{"error": err.Error()})
		if err := isolation.Start(cmd); err != nil {
			return nil, err
		}
		return tree, nil
	}

	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
	if err := isolation.Start(cmd); err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}

	if err := assignProcessToJob(job, cmd.Process.Pid); err != nil {
		_ = windows.CloseHandle(job)
		logger.DebugCF("shell", "Cannot put the command in a job object; killing it falls back to taskkill",
			map[string]any{"error": err.Error()})
	} else {
		tree.job = job
	}

	if err := resumeProcess(cmd.Process.Pid); err != nil {
		// A command that never runs would only end at its timeout.
		_ = tree.kill()
		tree.release()
		_ = cmd.Wait()
		return nil, fmt.Errorf("resume command: %w", err)
	}
	return tree, nil
}

// kill kills every process of the tree.
func (t *processTree) kill() error {
	t.mu.Lock()
	job := t.job
	t.mu.Unlock()
	if job != 0 {
		if err := windows.TerminateJobObject(job, 1); err == nil {
			return nil
		}
	}
	return terminateProcessTree(t.cmd)
}

// release lets go of the job once the command is done. Processes the command
// left running in the background keep running, as they do on Unix: the job
// stops killing on close before its handle is closed.
func (t *processTree) release() {
	t.mu.Lock()
	job := t.job
	t.job = 0
	t.mu.Unlock()
	if job == 0 {
		return
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	_, _ = windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
	_ = windows.CloseHandle(job)
}

// newKillOnCloseJob creates a job that kills its processes when its last
// handle is closed.
func newKillOnCloseJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, fmt.Errorf("create job object: %w", err)
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return 0, fmt.Errorf("set job object limits: %w", err)
	}
	return job, nil
}

func assignProcessToJob(job windows.Handle, pid int) error {
	proc, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return fmt.Errorf("open process %d: %w", pid, err)
	}
	defer windows.CloseHandle(proc)
	if err := windows.AssignProcessToJobObject(job, proc); err != nil {
		return fmt.Errorf("assign process %d to job: %w", pid, err)
	}
	return nil
}

// resumeProcess resumes the threads of a process created suspended.
func resumeProcess(pid int) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fmt.Errorf("snapshot threads: %w", err)
	}
	defer windows.CloseHandle(snapshot)

	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	resumed := 0
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != uint32(pid) {
			continue
		}
		thread, openErr := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if openErr != nil {
			continue
		}
		if _, resumeErr := windows.ResumeThread(thread); resumeErr == nil {
			resumed++
		}
		_ = windows.CloseHandle(thread)
	}
	if resumed == 0 {
		return fmt.Errorf("no thread of process %d could be resumed", pid)
	}
	return nil
}
