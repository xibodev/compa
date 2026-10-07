package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/xibodev/compa/v3/web/backend/launcherconfig"
)

type launcherConfigPayload struct {
	Port                    int      `json:"port"`
	Public                  bool     `json:"public"`
	AllowedCIDRs            []string `json:"allowed_cidrs"`
	AllowLocalhostBypass    bool     `json:"allow_localhost_bypass"`
	TrustedProxyCIDRs       []string `json:"trusted_proxy_cidrs"`
	AllowedHosts            []string `json:"allowed_hosts"`
	AllowLANWithoutPassword bool     `json:"allow_lan_without_password"`
	RemoteImages            string   `json:"remote_images"`
}

// launcherConfigUpdatePayload is a PUT body. The fields added after 1.0
// are pointers: a client that omits them keeps their saved values.
type launcherConfigUpdatePayload struct {
	Port                    int       `json:"port"`
	Public                  bool      `json:"public"`
	AllowedCIDRs            []string  `json:"allowed_cidrs"`
	AllowLocalhostBypass    *bool     `json:"allow_localhost_bypass"`
	TrustedProxyCIDRs       []string  `json:"trusted_proxy_cidrs"`
	AllowedHosts            *[]string `json:"allowed_hosts"`
	AllowLANWithoutPassword *bool     `json:"allow_lan_without_password"`
	RemoteImages            *string   `json:"remote_images"`
}

func (h *Handler) registerLauncherConfigRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/system/launcher-config", h.handleGetLauncherConfig)
	mux.HandleFunc("PUT /api/system/launcher-config", h.handleUpdateLauncherConfig)
}

func (h *Handler) launcherConfigPath() string {
	return launcherconfig.PathForAppConfig(h.configPath)
}

func (h *Handler) launcherFallbackConfig() launcherconfig.Config {
	port := h.serverPort
	if port <= 0 {
		port = launcherconfig.DefaultPort
	}
	return launcherconfig.Config{
		Port:                 port,
		Public:               h.serverPublic,
		AllowedCIDRs:         append([]string(nil), h.serverCIDRs...),
		AllowLocalhostBypass: h.serverAllowLocalhostBypass,
		TrustedProxyCIDRs:    append([]string(nil), h.serverTrustedProxyCIDRs...),
	}
}

func (h *Handler) loadLauncherConfig() (launcherconfig.Config, error) {
	return launcherconfig.Read(h.launcherConfigPath(), h.launcherFallbackConfig())
}

func launcherConfigResponse(cfg launcherconfig.Config) launcherConfigPayload {
	return launcherConfigPayload{
		Port:                    cfg.Port,
		Public:                  cfg.Public,
		AllowedCIDRs:            append([]string{}, cfg.AllowedCIDRs...),
		AllowLocalhostBypass:    cfg.AllowLocalhostBypass,
		TrustedProxyCIDRs:       append([]string{}, cfg.TrustedProxyCIDRs...),
		AllowedHosts:            append([]string{}, cfg.AllowedHosts...),
		AllowLANWithoutPassword: cfg.AllowLANWithoutPassword,
		RemoteImages:            cfg.EffectiveRemoteImages(),
	}
}

func (h *Handler) handleGetLauncherConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.loadLauncherConfig()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to load launcher config: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, launcherConfigResponse(cfg))
}

func (h *Handler) handleUpdateLauncherConfig(w http.ResponseWriter, r *http.Request) {
	var payload launcherConfigUpdatePayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("Invalid JSON: %v", err))
		return
	}

	// A change Validate refuses is the client's mistake, answered 400; the
	// load-modify-save runs under launcherconfig's file lock.
	var invalid error
	cfg, err := launcherconfig.Update(h.launcherConfigPath(), h.launcherFallbackConfig(), func(cfg *launcherconfig.Config) error {
		cfg.Port = payload.Port
		cfg.Public = payload.Public
		cfg.AllowedCIDRs = append([]string(nil), payload.AllowedCIDRs...)
		if payload.AllowLocalhostBypass != nil {
			cfg.AllowLocalhostBypass = *payload.AllowLocalhostBypass
		}
		cfg.TrustedProxyCIDRs = append([]string(nil), payload.TrustedProxyCIDRs...)
		if payload.AllowedHosts != nil {
			cfg.AllowedHosts = launcherconfig.NormalizeHosts(*payload.AllowedHosts)
		}
		if payload.AllowLANWithoutPassword != nil {
			cfg.AllowLANWithoutPassword = *payload.AllowLANWithoutPassword
		}
		if payload.RemoteImages != nil {
			cfg.RemoteImages = *payload.RemoteImages
		}
		invalid = launcherconfig.Validate(*cfg)
		return invalid
	})
	switch {
	case invalid != nil:
		writeJSONError(w, http.StatusBadRequest, invalid.Error())
		return
	case err != nil:
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to save launcher config: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, launcherConfigResponse(cfg))
}
