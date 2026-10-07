package model

// StatusResponse describes the launcher: its state, version and uptime. No
// route serves it; the dashboard reads the gateway's state from
// GET /api/gateway/status and the version from GET /api/system/version.
type StatusResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
	Uptime  string `json:"uptime"`
}
