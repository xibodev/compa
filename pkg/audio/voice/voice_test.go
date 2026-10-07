package voice

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	core "github.com/xibodev/llmgw-core"
	"github.com/xibodev/llmgw-core/extension"

	"github.com/xibodev/compa/v3/pkg/config"
	"github.com/xibodev/compa/v3/pkg/modelservice"
	"github.com/xibodev/compa/v3/pkg/providers"
)

func instance(id, kind, adapter, endpoint string) *config.ProviderInstanceConfig {
	return &config.ProviderInstanceConfig{
		ID: id, ProviderKind: kind, Adapter: adapter, Protocol: "openai",
		Endpoint: endpoint, AuthConnectionRef: "credential:" + id, State: config.ProviderInstanceStateEnabled,
	}
}

func secretResolver(secret string) *modelservice.Resolver {
	return modelservice.NewResolver(modelservice.WithCredentialResolver(func(string) (string, error) { return secret, nil }))
}

func newClient(t *testing.T, cfg *config.Config, target string, role Role, viaChat bool) *Client {
	t.Helper()
	client, err := New(cfg, target, role, Options{ViaChat: viaChat, Resolver: secretResolver("key")})
	if err != nil {
		t.Fatalf("New(%q) error = %v", target, err)
	}
	return client
}

// multipartFields reads a multipart request's fields and file.
func multipartFields(t *testing.T, r *http.Request) (map[string]string, string, []byte) {
	t.Helper()
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		t.Fatalf("parse multipart: %v", err)
	}
	fields := map[string]string{}
	for name, values := range r.MultipartForm.Value {
		fields[name] = values[0]
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		t.Fatalf("form file: %v", err)
	}
	defer file.Close()
	data, _ := io.ReadAll(file)
	return fields, header.Filename, data
}

func TestOpenAICompatibleTranscribesAndSpeaksThroughCore(t *testing.T) {
	var speech []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer key" || r.Header.Get("X-Extra") != "on" {
			t.Errorf("%s headers = %v", r.URL.Path, r.Header)
		}
		switch r.URL.Path {
		case "/v1/audio/transcriptions":
			fields, name, data := multipartFields(t, r)
			if fields["model"] != "whisper-1" || fields["response_format"] != "json" || name != "hello.wav" || string(data) != "RIFF" {
				t.Errorf("transcription request = %v %q %q", fields, name, data)
			}
			_, _ = w.Write([]byte(`{"text":"hello there","language":"en"}`))
		case "/v1/audio/speech":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			speech = append(speech, body)
			if body["response_format"] == "opus" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"message":"response_format opus is not supported"}}`))
				return
			}
			w.Header().Set("Content-Type", "audio/mpeg")
			_, _ = w.Write([]byte("mp3-bytes"))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	openai := instance("oa", "openai", config.ProviderAdapterOpenAICompatible, server.URL+"/v1/")
	openai.Headers = map[string]string{"X-Extra": "on"}
	cfg := &config.Config{ProviderInstances: []*config.ProviderInstanceConfig{openai}}

	result, err := newClient(t, cfg, "oa/whisper-1", SpeechToText, false).Transcribe(context.Background(), []byte("RIFF"), "hello.wav")
	if err != nil || result.Text != "hello there" || result.Language != "en" {
		t.Fatalf("Transcribe() = %#v, %v", result, err)
	}
	audio, err := newClient(t, cfg, "oa/tts-1", TextToSpeech, false).Synthesize(context.Background(), "hi", "alloy", "opus")
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	if string(audio.Audio) != "mp3-bytes" || audio.ContentType != "audio/mpeg" || audio.FileExt != ".mp3" {
		t.Fatalf("Synthesize() = %#v", audio)
	}
	if len(speech) != 2 || speech[0]["voice"] != "alloy" || speech[0]["input"] != "hi" || speech[0]["model"] != "tts-1" {
		t.Fatalf("speech requests = %v", speech)
	}
	if _, retried := speech[1]["response_format"]; retried {
		t.Fatalf("retry kept response_format: %v", speech[1])
	}
}

func TestSynthesizeTrustsTheReturnedContentTypeElseTheRequestedFormat(t *testing.T) {
	contentType := "audio/ogg"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write([]byte("audio"))
	}))
	defer server.Close()
	cfg := &config.Config{ProviderInstances: []*config.ProviderInstanceConfig{
		instance("oa", "openai", config.ProviderAdapterOpenAICompatible, server.URL),
	}}
	client := newClient(t, cfg, "oa/tts-1", TextToSpeech, false)
	if speech, err := client.Synthesize(context.Background(), "hi", "", "mp3"); err != nil || speech.FileExt != ".ogg" {
		t.Fatalf("declared ogg: %#v, %v", speech, err)
	}
	contentType = "application/octet-stream"
	if speech, err := client.Synthesize(context.Background(), "hi", "", "opus"); err != nil || speech.ContentType != "audio/ogg" {
		t.Fatalf("undeclared opus: %#v, %v", speech, err)
	}
}

func TestElevenLabsTranscribesAndSpeaksThroughCore(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("xi-api-key") != "key" {
			t.Errorf("xi-api-key = %q", r.Header.Get("xi-api-key"))
		}
		switch {
		case r.URL.Path == "/v1/speech-to-text":
			fields, _, _ := multipartFields(t, r)
			if fields["model_id"] != "scribe_v2" {
				t.Errorf("model_id = %q", fields["model_id"])
			}
			_, _ = w.Write([]byte(`{"text":"bonjour"}`))
		case r.URL.Path == "/v1/text-to-speech/rachel":
			w.Header().Set("Content-Type", "audio/mpeg")
			_, _ = w.Write([]byte("eleven-mp3"))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	cfg := &config.Config{ProviderInstances: []*config.ProviderInstanceConfig{
		instance("el", "elevenlabs", config.ProviderAdapterNative, server.URL+"/v1"),
	}}
	result, err := newClient(t, cfg, "el/scribe_v2", SpeechToText, false).Transcribe(context.Background(), []byte("RIFF"), "a.wav")
	if err != nil || result.Text != "bonjour" {
		t.Fatalf("Transcribe() = %#v, %v", result, err)
	}
	// ElevenLabs refuses Ogg Opus; the client asks again for its default.
	speech, err := newClient(t, cfg, "el/eleven_v3", TextToSpeech, false).Synthesize(context.Background(), "salut", "rachel", "opus")
	if err != nil || string(speech.Audio) != "eleven-mp3" || speech.FileExt != ".mp3" {
		t.Fatalf("Synthesize() = %#v, %v", speech, err)
	}
	if _, err := New(cfg, "el/whisper-1", SpeechToText, Options{Resolver: secretResolver("key")}); err == nil {
		t.Fatal("New(el/whisper-1) error = nil; ElevenLabs does not serve it")
	}
	if _, err := New(cfg, "el/scribe_v2", TextToSpeech, Options{Resolver: secretResolver("key")}); err == nil {
		t.Fatal("New(scribe_v2 as text-to-speech) error = nil")
	}
}

func TestMiMoSpeaksThroughCore(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Api-Key") != "key" {
			t.Errorf("request %s %v", r.URL.Path, r.Header)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if audio, _ := body["audio"].(map[string]any); audio["voice"] != "mimo_default" {
			t.Errorf("audio = %v", body["audio"])
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"audio":{"data":"` +
			base64.StdEncoding.EncodeToString([]byte("RIFFwav")) + `","format":"wav"}}}]}`))
	}))
	defer server.Close()
	cfg := &config.Config{ProviderInstances: []*config.ProviderInstanceConfig{
		instance("mimo", "mimo", config.ProviderAdapterNative, server.URL+"/v1"),
	}}
	speech, err := newClient(t, cfg, "mimo/mimo-v2.5-tts", TextToSpeech, false).Synthesize(context.Background(), "ni hao", "mimo_default", "opus")
	if err != nil || string(speech.Audio) != "RIFFwav" || speech.ContentType != "audio/wav" || speech.FileExt != ".wav" {
		t.Fatalf("Synthesize() = %#v, %v", speech, err)
	}
	if _, err := New(cfg, "mimo/mimo-v2.5-tts", SpeechToText, Options{Resolver: secretResolver("key")}); err == nil {
		t.Fatal("New(MiMo speech-to-text) error = nil")
	}
}

func TestExtensionSpeechThroughTheDaemon(t *testing.T) {
	original := providers.ExtensionDaemonSecret
	providers.ExtensionDaemonSecret = func() (string, error) { return "daemon-secret", nil }
	t.Cleanup(func() { providers.ExtensionDaemonSecret = original })
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer daemon-secret" {
			t.Errorf("daemon auth = %q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case extension.PathPrefix + "speaker/invoke":
			if r.Header.Get(extension.HeaderSurface) != "audio_speech" || r.Header.Get(extension.HeaderModel) != "voice-1" ||
				r.Header.Get(extension.HeaderCredentialToken) != "key" {
				t.Errorf("invoke headers = %v", r.Header)
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["input"] != "hello" {
				t.Errorf("invoke body = %v", body)
			}
			w.Header().Set("Content-Type", "audio/ogg")
			_, _ = w.Write([]byte("OggS"))
		default:
			t.Errorf("unexpected daemon path %s", r.URL.Path)
		}
	}))
	defer daemon.Close()
	// A speech-only daemon provider: its instance's protocol is its one
	// surface, and the default factory, providers.NewCoreProvider, builds it
	// from the surfaces the daemon listed. No daemon info call is made.
	ext := instance("ext", "speaker", config.ProviderAdapterExtension, daemon.URL)
	ext.Protocol = string(core.ModelSurfaceAudioSpeech)
	ext.Settings = map[string]any{
		config.ExtensionProviderSetting: "speaker", config.ExtensionCredentialSetting: "token",
		config.ExtensionSurfacesSetting: []any{"audio_speech"},
	}
	cfg := &config.Config{ProviderInstances: []*config.ProviderInstanceConfig{ext}}
	if err := cfg.ValidateProviderInstances(); err != nil {
		t.Fatalf("a speech-only extension instance is invalid: %v", err)
	}

	speech, err := newClient(t, cfg, "ext/voice-1", TextToSpeech, false).Synthesize(context.Background(), "hello", "", "opus")
	if err != nil || string(speech.Audio) != "OggS" || speech.FileExt != ".ogg" {
		t.Fatalf("Synthesize() = %#v, %v", speech, err)
	}
	if _, err := New(cfg, "ext/voice-1", SpeechToText, Options{Resolver: secretResolver("key")}); err == nil {
		t.Fatal("New(extension speech-to-text) error = nil; the daemon lists only audio_speech")
	}
}

func chatServer(t *testing.T, chats *int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		*chats++
		raw, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(raw), `"input_audio"`) || !strings.Contains(string(raw), `"model":"gpt-audio"`) {
			t.Errorf("chat body = %s", raw)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"via chat"},"finish_reason":"stop"}]}`))
	}))
}

func TestChatTranscriptionIsAnExplicitOptInForTheTargetModel(t *testing.T) {
	chats := 0
	server := chatServer(t, &chats)
	defer server.Close()
	cfg := &config.Config{ProviderInstances: []*config.ProviderInstanceConfig{
		instance("oa", "openai", config.ProviderAdapterOpenAICompatible, server.URL+"/v1"),
	}}
	result, err := newClient(t, cfg, "oa/gpt-audio", SpeechToText, true).Transcribe(context.Background(), []byte("RIFF"), "a.wav")
	if err != nil || result.Text != "via chat" || chats != 1 {
		t.Fatalf("Transcribe() = %#v, %v (chats %d)", result, err, chats)
	}

	target, err := secretResolver("key").ResolveVoiceTarget(cfg, "oa/gpt-audio")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := voiceProvider(target, SpeechToText, Options{ViaChat: true})
	if err != nil {
		t.Fatal(err)
	}
	// Only the target's model transcribes through chat, as a translated
	// surface; any other model transcribes on the provider's own endpoint.
	if !slices.Contains(Surfaces(provider, "gpt-audio"), core.ModelSurfaceAudioTranscriptions) ||
		core.ServesNatively(provider, "gpt-audio", core.ModelSurfaceAudioTranscriptions) ||
		!core.ServesNatively(provider, "other-model", core.ModelSurfaceAudioTranscriptions) {
		t.Fatal("chat transcription must be enabled for exactly the target's model")
	}

	// Without the opt-in no chat request transcribes.
	if _, err := newClient(t, cfg, "oa/gpt-audio", SpeechToText, false).Transcribe(context.Background(), []byte("RIFF"), "a.wav"); err == nil {
		t.Fatal("Transcribe() without stt_via_chat error = nil")
	}
	if chats != 1 {
		t.Fatalf("chat requests = %d, want 1", chats)
	}
	// A provider without chat cannot transcribe through it.
	mimo := &config.Config{ProviderInstances: []*config.ProviderInstanceConfig{
		instance("mimo", "mimo", config.ProviderAdapterNative, server.URL+"/v1"),
	}}
	if _, err := New(mimo, "mimo/mimo-v2.5-tts", SpeechToText, Options{ViaChat: true, Resolver: secretResolver("key")}); err == nil {
		t.Fatal("New(chat transcription on MiMo) error = nil")
	}
}

func TestNewRejectsUnusableTargets(t *testing.T) {
	disabled := instance("off", "openai", config.ProviderAdapterOpenAICompatible, "http://127.0.0.1:1")
	disabled.State = config.ProviderInstanceStateDisabled
	cfg := &config.Config{ProviderInstances: []*config.ProviderInstanceConfig{disabled}}
	for _, target := range []string{"", "no-slash", "missing/m", "off/m"} {
		if _, err := New(cfg, target, TextToSpeech, Options{Resolver: secretResolver("key")}); err == nil {
			t.Errorf("New(%q) error = nil", target)
		}
	}
}
