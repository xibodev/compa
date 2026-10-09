package module_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xibodev/compa/v4/internal/module"
	"github.com/xibodev/compa/v4/pkg/modproto"
)

// The process-tree guarantees, against a real module that starts a helper
// which holds stdout and ignores the polite request to stop.
//
// On Windows the tree used to be killed with taskkill /T after CommandContext
// had already killed the root, so the walk found nothing and the grandchild
// survived (3 of 3). And with no WaitDelay, a grandchild holding stdout
// stretched a 1.5s deadline to its own lifetime.

func describeFake(t *testing.T, r *module.Runner) *modproto.Descriptor {
	t.Helper()
	d, _, err := r.Describe(context.Background())
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	return d
}

// readMarker waits for the fake module to write a file into its workspace.
func readMarker(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			return strings.TrimSpace(string(b))
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the fake module never wrote %s", name)
	return ""
}

func markerPID(t *testing.T, dir, name string) int {
	t.Helper()
	pid, err := strconv.Atoi(readMarker(t, dir, name))
	if err != nil {
		t.Fatalf("%s does not hold a process ID: %v", name, err)
	}
	t.Cleanup(func() { killProcess(pid) })
	return pid
}

// waitGone fails the test when the process is still running after within.
func waitGone(t *testing.T, pid int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("process %d is still running %s after its module was stopped", pid, within)
}

func workspaceRoot(dir string) map[string]modproto.Root {
	return map[string]modproto.Root{"workspace": {Path: dir, Mode: "rw"}}
}

func TestADeadlineStopsTheWholeProcessTree(t *testing.T) {
	r := newRunner(t)
	d := describeFake(t, r)
	workspace := t.TempDir()

	started := time.Now()
	_, err := r.Invoke(context.Background(), d, &modproto.Request{
		Capability: "fake.misbehave.grandchild",
		Input:      json.RawMessage(`{}`),
		Roots:      workspaceRoot(workspace),
		DeadlineMS: 1500,
	})
	elapsed := time.Since(started)

	if err == nil || !strings.Contains(err.Error(), modproto.ErrHostTimeout) {
		t.Fatalf("a hanging module was not stopped on its deadline: %v", err)
	}
	if !strings.Contains(err.Error(), "invocation deadline") {
		t.Errorf("the error does not say which deadline fired: %v", err)
	}
	pid := markerPID(t, workspace, "grandchild.pid")
	waitGone(t, pid, 10*time.Second)

	// The helper holds stdout for two minutes. The invocation must end with
	// the deadline and the grace period, not with the helper.
	if elapsed > 20*time.Second {
		t.Errorf("a 1.5s deadline took %s: the helper holding stdout held the invocation", elapsed)
	}
}

func TestCancellationStopsTheWholeProcessTree(t *testing.T) {
	r := newRunner(t)
	d := describeFake(t, r)
	workspace := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		// Cancel once the helper exists, so it is part of what must be stopped.
		for i := 0; i < 400; i++ {
			if _, err := os.Stat(filepath.Join(workspace, "grandchild.pid")); err == nil {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
		cancel()
	}()

	_, err := r.Invoke(ctx, d, &modproto.Request{
		Capability: "fake.misbehave.grandchild",
		Input:      json.RawMessage(`{}`),
		Roots:      workspaceRoot(workspace),
		DeadlineMS: 60000,
	})
	if err == nil || !strings.Contains(err.Error(), modproto.ErrCancelled) {
		t.Fatalf("a cancelled invocation was not reported as cancelled: %v", err)
	}
	waitGone(t, markerPID(t, workspace, "grandchild.pid"), 10*time.Second)
}

// A module that ignores the request to stop is stopped by force after the
// grace period, rather than running on.
func TestAModuleIgnoringTerminationIsStoppedAfterTheGrace(t *testing.T) {
	r := newRunner(t)
	d := describeFake(t, r)
	workspace := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelled := make(chan time.Time, 1)
	go func() {
		for i := 0; i < 400; i++ {
			if _, err := os.Stat(filepath.Join(workspace, "ignoring-termination")); err == nil {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
		cancelled <- time.Now()
		cancel()
	}()

	_, err := r.Invoke(ctx, d, &modproto.Request{
		Capability: "fake.misbehave.ignore-termination",
		Input:      json.RawMessage(`{}`),
		Roots:      workspaceRoot(workspace),
		DeadlineMS: 60000,
	})
	stoppedAfter := time.Since(<-cancelled)

	if err == nil {
		t.Fatal("a module ignoring termination was not stopped")
	}
	waitGone(t, markerPID(t, workspace, "ignoring-termination"), 10*time.Second)
	if stoppedAfter > 15*time.Second {
		t.Fatalf("stopping took %s after cancellation", stoppedAfter)
	}
	// Off Windows the request to stop is always delivered, so the module had
	// the full grace period before it was killed.
	if runtime.GOOS != "windows" && stoppedAfter < 2*time.Second {
		t.Errorf("the module was killed %s after cancellation; it should have had a grace period", stoppedAfter)
	}
}

// A helper that keeps stdout open after the module answered does not hold the
// invocation: the answer is returned and the host says what it saw.
func TestAHelperHoldingStdoutDoesNotHoldTheAnswer(t *testing.T) {
	r := newRunner(t)
	d := describeFake(t, r)
	workspace := t.TempDir()

	started := time.Now()
	res, err := r.Invoke(context.Background(), d, &modproto.Request{
		Capability: "fake.misbehave.orphan-stdout",
		Input:      json.RawMessage(`{}`),
		Roots:      workspaceRoot(workspace),
		DeadlineMS: 60000,
	})
	elapsed := time.Since(started)
	markerPID(t, workspace, "grandchild.pid") // killed at cleanup

	if err != nil {
		t.Fatalf("a module that answered was reported as failing: %v", err)
	}
	if !res.Envelope.OK {
		t.Fatalf("the module's answer was lost: %+v", res.Envelope)
	}
	if elapsed > 20*time.Second {
		t.Fatalf("the answer took %s: the host waited for the helper holding stdout", elapsed)
	}
	if !strings.Contains(strings.Join(res.Warnings, " "), "kept its output open") {
		t.Errorf("the host did not say a helper kept the output open: %v", res.Warnings)
	}
}

// A module flooding stdout is stopped at the output bound instead of running
// until its deadline.
func TestAStdoutFloodStopsTheModule(t *testing.T) {
	r := newRunner(t)
	d := describeFake(t, r)
	r.MaxOutputBytes = 4096

	started := time.Now()
	res, err := r.Invoke(context.Background(), d, &modproto.Request{
		Capability: "fake.misbehave.flood",
		Input:      json.RawMessage(`{}`),
		DeadlineMS: 120000,
	})
	if err == nil || !strings.Contains(err.Error(), modproto.ErrHostOutputTooLarge) {
		t.Fatalf("a flood was not refused as too large: %v", err)
	}
	if res == nil || !res.Truncated {
		t.Fatal("truncation was not reported")
	}
	if elapsed := time.Since(started); elapsed > 30*time.Second {
		t.Fatalf("a flood ran for %s; it should be stopped at the bound", elapsed)
	}
}

// The caller's deadline is not the module's: the error says which one fired,
// so a module is not blamed for a budget it was never given.
func TestTheCallersDeadlineIsNotBlamedOnTheModule(t *testing.T) {
	r := newRunner(t)
	d := describeFake(t, r)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := r.Invoke(ctx, d, &modproto.Request{
		Capability: "fake.misbehave.hang",
		Input:      json.RawMessage(`{}`),
		DeadlineMS: 60000,
	})
	if err == nil {
		t.Fatal("a hanging module outlived its caller's deadline")
	}
	msg := err.Error()
	if !strings.Contains(msg, "caller's deadline") {
		t.Fatalf("the error does not say the caller's deadline fired: %v", msg)
	}
	if strings.Contains(msg, "module exceeded") {
		t.Fatalf("the module was blamed for the caller's deadline: %v", msg)
	}
}

// A capability the module never declared is refused before the module starts,
// and a flag-like ID never reaches its command line.
func TestAnUndeclaredCapabilityIsRefusedBeforeTheModuleStarts(t *testing.T) {
	r := newRunner(t)
	d := describeFake(t, r)

	for _, capability := range []string{"fake.nope", "--help", "-x"} {
		before := module.ProcessStarts()
		res, err := r.Invoke(context.Background(), d, &modproto.Request{
			Capability: capability,
			Input:      json.RawMessage(`{}`),
			DeadlineMS: 15000,
		})
		if err == nil || !strings.Contains(err.Error(), modproto.ErrUnknownCapability) {
			t.Fatalf("%q: the undeclared capability was not refused: %v", capability, err)
		}
		if res != nil || module.ProcessStarts() != before {
			t.Fatalf("%q: the module was started for a capability it never declared", capability)
		}
	}
}

// A module runs in the working directory the host chose, never beside its
// installed files or the host's.
func TestAModuleRunsInItsScratchDirectory(t *testing.T) {
	r := newRunner(t)
	d := describeFake(t, r)
	r.WorkDir = filepath.Join(t.TempDir(), "state", "fake", ".scratch")

	res, err := r.Invoke(context.Background(), d, &modproto.Request{
		Capability: "fake.where",
		Input:      json.RawMessage(`{}`),
		DeadlineMS: 15000,
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	var where struct {
		CWD string `json:"cwd"`
	}
	if err := json.Unmarshal(res.Envelope.Result, &where); err != nil {
		t.Fatal(err)
	}
	if !sameFile(t, where.CWD, r.WorkDir) {
		t.Fatalf("the module ran in %q, want its scratch directory %q", where.CWD, r.WorkDir)
	}
	if sameFile(t, where.CWD, filepath.Dir(r.Binary)) {
		t.Fatal("the module ran beside its own binary")
	}
}

func sameFile(t *testing.T, a, b string) bool {
	t.Helper()
	ia, errA := os.Stat(a)
	ib, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(ia, ib)
}

// An unchanged module is described once; a changed one, or one after the cache
// was cleared, is described again.
func TestDescribeCachedRunsAnUnchangedModuleOnce(t *testing.T) {
	r := &module.Runner{Binary: copyFakeModule(t), ModuleID: "fake"}
	module.InvalidateDescribeCache()

	if _, _, err := r.DescribeCached(context.Background()); err != nil {
		t.Fatalf("DescribeCached: %v", err)
	}
	before := module.ProcessStarts()
	d, res, err := r.DescribeCached(context.Background())
	if err != nil || d == nil || res == nil || res.Envelope == nil {
		t.Fatalf("cached describe: %v", err)
	}
	if module.ProcessStarts() != before {
		t.Fatal("an unchanged module was described again")
	}
	d.Capabilities = nil
	if again, _, _ := r.DescribeCached(context.Background()); len(again.Capabilities) == 0 {
		t.Fatal("a caller changing its descriptor changed the cached one")
	}

	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(r.Binary, later, later); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.DescribeCached(context.Background()); err != nil {
		t.Fatal(err)
	}
	if module.ProcessStarts() == before {
		t.Fatal("a changed binary was answered from the cache")
	}

	before = module.ProcessStarts()
	module.InvalidateDescribeCache()
	if _, _, err := r.DescribeCached(context.Background()); err != nil {
		t.Fatal(err)
	}
	if module.ProcessStarts() == before {
		t.Fatal("clearing the cache did not describe the module again")
	}
}
