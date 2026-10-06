package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"

	"github.com/xibodev/compa/v2/pkg/auth"
	"github.com/xibodev/compa/v2/pkg/config"
	"github.com/xibodev/compa/v2/pkg/modelservice"
)

// ProviderCatalogSyncInput is the complete server-owned connection description
// supplied to a catalog driver. It is never populated from request fields.
type ProviderCatalogSyncInput = modelservice.ProviderCatalogSyncInput

// providerInstanceResponse is the public view of a provider instance: its
// credential reference, headers and settings are write-only, so only whether
// they are set shows. Runtime is the instance's request settings, omitted
// when it runs with the provider defaults.
type providerInstanceResponse struct {
	ID string `json:"id"`
	// DisplayName is the name to show for the instance: an extension
	// instance's provider as the daemon names it, the core registry's label
	// of a registry provider kind, else the id.
	DisplayName string `json:"display_name"`
	// ManagedBy is "extension" for an instance that mirrors a provider the
	// extension daemon serves, else "".
	ManagedBy    string `json:"managed_by"`
	ProviderKind string `json:"provider_kind"`
	Adapter      string `json:"adapter"`
	Protocol     string `json:"protocol"`
	Endpoint     string `json:"endpoint,omitempty"`
	// CredentialKind is what the instance signs in with: "none", "api_key",
	// "token" or "oauth".
	CredentialKind string `json:"credential_kind"`
	// CredentialReady reports that the credential is in place - stored or
	// signed in - or that none is needed.
	CredentialReady bool                            `json:"credential_ready"`
	AuthConfigured  bool                            `json:"auth_configured"`
	HeaderNames     []string                        `json:"header_names"`
	SettingNames    []string                        `json:"setting_names"`
	Runtime         *config.ProviderInstanceRuntime `json:"runtime,omitempty"`
	State           config.ProviderInstanceState    `json:"state"`
}

type providerInstanceWriteOnly struct {
	AuthConnectionRef *string            `json:"auth_connection_ref,omitempty"`
	APIKey            *string            `json:"api_key,omitempty"`
	Headers           *map[string]string `json:"headers,omitempty"`
	Settings          *map[string]any    `json:"settings,omitempty"`
}

// providerInstanceRequestBody creates or updates a provider instance.
// Runtime sets the instance's request settings: {} resets them to the
// provider defaults, and an update that omits runtime keeps them.
type providerInstanceRequestBody struct {
	ID            string                          `json:"id"`
	ProviderKind  string                          `json:"provider_kind"`
	Adapter       string                          `json:"adapter"`
	Protocol      string                          `json:"protocol"`
	Endpoint      string                          `json:"endpoint,omitempty"`
	Runtime       *config.ProviderInstanceRuntime `json:"runtime,omitempty"`
	State         config.ProviderInstanceState    `json:"state"`
	WriteOnly     *providerInstanceWriteOnly      `json:"write_only,omitempty"`
	ClearAuth     bool                            `json:"clear_auth_connection,omitempty"`
	ClearHeaders  bool                            `json:"clear_headers,omitempty"`
	ClearSettings bool                            `json:"clear_settings,omitempty"`
}

func (h *Handler) registerProviderInstanceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/provider-instances", h.handleListProviderInstances)
	mux.HandleFunc("POST /api/provider-instances", h.handleCreateProviderInstance)
	mux.HandleFunc("POST /api/provider-instances/auto-connect-free", h.handleAutoConnectFreeProviders)
	mux.HandleFunc("PUT /api/provider-instances/{id}", h.handleUpdateProviderInstance)
	mux.HandleFunc("DELETE /api/provider-instances/{id}", h.handleDeleteProviderInstance)
	mux.HandleFunc("POST /api/provider-instances/{id}/catalog/sync", h.handleSyncProviderInstanceCatalog)
	mux.HandleFunc("POST /api/provider-instances/{id}/ping", h.handlePingProviderInstance)
	mux.HandleFunc("GET /api/provider-instances/catalogs", h.handleListProviderInstanceCatalogs)
	mux.HandleFunc("GET /api/provider-targets", h.handleListProviderTargets)
	mux.HandleFunc("GET /api/provider-roster", h.handleListProviderRoster)
	h.registerProviderRouteRoutes(mux)
}

func decodeStrictJSON(r *http.Request, dst any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("request must contain one JSON value")
		}
		return err
	}
	return nil
}

func (h *Handler) handleListProviderInstances(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}
	instances := make([]providerInstanceResponse, 0, len(cfg.ProviderInstances))
	for _, instance := range cfg.ProviderInstances {
		if instance != nil {
			instances = append(instances, h.providerInstanceView(instance))
		}
	}
	sort.Slice(instances, func(i, j int) bool { return instances[i].ID < instances[j].ID })
	writeJSON(w, http.StatusOK, map[string]any{"instances": instances, "total": len(instances)})
}

func (h *Handler) handleCreateProviderInstance(w http.ResponseWriter, r *http.Request) {
	var request providerInstanceRequestBody
	if err := decodeStrictJSON(r, &request); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}
	instance, err := providerInstanceFromCreateRequest(request)
	if err != nil {
		http.Error(w, fmt.Sprintf("Validation error: %v", err), http.StatusBadRequest)
		return
	}
	var pendingCredential *auth.AuthCredential
	credentialKey := "provider-" + instance.ID
	if request.WriteOnly != nil && request.WriteOnly.APIKey != nil && strings.TrimSpace(*request.WriteOnly.APIKey) != "" {
		pendingCredential = &auth.AuthCredential{Provider: instance.ProviderKind, AuthMethod: "api_key", AccessToken: strings.TrimSpace(*request.WriteOnly.APIKey)}
		instance.AuthConnectionRef = "credential:" + credentialKey
	}

	h.configMu.Lock()
	defer h.configMu.Unlock()
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}
	for _, existing := range cfg.ProviderInstances {
		if existing != nil && existing.ID == instance.ID {
			http.Error(w, "provider instance already exists", http.StatusConflict)
			return
		}
	}
	cfg.ProviderInstances = append(cfg.ProviderInstances, instance)
	if err := cfg.ValidateProviderInstances(); err != nil {
		http.Error(w, fmt.Sprintf("Validation error: %v", err), http.StatusBadRequest)
		return
	}
	if err := config.SaveConfig(h.configPath, cfg); err != nil {
		http.Error(w, fmt.Sprintf("Failed to save config: %v", err), http.StatusInternalServerError)
		return
	}
	if pendingCredential != nil {
		if err := auth.SetCredential(credentialKey, pendingCredential); err != nil {
			cfg.ProviderInstances = cfg.ProviderInstances[:len(cfg.ProviderInstances)-1]
			_ = config.SaveConfig(h.configPath, cfg)
			http.Error(w, fmt.Sprintf("Failed to save provider credential: %v", err), http.StatusInternalServerError)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "instance": h.providerInstanceView(instance)})
}

func (h *Handler) handleUpdateProviderInstance(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var request providerInstanceRequestBody
	if err := decodeStrictJSON(r, &request); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}
	if request.ID != id {
		http.Error(w, "provider instance id is immutable and must match the request path", http.StatusBadRequest)
		return
	}
	h.configMu.Lock()
	defer h.configMu.Unlock()
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}
	index := providerInstanceIndex(cfg, id)
	if index < 0 {
		http.Error(w, "provider instance not found", http.StatusNotFound)
		return
	}
	previousInstance := cfg.ProviderInstances[index]
	instance, err := providerInstanceFromUpdateRequest(request, previousInstance)
	if err != nil {
		http.Error(w, fmt.Sprintf("Validation error: %v", err), http.StatusBadRequest)
		return
	}
	var pendingCredential *auth.AuthCredential
	var previousCredential *auth.AuthCredential
	credentialKey := "provider-" + instance.ID
	if request.WriteOnly != nil && request.WriteOnly.APIKey != nil && strings.TrimSpace(*request.WriteOnly.APIKey) != "" {
		previousCredential, _ = auth.GetCredential(credentialKey)
		pendingCredential = &auth.AuthCredential{Provider: instance.ProviderKind, AuthMethod: "api_key", AccessToken: strings.TrimSpace(*request.WriteOnly.APIKey)}
		instance.AuthConnectionRef = "credential:" + credentialKey
	}
	if instance.State == config.ProviderInstanceStateDisabled {
		if routes := routesReferencingInstance(cfg.ModelRoutes, id); len(routes) > 0 {
			http.Error(w, fmt.Sprintf("provider instance %q is referenced by routes: %s", id, strings.Join(routes, ", ")), http.StatusConflict)
			return
		}
	}
	// A failed update restores the config as it was.
	snapshot, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}
	// A new connection invalidates the instance's catalog. Runtime settings
	// change how requests go, not which models the instance serves, so they
	// keep it.
	connectionChanged := pendingCredential != nil || previousInstance.ProviderKind != instance.ProviderKind || previousInstance.Protocol != instance.Protocol || previousInstance.Endpoint != instance.Endpoint || previousInstance.Adapter != instance.Adapter || previousInstance.AuthConnectionRef != instance.AuthConnectionRef || !reflect.DeepEqual(previousInstance.Headers, instance.Headers) || !reflect.DeepEqual(previousInstance.Settings, instance.Settings)
	if connectionChanged || instance.State == config.ProviderInstanceStateDisabled {
		// No active model, route or model selection may keep naming a model
		// of an instance whose catalog is gone or that is disabled.
		modelservice.DropTargets(cfg, func(target config.ExactModelTarget) bool { return target.InstanceID != id })
	}
	cfg.ProviderInstances[index] = instance
	if err := cfg.ValidateProviderInstances(); err != nil {
		http.Error(w, fmt.Sprintf("Validation error: %v", err), http.StatusBadRequest)
		return
	}
	if err := config.SaveConfig(h.configPath, cfg); err != nil {
		http.Error(w, fmt.Sprintf("Failed to save config: %v", err), http.StatusInternalServerError)
		return
	}
	if pendingCredential != nil {
		if err := auth.SetCredential(credentialKey, pendingCredential); err != nil {
			_ = config.SaveConfig(h.configPath, snapshot)
			http.Error(w, fmt.Sprintf("Failed to save provider credential: %v", err), http.StatusInternalServerError)
			return
		}
	}
	if connectionChanged {
		if err := deleteProviderInstanceCatalog(instance.ID); err != nil {
			if pendingCredential != nil {
				if previousCredential != nil {
					_ = auth.SetCredential(credentialKey, previousCredential)
				} else {
					_ = auth.DeleteCredential(credentialKey)
				}
			}
			_ = config.SaveConfig(h.configPath, snapshot)
			http.Error(w, fmt.Sprintf("Failed to invalidate provider catalog: %v", err), http.StatusInternalServerError)
			return
		}
	}
	if previousInstance.AuthConnectionRef == "credential:provider-"+id && instance.AuthConnectionRef != previousInstance.AuthConnectionRef {
		if err := auth.DeleteCredential("provider-" + id); err != nil {
			http.Error(w, fmt.Sprintf("Failed to delete replaced provider credential: %v", err), http.StatusInternalServerError)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "instance": h.providerInstanceView(instance)})
}

func (h *Handler) handleDeleteProviderInstance(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	h.configMu.Lock()
	defer h.configMu.Unlock()
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}
	index := providerInstanceIndex(cfg, id)
	if index < 0 {
		http.Error(w, "provider instance not found", http.StatusNotFound)
		return
	}
	if routes := routesReferencingInstance(cfg.ModelRoutes, id); len(routes) > 0 {
		http.Error(w, fmt.Sprintf("provider instance %q is referenced by routes: %s", id, strings.Join(routes, ", ")), http.StatusConflict)
		return
	}
	// A failed delete restores the config and the catalogs as they were.
	snapshot, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}
	previousCatalogs, err := loadCatalogs()
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load provider catalogs: %v", err), http.StatusInternalServerError)
		return
	}
	restore := func() {
		_ = config.SaveConfig(h.configPath, snapshot)
		_ = saveCatalogs(previousCatalogs)
	}
	removedInstance := cfg.ProviderInstances[index]
	cfg.ProviderInstances = append(cfg.ProviderInstances[:index], cfg.ProviderInstances[index+1:]...)
	modelservice.DropTargets(cfg, func(target config.ExactModelTarget) bool { return target.InstanceID != id })
	if err := cfg.ValidateProviderInstances(); err != nil {
		http.Error(w, fmt.Sprintf("Validation error: %v", err), http.StatusBadRequest)
		return
	}
	if err := config.SaveConfig(h.configPath, cfg); err != nil {
		http.Error(w, fmt.Sprintf("Failed to save config: %v", err), http.StatusInternalServerError)
		return
	}
	if err := deleteProviderInstanceCatalog(id); err != nil {
		restore()
		http.Error(w, fmt.Sprintf("Failed to delete provider catalog: %v", err), http.StatusInternalServerError)
		return
	}
	if removedInstance.AuthConnectionRef == "credential:provider-"+id {
		if err := auth.DeleteCredential("provider-" + id); err != nil {
			restore()
			http.Error(w, fmt.Sprintf("Failed to delete provider credential: %v", err), http.StatusInternalServerError)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) handleSyncProviderInstanceCatalog(w http.ResponseWriter, r *http.Request) {
	h.configMu.Lock()
	defer h.configMu.Unlock()
	var request struct{}
	if err := decodeStrictJSON(r, &request); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request: %v", err), http.StatusBadRequest)
		return
	}
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}
	index := providerInstanceIndex(cfg, r.PathValue("id"))
	if index < 0 {
		http.Error(w, "provider instance not found", http.StatusNotFound)
		return
	}
	instance := cfg.ProviderInstances[index]
	if instance.State == config.ProviderInstanceStateDisabled {
		http.Error(w, "provider instance is disabled", http.StatusConflict)
		return
	}
	if !modelservice.CatalogSyncSupported(instance) {
		http.Error(w, "provider instance adapter does not support catalog sync", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(instance.Endpoint) == "" {
		http.Error(w, "provider instance endpoint is required for catalog sync", http.StatusBadRequest)
		return
	}
	if h.providerCatalogSync == nil {
		http.Error(w, "provider catalog sync driver is not configured", http.StatusNotImplemented)
		return
	}

	input := catalogSyncInputFromInstance(instance)
	if strings.TrimSpace(instance.AuthConnectionRef) != "" {
		if h.providerCredentialResolver == nil {
			http.Error(w, "provider credential resolver is not configured", http.StatusInternalServerError)
			return
		}
		input.Secret, err = h.providerCredentialResolver(instance.AuthConnectionRef)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to resolve provider credential: %v", err), http.StatusBadGateway)
			return
		}
	}
	models, err := h.providerCatalogSync(r.Context(), input)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to sync provider catalog: %v", err), http.StatusBadGateway)
		return
	}
	previousCatalogs, err := loadCatalogs()
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load provider catalogs: %v", err), http.StatusInternalServerError)
		return
	}
	if err := saveProviderInstanceCatalog(instance, models); err != nil {
		http.Error(w, fmt.Sprintf("Failed to save provider catalog: %v", err), http.StatusInternalServerError)
		return
	}
	available := make(map[string]struct{}, len(models))
	for _, model := range models {
		available[strings.TrimSpace(model.ID)] = struct{}{}
	}
	// The instance's models the new catalog no longer holds leave the active
	// models, the routes and the model selections.
	keep := func(target config.ExactModelTarget) bool {
		if target.InstanceID != instance.ID {
			return true
		}
		_, ok := available[target.ModelID]
		return ok
	}
	if dropsTargets(cfg, keep) {
		modelservice.DropTargets(cfg, keep)
		if err := config.SaveConfig(h.configPath, cfg); err != nil {
			_ = saveCatalogs(previousCatalogs)
			http.Error(w, fmt.Sprintf("Failed to reconcile provider references: %v", err), http.StatusInternalServerError)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"instance_id": instance.ID, "models": models, "total": len(models)})
}

// dropsTargets reports whether modelservice.DropTargets(cfg, keep) changes
// cfg: whether keep rejects an exact target among the active models, the
// routes' targets and the model selections. A route, and a selection naming
// it, only goes when one of its targets does.
func dropsTargets(cfg *config.Config, keep func(target config.ExactModelTarget) bool) bool {
	rejected := func(raw string) bool {
		target, err := config.ParseExactModelTarget(raw)
		return err == nil && !keep(target)
	}
	references := append([]string(nil), cfg.ActiveModels...)
	for _, route := range cfg.ModelRoutes {
		if route != nil {
			references = append(references, route.Targets...)
		}
	}
	defaults := cfg.Agents.Defaults
	references = append(references, defaults.ModelName, defaults.ImageModel)
	if defaults.Routing != nil {
		references = append(references, defaults.Routing.LightModel)
	}
	for _, agent := range cfg.Agents.List {
		references = append(references, agent.Model)
	}
	for _, raw := range references {
		if rejected(raw) {
			return true
		}
	}
	return false
}

func (h *Handler) handlePingProviderInstance(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}
	index := providerInstanceIndex(cfg, r.PathValue("id"))
	if index < 0 {
		http.Error(w, "provider instance not found", http.StatusNotFound)
		return
	}
	instance := cfg.ProviderInstances[index]

	secret := ""
	if h.providerCredentialResolver != nil && instance.AuthConnectionRef != "" {
		secret, _ = h.providerCredentialResolver(instance.AuthConnectionRef)
	}
	res := modelservice.PingWithSync(r.Context(), instance, secret, h.providerCatalogHTTPClient, h.providerCatalogSync)
	writeJSON(w, http.StatusOK, res)
}

func (h *Handler) handleAutoConnectFreeProviders(w http.ResponseWriter, r *http.Request) {
	h.configMu.Lock()
	defer h.configMu.Unlock()
	result, err := modelservice.AutoConnectFreeAndSave(r.Context(), h.configPath, h.providerAnonymousVerify)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to auto-connect free providers: %v", err), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                 result.OK,
		"total":              result.Total,
		"catalog_discovered": result.CatalogDiscovered,
		"verified":           result.Verified,
		"instances":          result.Instances,
		"outcomes":           result.Outcomes,
		"default_model":      result.DefaultModel,
	})
}

func providerInstanceIndex(cfg *config.Config, id string) int {
	for i, instance := range cfg.ProviderInstances {
		if instance != nil && instance.ID == id {
			return i
		}
	}
	return -1
}

func catalogSyncInputFromInstance(instance *config.ProviderInstanceConfig) ProviderCatalogSyncInput {
	return modelservice.CatalogSyncInputFromInstance(instance)
}

func (h *Handler) providerInstanceView(instance *config.ProviderInstanceConfig) providerInstanceResponse {
	headerNames := make([]string, 0, len(instance.Headers))
	for name := range instance.Headers {
		headerNames = append(headerNames, name)
	}
	settingNames := make([]string, 0, len(instance.Settings))
	for name := range instance.Settings {
		settingNames = append(settingNames, name)
	}
	sort.Strings(headerNames)
	sort.Strings(settingNames)
	credentialKind, credentialReady := h.providerInstanceCredential(instance)
	return providerInstanceResponse{
		ID:              instance.ID,
		DisplayName:     providerInstanceDisplayName(instance),
		ManagedBy:       providerInstanceManagedBy(instance),
		ProviderKind:    instance.ProviderKind,
		Adapter:         instance.Adapter,
		Protocol:        instance.Protocol,
		Endpoint:        instance.Endpoint,
		CredentialKind:  credentialKind,
		CredentialReady: credentialReady,
		AuthConfigured:  strings.TrimSpace(instance.AuthConnectionRef) != "",
		HeaderNames:     headerNames,
		SettingNames:    settingNames,
		Runtime:         redactProviderInstanceRuntime(instance.Runtime),
		State:           instance.State,
	}
}

func routesReferencingInstance(routes []*config.ModelRouteConfig, instanceID string) []string {
	references := make([]string, 0)
	for _, route := range routes {
		if route == nil {
			continue
		}
		for _, raw := range route.Targets {
			target, err := config.ParseExactModelTarget(raw)
			if err == nil && target.InstanceID == instanceID {
				references = append(references, route.Name)
				break
			}
		}
	}
	sort.Strings(references)
	return references
}

func providerInstanceFromCreateRequest(request providerInstanceRequestBody) (*config.ProviderInstanceConfig, error) {
	if request.ClearAuth || request.ClearHeaders || request.ClearSettings {
		return nil, fmt.Errorf("clear fields are only valid when updating an existing provider instance")
	}
	instance := publicProviderInstanceFromRequest(request)
	if request.WriteOnly != nil {
		if request.WriteOnly.AuthConnectionRef != nil {
			instance.AuthConnectionRef = *request.WriteOnly.AuthConnectionRef
		}
		if request.WriteOnly.Headers != nil {
			instance.Headers = cloneStringValues(*request.WriteOnly.Headers)
		}
		if request.WriteOnly.Settings != nil {
			instance.Settings = cloneAnyValues(*request.WriteOnly.Settings)
		}
	}
	if err := validateProviderInstanceForAPI(instance); err != nil {
		return nil, err
	}
	return instance, nil
}

func providerInstanceFromUpdateRequest(request providerInstanceRequestBody, existing *config.ProviderInstanceConfig) (*config.ProviderInstanceConfig, error) {
	instance := publicProviderInstanceFromRequest(request)
	if request.Runtime == nil {
		instance.Runtime = cloneProviderInstanceRuntime(existing.Runtime)
	} else {
		keepRedactedProxy(instance.Runtime, existing.Runtime)
	}
	instance.AuthConnectionRef = existing.AuthConnectionRef
	instance.Headers = cloneStringValues(existing.Headers)
	instance.Settings = cloneAnyValues(existing.Settings)
	if request.WriteOnly != nil {
		if request.ClearAuth && request.WriteOnly.AuthConnectionRef != nil ||
			request.ClearHeaders && request.WriteOnly.Headers != nil ||
			request.ClearSettings && request.WriteOnly.Settings != nil {
			return nil, fmt.Errorf("a sensitive field cannot be replaced and cleared in the same request")
		}
		if request.WriteOnly.AuthConnectionRef != nil {
			instance.AuthConnectionRef = *request.WriteOnly.AuthConnectionRef
		}
		if request.WriteOnly.Headers != nil {
			instance.Headers = cloneStringValues(*request.WriteOnly.Headers)
		}
		if request.WriteOnly.Settings != nil {
			instance.Settings = cloneAnyValues(*request.WriteOnly.Settings)
		}
	}
	if request.ClearAuth {
		instance.AuthConnectionRef = ""
	}
	if request.ClearHeaders {
		instance.Headers = nil
	}
	if request.ClearSettings {
		instance.Settings = nil
	}
	if err := validateProviderInstanceForAPI(instance); err != nil {
		return nil, err
	}
	return instance, nil
}

// publicProviderInstanceFromRequest returns the instance a request describes
// without its write-only fields.
func publicProviderInstanceFromRequest(request providerInstanceRequestBody) *config.ProviderInstanceConfig {
	return &config.ProviderInstanceConfig{
		ID: request.ID, ProviderKind: request.ProviderKind, Adapter: request.Adapter,
		Protocol: request.Protocol, Endpoint: request.Endpoint,
		Runtime: normalizeProviderInstanceRuntime(request.Runtime), State: request.State,
	}
}

// normalizeProviderInstanceRuntime returns a copy of runtime with its text
// settings trimmed, or nil when every setting is the provider default.
func normalizeProviderInstanceRuntime(runtime *config.ProviderInstanceRuntime) *config.ProviderInstanceRuntime {
	normalized := cloneProviderInstanceRuntime(runtime)
	if normalized == nil {
		return nil
	}
	normalized.Proxy = strings.TrimSpace(normalized.Proxy)
	normalized.ThinkingLevel = strings.ToLower(strings.TrimSpace(normalized.ThinkingLevel))
	normalized.MaxTokensField = strings.TrimSpace(normalized.MaxTokensField)
	normalized.ToolSchemaTransform = strings.ToLower(strings.TrimSpace(normalized.ToolSchemaTransform))
	if len(normalized.ExtraBody) == 0 {
		normalized.ExtraBody = nil
	}
	if reflect.DeepEqual(*normalized, config.ProviderInstanceRuntime{}) {
		return nil
	}
	return normalized
}

func cloneProviderInstanceRuntime(runtime *config.ProviderInstanceRuntime) *config.ProviderInstanceRuntime {
	if runtime == nil {
		return nil
	}
	clone := *runtime
	if runtime.Streaming != nil {
		streaming := *runtime.Streaming
		clone.Streaming = &streaming
	}
	clone.ExtraBody = cloneAnyValues(runtime.ExtraBody)
	return &clone
}

func validateProviderInstanceForAPI(instance *config.ProviderInstanceConfig) error {
	if err := instance.Validate(); err != nil {
		return err
	}
	if err := config.SupportedProviderInstanceAdapter(instance); err != nil {
		return err
	}
	endpoint := strings.TrimSpace(instance.Endpoint)
	if endpoint == "" {
		return nil
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("endpoint must be an absolute URL")
	}
	if parsed.User != nil {
		return fmt.Errorf("endpoint must not contain URL userinfo")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return fmt.Errorf("endpoint must not contain a query string")
	}
	return nil
}

func cloneStringValues(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func cloneAnyValues(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	clone := make(map[string]any, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}
