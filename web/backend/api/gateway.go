package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"os"
	"os/exec"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xibodev/compa/v2/pkg/config"
	"github.com/xibodev/compa/v2/pkg/health"
	"github.com/xibodev/compa/v2/pkg/logger"
	"github.com/xibodev/compa/v2/pkg/netbind"
	ppid "github.com/xibodev/compa/v2/pkg/pid"
	"github.com/xibodev/compa/v2/web/backend/utils"
)

// gateway holds the state for the managed gateway process.
var gateway = struct {
	mu               sync.Mutex
	cmd              *exec.Cmd
	owned            bool // true if we started the process, false if we attached to an existing one
	bootDefaultModel string
	// bootConfig is the signature of the config the running gateway
	// applied, at boot or by a reload; nil when unknown. A saved config
	// whose signature differs needs a restart, or a reload for its live
	// parts (see applyLiveConfig).
	bootConfig      configSignature
	runtimeStatus   string
	startupDeadline time.Time
	logs            *LogBuffer
	pidData         *ppid.PidFileData // pid file data read from .compa.pid
	webChatToken    string            // cached raw web chat token for upstream gateway proxy injection
	// stopping is the kernel a stop or restart is ending, so its exit is
	// not taken for a crash.
	stopping *exec.Cmd
	// shutdown is set once the launcher exits: nothing restarts then.
	shutdown bool
	// crashRestarts are the automatic restarts within gatewayCrashWindow.
	crashRestarts []time.Time
}{
	runtimeStatus: "stopped",
	logs:          NewLogBuffer(200),
}

// refreshWebChatTokenLocked reads the web chat token from config and caches it.
// Caller must hold gateway.mu (or be sole writer).
func refreshWebChatTokenLocked(configPath string) {
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return
	}
	var webChatCfg config.WebChatSettings
	if bc := cfg.Channels.GetByType(config.ChannelWeb); bc != nil {
		decoded, err := bc.GetDecoded()
		if err == nil && decoded != nil {
			if p, ok := decoded.(*config.WebChatSettings); ok {
				webChatCfg = *p
			}
		}
	}
	gateway.webChatToken = webChatCfg.Token.String()
}

// ensureWebChatTokenCachedLocked lazily fills the in-memory web chat token cache when
// the launcher has already discovered a running gateway via pidData, but has
// not yet refreshed the token into memory.
func ensureWebChatTokenCachedLocked(configPath string) {
	if gateway.webChatToken != "" {
		return
	}
	refreshWebChatTokenLocked(configPath)
}

func (h *Handler) gatewayCommandArgs() []string {
	args := []string{"gateway", "-E"}
	if h.debug {
		args = append(args, "-d")
	}
	return args
}

const (
	protocolKey = "Sec-Websocket-Protocol"
	tokenPrefix = "token."
)

// webChatGatewayProtocol returns the gateway-facing web chat subprotocol that the
// launcher should inject when proxying browser traffic upstream.
func webChatGatewayProtocol() string {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	if gateway.webChatToken == "" {
		return ""
	}
	return tokenPrefix + gateway.webChatToken
}

var (
	gatewayStartupWindow          = 15 * time.Second
	gatewayRestartGracePeriod     = 5 * time.Second
	gatewayRestartForceKillWindow = 3 * time.Second
	gatewayRestartPollInterval    = 100 * time.Millisecond
	gatewayExecCommand            = exec.Command
	// gatewayStartProcess starts the prepared gateway command. Tests replace it
	// to observe the exact command (arguments and environment) deterministically.
	gatewayStartProcess = func(cmd *exec.Cmd) error { return cmd.Start() }
)

var gatewayHealthGet = func(url string, timeout time.Duration) (*http.Response, error) {
	client := http.Client{Timeout: timeout}
	return client.Get(url)
}

var gatewayProcessMatcher = isLikelyGatewayProcess

// getGatewayHealth checks the gateway health endpoint and returns the status response.
// Returns (*health.StatusResponse, statusCode, error). If error is not nil, the other values are not valid.
func (h *Handler) getGatewayHealth(cfg *config.Config, timeout time.Duration) (*health.StatusResponse, int, error) {
	// Prefer port/host from pidData when available.
	var port int
	var host string
	gateway.mu.Lock()
	if d := gateway.pidData; d != nil && d.Port > 0 {
		port = d.Port
		host = gatewayProbeHost(d.Host)
	}
	gateway.mu.Unlock()
	if port == 0 {
		port = 18790
		if cfg != nil && cfg.Gateway.Port != 0 {
			port = cfg.Gateway.Port
		}
	}
	if host == "" {
		host = gatewayProbeHost(h.effectiveGatewayBindHost(cfg))
	}

	url := "http://" + net.JoinHostPort(host, strconv.Itoa(port)) + "/health"

	return getGatewayHealthByURL(url, timeout)
}

func getGatewayHealthByURL(url string, timeout time.Duration) (*health.StatusResponse, int, error) {
	resp, err := gatewayHealthGet(url, timeout)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	var healthResponse health.StatusResponse
	if decErr := json.NewDecoder(resp.Body).Decode(&healthResponse); decErr != nil {
		return nil, resp.StatusCode, decErr
	}

	return &healthResponse, resp.StatusCode, nil
}

// gatewayReadyProbeTimeout bounds the readiness probe a status check makes.
const gatewayReadyProbeTimeout = 800 * time.Millisecond

// gatewayReadinessFailure asks the kernel pidData describes whether it
// processes messages: its /ready answers 503 naming each failed check. It
// returns the first failed check (by name) and its message, or "" when the
// kernel is ready, has not finished starting, or does not answer; whether it
// runs at all is decided elsewhere.
func (h *Handler) gatewayReadinessFailure(pidData *ppid.PidFileData, cfg *config.Config) (check, message string) {
	resp, err := gatewayHealthGet(gatewayBaseURLForPidData(h, pidData, cfg)+"/ready", gatewayReadyProbeTimeout)
	if err != nil {
		return "", ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		return "", ""
	}
	var ready health.StatusResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&ready); err != nil {
		return "", ""
	}
	names := make([]string, 0, len(ready.Checks))
	for name, c := range ready.Checks {
		if c.Status == "fail" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return "", ""
	}
	sort.Strings(names)
	failed := ready.Checks[names[0]]
	message = strings.TrimSpace(failed.Message)
	if message == "" {
		message = names[0] + " check failed"
	}
	return names[0], message
}

// trackedGatewayReadinessFailure is gatewayReadinessFailure for the kernel
// this launcher tracks, when it is alive and its PID file was read.
func (h *Handler) trackedGatewayReadinessFailure() (check, message string) {
	gateway.mu.Lock()
	cmd := gateway.cmd
	pidData := copyPidData(gateway.pidData)
	gateway.mu.Unlock()
	if pidData == nil || cmd == nil || cmd.Process == nil || cmd.Process.Pid != pidData.PID ||
		!isCmdProcessAliveLocked(cmd) {
		return "", ""
	}
	cfg, _ := config.LoadConfig(h.configPath)
	return h.gatewayReadinessFailure(pidData, cfg)
}

// isLikelyGatewayProcess returns whether PID appears to be a compa-kernel gateway
// process plus whether inspection was conclusive on this platform/environment.
// A process that no longer runs is conclusively no gateway.
func isLikelyGatewayProcess(pid int) (bool, bool) {
	if pid <= 0 || !ppid.IsProcessRunning(pid) {
		return false, true
	}
	return inspectGatewayProcess(pid)
}

// looksLikeGatewayCommandLine checks whether a process command line likely
// represents "compa-kernel gateway ..." regardless of executable filename.
func looksLikeGatewayCommandLine(cmdline string) bool {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(cmdline)))
	if len(fields) == 0 {
		return false
	}
	for _, f := range fields {
		token := strings.Trim(f, `"'`)
		if token == "gateway" || strings.HasSuffix(token, "/gateway") || strings.HasSuffix(token, `\gateway`) {
			return true
		}
	}
	return false
}

func (h *Handler) getGatewayHealthForPidData(
	pidData *ppid.PidFileData,
	cfg *config.Config,
	timeout time.Duration,
) (*health.StatusResponse, int, error) {
	if pidData == nil {
		return nil, 0, errors.New("nil pid data")
	}

	url := gatewayBaseURLForPidData(h, pidData, cfg) + "/health"
	return getGatewayHealthByURL(url, timeout)
}

// gatewayBaseURLForPidData is the base URL of the gateway pidData describes.
func gatewayBaseURLForPidData(h *Handler, pidData *ppid.PidFileData, cfg *config.Config) string {
	port := pidData.Port
	if port == 0 {
		port = 18790
		if cfg != nil && cfg.Gateway.Port != 0 {
			port = cfg.Gateway.Port
		}
	}

	host := gatewayProbeHost(strings.TrimSpace(pidData.Host))
	if host == "" {
		host = gatewayProbeHost(h.effectiveGatewayBindHost(cfg))
	}
	if host == "" {
		host = netbind.ResolveAdaptiveLoopbackHost()
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(port))
}

func (h *Handler) validateGatewayPidData(
	pidData *ppid.PidFileData,
	cfg *config.Config,
) (ok bool, decisive bool, reason string) {
	if pidData == nil || pidData.PID <= 0 {
		return false, true, "invalid pid data"
	}

	if gatewayProcess, inspected := cachedGatewayProcessMatch(pidData.PID); inspected {
		if !gatewayProcess {
			return false, true, "pid process command is not compa-kernel gateway"
		}
		return true, true, ""
	}

	healthResp, statusCode, err := h.getGatewayHealthForPidData(pidData, cfg, 800*time.Millisecond)
	if err != nil {
		return false, false, fmt.Sprintf("health probe failed: %v", err)
	}
	if statusCode != http.StatusOK {
		return false, false, fmt.Sprintf("health endpoint returned status %d", statusCode)
	}
	if healthResp.PID > 0 && healthResp.PID != pidData.PID {
		return false, true, fmt.Sprintf("health pid mismatch: pidFile=%d, health=%d", pidData.PID, healthResp.PID)
	}
	return true, true, ""
}

func (h *Handler) sanitizeGatewayPidData(pidData *ppid.PidFileData, cfg *config.Config) *ppid.PidFileData {
	if pidData == nil {
		return nil
	}

	ok, decisive, reason := h.validateGatewayPidData(pidData, cfg)
	if ok {
		return pidData
	}

	logger.Warnf("ignore pid file for PID %d: %s", pidData.PID, reason)
	if decisive && ppid.RemovePidFileIfPID(globalConfigDir(), pidData.PID) {
		logger.Warnf("removed stale pid file for PID %d", pidData.PID)
	}
	return nil
}

// registerGatewayRoutes binds gateway lifecycle endpoints to the ServeMux.
func (h *Handler) registerGatewayRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/gateway/status", h.handleGatewayStatus)
	mux.HandleFunc("GET /api/gateway/logs", h.handleGatewayLogs)
	mux.HandleFunc("POST /api/gateway/logs/clear", h.handleGatewayClearLogs)
	mux.HandleFunc("POST /api/gateway/start", h.handleGatewayStart)
	mux.HandleFunc("POST /api/gateway/stop", h.handleGatewayStop)
	mux.HandleFunc("POST /api/gateway/restart", h.handleGatewayRestart)
}

// TryAutoStartGateway starts the gateway, or attaches to one already
// running, when the config loads. Intended to be called by the backend at
// startup.
func (h *Handler) TryAutoStartGateway() {
	gatewayLifecycleMu.Lock()
	defer gatewayLifecycleMu.Unlock()

	pid, attached, err := h.ensureGatewayRunningLocked("starting")
	switch {
	case err != nil:
		logger.ErrorC("gateway", fmt.Sprintf("Failed to auto-start gateway: %v", err))
	case attached:
		logger.InfoC("gateway", fmt.Sprintf("Attached to running gateway via PID file (PID: %d)", pid))
	default:
		logger.InfoC("gateway", fmt.Sprintf("Gateway auto-started (PID: %d)", pid))
	}
}

// ensureGatewayRunningLocked makes a kernel run: a tracked kernel that is
// alive stays as it is, a kernel the PID file names is attached, and
// otherwise a new one starts. It reports the PID and whether it attached.
// The caller holds gatewayLifecycleMu.
func (h *Handler) ensureGatewayRunningLocked(initialStatus string) (int, bool, error) {
	cfg, err := h.gatewayStartConfig()
	if err != nil {
		return 0, false, err
	}

	gateway.mu.Lock()
	tracked := gateway.cmd
	gateway.mu.Unlock()
	if tracked != nil && tracked.Process != nil && isCmdProcessAliveLocked(tracked) {
		return tracked.Process.Pid, false, nil
	}

	// Check PID file first to detect an already-running gateway. Probing
	// happens before gateway.mu is taken.
	if pidData := h.sanitizeGatewayPidData(ppid.ReadPidFileWithCheck(globalConfigDir()), cfg); pidData != nil {
		gateway.mu.Lock()
		defer gateway.mu.Unlock()
		if err := attachToGatewayProcessLocked(pidData.PID, cfg); err != nil {
			return 0, false, fmt.Errorf("failed to attach to running gateway (PID %d): %w", pidData.PID, err)
		}
		gateway.pidData = pidData
		refreshWebChatTokenLocked(h.configPath)
		return pidData.PID, true, nil
	}

	h.logDefaultModelState(cfg)
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	gateway.cmd = nil
	pid, err := h.startGatewayLocked(initialStatus, 0)
	if err != nil {
		return 0, false, err
	}
	return pid, false, nil
}

// gatewayStartConfig returns the config the gateway would start with, or why
// it cannot start. Only a config that does not load blocks a start. The
// model never does: a gateway without a default model, or whose default
// selection does not resolve, starts anyway — its channels, tools and
// schedules work, and chat reports "No model selected" or why the selection
// does not resolve until a model is chosen. The gateway status reports the
// default model's state (see defaultModelState).
func (h *Handler) gatewayStartConfig() (*config.Config, error) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	return cfg, nil
}

// States of the default model in the gateway status.
const (
	// defaultModelNone: no default model is selected.
	defaultModelNone = "none"
	// defaultModelOK: the default selection resolves.
	defaultModelOK = "ok"
	// defaultModelUnavailable: the default selection does not resolve.
	defaultModelUnavailable = "unavailable"
)

// defaultModelState reports whether cfg's default model selection resolves
// against cfg and the saved catalogs — its instance exists and is enabled
// and its catalog holds the model, or its route exists — and, for one that
// does not, why.
func (h *Handler) defaultModelState(cfg *config.Config) (state, reason string) {
	selection := strings.TrimSpace(cfg.Agents.Defaults.GetModelName())
	if selection == "" {
		return defaultModelNone, ""
	}
	if err := h.modelResolver.Check(cfg, selection); err != nil {
		return defaultModelUnavailable, err.Error()
	}
	return defaultModelOK, ""
}

// logDefaultModelState logs a gateway start without a usable default model.
func (h *Handler) logDefaultModelState(cfg *config.Config) {
	switch state, reason := h.defaultModelState(cfg); state {
	case defaultModelNone:
		logger.InfoC("gateway", "No default model is selected; the gateway starts and chat reports that no model is selected")
	case defaultModelUnavailable:
		logger.WarnC("gateway", fmt.Sprintf(
			"Default model %q is unavailable (%s); the gateway starts and chat reports the error",
			strings.TrimSpace(cfg.Agents.Defaults.GetModelName()), reason,
		))
	}
}

// configSignature is the part of a config the restart indicator compares,
// keyed by what it covers: the model selections, the approval policy, the
// tools, the web tool settings, and per channel its access and the rest of
// its config, secrets included.
type configSignature map[string]string

// Keys of a configSignature. The channel keys are followed by the channel's
// name.
const (
	signatureModels   = "models"
	signatureApproval = "approval"
	signatureTools    = "tools"
	signatureWeb      = "web"
	// signatureAccess: a channel's allow_from, dm_policy and group_policy.
	signatureAccess = "access:"
	// signatureChannel: the rest of a channel's config, secrets included.
	signatureChannel = "channel:"
)

func computeConfigSignature(cfg *config.Config) configSignature {
	if cfg == nil {
		return nil
	}
	signature := configSignature{
		signatureModels:   modelSelectionSignature(cfg),
		signatureApproval: signatureJSON(cfg.Tools.Approval),
	}
	toolSignatures := []string{}
	if cfg.Tools.ReadFile.Enabled {
		toolSignatures = append(toolSignatures, "read_file")
	}
	if cfg.Tools.WriteFile.Enabled {
		toolSignatures = append(toolSignatures, "write_file")
	}
	if cfg.Tools.ListDir.Enabled {
		toolSignatures = append(toolSignatures, "list_dir")
	}
	if cfg.Tools.EditFile.Enabled {
		toolSignatures = append(toolSignatures, "edit_file")
	}
	if cfg.Tools.AppendFile.Enabled {
		toolSignatures = append(toolSignatures, "append_file")
	}
	if cfg.Tools.Exec.Enabled {
		toolSignatures = append(toolSignatures, "exec")
	}
	if cfg.Tools.Cron.Enabled {
		toolSignatures = append(toolSignatures, "cron")
	}
	if cfg.Tools.Web.Enabled {
		toolSignatures = append(toolSignatures, "web")
		signature[signatureWeb] = signatureJSON(cfg.Tools.Web)
	}
	if cfg.Tools.WebFetch.Enabled {
		toolSignatures = append(toolSignatures, "web_fetch")
	}
	if cfg.Tools.Message.Enabled {
		toolSignatures = append(toolSignatures, "message")
	}
	if cfg.Tools.SendFile.Enabled {
		toolSignatures = append(toolSignatures, "send_file")
	}
	if cfg.Tools.FindSkills.Enabled {
		toolSignatures = append(toolSignatures, "find_skills")
	}
	if cfg.Tools.InstallSkill.Enabled {
		toolSignatures = append(toolSignatures, "install_skill")
	}
	if cfg.Tools.Spawn.Enabled {
		toolSignatures = append(toolSignatures, "spawn")
	}
	if cfg.Tools.SpawnStatus.Enabled {
		toolSignatures = append(toolSignatures, "spawn_status")
	}
	if cfg.Tools.I2C.Enabled {
		toolSignatures = append(toolSignatures, "i2c")
	}
	if cfg.Tools.SPI.Enabled {
		toolSignatures = append(toolSignatures, "spi")
	}
	if cfg.Tools.MCP.Enabled {
		toolSignatures = append(toolSignatures, "mcp")
	}
	if cfg.Tools.MCP.Discovery.Enabled {
		toolSignatures = append(toolSignatures, "mcp_discovery")
	}
	if cfg.Tools.MCP.Discovery.UseRegex {
		toolSignatures = append(toolSignatures, "mcp_discovery_regex")
	}
	if cfg.Tools.MCP.Discovery.UseBM25 {
		toolSignatures = append(toolSignatures, "mcp_discovery_bm25")
	}
	signature[signatureTools] = strings.Join(toolSignatures, ",")
	for name, channel := range cfg.Channels {
		addChannelSignature(signature, name, channel)
	}
	return signature
}

// equal reports whether s and other cover the same config.
func (s configSignature) equal(other configSignature) bool {
	return maps.Equal(s, other)
}

// isLiveSignatureKey reports whether a change of what key covers takes
// effect in the running gateway through its /reload, without a restart: the
// model selections, the approval policy and the channels' access lists and
// policies.
func isLiveSignatureKey(key string) bool {
	return key == signatureModels || key == signatureApproval || strings.HasPrefix(key, signatureAccess)
}

// liveEqual reports whether s and other agree on their live parts (see
// isLiveSignatureKey).
func (s configSignature) liveEqual(other configSignature) bool {
	return s.equalOn(other, isLiveSignatureKey)
}

// equalBesidesLive reports whether s and other agree on all but their live
// parts.
func (s configSignature) equalBesidesLive(other configSignature) bool {
	return s.equalOn(other, func(key string) bool { return !isLiveSignatureKey(key) })
}

// equalOn reports whether s and other agree on the keys covered selects.
func (s configSignature) equalOn(other configSignature, covered func(key string) bool) bool {
	for key, value := range s {
		if otherValue, ok := other[key]; covered(key) && (!ok || otherValue != value) {
			return false
		}
	}
	for key := range other {
		if _, ok := s[key]; covered(key) && !ok {
			return false
		}
	}
	return true
}

// modelSelectionSignatures returns the model settings the gateway reads from
// the config it boots with — the default, image and light model selections,
// the light-model routing and each agent's model — so changing one requires
// a reload. Provider instances, their runtime settings, routes and catalogs
// are left out: the gateway resolves a selection against them as saved when
// a turn runs.
func modelSelectionSignatures(cfg *config.Config) []string {
	selections := modelSelections(cfg)
	signatures := make([]string, 0, len(selections)+1)
	for _, selection := range selections {
		signatures = append(signatures, selection.key+"="+selection.value)
	}
	if routing := cfg.Agents.Defaults.Routing; routing != nil {
		signatures = append(signatures, fmt.Sprintf(
			"routing=%t/%s", routing.Enabled, strconv.FormatFloat(routing.Threshold, 'g', -1, 64),
		))
	}
	return signatures
}

// modelSelectionSignature is modelSelectionSignatures of cfg, joined.
func modelSelectionSignature(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return strings.Join(modelSelectionSignatures(cfg), ",")
}

// addChannelSignature adds the signature of the channel name to signature:
// its access, and the rest of its config, secrets included.
func addChannelSignature(signature configSignature, name string, channel *config.Channel) {
	if channel == nil {
		signature[signatureChannel+name] = "<nil>"
		return
	}
	signature[signatureAccess+name] = marshalSignature(struct {
		AllowFrom   config.FlexibleStringSlice `json:"allow_from,omitempty"`
		DMPolicy    string                     `json:"dm_policy,omitempty"`
		GroupPolicy string                     `json:"group_policy,omitempty"`
	}{
		AllowFrom:   channel.AllowFrom,
		DMPolicy:    channel.DMPolicy,
		GroupPolicy: channel.GroupPolicy,
	})
	signature[signatureChannel+name] = marshalSignature(struct {
		Enabled            bool                      `json:"enabled"`
		Type               string                    `json:"type"`
		ReasoningChannelID string                    `json:"reasoning_channel_id,omitempty"`
		GroupTrigger       config.GroupTriggerConfig `json:"group_trigger,omitempty"`
		Typing             config.TypingConfig       `json:"typing,omitempty"`
		Placeholder        config.PlaceholderConfig  `json:"placeholder,omitempty"`
		Settings           json.RawMessage           `json:"settings,omitempty"`
	}{
		Enabled:            channel.Enabled,
		Type:               channel.Type,
		ReasoningChannelID: channel.ReasoningChannelID,
		GroupTrigger:       channel.GroupTrigger,
		Typing:             channel.Typing,
		Placeholder:        channel.Placeholder,
		Settings:           normalizeChannelSettings(channel),
	})
}

// signatureJSON is value as canonical JSON, secrets included.
func signatureJSON(value any) string {
	return marshalSignature(canonicalizeSignatureValue(reflect.ValueOf(value)))
}

func marshalSignature(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "<invalid>"
	}
	return string(encoded)
}

// normalizeChannelSettings is the channel's settings as canonical JSON,
// secrets included.
func normalizeChannelSettings(channel *config.Channel) json.RawMessage {
	if channel == nil {
		return nil
	}

	decoded, err := channel.GetDecoded()
	if err == nil && decoded != nil {
		normalized, err := json.Marshal(canonicalizeSignatureValue(reflect.ValueOf(decoded)))
		if err == nil {
			return normalized
		}
	}

	return normalizeRawJSON(channel.Settings)
}

func normalizeRawJSON(raw config.RawNode) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}

	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return bytes.TrimSpace(raw)
	}

	normalized, err := json.Marshal(value)
	if err != nil {
		return bytes.TrimSpace(raw)
	}
	return normalized
}

// canonicalizeSignatureValue turns value into maps, slices and plain values
// that encode the same way whatever the order of its maps. A secret becomes
// its value.
func canonicalizeSignatureValue(value reflect.Value) any {
	if !value.IsValid() {
		return nil
	}

	if value.CanInterface() {
		if secret, ok := secretSignatureValue(value.Interface()); ok {
			return secret
		}
	}

	switch value.Kind() {
	case reflect.Interface, reflect.Pointer:
		if value.IsNil() {
			return nil
		}
		return canonicalizeSignatureValue(value.Elem())
	case reflect.Struct:
		result := make(map[string]any)
		valueType := value.Type()
		for i := 0; i < value.NumField(); i++ {
			field := valueType.Field(i)
			if field.PkgPath != "" {
				continue
			}
			tag := field.Tag.Get("json")
			name := field.Name
			if tag != "" {
				if comma := strings.Index(tag, ","); comma >= 0 {
					tag = tag[:comma]
				}
				if tag == "-" {
					continue
				}
				if tag != "" {
					name = tag
				}
			}
			result[name] = canonicalizeSignatureValue(value.Field(i))
		}
		return result
	case reflect.Slice, reflect.Array:
		length := value.Len()
		result := make([]any, 0, length)
		for i := 0; i < length; i++ {
			result = append(result, canonicalizeSignatureValue(value.Index(i)))
		}
		return result
	case reflect.Map:
		if value.Type().Key().Kind() != reflect.String {
			return value.Interface()
		}
		result := make(map[string]any, value.Len())
		iter := value.MapRange()
		for iter.Next() {
			result[iter.Key().String()] = canonicalizeSignatureValue(iter.Value())
		}
		return result
	default:
		if value.CanInterface() {
			return value.Interface()
		}
		return nil
	}
}

// secretSignatureValue returns the value of v when v is a secret.
func secretSignatureValue(v any) (any, bool) {
	switch typed := v.(type) {
	case config.SecureString:
		return typed.String(), true
	case *config.SecureString:
		return typed.String(), true
	case config.SecureStrings:
		return typed.Values(), true
	case *config.SecureStrings:
		return typed.Values(), true
	}
	return nil, false
}

// gatewayRestartRequiredBySignature reports whether the running gateway
// runs a config other than the saved one. While a live apply is pending,
// the live parts are left out: the apply brings them to the gateway, and
// once it failed they count again.
func gatewayRestartRequiredBySignature(bootSignature, currentSignature configSignature, gatewayStatus string, liveApplyPending bool) bool {
	if gatewayStatus != "running" {
		return false
	}
	if len(bootSignature) == 0 || len(currentSignature) == 0 {
		return false
	}
	if liveApplyPending {
		return !bootSignature.equalBesidesLive(currentSignature)
	}
	return !bootSignature.equal(currentSignature)
}

// gatewayRestartsOnConfigChange reports whether a change that needs the
// kernel restarted (a reset, a channel binding) should restart it, given
// gatewayStatusData: when it runs, also when it runs with a failed readiness
// check.
func gatewayRestartsOnConfigChange(status map[string]any) bool {
	switch s, _ := status["gateway_status"].(string); s {
	case "running":
		return true
	case "error":
		_, alive := status["gateway_error_check"]
		return alive
	}
	return false
}

func isCmdProcessAliveLocked(cmd *exec.Cmd) bool {
	if cmd == nil || cmd.Process == nil {
		return false
	}
	// A kernel this launcher started: its waiter closes the channel once
	// Wait returned. cmd.ProcessState is not read, as Wait writes it.
	if done, ok := processExits.Load(cmd); ok {
		select {
		case <-done.(chan struct{}):
			return false
		default:
			return true
		}
	}
	// Wait already returned for it.
	if cmd.ProcessState != nil {
		return false
	}
	// An attached kernel: nobody here waits on it, so ask the system. On
	// Windows the handle os.FindProcess keeps open stops the PID from being
	// reused while it is tracked.
	return ppid.IsProcessRunning(cmd.Process.Pid)
}

func setGatewayRuntimeStatusLocked(status string) {
	gateway.runtimeStatus = status
	if status == "starting" || status == "restarting" {
		gateway.startupDeadline = time.Now().Add(gatewayStartupWindow)
		return
	}
	gateway.startupDeadline = time.Time{}
}

// attachToGatewayProcess attaches to an existing gateway process by PID
// and updates the gateway state accordingly.
// Assumes gateway.mu is held by the caller.
func attachToGatewayProcessLocked(pid int, cfg *config.Config) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("failed to find process for PID %d: %w", pid, err)
	}

	gateway.cmd = &exec.Cmd{Process: process}
	gateway.owned = false // We didn't start this process
	setGatewayRuntimeStatusLocked("running")

	// Update bootDefaultModel and bootConfig from config
	if cfg != nil {
		defaultModelName := strings.TrimSpace(cfg.Agents.Defaults.GetModelName())
		gateway.bootDefaultModel = defaultModelName
		gateway.bootConfig = computeConfigSignature(cfg)
	}

	logger.InfoC("gateway", fmt.Sprintf("Attached to gateway process (PID: %d)", pid))
	return nil
}

func gatewayStatusWithoutHealthLocked() string {
	if gateway.runtimeStatus == "starting" || gateway.runtimeStatus == "restarting" {
		if gateway.startupDeadline.IsZero() || time.Now().Before(gateway.startupDeadline) {
			return gateway.runtimeStatus
		}
		return "error"
	}
	if gateway.runtimeStatus == "running" {
		// For attached processes there is no waiter goroutine; degrade stale
		// running state once the tracked process exits.
		if !isCmdProcessAliveLocked(gateway.cmd) {
			gateway.cmd = nil
			gateway.owned = false
			gateway.bootDefaultModel = ""
			gateway.bootConfig = nil
			return "stopped"
		}
		return "running"
	}
	if gateway.runtimeStatus == "error" {
		return "error"
	}
	return "stopped"
}

func waitForGatewayProcessExit(cmd *exec.Cmd, timeout time.Duration) bool {
	if cmd == nil || cmd.Process == nil {
		return true
	}

	deadline := time.Now().Add(timeout)
	for {
		if !isCmdProcessAliveLocked(cmd) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(gatewayRestartPollInterval)
	}
}

// StopGateway stops the gateway process if it was started by this handler.
// This method is called during application shutdown to ensure the gateway subprocess
// is properly terminated. It only stops processes that were started by this handler:
// a gateway that was already running when the launcher started (attached) keeps
// running after the launcher quits.
func (h *Handler) StopGateway() {
	gateway.mu.Lock()
	gateway.shutdown = true
	gateway.mu.Unlock()

	gatewayLifecycleMu.Lock()
	defer gatewayLifecycleMu.Unlock()

	gateway.mu.Lock()
	owned := gateway.owned && gateway.cmd != nil && gateway.cmd.Process != nil
	gateway.mu.Unlock()
	if !owned {
		return
	}

	pid, err := stopTrackedGatewayLocked()
	if err != nil {
		logger.ErrorC("gateway", fmt.Sprintf("Failed to stop gateway (PID %d): %v", pid, err))
		return
	}
	logger.InfoC("gateway", fmt.Sprintf("Gateway stopped (PID: %d)", pid))
}

// errGatewayNotRunning reports a stop without a tracked kernel.
var errGatewayNotRunning = errors.New("gateway is not running")

// errNotAGateway refuses to stop a process the PID file named that turned
// out not to be the kernel.
var errNotAGateway = errors.New("refuse to stop non-gateway process")

// stopTrackedGatewayLocked stops the tracked kernel, attached ones too, and
// waits for it to exit (see terminateGatewayProcess). A kernel that vanished
// counts as stopped. The caller holds gatewayLifecycleMu, not gateway.mu.
func stopTrackedGatewayLocked() (int, error) {
	gateway.mu.Lock()
	cmd := gateway.cmd
	owned := gateway.owned
	pidData := copyPidData(gateway.pidData)
	gateway.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return 0, errGatewayNotRunning
	}
	pid := cmd.Process.Pid

	if isCmdProcessAliveLocked(cmd) {
		if !owned {
			if isGateway, inspected := gatewayProcessMatcher(pid); inspected && !isGateway {
				return pid, fmt.Errorf("%w (PID %d)", errNotAGateway, pid)
			}
		}
		gateway.mu.Lock()
		gateway.stopping = cmd
		gateway.mu.Unlock()
		logger.InfoC("gateway", fmt.Sprintf("Stopping gateway (PID: %d)", pid))
		if err := terminateGatewayProcess(cmd, pidData); err != nil {
			gateway.mu.Lock()
			if gateway.stopping == cmd {
				gateway.stopping = nil
			}
			gateway.mu.Unlock()
			return pid, err
		}
	}

	gateway.mu.Lock()
	if gateway.cmd == cmd {
		clearTrackedGatewayLocked()
		gateway.pidData = nil
	}
	if gateway.stopping == cmd {
		gateway.stopping = nil
	}
	setGatewayRuntimeStatusLocked("stopped")
	gateway.mu.Unlock()
	return pid, nil
}

// clearTrackedGatewayLocked forgets the tracked kernel. The caller holds
// gateway.mu.
func clearTrackedGatewayLocked() {
	gateway.cmd = nil
	gateway.owned = false
	gateway.bootDefaultModel = ""
	gateway.bootConfig = nil
}

func copyPidData(pidData *ppid.PidFileData) *ppid.PidFileData {
	if pidData == nil {
		return nil
	}
	copied := *pidData
	return &copied
}

func (h *Handler) startGatewayLocked(initialStatus string, existingPid int) (int, error) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		return 0, fmt.Errorf("failed to load config: %w", err)
	}
	defaultModelName := strings.TrimSpace(cfg.Agents.Defaults.GetModelName())

	var cmd *exec.Cmd
	var pid int

	if existingPid > 0 {
		// Attach to existing process
		pid = existingPid
		gateway.cmd = nil // Clear first to ensure clean state
		if err = attachToGatewayProcessLocked(pid, cfg); err != nil {
			logger.ErrorC("gateway", fmt.Sprintf("Failed to attach to existing gateway (PID %d): %v", pid, err))
			return 0, err
		}

		return pid, nil
	}

	// Start new process
	// Locate the kernel executable
	execPath := utils.FindKernelBinary()
	logger.InfoC("gateway", fmt.Sprintf("Starting gateway process (%s)", execPath))

	cmd = gatewayExecCommand(execPath, h.gatewayCommandArgs()...)
	applyLauncherProcAttrs(cmd)
	applyKernelProcAttrs(cmd)
	// Run in the Compa home, not wherever the launcher was started from: the
	// kernel reads skills from its working directory.
	if home := globalConfigDir(); home != "" {
		if err := os.MkdirAll(home, 0o755); err == nil {
			cmd.Dir = home
		}
	}
	cmd.Env = os.Environ()
	// Forward the launcher's config path via the environment variable that
	// GetConfigPath() already reads, so the gateway sub-process uses the same
	// config file without requiring a --config flag on the gateway subcommand.
	// The kernel binds the host its own config names, loopback by default,
	// even when the dashboard listens on the network: the dashboard proxies
	// everything a browser needs from it.
	if h.configPath != "" {
		cmd.Env = append(cmd.Env, config.EnvConfig+"="+h.configPath)
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return 0, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return 0, fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	// Clear old logs for this new run
	gateway.logs.Reset()

	// Ensure web chat channel is configured before starting gateway
	changed, err := h.EnsureWebChatChannel()
	if err != nil {
		logger.ErrorC("gateway", fmt.Sprintf("Warning: failed to ensure web channel: %v", err))
		// Non-fatal: gateway can still start without web channel
	}
	// Refresh cached web chat token in case EnsureWebChatChannel generated a new one.
	// Already holding gateway.mu from caller.
	if changed {
		refreshWebChatTokenLocked(h.configPath)
		cfg, err = config.LoadConfig(h.configPath)
		if err != nil {
			return 0, fmt.Errorf("failed to reload config after ensuring web channel: %w", err)
		}
		defaultModelName = strings.TrimSpace(cfg.Agents.Defaults.GetModelName())
	}

	if err := gatewayStartProcess(cmd); err != nil {
		return 0, fmt.Errorf("failed to start gateway: %w", err)
	}
	if err := bindKernelLifetime(cmd); err != nil {
		logger.WarnC("gateway", fmt.Sprintf("The gateway may outlive the launcher: %v", err))
	}
	exited := make(chan struct{})
	processExits.Store(cmd, exited)

	gateway.cmd = cmd
	gateway.owned = true // We started this process
	gateway.bootDefaultModel = defaultModelName
	gateway.bootConfig = computeConfigSignature(cfg)
	setGatewayRuntimeStatusLocked(initialStatus)
	pid = cmd.Process.Pid
	logger.InfoC("gateway", fmt.Sprintf("Started compa-kernel gateway (PID: %d) from %s", pid, execPath))

	// Capture stdout/stderr in background. The readers never stop before
	// the pipes close, so the kernel never blocks on a full pipe.
	var readers sync.WaitGroup
	readers.Add(2)
	go func() {
		defer readers.Done()
		drainLogPipe(stdoutPipe, gateway.logs)
	}()
	go func() {
		defer readers.Done()
		drainLogPipe(stderrPipe, gateway.logs)
	}()

	// Wait for exit in background and clean up. Wait closes the pipes, so it
	// runs only once both readers have read everything.
	go func() {
		readers.Wait()
		waitErr := cmd.Wait()
		close(exited)
		processExits.Delete(cmd)
		h.handleGatewayExit(cmd, waitErr)
	}()

	// Start a goroutine to probe pidFile and health, update runtime state once ready.
	go func() {
		healthConfirmed := false
		for i := 0; i < 30; i++ { // try for up to 15 seconds
			time.Sleep(500 * time.Millisecond)
			gateway.mu.Lock()
			stillOurs := gateway.cmd == cmd
			gateway.mu.Unlock()
			if !stillOurs {
				return
			}

			// Poll for pidFile first — once available we have port/host/token.
			if pd := ppid.ReadPidFileWithCheck(globalConfigDir()); pd != nil && pd.PID == pid {
				gateway.mu.Lock()
				if gateway.cmd == cmd {
					gateway.pidData = pd
					var webChatCfg config.WebChatSettings
					if bc := cfg.Channels.GetByType(config.ChannelWeb); bc != nil {
						decoded, err := bc.GetDecoded()
						if err == nil && decoded != nil {
							if p, ok := decoded.(*config.WebChatSettings); ok {
								webChatCfg = *p
							}
						}
					}
					gateway.webChatToken = webChatCfg.Token.String()
					setGatewayRuntimeStatusLocked("running")
				}
				gateway.mu.Unlock()
				logger.InfoC("gateway", fmt.Sprintf("Gateway pidFile detected (PID: %d, port: %d)", pd.PID, pd.Port))
				return
			}

			// Fallback: probe health endpoint to confirm liveness.
			cfg, err := config.LoadConfig(h.configPath)
			if err != nil {
				continue
			}
			_, statusCode, err := h.getGatewayHealth(cfg, 1*time.Second)
			if err == nil && statusCode == http.StatusOK {
				gateway.mu.Lock()
				if gateway.cmd == cmd {
					setGatewayRuntimeStatusLocked("running")
				}
				gateway.mu.Unlock()
				if !healthConfirmed {
					healthConfirmed = true
					logger.InfoC("gateway", "Gateway health endpoint reachable; waiting for pid file")
				}
				continue
			}
		}
	}()

	return pid, nil
}

// handleGatewayStart starts the compa-kernel gateway subprocess, or attaches
// to one already running. It starts without a usable default model too; see
// gatewayStartConfig.
//
//	POST /api/gateway/start
func (h *Handler) handleGatewayStart(w http.ResponseWriter, r *http.Request) {
	pid, err := h.StartGateway()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to start gateway: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"pid":    pid,
	})
}

// StartGateway starts the kernel unless one runs already: a tracked kernel
// that is alive is kept, and one the PID file names is attached. A tracked
// kernel whose readiness check failed (the status reads "error") is
// replaced, as it runs without processing messages. It returns the PID of
// the running kernel.
func (h *Handler) StartGateway() (int, error) {
	gatewayLifecycleMu.Lock()
	defer gatewayLifecycleMu.Unlock()

	gateway.mu.Lock()
	// A start by hand gives a kernel that crashed repeatedly a fresh chance.
	gateway.crashRestarts = nil
	gateway.mu.Unlock()
	if check, message := h.trackedGatewayReadinessFailure(); check != "" {
		cfg, err := h.gatewayStartConfig()
		if err != nil {
			return 0, err
		}
		logger.WarnC("gateway", fmt.Sprintf("Replacing the gateway: its %s check failed (%s)", check, message))
		return h.restartGatewayLocked(cfg)
	}
	pid, _, err := h.ensureGatewayRunningLocked("starting")
	return pid, err
}

// handleGatewayStop stops the running gateway subprocess gracefully.
// Note: Unlike StopGateway (which only stops self-started processes), this API endpoint
// stops any gateway process, including attached ones. This is intentional for user control.
//
//	POST /api/gateway/stop
func (h *Handler) handleGatewayStop(w http.ResponseWriter, r *http.Request) {
	gatewayLifecycleMu.Lock()
	defer gatewayLifecycleMu.Unlock()

	pid, err := stopTrackedGatewayLocked()
	switch {
	case errors.Is(err, errGatewayNotRunning):
		// A crash restart waiting for its turn is called off.
		gateway.mu.Lock()
		gateway.crashRestarts = nil
		if gateway.runtimeStatus == "restarting" || gateway.runtimeStatus == "error" {
			setGatewayRuntimeStatusLocked("stopped")
		}
		gateway.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"status": "not_running"})
	case errors.Is(err, errNotAGateway):
		writeJSONError(w, http.StatusConflict, fmt.Sprintf("Failed to stop gateway (PID %d): %v", pid, err))
	case err != nil:
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to stop gateway (PID %d): %v", pid, err))
	default:
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "ok",
			"pid":    pid,
		})
	}
}

// RestartGateway restarts the gateway process: it stops the current gateway
// (if running), waits for it to exit, and starts a new one, all under
// gatewayLifecycleMu so no other start runs in between. A config that does
// not load refuses the restart before the running gateway stops.
// Returns the PID of the new gateway process or an error.
func (h *Handler) RestartGateway() (int, error) {
	cfg, err := h.gatewayStartConfig()
	if err != nil {
		return 0, fmt.Errorf("failed to validate gateway start conditions: %w", err)
	}

	gatewayLifecycleMu.Lock()
	defer gatewayLifecycleMu.Unlock()
	return h.restartGatewayLocked(cfg)
}

// restartGatewayLocked is RestartGateway for a caller that holds
// gatewayLifecycleMu and checked that cfg loads.
func (h *Handler) restartGatewayLocked(cfg *config.Config) (int, error) {
	gateway.mu.Lock()
	previousCmd := gateway.cmd
	previousOwned := gateway.owned
	pidData := copyPidData(gateway.pidData)
	gateway.crashRestarts = nil
	setGatewayRuntimeStatusLocked("restarting")
	gateway.mu.Unlock()

	previousAlive := isCmdProcessAliveLocked(previousCmd)
	if previousAlive && !previousOwned {
		if isGateway, inspected := gatewayProcessMatcher(previousCmd.Process.Pid); inspected && !isGateway {
			logger.Warnf("refuse restarting non-gateway process (PID: %d)", previousCmd.Process.Pid)
			gateway.mu.Lock()
			if gateway.cmd == previousCmd {
				setGatewayRuntimeStatusLocked("running")
			}
			gateway.mu.Unlock()
			return 0, fmt.Errorf("refuse to restart non-gateway process (PID %d)", previousCmd.Process.Pid)
		}
	}

	if previousAlive {
		gateway.mu.Lock()
		gateway.stopping = previousCmd
		gateway.mu.Unlock()
		if err := terminateGatewayProcess(previousCmd, pidData); err != nil {
			gateway.mu.Lock()
			gateway.stopping = nil
			if gateway.cmd == previousCmd {
				if isCmdProcessAliveLocked(previousCmd) {
					setGatewayRuntimeStatusLocked("running")
				} else {
					clearTrackedGatewayLocked()
					setGatewayRuntimeStatusLocked("error")
				}
			}
			gateway.mu.Unlock()
			return 0, fmt.Errorf("failed to stop gateway: %w", err)
		}
	}

	h.logDefaultModelState(cfg)
	gateway.mu.Lock()
	if gateway.cmd == previousCmd {
		clearTrackedGatewayLocked()
	}
	gateway.stopping = nil
	pid, err := h.startGatewayLocked("restarting", 0)
	if err != nil {
		clearTrackedGatewayLocked()
		setGatewayRuntimeStatusLocked("error")
	}
	gateway.mu.Unlock()
	if err != nil {
		return 0, fmt.Errorf("failed to start gateway: %w", err)
	}

	return pid, nil
}

// handleGatewayRestart stops the gateway (if running) and starts a new instance.
//
//	POST /api/gateway/restart
func (h *Handler) handleGatewayRestart(w http.ResponseWriter, r *http.Request) {
	pid, err := h.RestartGateway()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to restart gateway: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"pid":    pid,
	})
}

// handleGatewayClearLogs clears the in-memory gateway log buffer.
//
//	POST /api/gateway/logs/clear
func (h *Handler) handleGatewayClearLogs(w http.ResponseWriter, r *http.Request) {
	gateway.logs.Clear()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":     "cleared",
		"log_total":  0,
		"log_run_id": gateway.logs.RunID(),
	})
}

// handleGatewayStatus returns the gateway run status and health info.
//
//	GET /api/gateway/status
func (h *Handler) handleGatewayStatus(w http.ResponseWriter, r *http.Request) {
	data := h.gatewayStatusData()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

// gatewayStatusData returns the gateway status: its run state, whether a
// restart would apply config changes, and whether it can start. Its default
// model fields hold selections: config_default_model is the config's
// default (omitted when none), config_default_model_status whether it
// resolves (defaultModelNone, defaultModelOK or defaultModelUnavailable,
// with config_default_model_error saying why not), and boot_default_model
// the default the running gateway booted with. The model never blocks a
// start; see gatewayStartConfig.
//
// A running kernel whose readiness check failed (its agent loop stopped,
// say) reads gateway_status "error", with gateway_error the check's message
// and gateway_error_check its name; starting it replaces it.
func (h *Handler) gatewayStatusData() map[string]any {
	data := map[string]any{}
	cfg, cfgErr := config.LoadConfig(h.configPath)
	if cfgErr == nil {
		if selection := strings.TrimSpace(cfg.Agents.Defaults.GetModelName()); selection != "" {
			data["config_default_model"] = selection
		}
		state, reason := h.defaultModelState(cfg)
		data["config_default_model_status"] = state
		if reason != "" {
			data["config_default_model_error"] = reason
		}
	}

	// Primary detection: read PID file and check if process is alive.
	pidData := h.sanitizeGatewayPidData(ppid.ReadPidFileWithCheck(globalConfigDir()), cfg)
	if pidData != nil {
		gateway.mu.Lock()
		gateway.pidData = pidData
		if pidData.Version != "" {
			data["gateway_version"] = pidData.Version
		}
		setGatewayRuntimeStatusLocked("running")

		// Attach if we don't already track this PID.
		if gateway.cmd == nil || gateway.cmd.Process == nil || gateway.cmd.Process.Pid != pidData.PID {
			_ = attachToGatewayProcessLocked(pidData.PID, cfg)
		}

		bootDefaultModel := gateway.bootDefaultModel
		if bootDefaultModel != "" {
			data["boot_default_model"] = bootDefaultModel
		}
		data["gateway_status"] = "running"
		data["pid"] = pidData.PID
		gateway.mu.Unlock()
	} else {
		// Intentionally skip health probe here; the startup goroutine
		// (startGatewayLocked) already handles liveness detection via
		// pidFile polling and health fallback.
		gateway.mu.Lock()
		status := gatewayStatusWithoutHealthLocked()
		data["gateway_status"] = status
		// Keep last known pidData while gateway is still in a transient
		// running state; otherwise websocket proxy may lose auth token
		// during short pid-file races.
		if status == "stopped" || status == "error" {
			gateway.pidData = nil
		}
		gateway.mu.Unlock()
	}

	gatewayStatus, _ := data["gateway_status"].(string)
	if gatewayStatus == "running" && pidData != nil {
		// A kernel whose agent loop stopped still answers /health; its
		// /ready names the failed check.
		if check, message := h.gatewayReadinessFailure(pidData, cfg); check != "" {
			gatewayStatus = "error"
			data["gateway_status"] = gatewayStatus
			data["gateway_error"] = message
			data["gateway_error_check"] = check
		}
	}
	currentConfig := computeConfigSignature(cfg)
	// Read before the applied signature: an apply records what it applied
	// before it stops being pending.
	liveApplyPending := h.pendingLiveApplies.Load() > 0
	gateway.mu.Lock()
	bootConfig := gateway.bootConfig
	gateway.mu.Unlock()
	data["gateway_restart_required"] = gatewayRestartRequiredBySignature(
		bootConfig,
		currentConfig,
		gatewayStatus,
		liveApplyPending,
	)

	if cfgErr != nil {
		data["gateway_start_allowed"] = false
		data["gateway_start_reason"] = fmt.Sprintf("failed to load config: %v", cfgErr)
	} else {
		data["gateway_start_allowed"] = true
	}

	return data
}

// handleGatewayLogs returns buffered gateway logs, optionally incrementally.
//
//	GET /api/gateway/logs
func (h *Handler) handleGatewayLogs(w http.ResponseWriter, r *http.Request) {
	data := gatewayLogsData(r)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

// gatewayLogsData reads log_offset and log_run_id query params from the request
// and returns incremental log lines.
func gatewayLogsData(r *http.Request) map[string]any {
	data := map[string]any{}
	clientOffset := 0
	clientRunID := -1

	if v := r.URL.Query().Get("log_offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			clientOffset = n
		}
	}

	if v := r.URL.Query().Get("log_run_id"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			clientRunID = n
		}
	}

	runID := gateway.logs.RunID()

	if runID == 0 {
		data["logs"] = []string{}
		data["log_total"] = 0
		data["log_run_id"] = 0
		return data
	}

	// If runID changed, reset offset to get all logs from new run
	offset := clientOffset
	if clientRunID != runID {
		offset = 0
	}

	lines, total, runID := gateway.logs.LinesSince(offset)
	if lines == nil {
		lines = []string{}
	}

	data["logs"] = lines
	data["log_total"] = total
	data["log_run_id"] = runID
	return data
}

// maxGatewayLogLine bounds one kernel log line kept in the buffer; the rest
// of a longer line is dropped, not left in the pipe.
const maxGatewayLogLine = 64 << 10

// drainLogPipe reads lines from r and appends them to buf until r reaches EOF
// or fails. It never stops early: a line longer than maxGatewayLogLine is cut
// and the rest discarded, so the kernel never blocks on a full pipe.
func drainLogPipe(r io.Reader, buf *LogBuffer) {
	reader := bufio.NewReaderSize(r, 64<<10)
	var line []byte
	dropped := 0
	flush := func() {
		text := strings.TrimSuffix(string(line), "\r")
		if dropped > 0 {
			text += fmt.Sprintf(" … [%d bytes cut]", dropped)
		}
		buf.Append(text)
		line = line[:0]
		dropped = 0
	}
	for {
		chunk, err := reader.ReadSlice('\n')
		data := chunk
		complete := err == nil
		if complete {
			data = chunk[:len(chunk)-1]
		}
		if room := maxGatewayLogLine - len(line); room > 0 {
			if len(data) > room {
				dropped += len(data) - room
				data = data[:room]
			}
			line = append(line, data...)
		} else {
			dropped += len(data)
		}
		if complete {
			flush()
			continue
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		// EOF or a closed pipe: keep what the last line had.
		if len(line) > 0 || dropped > 0 {
			flush()
		}
		return
	}
}
