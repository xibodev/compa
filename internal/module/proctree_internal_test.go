package module

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

// A stop asked for before the process is known signals nothing. On Windows a
// console break to process group 0 would reach every process on the console,
// this host included.
func TestInterruptBeforeTheProcessIsKnownSignalsNothing(t *testing.T) {
	if err := newProcessTree().interrupt(); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("interrupt() = %v, want os.ErrProcessDone", err)
	}
}

// On Windows a module starts suspended: one asked to stop before it was known
// ends without running, rather than running until it is killed.
func TestStopBeforeTheProcessIsKnownKeepsItFromRunning(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("only Windows starts a module suspended")
	}
	cmd := exec.Command("cmd.exe", "/c", "ping -n 30 127.0.0.1")
	tree := newProcessTree()
	tree.prepare(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	_ = tree.interrupt()
	if err := tree.started(cmd.Process); err != nil {
		t.Fatalf("started: %v", err)
	}
	waited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(waited)
	}()
	select {
	case <-waited:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("the module ran although it was asked to stop before it was known")
	}
	tree.finish(true)
}
