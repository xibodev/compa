package tts

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xibodev/compa/v4/pkg/auth"
	"github.com/xibodev/compa/v4/pkg/config"
	"github.com/xibodev/compa/v4/pkg/media"
)

func voiceInstance(id, kind, endpoint string) *config.ProviderInstanceConfig {
	return &config.ProviderInstanceConfig{
		ID: id, ProviderKind: kind, Adapter: config.ProviderAdapterOpenAICompatible, Protocol: "openai",
		Endpoint: endpoint, State: config.ProviderInstanceStateEnabled,
	}
}

func speechConfig(target, voice string, instances ...*config.ProviderInstanceConfig) *config.Config {
	return &config.Config{
		Voice:             config.VoiceConfig{TTSTarget: target, TTSVoice: voice},
		ProviderInstances: instances,
	}
}

func TestDetectTTSIsOffWithoutAUsableTarget(t *testing.T) {
	disabled := voiceInstance("disabled", "openai", "https://disabled.example.test/v1")
	disabled.State = config.ProviderInstanceStateDisabled
	anthropic := voiceInstance("claude", "anthropic", "https://claude.example.test/v1")
	anthropic.Adapter = config.ProviderAdapterAnthropicCompatible
	instances := []*config.ProviderInstanceConfig{voiceInstance("voice", "openai", "https://voice.example.test/v1"), disabled, anthropic}

	for name, cfg := range map[string]*config.Config{
		"nil config":        nil,
		"no target":         speechConfig("", "alloy", instances...),
		"malformed target":  speechConfig("tts-1", "", instances...),
		"missing instance":  speechConfig("gone/tts-1", "", instances...),
		"disabled instance": speechConfig("disabled/tts-1", "", instances...),
		"no audio surface":  speechConfig("claude/tts-1", "", instances...),
	} {
		t.Run(name, func(t *testing.T) {
			if provider := DetectTTS(cfg); provider != nil {
				t.Fatalf("DetectTTS() = %#v, want nil", provider)
			}
		})
	}
}

// Speech requests authenticate with the credential the instance's
// auth_connection_ref names, send the instance's headers and go through the
// instance's proxy, as the instance's model calls do.
func TestDetectTTSSendsThroughTheInstance(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	if err := auth.SetCredential("voice", &auth.AuthCredential{AccessToken: "stored-secret"}); err != nil {
		t.Fatal(err)
	}
	var gotURL, gotAuth, gotHeader string
	var gotBody map[string]any
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		gotAuth = r.Header.Get("Authorization")
		gotHeader = r.Header.Get("X-Voice")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = io.WriteString(w, "OggS-audio")
	}))
	defer proxy.Close()

	instance := voiceInstance("voice", "openai", "http://voice.example.invalid/v1")
	instance.AuthConnectionRef = "credential:voice"
	instance.Headers = map[string]string{"X-Voice": "voice"}
	instance.Runtime = &config.ProviderInstanceRuntime{Proxy: proxy.URL}
	provider := DetectTTS(speechConfig("voice/tts-1", "alloy", instance))
	if provider == nil {
		t.Fatal("DetectTTS() = nil")
	}

	stream, err := provider.Synthesize(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	defer stream.Close()
	if data, _ := io.ReadAll(stream); string(data) != "OggS-audio" {
		t.Errorf("audio = %q", data)
	}
	if gotURL != "http://voice.example.invalid/v1/audio/speech" {
		t.Errorf("proxied URL = %q, want the instance's speech URL", gotURL)
	}
	if gotAuth != "Bearer stored-secret" || gotHeader != "voice" {
		t.Errorf("Authorization = %q, X-Voice = %q", gotAuth, gotHeader)
	}
	if gotBody["model"] != "tts-1" || gotBody["voice"] != "alloy" || gotBody["response_format"] != "opus" {
		t.Errorf("request body = %#v", gotBody)
	}
}

type stubTTSProvider struct {
	name string
}

func (s stubTTSProvider) Name() string {
	return s.name
}

func (s stubTTSProvider) Synthesize(ctx context.Context, text string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("audio")), nil
}

func TestSynthesizeAndStore_UsesOggMetadataByDefault(t *testing.T) {
	t.Parallel()

	store := media.NewFileMediaStore()
	ref, err := SynthesizeAndStore(
		context.Background(),
		stubTTSProvider{name: "stub-tts"},
		store,
		"hello",
		"",
		"discord",
		"chat123",
	)
	if err != nil {
		t.Fatalf("SynthesizeAndStore failed: %v", err)
	}

	path, meta, err := store.ResolveWithMeta(ref)
	if err != nil {
		t.Fatalf("ResolveWithMeta failed: %v", err)
	}
	if meta.ContentType != "audio/ogg" {
		t.Fatalf("ContentType = %q, want %q", meta.ContentType, "audio/ogg")
	}
	if filepath.Ext(path) != ".ogg" {
		t.Fatalf("stored file extension = %q, want %q", filepath.Ext(path), ".ogg")
	}
	if filepath.Ext(meta.Filename) != ".ogg" {
		t.Fatalf("filename extension = %q, want %q", filepath.Ext(meta.Filename), ".ogg")
	}
}

func TestSynthesizeAndStore_UsesStreamProvidedAudioMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["response_format"] == "opus" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"response_format is invalid"}`))
			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("mp3-audio"))
	}))
	defer server.Close()

	store := media.NewFileMediaStore()
	provider := DetectTTS(speechConfig("xai/grok-voice-tts-1.0", "", voiceInstance("xai", "openai", server.URL)))
	if provider == nil {
		t.Fatal("DetectTTS() = nil")
	}
	ref, err := SynthesizeAndStore(
		context.Background(),
		provider,
		store,
		"hello",
		"reply.ogg",
		"telegram",
		"chat123",
	)
	if err != nil {
		t.Fatalf("SynthesizeAndStore failed: %v", err)
	}

	path, meta, err := store.ResolveWithMeta(ref)
	if err != nil {
		t.Fatalf("ResolveWithMeta failed: %v", err)
	}
	if meta.ContentType != "audio/mpeg" {
		t.Fatalf("ContentType = %q, want %q", meta.ContentType, "audio/mpeg")
	}
	if filepath.Ext(path) != ".mp3" {
		t.Fatalf("stored file extension = %q, want %q", filepath.Ext(path), ".mp3")
	}
	if meta.Filename != "reply.mp3" {
		t.Fatalf("filename = %q, want %q", meta.Filename, "reply.mp3")
	}
}

func TestSynthesizeAndStore_RejectsMissingInputs(t *testing.T) {
	t.Parallel()

	store := media.NewFileMediaStore()
	provider := stubTTSProvider{name: "stub-tts"}
	for name, call := range map[string]func() (string, error){
		"no provider": func() (string, error) {
			return SynthesizeAndStore(context.Background(), nil, store, "hello", "", "discord", "chat")
		},
		"no store": func() (string, error) {
			return SynthesizeAndStore(context.Background(), provider, nil, "hello", "", "discord", "chat")
		},
		"no chat": func() (string, error) {
			return SynthesizeAndStore(context.Background(), provider, store, "hello", "", "discord", "")
		},
		"no text": func() (string, error) {
			return SynthesizeAndStore(context.Background(), provider, store, "  ", "", "discord", "chat")
		},
	} {
		if _, err := call(); err == nil {
			t.Errorf("%s: SynthesizeAndStore() error = nil", name)
		}
	}
}
