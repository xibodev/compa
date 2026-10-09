package modelservice

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	core "github.com/xibodev/llmgw-core"
	"github.com/xibodev/llmgw-core/translation"

	"github.com/xibodev/compa/v4/pkg/config"
	"github.com/xibodev/compa/v4/pkg/providers"
)

// CatalogSyncSupported reports whether Compa can list instance's models: it
// runs a core provider for the instance.
func CatalogSyncSupported(instance *config.ProviderInstanceConfig) bool {
	_, ok := providers.InstanceRuntimeType(instance)
	return ok
}

// SyncCatalog returns the models an instance reaches with its credential:
// the catalog its llmgw-core provider lists (see providers.NewCoreProvider),
// an extension instance's through its daemon.
func SyncCatalog(ctx context.Context, input ProviderCatalogSyncInput) ([]CatalogModel, error) {
	instance := input.Instance()
	provider, err := providers.NewCoreProvider(instance)
	if err != nil {
		return nil, err
	}
	source, err := providers.InstanceCredential(instance, input.Secret)
	if err != nil {
		return nil, err
	}
	credential, err := source(ctx)
	if err != nil {
		return nil, err
	}
	models, err := provider.ListModels(ctx, credential)
	if err != nil {
		return nil, fmt.Errorf("catalog request failed: %w", err)
	}
	// An extension daemon's models that report nothing about what they serve
	// come back with their provider's surfaces, which llmgw-core's extension
	// client fills in, so a speech-only provider's models never pass for chat.
	return CatalogModelsFromCore(models), nil
}

// CatalogModelsFromCore keeps what Compa needs of a core catalog: each
// model's id, display name, owner, the surfaces it serves and the inputs it
// takes.
func CatalogModelsFromCore(models []core.ModelInfo) []CatalogModel {
	catalog := make([]CatalogModel, 0, len(models))
	for _, model := range models {
		if strings.TrimSpace(model.ID) == "" {
			continue
		}
		owner := model.OwnedBy
		if owner == "" {
			owner = model.Vendor
		}
		inputs := ModelInputModalities(model)
		catalog = append(catalog, CatalogModel{
			ID: model.ID, OwnedBy: owner, DisplayName: model.DisplayName, Surfaces: ModelSurfaces(model),
			InputModalities: inputs, AudioInput: slices.Contains(inputs, inputModalityAudio),
		})
	}
	return catalog
}

// apiSurfaces maps the endpoint paths a catalog row lists to the surfaces
// they serve: the paths core.SurfacePath writes, and the bare /videos some
// catalogs list.
var apiSurfaces = map[string]core.ModelSurface{
	"/chat/completions":     core.ModelSurfaceChatCompletions,
	"/responses":            core.ModelSurfaceResponses,
	"/messages":             core.ModelSurfaceMessages,
	"/embeddings":           core.ModelSurfaceEmbeddings,
	"/audio/transcriptions": core.ModelSurfaceAudioTranscriptions,
	"/audio/speech":         core.ModelSurfaceAudioSpeech,
	"/images/generations":   core.ModelSurfaceImages,
	"/videos/generations":   core.ModelSurfaceVideos,
	"/videos":               core.ModelSurfaceVideos,
}

// ModelSurfaces returns the surfaces a catalog row reports its model
// serves, from its supported APIs and typed capabilities, sorted. A row that
// reports neither has none: its surfaces are unknown.
//
// Audio in and out on a chat model are what its chat takes and gives (input
// audio, spoken answers), not the transcription and speech endpoints: only
// a row that serves no chat gets those surfaces from its capabilities; a
// chat row gets them only by listing their endpoints.
func ModelSurfaces(model core.ModelInfo) []string {
	set := map[core.ModelSurface]struct{}{}
	for _, api := range model.SupportedAPIs {
		path := "/" + strings.Trim(strings.ToLower(strings.TrimSpace(api)), "/")
		path = strings.TrimPrefix(path, "/v1")
		if surface, ok := apiSurfaces[path]; ok {
			set[surface] = struct{}{}
		} else if core.KnownModelSurface(core.ModelSurface(strings.Trim(path, "/"))) {
			set[core.ModelSurface(strings.Trim(path, "/"))] = struct{}{}
		}
	}
	if capabilities := model.Capabilities; capabilities != nil {
		supported := func(support core.Support, surface core.ModelSurface) {
			if support == core.SupportSupported {
				set[surface] = struct{}{}
			}
		}
		supported(capabilities.Surfaces.ChatCompletions, core.ModelSurfaceChatCompletions)
		supported(capabilities.Surfaces.Responses, core.ModelSurfaceResponses)
		supported(capabilities.Surfaces.Messages, core.ModelSurfaceMessages)
		supported(capabilities.Operations.Embeddings, core.ModelSurfaceEmbeddings)
		supported(capabilities.Operations.Image, core.ModelSurfaceImages)
		supported(capabilities.Operations.Video, core.ModelSurfaceVideos)
		listed := make([]core.ModelSurface, 0, len(set))
		for surface := range set {
			listed = append(listed, surface)
		}
		if capabilities.Operations.Chat != core.SupportSupported && !translation.ServesChat(listed...) {
			supported(capabilities.Operations.AudioIn, core.ModelSurfaceAudioTranscriptions)
			supported(capabilities.Operations.AudioOut, core.ModelSurfaceAudioSpeech)
		}
	}
	if len(set) == 0 {
		return nil
	}
	surfaces := make([]string, 0, len(set))
	for surface := range set {
		surfaces = append(surfaces, string(surface))
	}
	slices.Sort(surfaces)
	return surfaces
}

// Input modalities a catalog row can declare.
const (
	inputModalityAudio = "audio"
	inputModalityImage = "image"
	inputModalityText  = "text"
)

// ModelInputModalities returns the inputs a catalog row declares its model
// takes, sorted: text and image from its typed input capabilities, audio
// from its audio-in operation (see core.ModelOperationAudioIn), and any of
// them from an input modality list in its legacy capabilities, such as
// {"input_modalities": ["text", "audio"]} or {"modalities": {"input":
// [...]}}, or an explicit {"input_audio": true}. A row without typed
// capabilities is read through core.InferCapabilities. What is not declared
// is not supported: the model's id is never read.
func ModelInputModalities(model core.ModelInfo) []string {
	capabilities := model.Capabilities
	if capabilities == nil {
		capabilities = core.InferCapabilities(model, time.Time{}, time.Time{})
	}
	set := map[string]struct{}{}
	add := func(modality string) {
		switch modality = strings.ToLower(strings.TrimSpace(modality)); modality {
		case inputModalityAudio, inputModalityImage, inputModalityText:
			set[modality] = struct{}{}
		}
	}
	if capabilities.Inputs.Text == core.SupportSupported {
		add(inputModalityText)
	}
	if capabilities.Inputs.Image == core.SupportSupported {
		add(inputModalityImage)
	}
	if capabilities.Operations.AudioIn == core.SupportSupported {
		add(inputModalityAudio)
	}
	legacy := model.LegacyCapabilities
	for _, key := range []string{"input_audio", "audio_input"} {
		if enabled, _ := legacy[key].(bool); enabled {
			add(inputModalityAudio)
		}
	}
	for _, modality := range legacyInputModalities(legacy) {
		add(modality)
	}
	if len(set) == 0 {
		return nil
	}
	modalities := make([]string, 0, len(set))
	for modality := range set {
		modalities = append(modalities, modality)
	}
	slices.Sort(modalities)
	return modalities
}

// legacyInputModalities reads an input modality list from a row's legacy
// capabilities: "input_modalities" or "inputs" as a list, or "modalities"
// or "architecture" holding an "input" or "input_modalities" list.
func legacyInputModalities(legacy map[string]any) []string {
	var modalities []string
	collect := func(raw any) {
		switch list := raw.(type) {
		case []string:
			modalities = append(modalities, list...)
		case []any:
			for _, item := range list {
				if text, ok := item.(string); ok {
					modalities = append(modalities, text)
				}
			}
		}
	}
	collect(legacy["input_modalities"])
	collect(legacy["inputs"])
	for _, key := range []string{"modalities", "architecture"} {
		if nested, ok := legacy[key].(map[string]any); ok {
			collect(nested["input"])
			collect(nested["input_modalities"])
		}
	}
	return modalities
}
