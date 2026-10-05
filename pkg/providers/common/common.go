// Compa - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 Compa contributors

// Package common provides shared utilities used by multiple LLM provider
// implementations (openai_compat, azure, etc.).
package common

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	core "github.com/xibodev/llmgw-core"

	"github.com/xibodev/compa/pkg/logger"
	"github.com/xibodev/compa/pkg/providers/protocoltypes"
)

// Re-export protocol types used across providers.
type (
	ToolCall               = protocoltypes.ToolCall
	FunctionCall           = protocoltypes.FunctionCall
	LLMResponse            = protocoltypes.LLMResponse
	UsageInfo              = protocoltypes.UsageInfo
	Message                = protocoltypes.Message
	ToolDefinition         = protocoltypes.ToolDefinition
	ToolFunctionDefinition = protocoltypes.ToolFunctionDefinition
	ExtraContent           = protocoltypes.ExtraContent
	GoogleExtra            = protocoltypes.GoogleExtra
	ReasoningDetail        = protocoltypes.ReasoningDetail
)

// --- Message serialization ---

// openaiMessage is the wire-format message for OpenAI-compatible APIs.
// It mirrors protocoltypes.Message but omits SystemParts, which is an
// internal field that would be unknown to third-party endpoints.
type openaiMessage struct {
	Role             string           `json:"role"`
	Content          string           `json:"content"`
	ReasoningContent string           `json:"reasoning_content,omitempty"`
	ToolCalls        []openaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string           `json:"tool_call_id,omitempty"`
}

type openaiToolCall struct {
	ID       string              `json:"id"`
	Type     string              `json:"type,omitempty"`
	Function *openaiFunctionCall `json:"function,omitempty"`
}

type openaiFunctionCall struct {
	Name             string `json:"name"`
	Arguments        string `json:"arguments"`
	ThoughtSignature string `json:"thought_signature,omitempty"`
}

// SerializeMessages converts internal Message structs to the OpenAI wire format.
//   - Strips SystemParts (unknown to third-party endpoints)
//   - Converts messages with Media to multipart content format (text + image_url parts)
//   - Preserves ToolCallID, ToolCalls, and ReasoningContent for all messages
func SerializeMessages(messages []Message) []any {
	out := make([]any, 0, len(messages))
	for _, m := range messages {
		toolCalls := serializeToolCalls(m.ToolCalls)
		if len(m.Media) == 0 {
			out = append(out, openaiMessage{
				Role:             m.Role,
				Content:          m.Content,
				ReasoningContent: m.ReasoningContent,
				ToolCalls:        toolCalls,
				ToolCallID:       m.ToolCallID,
			})
			continue
		}

		// Multipart content format for messages with media
		parts := make([]map[string]any, 0, 1+len(m.Media))
		if m.Content != "" {
			parts = append(parts, map[string]any{
				"type": "text",
				"text": m.Content,
			})
		}
		for _, mediaURL := range m.Media {
			if strings.HasPrefix(mediaURL, "data:image/") {
				parts = append(parts, map[string]any{
					"type": "image_url",
					"image_url": map[string]any{
						"url": mediaURL,
					},
				})
				continue
			}

			if format, data, ok := ParseDataAudioURL(mediaURL); ok {
				parts = append(parts, map[string]any{
					"type": "input_audio",
					"input_audio": map[string]any{
						"data":   data,
						"format": format,
					},
				})
			}
		}

		msg := map[string]any{
			"role":    m.Role,
			"content": parts,
		}
		if m.ToolCallID != "" {
			msg["tool_call_id"] = m.ToolCallID
		}
		if len(toolCalls) > 0 {
			msg["tool_calls"] = toolCalls
		}
		if m.ReasoningContent != "" {
			msg["reasoning_content"] = m.ReasoningContent
		}
		out = append(out, msg)
	}
	return out
}

// systemPartSeparator joins a system message's SystemParts into its content,
// as the agent joins its prompt parts.
const systemPartSeparator = "\n\n---\n\n"

// SerializeMessagesWithSystemParts is SerializeMessages for an upstream that
// caches prompt prefixes by content part, as Anthropic does, reached through
// translation: a system message whose SystemParts render its content and mark
// a cache breakpoint is sent as those parts, each with its cache_control. A
// system message whose parts no longer render its content, as after a hook
// rewrote it, is sent as its content.
//
// Translation joins the text parts of one cached block with "\n" and starts
// the next block after a breakpoint fresh, so each part after the first
// carries the rest of the separator: the system text the model gets is the
// message's content either way.
func SerializeMessagesWithSystemParts(messages []Message) []any {
	out := SerializeMessages(messages)
	for i, m := range messages {
		if m.Role != "system" || len(m.Media) > 0 {
			continue
		}
		if parts := systemContentParts(m); parts != nil {
			out[i] = map[string]any{"role": m.Role, "content": parts}
		}
	}
	return out
}

// systemContentParts returns the content parts of a system message's
// SystemParts, or nil when they do not render its content or set no
// cache_control.
func systemContentParts(m Message) []any {
	blocks := make([]protocoltypes.ContentBlock, 0, len(m.SystemParts))
	texts := make([]string, 0, len(m.SystemParts))
	cached := false
	for _, block := range m.SystemParts {
		if block.Type != "" && block.Type != "text" {
			return nil
		}
		if strings.TrimSpace(block.Text) == "" {
			continue
		}
		blocks = append(blocks, block)
		texts = append(texts, block.Text)
		cached = cached || block.CacheControl != nil
	}
	if !cached || strings.Join(texts, systemPartSeparator) != m.Content {
		return nil
	}
	parts := make([]any, 0, len(blocks))
	for i, block := range blocks {
		text := block.Text
		switch {
		case i == 0:
		case blocks[i-1].CacheControl != nil:
			text = systemPartSeparator + text
		default:
			// Joined to the part before with "\n".
			text = systemPartSeparator[1:] + text
		}
		part := map[string]any{"type": "text", "text": text}
		if block.CacheControl != nil {
			part["cache_control"] = map[string]any{"type": block.CacheControl.Type}
		}
		parts = append(parts, part)
	}
	return parts
}

func serializeToolCalls(toolCalls []ToolCall) []openaiToolCall {
	if len(toolCalls) == 0 {
		return nil
	}

	out := make([]openaiToolCall, 0, len(toolCalls))
	for _, tc := range toolCalls {
		wireCall := openaiToolCall{
			ID:   tc.ID,
			Type: tc.Type,
		}

		if tc.Function != nil {
			thoughtSignature := tc.Function.ThoughtSignature
			if thoughtSignature == "" {
				thoughtSignature = tc.ThoughtSignature
			}
			if thoughtSignature == "" && tc.ExtraContent != nil && tc.ExtraContent.Google != nil {
				thoughtSignature = tc.ExtraContent.Google.ThoughtSignature
			}
			wireCall.Function = &openaiFunctionCall{
				Name:             tc.Function.Name,
				Arguments:        tc.Function.Arguments,
				ThoughtSignature: thoughtSignature,
			}
		} else if tc.Name != "" || len(tc.Arguments) > 0 || tc.ThoughtSignature != "" {
			thoughtSignature := tc.ThoughtSignature
			if thoughtSignature == "" && tc.ExtraContent != nil && tc.ExtraContent.Google != nil {
				thoughtSignature = tc.ExtraContent.Google.ThoughtSignature
			}
			argsJSON := "{}"
			if len(tc.Arguments) > 0 {
				if encoded, err := json.Marshal(tc.Arguments); err == nil {
					argsJSON = string(encoded)
				}
			}
			wireCall.Function = &openaiFunctionCall{
				Name:             tc.Name,
				Arguments:        argsJSON,
				ThoughtSignature: thoughtSignature,
			}
		}

		out = append(out, wireCall)
	}

	return out
}

// ParseDataAudioURL extracts the format and base64 data from a data:audio/... URL.
func ParseDataAudioURL(mediaURL string) (format, data string, ok bool) {
	if !strings.HasPrefix(mediaURL, "data:audio/") {
		return "", "", false
	}

	payload := strings.TrimPrefix(mediaURL, "data:audio/")
	meta, data, found := strings.Cut(payload, ",")
	if !found {
		return "", "", false
	}

	format, _, _ = strings.Cut(meta, ";")
	format = strings.TrimSpace(format)
	data = strings.TrimSpace(data)
	if format == "" || data == "" {
		return "", "", false
	}
	return format, data, true
}

// --- Response parsing ---

// completionToolCall is one tool call of a chat completion's message.
type completionToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function *struct {
		Name             string          `json:"name"`
		Arguments        json.RawMessage `json:"arguments"`
		ThoughtSignature string          `json:"thought_signature"`
	} `json:"function"`
	ExtraContent *struct {
		Google *struct {
			ThoughtSignature string `json:"thought_signature"`
		} `json:"google"`
		ToolFeedbackExplanation string `json:"tool_feedback_explanation"`
	} `json:"extra_content"`
}

// ParseResponse parses a JSON chat completion response body into an
// LLMResponse.
//
// Every choice counts: some upstreams answer one message split across
// choices, its text in one and its tool calls in another. The first
// non-empty text of each kind is the answer's, and the tool calls of every
// choice are. Tool calls are the answer's whatever finish_reason says: an
// answer that carries them finished for them (see NormalizeFinishReason).
func ParseResponse(body io.Reader) (*LLMResponse, error) {
	var apiResponse struct {
		Choices []struct {
			Message struct {
				Content          json.RawMessage      `json:"content"`
				ReasoningContent string               `json:"reasoning_content"`
				Reasoning        string               `json:"reasoning"`
				ReasoningDetails []ReasoningDetail    `json:"reasoning_details"`
				ToolCalls        []completionToolCall `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *UsageInfo      `json:"usage"`
		Error json.RawMessage `json:"error"`
	}

	if err := json.NewDecoder(body).Decode(&apiResponse); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if len(apiResponse.Choices) == 0 {
		if message := AnswerErrorMessage(apiResponse.Error); message != "" {
			return nil, AnswerError(message)
		}
		return &LLMResponse{
			Content:      "",
			FinishReason: "stop",
		}, nil
	}

	out := &LLMResponse{Usage: apiResponse.Usage}
	toolCalls := make([]ToolCall, 0)
	seenIDs := make(map[string]struct{})
	for _, choice := range apiResponse.Choices {
		message := choice.Message
		if out.Content == "" {
			out.Content = ContentText(message.Content)
		}
		if out.ReasoningContent == "" {
			out.ReasoningContent = message.ReasoningContent
		}
		if out.Reasoning == "" {
			out.Reasoning = message.Reasoning
		}
		if len(out.ReasoningDetails) == 0 {
			out.ReasoningDetails = message.ReasoningDetails
		}
		if out.FinishReason == "" {
			out.FinishReason = choice.FinishReason
		}
		for _, tc := range message.ToolCalls {
			if id := strings.TrimSpace(tc.ID); id != "" {
				if _, seen := seenIDs[id]; seen {
					continue
				}
				seenIDs[id] = struct{}{}
			}
			toolCall, err := completionToolCallToToolCall(tc, len(toolCalls))
			if err != nil {
				return nil, err
			}
			toolCalls = append(toolCalls, toolCall)
		}
	}
	out.ToolCalls = toolCalls
	out.FinishReason = NormalizeFinishReason(out.FinishReason, len(toolCalls) > 0)
	return out, nil
}

// completionToolCallToToolCall converts one tool call of an answer. A call
// whose arguments do not decode fails the answer: it must not run with
// arguments it did not mean.
func completionToolCallToToolCall(tc completionToolCall, index int) (ToolCall, error) {
	arguments := make(map[string]any)
	name := ""

	thoughtSignature := ""
	if tc.Function != nil {
		thoughtSignature = tc.Function.ThoughtSignature
	}
	if thoughtSignature == "" && tc.ExtraContent != nil && tc.ExtraContent.Google != nil {
		thoughtSignature = tc.ExtraContent.Google.ThoughtSignature
	}

	if tc.Function != nil {
		name = tc.Function.Name
		decoded, _, err := decodeToolCallArguments(tc.Function.Arguments)
		if err != nil {
			logger.WarnCF("providers", "Failed to decode tool call arguments", map[string]any{
				"tool":  name,
				"error": err.Error(),
			})
			return ToolCall{}, InvalidToolArgumentsError(name)
		}
		arguments = decoded
	}

	toolCall := ToolCall{
		ID:               ToolCallID(tc.ID, index),
		Name:             name,
		Arguments:        arguments,
		ThoughtSignature: thoughtSignature,
	}

	if thoughtSignature != "" || tc.ExtraContent != nil {
		extraContent := &ExtraContent{
			ToolFeedbackExplanation: "",
		}
		if tc.ExtraContent != nil {
			extraContent.ToolFeedbackExplanation = tc.ExtraContent.ToolFeedbackExplanation
		}
		if thoughtSignature != "" {
			extraContent.Google = &GoogleExtra{
				ThoughtSignature: thoughtSignature,
			}
		}
		if extraContent.Google != nil || strings.TrimSpace(extraContent.ToolFeedbackExplanation) != "" {
			toolCall.ExtraContent = extraContent
		}
	}
	return toolCall, nil
}

// NormalizeFinishReason returns the finish reason Compa records for an
// answer. "length" reads "truncated". An answer that carries tool calls
// finished to call them, whatever the upstream said - some finish such an
// answer with "stop", or with nothing - unless it was cut off. An empty
// reason is "stop".
func NormalizeFinishReason(reason string, hasToolCalls bool) string {
	reason = strings.TrimSpace(reason)
	switch {
	case reason == "length" || reason == "truncated":
		return "truncated"
	case hasToolCalls:
		return "tool_calls"
	case reason == "":
		return "stop"
	}
	return reason
}

// ToolCallID returns the id of the tool call at index of an answer: its own,
// or, for an upstream that sends none, a new one, so its result can name the
// call it answers.
func ToolCallID(id string, index int) string {
	if id = strings.TrimSpace(id); id != "" {
		return id
	}
	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return fmt.Sprintf("call_%d_%d", index, time.Now().UnixNano())
	}
	return fmt.Sprintf("call_%d_%s", index, hex.EncodeToString(suffix[:]))
}

// ContentText returns the text of a message's content: a string, null, or
// a list of parts whose text parts it joins.
func ContentText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return ""
	}
	var joined strings.Builder
	for _, part := range parts {
		if part.Type == "" || part.Type == "text" || part.Type == "output_text" {
			joined.WriteString(part.Text)
		}
	}
	return joined.String()
}

// AnswerErrorMessage returns the message of an error an upstream reported
// in a successful answer or a stream chunk: {"error": "..."} or
// {"error": {"message": "..."}}. It is "" when there is none.
func AnswerErrorMessage(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return strings.TrimSpace(text)
	}
	var detail struct {
		Message string `json:"message"`
		Code    any    `json:"code"`
		Type    string `json:"type"`
	}
	if err := json.Unmarshal(raw, &detail); err != nil {
		return ""
	}
	if message := strings.TrimSpace(detail.Message); message != "" {
		return message
	}
	if detail.Type != "" {
		return detail.Type
	}
	if detail.Code != nil {
		return fmt.Sprint(detail.Code)
	}
	return ""
}

// AnswerError is the error of an upstream that answered with an error
// instead of a completion. Another instance may still serve the request.
func AnswerError(message string) error {
	return &core.ProviderError{
		Message:        "the provider answered with an error: " + message,
		Class:          core.ProviderErrorUpstream,
		Classification: core.ProviderErrorClassification{FailoverEligible: true, CircuitFailure: true},
	}
}

// DecodeToolCallArguments decodes a tool call's arguments from raw JSON: a
// JSON object, or a JSON string holding one. Arguments that do not decode
// come back as {"raw": <their text>}.
func DecodeToolCallArguments(raw json.RawMessage, name string) map[string]any {
	arguments, text, err := decodeToolCallArguments(raw)
	if err != nil {
		logger.WarnCF("providers", "Failed to decode tool call arguments", map[string]any{
			"tool":  name,
			"error": err.Error(),
		})
		return map[string]any{"raw": text}
	}
	return arguments
}

// decodeToolCallArguments decodes a tool call's arguments, returning on
// failure the text that did not decode.
func decodeToolCallArguments(raw json.RawMessage) (map[string]any, string, error) {
	arguments := make(map[string]any)
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return arguments, "", nil
	}

	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, string(raw), err
	}

	switch v := decoded.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return arguments, "", nil
		}
		if err := json.Unmarshal([]byte(v), &arguments); err != nil {
			return nil, v, err
		}
		return arguments, "", nil
	case map[string]any:
		return v, "", nil
	default:
		return nil, string(raw), fmt.Errorf("unsupported tool call arguments type %T", decoded)
	}
}

// InvalidToolArgumentsError is the failure of an answer that calls tool with
// arguments that are not a JSON object: the call is not run. Another target
// may answer properly.
func InvalidToolArgumentsError(tool string) error {
	return &core.ProviderError{
		Message:        fmt.Sprintf("the provider sent arguments for tool %q that are not valid JSON; the tool was not run", tool),
		Class:          core.ProviderErrorUpstream,
		Classification: core.ProviderErrorClassification{FailoverEligible: true},
	}
}

// --- HTTP response helpers ---

// ReadAndParseResponse peeks at the response body to detect HTML errors,
// then parses the JSON response into an LLMResponse.
func ReadAndParseResponse(resp *http.Response, apiBase string) (*LLMResponse, error) {
	contentType := resp.Header.Get("Content-Type")
	reader := bufio.NewReader(resp.Body)
	prefix, err := reader.Peek(256)
	if err != nil && err != io.EOF && err != bufio.ErrBufferFull {
		return nil, fmt.Errorf("failed to inspect response: %w", err)
	}
	if LooksLikeHTML(prefix, contentType) {
		return nil, htmlResponseError(prefix, contentType, apiBase)
	}
	out, err := ParseResponse(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to parse JSON response: %w", err)
	}
	return out, nil
}

// LooksLikeHTML checks if the response body appears to be HTML.
func LooksLikeHTML(body []byte, contentType string) bool {
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	if strings.Contains(contentType, "text/html") || strings.Contains(contentType, "application/xhtml+xml") {
		return true
	}
	prefix := bytes.ToLower(leadingTrimmedPrefix(body, 128))
	return bytes.HasPrefix(prefix, []byte("<!doctype html")) ||
		bytes.HasPrefix(prefix, []byte("<html")) ||
		bytes.HasPrefix(prefix, []byte("<head")) ||
		bytes.HasPrefix(prefix, []byte("<body"))
}

// htmlResponseError reports an answer that is an HTML page instead of JSON,
// typically a proxy or a misconfigured endpoint. It is an unusable upstream
// answer: another instance may still serve the request, and it counts
// against this instance, but repeating it would get the same page.
func htmlResponseError(body []byte, contentType, apiBase string) error {
	return &core.ProviderError{
		Message: fmt.Sprintf(
			"API request failed: %s returned HTML instead of JSON (content-type: %s); check the provider instance's endpoint and proxy. Body: %s",
			apiBase, contentType, ResponsePreview(body, 128),
		),
		Class:          core.ProviderErrorUpstream,
		Classification: core.ProviderErrorClassification{FailoverEligible: true, CircuitFailure: true},
	}
}

// ResponsePreview returns a truncated preview of response body for error messages.
func ResponsePreview(body []byte, maxLen int) string {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return "<empty>"
	}
	if len(trimmed) <= maxLen {
		return string(trimmed)
	}
	return string(trimmed[:maxLen]) + "..."
}

func leadingTrimmedPrefix(body []byte, maxLen int) []byte {
	i := 0
	for i < len(body) {
		switch body[i] {
		case ' ', '\t', '\n', '\r', '\f', '\v':
			i++
		default:
			end := i + maxLen
			if end > len(body) {
				end = len(body)
			}
			return body[i:end]
		}
	}
	return nil
}

// --- Numeric helpers ---

// AsInt converts various numeric types to int.
func AsInt(v any) (int, bool) {
	switch val := v.(type) {
	case int:
		return val, true
	case int64:
		return int(val), true
	case float64:
		return int(val), true
	case float32:
		return int(val), true
	default:
		return 0, false
	}
}

// AsFloat converts various numeric types to float64.
func AsFloat(v any) (float64, bool) {
	switch val := v.(type) {
	case float64:
		return val, true
	case float32:
		return float64(val), true
	case int:
		return float64(val), true
	case int64:
		return float64(val), true
	default:
		return 0, false
	}
}
