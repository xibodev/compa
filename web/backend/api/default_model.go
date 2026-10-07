package api

// The default model is the selection agents run on unless their own model or
// a chat names another: an exact target "instance-id/model-id" or the name
// of a model route, saved as agents.defaults.model_name. Empty selects no
// model; the gateway still starts, and chat reports that no model is
// selected.

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/xibodev/compa/v3/pkg/config"
	"github.com/xibodev/compa/v3/pkg/modelservice"
)

// defaultModelResponse is the body of GET and PUT /api/default-model.
type defaultModelResponse struct {
	Selection string `json:"selection"`
}

func (h *Handler) registerDefaultModelRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/default-model", h.handleGetDefaultModel)
	mux.HandleFunc("PUT /api/default-model", h.handlePutDefaultModel)
}

// handleGetDefaultModel returns the default model selection, "" when none
// is selected.
//
//	GET /api/default-model
func (h *Handler) handleGetDefaultModel(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, defaultModelResponse{Selection: strings.TrimSpace(cfg.Agents.Defaults.GetModelName())})
}

// handlePutDefaultModel sets the default model selection. "" clears it; any
// other selection must resolve against the config and the saved catalogs —
// its instance exists and is enabled and its catalog holds the model, or the
// route exists — and is refused otherwise.
//
//	PUT /api/default-model
func (h *Handler) handlePutDefaultModel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Selection *string `json:"selection"`
	}
	if err := decodeStrictJSON(r, &req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}
	if req.Selection == nil {
		http.Error(w, `selection is required; send "" to clear the default model`, http.StatusBadRequest)
		return
	}
	selection := strings.TrimSpace(*req.Selection)

	h.configMu.Lock()
	defer h.configMu.Unlock()
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}
	if err := h.modelResolver.Check(cfg, selection); err != nil {
		http.Error(w, fmt.Sprintf("Invalid default model %q: %v", selection, err), http.StatusBadRequest)
		return
	}
	// Routes only hold chat models (checked when saved); an exact target must
	// serve chat too, since the default model answers chat turns.
	if _, parseErr := config.ParseExactModelTarget(selection); selection != "" && parseErr == nil {
		store, loadErr := loadCatalogs()
		if loadErr != nil {
			http.Error(w, fmt.Sprintf("Failed to load catalogs: %v", loadErr), http.StatusInternalServerError)
			return
		}
		for _, target := range providerTargetResponses(cfg, store) {
			if target.Target == selection && !modelservice.ServesChat(target.Surfaces) {
				http.Error(w, fmt.Sprintf("Invalid default model %q: it does not serve chat (its catalog lists %s)", selection, strings.Join(target.Surfaces, ", ")), http.StatusBadRequest)
				return
			}
		}
	}
	cfg.Agents.Defaults.ModelName = selection
	if err := config.SaveConfig(h.configPath, cfg); err != nil {
		http.Error(w, fmt.Sprintf("Failed to save config: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, defaultModelResponse{Selection: selection})
}

// checkChangedSelections checks each model selection cfg sets to a value
// previous does not hold: like PUT /api/default-model, it must resolve
// against cfg and the saved catalogs. A selection is checked when it is set,
// so one left dangling by a provider change never blocks an unrelated edit.
func (h *Handler) checkChangedSelections(previous, cfg *config.Config) []string {
	held := make(map[string]string)
	for _, selection := range config.ModelSelections(previous) {
		held[selection.Key] = selection.Value
	}
	var errs []string
	for _, selection := range config.ModelSelections(cfg) {
		if value, ok := held[selection.Key]; ok && value == selection.Value {
			continue
		}
		if err := h.modelResolver.Check(cfg, selection.Value); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", selection.Path, err))
		}
	}
	return errs
}
