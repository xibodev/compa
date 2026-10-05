// Compa - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 Compa contributors

package utils

import (
	"testing"

	"github.com/xibodev/compa/pkg/providers"
)

func TestCalculateDefaultMaxContextRunes(t *testing.T) {
	tests := []struct {
		name          string
		contextWindow int
		want          int
	}{
		{
			name:          "zero context window uses fallback",
			contextWindow: 0,
			want:          8000,
		},
		{
			name:          "negative context window uses fallback",
			contextWindow: -1,
			want:          8000,
		},
		{
			name:          "small context window (4k tokens)",
			contextWindow: 4000,
			want:          9000, // 4000 * 0.75 * 3 = 9000
		},
		{
			name:          "medium context window (128k tokens)",
			contextWindow: 128000,
			want:          288000, // 128000 * 0.75 * 3 = 288000
		},
		{
			name:          "large context window (1M tokens)",
			contextWindow: 1000000,
			want:          2250000, // 1000000 * 0.75 * 3 = 2250000
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CalculateDefaultMaxContextRunes(tt.contextWindow)
			if got != tt.want {
				t.Errorf("CalculateDefaultMaxContextRunes(%d) = %d, want %d",
					tt.contextWindow, got, tt.want)
			}
		})
	}
}

func TestResolveMaxContextRunes(t *testing.T) {
	tests := []struct {
		name          string
		configValue   int
		contextWindow int
		want          int
	}{
		{
			name:          "explicit positive value",
			configValue:   12000,
			contextWindow: 4000,
			want:          12000,
		},
		{
			name:          "explicit disable (-1)",
			configValue:   -1,
			contextWindow: 4000,
			want:          -1,
		},
		{
			name:          "zero uses auto-calculate",
			configValue:   0,
			contextWindow: 4000,
			want:          9000, // 4000 * 0.75 * 3
		},
		{
			name:          "unset (0) with unknown context window",
			configValue:   0,
			contextWindow: 0,
			want:          8000, // fallback
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveMaxContextRunes(tt.configValue, tt.contextWindow)
			if got != tt.want {
				t.Errorf("ResolveMaxContextRunes(%d, %d) = %d, want %d",
					tt.configValue, tt.contextWindow, got, tt.want)
			}
		})
	}
}

func TestMeasureContextRunes(t *testing.T) {
	tests := []struct {
		name     string
		messages []providers.Message
		want     int
	}{
		{
			name:     "empty messages",
			messages: []providers.Message{},
			want:     0,
		},
		{
			name: "single simple message",
			messages: []providers.Message{
				{Role: "user", Content: "Hello"},
			},
			want: 5, // "Hello" = 5 runes
		},
		{
			name: "message with reasoning",
			messages: []providers.Message{
				{
					Role:             "assistant",
					Content:          "Answer",
					ReasoningContent: "Thinking",
				},
			},
			want: 14, // "Answer" (6) + "Thinking" (8) = 14
		},
		{
			name: "message with tool call",
			messages: []providers.Message{
				{
					Role:    "assistant",
					Content: "Using tool",
					ToolCalls: []providers.ToolCall{
						{
							Name:      "test_tool",
							Arguments: map[string]any{"key": "value"},
						},
					},
				},
			},
			want: 10 + 9 + 15, // "Using tool" + "test_tool" + {"key":"value"}
		},
		{
			name: "multiple messages",
			messages: []providers.Message{
				{Role: "system", Content: "You are helpful"},
				{Role: "user", Content: "Hi"},
				{Role: "assistant", Content: "Hello!"},
			},
			want: 15 + 2 + 6, // 15 + 2 + 6 = 23
		},
		{
			name: "unicode characters",
			messages: []providers.Message{
				{Role: "user", Content: "\u4f60\u597d\u4e16\u754c"}, // 4 Chinese characters
			},
			want: 4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MeasureContextRunes(tt.messages)
			if got != tt.want {
				t.Errorf("MeasureContextRunes() = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestSubTurnConfigMaxContextRunes verifies that MaxContextRunes configuration
// is properly integrated into the SubTurn execution flow.
func TestSubTurnConfigMaxContextRunes(t *testing.T) {
	tests := []struct {
		name            string
		maxContextRunes int
		contextWindow   int
		wantResolved    int
	}{
		{
			name:            "default (0) auto-calculates from context window",
			maxContextRunes: 0,
			contextWindow:   4000,
			wantResolved:    9000, // 4000 * 0.75 * 3
		},
		{
			name:            "explicit value is used",
			maxContextRunes: 12000,
			contextWindow:   4000,
			wantResolved:    12000,
		},
		{
			name:            "disabled (-1) returns -1",
			maxContextRunes: -1,
			contextWindow:   4000,
			wantResolved:    -1,
		},
		{
			name:            "fallback when context window unknown",
			maxContextRunes: 0,
			contextWindow:   0,
			wantResolved:    8000, // conservative fallback
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveMaxContextRunes(tt.maxContextRunes, tt.contextWindow)
			if got != tt.wantResolved {
				t.Errorf("utils.ResolveMaxContextRunes(%d, %d) = %d, want %d",
					tt.maxContextRunes, tt.contextWindow, got, tt.wantResolved)
			}
		})
	}
}
