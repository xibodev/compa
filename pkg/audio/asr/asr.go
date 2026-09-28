package asr

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xibodev/compa/pkg/audio/voice"
	"github.com/xibodev/compa/pkg/config"
	"github.com/xibodev/compa/pkg/logger"
	"github.com/xibodev/compa/pkg/modelservice"
	"github.com/xibodev/compa/pkg/utils"
)

// Transcriber turns recorded speech into text.
type Transcriber interface {
	Name() string
	Transcribe(ctx context.Context, audioFilePath string) (*TranscriptionResponse, error)
}

// TranscriptionResponse is a transcription.
type TranscriptionResponse = voice.Transcription

// DetectTranscriber returns the transcriber of cfg's speech-to-text target,
// cfg.Voice.STTTarget, on its instance's core provider; a chat model when
// cfg.Voice.STTViaChat opts in, provided its catalog declares audio input
// (see modelservice.CheckChatTranscriptionTarget). It returns nil, which
// turns voice transcription off, when no target is configured or the target
// cannot transcribe.
func DetectTranscriber(cfg *config.Config) Transcriber {
	if cfg == nil {
		return nil
	}
	target := strings.TrimSpace(cfg.Voice.STTTarget)
	if target == "" {
		return nil
	}
	client, err := voice.New(cfg, target, voice.SpeechToText, voice.Options{ViaChat: cfg.Voice.STTViaChat})
	if err == nil && cfg.Voice.STTViaChat {
		err = modelservice.CheckChatTranscriptionTarget(cfg, nil, target)
	}
	if err != nil {
		logger.WarnCF("voice", "Speech-to-text target is unavailable; voice transcription is off", map[string]any{
			"target": target,
			"error":  err.Error(),
		})
		return nil
	}
	return NewTranscriber(client)
}

// NewTranscriber returns the Transcriber of a speech-to-text voice client.
// Its transcripts hold only recognized speech: one without speech is empty
// (see voice.RecognizedSpeech).
func NewTranscriber(client *voice.Client) Transcriber {
	return &coreTranscriber{client: client}
}

type coreTranscriber struct {
	client *voice.Client
}

func (t *coreTranscriber) Name() string { return "voice:" + t.client.Target }

func (t *coreTranscriber) Transcribe(ctx context.Context, audioFilePath string) (*TranscriptionResponse, error) {
	data, err := os.ReadFile(audioFilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read audio file %s: %w", audioFilePath, err)
	}
	result, err := t.client.Transcribe(ctx, data, filepath.Base(audioFilePath))
	if err != nil {
		logger.ErrorCF("voice", "Transcription failed", map[string]any{"target": t.client.Target, "error": err.Error()})
		return nil, err
	}
	result.Text, _ = voice.RecognizedSpeech(result.Text)
	logger.InfoCF("voice", "Transcription completed", map[string]any{
		"target":                t.client.Target,
		"text_length":           len(result.Text),
		"transcription_preview": utils.Truncate(result.Text, 50),
	})
	return result, nil
}
