package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/xibodev/compa/v3/pkg/agent"
	"github.com/xibodev/compa/v3/pkg/approval"
	"github.com/xibodev/compa/v3/pkg/bus"
	"github.com/xibodev/compa/v3/pkg/config"
	"github.com/xibodev/compa/v3/pkg/cron"
	runtimeevents "github.com/xibodev/compa/v3/pkg/events"
	"github.com/xibodev/compa/v3/pkg/health"
	"github.com/xibodev/compa/v3/pkg/logger"
	"github.com/xibodev/compa/v3/pkg/state"
	"github.com/xibodev/compa/v3/pkg/tools"
)

func TestRun_StartupFailuresReturnErrorAndEmitStructuredLog(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		prepare    func(t *testing.T, dir string) string
		wantErr    string
		wantLogSub string
	}{
		{
			name: "invalid config returns load error",
			prepare: func(t *testing.T, dir string) string {
				t.Helper()
				cfgPath := filepath.Join(dir, "invalid-config.json")
				if err := os.WriteFile(cfgPath, []byte("{invalid-json"), 0o644); err != nil {
					t.Fatalf("WriteFile(invalid config) error = %v", err)
				}
				return cfgPath
			},
			wantErr:    "error loading config:",
			wantLogSub: "error loading config:",
		},
		{
			name: "invalid config returns pre-check error",
			prepare: func(t *testing.T, dir string) string {
				t.Helper()
				cfg := config.DefaultConfig()
				cfg.Gateway.Port = 0
				cfgPath := filepath.Join(dir, "config.json")
				if err := config.SaveConfig(cfgPath, cfg); err != nil {
					t.Fatalf("SaveConfig() error = %v", err)
				}
				return cfgPath
			},
			wantErr:    "config pre-check failed: invalid gateway port: 0",
			wantLogSub: "config pre-check failed: invalid gateway port: 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			homeDir := t.TempDir()
			configPath := tt.prepare(t, homeDir)

			cmd := exec.Command(os.Args[0], "-test.run=TestGatewayRunStartupFailureHelper")
			cmd.Env = append(os.Environ(),
				"GO_WANT_GATEWAY_RUN_HELPER=1",
				"COMPA_TEST_HOME="+homeDir,
				"COMPA_TEST_CONFIG="+configPath,
			)

			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("helper exited unexpectedly: %v\noutput:\n%s", err, string(output))
			}

			out := string(output)
			if !strings.Contains(out, tt.wantErr) {
				t.Fatalf("helper output missing expected error substring %q:\n%s", tt.wantErr, out)
			}

			logData, readErr := os.ReadFile(filepath.Join(homeDir, logPath, logFile))
			if readErr != nil {
				t.Fatalf("ReadFile(gateway.log) error = %v", readErr)
			}
			logText := string(logData)
			if !strings.Contains(logText, "Gateway startup failed") {
				t.Fatalf("gateway.log missing structured startup failure log:\n%s", logText)
			}
			if !strings.Contains(logText, tt.wantLogSub) {
				t.Fatalf("gateway.log missing expected failure detail %q:\n%s", tt.wantLogSub, logText)
			}
		})
	}
}

func TestGatewayRunStartupFailureHelper(t *testing.T) {
	if os.Getenv("GO_WANT_GATEWAY_RUN_HELPER") != "1" {
		return
	}

	homeDir := os.Getenv("COMPA_TEST_HOME")
	configPath := os.Getenv("COMPA_TEST_CONFIG")

	err := Run(false, homeDir, configPath, false)
	if err == nil {
		fmt.Fprintln(os.Stdout, "expected startup error, got nil")
		os.Exit(2)
	}

	fmt.Fprintln(os.Stdout, err.Error())
	os.Exit(0)
}

func TestCollectGatewayStartupStatusHandlesMalformedInfo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		startupInfo         map[string]any
		wantToolsCount      int
		wantSkillsAvailable int
		wantSkillsTotal     int
		wantLogFields       map[string]any
	}{
		{
			name:          "missing info",
			startupInfo:   map[string]any{},
			wantLogFields: map[string]any{},
		},
		{
			name: "wrong map shapes",
			startupInfo: map[string]any{
				"tools":  "unexpected",
				"skills": []any{"unexpected"},
			},
			wantLogFields: map[string]any{},
		},
		{
			name: "valid startup info",
			startupInfo: map[string]any{
				"tools": map[string]any{
					"count": 3,
				},
				"skills": map[string]any{
					"available": 2,
					"total":     5,
				},
			},
			wantToolsCount:      3,
			wantSkillsAvailable: 2,
			wantSkillsTotal:     5,
			wantLogFields: map[string]any{
				"tools_count":      3,
				"skills_available": 2,
				"skills_total":     5,
			},
		},
		{
			name: "json number startup info",
			startupInfo: map[string]any{
				"tools": map[string]any{
					"count": float64(4),
				},
				"skills": map[string]any{
					"available": float64(1),
					"total":     float64(6),
				},
			},
			wantToolsCount:      4,
			wantSkillsAvailable: 1,
			wantSkillsTotal:     6,
			wantLogFields: map[string]any{
				"tools_count":      4,
				"skills_available": 1,
				"skills_total":     6,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := collectGatewayStartupStatus(tt.startupInfo)
			if got.toolsCount != tt.wantToolsCount {
				t.Fatalf("toolsCount = %d, want %d", got.toolsCount, tt.wantToolsCount)
			}
			if got.skillsAvailable != tt.wantSkillsAvailable {
				t.Fatalf("skillsAvailable = %d, want %d", got.skillsAvailable, tt.wantSkillsAvailable)
			}
			if got.skillsTotal != tt.wantSkillsTotal {
				t.Fatalf("skillsTotal = %d, want %d", got.skillsTotal, tt.wantSkillsTotal)
			}
			if !reflect.DeepEqual(got.logFields, tt.wantLogFields) {
				t.Fatalf("logFields = %#v, want %#v", got.logFields, tt.wantLogFields)
			}
		})
	}
}

func TestPublishGatewayEvent(t *testing.T) {
	eventBus := runtimeevents.NewBus()
	t.Cleanup(func() {
		if err := eventBus.Close(); err != nil {
			t.Fatalf("Close runtime event bus: %v", err)
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sub, eventsCh, err := eventBus.Channel().OfKind(runtimeevents.KindGatewayStart).SubscribeChan(
		ctx,
		runtimeevents.SubscribeOptions{Name: "gateway-test", Buffer: 4},
	)
	if err != nil {
		t.Fatalf("SubscribeChan() error = %v", err)
	}
	t.Cleanup(func() {
		if err := sub.Close(); err != nil {
			t.Fatalf("Close subscription: %v", err)
		}
	})

	al := agent.NewAgentLoop(
		config.DefaultConfig(),
		bus.NewMessageBus(),
		nil,
		agent.WithRuntimeEvents(eventBus),
	)
	t.Cleanup(al.Close)

	startedAt := time.Now().Add(-1500 * time.Millisecond)
	publishGatewayEvent(al, runtimeevents.KindGatewayStart, startedAt, nil)

	evt := receiveGatewayRuntimeEvent(t, eventsCh)
	if evt.Kind != runtimeevents.KindGatewayStart ||
		evt.Source.Component != "gateway" ||
		evt.Severity != runtimeevents.SeverityInfo {
		t.Fatalf("gateway event = %+v", evt)
	}
	payload, ok := evt.Payload.(gatewayEventPayload)
	if !ok {
		t.Fatalf("payload type = %T, want gatewayEventPayload", evt.Payload)
	}
	if payload.DurationMS <= 0 {
		t.Fatalf("DurationMS = %d, want positive", payload.DurationMS)
	}
	if evt.Attrs["duration_ms"] == nil {
		t.Fatalf("gateway event attrs missing duration_ms: %#v", evt.Attrs)
	}
}

func TestShutdownGatewayClosesMessageBus(t *testing.T) {
	msgBus := bus.NewMessageBus()
	al := agent.NewAgentLoop(
		config.DefaultConfig(),
		msgBus,
		nil,
	)
	msgBus.SetEventPublisher(al.RuntimeEventBus())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub, eventsCh, err := al.RuntimeEventBus().Channel().OfKind(runtimeevents.KindBusCloseCompleted).SubscribeChan(
		ctx,
		runtimeevents.SubscribeOptions{Name: "bus-close-test", Buffer: 4},
	)
	if err != nil {
		t.Fatalf("SubscribeChan() error = %v", err)
	}
	defer func() {
		_ = sub.Close()
	}()

	shutdownGateway(&services{}, al, msgBus, true)

	evt := receiveGatewayRuntimeEvent(t, eventsCh)
	if evt.Kind != runtimeevents.KindBusCloseCompleted {
		t.Fatalf("shutdown event kind = %q, want %q", evt.Kind, runtimeevents.KindBusCloseCompleted)
	}
	if err := msgBus.PublishVoiceControl(context.Background(), bus.VoiceControl{}); !errors.Is(err, bus.ErrBusClosed) {
		t.Fatalf("PublishVoiceControl after shutdown error = %v, want %v", err, bus.ErrBusClosed)
	}
}

func receiveGatewayRuntimeEvent(t *testing.T, ch <-chan runtimeevents.Event) runtimeevents.Event {
	t.Helper()

	select {
	case evt := <-ch:
		return evt
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for gateway runtime event")
		return runtimeevents.Event{}
	}
}

// newTestHealthMux returns a ready health server with token "tok" and a mux
// serving its endpoints.
func newTestHealthMux() (*health.Server, *http.ServeMux) {
	hs := health.NewServer("127.0.0.1", 0, "tok")
	hs.SetReady(true)
	mux := http.NewServeMux()
	hs.RegisterOnMux(mux)
	return hs, mux
}

func getReady(t *testing.T, mux *http.ServeMux) (int, string) {
	t.Helper()
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ready", nil))
	return w.Code, w.Body.String()
}

func TestRunAgentLoopFailureMarksTheGatewayNotReady(t *testing.T) {
	hs, mux := newTestHealthMux()

	runAgentLoop(context.Background(), func(context.Context) error {
		return errors.New("agent loop has no message bus")
	}, hs)

	code, body := getReady(t, mux)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("GET /ready after the agent loop failed = %d, want %d: %s", code, http.StatusServiceUnavailable, body)
	}
	if !strings.Contains(body, agentLoopCheck) || !strings.Contains(body, "no message bus") {
		t.Fatalf("GET /ready body = %s, want the failed %s check with its error", body, agentLoopCheck)
	}
}

func TestRunAgentLoopStoppingCleanlyKeepsTheGatewayReady(t *testing.T) {
	hs, mux := newTestHealthMux()

	runAgentLoop(context.Background(), func(context.Context) error { return nil }, hs)

	if code, body := getReady(t, mux); code != http.StatusOK {
		t.Fatalf("GET /ready after a clean stop = %d, want %d: %s", code, http.StatusOK, body)
	}
}

func TestShutdownEndpointTakesTheSignalPath(t *testing.T) {
	hs, mux := newTestHealthMux()
	sigChan := make(chan os.Signal, 1)
	hs.SetShutdownFunc(shutdownRequester(sigChan))

	req := httptest.NewRequest(http.MethodPost, "/shutdown", nil)
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("POST /shutdown = %d, want %d", w.Code, http.StatusAccepted)
	}
	select {
	case sig := <-sigChan:
		if sig != syscall.SIGTERM {
			t.Fatalf("shutdown sent %v, want SIGTERM", sig)
		}
	case <-time.After(time.Second):
		t.Fatal("POST /shutdown did not reach the gateway loop")
	}

	// A second request while one is pending doesn't block.
	request := shutdownRequester(sigChan)
	request()
	request()
	if len(sigChan) != 1 {
		t.Fatalf("pending shutdowns = %d, want 1", len(sigChan))
	}
}

// POST /reload waits for a reload in progress, which may have read the
// config before it was saved, then answers with the outcome of its own.
func TestAwaitReloadWaitsForTheReloadInProgress(t *testing.T) {
	busy, reloads := 2, 0
	failed := errors.New("load config: bad")
	trigger := func(done chan error) error {
		if busy > 0 {
			busy--
			return errReloadInProgress
		}
		reloads++
		done <- failed
		return nil
	}
	if err := awaitReload(context.Background(), trigger); !errors.Is(err, failed) {
		t.Fatalf("awaitReload() = %v, want the outcome of its reload", err)
	}
	if busy != 0 || reloads != 1 {
		t.Fatalf("reloads = %d with %d refusals left, want 1 after both", reloads, busy)
	}
}

// POST /reload stops waiting once its caller is gone, and answers another
// refusal at once.
func TestAwaitReloadEndsWithItsCaller(t *testing.T) {
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	inProgress := func(chan error) error { return errReloadInProgress }
	if err := awaitReload(gone, inProgress); !errors.Is(err, context.Canceled) {
		t.Fatalf("awaitReload() behind a reload in progress = %v, want context.Canceled", err)
	}
	unanswered := func(chan error) error { return nil }
	if err := awaitReload(gone, unanswered); !errors.Is(err, context.Canceled) {
		t.Fatalf("awaitReload() of an unfinished reload = %v, want context.Canceled", err)
	}
	queued := errors.New("reload already queued")
	if err := awaitReload(context.Background(), func(chan error) error { return queued }); !errors.Is(err, queued) {
		t.Fatalf("awaitReload() = %v, want the refusal", err)
	}
}

// The config watcher signals that the file changed; the gateway then loads
// the file as it is, not as it was when the change was seen.
func TestConfigWatcherSignalsAChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, stop := setupConfigWatcherPolling(path, false)
	defer stop()

	if err := os.WriteFile(path, []byte(`{"gateway":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	case <-time.After(10 * time.Second):
		t.Fatal("the watcher did not signal the change")
	}
}

func TestHeartbeatHandler(t *testing.T) {
	t.Run("a reply other than HEARTBEAT_OK is delivered", func(t *testing.T) {
		var gotChannel, gotChat string
		handler := heartbeatHandler(func(_ context.Context, _, channel, chatID string) (string, error) {
			gotChannel, gotChat = channel, chatID
			return "Disk is 95% full\n", nil
		})
		result := handler("check", "telegram", "123")
		if gotChannel != "telegram" || gotChat != "123" {
			t.Fatalf("heartbeat ran as %s:%s, want telegram:123", gotChannel, gotChat)
		}
		if result == nil || result.Silent || result.IsError || result.ForUser != "Disk is 95% full" {
			t.Fatalf("result = %+v, want a non-silent reply for the user", result)
		}
	})

	t.Run("HEARTBEAT_OK and empty replies stay silent", func(t *testing.T) {
		for _, reply := range []string{"HEARTBEAT_OK", " HEARTBEAT_OK\n", ""} {
			handler := heartbeatHandler(func(context.Context, string, string, string) (string, error) {
				return reply, nil
			})
			if result := handler("check", "telegram", "123"); result == nil || !result.Silent {
				t.Fatalf("reply %q gave %+v, want a silent result", reply, result)
			}
		}
	})

	t.Run("no known chat skips the turn", func(t *testing.T) {
		for _, target := range [][2]string{{"", ""}, {"telegram", ""}, {"", "123"}} {
			called := false
			handler := heartbeatHandler(func(context.Context, string, string, string) (string, error) {
				called = true
				return "ran", nil
			})
			if result := handler("check", target[0], target[1]); result != nil || called {
				t.Fatalf("target %q ran the heartbeat (result %+v); it must not fall back to another identity",
					target, result)
			}
		}
	})

	t.Run("errors are reported", func(t *testing.T) {
		handler := heartbeatHandler(func(context.Context, string, string, string) (string, error) {
			return "", errors.New("provider down")
		})
		if result := handler("check", "telegram", "123"); result == nil || !result.IsError {
			t.Fatalf("result = %+v, want an error result", result)
		}
	})
}

func TestSetupCronToolKeepsTheStoreOutOfTheWorkspace(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")

	cfg := config.DefaultConfig()
	cfg.Tools.Cron.Enabled = false
	cs, err := setupCronTool(nil, nil, home, workspace, true, 0, cfg)
	if err != nil {
		t.Fatalf("setupCronTool() error = %v", err)
	}
	every := int64(3_600_000)
	if _, err := cs.AddJob("daily", cron.CronSchedule{Kind: "every", EveryMS: &every}, "hi", "cli", "direct"); err != nil {
		t.Fatalf("AddJob() error = %v", err)
	}

	if _, err := os.Stat(cron.DefaultStorePath(home)); err != nil {
		t.Fatalf("store not at %s: %v", cron.DefaultStorePath(home), err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "cron")); !os.IsNotExist(err) {
		t.Fatalf("store written inside the workspace (stat err = %v)", err)
	}
}

// cronApprover approves every call it is asked about, and records them.
type cronApprover struct {
	mu       sync.Mutex
	requests []*agent.ToolApprovalRequest
}

func (a *cronApprover) ApproveTool(_ context.Context, req *agent.ToolApprovalRequest) (agent.ApprovalDecision, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.requests = append(a.requests, req)
	return agent.ApprovalDecision{Approved: true}, nil
}

func TestSetupCronToolDecidesScheduledCommandsThroughTheAgentLoop(t *testing.T) {
	home := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = filepath.Join(home, "workspace")
	cfg.Tools.Exec.Enabled = true
	cfg.Tools.Cron.Enabled = true
	cfg.Tools.Approval = approval.Policy{Rules: []approval.Rule{
		{Tool: "exec", Origin: []approval.Origin{approval.OriginCron}, Action: approval.Ask},
	}}
	msgBus := bus.NewMessageBus()
	t.Cleanup(msgBus.Close)
	al := agent.NewAgentLoop(cfg, msgBus, nil)
	t.Cleanup(al.Close)
	if _, err := setupCronTool(al, msgBus, home, cfg.Agents.Defaults.Workspace, true, 0, cfg); err != nil {
		t.Fatalf("setupCronTool() error = %v", err)
	}
	registered, ok := al.GetRegistry().GetDefaultAgent().Tools.Get("cron")
	if !ok {
		t.Fatal("cron tool not registered")
	}
	cronTool, ok := registered.(*tools.CronTool)
	if !ok {
		t.Fatalf("cron tool is a %T", registered)
	}

	readOutput := func() string {
		t.Helper()
		select {
		case out := <-msgBus.OutboundChan():
			return out.Content
		case <-time.After(5 * time.Second):
			t.Fatal("the job reported nothing")
		}
		return ""
	}
	job := &cron.CronJob{ID: "job1", Name: "disk check"}
	job.Payload.Command = "echo approved-by-hook"

	// No approver, and no chat of the owner's known yet: nobody can be asked.
	if _, err := cronTool.RunJob(context.Background(), job); err == nil {
		t.Fatal("RunJob() error = nil, want the unapproved command refused")
	}
	if out := readOutput(); !strings.Contains(out, "was not run: The owner's approval is needed") {
		t.Fatalf("job output = %q, want the command refused for want of an approval", out)
	}

	// The loop's approvers decide the ask.
	approver := &cronApprover{}
	if err := al.MountHook(agent.NamedHook("approver", approver)); err != nil {
		t.Fatalf("MountHook() error = %v", err)
	}
	if _, err := cronTool.RunJob(context.Background(), job); err != nil {
		t.Fatalf("RunJob() error = %v, want the approved command run", err)
	}
	if out := readOutput(); !strings.Contains(out, "approved-by-hook") {
		t.Fatalf("job output = %q, want the command's output", out)
	}
	approver.mu.Lock()
	defer approver.mu.Unlock()
	if len(approver.requests) != 1 || approver.requests[0].Origin != approval.OriginCron {
		t.Fatalf("approver requests = %+v, want one from origin cron", approver.requests)
	}
}

func TestSharedStateManagerIsTheAgentLoops(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	al := agent.NewAgentLoop(cfg, bus.NewMessageBus(), nil)
	t.Cleanup(al.Close)
	if al.StateManager() == nil {
		t.Fatal("agent loop has no state manager")
	}
	// The heartbeat and the device service read the owner's chat the loop
	// records, from the loop's own manager.
	if got := sharedStateManager(al, t.TempDir()); got != al.StateManager() {
		t.Fatal("sharedStateManager() is not the agent loop's state manager")
	}

	// Without one, the workspace's state file stands in.
	workspace := t.TempDir()
	if err := state.NewManager(workspace).SetOwnerChat("telegram", "42"); err != nil {
		t.Fatal(err)
	}
	if channel, chatID := sharedStateManager(nil, workspace).GetOwnerChat(); channel != "telegram" || chatID != "42" {
		t.Fatalf("owner chat = %s:%s, want telegram:42", channel, chatID)
	}
}

func TestApplyLoggingSettings(t *testing.T) {
	t.Cleanup(func() {
		logger.SetRedaction(true)
		logger.SetRotation(0, 0)
	})
	const line = "GET https://user:secret@example.com/x"

	cfg := config.DefaultConfig()
	cfg.Logging.RedactSecrets = false
	applyLoggingSettings(cfg)
	if got := logger.Redact(line); got != line {
		t.Fatalf("Redact() with redact_secrets off = %q, want the line unchanged", got)
	}

	cfg.Logging.RedactSecrets = true
	applyLoggingSettings(cfg)
	if got := logger.Redact(line); strings.Contains(got, "secret") {
		t.Fatalf("Redact() with redact_secrets on = %q, want the password masked", got)
	}
}
