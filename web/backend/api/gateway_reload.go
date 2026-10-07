package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/xibodev/compa/v3/pkg/config"
	"github.com/xibodev/compa/v3/pkg/logger"
	ppid "github.com/xibodev/compa/v3/pkg/pid"
)

// gatewayReloadTimeout bounds the wait for the gateway to apply a reloaded
// config. Its /reload answers once it has: after a reload in progress, it
// rebuilds its agents and restarts the services that changed. A wait this
// long means the reload is stuck.
const gatewayReloadTimeout = 2 * time.Minute

// liveApplyMu serializes applying saved changes, so a change saved while
// another is applied is applied after it rather than refused.
var liveApplyMu sync.Mutex

// liveConfigRoutes are the API families whose writes can change what takes
// effect in the running gateway without a restart: a model selection (the
// default, image, light or an agent's model, directly or by dropping targets
// a provider no longer serves), the approval policy, or a channel's access
// lists and policies (PATCH /api/config).
var liveConfigRoutes = []string{
	"/api/config",
	"/api/default-model",
	"/api/provider-instances",
	"/api/model-routes",
	"/api/active-models",
	"/api/credentials",
	"/api/extension",
}

// ApplyLiveChanges wraps next so that a request that changes what the
// running gateway applies without a restart - the model selections, the
// approval policy (tools.approval), a channel's allow_from, dm_policy or
// group_policy - takes effect in it: once the request was answered, the
// gateway reloads its config, as a restart would apply it, without
// restarting (see applyLiveConfig). Other config changes keep waiting for a
// restart. Only successful writes to liveConfigRoutes trigger it, and the
// reload runs in the background, so no request waits for the gateway.
func (h *Handler) ApplyLiveChanges(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !mayChangeLiveConfig(r) {
			next.ServeHTTP(w, r)
			return
		}
		// Pending from before the save: the client reads the gateway status
		// as soon as the response reached it, which can be before next
		// returns.
		h.pendingLiveApplies.Add(1)
		defer h.pendingLiveApplies.Add(-1)
		status := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(status, r)
		if status.status < http.StatusBadRequest {
			h.scheduleLiveApply()
		}
	})
}

// mayChangeLiveConfig reports whether r is a write to an API that can change
// what the gateway applies without a restart.
func mayChangeLiveConfig(r *http.Request) bool {
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		return false
	}
	path := r.URL.Path
	for _, route := range liveConfigRoutes {
		if path == route || strings.HasPrefix(path, route+"/") {
			return true
		}
	}
	return false
}

// scheduleLiveApply applies the saved config's live changes in the
// background. The apply is pending from now until applyLiveConfig returned.
func (h *Handler) scheduleLiveApply() {
	h.pendingLiveApplies.Add(1)
	h.liveApplies.Add(1)
	go func() {
		defer h.liveApplies.Done()
		defer h.pendingLiveApplies.Add(-1)
		h.applyLiveConfig()
	}()
}

// waitForLiveApplies waits until every scheduled apply finished.
func (h *Handler) waitForLiveApplies() {
	h.liveApplies.Wait()
}

// statusRecorder remembers the status a handler answered with.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wroteHeader {
		s.status = code
		s.wroteHeader = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	s.wroteHeader = true
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (s *statusRecorder) Unwrap() http.ResponseWriter {
	return s.ResponseWriter
}

// applyLiveConfig reloads the running gateway's config when the saved
// config's live parts - the model selections, the approval policy, the
// channels' access lists and policies - differ from those the gateway
// applied (see configSignature.liveEqual), and records what the reload
// applied. A reload that fails leaves the restart to do. It never starts a
// gateway.
func (h *Handler) applyLiveConfig() {
	liveApplyMu.Lock()
	defer liveApplyMu.Unlock()

	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		return
	}
	saved := computeConfigSignature(cfg)
	gateway.mu.Lock()
	applied := gateway.bootConfig
	pidData := copyPidData(gateway.pidData)
	running := gateway.runtimeStatus == "running"
	gateway.mu.Unlock()
	if !running || pidData == nil || len(applied) == 0 || saved.liveEqual(applied) {
		return
	}

	if err := h.reloadGateway(pidData, cfg); err != nil {
		logger.WarnC("gateway", fmt.Sprintf("The saved changes could not be applied without a restart: %v", err))
		return
	}
	gateway.mu.Lock()
	if gateway.pidData != nil && gateway.pidData.PID == pidData.PID {
		// A reload rebuilds the agents and their tools from the saved config
		// and restarts each channel whose config, secrets included, changed:
		// the gateway runs the saved config now.
		h.recordAppliedConfigLocked(cfg, saved)
	}
	gateway.mu.Unlock()
	logger.InfoC("gateway", "Applied the saved changes to the running gateway")
}

// recordAppliedConfigLocked records that the running gateway applied cfg,
// whose signature is signature: the default model it boots turns with, its
// config, and the web chat token it accepts. The caller holds gateway.mu.
func (h *Handler) recordAppliedConfigLocked(cfg *config.Config, signature configSignature) {
	gateway.bootDefaultModel = strings.TrimSpace(cfg.Agents.Defaults.GetModelName())
	gateway.bootConfig = signature
	refreshWebChatTokenLocked(h.configPath)
}

// reloadGateway asks the gateway pidData describes to reload its config and
// waits until it has: the gateway's /reload answers once the reload is done.
func (h *Handler) reloadGateway(pidData *ppid.PidFileData, cfg *config.Config) error {
	request, err := http.NewRequest(http.MethodPost, gatewayBaseURLForPidData(h, pidData, cfg)+"/reload", http.NoBody)
	if err != nil {
		return err
	}
	if token := strings.TrimSpace(pidData.Token); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{
		Timeout:       gatewayReloadTimeout,
		Transport:     &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("reach the gateway: %w", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4<<10))
	if response.StatusCode == http.StatusOK {
		return nil
	}
	var answer struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(bytes.TrimSpace(body), &answer) == nil && answer.Error != "" {
		return fmt.Errorf("the gateway refused the reload: %s", answer.Error)
	}
	return fmt.Errorf("the gateway answered the reload with HTTP %d", response.StatusCode)
}
