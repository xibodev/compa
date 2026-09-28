package modelservice

import (
	"errors"
	"fmt"
	"strings"

	"github.com/xibodev/compa/pkg/auth"
	"github.com/xibodev/compa/pkg/config"
	"github.com/xibodev/compa/pkg/providers"
)

// CatalogLoader returns the model catalogs of cfg's provider instances,
// keyed by instance ID.
type CatalogLoader func(cfg *config.Config) (map[string]providers.InstanceCatalog, error)

// Resolver resolves model selections — exact targets "instance-id/model-id"
// and model route names — against a config's provider instances and their
// saved catalogs. It is the one model resolution every consumer shares: the
// agent, the gateway, the web API and the CLI.
type Resolver struct {
	loadCatalogs      CatalogLoader
	resolveCredential providers.InstanceCredentialResolver
	createProvider    providers.InstanceProviderFactory
}

// ResolverOption overrides one of a Resolver's defaults.
type ResolverOption func(*Resolver)

// WithCatalogLoader replaces how the resolver loads catalogs. Nil keeps
// LoadInstanceCatalogs.
func WithCatalogLoader(load CatalogLoader) ResolverOption {
	return func(r *Resolver) {
		if load != nil {
			r.loadCatalogs = load
		}
	}
}

// WithCredentialResolver replaces how the resolver turns an instance's
// auth_connection_ref into its secret. Nil keeps ResolveCredentialReference.
func WithCredentialResolver(resolve providers.InstanceCredentialResolver) ResolverOption {
	return func(r *Resolver) {
		if resolve != nil {
			r.resolveCredential = resolve
		}
	}
}

// WithProviderFactory replaces how the resolver builds a candidate's
// provider. Nil keeps providers.CreateProviderFromInstance.
func WithProviderFactory(create providers.InstanceProviderFactory) ResolverOption {
	return func(r *Resolver) {
		if create != nil {
			r.createProvider = create
		}
	}
}

// NewResolver returns a resolver that loads the saved catalogs on every call
// (LoadInstanceCatalogs), resolves credentials from the auth store
// (ResolveCredentialReference) and builds providers with
// providers.CreateProviderFromInstance, unless opts override them.
func NewResolver(opts ...ResolverOption) *Resolver {
	r := &Resolver{
		loadCatalogs:      LoadInstanceCatalogs,
		resolveCredential: ResolveCredentialReference,
		createProvider:    providers.CreateProviderFromInstance,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}
	return r
}

// Resolve resolves selection against cfg as given — the caller decides which
// config is current — and the catalogs as saved now, so a catalog synced a
// moment ago is visible. The resolution's candidates are ordered: one for an
// exact target, the route's targets for a route. Credentials are resolved and
// providers built when a candidate's provider is first requested.
func (r *Resolver) Resolve(cfg *config.Config, selection string) (*providers.InstanceResolution, error) {
	if cfg == nil {
		return nil, errors.New("config is required")
	}
	catalogs, err := r.loadCatalogs(cfg)
	if err != nil {
		return nil, fmt.Errorf("load instance catalogs: %w", err)
	}
	return providers.ResolveInstanceTargetOrRoute(cfg, catalogs, selection, r.resolveCredential, r.createProvider)
}

// Check reports whether selection resolves against cfg and the saved
// catalogs: every target's instance exists and is enabled and its catalog
// holds the model. It neither resolves credentials nor builds providers, so
// it validates a selection when it is set. An empty selection sets no model
// and is valid.
func (r *Resolver) Check(cfg *config.Config, selection string) error {
	if strings.TrimSpace(selection) == "" {
		return nil
	}
	if err := config.ValidateSelectionSyntax(selection); err != nil {
		return err
	}
	if cfg == nil {
		return errors.New("config is required")
	}
	catalogs, err := r.loadCatalogs(cfg)
	if err != nil {
		return fmt.Errorf("load instance catalogs: %w", err)
	}
	_, err = providers.ResolveInstanceTargetOrRoute(cfg, catalogs, selection, checkOnlyCredential, checkOnlyProvider)
	return err
}

var errCheckOnly = errors.New("a checked selection has no providers")

func checkOnlyCredential(string) (string, error) { return "", errCheckOnly }

func checkOnlyProvider(*config.ProviderInstanceConfig, string, string) (providers.LLMProvider, error) {
	return nil, errCheckOnly
}

// ResolveCredentialReference returns the secret an instance's
// auth_connection_ref names. The one reference kind is credential:<key>: the
// access token stored under key in the auth store, which must not be
// expired.
func ResolveCredentialReference(ref string) (string, error) {
	kind, key, found := strings.Cut(strings.TrimSpace(ref), ":")
	key = strings.TrimSpace(key)
	if !found || kind != "credential" || key == "" {
		return "", errors.New("auth_connection_ref must use credential:<store-key>")
	}
	cred, err := auth.GetCredential(key)
	if err != nil {
		return "", fmt.Errorf("load credential reference: %w", err)
	}
	if cred == nil || strings.TrimSpace(cred.AccessToken) == "" {
		return "", fmt.Errorf("credential %q has no access token", key)
	}
	if cred.IsExpired() {
		return "", fmt.Errorf("credential %q is expired", key)
	}
	return strings.TrimSpace(cred.AccessToken), nil
}

// ValidInstanceCatalog reports whether entry, saved under key, is the catalog
// of instance: saved under the instance's ID, for that instance, from the
// instance's provider kind. A catalog left behind by a deleted instance or a
// re-created one of another kind is not.
func ValidInstanceCatalog(key string, entry *CatalogEntry, instance *config.ProviderInstanceConfig) bool {
	return entry != nil && instance != nil && key == instance.ID && entry.ID == instance.ID &&
		entry.InstanceID == instance.ID && entry.Provider == instance.ProviderKind
}

// InstanceCatalogs returns the valid catalogs in store of cfg's provider
// instances (see ValidInstanceCatalog), keyed by instance ID.
func InstanceCatalogs(cfg *config.Config, store *CatalogStore) map[string]providers.InstanceCatalog {
	catalogs := make(map[string]providers.InstanceCatalog)
	if cfg == nil || store == nil {
		return catalogs
	}
	instances := make(map[string]*config.ProviderInstanceConfig, len(cfg.ProviderInstances))
	for _, instance := range cfg.ProviderInstances {
		if instance != nil {
			instances[instance.ID] = instance
		}
	}
	for key, entry := range store.Entries {
		instance := instances[key]
		if !ValidInstanceCatalog(key, entry, instance) {
			continue
		}
		catalog := providers.InstanceCatalog{InstanceID: instance.ID, Models: make([]string, 0, len(entry.Models))}
		for _, model := range entry.Models {
			if id := strings.TrimSpace(model.ID); id != "" {
				catalog.Models = append(catalog.Models, id)
			}
		}
		catalogs[key] = catalog
	}
	return catalogs
}

// LoadInstanceCatalogs reads the saved catalogs and returns those of cfg's
// provider instances.
func LoadInstanceCatalogs(cfg *config.Config) (map[string]providers.InstanceCatalog, error) {
	store, err := LoadCatalogs()
	if err != nil {
		return nil, err
	}
	return InstanceCatalogs(cfg, store), nil
}
