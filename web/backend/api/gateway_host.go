package api

import (
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/xibodev/compa/v3/pkg/config"
	"github.com/xibodev/compa/v3/pkg/netbind"
)

// effectiveGatewayBindHost is the host the kernel binds: its own config's
// gateway.host. The launcher no longer widens it to its own listen host in
// LAN mode: the kernel stays on loopback unless its config says otherwise,
// and the dashboard proxies everything a browser needs from it.
func (h *Handler) effectiveGatewayBindHost(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return strings.TrimSpace(cfg.Gateway.Host)
}

func gatewayProbeHost(bindHost string) string {
	plan, err := netbind.BuildPlan(bindHost, netbind.DefaultLoopback)
	if err != nil || strings.TrimSpace(plan.ProbeHost) == "" {
		return netbind.ResolveAdaptiveLoopbackHost()
	}
	return plan.ProbeHost
}

// gatewayProxyURL is where the web chat proxy sends requests: the running
// kernel's address from its PID file, so a port saved in the config but not
// yet applied by a restart does not break chat. Without a PID file the config
// decides.
func (h *Handler) gatewayProxyURL() *url.URL {
	gateway.mu.Lock()
	pidData := copyPidData(gateway.pidData)
	gateway.mu.Unlock()

	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		cfg = nil
	}
	if pidData != nil && pidData.Port > 0 {
		if target, parseErr := url.Parse(gatewayBaseURLForPidData(h, pidData, cfg)); parseErr == nil {
			return target
		}
	}

	port := 18790
	bindHost := ""
	if cfg != nil {
		if cfg.Gateway.Port != 0 {
			port = cfg.Gateway.Port
		}
		bindHost = h.effectiveGatewayBindHost(cfg)
	}

	return &url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(gatewayProbeHost(bindHost), strconv.Itoa(port)),
	}
}
