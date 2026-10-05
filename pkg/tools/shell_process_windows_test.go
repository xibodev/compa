//go:build windows

package tools

import (
	"context"
	"regexp"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// windowsProcessRunning reports whether a process with the given ID is still
// running.
func windowsProcessRunning(pid int) bool {
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	event, err := windows.WaitForSingleObject(handle, 0)
	return err == nil && event == uint32(windows.WAIT_TIMEOUT)
}

// TestShellTool_TimeoutKillsDescendants verifies that a timeout ends the
// processes a command started, not only the shell: the descendant here would
// otherwise outlive the PowerShell that started it.
func TestShellTool_TimeoutKillsDescendants(t *testing.T) {
	tool, err := NewExecTool(t.TempDir(), false)
	if err != nil {
		t.Fatalf("unable to configure exec tool: %s", err)
	}
	tool.SetTimeout(4 * time.Second)

	result := tool.Execute(context.Background(), map[string]any{
		"action": "run",
		"command": "$p = Start-Process -FilePath powershell -ArgumentList '-NoProfile','-Command','Start-Sleep -Seconds 120' " +
			"-PassThru -WindowStyle Hidden; Write-Output \"child=$($p.Id)\"; Start-Sleep -Seconds 120",
	})
	if !result.IsError {
		t.Fatalf("expected a timeout, got success: %s", result.ForLLM)
	}

	match := regexp.MustCompile(`child=(\d+)`).FindStringSubmatch(result.ForLLM)
	if match == nil {
		t.Fatalf("no child process id in output: %s", result.ForLLM)
	}
	childPID, _ := strconv.Atoi(match[1])

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !windowsProcessRunning(childPID) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("child process %d is still running after the timeout", childPID)
}

// TestShellTool_NormalExitLeavesBackgroundProcesses verifies that a command
// that finishes normally does not take the processes it detached with it.
func TestShellTool_NormalExitLeavesBackgroundProcesses(t *testing.T) {
	tool, err := NewExecTool(t.TempDir(), false)
	if err != nil {
		t.Fatalf("unable to configure exec tool: %s", err)
	}
	tool.SetTimeout(30 * time.Second)

	result := tool.Execute(context.Background(), map[string]any{
		"action": "run",
		"command": "$p = Start-Process -FilePath powershell -ArgumentList '-NoProfile','-Command','Start-Sleep -Seconds 30' " +
			"-PassThru -WindowStyle Hidden; Write-Output \"child=$($p.Id)\"",
	})
	if result.IsError {
		t.Fatalf("command failed: %s", result.ForLLM)
	}
	match := regexp.MustCompile(`child=(\d+)`).FindStringSubmatch(result.ForLLM)
	if match == nil {
		t.Fatalf("no child process id in output: %s", result.ForLLM)
	}
	childPID, _ := strconv.Atoi(match[1])
	t.Cleanup(func() {
		if handle, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(childPID)); err == nil {
			_ = windows.TerminateProcess(handle, 1)
			_ = windows.CloseHandle(handle)
		}
	})

	time.Sleep(500 * time.Millisecond)
	if !windowsProcessRunning(childPID) {
		t.Fatalf("detached process %d was killed when the command finished", childPID)
	}
}
