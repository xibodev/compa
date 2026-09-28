package providers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/xibodev/llm-provider-auth/tokenstore"
	core "github.com/xibodev/llmgw-core"
	"github.com/xibodev/llmgw-core/extension"
	coreproviders "github.com/xibodev/llmgw-core/providers"
	"github.com/xibodev/llmgw-core/translation"

	"github.com/xibodev/compa/pkg/auth"
	"github.com/xibodev/compa/pkg/config"
	"github.com/xibodev/compa/pkg/providers/coretransport"
)

// Every provider instance runs on an llmgw-core provider. Core has no
// generic factory yet (llmgw-core issue #33), so this file is the one place
// that maps an instance to a core constructor: the runtime type the core
// registry records for the instance's provider kind selects the constructor,
// and an instance whose kind the registry does not know falls back on its
// adapter.

// coreBuild is what a constructor builds a core provider from.
type coreBuild struct {
	instance *config.ProviderInstanceConfig
	// registryID is the canonical registry id of the instance's kind, or ""
	// for a kind the registry does not know.
	registryID string
	endpoint   string
	client     *http.Client
}

// coreRuntimes maps a core registry runtime type to its constructor.
var coreRuntimes = map[string]func(coreBuild) (core.Provider, error){
	"openai_compatible": func(b coreBuild) (core.Provider, error) {
		return coreproviders.NewOpenAICompatible(coreproviders.OpenAICompatibleConfig{
			BaseURL: b.endpoint, RegistryID: b.registryID, ForwardAllFields: true,
			Client: b.client, CatalogClient: b.client,
		})
	},
	"anthropic": func(b coreBuild) (core.Provider, error) {
		// Core appends /v1/messages; instances record the API root with its
		// /v1, as Anthropic documents it.
		return coreproviders.NewAnthropic(coreproviders.AnthropicConfig{
			BaseURL: strings.TrimSuffix(strings.TrimRight(b.endpoint, "/"), "/v1"),
			Client:  b.client, CatalogClient: b.client,
		})
	},
	"ai_studio": func(b coreBuild) (core.Provider, error) {
		return coreproviders.NewGoogle(coreproviders.GoogleConfig{Deployment: coreproviders.GoogleAIStudio, BaseURL: b.endpoint, Client: b.client})
	},
	"vertex_ai": func(b coreBuild) (core.Provider, error) {
		project, _ := b.instance.Settings["project"].(string)
		location, _ := b.instance.Settings["location"].(string)
		return coreproviders.NewGoogle(coreproviders.GoogleConfig{
			Deployment: coreproviders.GoogleVertexAI, BaseURL: b.endpoint, Client: b.client,
			Project: strings.TrimSpace(project), Location: strings.TrimSpace(location),
		})
	},
	"azure_openai": func(b coreBuild) (core.Provider, error) {
		return coreproviders.NewAzureOpenAI(coreproviders.AzureOpenAIConfig{BaseURL: b.endpoint, Client: b.client, CatalogClient: b.client})
	},
	"bedrock": func(b coreBuild) (core.Provider, error) {
		// A Bedrock endpoint is a URL or, without a scheme, a region name.
		region, base := "", b.endpoint
		if base != "" && !strings.Contains(base, "://") {
			region, base = base, ""
		}
		return coreproviders.NewBedrock(region, base, coreproviders.OpenAICompatibleConfig{
			RegistryID: b.registryID, ForwardAllFields: true, Client: b.client, CatalogClient: b.client,
		})
	},
	"elevenlabs": func(b coreBuild) (core.Provider, error) {
		// An empty endpoint selects core's https://api.elevenlabs.io/v1.
		return coreproviders.NewElevenLabs(coreproviders.ElevenLabsConfig{BaseURL: b.endpoint, Client: b.client})
	},
	"mimo": func(b coreBuild) (core.Provider, error) {
		// An empty endpoint selects core's https://api.xiaomimimo.com/v1.
		return coreproviders.NewMiMo(coreproviders.MiMoConfig{BaseURL: b.endpoint, Client: b.client})
	},
	"ollama": func(b coreBuild) (core.Provider, error) {
		// Core speaks Ollama's native API at the daemon's root, not its
		// OpenAI-compatible /v1.
		return coreproviders.NewOllama(coreproviders.OllamaConfig{
			BaseURL: strings.TrimSuffix(strings.TrimRight(b.endpoint, "/"), "/v1"),
			Client:  b.client, CatalogClient: b.client,
		})
	},
}

// adapterRuntimes is the runtime type of an adapter, for an instance whose
// provider kind the registry does not know.
var adapterRuntimes = map[string]string{
	config.ProviderAdapterOpenAICompatible:    "openai_compatible",
	config.ProviderAdapterAnthropicCompatible: "anthropic",
}

// InstanceRuntimeType returns the core runtime type that serves instance,
// or false when Compa runs none for it. Extension instances run on the
// extension daemon and report "extension".
func InstanceRuntimeType(instance *config.ProviderInstanceConfig) (string, bool) {
	if instance == nil {
		return "", false
	}
	adapter := strings.ToLower(strings.TrimSpace(instance.Adapter))
	if adapter == config.ProviderAdapterExtension {
		return "extension", true
	}
	if entry, ok := coreproviders.RegistryProvider(strings.TrimSpace(instance.ProviderKind)); ok {
		if _, served := coreRuntimes[entry.RuntimeType]; served {
			return entry.RuntimeType, true
		}
	}
	runtime, ok := adapterRuntimes[adapter]
	return runtime, ok
}

// RegistryAdapter returns the adapter an instance of a registry entry
// records: the compatible adapter of its wire, or native for any other
// runtime Compa runs.
func RegistryAdapter(entry coreproviders.RegistryEntry) (string, bool) {
	switch entry.RuntimeType {
	case "openai_compatible":
		return config.ProviderAdapterOpenAICompatible, true
	case "anthropic":
		return config.ProviderAdapterAnthropicCompatible, true
	}
	if _, ok := coreRuntimes[entry.RuntimeType]; ok {
		return config.ProviderAdapterNative, true
	}
	return "", false
}

// NewCoreProvider builds the core provider of instance, able to serve Chat
// Completions: a provider that does not speak it natively is wrapped in a
// translation.Adapter. Its HTTP client carries the instance's headers and
// honours its proxy and request timeout.
func NewCoreProvider(instance *config.ProviderInstanceConfig) (core.Provider, error) {
	if instance == nil {
		return nil, fmt.Errorf("provider instance is required")
	}
	if err := config.SupportedProviderInstanceAdapter(instance); err != nil {
		return nil, err
	}
	runtimeType, ok := InstanceRuntimeType(instance)
	if !ok {
		return nil, fmt.Errorf("provider instance %q: Compa runs no provider for kind %q with adapter %q", instance.ID, instance.ProviderKind, instance.Adapter)
	}
	var (
		provider core.Provider
		err      error
	)
	if runtimeType == "extension" {
		provider, err = newExtensionCoreProvider(instance)
	} else {
		registryID := ""
		if entry, ok := coreproviders.RegistryProvider(strings.TrimSpace(instance.ProviderKind)); ok {
			registryID = entry.ID
		}
		client, clientErr := instanceHTTPClient(instance, false)
		if clientErr != nil {
			return nil, clientErr
		}
		provider, err = coreRuntimes[runtimeType](coreBuild{
			instance: instance, registryID: registryID,
			endpoint: strings.TrimSpace(instance.Endpoint), client: client,
		})
	}
	if err != nil {
		return nil, fmt.Errorf("provider instance %q: %w", instance.ID, err)
	}
	return servingChat(provider), nil
}

// servingChat returns provider able to serve Chat Completions: as is when it
// speaks it natively, and otherwise through a translation.Adapter, which
// serves Chat over Responses or Messages.
func servingChat(provider core.Provider) core.Provider {
	if core.ServesNatively(provider, "", core.ModelSurfaceChatCompletions) {
		return provider
	}
	return translation.Adapter{Provider: provider}
}

// defaultRequestTimeout bounds the wait for a response's headers when the
// instance sets no request_timeout.
const defaultRequestTimeout = 5 * time.Minute

// instanceHTTPClient returns the client a core provider of instance sends
// its requests with. The request timeout bounds the wait for response
// headers rather than the whole exchange, so it never cuts off a stream.
// direct skips the proxy, for a daemon on the loopback interface.
func instanceHTTPClient(instance *config.ProviderInstanceConfig, direct bool) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = defaultRequestTimeout
	transport.Proxy = http.ProxyFromEnvironment
	if direct {
		transport.Proxy = nil
	}
	if runtime := instance.Runtime; runtime != nil {
		if runtime.RequestTimeout > 0 {
			transport.ResponseHeaderTimeout = time.Duration(runtime.RequestTimeout) * time.Second
		}
		if proxy := strings.TrimSpace(runtime.Proxy); proxy != "" && !direct {
			parsed, err := url.Parse(proxy)
			if err != nil || parsed.Host == "" {
				return nil, fmt.Errorf("provider instance %q has an invalid proxy URL", instance.ID)
			}
			transport.Proxy = http.ProxyURL(parsed)
		}
	}
	var roundTripper http.RoundTripper = transport
	if len(instance.Headers) > 0 {
		roundTripper = &headerTransport{headers: cloneStringMap(instance.Headers), base: transport}
	}
	return &http.Client{
		Transport:     roundTripper,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, nil
}

// headerTransport adds an instance's headers to every request. A header the
// provider set, such as its authentication, is never replaced.
type headerTransport struct {
	headers map[string]string
	base    http.RoundTripper
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	out := req.Clone(req.Context())
	for name, value := range t.headers {
		if strings.TrimSpace(name) != "" && out.Header.Get(name) == "" {
			out.Header.Set(name, value)
		}
	}
	return t.base.RoundTrip(out)
}

// newExtensionCoreProvider returns the extension.Provider of an extension
// instance: the daemon's provider, serving every surface the daemon lists for
// it (config.ProviderInstanceConfig.ExtensionSurfaces), chat or not, so voice
// and chat build it through NewCoreProvider alike.
func newExtensionCoreProvider(instance *config.ProviderInstanceConfig) (core.Provider, error) {
	endpoint := strings.TrimSpace(instance.Endpoint)
	base, err := url.Parse(endpoint)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return nil, fmt.Errorf("needs the extension daemon's http(s) URL as its endpoint")
	}
	secret, err := ExtensionDaemonSecret()
	if err != nil {
		return nil, fmt.Errorf("extension daemon secret: %w", err)
	}
	httpClient, err := instanceHTTPClient(instance, true)
	if err != nil {
		return nil, err
	}
	client, err := extension.NewClient(extension.Config{BaseURL: endpoint, Secret: secret, HTTPClient: httpClient})
	if err != nil {
		return nil, err
	}
	listed := instance.ExtensionSurfaces()
	surfaces := make([]core.ModelSurface, 0, len(listed))
	for _, surface := range listed {
		surfaces = append(surfaces, core.ModelSurface(surface))
	}
	return extension.NewProvider(client, extension.ProviderInfo{
		ID: instance.ExtensionProvider(), Surfaces: surfaces,
	}), nil
}

// InstanceCredential returns the source of the credential each request of
// instance carries. secret is what the instance's auth_connection_ref
// resolved to: an API key, or an extension provider's token. A signed-in
// extension provider's credential is read, and refreshed when due, per
// request.
func InstanceCredential(instance *config.ProviderInstanceConfig, secret string) (coretransport.CredentialSource, error) {
	if instance == nil {
		return nil, fmt.Errorf("provider instance is required")
	}
	secret = strings.TrimSpace(secret)
	if instance.ExtensionProvider() == "" {
		if secret == "" {
			return coretransport.StaticCredential(nil), nil
		}
		return coretransport.StaticCredential(&core.Credential{APIKey: secret, TokenType: core.TokenTypeAPIKey}), nil
	}
	credentialKind, _ := instance.Settings[config.ExtensionCredentialSetting].(string)
	switch strings.ToLower(strings.TrimSpace(credentialKind)) {
	case "none":
		return coretransport.StaticCredential(nil), nil
	case "token":
		if secret == "" {
			return nil, fmt.Errorf("provider instance %q needs its token", instance.ID)
		}
		return coretransport.StaticCredential(&core.Credential{Token: secret}), nil
	case "oauth":
		key, _ := instance.Settings[config.ExtensionCredentialKeySetting].(string)
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, ErrExtensionSignInRequired
		}
		endpoint, provider := strings.TrimSpace(instance.Endpoint), instance.ExtensionProvider()
		return func(ctx context.Context) (*core.Credential, error) {
			return ExtensionSignedInCredential(ctx, endpoint, provider, key)
		}, nil
	}
	return nil, fmt.Errorf("provider instance %q has unknown credential kind %q", instance.ID, credentialKind)
}

// ExtensionDaemonSecret returns the extension daemon's shared secret. It reads
// the auth store on every call, so a secret changed in the UI applies to the
// next provider built. Tests replace it.
var ExtensionDaemonSecret = func() (string, error) {
	credential, err := auth.GetCredential(auth.ExtensionDaemonKey)
	if err != nil || credential == nil {
		return "", err
	}
	return credential.AccessToken, nil
}

// ExtensionControlClient returns the client for a daemon's control routes
// (info, models, refresh and sign-in): it never goes through a proxy and
// never follows redirects.
func ExtensionControlClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &http.Client{
		Transport:     transport,
		Timeout:       extension.DefaultTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// ExtensionSignedInCredential returns the current credential stored under key
// for the daemon provider the owner signed in to, refreshing it through the
// daemon when due. Every process shares the auth store's leases, so a refresh
// token is spent once. Tests replace it.
var ExtensionSignedInCredential = func(ctx context.Context, endpoint, provider, key string) (*core.Credential, error) {
	secret, err := ExtensionDaemonSecret()
	if err != nil {
		return nil, fmt.Errorf("extension daemon secret: %w", err)
	}
	client, err := extension.NewClient(extension.Config{BaseURL: endpoint, Secret: secret, HTTPClient: ExtensionControlClient()})
	if err != nil {
		return nil, err
	}
	coordinator, err := tokenstore.NewCoordinator(auth.DefaultTokenStore(), client.RefreshFunc(provider))
	if err != nil {
		return nil, err
	}
	record, err := coordinator.Token(ctx, key)
	if errors.Is(err, tokenstore.ErrNotFound) {
		return nil, ErrExtensionSignInRequired
	}
	if err != nil {
		return nil, err
	}
	return core.CredentialFromRecord(key, record), nil
}

// ErrExtensionSignInRequired reports a provider whose credential comes from a
// sign-in that has not happened.
var ErrExtensionSignInRequired = errors.New("sign in to this provider first")
