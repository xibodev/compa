package api

import (
	"errors"
	"fmt"

	"github.com/xibodev/compa/v3/pkg/config"
)

// errConfigUnchanged lets an updateConfig change report that it left the
// config as it was, so nothing is saved.
var errConfigUnchanged = errors.New("config unchanged")

// updateConfig is the one load-modify-save cycle for config.json in the
// launcher: it holds configMu from the load to the save, so two writers
// cannot each save over the other's change. A change that returns an error
// saves nothing; errConfigUnchanged is not reported as an error.
//
// Lock order: gatewayLifecycleMu, then gateway.mu, then configMu. Nothing
// may wait on network I/O while it holds configMu.
func (h *Handler) updateConfig(change func(cfg *config.Config) error) (*config.Config, error) {
	h.configMu.Lock()
	defer h.configMu.Unlock()
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	if err := change(cfg); err != nil {
		if errors.Is(err, errConfigUnchanged) {
			return cfg, nil
		}
		return nil, err
	}
	if err := config.SaveConfig(h.configPath, cfg); err != nil {
		return nil, fmt.Errorf("failed to save config: %w", err)
	}
	return cfg, nil
}
