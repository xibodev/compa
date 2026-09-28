package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	core "github.com/xibodev/llmgw-core"

	"github.com/xibodev/compa/pkg/audio/voice"
	"github.com/xibodev/compa/pkg/config"
	"github.com/xibodev/compa/pkg/modelservice"
)

// Voice modes. A push-to-talk ("cascade") turn ends when the user stops
// recording, a hands-free ("live") one when voice activity detection hears
// the user stop. Voice runs with a speech-to-text target, a text-to-speech
// target, or both: speech-to-text alone dictates what the user says, and
// text-to-speech alone speaks the replies.
const (
	voiceModeCascade = "cascade"
	voiceModeLive    = "live"
)

const (
	// maxVoiceUpload bounds one recording sent for transcription.
	maxVoiceUpload = 32 << 20
	// voiceRequestTimeout bounds one transcription or synthesis.
	voiceRequestTimeout = 60 * time.Second
	// voicePlaybackFormat is the audio requested for the browser, which
	// plays MP3 everywhere; a provider that cannot return it answers in its
	// own format (such as WAV), which browsers play too.
	voicePlaybackFormat = "mp3"
)

func (h *Handler) registerVoiceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/voice/config", h.handleGetVoiceConfig)
	mux.HandleFunc("PUT /api/voice/config", h.handleUpdateVoiceConfig)
	mux.HandleFunc("GET /api/voice/options", h.handleVoiceOptions)
	mux.HandleFunc("POST /api/voice/transcribe", h.handleVoiceTranscribe)
	mux.HandleFunc("POST /api/voice/synthesize", h.handleVoiceSynthesize)
}

// voiceSettings is the voice configuration the API reads and writes whole.
type voiceSettings struct {
	Enabled bool   `json:"enabled"`
	Mode    string `json:"mode"`
	// STTTarget and TTSTarget are exact targets "instance-id/model-id".
	STTTarget string `json:"stt_target"`
	// STTViaChat makes STTTarget a chat model that transcribes.
	STTViaChat bool   `json:"stt_via_chat"`
	TTSTarget  string `json:"tts_target"`
	// TTSVoice is the synthesis voice; empty uses the provider's default.
	TTSVoice          string `json:"tts_voice"`
	EchoTranscription bool   `json:"echo_transcription"`
}

func voiceSettingsFromConfig(vc config.VoiceConfig) voiceSettings {
	mode := vc.Mode
	if mode == "" {
		mode = voiceModeCascade
	}
	return voiceSettings{
		Enabled:           vc.Enabled,
		Mode:              mode,
		STTTarget:         vc.STTTarget,
		STTViaChat:        vc.STTViaChat,
		TTSTarget:         vc.TTSTarget,
		TTSVoice:          vc.TTSVoice,
		EchoTranscription: vc.EchoTranscription,
	}
}

func (h *Handler) handleGetVoiceConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"config": voiceSettingsFromConfig(cfg.Voice),
	})
}

func (h *Handler) handleUpdateVoiceConfig(w http.ResponseWriter, r *http.Request) {
	var req voiceSettings
	if err := decodeStrictJSON(r, &req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid voice config: %v", err), http.StatusBadRequest)
		return
	}
	settings := config.VoiceConfig{
		Enabled:           req.Enabled,
		Mode:              strings.TrimSpace(req.Mode),
		STTTarget:         strings.TrimSpace(req.STTTarget),
		STTViaChat:        req.STTViaChat,
		TTSTarget:         strings.TrimSpace(req.TTSTarget),
		TTSVoice:          strings.TrimSpace(req.TTSVoice),
		EchoTranscription: req.EchoTranscription,
	}
	if settings.Mode == "" {
		settings.Mode = voiceModeCascade
	}
	if settings.Mode != voiceModeCascade && settings.Mode != voiceModeLive {
		http.Error(w, "Voice mode must be cascade or live", http.StatusBadRequest)
		return
	}
	// Voice needs something to do: dictation, spoken replies, or both.
	if settings.Enabled && settings.STTTarget == "" && settings.TTSTarget == "" {
		http.Error(w, "Voice needs a speech-to-text target, a text-to-speech target, or both", http.StatusBadRequest)
		return
	}
	// Chat transcription is an option of a speech-to-text target.
	if settings.STTTarget == "" {
		settings.STTViaChat = false
	}

	h.configMu.Lock()
	defer h.configMu.Unlock()
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}
	for _, target := range []struct {
		role   voice.Role
		target string
	}{
		{voice.SpeechToText, settings.STTTarget},
		{voice.TextToSpeech, settings.TTSTarget},
	} {
		if target.target == "" {
			continue
		}
		if err := checkVoiceTarget(cfg, target.target, target.role, settings.STTViaChat); err != nil {
			http.Error(w, fmt.Sprintf("Invalid %s target %q: %v", target.role, target.target, err), http.StatusBadRequest)
			return
		}
	}
	if settings.STTViaChat {
		if err := checkChatTranscriptionTarget(cfg, settings.STTTarget); err != nil {
			http.Error(w, fmt.Sprintf("Invalid %s target %q: %v", voice.SpeechToText, settings.STTTarget, err), http.StatusBadRequest)
			return
		}
	}
	cfg.Voice = settings
	if err := config.SaveConfig(h.configPath, cfg); err != nil {
		http.Error(w, fmt.Sprintf("Failed to save config: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"config": voiceSettingsFromConfig(cfg.Voice),
	})
}

// checkVoiceTarget reports whether target can serve role: its provider
// instance exists and is enabled, and its core provider serves the role's
// surface for the target's model (chat, when viaChat opts speech-to-text in
// to a chat model). It does not read the instance's credential.
func checkVoiceTarget(cfg *config.Config, target string, role voice.Role, viaChat bool) error {
	_, err := voice.New(cfg, target, role, voice.Options{
		ViaChat:  viaChat && role == voice.SpeechToText,
		Resolver: modelservice.NewResolver(modelservice.WithCredentialResolver(skipVoiceCredential)),
	})
	return err
}

// checkChatTranscriptionTarget reports whether target, a chat model voice
// transcribes with, takes audio: its saved catalog must declare audio input
// (see modelservice.CheckChatTranscriptionTarget).
func checkChatTranscriptionTarget(cfg *config.Config, target string) error {
	store, err := loadCatalogs()
	if err != nil {
		return fmt.Errorf("the model catalogs could not be read: %w", err)
	}
	return modelservice.CheckChatTranscriptionTarget(cfg, store, target)
}

func skipVoiceCredential(string) (string, error) {
	return "", nil
}

// voiceOption is a target voice can run on.
type voiceOption struct {
	Target       string `json:"target"`
	Label        string `json:"label"`
	ProviderKind string `json:"provider_kind"`
}

// handleVoiceOptions lists the targets voice can run on, from what the
// enabled instances' catalogs report: speech-to-text targets serve
// audio_transcriptions, text-to-speech targets audio_speech, and the chat
// models voice can transcribe with (stt_chat) serve chat_completions and
// declare audio input. Model IDs are never read for what a model does. The
// configured targets are listed too, except a configured chat model whose
// catalog no longer declares audio input: it cannot transcribe.
func (h *Handler) handleVoiceOptions(w http.ResponseWriter, r *http.Request) {
	cfg, store, err := h.loadProviderProjectionState()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	stt := make([]voiceOption, 0)
	tts := make([]voiceOption, 0)
	chat := make([]voiceOption, 0)
	for _, target := range providerTargetResponses(cfg, store) {
		option := voiceOption{Target: target.Target, Label: target.Label, ProviderKind: target.ProviderKind}
		if slices.Contains(target.Surfaces, string(core.ModelSurfaceAudioTranscriptions)) {
			stt = append(stt, option)
		}
		if slices.Contains(target.Surfaces, string(core.ModelSurfaceAudioSpeech)) {
			tts = append(tts, option)
		}
		if slices.Contains(target.Surfaces, string(core.ModelSurfaceChatCompletions)) && target.audioInput {
			chat = append(chat, option)
		}
	}
	configuredSTT := cfg.Voice.STTTarget
	if cfg.Voice.STTViaChat {
		for _, option := range chat {
			if !slices.ContainsFunc(stt, func(o voiceOption) bool { return o.Target == option.Target }) {
				stt = append(stt, option)
			}
		}
		sort.Slice(stt, func(i, j int) bool { return stt[i].Target < stt[j].Target })
		if !slices.ContainsFunc(chat, func(o voiceOption) bool { return o.Target == strings.TrimSpace(configuredSTT) }) {
			configuredSTT = ""
		}
	}
	instances := providerInstancesByID(cfg)
	writeJSON(w, http.StatusOK, map[string]any{
		"stt":      withConfiguredVoiceTarget(stt, configuredSTT, instances),
		"tts":      withConfiguredVoiceTarget(tts, cfg.Voice.TTSTarget, instances),
		"stt_chat": chat,
	})
}

// withConfiguredVoiceTarget returns options with configured, a configured
// voice target, among them.
func withConfiguredVoiceTarget(
	options []voiceOption,
	configured string,
	instances map[string]*config.ProviderInstanceConfig,
) []voiceOption {
	configured = strings.TrimSpace(configured)
	parsed, err := config.ParseExactModelTarget(configured)
	if err != nil {
		return options
	}
	for _, option := range options {
		if option.Target == configured {
			return options
		}
	}
	option := voiceOption{Target: configured, Label: parsed.ModelID}
	if instance := instances[parsed.InstanceID]; instance != nil {
		option.ProviderKind = instance.ProviderKind
	}
	options = append(options, option)
	sort.Slice(options, func(i, j int) bool { return options[i].Target < options[j].Target })
	return options
}

// voiceRequestConfig loads the config of a voice request. When it does not
// load, it answers the request itself and returns nil. Voice need not be on:
// voice.enabled only decides whether Chat shows its controls, so the voice
// settings can preview a voice and test the microphone before turning them
// on.
func (h *Handler) voiceRequestConfig(w http.ResponseWriter) *config.Config {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return nil
	}
	return cfg
}

// voiceClient returns the client of cfg's target for role, with the
// instance's credential. When the target is not configured or cannot serve
// role, it answers the request itself and returns nil: voice may run with
// only one of its targets, so a request for the other is refused clearly.
func (h *Handler) voiceClient(w http.ResponseWriter, cfg *config.Config, role voice.Role) *voice.Client {
	target := cfg.Voice.TTSTarget
	if role == voice.SpeechToText {
		target = cfg.Voice.STTTarget
	}
	client, err := voice.New(cfg, target, role, voice.Options{
		ViaChat:  role == voice.SpeechToText && cfg.Voice.STTViaChat,
		Resolver: modelservice.NewResolver(modelservice.WithCredentialResolver(h.providerCredentialResolver)),
	})
	if errors.Is(err, modelservice.ErrNoVoiceTarget) {
		action := "transcribe speech"
		if role == voice.TextToSpeech {
			action = "speak replies"
		}
		http.Error(w, fmt.Sprintf("Voice %s target is not configured: choose a %s model in the voice settings to %s", role, role, action), http.StatusBadRequest)
		return nil
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("Voice %s target failed: %v", role, err), http.StatusBadRequest)
		return nil
	}
	return client
}

func (h *Handler) handleVoiceTranscribe(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxVoiceUpload)
	cfg := h.voiceRequestConfig(w)
	if cfg == nil {
		return
	}
	client := h.voiceClient(w, cfg, voice.SpeechToText)
	if client == nil {
		return
	}
	if cfg.Voice.STTViaChat {
		if err := checkChatTranscriptionTarget(cfg, cfg.Voice.STTTarget); err != nil {
			http.Error(w, fmt.Sprintf("Voice %s target %q cannot transcribe: %v", voice.SpeechToText, cfg.Voice.STTTarget, err), http.StatusBadRequest)
			return
		}
	}
	audio, filename, err := readVoiceUpload(r)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid recording: %v", err), http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), voiceRequestTimeout)
	defer cancel()
	result, err := client.Transcribe(ctx, audio, filename)
	if err != nil {
		http.Error(w, fmt.Sprintf("Transcription failed: %v", err), http.StatusBadGateway)
		return
	}
	// A recording without speech is not a message: an empty transcript, or
	// one that only annotates silence, is never answered as a success.
	text, recognized := voice.RecognizedSpeech(result.Text)
	if !recognized {
		http.Error(w, "No speech was recognized", http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":   true,
		"text": text,
	})
}

// readVoiceUpload reads the recording of a transcription request: the file
// in a multipart form's file field, or the raw body. The file name it
// returns tells the provider the audio's format.
func readVoiceUpload(r *http.Request) ([]byte, string, error) {
	contentType := r.Header.Get("Content-Type")
	filename := "audio.wav"
	var data []byte
	if strings.HasPrefix(contentType, "multipart/form-data") {
		if err := r.ParseMultipartForm(maxVoiceUpload); err != nil {
			return nil, "", fmt.Errorf("parse multipart form: %w", err)
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			return nil, "", errors.New("the multipart form has no file")
		}
		defer file.Close()
		if data, err = io.ReadAll(file); err != nil {
			return nil, "", fmt.Errorf("read audio file: %w", err)
		}
		if header.Filename != "" {
			filename = header.Filename
		}
	} else {
		var err error
		if data, err = io.ReadAll(r.Body); err != nil {
			return nil, "", fmt.Errorf("read audio body: %w", err)
		}
		switch {
		case strings.Contains(contentType, "webm"):
			filename = "audio.webm"
		case strings.Contains(contentType, "ogg"):
			filename = "audio.ogg"
		case strings.Contains(contentType, "mpeg"), strings.Contains(contentType, "mp3"):
			filename = "audio.mp3"
		}
	}
	if len(data) == 0 {
		return nil, "", errors.New("the audio is empty")
	}
	return data, filename, nil
}

func (h *Handler) handleVoiceSynthesize(w http.ResponseWriter, r *http.Request) {
	cfg := h.voiceRequestConfig(w)
	if cfg == nil {
		return
	}
	client := h.voiceClient(w, cfg, voice.TextToSpeech)
	if client == nil {
		return
	}
	var req struct {
		Input string `json:"input"`
	}
	if err := decodeStrictJSON(r, &req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid synthesize payload: %v", err), http.StatusBadRequest)
		return
	}
	text := strings.TrimSpace(req.Input)
	if text == "" {
		http.Error(w, "Input text is required", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), voiceRequestTimeout)
	defer cancel()
	speech, err := client.Synthesize(ctx, text, cfg.Voice.TTSVoice, voicePlaybackFormat)
	if err != nil {
		http.Error(w, fmt.Sprintf("Speech synthesis failed: %v", err), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", speech.ContentType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(speech.Audio)
}
