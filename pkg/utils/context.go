// Compa - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 Compa contributors

package utils

import (
	"encoding/json"
	"unicode/utf8"

	"github.com/xibodev/compa/v2/pkg/providers"
)

// CalculateDefaultMaxContextRunes computes a default context limit based on the model's context window.
// Strategy: Use 75% of the context window and convert to rune estimate.
//
// Token-to-rune conversion ratios (conservative estimates):
//   - English: ~4 chars per token
//   - Chinese: ~1.5-2 chars per token
//   - Mixed: ~3 chars per token (used here for safety)
func CalculateDefaultMaxContextRunes(contextWindow int) int {
	if contextWindow <= 0 {
		// Conservative fallback when context window is unknown
		return 8000 // ~2000 tokens
	}

	// Use 75% of context window to leave headroom
	targetTokens := int(float64(contextWindow) * 0.75)

	// Convert tokens to runes using conservative ratio
	const avgCharsPerToken = 3
	return targetTokens * avgCharsPerToken
}

// ResolveMaxContextRunes determines the final MaxContextRunes value to use.
// Priority: explicit config > auto-calculate > conservative default
func ResolveMaxContextRunes(configValue, contextWindow int) int {
	switch {
	case configValue > 0:
		// Explicitly configured, use as-is
		return configValue
	case configValue == -1:
		// Explicitly disabled
		return -1
	default:
		// 0 or unset: auto-calculate
		return CalculateDefaultMaxContextRunes(contextWindow)
	}
}

// MeasureContextRunes calculates the total rune count of a message list.
// Includes content, reasoning content, and estimates for tool calls.
func MeasureContextRunes(messages []providers.Message) int {
	totalRunes := 0
	for _, msg := range messages {
		totalRunes += utf8.RuneCountInString(msg.Content)
		totalRunes += utf8.RuneCountInString(msg.ReasoningContent)

		// Tool calls: serialize to JSON and count
		if len(msg.ToolCalls) > 0 {
			for _, tc := range msg.ToolCalls {
				totalRunes += utf8.RuneCountInString(tc.Name)
				// Arguments: serialize and count
				if argsJSON, err := json.Marshal(tc.Arguments); err == nil {
					totalRunes += utf8.RuneCount(argsJSON)
				} else {
					// Fallback estimate if serialization fails
					totalRunes += 100
				}
			}
		}

		// ToolCallID
		totalRunes += utf8.RuneCountInString(msg.ToolCallID)
	}
	return totalRunes
}
