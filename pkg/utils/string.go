package utils

import (
	"strings"
	"sync/atomic"
	"unicode"
)

// Global variable to disable truncation
var disableTruncation atomic.Bool

// SetDisableTruncation globally enables or disables string truncation
func SetDisableTruncation(enabled bool) {
	disableTruncation.Store(enabled)
}

// SanitizeMessageContent removes Unicode control characters, format characters (RTL overrides,
// zero-width spaces), and other non-graphic characters that could confuse an LLM
// or cause display issues in the agent UI. The joiners ZWJ and ZWNJ and emoji tag
// characters stay: Persian and Indic words and emoji sequences need them.
func SanitizeMessageContent(input string) string {
	var sb strings.Builder
	// Pre-allocate memory to avoid multiple allocations
	sb.Grow(len(input))

	for _, r := range input {
		// unicode.IsGraphic returns true if the rune is a Unicode graphic character.
		// This includes letters, marks, numbers, punctuation, and symbols.
		// It excludes control characters (Cc), format characters (Cf),
		// surrogates (Cs), and private use (Co).
		if unicode.IsGraphic(r) || r == '\n' || r == '\r' || r == '\t' || keptFormatRune(r) {
			sb.WriteRune(r)
		}
	}

	return sb.String()
}

// keptFormatRune reports a format character text needs: ZWNJ (U+200C) and
// ZWJ (U+200D), which shape Persian and Indic words and join emoji, and the
// tag characters of emoji flag sequences (U+E0020-U+E007F).
func keptFormatRune(r rune) bool {
	return r == '\u200c' || r == '\u200d' || (r >= 0xE0020 && r <= 0xE007F)
}

// Truncate returns a truncated version of s with at most maxLen runes.
// Handles multi-byte Unicode characters properly.
// If the string is truncated, "..." is appended to indicate truncation.
func Truncate(s string, maxLen int) string {
	// If the no-truncate flag is active, it returns the full string
	if disableTruncation.Load() {
		return s
	}
	if maxLen <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	// Reserve 3 chars for "..."
	if maxLen <= 3 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-3]) + "..."
}

// DerefStr dereferences a pointer to a string and
// returns the value or a fallback if the pointer is nil.
func DerefStr(s *string, fallback string) string {
	if s == nil {
		return fallback
	}
	return *s
}
