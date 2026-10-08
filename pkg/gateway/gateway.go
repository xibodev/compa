package gateway

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/xibodev/compa/v3/internal/moduleagent"
	"github.com/xibodev/compa/v3/pkg/agent"
	"github.com/xibodev/compa/v3/pkg/audio/asr"
	"github.com/xibodev/compa/v3/pkg/audio/tts"
	"github.com/xibodev/compa/v3/pkg/bus"
	"github.com/xibodev/compa/v3/pkg/channels"
	_ "github.com/xibodev/compa/v3/pkg/channels/deltachat"
	_ "github.com/xibodev/compa/v3/pkg/channels/dingtalk"
	_ "github.com/xibodev/compa/v3/pkg/channels/discord"
	_ "github.com/xibodev/compa/v3/pkg/channels/feishu"
	_ "github.com/xibodev/compa/v3/pkg/channels/irc"
	_ "github.com/xibodev/compa/v3/pkg/channels/line"
	_ "github.com/xibodev/compa/v3/pkg/channels/maixcam"
	_ "github.com/xibodev/compa/v3/pkg/channels/mqtt"
	_ "github.com/xibodev/compa/v3/pkg/channels/onebot"
	_ "github.com/xibodev/compa/v3/pkg/channels/qq"
	_ "github.com/xibodev/compa/v3/pkg/channels/slack"
	_ "github.com/xibodev/compa/v3/pkg/channels/slack_webhook"
	_ "github.com/xibodev/compa/v3/pkg/channels/teams_webhook"
	_ "github.com/xibodev/compa/v3/pkg/channels/telegram"
	_ "github.com/xibodev/compa/v3/pkg/channels/vk"
	_ "github.com/xibodev/compa/v3/pkg/channels/web"
	_ "github.com/xibodev/compa/v3/pkg/channels/wecom"
	_ "github.com/xibodev/compa/v3/pkg/channels/weixin"
	_ "github.com/xibodev/compa/v3/pkg/channels/whatsapp"
	_ "github.com/xibodev/compa/v3/pkg/channels/whatsapp_native"
	"github.com/xibodev/compa/v3/pkg/config"
	"github.com/xibodev/compa/v3/pkg/cron"
	"github.com/xibodev/compa/v3/pkg/devices"
	runtimeevents "github.com/xibodev/compa/v3/pkg/events"
	"github.com/xibodev/compa/v3/pkg/health"
	"github.com/xibodev/compa/v3/pkg/heartbeat"
	"github.com/xibodev/compa/v3/pkg/logger"
	"github.com/xibodev/compa/v3/pkg/media"
	"github.com/xibodev/compa/v3/pkg/modelservice"
	"github.com/xibodev/compa/v3/pkg/netbind"
	"github.com/xibodev/compa/v3/pkg/pid"
	"github.com/xibodev/compa/v3/pkg/providers"
	"github.com/xibodev/compa/v3/pkg/session/history"
	"github.com/xibodev/compa/v3/pkg/state"
	"github.com/xibodev/compa/v3/pkg/tools"
)

const (
	serviceShutdownTimeout  = 30 * time.Second
	providerReloadTimeout   = 30 * time.Second
	gracefulShutdownTimeout = 15 * time.Second
	// reloadRetryInterval is how often POST /reload checks whether the
	// reload in progress it waits for is over.
	reloadRetryInterval = 100 * time.Millisecond

	logPath   = "logs"
	panicFile = "gateway_panic.log"
	logFile   = "gateway.log"
)

// errReloadInProgress refuses a reload asked for while another one is queued
// or running.
var errReloadInProgress = errors.New("reload already in progress")

// reloadRequest asks the gateway loop for a manual reload. done, when set,
// receives the reload's outcome.
type reloadRequest struct {
	done chan error
}

// finish reports the reload's outcome to whoever waits for it.
func (r reloadRequest) finish(err error) {
	if r.done != nil {
		r.done <- err
	}
}

// awaitReload asks trigger for a reload and returns its outcome once it is
// done. A reload in progress may have read the config before the caller
// saved it, so awaitReload waits for that one to end and asks for its own.
// It gives up once ctx ends.
func awaitReload(ctx context.Context, trigger func(done chan error) error) error {
	done := make(chan error, 1)
	for {
		err := trigger(done)
		if err == nil {
			break
		}
		if !errors.Is(err, errReloadInProgress) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(reloadRetryInterval):
		}
	}
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

type services struct {
	CronService      *cron.CronService
	HeartbeatService *heartbeat.HeartbeatService
	MediaStore       media.MediaStore
	ChannelManager   *channels.Manager
	DeviceService    *devices.Service
	HealthServer     *health.Server
	VoiceAgentCancel context.CancelFunc
	manualReloadChan chan reloadRequest
	reloading        atomic.Bool
	authToken        string
	// homePath is the Compa home, where the cron store lives.
	homePath string
}

// agentLoopCheck is the readiness check that fails once the agent loop
// stopped processing messages.
const agentLoopCheck = "agent_loop"

// heartbeatOK is the heartbeat reply that means nothing needs attention.
const heartbeatOK = "HEARTBEAT_OK"

func logChannelVoiceCapabilities(cm *channels.Manager, asrAvailable bool, ttsAvailable bool) {
	if cm == nil {
		return
	}

	names := cm.GetEnabledChannels()
	sort.Strings(names)
	for _, name := range names {
		ch, ok := cm.GetChannel(name)
		if !ok {
			continue
		}
		caps := channels.DetectVoiceCapabilities(name, ch, asrAvailable, ttsAvailable)
		logger.InfoCF("voice", "Channel voice capabilities", map[string]any{
			"channel": name,
			"asr":     caps.ASR,
			"tts":     caps.TTS,
		})
	}
}

// newModelResolver resolves every model selection the gateway runs on — the
// agents' default, image and light models, /switch model, hook rewrites and
// per-message selections — against the provider connections (provider
// instances and model routes) saved in configPath now, and the catalogs
// saved now. The web UI saves a provider it connects straight to
// config.json, so each selection runs on the connections as they are saved
// the moment it resolves, whichever path it comes from; no selection ever
// sees a stale snapshot while another sees a fresh one. Everything else —
// which selection is the default, tools, channels — is the gateway's
// loaded config and changes on reload.
func newModelResolver(configPath string) agent.ModelResolver {
	resolver := modelservice.NewResolver()
	return func(_ *config.Config, selection string) (*providers.InstanceResolution, error) {
		saved, err := config.LoadConfig(configPath)
		if err != nil {
			return nil, fmt.Errorf("load provider connections: %w", err)
		}
		return resolver.Resolve(saved, selection)
	}
}

// checkDefaultModel checks that cfg's default model selection resolves.
// Without allowEmpty the gateway does not start, or take a reloaded config,
// when it does not. With allowEmpty the gateway runs without a default
// model: turns without a per-message model selection fail with a message
// telling the user to choose one.
func checkDefaultModel(cfg *config.Config, resolve agent.ModelResolver, allowEmpty bool) error {
	selection := strings.TrimSpace(cfg.Agents.Defaults.GetModelName())
	var err error
	if selection == "" {
		err = errors.New("no default model is selected")
	} else if _, resolveErr := resolve(cfg, selection); resolveErr != nil {
		err = fmt.Errorf("default model %q is not available: %w", selection, resolveErr)
	}
	if err == nil {
		return nil
	}
	if !allowEmpty {
		return fmt.Errorf(
			"%w; connect a provider under Models and choose a default model, or start with --allow-empty",
			err,
		)
	}
	logger.WarnCF("gateway", "Gateway running without a default model", map[string]any{
		"limited_mode": true,
		"error":        err.Error(),
	})
	return nil
}

// Run starts the gateway runtime using the configuration loaded from configPath.
func Run(debug bool, homePath, configPath string, allowEmptyStartup bool) (runErr error) {
	startedAt := time.Now()
	panicPath := filepath.Join(homePath, logPath, panicFile)
	panicFunc, err := logger.InitPanic(panicPath)
	if err != nil {
		return fmt.Errorf("error initializing panic log: %w", err)
	}
	defer panicFunc()

	if err = logger.EnableFileLogging(filepath.Join(homePath, logPath, logFile)); err != nil {
		logger.Fatal(fmt.Sprintf("error enabling file logging: %v", err))
	}
	defer logger.DisableFileLogging()

	if debug {
		logger.SetLevel(logger.DEBUG)
	} else {
		logger.SetLevelFromString(config.ResolveGatewayLogLevel(configPath))
	}
	defer func() {
		if runErr != nil {
			logger.ErrorCF("gateway", "Gateway startup failed", map[string]any{
				"config_path": configPath,
				"error":       runErr.Error(),
				"home_path":   homePath,
				"allow_empty": allowEmptyStartup,
				"debug":       debug,
			})
		}
	}()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return fmt.Errorf("error loading config: %w", err)
	}
	applyLoggingSettings(cfg)

	if err = preCheckConfig(cfg); err != nil {
		return fmt.Errorf("config pre-check failed: %w", err)
	}

	// Debug mode permanently overrides the config log level to DEBUG.
	if debug {
		fmt.Println("🔍 Debug mode enabled")
	} else {
		effectiveLogLevel := config.EffectiveGatewayLogLevel(cfg)
		logger.SetLevelFromString(effectiveLogLevel)
		logger.Infof("Log level set to %q", effectiveLogLevel)
	}

	bindPlan, listenResult, err := openGatewayListeners(cfg.Gateway.Host, cfg.Gateway.Port)
	if err != nil {
		return fmt.Errorf("error opening gateway listeners: %w", err)
	}

	// Enforce singleton: write PID file with generated token.
	pidData, err := pid.WritePidFile(homePath, bindPlan.ProbeHost, cfg.Gateway.Port)
	if err != nil {
		logger.Warnf("write pid file failed: %v", err)
		for _, ln := range listenResult.Listeners {
			_ = ln.Close()
		}
		return fmt.Errorf("singleton check failed: %w", err)
	}
	defer pid.RemovePidFile(homePath)
	closeListeners := true
	defer func() {
		if !closeListeners {
			return
		}
		for _, ln := range listenResult.Listeners {
			_ = ln.Close()
		}
	}()

	modelResolver := newModelResolver(configPath)
	if err = checkDefaultModel(cfg, modelResolver, allowEmptyStartup); err != nil {
		return fmt.Errorf("error resolving the default model: %w", err)
	}

	msgBus := bus.NewMessageBus()
	agentLoop := agent.NewAgentLoop(cfg, msgBus, nil,
		agent.WithModelResolver(modelResolver),
		// Compa is the composition root that turns installed modules into
		// agent tools. The KERNEL no longer knows the module host exists --
		// see agent.ToolProvider. A standalone product wires its own native
		// provider here instead, or none at all.
		agent.WithToolProviders(
			moduleagent.NewProvider(config.GetHome()),
		),
	)
	msgBus.SetEventPublisher(agentLoop.RuntimeEventBus())
	publishGatewayEvent(agentLoop, runtimeevents.KindGatewayStart, startedAt, nil)

	fmt.Println("\n📦 Agent Status:")
	startupStatus := collectGatewayStartupStatus(agentLoop.GetStartupInfo())
	fmt.Printf("  • Tools: %d loaded\n", startupStatus.toolsCount)
	fmt.Printf("  • Skills: %d/%d available\n", startupStatus.skillsAvailable, startupStatus.skillsTotal)

	logger.InfoCF("agent", "Agent initialized", startupStatus.logFields)

	runningServices, err := setupAndStartServices(cfg, agentLoop, msgBus, homePath, pidData.Token, listenResult)
	if err != nil {
		return err
	}
	// All services (channels + shared HTTP server) are up; mark the health
	// server ready so GET /ready reports "ready". The health endpoints are
	// mounted on the shared gateway mux, so Health.Server.Start() (which would
	// otherwise set this) is never called — we flip the flag explicitly here.
	runningServices.HealthServer.SetConfigDigest(config.NewRestartSignature(cfg).Digest())
	runningServices.HealthServer.SetReady(true)
	publishGatewayEvent(agentLoop, runtimeevents.KindGatewayReady, startedAt, nil)
	closeListeners = false

	// Setup manual reload channel for /reload endpoint
	manualReloadChan := make(chan reloadRequest, 1)
	runningServices.manualReloadChan = manualReloadChan
	reloadTrigger := func(done chan error) error {
		if !runningServices.reloading.CompareAndSwap(false, true) {
			return errReloadInProgress
		}
		select {
		case manualReloadChan <- reloadRequest{done: done}:
			return nil
		default:
			// Should not happen, but reset flag if channel is full
			runningServices.reloading.Store(false)
			return fmt.Errorf("reload already queued")
		}
	}
	// POST /reload answers once its reload is done, so its caller knows the
	// config it saved is in effect (awaitReload). A reload asked for from a
	// chat command runs on its own: that turn must not wait for the agents it
	// rebuilds.
	runningServices.HealthServer.SetReloadFunc(func(ctx context.Context) error {
		return awaitReload(ctx, reloadTrigger)
	})
	agentLoop.SetReloadFunc(func() error { return reloadTrigger(nil) })

	for _, bindHost := range listenResult.BindHosts {
		fmt.Printf("✓ Gateway started on %s\n", net.JoinHostPort(bindHost, strconv.Itoa(cfg.Gateway.Port)))
	}
	// Only someone at a terminal can press it; Compa's own logs never ask.
	if term.IsTerminal(int(os.Stdout.Fd())) {
		fmt.Println("Press Ctrl+C to stop")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go runAgentLoop(ctx, agentLoop.Run, runningServices.HealthServer)

	var configChanged <-chan struct{}
	stopWatch := func() {}
	if cfg.Gateway.HotReload {
		configChanged, stopWatch = setupConfigWatcherPolling(configPath, debug)
		logger.Info("Config hot reload enabled")
	}
	defer stopWatch()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	// POST /shutdown is how the launcher stops the kernel where it can't
	// send a SIGTERM (Windows); it shuts down the same way.
	runningServices.HealthServer.SetShutdownFunc(shutdownRequester(sigChan))

	for {
		select {
		case <-sigChan:
			logger.Info("Shutting down...")
			shutdownGateway(runningServices, agentLoop, msgBus, true)
			return nil
		case <-configChanged:
			if !runningServices.reloading.CompareAndSwap(false, true) {
				logger.Warn("Config reload skipped: another reload is in progress")
				continue
			}
			// The file as it is now: it may have changed again since the
			// watcher saw it change.
			newCfg, err := config.LoadConfig(configPath)
			if err != nil {
				logger.Errorf("Error loading the changed config, keeping the current one: %v", err)
				runningServices.reloading.Store(false)
				continue
			}
			err = executeReload(ctx, agentLoop, newCfg, modelResolver, runningServices, msgBus, allowEmptyStartup, debug)
			if err != nil {
				logger.Errorf("Config reload failed: %v", err)
			}
		case request := <-manualReloadChan:
			logger.Info("Manual reload triggered via /reload endpoint")
			// LoadConfig validates the config, provider instances and model
			// selections included.
			newCfg, err := config.LoadConfig(configPath)
			if err != nil {
				logger.Errorf("Error loading config for manual reload: %v", err)
				runningServices.reloading.Store(false)
				request.finish(fmt.Errorf("load config: %w", err))
				continue
			}
			err = executeReload(ctx, agentLoop, newCfg, modelResolver, runningServices, msgBus, allowEmptyStartup, debug)
			if err != nil {
				logger.Errorf("Manual reload failed: %v", err)
			} else {
				logger.Info("Manual reload completed successfully")
			}
			request.finish(err)
		}
	}
}

func preCheckConfig(cfg *config.Config) error {
	if cfg.Gateway.Port <= 0 || cfg.Gateway.Port > 65535 {
		return fmt.Errorf("invalid gateway port: %d, port must be between 1 and 65535", cfg.Gateway.Port)
	}
	return nil
}

// applyLoggingSettings applies cfg's logging settings: whether log lines are
// redacted and when the log file rotates.
func applyLoggingSettings(cfg *config.Config) {
	logger.SetRedaction(cfg.Logging.RedactSecrets)
	logger.SetRotation(cfg.Logging.EffectiveMaxSizeMB(), cfg.Logging.EffectiveMaxFiles())
}

// runAgentLoop runs the agent loop until ctx ends. A loop that stops on an
// error leaves the channels and the web UI up while no message is processed,
// so the failure is logged and the gateway's readiness check fails with it.
func runAgentLoop(ctx context.Context, run func(context.Context) error, hs *health.Server) {
	err := run(ctx)
	if err == nil {
		return
	}
	logger.ErrorCF("agent", "Agent loop stopped; messages are not processed until the gateway restarts",
		map[string]any{"error": err.Error()})
	if hs != nil {
		hs.RegisterCheck(agentLoopCheck, func() (bool, string) {
			return false, fmt.Sprintf("agent loop stopped: %v", err)
		})
	}
}

// shutdownRequester returns what POST /shutdown runs: it hands the gateway
// loop a SIGTERM, so the launcher's graceful stop takes the signal's path. A
// shutdown already pending isn't queued twice.
func shutdownRequester(sigChan chan<- os.Signal) func() {
	return func() {
		select {
		case sigChan <- syscall.SIGTERM:
		default:
		}
	}
}

type gatewayStartupStatus struct {
	toolsCount      int
	skillsAvailable int
	skillsTotal     int
	logFields       map[string]any
}

func collectGatewayStartupStatus(startupInfo map[string]any) gatewayStartupStatus {
	status := gatewayStartupStatus{logFields: map[string]any{}}

	if toolsInfo, ok := startupInfo["tools"].(map[string]any); ok {
		if count, ok := startupInfoInt(toolsInfo["count"]); ok {
			status.toolsCount = count
			status.logFields["tools_count"] = count
		}
	}

	if skillsInfo, ok := startupInfo["skills"].(map[string]any); ok {
		if total, ok := startupInfoInt(skillsInfo["total"]); ok {
			status.skillsTotal = total
			status.logFields["skills_total"] = total
		}
		if available, ok := startupInfoInt(skillsInfo["available"]); ok {
			status.skillsAvailable = available
			status.logFields["skills_available"] = available
		}
	}

	return status
}

func startupInfoInt(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int32:
		return int(v), true
	case int64:
		return int(v), true
	case float32:
		return int(v), true
	case float64:
		return int(v), true
	default:
		return 0, false
	}
}

func executeReload(
	ctx context.Context,
	agentLoop *agent.AgentLoop,
	newCfg *config.Config,
	resolve agent.ModelResolver,
	runningServices *services,
	msgBus *bus.MessageBus,
	allowEmptyStartup bool,
	debug bool,
) (err error) {
	startedAt := time.Now()
	publishGatewayEvent(agentLoop, runtimeevents.KindGatewayReloadStarted, startedAt, nil)
	defer runningServices.reloading.Store(false)
	defer func() {
		if err != nil {
			publishGatewayEvent(agentLoop, runtimeevents.KindGatewayReloadFailed, startedAt, err)
			return
		}
		// /ready reports the config applied now, so the launcher sees the
		// gateway runs the saved config however the reload was asked for.
		if runningServices.HealthServer != nil {
			runningServices.HealthServer.SetConfigDigest(config.NewRestartSignature(newCfg).Digest())
		}
		publishGatewayEvent(agentLoop, runtimeevents.KindGatewayReloadCompleted, startedAt, nil)
	}()

	err = handleConfigReload(ctx, agentLoop, newCfg, resolve, runningServices, msgBus, allowEmptyStartup, debug)
	return err
}

func setupAndStartServices(
	cfg *config.Config,
	agentLoop *agent.AgentLoop,
	msgBus *bus.MessageBus,
	homePath string,
	authToken string,
	listenResult netbind.OpenResult,
) (*services, error) {
	runningServices := &services{homePath: homePath}

	execTimeout := time.Duration(cfg.Tools.Cron.ExecTimeoutMinutes) * time.Minute
	var err error
	runningServices.CronService, err = setupCronTool(
		agentLoop,
		msgBus,
		homePath,
		cfg.WorkspacePath(),
		cfg.Agents.Defaults.RestrictToWorkspace,
		execTimeout,
		cfg,
	)
	if err != nil {
		return nil, fmt.Errorf("error setting up cron service: %w", err)
	}
	if err = runningServices.CronService.Start(); err != nil {
		return nil, fmt.Errorf("error starting cron service: %w", err)
	}
	fmt.Println("✓ Cron service started")

	stateManager := sharedStateManager(agentLoop, cfg.WorkspacePath())

	runningServices.HeartbeatService = newHeartbeatService(cfg, agentLoop, msgBus, stateManager)
	if err = runningServices.HeartbeatService.Start(); err != nil {
		return nil, fmt.Errorf("error starting heartbeat service: %w", err)
	}
	if cfg.Heartbeat.Enabled {
		fmt.Println("✓ Heartbeat service started")
	}

	runningServices.MediaStore = media.NewFileMediaStoreWithCleanup(media.MediaCleanerConfig{
		Enabled:  cfg.Tools.MediaCleanup.Enabled,
		MaxAge:   time.Duration(cfg.Tools.MediaCleanup.MaxAge) * time.Minute,
		Interval: time.Duration(cfg.Tools.MediaCleanup.Interval) * time.Minute,
	})
	if fms, ok := runningServices.MediaStore.(*media.FileMediaStore); ok {
		fms.Start()
	}

	runningServices.ChannelManager, err = channels.NewManager(
		cfg,
		msgBus,
		runningServices.MediaStore,
		channels.WithRuntimeEvents(agentLoop.RuntimeEventBus()),
	)
	if err != nil {
		if fms, ok := runningServices.MediaStore.(*media.FileMediaStore); ok {
			fms.Stop()
		}
		return nil, fmt.Errorf("error creating channel manager: %w", err)
	}

	agentLoop.SetChannelManager(runningServices.ChannelManager)
	agentLoop.SetMediaStore(runningServices.MediaStore)
	// The web chat serves its session history from the workspace of the
	// config in force, which a reload may change.
	runningServices.ChannelManager.SetSessionHistory(func() history.Reader {
		cfg := agentLoop.GetConfig()
		return history.Reader{
			Dir:           history.SessionsDir(cfg.Agents.Defaults.Workspace),
			MaxArgsLength: cfg.Agents.Defaults.GetToolFeedbackMaxArgsLength(),
		}
	})

	transcriber := asr.DetectTranscriber(cfg)
	if transcriber != nil {
		agentLoop.SetTranscriber(transcriber)
		logger.InfoCF("voice", "Transcription enabled (agent-level)", map[string]any{"provider": transcriber.Name()})
	}

	ttsAvailable := tts.DetectTTS(cfg) != nil

	enabledChannels := runningServices.ChannelManager.GetEnabledChannels()
	if len(enabledChannels) > 0 {
		fmt.Printf("✓ Channels enabled: %s\n", enabledChannels)
	} else {
		fmt.Println("⚠ Warning: No channels enabled")
	}

	runningServices.authToken = authToken
	runningServices.HealthServer = health.NewServer(listenResult.ProbeHost, cfg.Gateway.Port, authToken)

	var listenAddr string
	if len(listenResult.Listeners) > 0 {
		listenAddr = listenResult.Listeners[0].Addr().String()
	} else {
		listenAddr = net.JoinHostPort(listenResult.ProbeHost, strconv.Itoa(cfg.Gateway.Port))
	}
	runningServices.ChannelManager.SetupHTTPServerListeners(
		listenResult.Listeners,
		listenAddr,
		runningServices.HealthServer,
	)

	if err = runningServices.ChannelManager.StartAll(context.Background()); err != nil {
		return nil, fmt.Errorf("error starting channels: %w", err)
	}

	logChannelVoiceCapabilities(runningServices.ChannelManager, transcriber != nil, ttsAvailable)

	if transcriber != nil {
		// Start Voice Agent Orchestrator after channels are ready.
		vaCtx, vaCancel := context.WithCancel(context.Background())
		runningServices.VoiceAgentCancel = vaCancel
		voiceAgent := asr.NewAgent(msgBus, transcriber)
		voiceAgent.Start(vaCtx)
	}

	healthAddr := net.JoinHostPort(listenResult.ProbeHost, strconv.Itoa(cfg.Gateway.Port))
	fmt.Printf(
		"✓ Health endpoints available at http://%s/health, /ready and /reload (POST)\n",
		healthAddr,
	)

	runningServices.DeviceService = devices.NewService(devices.Config{
		Enabled:    cfg.Devices.Enabled,
		MonitorUSB: cfg.Devices.MonitorUSB,
	}, stateManager)
	runningServices.DeviceService.SetBus(msgBus)
	if err = runningServices.DeviceService.Start(context.Background()); err != nil {
		logger.ErrorCF("device", "Error starting device service", map[string]any{"error": err.Error()})
	} else if cfg.Devices.Enabled {
		fmt.Println("✓ Device event service started")
	}

	return runningServices, nil
}

func stopAndCleanupServices(runningServices *services, shutdownTimeout time.Duration, isReload bool) {
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()

	// reload should not stop channel manager
	if !isReload && runningServices.ChannelManager != nil {
		runningServices.ChannelManager.StopAll(shutdownCtx)
	}
	if runningServices.VoiceAgentCancel != nil {
		runningServices.VoiceAgentCancel()
	}
	if runningServices.DeviceService != nil {
		runningServices.DeviceService.Stop()
	}
	if runningServices.HeartbeatService != nil {
		runningServices.HeartbeatService.Stop()
	}
	if runningServices.CronService != nil {
		runningServices.CronService.Stop()
	}
	if runningServices.MediaStore != nil {
		if fms, ok := runningServices.MediaStore.(*media.FileMediaStore); ok {
			fms.Stop()
		}
	}
}

func shutdownGateway(
	runningServices *services,
	agentLoop *agent.AgentLoop,
	msgBus *bus.MessageBus,
	fullShutdown bool,
) {
	publishGatewayEvent(agentLoop, runtimeevents.KindGatewayShutdown, time.Time{}, nil)

	stopAndCleanupServices(runningServices, gracefulShutdownTimeout, false)

	if fullShutdown && msgBus != nil {
		msgBus.Close()
	}

	// Closing the loop closes its agents and the providers they run on.
	agentLoop.Stop()
	agentLoop.Close()

	logger.Info("✓ Gateway stopped")
}

// handleConfigReload applies a reloaded config: the agents are rebuilt from
// it and resolve their model selections anew. A config whose default model
// does not resolve is refused unless the gateway runs with allowEmpty (see
// checkDefaultModel); the previous config then stays in effect.
func handleConfigReload(
	ctx context.Context,
	al *agent.AgentLoop,
	newCfg *config.Config,
	resolve agent.ModelResolver,
	runningServices *services,
	msgBus *bus.MessageBus,
	allowEmptyStartup bool,
	debug bool,
) error {
	logger.Info("🔄 Config file changed, reloading...")

	if err := checkDefaultModel(newCfg, resolve, allowEmptyStartup); err != nil {
		logger.Errorf("  ⚠ Keeping the current config: %v", err)
		return fmt.Errorf("error resolving the default model: %w", err)
	}

	logger.Infof(" New model is '%s', rebuilding agents...", newCfg.Agents.Defaults.GetModelName())

	logger.Info("  Stopping all services...")
	stopAndCleanupServices(runningServices, serviceShutdownTimeout, true)

	reloadCtx, reloadCancel := context.WithTimeout(context.Background(), providerReloadTimeout)
	defer reloadCancel()

	if err := al.ReloadProviderAndConfig(reloadCtx, nil, newCfg); err != nil {
		logger.Errorf("  ⚠ Error reloading agent loop: %v", err)
		logger.Warn("  Attempting to restart services with old provider and config...")
		if restartErr := restartServices(al, runningServices, msgBus); restartErr != nil {
			logger.Errorf("  ⚠ Failed to restart services: %v", restartErr)
		}
		return fmt.Errorf("error reloading agent loop: %w", err)
	}

	logger.Info("  Restarting all services with new configuration...")
	if err := restartServices(al, runningServices, msgBus); err != nil {
		logger.Errorf("  ⚠ Error restarting services: %v", err)
		return fmt.Errorf("error restarting services: %w", err)
	}

	logger.Info("  ✓ Agents, configuration, and services reloaded successfully (thread-safe)")
	applyLoggingSettings(newCfg)

	// Debug mode permanently overrides the config log level to DEBUG.
	if !debug {
		// Update log level last so that reload-related info/warn logs above are not suppressed.
		effectiveLogLevel := config.EffectiveGatewayLogLevel(newCfg)
		logger.SetLevelFromString(effectiveLogLevel)
		logger.Infof("Log level changing from current to %q", effectiveLogLevel)
	}

	return nil
}

func restartServices(
	al *agent.AgentLoop,
	runningServices *services,
	msgBus *bus.MessageBus,
) error {
	cfg := al.GetConfig()

	execTimeout := time.Duration(cfg.Tools.Cron.ExecTimeoutMinutes) * time.Minute
	var err error
	runningServices.CronService, err = setupCronTool(
		al,
		msgBus,
		runningServices.homePath,
		cfg.WorkspacePath(),
		cfg.Agents.Defaults.RestrictToWorkspace,
		execTimeout,
		cfg,
	)
	if err != nil {
		return fmt.Errorf("error restarting cron service: %w", err)
	}
	if err = runningServices.CronService.Start(); err != nil {
		return fmt.Errorf("error restarting cron service: %w", err)
	}
	fmt.Println("  ✓ Cron service restarted")

	stateManager := sharedStateManager(al, cfg.WorkspacePath())

	runningServices.HeartbeatService = newHeartbeatService(cfg, al, msgBus, stateManager)
	if err = runningServices.HeartbeatService.Start(); err != nil {
		return fmt.Errorf("error restarting heartbeat service: %w", err)
	}
	if cfg.Heartbeat.Enabled {
		fmt.Println("  ✓ Heartbeat service restarted")
	}

	runningServices.MediaStore = media.NewFileMediaStoreWithCleanup(media.MediaCleanerConfig{
		Enabled:  cfg.Tools.MediaCleanup.Enabled,
		MaxAge:   time.Duration(cfg.Tools.MediaCleanup.MaxAge) * time.Minute,
		Interval: time.Duration(cfg.Tools.MediaCleanup.Interval) * time.Minute,
	})
	if fms, ok := runningServices.MediaStore.(*media.FileMediaStore); ok {
		fms.Start()
	}
	if runningServices.ChannelManager != nil {
		runningServices.ChannelManager.SetMediaStore(runningServices.MediaStore)
	}
	al.SetMediaStore(runningServices.MediaStore)

	al.SetChannelManager(runningServices.ChannelManager)

	if err = runningServices.ChannelManager.Reload(context.Background(), cfg); err != nil {
		return fmt.Errorf("error reload channels: %w", err)
	}
	fmt.Println("  ✓ Channels restarted.")

	enabledChannels := runningServices.ChannelManager.GetEnabledChannels()
	if len(enabledChannels) > 0 {
		fmt.Printf("  ✓ Channels enabled: %s\n", enabledChannels)
	} else {
		fmt.Println("  ⚠ Warning: No channels enabled")
	}

	runningServices.DeviceService = devices.NewService(devices.Config{
		Enabled:    cfg.Devices.Enabled,
		MonitorUSB: cfg.Devices.MonitorUSB,
	}, stateManager)
	runningServices.DeviceService.SetBus(msgBus)
	if err := runningServices.DeviceService.Start(context.Background()); err != nil {
		logger.WarnCF("device", "Failed to restart device service", map[string]any{"error": err.Error()})
	} else if cfg.Devices.Enabled {
		fmt.Println("  ✓ Device event service restarted")
	}

	transcriber := asr.DetectTranscriber(cfg)
	al.SetTranscriber(transcriber)
	if transcriber != nil {
		logger.InfoCF("voice", "Transcription re-enabled (agent-level)", map[string]any{"provider": transcriber.Name()})

		// Start Voice Agent Orchestrator on reload
		vaCtx, vaCancel := context.WithCancel(context.Background())
		runningServices.VoiceAgentCancel = vaCancel
		voiceAgent := asr.NewAgent(msgBus, transcriber)
		voiceAgent.Start(vaCtx)
	} else {
		logger.InfoCF("voice", "Transcription disabled", nil)
	}

	ttsAvailable := tts.DetectTTS(cfg) != nil
	logChannelVoiceCapabilities(runningServices.ChannelManager, transcriber != nil, ttsAvailable)
	// NOTE: PID file is written once at startup and not updated on reload.
	// Changing the gateway listen address requires a full restart.

	return nil
}

// setupConfigWatcherPolling signals on the returned channel when the config
// file at configPath changes. The gateway loads the file when it takes the
// signal, so it applies the file as it is then: a change seen while a signal
// is pending needs no signal of its own.
func setupConfigWatcherPolling(configPath string, debug bool) (<-chan struct{}, func()) {
	changed := make(chan struct{}, 1)
	stop := make(chan struct{})
	var wg sync.WaitGroup

	lastModTime := getFileModTime(configPath)
	lastSize := getFileSize(configPath)

	wg.Add(1)
	go func() {
		defer wg.Done()

		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				currentModTime := getFileModTime(configPath)
				currentSize := getFileSize(configPath)

				if currentModTime.After(lastModTime) || currentSize != lastSize {
					if debug {
						logger.Debugf("🔍 Config file change detected")
					}

					time.Sleep(500 * time.Millisecond)

					lastModTime = currentModTime
					lastSize = currentSize

					select {
					case changed <- struct{}{}:
					default:
					}
				}
			case <-stop:
				return
			}
		}
	}()

	stopFunc := func() {
		close(stop)
		wg.Wait()
	}

	return changed, stopFunc
}

func getFileModTime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

func getFileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func setupCronTool(
	agentLoop *agent.AgentLoop,
	msgBus *bus.MessageBus,
	homePath string,
	workspace string,
	restrict bool,
	execTimeout time.Duration,
	cfg *config.Config,
) (*cron.CronService, error) {
	cronService := cron.NewCronService(cron.DefaultStorePath(homePath), nil, cronOptions(execTimeout)...)

	var cronTool *tools.CronTool
	if cfg.Tools.IsToolEnabled("cron") {
		var err error
		cronTool, err = tools.NewCronTool(cronService, agentLoop, msgBus, workspace, restrict, execTimeout, cfg)
		if err != nil {
			return nil, fmt.Errorf("critical error during CronTool initialization: %w", err)
		}
		// tools.approval decides each run of a scheduled command; the loop
		// asks the approvers or the owner when the policy says to.
		cronTool.SetApprovalGate(agentLoop)

		agentLoop.RegisterTool(cronTool)
	}

	if cronTool != nil {
		// RunJob reports a failed run, one cut short by the job timeout
		// included, as an error, so the job's last status records it.
		cronService.SetOnJobContext(cronTool.RunJob)
	}

	return cronService, nil
}

// cronOptions lets a job run at least as long as the configured exec
// timeout, so that one isn't cut short.
func cronOptions(execTimeout time.Duration) []cron.Option {
	var opts []cron.Option
	if execTimeout > cron.DefaultJobTimeout {
		opts = append(opts, cron.WithJobTimeout(execTimeout))
	}
	return opts
}

// sharedStateManager returns the state manager the agent loop records the
// owner's chat in; the heartbeat and the device service report to that chat.
// Without a default agent the loop has none, and one reading the workspace's
// state file stands in.
func sharedStateManager(agentLoop *agent.AgentLoop, workspace string) *state.Manager {
	if agentLoop != nil {
		if sm := agentLoop.StateManager(); sm != nil {
			return sm
		}
	}
	return state.NewManager(workspace)
}

// newHeartbeatService builds the heartbeat service: it reads the owner's chat,
// which it runs for and reports to, from sm, the agent loop's state manager.
func newHeartbeatService(
	cfg *config.Config,
	agentLoop *agent.AgentLoop,
	msgBus *bus.MessageBus,
	sm *state.Manager,
) *heartbeat.HeartbeatService {
	hs := heartbeat.NewHeartbeatService(cfg.WorkspacePath(), cfg.Heartbeat.Interval, cfg.Heartbeat.Enabled)
	hs.SetBus(msgBus)
	hs.SetStateManager(sm)
	hs.SetHandler(createHeartbeatHandler(agentLoop))
	return hs
}

func createHeartbeatHandler(agentLoop *agent.AgentLoop) heartbeat.HeartbeatHandler {
	return heartbeatHandler(agentLoop.ProcessHeartbeat)
}

// heartbeatHandler runs a heartbeat turn with process. A reply other than
// HEARTBEAT_OK needs the owner's attention, so it is returned for the
// heartbeat service to deliver.
func heartbeatHandler(
	process func(ctx context.Context, prompt, channel, chatID string) (string, error),
) heartbeat.HeartbeatHandler {
	return func(prompt, channel, chatID string) *tools.ToolResult {
		// The heartbeat runs as the owner's chat it reports to. Without one
		// it is skipped: an internal identity such as the CLI's would give
		// unattended work local privileges.
		if channel == "" || chatID == "" {
			logger.InfoC("heartbeat", "Heartbeat skipped: the owner's chat is not known")
			return nil
		}

		response, err := process(context.Background(), prompt, channel, chatID)
		if err != nil {
			return tools.ErrorResult(fmt.Sprintf("Heartbeat error: %v", err))
		}
		response = strings.TrimSpace(response)
		if response == "" || response == heartbeatOK {
			return tools.SilentResult("Heartbeat OK")
		}
		return tools.UserResult(response)
	}
}
