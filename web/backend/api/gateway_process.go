package api

import (
	"fmt"
	"os/exec"
	"sync"
	"time"

	"github.com/xibodev/compa/v2/pkg/logger"
	ppid "github.com/xibodev/compa/v2/pkg/pid"
)

// gatewayLifecycleMu serializes starting, stopping and restarting the kernel,
// waits included, so two requests (the dashboard, the tray, a config reset, a
// channel binding, a crash restart) can never both start one, and a start
// never drops a kernel another just started. gateway.mu only guards the state
// fields and is never held while a process is probed or awaited.
var gatewayLifecycleMu sync.Mutex

// processExits holds, for each kernel this launcher started, a channel its
// waiter closes once cmd.Wait returned. Liveness reads it rather than
// cmd.ProcessState, which Wait writes concurrently.
var processExits sync.Map // *exec.Cmd -> chan struct{}

var (
	// gatewayMaxCrashRestarts kernel crashes within gatewayCrashWindow are
	// restarted; the next one leaves the kernel stopped with status error.
	gatewayMaxCrashRestarts = 5
	gatewayCrashWindow      = 10 * time.Minute
	// gatewayCrashBackoff is the wait before the first restart; each further
	// one waits twice as long, up to gatewayCrashBackoffMax.
	gatewayCrashBackoff    = time.Second
	gatewayCrashBackoffMax = 30 * time.Second
)

// gatewayIdentityTTL bounds how long a status check trusts an earlier answer
// about which program a PID runs; a changed start time ends it sooner.
const gatewayIdentityTTL = 30 * time.Second

type gatewayIdentity struct {
	gateway     bool
	fingerprint string
	checked     time.Time
}

var gatewayIdentities = struct {
	sync.Mutex
	byPID map[int]gatewayIdentity
}{byPID: make(map[int]gatewayIdentity)}

// cachedGatewayProcessMatch answers gatewayProcessMatcher from a recent
// conclusive answer about the same process, so status polls and proxied
// requests do not inspect the kernel every time. Stop and restart, which act
// on the answer, ask gatewayProcessMatcher directly.
func cachedGatewayProcessMatch(pid int) (bool, bool) {
	if pid <= 0 {
		return false, true
	}
	fingerprint := ppid.ProcessStartFingerprint(pid)
	if fingerprint == "" {
		// Gone, or its start time is unreadable: nothing would tell a cached
		// answer from a reused PID, so the matcher (which sees a dead process
		// as no gateway) decides.
		return gatewayProcessMatcher(pid)
	}
	now := time.Now()

	gatewayIdentities.Lock()
	entry, ok := gatewayIdentities.byPID[pid]
	gatewayIdentities.Unlock()
	if ok && entry.fingerprint == fingerprint && now.Sub(entry.checked) < gatewayIdentityTTL {
		return entry.gateway, true
	}

	isGateway, inspected := gatewayProcessMatcher(pid)
	if inspected {
		gatewayIdentities.Lock()
		if len(gatewayIdentities.byPID) >= 64 {
			clear(gatewayIdentities.byPID)
		}
		gatewayIdentities.byPID[pid] = gatewayIdentity{gateway: isGateway, fingerprint: fingerprint, checked: now}
		gatewayIdentities.Unlock()
	}
	return isGateway, inspected
}

// terminateGatewayProcess stops a kernel and waits for it to exit: first it
// asks for a graceful shutdown (requestGatewayStop) and allows
// gatewayRestartGracePeriod, then it kills the process. A process that
// survives both is reported.
func terminateGatewayProcess(cmd *exec.Cmd, pidData *ppid.PidFileData) error {
	if !isCmdProcessAliveLocked(cmd) {
		return nil
	}
	if requestGatewayStop(cmd, pidData) && waitForGatewayProcessExit(cmd, gatewayRestartGracePeriod) {
		return nil
	}
	if err := cmd.Process.Kill(); err != nil && isCmdProcessAliveLocked(cmd) {
		return fmt.Errorf("failed to force-stop existing gateway: %w", err)
	}
	if waitForGatewayProcessExit(cmd, gatewayRestartForceKillWindow) {
		return nil
	}
	return fmt.Errorf("existing gateway did not exit")
}

// handleGatewayExit runs once a kernel this launcher started has exited and
// its output was read. A kernel that failed while nobody was stopping it is
// restarted (see scheduleGatewayCrashRestart).
func (h *Handler) handleGatewayExit(cmd *exec.Cmd, waitErr error) {
	if waitErr != nil {
		logger.ErrorC("gateway", fmt.Sprintf("Gateway process exited: %v", waitErr))
	} else {
		logger.InfoC("gateway", "Gateway process exited normally")
	}

	gateway.mu.Lock()
	crashed := false
	if gateway.cmd == cmd {
		crashed = waitErr != nil && gateway.stopping != cmd && !gateway.shutdown
		clearTrackedGatewayLocked()
		if gateway.runtimeStatus != "restarting" {
			setGatewayRuntimeStatusLocked("stopped")
		}
	}
	gateway.mu.Unlock()

	if crashed {
		h.scheduleGatewayCrashRestart()
	}
}

// scheduleGatewayCrashRestart restarts a crashed kernel after a backoff that
// doubles with each crash. After gatewayMaxCrashRestarts crashes within
// gatewayCrashWindow the kernel stays stopped and the status reads error
// until someone starts it.
func (h *Handler) scheduleGatewayCrashRestart() {
	gateway.mu.Lock()
	now := time.Now()
	recent := gateway.crashRestarts[:0]
	for _, at := range gateway.crashRestarts {
		if now.Sub(at) < gatewayCrashWindow {
			recent = append(recent, at)
		}
	}
	gateway.crashRestarts = recent
	if len(recent) >= gatewayMaxCrashRestarts {
		setGatewayRuntimeStatusLocked("error")
		gateway.mu.Unlock()
		logger.ErrorC("gateway", fmt.Sprintf(
			"Gateway crashed %d times within %s; it stays stopped until it is started again",
			len(recent)+1, gatewayCrashWindow))
		return
	}
	delay := gatewayCrashBackoff << len(recent)
	if delay > gatewayCrashBackoffMax || delay <= 0 {
		delay = gatewayCrashBackoffMax
	}
	gateway.crashRestarts = append(recent, now)
	setGatewayRuntimeStatusLocked("restarting")
	gateway.startupDeadline = now.Add(delay + gatewayStartupWindow)
	gateway.mu.Unlock()

	logger.WarnC("gateway", fmt.Sprintf("Gateway crashed; restarting in %s", delay))
	time.AfterFunc(delay, func() {
		gatewayLifecycleMu.Lock()
		defer gatewayLifecycleMu.Unlock()

		gateway.mu.Lock()
		// A stop, start or restart since the crash decided already.
		superseded := gateway.shutdown || gateway.cmd != nil || gateway.runtimeStatus != "restarting"
		gateway.mu.Unlock()
		if superseded {
			return
		}
		pid, _, err := h.ensureGatewayRunningLocked("restarting")
		if err != nil {
			gateway.mu.Lock()
			setGatewayRuntimeStatusLocked("error")
			gateway.mu.Unlock()
			logger.ErrorC("gateway", fmt.Sprintf("Failed to restart the gateway after a crash: %v", err))
			return
		}
		logger.InfoC("gateway", fmt.Sprintf("Gateway restarted after a crash (PID: %d)", pid))
	})
}
