package api

// An extension daemon serves providers Compa does not carry itself, over
// llmgw-core's extension protocol. These routes connect Compa to one daemon,
// turn each provider it serves into a provider instance, and store the
// credential each provider needs: nothing, a pasted token, or a sign-in.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/xibodev/llm-provider-auth/tokenstore"
	"github.com/xibodev/llmgw-core/extension"
	"github.com/xibodev/llmgw-core/oauthflow"

	"github.com/xibodev/compa/v4/pkg/auth"
	"github.com/xibodev/compa/v4/pkg/config"
	"github.com/xibodev/compa/v4/pkg/modelservice"
)

const (
	extensionProviderKind = "extension"
	extensionFlowTTL      = 10 * time.Minute
	extensionSyncTimeout  = 30 * time.Second
	// extensionTokenPrefix and extensionSignInPrefix key a provider's pasted
	// token and signed-in credential in the auth store.
	extensionTokenPrefix  = "extension:"
	extensionSignInPrefix = "extension-signin:"
)

// Credential kinds, as the extension protocol names them.
const (
	extensionCredentialNone  = string(extension.CredentialNone)
	extensionCredentialToken = string(extension.CredentialToken)
	extensionCredentialOAuth = string(extension.CredentialOAuth)
)

// extensionSignInMethods are the sign-in methods Compa completes: a device
// code the owner approves elsewhere, or a code the owner pastes back. A
// browser redirect would need a callback the provider knows about.
var extensionSignInMethods = []oauthflow.Method{oauthflow.MethodDevice, oauthflow.MethodManual}

var extensionIDUnsafe = regexp.MustCompile(`[^a-z0-9._-]+`)

type extensionFlow struct {
	provider   string
	instanceID string
	flow       oauthflow.Flow
}

type extensionProviderView struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Credential string   `json:"credential"`
	Methods    []string `json:"methods,omitempty"`
	Supported  bool     `json:"supported"`
	Reason     string   `json:"reason,omitempty"`
	InstanceID string   `json:"instance_id,omitempty"`
	Connected  bool     `json:"connected"`
}

type extensionStatusResponse struct {
	URL       string                  `json:"url,omitempty"`
	HasSecret bool                    `json:"has_secret"`
	Status    string                  `json:"status"`
	Error     string                  `json:"error,omitempty"`
	Version   string                  `json:"version,omitempty"`
	Providers []extensionProviderView `json:"providers"`
}

type extensionFlowResponse struct {
	FlowID                  string `json:"flow_id"`
	Method                  string `json:"method"`
	Status                  string `json:"status"`
	AuthorizationURL        string `json:"authorization_url,omitempty"`
	UserCode                string `json:"user_code,omitempty"`
	VerificationURI         string `json:"verification_uri,omitempty"`
	VerificationURIComplete string `json:"verification_uri_complete,omitempty"`
	IntervalSeconds         int    `json:"interval_seconds,omitempty"`
	ExpiresAt               string `json:"expires_at,omitempty"`
}

func (h *Handler) registerExtensionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/extension", h.handleGetExtension)
	mux.HandleFunc("PUT /api/extension", h.handlePutExtension)
	mux.HandleFunc("DELETE /api/extension", h.handleDeleteExtension)
	mux.HandleFunc("POST /api/extension/providers/{id}/token", h.handleExtensionToken)
	mux.HandleFunc("DELETE /api/extension/providers/{id}/credential", h.handleExtensionSignOut)
	mux.HandleFunc("POST /api/extension/providers/{id}/signin", h.handleExtensionSignInStart)
	mux.HandleFunc("POST /api/extension/signin/{flow}/poll", h.handleExtensionSignInPoll)
	mux.HandleFunc("POST /api/extension/signin/{flow}/complete", h.handleExtensionSignInComplete)
}

func extensionInstanceID(provider string) string {
	id := extensionIDUnsafe.ReplaceAllString(strings.ToLower(strings.TrimSpace(provider)), "-")
	return "ext-" + strings.Trim(id, "-._")
}

func extensionDaemonSecret() (string, error) {
	credential, err := getStoredCredential(auth.ExtensionDaemonKey)
	if err != nil || credential == nil {
		return "", err
	}
	return credential.AccessToken, nil
}

func validateExtensionURL(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", errors.New("the daemon URL must be an http(s) URL such as http://127.0.0.1:18888")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return "", errors.New("the daemon URL must not carry credentials, a query or a fragment")
	}
	return raw, nil
}

func extensionView(info extension.ProviderInfo, cfg *config.Config) extensionProviderView {
	view := extensionProviderView{ID: info.ID, Name: info.Name, Credential: string(info.CredentialKind()), Supported: true}
	if view.Name == "" {
		view.Name = info.ID
	}
	switch {
	case modelservice.ExtensionSurface(info) == "":
		view.Supported, view.Reason = false, "serves no surface llmgw-core defines"
	case view.Credential == "":
		// Without a declared kind there is no telling whether the provider
		// takes a pasted token or no credential at all.
		view.Supported, view.Reason = false, "does not declare the credential it needs"
	}
	if view.Credential == extensionCredentialOAuth {
		for _, method := range info.OAuthMethods {
			if slices.Contains(extensionSignInMethods, method) {
				view.Methods = append(view.Methods, string(method))
			}
		}
		if view.Supported && len(view.Methods) == 0 {
			view.Supported, view.Reason = false, "offers only a sign-in Compa cannot complete"
		}
	}
	if index := providerInstanceIndex(cfg, extensionInstanceID(info.ID)); index >= 0 {
		instance := cfg.ProviderInstances[index]
		view.InstanceID = instance.ID
		view.Connected = instance.State == config.ProviderInstanceStateEnabled
	}
	return view
}

func (h *Handler) handleGetExtension(w http.ResponseWriter, r *http.Request) {
	cfg, err := loadConfigFile(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to load config: %v", err), http.StatusInternalServerError)
		return
	}
	secret, err := extensionDaemonSecret()
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to load credentials: %v", err), http.StatusInternalServerError)
		return
	}
	resp := extensionStatusResponse{Status: "not_configured", HasSecret: secret != "", Providers: []extensionProviderView{}}
	if cfg.Extension == nil || strings.TrimSpace(cfg.Extension.URL) == "" {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp.URL = cfg.Extension.URL
	ctx, cancel := context.WithTimeout(r.Context(), extension.DefaultControlTimeout)
	defer cancel()
	info, err := modelservice.DiscoverExtension(ctx, cfg.Extension.URL, secret)
	if err != nil {
		resp.Status, resp.Error = "unreachable", err.Error()
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp.Status, resp.Version = "connected", info.Version
	for _, provider := range info.Providers {
		resp.Providers = append(resp.Providers, extensionView(provider, cfg))
	}
	writeJSON(w, http.StatusOK, resp)
}

func decodeJSONBody(r *http.Request, target any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return errors.New("failed to read request body")
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("invalid JSON: %v", err)
	}
	return nil
}

func (h *Handler) handlePutExtension(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL        string `json:"url"`
		Secret     string `json:"secret"`
		KeepSecret bool   `json:"keep_secret"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	endpoint, err := validateExtensionURL(req.URL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	secret := strings.TrimSpace(req.Secret)
	if req.KeepSecret {
		// The stored secret only ever goes to the daemon it was saved for,
		// so a changed URL cannot carry it to another host.
		cfg, err := loadConfigFile(h.configPath)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to load config: %v", err), http.StatusInternalServerError)
			return
		}
		if cfg.Extension == nil || cfg.Extension.URL != endpoint {
			http.Error(w, "enter the daemon secret again when changing its URL", http.StatusBadRequest)
			return
		}
		if secret, err = extensionDaemonSecret(); err != nil {
			http.Error(w, fmt.Sprintf("failed to load credentials: %v", err), http.StatusInternalServerError)
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), extension.DefaultControlTimeout)
	defer cancel()
	info, err := modelservice.DiscoverExtension(ctx, endpoint, secret)
	if err != nil {
		http.Error(w, fmt.Sprintf("could not reach the extension daemon: %v", err), http.StatusBadGateway)
		return
	}

	if secret == "" {
		err = deleteStoredCredential(auth.ExtensionDaemonKey)
	} else {
		err = setStoredCredential(auth.ExtensionDaemonKey, &auth.AuthCredential{AccessToken: secret, Provider: auth.ExtensionDaemonKey, AuthMethod: "token"})
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to save the daemon secret: %v", err), http.StatusInternalServerError)
		return
	}
	if err := h.reconcileExtensionInstances(endpoint, info); err != nil {
		http.Error(w, fmt.Sprintf("failed to update providers: %v", err), http.StatusInternalServerError)
		return
	}
	h.handleGetExtension(w, r)
}

// reconcileExtensionInstances records the daemon and keeps one instance per
// provider it serves. Instances start disabled; a provider that needs no
// credential is enabled as soon as its catalog loads. An instance the daemon
// already had keeps its state, headers and runtime settings, and its
// credential while the provider asks for the same kind. The targets of an
// instance that goes from enabled to disabled, and those a newly loaded
// catalog no longer holds, are dropped (see modelservice.DropTargets).
func (h *Handler) reconcileExtensionInstances(endpoint string, info extension.InfoResponse) error {
	h.configMu.Lock()
	defer h.configMu.Unlock()
	cfg, err := loadConfigFile(h.configPath)
	if err != nil {
		return err
	}
	cfg.Extension = &config.ExtensionDaemonConfig{URL: endpoint}

	served := make(map[string]struct{})
	disabled := make(map[string]struct{})
	var keyless []*config.ProviderInstanceConfig
	for _, provider := range info.Providers {
		view := extensionView(provider, cfg)
		if !view.Supported {
			continue
		}
		id := extensionInstanceID(provider.ID)
		served[id] = struct{}{}
		instance := &config.ProviderInstanceConfig{
			ID: id, ProviderKind: extensionProviderKind, Adapter: config.ProviderAdapterExtension,
			Protocol: modelservice.ExtensionSurface(provider), Endpoint: endpoint,
			Settings: map[string]any{
				config.ExtensionProviderSetting:    provider.ID,
				config.ExtensionCredentialSetting:  view.Credential,
				config.ExtensionDisplayNameSetting: view.Name,
				config.ExtensionSurfacesSetting:    modelservice.ExtensionSurfaces(provider),
			},
			State: config.ProviderInstanceStateDisabled,
		}
		if index := providerInstanceIndex(cfg, id); index >= 0 {
			existing := cfg.ProviderInstances[index]
			instance.State = existing.State
			instance.Headers = existing.Headers
			instance.Runtime = existing.Runtime
			if previous, _ := existing.Settings[config.ExtensionCredentialSetting].(string); previous == view.Credential {
				instance.AuthConnectionRef = existing.AuthConnectionRef
				if key, ok := existing.Settings[config.ExtensionCredentialKeySetting]; ok {
					instance.Settings[config.ExtensionCredentialKeySetting] = key
				}
			} else {
				// The provider now asks for another kind of credential, so
				// the stored one no longer signs it in.
				instance.State = config.ProviderInstanceStateDisabled
				if existing.State == config.ProviderInstanceStateEnabled {
					disabled[id] = struct{}{}
				}
			}
			cfg.ProviderInstances[index] = instance
		} else {
			cfg.ProviderInstances = append(cfg.ProviderInstances, instance)
		}
		if view.Credential == extensionCredentialNone && instance.State != config.ProviderInstanceStateEnabled {
			keyless = append(keyless, instance)
		}
	}
	// A provider the daemon no longer serves stays configured but disabled.
	for _, instance := range cfg.ProviderInstances {
		if instance == nil || instance.ExtensionProvider() == "" {
			continue
		}
		if _, ok := served[instance.ID]; !ok && instance.State == config.ProviderInstanceStateEnabled {
			instance.State = config.ProviderInstanceStateDisabled
			disabled[instance.ID] = struct{}{}
		}
	}
	// A catalog loaded now replaces the instance's catalog, whose models
	// the instance may no longer serve.
	loaded := make(map[string]map[string]struct{})
	for _, instance := range keyless {
		// A provider whose catalog fails stays disabled; the owner can
		// retry from the provider list.
		if models, syncErr := h.syncExtensionCatalog(instance, ""); syncErr == nil && len(models) > 0 {
			if err := saveProviderInstanceCatalog(instance, models); err != nil {
				return err
			}
			instance.State = config.ProviderInstanceStateEnabled
			delete(disabled, instance.ID)
			available := make(map[string]struct{}, len(models))
			for _, model := range models {
				available[strings.TrimSpace(model.ID)] = struct{}{}
			}
			loaded[instance.ID] = available
		}
	}
	modelservice.DropTargets(cfg, func(target config.ExactModelTarget) bool {
		if _, drop := disabled[target.InstanceID]; drop {
			return false
		}
		if available, ok := loaded[target.InstanceID]; ok {
			_, served := available[target.ModelID]
			return served
		}
		return true
	})
	return saveConfigFile(h.configPath, cfg)
}

func (h *Handler) syncExtensionCatalog(instance *config.ProviderInstanceConfig, token string) ([]CatalogModel, error) {
	input := catalogSyncInputFromInstance(instance)
	input.Secret = token
	ctx, cancel := context.WithTimeout(context.Background(), extensionSyncTimeout)
	defer cancel()
	models, err := h.providerCatalogSync(ctx, input)
	if err != nil {
		return nil, err
	}
	if len(models) == 0 {
		return nil, errors.New("the provider returned no models")
	}
	return models, nil
}

func (h *Handler) handleDeleteExtension(w http.ResponseWriter, r *http.Request) {
	h.configMu.Lock()
	cfg, err := loadConfigFile(h.configPath)
	if err != nil {
		h.configMu.Unlock()
		http.Error(w, fmt.Sprintf("failed to load config: %v", err), http.StatusInternalServerError)
		return
	}
	cfg.Extension = nil
	disabled := make(map[string]struct{})
	for _, instance := range cfg.ProviderInstances {
		if instance != nil && instance.ExtensionProvider() != "" {
			instance.State = config.ProviderInstanceStateDisabled
			disabled[instance.ID] = struct{}{}
		}
	}
	modelservice.DropTargets(cfg, func(target config.ExactModelTarget) bool {
		_, drop := disabled[target.InstanceID]
		return !drop
	})
	err = saveConfigFile(h.configPath, cfg)
	h.configMu.Unlock()
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to save config: %v", err), http.StatusInternalServerError)
		return
	}
	for id := range disabled {
		_ = deleteProviderInstanceCatalog(id)
	}
	if err := deleteStoredCredential(auth.ExtensionDaemonKey); err != nil {
		http.Error(w, fmt.Sprintf("failed to delete the daemon secret: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// extensionInstanceFor returns the instance of a daemon provider id.
func (h *Handler) extensionInstanceFor(providerID string) (*config.ProviderInstanceConfig, error) {
	cfg, err := loadConfigFile(h.configPath)
	if err != nil {
		return nil, err
	}
	index := providerInstanceIndex(cfg, extensionInstanceID(providerID))
	if index < 0 || cfg.ProviderInstances[index].ExtensionProvider() == "" {
		return nil, errNotFound
	}
	return cfg.ProviderInstances[index], nil
}

var errNotFound = errors.New("provider not found; reconnect the extension daemon")

// enableExtensionInstance applies credential to the stored instance, loads its
// catalog, and enables it.
func (h *Handler) enableExtensionInstance(instanceID string, token string, apply func(*config.ProviderInstanceConfig)) error {
	h.configMu.Lock()
	defer h.configMu.Unlock()
	cfg, err := loadConfigFile(h.configPath)
	if err != nil {
		return err
	}
	index := providerInstanceIndex(cfg, instanceID)
	if index < 0 {
		return errNotFound
	}
	instance := cloneProviderInstanceForExtension(cfg.ProviderInstances[index])
	apply(instance)
	models, err := h.syncExtensionCatalog(instance, token)
	if err != nil {
		return err
	}
	instance.State = config.ProviderInstanceStateEnabled
	cfg.ProviderInstances[index] = instance
	if err := saveProviderInstanceCatalog(instance, models); err != nil {
		return err
	}
	return saveConfigFile(h.configPath, cfg)
}

func cloneProviderInstanceForExtension(instance *config.ProviderInstanceConfig) *config.ProviderInstanceConfig {
	clone := *instance
	clone.Settings = make(map[string]any, len(instance.Settings))
	for key, value := range instance.Settings {
		clone.Settings[key] = value
	}
	return &clone
}

func (h *Handler) handleExtensionToken(w http.ResponseWriter, r *http.Request) {
	instance, err := h.extensionInstanceFor(r.PathValue("id"))
	if err != nil {
		writeExtensionError(w, err)
		return
	}
	if kind, _ := instance.Settings[config.ExtensionCredentialSetting].(string); kind != extensionCredentialToken {
		http.Error(w, "this provider does not take a pasted token", http.StatusBadRequest)
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	token := strings.TrimSpace(req.Token)
	if token == "" {
		http.Error(w, "token is required", http.StatusBadRequest)
		return
	}
	key := extensionTokenPrefix + instance.ExtensionProvider()
	previous, err := getStoredCredential(key)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to load credentials: %v", err), http.StatusInternalServerError)
		return
	}
	if err := setStoredCredential(key, &auth.AuthCredential{AccessToken: token, Provider: key, AuthMethod: "token"}); err != nil {
		http.Error(w, fmt.Sprintf("failed to save the token: %v", err), http.StatusInternalServerError)
		return
	}
	err = h.enableExtensionInstance(instance.ID, token, func(target *config.ProviderInstanceConfig) {
		target.AuthConnectionRef = "credential:" + key
	})
	if err != nil {
		if previous != nil {
			_ = setStoredCredential(key, previous)
		} else {
			_ = deleteStoredCredential(key)
		}
		http.Error(w, fmt.Sprintf("the provider rejected the token: %v", err), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "instance_id": instance.ID})
}

func (h *Handler) handleExtensionSignOut(w http.ResponseWriter, r *http.Request) {
	instance, err := h.extensionInstanceFor(r.PathValue("id"))
	if err != nil {
		writeExtensionError(w, err)
		return
	}
	provider := instance.ExtensionProvider()
	if err := deleteStoredCredentials(extensionTokenPrefix+provider, extensionSignInPrefix+provider); err != nil {
		http.Error(w, fmt.Sprintf("failed to delete credentials: %v", err), http.StatusInternalServerError)
		return
	}
	h.configMu.Lock()
	cfg, err := loadConfigFile(h.configPath)
	if err == nil {
		if index := providerInstanceIndex(cfg, instance.ID); index >= 0 {
			target := cfg.ProviderInstances[index]
			target.State = config.ProviderInstanceStateDisabled
			target.AuthConnectionRef = ""
			delete(target.Settings, config.ExtensionCredentialKeySetting)
		}
		modelservice.DropTargets(cfg, func(target config.ExactModelTarget) bool { return target.InstanceID != instance.ID })
		err = saveConfigFile(h.configPath, cfg)
	}
	h.configMu.Unlock()
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to update config: %v", err), http.StatusInternalServerError)
		return
	}
	_ = deleteProviderInstanceCatalog(instance.ID)
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func writeExtensionError(w http.ResponseWriter, err error) {
	if errors.Is(err, errNotFound) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

func (h *Handler) extensionSignInDriver(provider string) (*extension.OAuthDriver, error) {
	cfg, err := loadConfigFile(h.configPath)
	if err != nil {
		return nil, err
	}
	if cfg.Extension == nil || cfg.Extension.URL == "" {
		return nil, errors.New("connect the extension daemon first")
	}
	secret, err := extensionDaemonSecret()
	if err != nil {
		return nil, err
	}
	client, err := modelservice.NewExtensionClient(cfg.Extension.URL, secret)
	if err != nil {
		return nil, err
	}
	return client.OAuthDriver(provider), nil
}

func newExtensionFlowID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func (h *Handler) handleExtensionSignInStart(w http.ResponseWriter, r *http.Request) {
	instance, err := h.extensionInstanceFor(r.PathValue("id"))
	if err != nil {
		writeExtensionError(w, err)
		return
	}
	if kind, _ := instance.Settings[config.ExtensionCredentialSetting].(string); kind != extensionCredentialOAuth {
		http.Error(w, "this provider does not use sign-in", http.StatusBadRequest)
		return
	}
	var req struct {
		Method string `json:"method"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	method := oauthflow.Method(strings.TrimSpace(req.Method))
	if !slices.Contains(extensionSignInMethods, method) {
		http.Error(w, fmt.Sprintf("unsupported sign-in method %q", req.Method), http.StatusBadRequest)
		return
	}
	provider := instance.ExtensionProvider()
	driver, err := h.extensionSignInDriver(provider)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), extension.DefaultControlTimeout)
	defer cancel()
	authorization, err := driver.Start(ctx, oauthflow.StartRequest{Instance: instance.ID, Method: method})
	if err != nil {
		http.Error(w, fmt.Sprintf("could not start the sign-in: %v", err), http.StatusBadGateway)
		return
	}
	id, err := newExtensionFlowID()
	if err != nil {
		http.Error(w, "could not start the sign-in", http.StatusInternalServerError)
		return
	}
	ttl := authorization.ExpiresIn
	if ttl <= 0 || ttl > extensionFlowTTL {
		ttl = extensionFlowTTL
	}
	now := time.Now()
	flow := &extensionFlow{provider: provider, instanceID: instance.ID, flow: oauthflow.Flow{
		ID: id, Instance: instance.ID, Method: method, CreatedAt: now, ExpiresAt: now.Add(ttl),
		AuthorizationURL: authorization.AuthorizationURL, UserCode: authorization.UserCode,
		VerificationURI: authorization.VerificationURI, VerificationURIComplete: authorization.VerificationURIComplete,
		Secrets: authorization.Secrets,
	}}
	h.storeExtensionFlow(flow)
	resp := extensionFlowResponse{
		FlowID: id, Method: string(method), Status: string(oauthflow.PollPending),
		AuthorizationURL: authorization.AuthorizationURL, UserCode: authorization.UserCode,
		VerificationURI: authorization.VerificationURI, VerificationURIComplete: authorization.VerificationURIComplete,
		IntervalSeconds: int(authorization.Interval / time.Second), ExpiresAt: flow.flow.ExpiresAt.Format(time.RFC3339),
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) storeExtensionFlow(flow *extensionFlow) {
	h.extensionMu.Lock()
	defer h.extensionMu.Unlock()
	now := time.Now()
	for id, existing := range h.extensionFlows {
		if existing.flow.Expired(now) {
			delete(h.extensionFlows, id)
		}
	}
	h.extensionFlows[flow.flow.ID] = flow
}

func (h *Handler) takeExtensionFlow(id string, consume bool) (*extensionFlow, bool) {
	h.extensionMu.Lock()
	defer h.extensionMu.Unlock()
	flow, ok := h.extensionFlows[id]
	if !ok || flow.flow.Expired(time.Now()) {
		delete(h.extensionFlows, id)
		return nil, false
	}
	if consume {
		delete(h.extensionFlows, id)
	}
	return flow, true
}

func (h *Handler) handleExtensionSignInPoll(w http.ResponseWriter, r *http.Request) {
	flow, ok := h.takeExtensionFlow(r.PathValue("flow"), false)
	if !ok || flow.flow.Method != oauthflow.MethodDevice {
		http.Error(w, "sign-in not found or expired", http.StatusNotFound)
		return
	}
	driver, err := h.extensionSignInDriver(flow.provider)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), extension.DefaultControlTimeout)
	defer cancel()
	result, err := driver.Poll(ctx, flow.flow)
	if err != nil {
		http.Error(w, fmt.Sprintf("sign-in failed: %v", err), http.StatusBadGateway)
		return
	}
	switch result.Status {
	case oauthflow.PollPending, oauthflow.PollSlowDown:
		writeJSON(w, http.StatusOK, extensionFlowResponse{FlowID: flow.flow.ID, Method: string(flow.flow.Method), Status: string(result.Status)})
	case oauthflow.PollApproved:
		h.takeExtensionFlow(flow.flow.ID, true)
		h.finishExtensionSignIn(w, flow, result.Record)
	default:
		h.takeExtensionFlow(flow.flow.ID, true)
		writeJSON(w, http.StatusOK, extensionFlowResponse{FlowID: flow.flow.ID, Method: string(flow.flow.Method), Status: string(result.Status)})
	}
}

// signInCode accepts the code a manual sign-in shows, or the whole redirect
// URL it lands on. A URL's state must match the flow's.
func signInCode(input, state string) (string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", errors.New("paste the code or the page address you were sent to")
	}
	parsed, err := url.Parse(input)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return input, nil
	}
	query := parsed.Query()
	if fragment, fragmentErr := url.ParseQuery(parsed.Fragment); fragmentErr == nil && query.Get("code") == "" {
		query = fragment
	}
	code := query.Get("code")
	if code == "" {
		if reason := query.Get("error"); reason != "" {
			return "", fmt.Errorf("the provider refused the sign-in: %s", reason)
		}
		return "", errors.New("the address holds no sign-in code")
	}
	if got := query.Get("state"); state != "" && got != "" && got != state {
		return "", errors.New("the address belongs to a different sign-in")
	}
	return code, nil
}

func (h *Handler) handleExtensionSignInComplete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	flow, ok := h.takeExtensionFlow(r.PathValue("flow"), false)
	if !ok || flow.flow.Method != oauthflow.MethodManual {
		http.Error(w, "sign-in not found or expired", http.StatusNotFound)
		return
	}
	code, err := signInCode(req.Code, flow.flow.Secrets.State)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	driver, err := h.extensionSignInDriver(flow.provider)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// A code is spent on the first exchange, whatever its outcome.
	h.takeExtensionFlow(flow.flow.ID, true)
	ctx, cancel := context.WithTimeout(r.Context(), extension.DefaultControlTimeout)
	defer cancel()
	record, err := driver.Exchange(ctx, flow.flow, code)
	if err != nil {
		http.Error(w, fmt.Sprintf("sign-in failed: %v", err), http.StatusBadGateway)
		return
	}
	h.finishExtensionSignIn(w, flow, record)
}

var extensionTokenStore = func() *auth.TokenStore { return auth.DefaultTokenStore() }

func (h *Handler) finishExtensionSignIn(w http.ResponseWriter, flow *extensionFlow, record tokenstore.Record) {
	key := extensionSignInPrefix + flow.provider
	store := extensionTokenStore()
	if _, err := store.Save(context.Background(), key, record); err != nil {
		http.Error(w, fmt.Sprintf("failed to save the sign-in: %v", err), http.StatusInternalServerError)
		return
	}
	err := h.enableExtensionInstance(flow.instanceID, "", func(target *config.ProviderInstanceConfig) {
		target.AuthConnectionRef = ""
		target.Settings[config.ExtensionCredentialKeySetting] = key
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("signed in, but the provider's models could not load: %v", err), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, extensionFlowResponse{FlowID: flow.flow.ID, Method: string(flow.flow.Method), Status: string(oauthflow.PollApproved)})
}

// extensionFlowsState is embedded in Handler.
type extensionFlowsState struct {
	extensionMu    sync.Mutex
	extensionFlows map[string]*extensionFlow
}
