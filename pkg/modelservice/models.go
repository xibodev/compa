package modelservice

import (
	"context"

	"github.com/xibodev/compa/v3/pkg/config"
)

// CatalogModel represents a single model entry in a saved catalog.
type CatalogModel struct {
	ID          string `json:"id"`
	OwnedBy     string `json:"owned_by,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	// Surfaces are the llmgw-core surfaces the catalog reports the model
	// serves, such as chat_completions, audio_transcriptions and
	// audio_speech; empty when it reports none.
	Surfaces []string `json:"surfaces,omitempty"`
	// InputModalities are the inputs the catalog declares the model takes,
	// sorted, among "audio", "image" and "text"; empty when it declares
	// none. Nothing is inferred from the model's id.
	InputModalities []string `json:"input_modalities,omitempty"`
	// AudioInput reports that the catalog declares the model takes audio:
	// a transcription model, or a chat model that accepts input audio. Only
	// such chat models transcribe voice (config.VoiceConfig.STTViaChat).
	AudioInput bool `json:"audio_input,omitempty"`
}

// CatalogEntry is a saved list of upstream models fetched for a specific provider+key combination.
type CatalogEntry struct {
	ID         string         `json:"id"`
	InstanceID string         `json:"instance_id,omitempty"`
	Provider   string         `json:"provider"`
	APIBase    string         `json:"api_base"`
	Models     []CatalogModel `json:"models"`
	FetchedAt  string         `json:"fetched_at"`
}

// CatalogStore holds all saved model catalogs.
type CatalogStore struct {
	Entries map[string]*CatalogEntry `json:"entries"`
}

// ProviderRosterItem describes a provider from the curated registry and its local configuration status.
type ProviderRosterItem struct {
	ID                  string   `json:"id"`
	DisplayName         string   `json:"display_name"`
	Label               string   `json:"label,omitempty"`
	Description         string   `json:"description,omitempty"`
	Categories          []string `json:"categories,omitempty"`
	Adapter             string   `json:"adapter,omitempty"`
	Protocol            string   `json:"protocol,omitempty"`
	DefaultEndpoint     string   `json:"default_endpoint,omitempty"`
	Compatibility       string   `json:"compatibility"`
	AuthMethods         []string `json:"auth_methods,omitempty"`
	RequiresAPIKey      bool     `json:"requires_api_key"`
	RequiresBaseURL     bool     `json:"requires_base_url"`
	AnonymousAutomation bool     `json:"anonymous_automation"`
	OnboardingFields    []string `json:"onboarding_fields,omitempty"`
	Configured          bool     `json:"configured"`
	InstanceCount       int      `json:"instance_count"`
	ConfiguredInstances []string `json:"configured_instances,omitempty"`
}

// ProviderCatalogSyncInput defines parameters for syncing models from an upstream provider.
type ProviderCatalogSyncInput struct {
	InstanceID        string
	ProviderKind      string
	Adapter           string
	Protocol          string
	Endpoint          string
	AuthConnectionRef string
	Headers           map[string]string
	Settings          map[string]any
	Runtime           *config.ProviderInstanceRuntime
	Secret            string
}

// Instance returns the provider instance input describes.
func (in ProviderCatalogSyncInput) Instance() *config.ProviderInstanceConfig {
	return &config.ProviderInstanceConfig{
		ID: in.InstanceID, ProviderKind: in.ProviderKind, Adapter: in.Adapter, Protocol: in.Protocol,
		Endpoint: in.Endpoint, AuthConnectionRef: in.AuthConnectionRef, Headers: in.Headers,
		Settings: in.Settings, Runtime: in.Runtime, State: config.ProviderInstanceStateEnabled,
	}
}

// CatalogSyncFunc queries models for a provider instance.
type CatalogSyncFunc func(ctx context.Context, input ProviderCatalogSyncInput) ([]CatalogModel, error)

// PingResult describes the outcome of probing an endpoint's reachability.
type PingResult struct {
	OK         bool   `json:"ok"`
	InstanceID string `json:"instance_id"`
	LatencyMS  int64  `json:"latency_ms"`
	ModelCount int    `json:"model_count,omitempty"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
}

// AutoConnectResult reports the results of auto-connecting the free providers
// that need no key.
type AutoConnectResult struct {
	OK                bool                       `json:"ok"`
	Total             int                        `json:"total"`
	CatalogDiscovered int                        `json:"catalog_discovered"`
	Verified          int                        `json:"verified"`
	Instances         []string                   `json:"instances"`
	Outcomes          []AnonymousProviderOutcome `json:"outcomes"`
	// DefaultModel is the verified model this connection made the default
	// model, there being none; "" when it set none.
	DefaultModel string `json:"default_model,omitempty"`
}

// AnonymousProviderOutcome reports catalog and inference evidence separately.
// Models are the free models the provider's catalog lists and ProbeModel the
// one it was tested with; Error is a short sentence, never an upstream's
// answer.
type AnonymousProviderOutcome struct {
	RegistryID string   `json:"registry_id"`
	ProviderID string   `json:"provider_id"`
	Status     string   `json:"status"`
	Models     []string `json:"models,omitempty"`
	ProbeModel string   `json:"probe_model,omitempty"`
	LatencyMS  int64    `json:"latency_ms,omitempty"`
	ErrorClass string   `json:"error_class,omitempty"`
	Error      string   `json:"error,omitempty"`

	// catalog is the catalog Models name, as the provider listed it.
	catalog []CatalogModel
}

// CatalogSyncInputFromInstance converts a config.ProviderInstanceConfig to ProviderCatalogSyncInput.
func CatalogSyncInputFromInstance(instance *config.ProviderInstanceConfig) ProviderCatalogSyncInput {
	if instance == nil {
		return ProviderCatalogSyncInput{}
	}
	headers := make(map[string]string, len(instance.Headers))
	for name, value := range instance.Headers {
		headers[name] = value
	}
	settings := make(map[string]any, len(instance.Settings))
	for name, value := range instance.Settings {
		settings[name] = value
	}
	return ProviderCatalogSyncInput{
		InstanceID:        instance.ID,
		ProviderKind:      instance.ProviderKind,
		Adapter:           instance.Adapter,
		Protocol:          instance.Protocol,
		Endpoint:          instance.Endpoint,
		AuthConnectionRef: instance.AuthConnectionRef,
		Headers:           headers,
		Settings:          settings,
		Runtime:           instance.Runtime,
	}
}
