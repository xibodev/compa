package tts

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xibodev/compa/v2/pkg/audio/voice"
	"github.com/xibodev/compa/v2/pkg/config"
	"github.com/xibodev/compa/v2/pkg/logger"
	"github.com/xibodev/compa/v2/pkg/media"
)

type TTSProvider interface {
	Name() string
	Synthesize(ctx context.Context, text string) (io.ReadCloser, error)
}

type ttsAudioMetaProvider interface {
	AudioFileMeta() (fileExt string, contentType string)
}

// DefaultResponseFormat is the audio format channels request when Options
// do not name one: Ogg Opus, which Discord voice playback and chat voice
// messages need. A provider that cannot return it returns its own format,
// which the stream reports.
const DefaultResponseFormat = "opus"

// Options configure a TTSProvider.
type Options struct {
	// Voice is the synthesis voice; empty uses the provider's default.
	Voice string
	// ResponseFormat is the requested OpenAI response format, such as "opus"
	// or "mp3"; empty uses DefaultResponseFormat.
	ResponseFormat string
}

// NewProvider returns the TTSProvider of a text-to-speech voice client.
func NewProvider(client *voice.Client, options Options) TTSProvider {
	format := strings.ToLower(strings.TrimSpace(options.ResponseFormat))
	if format == "" {
		format = DefaultResponseFormat
	}
	return &coreTTS{client: client, voice: strings.TrimSpace(options.Voice), responseFormat: format}
}

type coreTTS struct {
	client         *voice.Client
	voice          string
	responseFormat string
}

func (t *coreTTS) Name() string { return "voice:" + t.client.Target }

func (t *coreTTS) Synthesize(ctx context.Context, text string) (io.ReadCloser, error) {
	speech, err := t.client.Synthesize(ctx, text, t.voice, t.responseFormat)
	if err != nil {
		return nil, err
	}
	return &audioStream{
		ReadCloser: io.NopCloser(bytes.NewReader(speech.Audio)),
		fileExt:    speech.FileExt, contentType: speech.ContentType,
	}, nil
}

// DetectTTS returns the TTS provider of cfg's text-to-speech target,
// cfg.Voice.TTSTarget, speaking with cfg.Voice.TTSVoice. It returns nil,
// which turns speech synthesis off, when no target is configured or the
// target cannot synthesize speech.
func DetectTTS(cfg *config.Config) TTSProvider {
	if cfg == nil {
		return nil
	}
	target := strings.TrimSpace(cfg.Voice.TTSTarget)
	if target == "" {
		return nil
	}
	client, err := voice.New(cfg, target, voice.TextToSpeech, voice.Options{})
	if err != nil {
		logger.WarnCF("voice-tts", "Text-to-speech target is unavailable; speech synthesis is off", map[string]any{
			"target": target,
			"error":  err.Error(),
		})
		return nil
	}
	return NewProvider(client, Options{Voice: cfg.Voice.TTSVoice})
}

// StreamAudioMeta returns the file extension and content type of stream,
// audio a TTSProvider synthesized; ok is false when the provider does not
// say.
func StreamAudioMeta(stream io.Reader) (fileExt string, contentType string, ok bool) {
	meta, isMeta := stream.(ttsAudioMetaProvider)
	if !isMeta {
		return "", "", false
	}
	fileExt, contentType = meta.AudioFileMeta()
	return fileExt, contentType, fileExt != "" && contentType != ""
}

// audioStream is synthesized audio that knows its format.
type audioStream struct {
	io.ReadCloser
	fileExt     string
	contentType string
}

func (s *audioStream) AudioFileMeta() (string, string) {
	return s.fileExt, s.contentType
}

// SynthesizeAndStore synthesizes text to speech and registers it in the media store, returning the media reference.
func SynthesizeAndStore(
	ctx context.Context,
	provider TTSProvider,
	store media.MediaStore,
	text string,
	filename string,
	channel string,
	chatID string,
) (string, error) {
	if provider == nil {
		return "", fmt.Errorf("tts provider is not configured")
	}
	if store == nil {
		return "", fmt.Errorf("media store not configured")
	}
	if channel == "" || chatID == "" {
		return "", fmt.Errorf("no target channel/chat available")
	}
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("text is required")
	}

	stream, err := provider.Synthesize(ctx, text)
	if err != nil {
		return "", fmt.Errorf("tts synthesize failed: %w", err)
	}
	defer stream.Close()

	err = os.MkdirAll(media.TempDir(), 0o700)
	if err != nil {
		return "", fmt.Errorf("failed to create media temp dir: %w", err)
	}

	fileExt := ".ogg"
	contentType := "audio/ogg"
	if ext, ct, ok := StreamAudioMeta(stream); ok {
		fileExt = ext
		contentType = ct
	}

	file, err := os.CreateTemp(media.TempDir(), "tts-*"+fileExt)
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}

	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(file.Name())
		}
	}()

	_, err = io.Copy(file, stream)
	if err != nil {
		_ = file.Close()
		return "", fmt.Errorf("failed to write tts audio: %w", err)
	}

	err = file.Close()
	if err != nil {
		return "", fmt.Errorf("failed to close tts audio file: %w", err)
	}

	filename = strings.TrimSpace(filename)
	if filename == "" {
		filename = fmt.Sprintf("tts-%d%s", time.Now().Unix(), fileExt)
	}

	ext := strings.ToLower(filepath.Ext(filename))
	if ext == "" {
		filename += fileExt
	} else if ext != fileExt {
		filename = strings.TrimSuffix(filename, filepath.Ext(filename)) + fileExt
	}

	scope := fmt.Sprintf("tool:send_tts:%s:%s:%d", channel, chatID, time.Now().UnixNano())
	ref, err := store.Store(file.Name(), media.MediaMeta{
		Filename:    filename,
		ContentType: contentType,
		Source:      "tool:send_tts",
	}, scope)
	if err != nil {
		return "", fmt.Errorf("failed to register audio: %w", err)
	}
	removeTemp = false

	return ref, nil
}
