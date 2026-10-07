// Package voice runs speech-to-text and text-to-speech for a voice target
// "instance-id/model-id" on the llmgw-core provider of the target's instance:
// transcription is an Invoke on the audio_transcriptions surface with the
// OpenAI multipart transcription body, synthesis one on audio_speech with the
// OpenAI speech JSON. OpenAI-compatible endpoints, ElevenLabs, MiMo and
// extension providers all go through the same code.
package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"unicode"

	core "github.com/xibodev/llmgw-core"
	"github.com/xibodev/llmgw-core/translation"

	"github.com/xibodev/compa/v3/pkg/config"
	"github.com/xibodev/compa/v3/pkg/modelservice"
	"github.com/xibodev/compa/v3/pkg/providers"
	"github.com/xibodev/compa/v3/pkg/providers/coretransport"
)

// Role is what a voice target does.
type Role int

const (
	// SpeechToText transcribes audio: the audio_transcriptions surface.
	SpeechToText Role = iota
	// TextToSpeech synthesizes speech: the audio_speech surface.
	TextToSpeech
)

func (r Role) surface() core.ModelSurface {
	if r == TextToSpeech {
		return core.ModelSurfaceAudioSpeech
	}
	return core.ModelSurfaceAudioTranscriptions
}

func (r Role) String() string {
	if r == TextToSpeech {
		return "text-to-speech"
	}
	return "speech-to-text"
}

// Options configure New.
type Options struct {
	// ViaChat serves SpeechToText through the target's chat model with
	// llmgw-core's ChatTranscriptionAdapter, opted in for exactly the
	// target's model (config.VoiceConfig.STTViaChat).
	ViaChat bool
	// Resolver resolves the target and its secret; nil reads the auth store.
	Resolver *modelservice.Resolver
	// NewProvider builds the instance's core provider; nil uses
	// providers.NewCoreProvider, as for every instance (an extension
	// instance's provider serves the surfaces the daemon lists for it).
	NewProvider func(*config.ProviderInstanceConfig) (core.Provider, error)
}

// Client is one voice target on its core provider.
type Client struct {
	Target     string
	Model      string
	InstanceID string
	role       Role
	provider   core.Provider
	credential coretransport.CredentialSource
}

// New returns the client of target for role, checking that the target's
// provider serves the role's surface for the target's model.
func New(cfg *config.Config, target string, role Role, options Options) (*Client, error) {
	resolver := options.Resolver
	if resolver == nil {
		resolver = modelservice.NewResolver()
	}
	resolved, err := resolver.ResolveVoiceTarget(cfg, target)
	if err != nil {
		return nil, err
	}
	provider, err := voiceProvider(resolved, role, options)
	if err != nil {
		return nil, err
	}
	surfaces := Surfaces(provider, resolved.Model)
	viaChat := options.ViaChat && role == SpeechToText
	if !slices.Contains(surfaces, role.surface()) || (viaChat && !slices.Contains(surfaces, core.ModelSurfaceChatCompletions)) {
		if viaChat {
			return nil, fmt.Errorf("provider %q cannot transcribe with chat model %q: it does not serve chat_completions for it", resolved.Instance.ID, resolved.Model)
		}
		return nil, fmt.Errorf("provider %q does not serve %s for model %q", resolved.Instance.ID, role.surface(), resolved.Model)
	}
	credential, err := providers.InstanceCredential(resolved.Instance, resolved.Secret)
	if err != nil {
		return nil, err
	}
	return &Client{
		Target: strings.TrimSpace(target), Model: resolved.Model, InstanceID: resolved.Instance.ID,
		role: role, provider: provider, credential: credential,
	}, nil
}

func voiceProvider(target modelservice.VoiceTarget, role Role, options Options) (core.Provider, error) {
	build := options.NewProvider
	if build == nil {
		build = providers.NewCoreProvider
	}
	provider, err := build(target.Instance)
	if err != nil {
		return nil, err
	}
	if role == SpeechToText && options.ViaChat {
		model := target.Model
		return translation.ChatTranscriptionAdapter{
			Provider: provider,
			Enabled:  func(candidate string) bool { return candidate == model },
		}, nil
	}
	return provider, nil
}

// Surfaces returns every surface provider serves for model: translated ones
// when it reports them, else its native ones.
func Surfaces(provider core.Provider, model string) []core.ModelSurface {
	if translated, ok := provider.(interface {
		Surfaces(string) []core.ModelSurface
	}); ok {
		return translated.Surfaces(model)
	}
	return provider.NativeSurfaces(model)
}

func (c *Client) invoke(ctx context.Context, body []byte, contentType string) (core.Response, error) {
	credential, err := c.credential(ctx)
	if err != nil {
		return core.Response{}, err
	}
	return c.provider.Invoke(ctx, core.Request{
		Surface: c.role.surface(), Model: c.Model, Body: body, ContentType: contentType, Credential: credential,
	})
}

// Transcription is a speech-to-text result.
type Transcription struct {
	Text     string  `json:"text"`
	Language string  `json:"language,omitempty"`
	Duration float64 `json:"duration,omitempty"`
}

// annotationOnly matches a transcript made only of annotations a
// transcriber writes for audio without speech, such as "[BLANK_AUDIO]",
// "(silence)", "<|nospeech|>" or "[Music] [Applause]".
var annotationOnly = regexp.MustCompile(`^(?:\s*[\[(<{*]+[^\[\]()<>{}*]*[\])>}*]+)+\s*$`)

// RecognizedSpeech returns the speech a transcript holds, trimmed, and
// whether it holds any. An empty transcript holds none, and neither does one
// without a letter or digit, such as "...", or one that is only a
// transcriber's annotation of audio without speech.
func RecognizedSpeech(text string) (string, bool) {
	text = strings.TrimSpace(text)
	spoken := strings.ContainsFunc(text, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) })
	if !spoken || annotationOnly.MatchString(text) {
		return "", false
	}
	return text, true
}

// Transcribe transcribes audio. filename, such as "recording.wav", tells the
// provider the audio's format.
func (c *Client) Transcribe(ctx context.Context, audio []byte, filename string) (*Transcription, error) {
	if c.role != SpeechToText {
		return nil, errors.New("voice client is not a speech-to-text client")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(audio); err != nil {
		return nil, err
	}
	for name, value := range map[string]string{"model": c.Model, "response_format": "json"} {
		if err := writer.WriteField(name, value); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	response, err := c.invoke(ctx, body.Bytes(), writer.FormDataContentType())
	if err != nil {
		return nil, err
	}
	var result Transcription
	if err := json.Unmarshal(response.Body, &result); err != nil {
		return nil, fmt.Errorf("decode transcription response: %w", err)
	}
	return &result, nil
}

// Speech is synthesized audio and its format.
type Speech struct {
	Audio       []byte
	ContentType string
	// FileExt is the file extension of ContentType, such as ".ogg".
	FileExt string
}

// Synthesize speaks text with voice (empty: the provider's default),
// requesting responseFormat (such as "opus" or "mp3"). A provider that
// rejects the format is asked again without one; the audio's format is what
// the provider says it returned, else what was asked for.
func (c *Client) Synthesize(ctx context.Context, text, voice, responseFormat string) (*Speech, error) {
	if c.role != TextToSpeech {
		return nil, errors.New("voice client is not a text-to-speech client")
	}
	responseFormat = strings.ToLower(strings.TrimSpace(responseFormat))
	response, err := c.speak(ctx, text, voice, responseFormat)
	if responseFormat != "" && rejectsResponseFormat(err) {
		responseFormat = ""
		response, err = c.speak(ctx, text, voice, "")
	}
	if err != nil {
		return nil, err
	}
	fileExt, contentType, ok := AudioMetaForContentType(response.ContentType)
	if !ok {
		fileExt, contentType = AudioMetaForResponseFormat(responseFormat)
	}
	return &Speech{Audio: response.Body, ContentType: contentType, FileExt: fileExt}, nil
}

func (c *Client) speak(ctx context.Context, text, voice, responseFormat string) (core.Response, error) {
	payload := map[string]any{"model": c.Model, "input": text}
	if voice = strings.TrimSpace(voice); voice != "" {
		payload["voice"] = voice
	}
	if responseFormat != "" {
		payload["response_format"] = responseFormat
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return core.Response{}, err
	}
	return c.invoke(ctx, body, core.ContentTypeJSON)
}

// rejectsResponseFormat reports whether err may be a provider refusing the
// requested response_format: a refusal made before anything was sent that
// names it, or an upstream's 400 or 422. Core names an upstream refusal only
// by its status and error identifiers, never its text, so any such refusal
// is taken for one; asking again without the format tells.
func rejectsResponseFormat(err error) bool {
	var providerErr *core.ProviderError
	if !errors.As(err, &providerErr) {
		return false
	}
	switch providerErr.Classification.StatusCode {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return true
	}
	return providerErr.Class == core.ProviderErrorInvalidRequest &&
		strings.Contains(strings.ToLower(providerErr.Message), "response_format")
}

// AudioMetaForResponseFormat returns the file extension and content type of
// audio in the OpenAI response format; empty is the API's default, MP3.
func AudioMetaForResponseFormat(responseFormat string) (string, string) {
	switch strings.ToLower(strings.TrimSpace(responseFormat)) {
	case "opus":
		return ".ogg", "audio/ogg"
	case "wav":
		return ".wav", "audio/wav"
	case "aac":
		return ".aac", "audio/aac"
	case "flac":
		return ".flac", "audio/flac"
	default:
		return ".mp3", "audio/mpeg"
	}
}

// AudioMetaForContentType returns the file extension and content type of
// audio declared as contentType; ok is false when it names no audio format,
// such as application/octet-stream.
func AudioMetaForContentType(contentType string) (fileExt string, audioType string, ok bool) {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "", "", false
	}
	switch strings.ToLower(mediaType) {
	case "audio/mpeg", "audio/mp3":
		return ".mp3", "audio/mpeg", true
	case "audio/ogg", "audio/opus":
		return ".ogg", "audio/ogg", true
	case "audio/wav", "audio/wave", "audio/x-wav":
		return ".wav", "audio/wav", true
	case "audio/aac":
		return ".aac", "audio/aac", true
	case "audio/flac", "audio/x-flac":
		return ".flac", "audio/flac", true
	case "audio/l16", "audio/pcm":
		return ".pcm", "audio/L16", true
	default:
		return "", "", false
	}
}
