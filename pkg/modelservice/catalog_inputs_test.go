package modelservice

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	core "github.com/xibodev/llmgw-core"
	"github.com/xibodev/llmgw-core/extension"

	"github.com/xibodev/compa/v3/pkg/config"
	"github.com/xibodev/compa/v3/pkg/providers"
)

// A catalog row's inputs come from what core's ModelInfo declares: typed
// capabilities, legacy capability maps, or the audio-in operation. Nothing
// is read from the model's id.
func TestCatalogModelsFromCoreKeepDeclaredInputs(t *testing.T) {
	chatWithAudio := &core.ModelCapabilities{
		Operations: core.ModelOperationCapabilities{Chat: core.SupportSupported, AudioIn: core.SupportSupported},
		Surfaces:   core.ModelSurfaceCapabilities{ChatCompletions: core.SupportSupported},
		Inputs:     core.ModelInputCapabilities{Text: core.SupportSupported, Image: core.SupportSupported},
	}
	textChat := &core.ModelCapabilities{
		Operations: core.ModelOperationCapabilities{Chat: core.SupportSupported, AudioIn: core.SupportUnsupported},
		Surfaces:   core.ModelSurfaceCapabilities{ChatCompletions: core.SupportSupported},
		Inputs:     core.ModelInputCapabilities{Text: core.SupportSupported},
	}
	models := CatalogModelsFromCore([]core.ModelInfo{
		{ID: "gemini-voice", Capabilities: chatWithAudio},
		{ID: "gpt-audio-named-but-text", Capabilities: textChat},
		{ID: "listens", SupportedAPIs: []string{"/v1/chat/completions"}, LegacyCapabilities: map[string]any{"input_modalities": []any{"text", "audio"}}},
		{ID: "nested", SupportedAPIs: []string{"/v1/chat/completions"}, LegacyCapabilities: map[string]any{"modalities": map[string]any{"input": []any{"text", "image", "audio"}}}},
		{ID: "flagged", SupportedAPIs: []string{"/v1/chat/completions"}, LegacyCapabilities: map[string]any{"input_audio": true}},
		{ID: "whisper-1", SupportedAPIs: []string{"/v1/audio/transcriptions"}},
		// A voice that speaks: its "audio" is output, not input.
		{ID: "en-US-Voice", SupportedAPIs: []string{"/v1/audio/speech"}, LegacyCapabilities: map[string]any{"tts": true, "audio": true}},
		{ID: "unknown"},
	})
	byID := make(map[string]CatalogModel, len(models))
	for _, model := range models {
		byID[model.ID] = model
	}
	for id, want := range map[string]struct {
		inputs   string
		audio    bool
		surfaces string
	}{
		"gemini-voice":             {"audio,image,text", true, "chat_completions"},
		"gpt-audio-named-but-text": {"text", false, "chat_completions"},
		"listens":                  {"audio,text", true, "chat_completions"},
		"nested":                   {"audio,image,text", true, "chat_completions"},
		"flagged":                  {"audio,text", true, "chat_completions"},
		"whisper-1":                {"audio", true, "audio_transcriptions"},
		"en-US-Voice":              {"", false, "audio_speech"},
		"unknown":                  {"", false, ""},
	} {
		got := byID[id]
		if strings.Join(got.InputModalities, ",") != want.inputs || got.AudioInput != want.audio || strings.Join(got.Surfaces, ",") != want.surfaces {
			t.Errorf("%s: inputs %v audio %v surfaces %v, want %s %v %s", id, got.InputModalities, got.AudioInput, got.Surfaces, want.inputs, want.audio, want.surfaces)
		}
	}
}

// Audio in and out on a chat model are what its chat takes and gives, not
// the transcription and speech endpoints; a row that serves no chat keeps
// them as its surfaces.
func TestModelSurfacesKeepAudioOperationsOfChatModelsOffTheAudioSurfaces(t *testing.T) {
	chat := core.ModelInfo{ID: "chat", Capabilities: &core.ModelCapabilities{
		Operations: core.ModelOperationCapabilities{Chat: core.SupportSupported, AudioIn: core.SupportSupported, AudioOut: core.SupportSupported},
		Surfaces:   core.ModelSurfaceCapabilities{ChatCompletions: core.SupportSupported},
	}}
	if got := ModelSurfaces(chat); !slices.Equal(got, []string{"chat_completions"}) {
		t.Fatalf("chat model surfaces = %v", got)
	}
	listed := chat
	listed.SupportedAPIs = []string{"/v1/audio/transcriptions"}
	if got := ModelSurfaces(listed); !slices.Equal(got, []string{"audio_transcriptions", "chat_completions"}) {
		t.Fatalf("chat model listing the transcription endpoint surfaces = %v", got)
	}
	transcriber := core.ModelInfo{ID: "stt", Capabilities: &core.ModelCapabilities{
		Operations: core.ModelOperationCapabilities{AudioIn: core.SupportSupported},
	}}
	speaker := core.ModelInfo{ID: "tts", Capabilities: &core.ModelCapabilities{
		Operations: core.ModelOperationCapabilities{AudioOut: core.SupportSupported},
	}}
	if got := ModelSurfaces(transcriber); !slices.Equal(got, []string{"audio_transcriptions"}) {
		t.Fatalf("transcription model surfaces = %v", got)
	}
	if got := ModelSurfaces(speaker); !slices.Equal(got, []string{"audio_speech"}) {
		t.Fatalf("speech model surfaces = %v", got)
	}
}

// A daemon catalog's declared audio input reaches the saved catalog, so
// voice can tell a chat model that listens from one that only reads.
func TestSyncCatalogKeepsADaemonModelsAudioInput(t *testing.T) {
	original := providers.ExtensionDaemonSecret
	providers.ExtensionDaemonSecret = func() (string, error) { return "daemon-secret", nil }
	t.Cleanup(func() { providers.ExtensionDaemonSecret = original })
	listens := &core.ModelCapabilities{
		Operations: core.ModelOperationCapabilities{Chat: core.SupportSupported, AudioIn: core.SupportSupported},
		Surfaces:   core.ModelSurfaceCapabilities{ChatCompletions: core.SupportSupported},
		Inputs:     core.ModelInputCapabilities{Text: core.SupportSupported},
	}
	reads := &core.ModelCapabilities{
		Operations: core.ModelOperationCapabilities{Chat: core.SupportSupported},
		Surfaces:   core.ModelSurfaceCapabilities{ChatCompletions: core.SupportSupported},
		Inputs:     core.ModelInputCapabilities{Text: core.SupportSupported},
	}
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != extension.PathPrefix+"acme/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(extension.ModelsResponse{Models: []core.ModelInfo{
			{ID: "listens", Capabilities: listens}, {ID: "reads", Capabilities: reads},
		}})
	}))
	defer daemon.Close()
	models, err := SyncCatalog(context.Background(), ProviderCatalogSyncInput{
		InstanceID: "ext-acme", ProviderKind: "extension", Adapter: config.ProviderAdapterExtension,
		Protocol: config.ExtensionSurfaceChatCompletions, Endpoint: daemon.URL,
		Settings: map[string]any{
			config.ExtensionProviderSetting:   "acme",
			config.ExtensionCredentialSetting: "none",
			config.ExtensionSurfacesSetting:   []any{"chat_completions"},
		},
	})
	if err != nil {
		t.Fatalf("SyncCatalog() error = %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %+v", models)
	}
	for _, model := range models {
		wantAudio := model.ID == "listens"
		if model.AudioInput != wantAudio || !slices.Equal(model.Surfaces, []string{"chat_completions"}) {
			t.Errorf("%s: audio input %v surfaces %v, want %v and chat_completions", model.ID, model.AudioInput, model.Surfaces, wantAudio)
		}
	}
}

func TestCheckChatTranscriptionTargetNeedsDeclaredAudioInput(t *testing.T) {
	cfg := &config.Config{ProviderInstances: []*config.ProviderInstanceConfig{{
		ID: "chat", ProviderKind: "openai", Adapter: config.ProviderAdapterOpenAICompatible, Protocol: "openai",
		State: config.ProviderInstanceStateEnabled,
	}}}
	store := &CatalogStore{Entries: map[string]*CatalogEntry{
		"chat": {ID: "chat", InstanceID: "chat", Provider: "openai", Models: []CatalogModel{
			{ID: "listens", Surfaces: []string{"chat_completions"}, AudioInput: true},
			{ID: "reads", Surfaces: []string{"chat_completions"}},
		}},
		// A catalog saved for another kind is not the instance's.
		"stale": {ID: "stale", InstanceID: "stale", Provider: "other", Models: []CatalogModel{{ID: "listens", AudioInput: true}}},
	}}
	if err := CheckChatTranscriptionTarget(cfg, store, "chat/listens"); err != nil {
		t.Fatalf("audio chat model: %v", err)
	}
	for _, target := range []string{"chat/reads", "chat/missing", "stale/listens", "gone/listens"} {
		err := CheckChatTranscriptionTarget(cfg, store, target)
		if !errors.Is(err, ErrNoAudioInput) || !strings.Contains(err.Error(), "does not accept audio input") {
			t.Errorf("%s: error = %v, want ErrNoAudioInput", target, err)
		}
	}
}

func TestAdoptDefaultModelNeverReplacesADefault(t *testing.T) {
	cfg := &config.Config{}
	if !AdoptDefaultModel(cfg, " free/model ") || cfg.Agents.Defaults.ModelName != "free/model" {
		t.Fatalf("default = %q, want the adopted model", cfg.Agents.Defaults.ModelName)
	}
	if AdoptDefaultModel(cfg, "other/model") || cfg.Agents.Defaults.ModelName != "free/model" {
		t.Fatalf("default = %q, want the first default kept", cfg.Agents.Defaults.ModelName)
	}
	if AdoptDefaultModel(&config.Config{}, "  ") || AdoptDefaultModel(nil, "x/y") {
		t.Fatal("adopted an empty target or into no config")
	}
}

func verifiedOutcomes(context.Context, *config.Config) []AnonymousProviderOutcome {
	return []AnonymousProviderOutcome{
		{RegistryID: "pollinations", ProviderID: "pollinations", Status: "verified", Models: []string{"openai-fast"}, ProbeModel: "openai-fast"},
		{RegistryID: "llm7", ProviderID: "llm7", Status: "verified", Models: []string{"codestral-latest"}, ProbeModel: "codestral-latest"},
	}
}

func TestAutoConnectFreeMakesTheFirstVerifiedModelTheDefault(t *testing.T) {
	t.Setenv("COMPA_HOME", t.TempDir())
	cfg := config.DefaultConfig()
	result, err := AutoConnectFree(context.Background(), cfg, verifiedOutcomes)
	if err != nil {
		t.Fatal(err)
	}
	if result.Verified != 2 || cfg.Agents.Defaults.ModelName != "pollinations/openai-fast" || result.DefaultModel != "pollinations/openai-fast" {
		t.Fatalf("verified = %d, default = %q, reported %q", result.Verified, cfg.Agents.Defaults.ModelName, result.DefaultModel)
	}
}

func TestAutoConnectFreeKeepsAChosenDefault(t *testing.T) {
	t.Setenv("COMPA_HOME", t.TempDir())
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ModelName = "mine/model"
	result, err := AutoConnectFree(context.Background(), cfg, verifiedOutcomes)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agents.Defaults.ModelName != "mine/model" || result.DefaultModel != "" {
		t.Fatalf("default = %q, reported %q; want the chosen default kept", cfg.Agents.Defaults.ModelName, result.DefaultModel)
	}
}

func TestAutoConnectFreeSetsNoDefaultWithoutAVerifiedModel(t *testing.T) {
	t.Setenv("COMPA_HOME", t.TempDir())
	cfg := config.DefaultConfig()
	if _, err := AutoConnectFree(context.Background(), cfg, func(context.Context, *config.Config) []AnonymousProviderOutcome {
		return []AnonymousProviderOutcome{{RegistryID: "pollinations", ProviderID: "pollinations", Status: "connected", ProbeModel: "openai-fast", Models: []string{"openai-fast"}}}
	}); err != nil {
		t.Fatal(err)
	}
	if cfg.Agents.Defaults.ModelName != "" {
		t.Fatalf("default = %q, want none", cfg.Agents.Defaults.ModelName)
	}
}

// Core's registry lists Google's Gemini API as both "gemini" and
// "ai_studio"; the roster offers it once, and counts an instance of the
// left-out kind under the kept entry.
func TestListRosterOffersEachProviderOnce(t *testing.T) {
	cfg := &config.Config{ProviderInstances: []*config.ProviderInstanceConfig{
		{ID: "studio", ProviderKind: "ai_studio", Adapter: config.ProviderAdapterNative},
	}}
	roster := ListRoster(cfg)
	var gemini *ProviderRosterItem
	for i, item := range roster {
		if item.ID == "ai_studio" {
			t.Fatalf("roster lists ai_studio next to gemini: %+v", item)
		}
		if item.ID == "gemini" {
			gemini = &roster[i]
		}
	}
	if gemini == nil {
		t.Fatal("roster has no gemini entry")
	}
	if !gemini.Configured || !slices.Equal(gemini.ConfiguredInstances, []string{"studio"}) || gemini.Adapter != config.ProviderAdapterOpenAICompatible {
		t.Fatalf("gemini entry = %+v, want the ai_studio instance counted", gemini)
	}
	endpoints := map[string]string{}
	for _, item := range roster {
		if item.DefaultEndpoint == "" {
			continue
		}
		endpoint := strings.TrimSuffix(item.DefaultEndpoint, "/")
		if other, seen := endpoints[endpoint]; seen {
			t.Errorf("roster entries %s and %s share the endpoint %s", other, item.ID, endpoint)
		}
		endpoints[endpoint] = item.ID
	}
}
