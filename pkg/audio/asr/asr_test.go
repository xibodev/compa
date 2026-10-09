package asr

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/xibodev/compa/v4/pkg/auth"
	"github.com/xibodev/compa/v4/pkg/config"
	"github.com/xibodev/compa/v4/pkg/modelservice"
)

func voiceInstance(id, endpoint string) *config.ProviderInstanceConfig {
	return &config.ProviderInstanceConfig{
		ID: id, ProviderKind: "openai", Adapter: config.ProviderAdapterOpenAICompatible, Protocol: "openai",
		Endpoint: endpoint, State: config.ProviderInstanceStateEnabled,
	}
}

func voiceConfig(sttTarget string, instances ...*config.ProviderInstanceConfig) *config.Config {
	return &config.Config{
		Voice:             config.VoiceConfig{STTTarget: sttTarget},
		ProviderInstances: instances,
	}
}

func TestDetectTranscriberIsOffWithoutAUsableTarget(t *testing.T) {
	disabled := voiceInstance("disabled", "https://disabled.example.test/v1")
	disabled.State = config.ProviderInstanceStateDisabled
	anthropic := voiceInstance("claude", "https://claude.example.test/v1")
	anthropic.Adapter = config.ProviderAdapterAnthropicCompatible
	anthropic.ProviderKind = "anthropic"
	instances := []*config.ProviderInstanceConfig{voiceInstance("voice", "https://voice.example.test/v1"), disabled, anthropic}

	for name, cfg := range map[string]*config.Config{
		"nil config":        nil,
		"no target":         voiceConfig("", instances...),
		"blank target":      voiceConfig("  ", instances...),
		"malformed target":  voiceConfig("whisper-1", instances...),
		"missing instance":  voiceConfig("gone/whisper-1", instances...),
		"disabled instance": voiceConfig("disabled/whisper-1", instances...),
		"no audio surface":  voiceConfig("claude/whisper-1", instances...),
	} {
		t.Run(name, func(t *testing.T) {
			if tr := DetectTranscriber(cfg); tr != nil {
				t.Fatalf("DetectTranscriber() = %#v, want nil", tr)
			}
		})
	}
}

// The transcriber authenticates with the credential the instance's
// auth_connection_ref names, sends the instance's headers and goes through
// the instance's proxy, as the instance's model calls do.
func TestDetectTranscriberSendsThroughTheInstance(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	if err := auth.SetCredential("voice", &auth.AuthCredential{AccessToken: "stored-secret"}); err != nil {
		t.Fatal(err)
	}
	var gotURL, gotAuth, gotHeader, gotModel string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		gotAuth = r.Header.Get("Authorization")
		gotHeader = r.Header.Get("X-Voice")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("ParseMultipartForm() error = %v", err)
		}
		gotModel = r.FormValue("model")
		_ = json.NewEncoder(w).Encode(TranscriptionResponse{Text: "through the proxy"})
	}))
	defer proxy.Close()

	instance := voiceInstance("voice", "http://voice.example.invalid/v1")
	instance.AuthConnectionRef = "credential:voice"
	instance.Headers = map[string]string{"X-Voice": "voice"}
	instance.Runtime = &config.ProviderInstanceRuntime{Proxy: proxy.URL}
	tr := DetectTranscriber(voiceConfig("voice/whisper-1", instance))
	if tr == nil {
		t.Fatal("DetectTranscriber() = nil")
	}
	clip := filepath.Join(t.TempDir(), "clip.wav")
	if err := os.WriteFile(clip, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	resp, err := tr.Transcribe(context.Background(), clip)
	if err != nil {
		t.Fatalf("Transcribe() error = %v", err)
	}
	if resp.Text != "through the proxy" {
		t.Errorf("Text = %q", resp.Text)
	}
	if gotURL != "http://voice.example.invalid/v1/audio/transcriptions" {
		t.Errorf("proxied URL = %q, want the instance's transcription URL", gotURL)
	}
	if gotAuth != "Bearer stored-secret" || gotHeader != "voice" || gotModel != "whisper-1" {
		t.Errorf("request auth = %q, X-Voice = %q, model = %q", gotAuth, gotHeader, gotModel)
	}
}

// A chat model transcribes only when its catalog declares audio input: one
// that takes no audio would answer a recording it never heard.
func TestDetectTranscriberNeedsAChatModelThatTakesAudio(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	instance := voiceInstance("voice", "https://voice.example.test/v1")
	cfg := voiceConfig("voice/gpt-text", instance)
	cfg.Voice.STTViaChat = true
	if err := modelservice.SaveProviderInstanceCatalog(instance, []modelservice.CatalogModel{
		{ID: "gpt-text", Surfaces: []string{"chat_completions"}},
		{ID: "gpt-audio", Surfaces: []string{"chat_completions"}, AudioInput: true},
	}); err != nil {
		t.Fatal(err)
	}
	if tr := DetectTranscriber(cfg); tr != nil {
		t.Fatalf("DetectTranscriber() = %#v for a chat model without audio input, want nil", tr)
	}
	cfg.Voice.STTTarget = "voice/gpt-audio"
	if tr := DetectTranscriber(cfg); tr == nil {
		t.Fatal("DetectTranscriber() = nil for a chat model that takes audio")
	}
}

// A transcript without speech reads as empty, so it never becomes a message.
func TestTranscriberDropsTranscriptsWithoutSpeech(t *testing.T) {
	for _, transcript := range []string{"  ", "[BLANK_AUDIO]", "(silence)"} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(TranscriptionResponse{Text: transcript})
		}))
		tr := DetectTranscriber(voiceConfig("voice/whisper-1", voiceInstance("voice", server.URL+"/v1")))
		if tr == nil {
			server.Close()
			t.Fatal("DetectTranscriber() = nil")
		}
		clip := filepath.Join(t.TempDir(), "clip.wav")
		if err := os.WriteFile(clip, []byte("audio"), 0o600); err != nil {
			t.Fatal(err)
		}
		resp, err := tr.Transcribe(context.Background(), clip)
		server.Close()
		if err != nil || resp.Text != "" {
			t.Errorf("transcript %q: Text = %q, err = %v, want empty", transcript, resp.Text, err)
		}
	}
}

// An audio file over the size speech-to-text APIs accept is refused before
// it is read into memory and sent.
func TestTranscriberRefusesAnOversizedFile(t *testing.T) {
	var sent atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent.Store(true)
		_ = json.NewEncoder(w).Encode(TranscriptionResponse{Text: "heard"})
	}))
	defer server.Close()
	tr := DetectTranscriber(voiceConfig("voice/whisper-1", voiceInstance("voice", server.URL+"/v1")))
	if tr == nil {
		t.Fatal("DetectTranscriber() = nil")
	}

	clip := filepath.Join(t.TempDir(), "long.ogg")
	f, err := os.Create(clip)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxAudioFileBytes + 1); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()

	if _, err := tr.Transcribe(context.Background(), clip); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("Transcribe() error = %v, want the file refused as too large", err)
	}
	if sent.Load() {
		t.Fatal("the oversized file was sent")
	}
}
