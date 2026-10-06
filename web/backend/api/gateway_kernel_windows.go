//go:build windows

package api

import (
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	ppid "github.com/xibodev/compa/v2/pkg/pid"
)

// gatewayShutdownRequestTimeout bounds asking the kernel to shut down.
const gatewayShutdownRequestTimeout = 3 * time.Second

// applyKernelProcAttrs has nothing to add on Windows: the window is hidden by
// applyLauncherProcAttrs, and bindKernelLifetime ties the kernel to the
// launcher once it runs.
func applyKernelProcAttrs(*exec.Cmd) {}

// kernelJob is the job object every kernel this launcher starts joins. It is
// never closed: when the launcher exits, crashed or not, Windows closes it and
// KILL_ON_JOB_CLOSE ends the kernels in it. Processes the kernel starts break
// away silently, so a program the agent opened outlives a restart as before.
var kernelJob struct {
	once   sync.Once
	handle windows.Handle
	err    error
}

// bindKernelLifetime puts a started kernel into kernelJob, so it cannot
// outlive the launcher.
func bindKernelLifetime(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	kernelJob.once.Do(func() {
		job, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			kernelJob.err = fmt.Errorf("create job object: %w", err)
			return
		}
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
			BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
				LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_SILENT_BREAKAWAY_OK,
			},
		}
		if _, err := windows.SetInformationJobObject(
			job,
			windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)),
			uint32(unsafe.Sizeof(info)),
		); err != nil {
			_ = windows.CloseHandle(job)
			kernelJob.err = fmt.Errorf("configure job object: %w", err)
			return
		}
		kernelJob.handle = job
	})
	if kernelJob.err != nil {
		return kernelJob.err
	}

	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return fmt.Errorf("open gateway process: %w", err)
	}
	defer windows.CloseHandle(process)
	if err := windows.AssignProcessToJobObject(kernelJob.handle, process); err != nil {
		return fmt.Errorf("assign gateway to job object: %w", err)
	}
	return nil
}

// requestGatewayStop asks the kernel to shut down gracefully through its
// token-authenticated /shutdown endpoint, since Windows has no SIGTERM to
// send another process. It reports whether the kernel agreed; a kernel
// without the endpoint is then killed.
func requestGatewayStop(cmd *exec.Cmd, pidData *ppid.PidFileData) bool {
	if cmd == nil || cmd.Process == nil || pidData == nil ||
		pidData.PID != cmd.Process.Pid || pidData.Token == "" || pidData.Port <= 0 {
		return false
	}
	target := "http://" + net.JoinHostPort(gatewayProbeHost(pidData.Host), strconv.Itoa(pidData.Port)) + "/shutdown"
	request, err := http.NewRequest(http.MethodPost, target, http.NoBody)
	if err != nil {
		return false
	}
	request.Header.Set("Authorization", "Bearer "+pidData.Token)
	client := &http.Client{
		Timeout:       gatewayShutdownRequestTimeout,
		Transport:     &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	response, err := client.Do(request)
	if err != nil {
		return false
	}
	_ = response.Body.Close()
	return response.StatusCode == http.StatusAccepted
}
