package modelservice

// Connecting a provider with an API key. The key lives in the auth store under
// the provider's name, and the provider's instance authenticates with it
// through auth_connection_ref credential:<provider>, so the web API and the
// CLI connect and disconnect a provider the same way.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/xibodev/compa/pkg/auth"
	"github.com/xibodev/compa/pkg/config"
)

// Providers ConnectAPIKeyProvider connects. Each is also its instance's ID
// and its key's auth store key.
const (
	APIKeyProviderOpenAI    = "openai"
	APIKeyProviderAnthropic = "anthropic"
)

// APIKeyAuthMethod is the auth method of a stored API key.
const APIKeyAuthMethod = "api_key"

// catalogDiscoveryTimeout bounds the catalog request that verifies a key.
const catalogDiscoveryTimeout = 30 * time.Second

// ConnectDeps are the effects of ConnectAPIKeyProvider and
// DisconnectAPIKeyProvider. A nil field uses the default; tests and callers
// with their own seams replace them.
type ConnectDeps struct {
	// SyncCatalog discovers the models a key reaches. Nil uses SyncCatalog.
	SyncCatalog CatalogSyncFunc
	// LoadConfig and SaveConfig read and write the config file. Nil uses
	// config.LoadConfig and config.SaveConfig.
	LoadConfig func(path string) (*config.Config, error)
	SaveConfig func(path string, cfg *config.Config) error
	// GetCredential, SetCredential and DeleteCredential reach the auth
	// store. Nil uses auth.GetCredential, auth.SetCredential and
	// auth.DeleteCredential.
	GetCredential    func(key string) (*auth.AuthCredential, error)
	SetCredential    func(key string, cred *auth.AuthCredential) error
	DeleteCredential func(key string) error
}

func (d ConnectDeps) withDefaults() ConnectDeps {
	if d.SyncCatalog == nil {
		d.SyncCatalog = SyncCatalog
	}
	if d.LoadConfig == nil {
		d.LoadConfig = config.LoadConfig
	}
	if d.SaveConfig == nil {
		d.SaveConfig = config.SaveConfig
	}
	if d.GetCredential == nil {
		d.GetCredential = auth.GetCredential
	}
	if d.SetCredential == nil {
		d.SetCredential = auth.SetCredential
	}
	if d.DeleteCredential == nil {
		d.DeleteCredential = auth.DeleteCredential
	}
	return d
}

// APIKeyProviderInstance returns the provider instance ConnectAPIKeyProvider
// records for provider, authenticating with the key stored under the
// provider's name.
func APIKeyProviderInstance(provider string) (*config.ProviderInstanceConfig, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	instance := &config.ProviderInstanceConfig{
		ID:                provider,
		AuthConnectionRef: "credential:" + provider,
		State:             config.ProviderInstanceStateEnabled,
	}
	switch provider {
	case APIKeyProviderOpenAI:
		instance.ProviderKind = "openai"
		instance.Adapter = config.ProviderAdapterOpenAICompatible
		instance.Protocol = "openai"
		instance.Endpoint = "https://api.openai.com/v1"
	case APIKeyProviderAnthropic:
		instance.ProviderKind = "anthropic"
		instance.Adapter = config.ProviderAdapterAnthropicCompatible
		instance.Protocol = "anthropic-messages"
		instance.Endpoint = "https://api.anthropic.com/v1"
	default:
		return nil, fmt.Errorf("unsupported provider %q", provider)
	}
	return instance, nil
}

// ConnectAPIKeyProvider stores key as provider's API key and connects the
// provider in the config at cfgPath: it discovers the models the key reaches,
// records the provider's instance (see APIKeyProviderInstance; an existing
// instance keeps its headers and runtime settings), saves the catalog, and
// drops targets on the instance that the catalog no longer holds (see
// DropTargets). When the provider cannot connect, the previous key is
// restored, or the new one removed.
func ConnectAPIKeyProvider(ctx context.Context, cfgPath, provider, key string, deps ConnectDeps) error {
	instance, err := APIKeyProviderInstance(provider)
	if err != nil {
		return err
	}
	provider = instance.ID
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("API key is required")
	}
	deps = deps.withDefaults()

	previous, err := deps.GetCredential(provider)
	if err != nil {
		return fmt.Errorf("loading previous credential: %w", err)
	}
	cred := &auth.AuthCredential{AccessToken: key, Provider: provider, AuthMethod: APIKeyAuthMethod}
	if err := deps.SetCredential(provider, cred); err != nil {
		return fmt.Errorf("saving credential: %w", err)
	}
	if err := connectAPIKeyInstance(ctx, cfgPath, instance, key, deps); err != nil {
		if previous != nil {
			_ = deps.SetCredential(provider, previous)
		} else {
			_ = deps.DeleteCredential(provider)
		}
		return fmt.Errorf("connecting provider: %w", err)
	}
	return nil
}

func connectAPIKeyInstance(ctx context.Context, cfgPath string, instance *config.ProviderInstanceConfig, key string, deps ConnectDeps) error {
	cfg, err := deps.LoadConfig(cfgPath)
	if err != nil {
		return err
	}
	index := -1
	for i, existing := range cfg.ProviderInstances {
		if existing != nil && existing.ID == instance.ID {
			index = i
			instance.Headers = cloneHeaders(existing.Headers)
			instance.Runtime = existing.Runtime
			break
		}
	}

	input := CatalogSyncInputFromInstance(instance)
	input.Secret = key
	if ctx == nil {
		ctx = context.Background()
	}
	syncCtx, cancel := context.WithTimeout(ctx, catalogDiscoveryTimeout)
	defer cancel()
	models, err := deps.SyncCatalog(syncCtx, input)
	if err != nil {
		return err
	}
	if len(models) == 0 {
		return errors.New("provider returned no usable models")
	}

	if index >= 0 {
		cfg.ProviderInstances[index] = instance
	} else {
		cfg.ProviderInstances = append(cfg.ProviderInstances, instance)
	}
	available := make(map[string]struct{}, len(models))
	for _, model := range models {
		available[strings.TrimSpace(model.ID)] = struct{}{}
	}
	DropTargets(cfg, func(target config.ExactModelTarget) bool {
		if target.InstanceID != instance.ID {
			return true
		}
		_, ok := available[target.ModelID]
		return ok
	})

	previousCatalogs, err := LoadCatalogs()
	if err != nil {
		return err
	}
	if err := SaveProviderInstanceCatalog(instance, models); err != nil {
		return err
	}
	if err := deps.SaveConfig(cfgPath, cfg); err != nil {
		_ = SaveCatalogs(previousCatalogs)
		return err
	}
	return nil
}

// DisconnectAPIKeyProvider removes provider's stored API key and disconnects
// the provider in the config at cfgPath: every instance authenticating with
// the key is disabled, the targets on those instances are dropped (see
// DropTargets) and their catalogs deleted. When the config cannot be
// updated, the key and the config are restored.
func DisconnectAPIKeyProvider(cfgPath, provider string, deps ConnectDeps) error {
	instance, err := APIKeyProviderInstance(provider)
	if err != nil {
		return err
	}
	provider = instance.ID
	deps = deps.withDefaults()

	previousCredential, err := deps.GetCredential(provider)
	if err != nil {
		return fmt.Errorf("loading credential: %w", err)
	}
	previousConfig, err := deps.LoadConfig(cfgPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	if err := deps.DeleteCredential(provider); err != nil {
		return fmt.Errorf("deleting credential: %w", err)
	}
	if err := disconnectAPIKeyInstances(cfgPath, instance.AuthConnectionRef, deps); err != nil {
		_ = deps.SaveConfig(cfgPath, previousConfig)
		if previousCredential != nil {
			_ = deps.SetCredential(provider, previousCredential)
		}
		return fmt.Errorf("updating config: %w", err)
	}
	return nil
}

func disconnectAPIKeyInstances(cfgPath, ref string, deps ConnectDeps) error {
	cfg, err := deps.LoadConfig(cfgPath)
	if err != nil {
		return err
	}
	disabled := make(map[string]struct{})
	var disabledIDs []string
	for _, instance := range cfg.ProviderInstances {
		if instance != nil && instance.AuthConnectionRef == ref {
			instance.State = config.ProviderInstanceStateDisabled
			if _, seen := disabled[instance.ID]; !seen {
				disabled[instance.ID] = struct{}{}
				disabledIDs = append(disabledIDs, instance.ID)
			}
		}
	}
	DropTargets(cfg, func(target config.ExactModelTarget) bool {
		_, off := disabled[target.InstanceID]
		return !off
	})
	if err := deps.SaveConfig(cfgPath, cfg); err != nil {
		return err
	}
	for _, id := range disabledIDs {
		if err := DeleteProviderInstanceCatalog(id); err != nil {
			return err
		}
	}
	return nil
}

// DropTargets removes from cfg every exact target keep rejects: from the
// active models, from the model routes (a route left without targets is
// removed), and from the model selections — the default, per-agent, image
// and light models — which are cleared when they name a dropped target or a
// removed route.
func DropTargets(cfg *config.Config, keep func(target config.ExactModelTarget) bool) {
	if cfg == nil || keep == nil {
		return
	}
	dropped := func(raw string) bool {
		target, err := config.ParseExactModelTarget(raw)
		return err == nil && !keep(target)
	}

	if len(cfg.ActiveModels) > 0 {
		active := make([]string, 0, len(cfg.ActiveModels))
		for _, raw := range cfg.ActiveModels {
			if !dropped(raw) {
				active = append(active, raw)
			}
		}
		cfg.ActiveModels = active
	}

	removedRoutes := make(map[string]struct{})
	if len(cfg.ModelRoutes) > 0 {
		routes := make([]*config.ModelRouteConfig, 0, len(cfg.ModelRoutes))
		for _, route := range cfg.ModelRoutes {
			if route == nil {
				continue
			}
			targets := make([]string, 0, len(route.Targets))
			for _, raw := range route.Targets {
				if !dropped(raw) {
					targets = append(targets, raw)
				}
			}
			route.Targets = targets
			if len(targets) == 0 {
				removedRoutes[route.Name] = struct{}{}
				continue
			}
			routes = append(routes, route)
		}
		cfg.ModelRoutes = routes
	}

	clearStale := func(selection *string) {
		value := strings.TrimSpace(*selection)
		if value == "" {
			return
		}
		if _, err := config.ParseExactModelTarget(value); err == nil {
			if dropped(value) {
				*selection = ""
			}
			return
		}
		if _, removed := removedRoutes[value]; removed {
			*selection = ""
		}
	}
	defaults := &cfg.Agents.Defaults
	clearStale(&defaults.ModelName)
	clearStale(&defaults.ImageModel)
	if defaults.Routing != nil {
		clearStale(&defaults.Routing.LightModel)
	}
	for i := range cfg.Agents.List {
		clearStale(&cfg.Agents.List[i].Model)
	}
}
