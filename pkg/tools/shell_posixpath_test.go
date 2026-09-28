package tools

import (
	"path/filepath"
	"runtime"
	"testing"
)

// The bug this exists for: filepath.IsAbs("/etc/passwd") is FALSE on Windows,
// because Windows absolute paths carry a drive. The guard therefore joined
// every POSIX-absolute path UNDER the workspace and judged it inside -- while
// PowerShell resolves "/tmp" against the current drive root, i.e. C:\tmp,
// outside the workspace entirely.
//
// TestWindows_SymlinkBypassPrevented demonstrated this rather than theorised
// it: the test run printed a real directory listing of C:\tmp.
func TestPOSIXAbsolutePathsAreNotJudgedInsideTheWorkspace(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("filepath.IsAbs already handles these on POSIX")
	}

	ws := t.TempDir()
	tool := &ExecTool{workingDir: ws, restrictToWorkspace: true}

	for _, cmd := range []string{
		`ls /tmp`,
		`cat /etc/passwd`,
		`cc -I/etc main.c`,
		`cat --file=/etc/passwd`,
	} {
		if guard := tool.guardCommand(cmd, ws); guard == "" {
			t.Errorf("the guard allowed %q; PowerShell resolves that path"+
				" against the drive root, outside the workspace", cmd)
		}
	}
}

// A path on another drive cannot be made relative to the workspace, and
// filepath.Rel returns an error saying so. The guard treated that error as
// "continue", which is ALLOW -- so any other-drive path was unguarded.
func TestACrossDrivePathIsRefusedRatherThanSkipped(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("a single-root filesystem has no cross-drive case")
	}

	ws := t.TempDir()
	other := filepath.Join("Z:"+string(filepath.Separator), "secrets", "keys.txt")
	if _, err := filepath.Rel(ws, other); err == nil {
		t.Skip("this workspace and Z: are comparable; the case does not arise")
	}

	tool := &ExecTool{workingDir: ws, restrictToWorkspace: true}

	if guard := tool.guardCommand("cat "+other, ws); guard == "" {
		t.Fatalf("a path on another drive was allowed: %q", other)
	}
}

// /dev/null must stay usable: the guard has an explicit allowlist for kernel
// pseudo-devices, and it was consulted AFTER joining the path to the workspace,
// so the literal key was never matched and the allowlist was dead on Windows.
func TestDevNullIsStillAllowed(t *testing.T) {
	ws := t.TempDir()
	tool := &ExecTool{workingDir: ws, restrictToWorkspace: true}

	if guard := tool.guardCommand("echo hi 2>/dev/null", ws); guard != "" {
		t.Fatalf("a redirect to /dev/null was blocked: %s", guard)
	}
}

// An in-workspace absolute path must run. The guard's own regex stops at the
// second backslash, so "C:\Users\me\ws\f.go" matched only "C:\Users" -- a
// prefix that is by definition above the workspace, so a legitimate file was
// blocked and the real path never validated at all.
func TestAnAbsoluteInWorkspacePathIsAllowed(t *testing.T) {
	ws := t.TempDir()
	tool := &ExecTool{workingDir: ws, restrictToWorkspace: true}

	cmd := "find " + ws + " -name '*.go'"
	if guard := tool.guardCommand(cmd, ws); guard != "" {
		t.Fatalf("the guard blocked a path inside the workspace: %s\n  %s", guard, cmd)
	}
}
