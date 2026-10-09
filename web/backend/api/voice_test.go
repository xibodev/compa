package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xibodev/compa/v4/pkg/config"
)

func voiceTestInstance(id, kind, endpoint string) *config.ProviderInstanceConfig {
	return &config.ProviderInstanceConfig{
		ID: id, ProviderKind: kind, Adapter: config.ProviderAdapterOpenAICompatible, Protocol: "openai",
		Endpoint: endpoint, AuthConnectionRef: "credential:" + id, Headers: map[string]string{"X-Instance": id},
		State: config.ProviderInstanceStateEnabled,
	}
}

// voiceTestHandler serves the voice routes over a config holding voice and
// instances, resolving every credential reference to "secret-for-<ref>".
func voiceTestHandler(
	t *testing.T,
	voice config.VoiceConfig,
	instances ...*config.ProviderInstanceConfig,
) (*Handler, *http.ServeMux, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("COMPA_HOME", home)
	configPath := filepath.Join(home, "config.json")
	cfg := config.DefaultConfig()
	cfg.Voice = voice
	cfg.ProviderInstances = instances
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	h := NewHandler(configPath)
	h.providerCredentialResolver = func(ref string) (string, error) { return "secret-for-" + ref, nil }
	mux := http.NewServeMux()
	h.registerVoiceRoutes(mux)
	return h, mux, configPath
}

func voiceRequest(mux *http.ServeMux, method, path, contentType string, body io.Reader) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, body)
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	mux.ServeHTTP(recorder, request)
	return recorder
}

func decodeVoiceSettings(t *testing.T, recorder *httptest.ResponseRecorder) voiceSettings {
	t.Helper()
	var response struct {
		OK     bool          `json:"ok"`
		Config voiceSettings `json:"config"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || !response.OK {
		t.Fatalf("voice config response = %s (%v)", recorder.Body.String(), err)
	}
	return response.Config
}

func loadVoiceConfig(t *testing.T, configPath string) config.VoiceConfig {
	t.Helper()
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	return cfg.Voice
}

func TestVoiceConfigReadsAndWritesTheInstanceTargets(t *testing.T) {
	h, mux, configPath := voiceTestHandler(t, config.VoiceConfig{}, voiceTestInstance("voice", "openai", "https://voice.example.test/v1"))
	// Saving checks the targets without reading their credentials.
	h.providerCredentialResolver = func(string) (string, error) { return "", errors.New("vault sealed") }

	got := voiceRequest(mux, http.MethodGet, "/api/voice/config", "", nil)
	if got.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body = %s", got.Code, got.Body.String())
	}
	if settings := decodeVoiceSettings(t, got); settings != (voiceSettings{Mode: voiceModeCascade}) {
		t.Fatalf("default voice config = %#v", settings)
	}

	body := `{"enabled":true,"mode":"live","stt_target":" voice/whisper-1 ","tts_target":"voice/tts-1","tts_voice":" nova ","echo_transcription":true}`
	put := voiceRequest(mux, http.MethodPut, "/api/voice/config", "application/json", strings.NewReader(body))
	if put.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body = %s", put.Code, put.Body.String())
	}
	want := voiceSettings{
		Enabled: true, Mode: voiceModeLive, STTTarget: "voice/whisper-1", TTSTarget: "voice/tts-1",
		TTSVoice: "nova", EchoTranscription: true,
	}
	if settings := decodeVoiceSettings(t, put); settings != want {
		t.Fatalf("PUT response config = %#v, want %#v", settings, want)
	}
	saved := loadVoiceConfig(t, configPath)
	if saved != (config.VoiceConfig{
		Enabled: true, Mode: voiceModeLive, STTTarget: "voice/whisper-1", TTSTarget: "voice/tts-1",
		TTSVoice: "nova", EchoTranscription: true,
	}) {
		t.Fatalf("saved voice config = %#v", saved)
	}
	if settings := decodeVoiceSettings(t, voiceRequest(mux, http.MethodGet, "/api/voice/config", "", nil)); settings != want {
		t.Fatalf("GET after PUT = %#v, want %#v", settings, want)
	}

	// Voice off may keep its targets, or clear them.
	put = voiceRequest(mux, http.MethodPut, "/api/voice/config", "application/json", strings.NewReader(`{"enabled":false}`))
	if put.Code != http.StatusOK {
		t.Fatalf("PUT disabled status = %d, body = %s", put.Code, put.Body.String())
	}
	if saved := loadVoiceConfig(t, configPath); saved != (config.VoiceConfig{Mode: voiceModeCascade}) {
		t.Fatalf("saved voice config = %#v, want voice off without targets", saved)
	}
}

func TestVoiceConfigRejectsInvalidSettings(t *testing.T) {
	disabled := voiceTestInstance("off", "openai", "https://off.example.test/v1")
	disabled.State = config.ProviderInstanceStateDisabled
	anthropic := voiceTestInstance("claude", "anthropic", "https://claude.example.test/v1")
	anthropic.Adapter, anthropic.Protocol = config.ProviderAdapterAnthropicCompatible, "anthropic"
	original := config.VoiceConfig{Mode: voiceModeCascade, TTSVoice: "alloy"}
	eleven := voiceTestInstance("el", "elevenlabs", "https://el.example.test/v1")
	eleven.Adapter, eleven.Protocol = config.ProviderAdapterNative, "native"
	_, mux, configPath := voiceTestHandler(t, original, voiceTestInstance("voice", "openai", "https://voice.example.test/v1"), disabled, anthropic, eleven)

	for name, tc := range map[string]struct{ body, want string }{
		"removed endpoint field": {`{"stt_endpoint":"https://voice.example.test/v1/audio/transcriptions"}`, "unknown field"},
		"removed live key":       {`{"live_key":"secret"}`, "unknown field"},
		"removed tts model":      {`{"tts_model_name":"tts-1"}`, "unknown field"},
		"unknown mode":           {`{"mode":"duplex"}`, "cascade or live"},
		"enabled without target": {`{"enabled":true}`, "speech-to-text target, a text-to-speech target, or both"},
		"malformed target":       {`{"stt_target":"whisper-1"}`, "instance-id/model-id"},
		"missing instance":       {`{"stt_target":"gone/whisper-1"}`, "unavailable"},
		"disabled instance":      {`{"tts_target":"off/tts-1"}`, "unavailable"},
		"anthropic instance":     {`{"tts_target":"claude/tts-1"}`, "does not serve audio_speech"},
		"elevenlabs unknown":     {`{"tts_target":"el/tts-1"}`, "does not serve audio_speech"},
		"chat stt without chat":  {`{"stt_target":"el/scribe_v2","stt_via_chat":true}`, "cannot transcribe with chat model"},
		// The instance's catalog does not declare that the model takes audio.
		"chat stt without audio input": {`{"enabled":true,"stt_target":"voice/gpt-4o","stt_via_chat":true}`, "does not accept audio input"},
	} {
		t.Run(name, func(t *testing.T) {
			put := voiceRequest(mux, http.MethodPut, "/api/voice/config", "application/json", strings.NewReader(tc.body))
			if put.Code != http.StatusBadRequest || !strings.Contains(put.Body.String(), tc.want) {
				t.Fatalf("PUT %s status = %d, body = %q, want 400 containing %q", tc.body, put.Code, put.Body.String(), tc.want)
			}
		})
	}
	if saved := loadVoiceConfig(t, configPath); saved != original {
		t.Fatalf("rejected updates changed the voice config: %#v", saved)
	}
}

// voiceUpstream is an OpenAI-compatible voice endpoint recording what it is
// sent.
type voiceUpstream struct {
	*httptest.Server
	requests                    int
	path, auth, instance, model string
	filename                    string
	speech                      map[string]any
}

func newVoiceUpstream(t *testing.T, status int) *voiceUpstream {
	t.Helper()
	upstream := &voiceUpstream{}
	upstream.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream.requests++
		upstream.path, upstream.auth, upstream.instance = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("X-Instance")
		if status != http.StatusOK {
			http.Error(w, `{"error":"invalid_api_key"}`, status)
			return
		}
		switch r.URL.Path {
		case "/v1/audio/transcriptions":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Errorf("ParseMultipartForm() error = %v", err)
			}
			upstream.model = r.FormValue("model")
			if _, header, err := r.FormFile("file"); err == nil {
				upstream.filename = header.Filename
			}
			writeJSON(w, http.StatusOK, map[string]string{"text": "  voice target works \n"})
		case "/v1/audio/speech":
			upstream.speech = nil
			_ = json.NewDecoder(r.Body).Decode(&upstream.speech)
			_, _ = w.Write([]byte("MP3-AUDIO"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)
	return upstream
}

func multipartRecording(t *testing.T, filename string, audio []byte) (string, io.Reader) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(audio)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return writer.FormDataContentType(), &body
}

func TestVoiceTranscribeUsesTheTargetInstance(t *testing.T) {
	upstream := newVoiceUpstream(t, http.StatusOK)
	_, mux, _ := voiceTestHandler(t,
		config.VoiceConfig{Enabled: true, STTTarget: "voice/whisper-large-v3", TTSTarget: "voice/tts-1"},
		voiceTestInstance("voice", "voice", upstream.URL+"/v1"))

	contentType, body := multipartRecording(t, "recording.wav", []byte("RIFF"))
	transcribe := voiceRequest(mux, http.MethodPost, "/api/voice/transcribe", contentType, body)
	if transcribe.Code != http.StatusOK {
		t.Fatalf("transcribe status = %d, body = %s", transcribe.Code, transcribe.Body.String())
	}
	var response struct {
		OK   bool   `json:"ok"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(transcribe.Body.Bytes(), &response); err != nil || !response.OK || response.Text != "voice target works" {
		t.Fatalf("transcribe response = %s", transcribe.Body.String())
	}
	if upstream.path != "/v1/audio/transcriptions" || upstream.model != "whisper-large-v3" || upstream.filename != "recording.wav" {
		t.Errorf("upstream got path %q, model %q, file %q", upstream.path, upstream.model, upstream.filename)
	}
	if upstream.auth != "Bearer secret-for-credential:voice" || upstream.instance != "voice" {
		t.Errorf("upstream got Authorization %q, X-Instance %q", upstream.auth, upstream.instance)
	}

	raw := voiceRequest(mux, http.MethodPost, "/api/voice/transcribe", "audio/webm", strings.NewReader("WEBM"))
	if raw.Code != http.StatusOK || upstream.filename != "audio.webm" {
		t.Fatalf("raw transcribe status = %d, file = %q, body = %s", raw.Code, upstream.filename, raw.Body.String())
	}
	empty := voiceRequest(mux, http.MethodPost, "/api/voice/transcribe", "audio/wav", strings.NewReader(""))
	if empty.Code != http.StatusBadRequest {
		t.Fatalf("empty transcribe status = %d, body = %s", empty.Code, empty.Body.String())
	}
}

func TestVoiceSynthesizeUsesTheTargetInstanceAndVoice(t *testing.T) {
	upstream := newVoiceUpstream(t, http.StatusOK)
	_, mux, configPath := voiceTestHandler(t,
		config.VoiceConfig{Enabled: true, STTTarget: "voice/whisper-1", TTSTarget: "voice/gpt-4o-mini-tts", TTSVoice: "nova"},
		voiceTestInstance("voice", "voice", upstream.URL+"/v1"))

	synthesize := voiceRequest(mux, http.MethodPost, "/api/voice/synthesize", "application/json", strings.NewReader(`{"input":" hello "}`))
	if synthesize.Code != http.StatusOK || synthesize.Body.String() != "MP3-AUDIO" {
		t.Fatalf("synthesize status = %d, body = %s", synthesize.Code, synthesize.Body.String())
	}
	if got := synthesize.Header().Get("Content-Type"); got != "audio/mpeg" {
		t.Errorf("Content-Type = %q, want audio/mpeg", got)
	}
	if upstream.path != "/v1/audio/speech" || upstream.auth != "Bearer secret-for-credential:voice" || upstream.instance != "voice" {
		t.Errorf("upstream got path %q, Authorization %q, X-Instance %q", upstream.path, upstream.auth, upstream.instance)
	}
	wantSpeech := map[string]any{"model": "gpt-4o-mini-tts", "input": "hello", "voice": "nova", "response_format": "mp3"}
	for field, value := range wantSpeech {
		if upstream.speech[field] != value {
			t.Errorf("speech request %s = %#v, want %#v", field, upstream.speech[field], value)
		}
	}

	// Without a configured voice the provider speaks with its default one.
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Voice.TTSVoice = ""
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	if synthesize := voiceRequest(mux, http.MethodPost, "/api/voice/synthesize", "application/json", strings.NewReader(`{"input":"hello"}`)); synthesize.Code != http.StatusOK {
		t.Fatalf("synthesize status = %d, body = %s", synthesize.Code, synthesize.Body.String())
	}
	if voice, ok := upstream.speech["voice"]; ok {
		t.Errorf("speech request voice = %#v, want none", voice)
	}

	for _, body := range []string{`{"input":"  "}`, `{"input":"hello","voice":"alloy"}`, `{"text":"hello"}`, `not json`} {
		if rejected := voiceRequest(mux, http.MethodPost, "/api/voice/synthesize", "application/json", strings.NewReader(body)); rejected.Code != http.StatusBadRequest {
			t.Errorf("synthesize %s status = %d, want 400", body, rejected.Code)
		}
	}
}

func TestVoiceRunsOnElevenLabs(t *testing.T) {
	var speechPath, sttModel, key string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key = r.Header.Get("xi-api-key")
		switch {
		case r.URL.Path == "/v1/speech-to-text":
			_ = r.ParseMultipartForm(1 << 20)
			sttModel = r.FormValue("model_id")
			_, _ = w.Write([]byte(`{"text":"eleven"}`))
		case strings.HasPrefix(r.URL.Path, "/v1/text-to-speech/"):
			speechPath = r.URL.Path
			w.Header().Set("Content-Type", "audio/mpeg")
			_, _ = w.Write([]byte("ELEVEN-MP3"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	eleven := voiceTestInstance("el", "elevenlabs", upstream.URL+"/v1")
	eleven.Adapter, eleven.Protocol = config.ProviderAdapterNative, "native"
	_, mux, _ := voiceTestHandler(t,
		config.VoiceConfig{Enabled: true, STTTarget: "el/scribe_v2", TTSTarget: "el/eleven_v3", TTSVoice: "rachel"}, eleven)

	transcribe := voiceRequest(mux, http.MethodPost, "/api/voice/transcribe", "audio/wav", strings.NewReader("RIFF"))
	if transcribe.Code != http.StatusOK || !strings.Contains(transcribe.Body.String(), `"eleven"`) || sttModel != "scribe_v2" {
		t.Fatalf("transcribe status = %d, body = %s, model = %q", transcribe.Code, transcribe.Body.String(), sttModel)
	}
	synthesize := voiceRequest(mux, http.MethodPost, "/api/voice/synthesize", "application/json", strings.NewReader(`{"input":"hello"}`))
	if synthesize.Code != http.StatusOK || synthesize.Body.String() != "ELEVEN-MP3" || synthesize.Header().Get("Content-Type") != "audio/mpeg" {
		t.Fatalf("synthesize status = %d, body = %s", synthesize.Code, synthesize.Body.String())
	}
	if speechPath != "/v1/text-to-speech/rachel" || key != "secret-for-credential:el" {
		t.Errorf("speech path = %q, key = %q", speechPath, key)
	}
}

func TestVoiceTranscribesWithAChatModelOnlyWhenOptedIn(t *testing.T) {
	chats := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		chats++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"chat heard you"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()
	saveAudioChatCatalog := func(t *testing.T) {
		t.Helper()
		if err := saveCatalogs(&CatalogStore{Entries: map[string]*CatalogEntry{
			"voice": {ID: "voice", InstanceID: "voice", Provider: "openai", Models: []CatalogModel{
				{ID: "gpt-audio", Surfaces: []string{"chat_completions"}, InputModalities: []string{"audio", "text"}, AudioInput: true},
				{ID: "gpt-text", Surfaces: []string{"chat_completions"}, InputModalities: []string{"text"}},
			}},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	_, mux, configPath := voiceTestHandler(t,
		config.VoiceConfig{Enabled: true, STTTarget: "voice/gpt-audio", STTViaChat: true, TTSTarget: "voice/tts-1"},
		voiceTestInstance("voice", "openai", upstream.URL+"/v1"))
	saveAudioChatCatalog(t)
	transcribe := voiceRequest(mux, http.MethodPost, "/api/voice/transcribe", "audio/wav", strings.NewReader("RIFF"))
	if transcribe.Code != http.StatusOK || !strings.Contains(transcribe.Body.String(), "chat heard you") || chats != 1 {
		t.Fatalf("transcribe status = %d, body = %s, chats = %d", transcribe.Code, transcribe.Body.String(), chats)
	}

	// A chat model whose catalog does not declare audio input never
	// transcribes: it would answer a recording it never heard.
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Voice.STTTarget = "voice/gpt-text"
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	if transcribe := voiceRequest(mux, http.MethodPost, "/api/voice/transcribe", "audio/wav", strings.NewReader("RIFF")); transcribe.Code != http.StatusBadRequest ||
		!strings.Contains(transcribe.Body.String(), "does not accept audio input") || chats != 1 {
		t.Fatalf("transcribe with a text-only chat model status = %d, body = %s, chats = %d", transcribe.Code, transcribe.Body.String(), chats)
	}

	_, mux, _ = voiceTestHandler(t,
		config.VoiceConfig{Enabled: true, STTTarget: "voice/gpt-audio", TTSTarget: "voice/tts-1"},
		voiceTestInstance("voice", "openai", upstream.URL+"/v1"))
	saveAudioChatCatalog(t)
	if transcribe := voiceRequest(mux, http.MethodPost, "/api/voice/transcribe", "audio/wav", strings.NewReader("RIFF")); transcribe.Code != http.StatusBadGateway || chats != 1 {
		t.Fatalf("transcribe without stt_via_chat status = %d, chats = %d", transcribe.Code, chats)
	}
}

// Voice runs with one target as well as with both: text-to-speech alone
// speaks the replies, speech-to-text alone dictates. A request for the
// missing one is refused clearly.
func TestVoiceRunsWithOnlyOneTarget(t *testing.T) {
	upstream := newVoiceUpstream(t, http.StatusOK)
	instance := voiceTestInstance("voice", "voice", upstream.URL+"/v1")
	_, mux, configPath := voiceTestHandler(t, config.VoiceConfig{}, instance)

	put := voiceRequest(mux, http.MethodPut, "/api/voice/config", "application/json", strings.NewReader(`{"enabled":true,"tts_target":"voice/tts-1"}`))
	if put.Code != http.StatusOK {
		t.Fatalf("PUT text-to-speech only status = %d, body = %s", put.Code, put.Body.String())
	}
	want := voiceSettings{Enabled: true, Mode: voiceModeCascade, TTSTarget: "voice/tts-1"}
	if got := decodeVoiceSettings(t, voiceRequest(mux, http.MethodGet, "/api/voice/config", "", nil)); got != want {
		t.Fatalf("GET after text-to-speech only = %#v, want %#v", got, want)
	}
	if synthesize := voiceRequest(mux, http.MethodPost, "/api/voice/synthesize", "application/json", strings.NewReader(`{"input":"hello"}`)); synthesize.Code != http.StatusOK {
		t.Fatalf("synthesize status = %d, body = %s", synthesize.Code, synthesize.Body.String())
	}
	transcribe := voiceRequest(mux, http.MethodPost, "/api/voice/transcribe", "audio/wav", strings.NewReader("RIFF"))
	if transcribe.Code != http.StatusBadRequest || !strings.Contains(transcribe.Body.String(), "speech-to-text target is not configured") {
		t.Fatalf("transcribe without a speech-to-text target status = %d, body = %q", transcribe.Code, transcribe.Body.String())
	}

	put = voiceRequest(mux, http.MethodPut, "/api/voice/config", "application/json", strings.NewReader(`{"enabled":true,"mode":"live","stt_target":"voice/whisper-1","stt_via_chat":false}`))
	if put.Code != http.StatusOK {
		t.Fatalf("PUT speech-to-text only status = %d, body = %s", put.Code, put.Body.String())
	}
	if saved := loadVoiceConfig(t, configPath); saved != (config.VoiceConfig{Enabled: true, Mode: voiceModeLive, STTTarget: "voice/whisper-1"}) {
		t.Fatalf("saved speech-to-text only config = %#v", saved)
	}
	if transcribe := voiceRequest(mux, http.MethodPost, "/api/voice/transcribe", "audio/wav", strings.NewReader("RIFF")); transcribe.Code != http.StatusOK {
		t.Fatalf("transcribe status = %d, body = %s", transcribe.Code, transcribe.Body.String())
	}
	synthesize := voiceRequest(mux, http.MethodPost, "/api/voice/synthesize", "application/json", strings.NewReader(`{"input":"hello"}`))
	if synthesize.Code != http.StatusBadRequest || !strings.Contains(synthesize.Body.String(), "text-to-speech target is not configured") {
		t.Fatalf("synthesize without a text-to-speech target status = %d, body = %q", synthesize.Code, synthesize.Body.String())
	}
	// Chat transcription is an option of a speech-to-text target: without
	// one it does not stick.
	put = voiceRequest(mux, http.MethodPut, "/api/voice/config", "application/json", strings.NewReader(`{"enabled":true,"tts_target":"voice/tts-1","stt_via_chat":true}`))
	if put.Code != http.StatusOK || decodeVoiceSettings(t, put).STTViaChat {
		t.Fatalf("PUT stt_via_chat without a target status = %d, body = %s", put.Code, put.Body.String())
	}
}

// A transcript without speech is never a successful transcription: the
// web chat would send it as the user's message.
func TestVoiceTranscribeWithoutSpeechIsUnprocessable(t *testing.T) {
	for _, transcript := range []string{"", "  \n ", "[BLANK_AUDIO]", "(silence)", "..."} {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, map[string]string{"text": transcript})
		}))
		_, mux, _ := voiceTestHandler(t,
			config.VoiceConfig{Enabled: true, STTTarget: "voice/whisper-1"},
			voiceTestInstance("voice", "voice", upstream.URL+"/v1"))
		transcribe := voiceRequest(mux, http.MethodPost, "/api/voice/transcribe", "audio/wav", strings.NewReader("RIFF"))
		upstream.Close()
		if transcribe.Code != http.StatusUnprocessableEntity || !strings.Contains(transcribe.Body.String(), "No speech was recognized") {
			t.Errorf("transcript %q: status = %d, body = %q, want 422 No speech was recognized", transcript, transcribe.Code, transcribe.Body.String())
		}
	}
}

// A chat model voice transcribes with must be one whose catalog declares
// audio input.
func TestVoiceConfigAcceptsChatTranscriptionOnlyWithAudioInput(t *testing.T) {
	_, mux, configPath := voiceTestHandler(t, config.VoiceConfig{}, voiceTestInstance("voice", "openai", "https://voice.example.test/v1"))
	if err := saveCatalogs(&CatalogStore{Entries: map[string]*CatalogEntry{
		"voice": {ID: "voice", InstanceID: "voice", Provider: "openai", Models: []CatalogModel{
			{ID: "gpt-audio", Surfaces: []string{"chat_completions"}, InputModalities: []string{"audio", "text"}, AudioInput: true},
			{ID: "gpt-text", Surfaces: []string{"chat_completions"}, InputModalities: []string{"text"}},
		}},
	}}); err != nil {
		t.Fatal(err)
	}
	rejected := voiceRequest(mux, http.MethodPut, "/api/voice/config", "application/json", strings.NewReader(`{"enabled":true,"stt_target":"voice/gpt-text","stt_via_chat":true}`))
	if rejected.Code != http.StatusBadRequest || !strings.Contains(rejected.Body.String(), "does not accept audio input") {
		t.Fatalf("PUT a text-only chat model status = %d, body = %q", rejected.Code, rejected.Body.String())
	}
	accepted := voiceRequest(mux, http.MethodPut, "/api/voice/config", "application/json", strings.NewReader(`{"enabled":true,"stt_target":"voice/gpt-audio","stt_via_chat":true}`))
	if accepted.Code != http.StatusOK {
		t.Fatalf("PUT an audio chat model status = %d, body = %s", accepted.Code, accepted.Body.String())
	}
	if saved := loadVoiceConfig(t, configPath); saved != (config.VoiceConfig{Enabled: true, Mode: voiceModeCascade, STTTarget: "voice/gpt-audio", STTViaChat: true}) {
		t.Fatalf("saved = %#v", saved)
	}
}

// A voice request needs its target, not voice turned on: the voice settings
// preview a voice and test the microphone before Chat shows voice.
func TestVoiceRequestsNeedTheirTarget(t *testing.T) {
	upstream := newVoiceUpstream(t, http.StatusOK)
	instance := voiceTestInstance("voice", "voice", upstream.URL+"/v1")

	_, mux, _ := voiceTestHandler(t, config.VoiceConfig{TTSTarget: "voice/tts-1"}, instance)
	previewed := voiceRequest(mux, http.MethodPost, "/api/voice/synthesize", "application/json", strings.NewReader(`{"input":"hello"}`))
	if previewed.Code != http.StatusOK {
		t.Errorf("synthesize with voice off status = %d, body = %q; want the preview spoken", previewed.Code, previewed.Body.String())
	}

	// A config file can enable voice without its targets.
	_, mux, _ = voiceTestHandler(t, config.VoiceConfig{Enabled: true}, instance)
	for _, path := range []string{"/api/voice/transcribe", "/api/voice/synthesize"} {
		rejected := voiceRequest(mux, http.MethodPost, path, "application/json", strings.NewReader(`{"input":"hello"}`))
		if rejected.Code != http.StatusBadRequest || !strings.Contains(rejected.Body.String(), "not configured") {
			t.Errorf("targetless %s status = %d, body = %q", path, rejected.Code, rejected.Body.String())
		}
	}

	_, mux, _ = voiceTestHandler(t, config.VoiceConfig{Enabled: true, STTTarget: "gone/whisper-1", TTSTarget: "gone/tts-1"}, instance)
	for _, path := range []string{"/api/voice/transcribe", "/api/voice/synthesize"} {
		rejected := voiceRequest(mux, http.MethodPost, path, "application/json", strings.NewReader(`{"input":"hello"}`))
		if rejected.Code != http.StatusBadRequest || !strings.Contains(rejected.Body.String(), "unavailable") {
			t.Errorf("dangling %s status = %d, body = %q", path, rejected.Code, rejected.Body.String())
		}
	}
	if upstream.requests != 1 {
		t.Errorf("upstream requests = %d, want only the preview's", upstream.requests)
	}
}

func TestVoiceUpstreamFailuresAreBadGateway(t *testing.T) {
	upstream := newVoiceUpstream(t, http.StatusUnauthorized)
	_, mux, _ := voiceTestHandler(t,
		config.VoiceConfig{Enabled: true, STTTarget: "voice/whisper-1", TTSTarget: "voice/tts-1"},
		voiceTestInstance("voice", "voice", upstream.URL+"/v1"))

	transcribe := voiceRequest(mux, http.MethodPost, "/api/voice/transcribe", "audio/wav", strings.NewReader("RIFF"))
	synthesize := voiceRequest(mux, http.MethodPost, "/api/voice/synthesize", "application/json", strings.NewReader(`{"input":"hello"}`))
	for name, recorder := range map[string]*httptest.ResponseRecorder{"transcribe": transcribe, "synthesize": synthesize} {
		if recorder.Code != http.StatusBadGateway || !strings.Contains(recorder.Body.String(), "HTTP 401") {
			t.Errorf("%s status = %d, body = %q, want 502 quoting the upstream status", name, recorder.Code, recorder.Body.String())
		}
	}
}

func TestVoiceOptionsListTheTargetsBySurface(t *testing.T) {
	disabled := voiceTestInstance("off", "openai", "https://off.example.test/v1")
	disabled.State = config.ProviderInstanceStateDisabled
	settings := config.VoiceConfig{STTTarget: "openai/custom-asr", TTSTarget: "openai/tts-1"}
	_, mux, configPath := voiceTestHandler(t, settings,
		voiceTestInstance("openai", "openai", "https://api.openai.example.test/v1"), disabled)
	model := func(id string, surfaces ...string) CatalogModel { return CatalogModel{ID: id, Surfaces: surfaces} }
	audioChat := CatalogModel{ID: "gpt-audio", DisplayName: "GPT Audio", Surfaces: []string{"chat_completions"}, InputModalities: []string{"audio", "text"}, AudioInput: true}
	if err := saveCatalogs(&CatalogStore{Entries: map[string]*CatalogEntry{
		"openai": {ID: "openai", InstanceID: "openai", Provider: "openai", Models: []CatalogModel{
			// A chat model whose catalog declares no audio input cannot
			// transcribe.
			model("gpt-4o", "chat_completions", "responses"),
			audioChat,
			model("whisper-1", "audio_transcriptions"),
			model("scribe", "audio_transcriptions"),
			model("speaker", "audio_speech"),
			// Names never decide: this model reports no surface.
			model("tts-whisper"),
		}},
		"off": {ID: "off", InstanceID: "off", Provider: "openai", Models: []CatalogModel{
			model("whisper-1", "audio_transcriptions"), model("tts-1", "audio_speech"), audioChat,
		}},
	}}); err != nil {
		t.Fatalf("saveCatalogs() error = %v", err)
	}
	targets := func(options []voiceOption) string {
		names := make([]string, 0, len(options))
		for _, option := range options {
			names = append(names, option.Target+"|"+option.Label+"|"+option.ProviderKind)
		}
		return strings.Join(names, ",")
	}
	read := func() (stt, tts, chat string) {
		options := voiceRequest(mux, http.MethodGet, "/api/voice/options", "", nil)
		if options.Code != http.StatusOK {
			t.Fatalf("options status = %d, body = %s", options.Code, options.Body.String())
		}
		var got struct {
			STT     []voiceOption `json:"stt"`
			TTS     []voiceOption `json:"tts"`
			STTChat []voiceOption `json:"stt_chat"`
		}
		if err := json.Unmarshal(options.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return targets(got.STT), targets(got.TTS), targets(got.STTChat)
	}

	stt, tts, chat := read()
	if want := "openai/custom-asr|custom-asr|openai,openai/scribe|scribe|openai,openai/whisper-1|whisper-1|openai"; stt != want {
		t.Errorf("stt options = %s, want %s", stt, want)
	}
	if want := "openai/speaker|speaker|openai,openai/tts-1|tts-1|openai"; tts != want {
		t.Errorf("tts options = %s, want %s", tts, want)
	}
	if want := "openai/gpt-audio|GPT Audio|openai"; chat != want {
		t.Errorf("stt_chat options = %s, want %s", chat, want)
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Voice = config.VoiceConfig{STTTarget: "openai/gpt-audio", STTViaChat: true}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	stt, _, chat = read()
	if want := "openai/gpt-audio|GPT Audio|openai,openai/scribe|scribe|openai,openai/whisper-1|whisper-1|openai"; stt != want {
		t.Errorf("stt options with stt_via_chat = %s, want %s", stt, want)
	}
	if want := "openai/gpt-audio|GPT Audio|openai"; chat != want {
		t.Errorf("stt_chat options with stt_via_chat = %s, want %s", chat, want)
	}

	// A configured chat model that takes no audio is not offered at all.
	cfg.Voice = config.VoiceConfig{STTTarget: "openai/gpt-4o", STTViaChat: true}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	stt, _, chat = read()
	if strings.Contains(stt, "gpt-4o") || strings.Contains(chat, "gpt-4o") {
		t.Errorf("a chat model without audio input is offered: stt = %s, stt_chat = %s", stt, chat)
	}
}
