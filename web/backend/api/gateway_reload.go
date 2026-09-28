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

	"github.com/xibodev/compa/pkg/config"
	"github.com/xibodev/compa/pkg/logger"
	ppid "github.com/xibodev/compa/pkg/pid"
)

// gatewayReloadTimeout bounds the wait for the gateway to apply a reloaded
// config: it rebuilds its agents and restarts the services that changed.
const gatewayReloadTimeout = 30 * time.Second

// modelApplyMu serializes applying model changes, so a change saved while
// another is applied is applied after it rather than refused.
var modelApplyMu sync.Mutex

// ApplyModelChanges wraps next so that a request that changes the model
// selections the gateway runs with - the default model, the image or light
// model, an agent's model - takes effect in the running gateway before its
// answer is sent: the gateway reloads its config, as a restart would apply
// it, without restarting. Other config changes keep waiting for a restart.
func (h *Handler) ApplyModelChanges(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		if mayChangeConfig(r) {
			h.applyModelSelections()
		}
	})
}

// mayChangeConfig reports whether r is an API request that can save the
// config: one that is not a read and does not drive the gateway itself.
func mayChangeConfig(r *http.Request) bool {
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		return false
	}
	path := r.URL.Path
	return strings.HasPrefix(path, "/api/") && !strings.HasPrefix(path, "/api/gateway/")
}

// applyModelSelections reloads the running gateway's config when its saved
// model selections differ from the ones the gateway applied, and records
// that the gateway now runs the saved config. It never starts a gateway.
func (h *Handler) applyModelSelections() {
	modelApplyMu.Lock()
	defer modelApplyMu.Unlock()

	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		return
	}
	saved := modelSelectionSignature(cfg)
	gateway.mu.Lock()
	applied := gateway.bootModelSignature
	var pidData *ppid.PidFileData
	if gateway.pidData != nil {
		copied := *gateway.pidData
		pidData = &copied
	}
	running := gateway.runtimeStatus == "running"
	gateway.mu.Unlock()
	if !running || pidData == nil || applied == "" || applied == saved {
		return
	}

	if err := h.reloadGateway(pidData, cfg); err != nil {
		logger.WarnC("gateway", fmt.Sprintf("The new model selection could not be applied without a restart: %v", err))
		return
	}
	gateway.mu.Lock()
	if gateway.pidData != nil && gateway.pidData.PID == pidData.PID {
		gateway.bootDefaultModel = strings.TrimSpace(cfg.Agents.Defaults.GetModelName())
		gateway.bootConfigSignature = computeConfigSignature(cfg)
		gateway.bootModelSignature = saved
	}
	gateway.mu.Unlock()
	logger.InfoC("gateway", "Applied the new model selection to the running gateway")
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
