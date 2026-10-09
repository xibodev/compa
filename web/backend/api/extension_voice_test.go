package api

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	core "github.com/xibodev/llmgw-core"

	"github.com/xibodev/compa/v4/pkg/config"
)

// A daemon provider serving speech only becomes an extension instance, its
// catalog reports the provider's surfaces, voice synthesizes through the
// normal provider factory, and chat never offers or accepts its models.
func TestSpeechOnlyExtensionProviderServesVoiceNotChat(t *testing.T) {
	configPath, cleanup := setupCredentialTestEnv(t)
	defer cleanup()
	resetCredentialHooks(t)
	daemon := newFakeExtensionDaemon(t)

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	if rec := extensionRequest(t, mux, http.MethodPut, "/api/extension", map[string]any{"url": daemon.server.URL, "secret": testDaemonSecret}); rec.Code != http.StatusOK {
		t.Fatalf("connect status = %d, body=%s", rec.Code, rec.Body.String())
	}

	speaker := loadExtensionInstance(t, configPath, "ext-speaker")
	if speaker == nil || speaker.State != config.ProviderInstanceStateEnabled || speaker.Protocol != string(core.ModelSurfaceAudioSpeech) {
		t.Fatalf("speech-only provider = %+v, want an enabled audio_speech instance", speaker)
	}
	if got := speaker.ExtensionSurfaces(); !slices.Equal(got, []string{"audio_speech"}) {
		t.Fatalf("instance surfaces = %v", got)
	}

	// The catalog's model carries the provider's surfaces.
	var all struct {
		Targets []providerTargetResponse `json:"targets"`
	}
	rec := extensionRequest(t, mux, http.MethodGet, "/api/provider-targets?all=true", nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &all); err != nil {
		t.Fatal(err)
	}
	const target = "ext-speaker/speaker-voice"
	idx := slices.IndexFunc(all.Targets, func(candidate providerTargetResponse) bool { return candidate.Target == target })
	if idx < 0 || !slices.Equal(all.Targets[idx].Surfaces, []string{"audio_speech"}) {
		t.Fatalf("targets = %+v, want %s serving audio_speech", all.Targets, target)
	}
	// A chat provider's models carry its chat surface the same way.
	if free := slices.IndexFunc(all.Targets, func(c providerTargetResponse) bool { return c.Target == "ext-free/free-model" }); free < 0 ||
		!slices.Equal(all.Targets[free].Surfaces, []string{"chat_completions"}) {
		t.Fatalf("targets = %+v, want ext-free/free-model serving chat_completions", all.Targets)
	}

	// Chat refuses it: shortlist, route and the Chat target list.
	if rec := extensionRequest(t, mux, http.MethodPost, "/api/active-models/add", map[string]any{"target": target}); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "does not serve chat") {
		t.Fatalf("shortlisting a speech model status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if rec := extensionRequest(t, mux, http.MethodPost, "/api/model-routes", map[string]any{"name": "speech", "targets": []string{target}}); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "does not serve chat") {
		t.Fatalf("routing a speech model status = %d, body=%s", rec.Code, rec.Body.String())
	}
	editSavedConfig(t, configPath, func(cfg *config.Config) { cfg.ActiveModels = []string{target, "ext-free/free-model"} })
	var chat struct {
		Targets []providerTargetResponse `json:"targets"`
	}
	rec = extensionRequest(t, mux, http.MethodGet, "/api/provider-targets", nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &chat); err != nil {
		t.Fatal(err)
	}
	if len(chat.Targets) != 1 || chat.Targets[0].Target != "ext-free/free-model" {
		t.Fatalf("Chat targets = %+v, want only the chat model", chat.Targets)
	}

	// Voice offers and accepts it as a text-to-speech target...
	var options struct {
		TTS []voiceOption `json:"tts"`
	}
	rec = extensionRequest(t, mux, http.MethodGet, "/api/voice/options", nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &options); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(options.TTS, func(option voiceOption) bool { return option.Target == target }) {
		t.Fatalf("voice tts options = %+v", options.TTS)
	}
	if rec := extensionRequest(t, mux, http.MethodPut, "/api/voice/config", map[string]any{"enabled": false, "tts_target": target}); rec.Code != http.StatusOK {
		t.Fatalf("voice config status = %d, body=%s", rec.Code, rec.Body.String())
	}
	// ...and synthesizes through the daemon.
	editSavedConfig(t, configPath, func(cfg *config.Config) { cfg.Voice.Enabled = true })
	rec = extensionRequest(t, mux, http.MethodPost, "/api/voice/synthesize", map[string]any{"input": "hello"})
	if rec.Code != http.StatusOK || rec.Body.String() != "OggS-speaker" || rec.Header().Get("Content-Type") != "audio/ogg" {
		t.Fatalf("synthesize = %d %q (%s)", rec.Code, rec.Body.String(), rec.Header().Get("Content-Type"))
	}
	// It does not transcribe: the daemon lists only audio_speech.
	if rec := extensionRequest(t, mux, http.MethodPut, "/api/voice/config", map[string]any{"enabled": false, "stt_target": target}); rec.Code != http.StatusBadRequest {
		t.Fatalf("speech-to-text on a speech-only provider status = %d, body=%s", rec.Code, rec.Body.String())
	}
}
