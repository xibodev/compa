package api

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/xibodev/compa/v3/pkg/modelservice"
	"github.com/xibodev/compa/v3/web/backend/launcherconfig"
)

// Handler serves HTTP API requests.
type Handler struct {
	configPath                 string
	serverPort                 int
	serverPublic               bool
	serverPublicExplicit       bool
	serverHostInput            string
	serverHostExplicit         bool
	serverCIDRs                []string
	serverAllowLocalhostBypass bool
	serverTrustedProxyCIDRs    []string
	debug                      bool
	providerCredentialResolver func(string) (string, error)
	providerCatalogHTTPClient  *http.Client
	providerCatalogSync        func(context.Context, ProviderCatalogSyncInput) ([]CatalogModel, error)
	providerAnonymousVerify    modelservice.AnonymousVerifyFunc
	// modelResolver checks model selections against the config and the
	// saved catalogs.
	modelResolver *modelservice.Resolver
	// Serializes model-related config writes. Other config endpoints still
	// coordinate their own load-modify-save cycles.
	configMu sync.Mutex
	// The QR login flows of the paused WeChat and WeCom channels, in builds
	// that include them.
	pausedChannelFlows
	// liveApplies tracks the background applies of saved changes that
	// ApplyLiveChanges and a pairing approval schedule, so tests can wait
	// for them.
	liveApplies sync.WaitGroup
	// pendingLiveApplies counts the saves and applies whose live changes
	// may not have reached the running gateway yet; while it is not zero,
	// the gateway status leaves the live parts of the config out.
	pendingLiveApplies atomic.Int32
	extensionFlowsState
}

// NewHandler creates an instance of the API handler.
func NewHandler(configPath string) *Handler {
	h := &Handler{
		configPath:                 configPath,
		serverPort:                 launcherconfig.DefaultPort,
		serverAllowLocalhostBypass: launcherconfig.Default().AllowLocalhostBypass,
		extensionFlowsState:        extensionFlowsState{extensionFlows: make(map[string]*extensionFlow)},
	}
	h.initPausedChannelFlows()
	h.providerCredentialResolver = modelservice.ResolveCredentialReference
	h.providerCatalogHTTPClient = http.DefaultClient
	h.providerCatalogSync = h.syncProviderCatalog
	h.modelResolver = modelservice.NewResolver()
	return h
}

// SetServerOptions stores current backend listen options for fallback behavior.
func (h *Handler) SetServerOptions(port int, public bool, publicExplicit bool, allowedCIDRs []string) {
	h.serverPort = port
	h.serverPublic = public
	h.serverPublicExplicit = publicExplicit
	h.serverHostInput = ""
	h.serverHostExplicit = false
	h.serverCIDRs = append([]string(nil), allowedCIDRs...)
}

func (h *Handler) SetServerAccessOptions(allowLocalhostBypass bool, trustedProxyCIDRs []string) {
	h.serverAllowLocalhostBypass = allowLocalhostBypass
	h.serverTrustedProxyCIDRs = append([]string(nil), trustedProxyCIDRs...)
}

// SetServerBindHost stores the launcher's effective bind host.
// When explicit is true, hostInput is the normalized -host / COMPA_LAUNCHER_HOST value.
func (h *Handler) SetServerBindHost(hostInput string, explicit bool) {
	h.serverHostInput = strings.TrimSpace(hostInput)
	if !explicit {
		h.serverHostInput = ""
	}
	h.serverHostExplicit = explicit
}

func (h *Handler) SetDebug(debug bool) {
	h.debug = debug
}

// RegisterRoutes binds all API endpoint handlers to the ServeMux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	// Config CRUD
	h.registerConfigRoutes(mux)

	// Web chat channel (WebSocket and media proxy)
	h.registerWebChatRoutes(mux)

	// Gateway process lifecycle
	h.registerGatewayRoutes(mux)

	// Session history
	h.registerSessionRoutes(mux)

	// Stored provider API keys
	h.registerCredentialRoutes(mux)

	// Provider instances, their catalogs, model routes and the default model
	h.registerProviderInstanceRoutes(mux)
	h.registerDefaultModelRoutes(mux)
	h.registerExtensionRoutes(mux)

	// Voice conversation routes (Cascade STT/TTS & Live mode)
	h.registerVoiceRoutes(mux)

	// Channel catalog (for frontend navigation/config pages)
	h.registerChannelRoutes(mux)

	// Skills and tools support/actions
	h.registerSkillRoutes(mux)
	h.registerToolRoutes(mux)

	// OS startup / launch-at-login
	h.registerStartupRoutes(mux)

	// Launcher service parameters (port/public)
	h.registerLauncherConfigRoutes(mux)

	// Self-update endpoint (requires dashboard auth)
	h.registerUpdateRoutes(mux)

	// Detached module lifecycle: install, remove, inspect, invoke
	h.registerModuleRoutes(mux)

	// Runtime build/version metadata
	h.registerVersionRoutes(mux)

	// WeChat and WeCom QR login flows, in builds with the paused channels
	h.registerPausedChannelRoutes(mux)
}

// Shutdown gracefully shuts down the handler, stopping the gateway if it was started by this handler.
func (h *Handler) Shutdown() {
	h.StopGateway()
}
