//go:build paused_channels

package api

import (
	"net/http"
	"sync"
)

// pausedChannelFlows holds the QR login flows of WeChat and WeCom, paused
// channels that only builds made with the paused_channels build tag include.
type pausedChannelFlows struct {
	weixinMu    sync.Mutex
	weixinFlows map[string]*weixinFlow
	wecomMu     sync.Mutex
	wecomFlows  map[string]*wecomFlow
}

func (h *Handler) initPausedChannelFlows() {
	h.weixinFlows = make(map[string]*weixinFlow)
	h.wecomFlows = make(map[string]*wecomFlow)
}

// registerPausedChannelRoutes binds the WeChat and WeCom QR login endpoints.
func (h *Handler) registerPausedChannelRoutes(mux *http.ServeMux) {
	h.registerWeixinRoutes(mux)
	h.registerWecomRoutes(mux)
}
