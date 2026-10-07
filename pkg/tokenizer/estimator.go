package tokenizer

import (
	"encoding/json"

	"github.com/xibodev/compa/v3/pkg/providers"
)

// IsCJK reports whether r is a CJK ideograph, a kana or a Hangul syllable
// (U+2E80–U+9FFF, U+F900–U+FAFF, U+AC00–U+D7AF). Tokenizers spend about one
// token on each of these runes, against about one per four for Latin text.
func IsCJK(r rune) bool {
	return r >= 0x2E80 && r <= 0x9FFF || r >= 0xF900 && r <= 0xFAFF || r >= 0xAC00 && r <= 0xD7AF
}

// textSize counts the runes of some text by how tokenizers treat them.
type textSize struct {
	cjk   int
	other int
}

func sizeOf(s string) textSize {
	var size textSize
	for _, r := range s {
		if IsCJK(r) {
			size.cjk++
		} else {
			size.other++
		}
	}
	return size
}

func (s textSize) plus(o textSize) textSize {
	return textSize{cjk: s.cjk + o.cjk, other: s.other + o.other}
}

// tokens estimates one token per CJK rune and 2.5 characters per token for
// everything else.
func (s textSize) tokens() int {
	return s.cjk + s.other*2/5
}

// EstimateTextTokens estimates the token count of plain text.
func EstimateTextTokens(s string) int {
	return sizeOf(s).tokens()
}

// EstimateMessageTokens estimates the token count for a single message,
// including Content, ReasoningContent, ToolCalls arguments, ToolCallID
// metadata, and Media items. CJK runes count as one token each; other text
// as 2.5 characters per token.
func EstimateMessageTokens(msg providers.Message) int {
	content := sizeOf(msg.Content)

	// SystemParts are structured system blocks used for cache-aware adapters.
	// They carry the same content as Content, but in multiple blocks.
	// We estimate them as an alternative representation, not additive.
	if len(msg.SystemParts) > 0 {
		var parts textSize
		for _, part := range msg.SystemParts {
			parts = parts.plus(sizeOf(part.Text))
		}
		// Per-part overhead for JSON structure (type, text, cache_control).
		const perPartOverhead = 20
		parts.other += len(msg.SystemParts) * perPartOverhead
		// Use the larger of the two representations to stay conservative.
		if parts.tokens() > content.tokens() {
			content = parts
		}
	}

	total := content.plus(sizeOf(msg.ReasoningContent))

	for _, tc := range msg.ToolCalls {
		total.other += len(tc.ID) + len(tc.Type)
		if tc.Function != nil {
			// Count function name + arguments (the wire format for most providers).
			// tc.Name mirrors tc.Function.Name — count only once to avoid double-counting.
			total.other += len(tc.Function.Name)
			total = total.plus(sizeOf(tc.Function.Arguments))
		} else {
			// Fallback: some provider formats use top-level Name without Function.
			total.other += len(tc.Name)
		}
	}

	total.other += len(msg.ToolCallID)

	// Per-message overhead for role label, JSON structure, separators.
	const messageOverhead = 12
	total.other += messageOverhead

	tokens := total.tokens()

	// Media items (images, files) are serialized by provider adapters into
	// multipart or image_url payloads. Add a fixed per-item token estimate
	// directly (not through the chars heuristic) since actual cost depends
	// on resolution and provider-specific image tokenization.
	const mediaTokensPerItem = 256
	tokens += len(msg.Media) * mediaTokensPerItem

	return tokens
}

// EstimateToolDefsTokens estimates the total token cost of tool definitions
// as they appear in the LLM request.
func EstimateToolDefsTokens(defs []providers.ToolDefinition) int {
	if len(defs) == 0 {
		return 0
	}

	var total textSize
	for _, d := range defs {
		total.other += len(d.Function.Name)
		total = total.plus(sizeOf(d.Function.Description))

		if d.Function.Parameters != nil {
			if paramJSON, err := json.Marshal(d.Function.Parameters); err == nil {
				total = total.plus(sizeOf(string(paramJSON)))
			}
		}

		// Per-tool overhead: type field, JSON structure, separators.
		total.other += 20
	}

	return total.tokens()
}
