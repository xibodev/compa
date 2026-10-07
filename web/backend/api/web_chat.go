package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"time"

	"github.com/xibodev/compa/v3/pkg/config"
	"github.com/xibodev/compa/v3/pkg/logger"
	ppid "github.com/xibodev/compa/v3/pkg/pid"
	"github.com/xibodev/compa/v3/web/backend/middleware"
)

// registerWebChatRoutes binds the web chat channel proxy endpoints to the ServeMux.
func (h *Handler) registerWebChatRoutes(mux *http.ServeMux) {
	// WebSocket proxy: forward /web/ws to gateway
	// This allows the frontend to connect via the same port as the web UI,
	// avoiding the need to expose extra ports for WebSocket communication.
	mux.HandleFunc("GET /web/ws", h.handleWebSocketProxy())
	mux.HandleFunc("GET /web/media/{id}", h.handleWebChatMediaProxy())
	mux.HandleFunc("HEAD /web/media/{id}", h.handleWebChatMediaProxy())
}

// mediaContentSecurityPolicy is the CSP of a downloaded chat file, as the
// kernel sets it too: opened in the browser, it runs no script and loads
// nothing.
const mediaContentSecurityPolicy = "sandbox; default-src 'none'"

// stripBrowserCredentials removes what the browser sent for the dashboard
// from a request bound for the kernel: the dashboard session cookie, any
// Authorization, and the Origin the dashboard already checked. The kernel
// authenticates the launcher by the web chat token alone.
func stripBrowserCredentials(header http.Header) {
	header.Del("Cookie")
	header.Del("Authorization")
	header.Del("Origin")
}

// createWsProxy creates a reverse proxy to the current gateway WebSocket endpoint.
// The gateway address comes from the running kernel's PID file (see gatewayProxyURL).
func (h *Handler) createWsProxy(origProtocol string, upstreamProtocol string) *httputil.ReverseProxy {
	wsProxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			target := h.gatewayProxyURL()
			r.SetURL(target)
			stripBrowserCredentials(r.Out.Header)
			r.Out.Header.Del(protocolKey)
			if upstreamProtocol != "" {
				r.Out.Header.Set(protocolKey, upstreamProtocol)
			}
		},
		ModifyResponse: func(r *http.Response) error {
			if prot := r.Header.Values(protocolKey); len(prot) > 0 {
				r.Header.Del(protocolKey)
				if origProtocol != "" {
					r.Header.Set(protocolKey, origProtocol)
				}
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			// A browser closing the chat cancels the request; that is a normal
			// disconnect, not a gateway failure.
			if errors.Is(err, context.Canceled) {
				logger.Debugf("Web chat connection closed: %v", err)
				return
			}
			logger.Errorf("Failed to proxy WebSocket: %v", err)
			http.Error(w, "Gateway unavailable", http.StatusBadGateway)
		},
	}
	return wsProxy
}

func (h *Handler) createWebChatHTTPProxy(token string) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			target := h.gatewayProxyURL()
			r.SetURL(target)
			stripBrowserCredentials(r.Out.Header)
			r.Out.Header.Set("Authorization", "Bearer "+token)
		},
		ModifyResponse: func(r *http.Response) error {
			// A file served on the dashboard's origin must not run there,
			// whatever the kernel that sent it says.
			r.Header.Set("X-Content-Type-Options", "nosniff")
			r.Header.Set("Content-Security-Policy", mediaContentSecurityPolicy)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(err, context.Canceled) {
				logger.Debugf("Web chat media request closed: %v", err)
				return
			}
			logger.Errorf("Failed to proxy web chat HTTP request: %v", err)
			http.Error(w, "Gateway unavailable", http.StatusBadGateway)
		},
	}
}

func (h *Handler) gatewayAvailableForProxy() bool {
	gateway.mu.Lock()
	ensureWebChatTokenCachedLocked(h.configPath)
	cachedPID := gateway.pidData
	trackedCmd := gateway.cmd
	gateway.mu.Unlock()

	if pidData := h.sanitizeGatewayPidData(ppid.ReadPidFileWithCheck(globalConfigDir()), nil); pidData != nil {
		gateway.mu.Lock()
		gateway.pidData = pidData
		setGatewayRuntimeStatusLocked("running")
		gateway.mu.Unlock()
		return true
	}

	if cachedPID == nil {
		return false
	}

	if isCmdProcessAliveLocked(trackedCmd) {
		return true
	}

	gateway.mu.Lock()
	if gateway.cmd == trackedCmd {
		gateway.pidData = nil
		setGatewayRuntimeStatusLocked("stopped")
	}
	available := gateway.pidData != nil
	gateway.mu.Unlock()
	return available
}

// handleWebSocketProxy wraps a reverse proxy to handle WebSocket connections.
// It relies on launcher dashboard auth, then injects the raw web chat token
// only on the upstream gateway request.
func (h *Handler) handleWebSocketProxy() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// The session cookie also comes along from other pages on this
		// host; only the dashboard's own page may open the chat.
		if !middleware.SameOriginRequest(r) {
			http.Error(w, "cross-origin request rejected", http.StatusForbidden)
			return
		}
		if !h.gatewayAvailableForProxy() {
			logger.Warnf("Gateway not available for WebSocket proxy")
			http.Error(w, "Gateway not available", http.StatusServiceUnavailable)
			return
		}

		upstreamProtocol := webChatGatewayProtocol()
		if upstreamProtocol == "" {
			logger.Warn("web chat token unavailable for WebSocket proxy")
			http.Error(w, "web chat channel not configured", http.StatusServiceUnavailable)
			return
		}

		var origProtocol string
		if prot := r.Header.Values(protocolKey); len(prot) > 0 {
			origProtocol = prot[0]
		}

		h.createWsProxy(origProtocol, upstreamProtocol).ServeHTTP(w, r)
	}
}

func (h *Handler) handleWebChatMediaProxy() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.gatewayAvailableForProxy() {
			logger.Warnf("Gateway not available for web chat media proxy")
			http.Error(w, "Gateway not available", http.StatusServiceUnavailable)
			return
		}

		gateway.mu.Lock()
		webChatToken := gateway.webChatToken
		gateway.mu.Unlock()

		if webChatToken == "" {
			logger.Warnf("Missing web chat token for media proxy")
			http.Error(w, "Invalid web chat token", http.StatusForbidden)
			return
		}

		// The file gets its own policy (see createWebChatHTTPProxy), not the
		// dashboard page's.
		w.Header().Del("Content-Security-Policy")
		w.Header().Del("X-Content-Type-Options")
		h.createWebChatHTTPProxy(webChatToken).ServeHTTP(w, r)
	}
}

// EnsureWebChatChannel enables the web chat channel with sane defaults if it
// isn't already configured. Returns true when the config was modified.
func (h *Handler) EnsureWebChatChannel() (bool, error) {
	changed := false
	_, err := h.updateConfig(func(cfg *config.Config) error {
		bc := cfg.Channels.GetByType(config.ChannelWeb)
		if bc == nil {
			bc = &config.Channel{Type: config.ChannelWeb}
			cfg.Channels[config.ChannelWeb] = bc
		}

		if !bc.Enabled {
			bc.Enabled = true
			changed = true
		}

		if decoded, err := bc.GetDecoded(); err == nil && decoded != nil {
			if webChatCfg, ok := decoded.(*config.WebChatSettings); ok {
				if webChatCfg.Token.String() == "" {
					webChatCfg.Token = *config.NewSecureString(generateSecureToken())
					changed = true
				}
			}
		}
		if !changed {
			return errConfigUnchanged
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return changed, nil
}

// generateSecureToken creates a random 32-character hex string.
func generateSecureToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Fallback to something pseudo-random if crypto/rand fails
		return fmt.Sprintf("%032x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
