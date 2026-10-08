//go:build !paused_channels

package api

import "net/http"

// pausedChannelFlows would hold the QR login flows of WeChat and WeCom,
// paused channels this build leaves out.
type pausedChannelFlows struct{}

func (h *Handler) initPausedChannelFlows() {}

// registerPausedChannelRoutes binds nothing: this build has no paused
// channels to log in to.
func (h *Handler) registerPausedChannelRoutes(*http.ServeMux) {}
