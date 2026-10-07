package api

// Stored provider API keys. A key lives in the auth store under its
// provider's name; saving one connects the provider as a provider instance
// whose catalog loads with the key, and deleting it disables that instance.
// modelservice.ConnectAPIKeyProvider and DisconnectAPIKeyProvider do the
// work, so the CLI connects and disconnects a provider the same way.

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/xibodev/compa/v3/pkg/auth"
	"github.com/xibodev/compa/v3/pkg/config"
	"github.com/xibodev/compa/v3/pkg/modelservice"
)

const (
	credentialProviderOpenAI    = modelservice.APIKeyProviderOpenAI
	credentialProviderAnthropic = modelservice.APIKeyProviderAnthropic

	credentialMethodAPIKey = modelservice.APIKeyAuthMethod
)

var credentialProviderOrder = []string{
	credentialProviderOpenAI,
	credentialProviderAnthropic,
}

var credentialProviderLabels = map[string]string{
	credentialProviderOpenAI:    "OpenAI",
	credentialProviderAnthropic: "Anthropic",
}

// Seams over the auth store and the config file, replaced by tests.
var (
	getStoredCredential     = auth.GetCredential
	setStoredCredential     = auth.SetCredential
	deleteStoredCredential  = auth.DeleteCredential
	deleteStoredCredentials = auth.DeleteCredentials
	loadConfigFile          = config.LoadConfig
	saveConfigFile          = config.SaveConfig
)

type credentialProviderStatus struct {
	Provider    string `json:"provider"`
	DisplayName string `json:"display_name"`
	LoggedIn    bool   `json:"logged_in"`
}

// registerCredentialRoutes binds the stored API-key endpoints to the ServeMux.
func (h *Handler) registerCredentialRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/credentials/providers", h.handleListCredentialProviders)
	mux.HandleFunc("POST /api/credentials", h.handleSaveCredential)
	mux.HandleFunc("DELETE /api/credentials/{provider}", h.handleDeleteCredential)
}

func (h *Handler) handleListCredentialProviders(w http.ResponseWriter, r *http.Request) {
	providersResp := make([]credentialProviderStatus, 0, len(credentialProviderOrder))
	for _, provider := range credentialProviderOrder {
		cred, err := getStoredCredential(provider)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to load credentials: %v", err), http.StatusInternalServerError)
			return
		}
		providersResp = append(providersResp, credentialProviderStatus{
			Provider:    provider,
			DisplayName: credentialProviderLabels[provider],
			LoggedIn:    cred != nil && strings.TrimSpace(cred.AccessToken) != "",
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": providersResp})
}

func (h *Handler) handleSaveCredential(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider string `json:"provider"`
		Token    string `json:"token"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	provider, err := normalizeCredentialProvider(req.Provider)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	token := strings.TrimSpace(req.Token)
	if token == "" {
		http.Error(w, "token is required", http.StatusBadRequest)
		return
	}
	h.configMu.Lock()
	err = modelservice.ConnectAPIKeyProvider(r.Context(), h.configPath, provider, token, h.credentialConnectDeps())
	h.configMu.Unlock()
	if err != nil {
		http.Error(w, fmt.Sprintf("saving the API key failed: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "provider": provider})
}

func (h *Handler) handleDeleteCredential(w http.ResponseWriter, r *http.Request) {
	provider, err := normalizeCredentialProvider(r.PathValue("provider"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h.configMu.Lock()
	err = modelservice.DisconnectAPIKeyProvider(h.configPath, provider, h.credentialConnectDeps())
	h.configMu.Unlock()
	if err != nil {
		http.Error(w, fmt.Sprintf("removing the API key failed: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "provider": provider})
}

// credentialConnectDeps are the effects of connecting and disconnecting a
// provider: the handler's catalog discovery and the seams above.
func (h *Handler) credentialConnectDeps() modelservice.ConnectDeps {
	return modelservice.ConnectDeps{
		SyncCatalog:      h.providerCatalogSync,
		LoadConfig:       loadConfigFile,
		SaveConfig:       saveConfigFile,
		GetCredential:    getStoredCredential,
		SetCredential:    setStoredCredential,
		DeleteCredential: deleteStoredCredential,
	}
}

func normalizeCredentialProvider(raw string) (string, error) {
	provider := strings.ToLower(strings.TrimSpace(raw))
	switch provider {
	case credentialProviderOpenAI, credentialProviderAnthropic:
		return provider, nil
	default:
		return "", fmt.Errorf("unsupported provider %q", raw)
	}
}
