package openai_compat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"time"

	core "github.com/xibodev/llmgw-core"

	"github.com/xibodev/compa/pkg/logger"
	"github.com/xibodev/compa/pkg/providers/common"
	"github.com/xibodev/compa/pkg/providers/messageutil"
	"github.com/xibodev/compa/pkg/providers/protocoltypes"
)

type (
	ToolCall               = protocoltypes.ToolCall
	FunctionCall           = protocoltypes.FunctionCall
	LLMResponse            = protocoltypes.LLMResponse
	StreamChunk            = protocoltypes.StreamChunk
	UsageInfo              = protocoltypes.UsageInfo
	Message                = protocoltypes.Message
	ToolDefinition         = protocoltypes.ToolDefinition
	ToolFunctionDefinition = protocoltypes.ToolFunctionDefinition
	ExtraContent           = protocoltypes.ExtraContent
	GoogleExtra            = protocoltypes.GoogleExtra
	ReasoningDetail        = protocoltypes.ReasoningDetail
)

// Provider is Compa's chat client: it shapes Chat Completions requests from
// the agent's messages, tools and options, and parses the answers, streamed
// or not, tool calls and reasoning included. It never addresses an upstream
// itself: its HTTP client's transport hands every request to the provider
// instance's llmgw-core provider (see coretransport), which owns the
// endpoint, the credential, the proxy and the vendor's wire.
type Provider struct {
	// endpoint is the instance's endpoint. It only selects request shaping
	// that depends on the upstream, such as DeepSeek's reasoning replay.
	endpoint       string
	providerName   string
	maxTokensField string // Field name for max tokens (e.g., "max_completion_tokens" for o1/glm models)
	httpClient     *http.Client
	extraBody      map[string]any // Additional fields to inject into request body
	// streamedChat makes Chat stream its request and assemble the answer.
	streamedChat bool
}

type Option func(*Provider)

const defaultStreamingReadIdleTimeout = 5 * time.Minute

// chatCompletionsURL is where requests go. The transport reads only its
// path's surface.
const chatCompletionsURL = "http://core.invalid/v1/chat/completions"

func WithMaxTokensField(maxTokensField string) Option {
	return func(p *Provider) {
		p.maxTokensField = maxTokensField
	}
}

func WithExtraBody(extraBody map[string]any) Option {
	return func(p *Provider) {
		p.extraBody = extraBody
	}
}

func WithProviderName(providerName string) Option {
	return func(p *Provider) {
		p.providerName = strings.ToLower(strings.TrimSpace(providerName))
	}
}

// WithStreamedChat makes Chat stream its request and return the answer the
// stream assembles, for an upstream whose streamed answer is complete where
// its plain one may not be.
func WithStreamedChat() Option {
	return func(p *Provider) {
		p.streamedChat = true
	}
}

// NewProvider returns the chat client of an instance at endpoint, sending
// its requests with client, whose transport reaches the instance's core
// provider.
func NewProvider(endpoint string, client *http.Client, opts ...Option) *Provider {
	p := &Provider{
		endpoint:   strings.TrimRight(strings.TrimSpace(endpoint), "/"),
		httpClient: client,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(p)
		}
	}
	return p
}

// GetDefaultModel returns "": every call names its model.
func (p *Provider) GetDefaultModel() string { return "" }

// buildRequestBody constructs the common request body for Chat and ChatStream.
func (p *Provider) buildRequestBody(
	messages []Message, tools []ToolDefinition, model string, options map[string]any,
) map[string]any {
	preparedMessages := p.prepareMessagesForRequest(messages)
	preparedTools := tools
	requestBody := map[string]any{
		"model":    model,
		"messages": common.SerializeMessages(preparedMessages),
	}

	// When fallback uses a different provider (e.g. DeepSeek), that provider must not inject web_search_preview.
	nativeSearch, ok := options["native_search"].(bool)
	if !ok {
		// If the option is present but not a bool, log a warning and
		// treat it as false — web_search_preview must not be injected
		// when the caller cannot express a well-typed intent.
		if _, present := options["native_search"]; present {
			logger.WarnCF(
				"provider.openai_compat",
				"native_search option has unexpected type, ignoring",
				map[string]any{
					"type": fmt.Sprintf("%T", options["native_search"]),
				},
			)
		}
	}
	nativeSearch = nativeSearch && isNativeSearchHost(p.endpoint)
	if len(preparedTools) > 0 || nativeSearch {
		requestBody["tools"] = buildToolsList(preparedTools, nativeSearch)
		requestBody["tool_choice"] = "auto"
	}

	if maxTokens, ok := common.AsInt(options["max_tokens"]); ok {
		fieldName := p.maxTokensField
		if fieldName == "" {
			lowerModel := strings.ToLower(model)
			if strings.Contains(lowerModel, "glm") || strings.Contains(lowerModel, "o1") ||
				strings.Contains(lowerModel, "gpt-5") {
				fieldName = "max_completion_tokens"
			} else {
				fieldName = "max_tokens"
			}
		}
		requestBody[fieldName] = maxTokens
	}

	if temperature, ok := common.AsFloat(options["temperature"]); ok {
		lowerModel := strings.ToLower(model)
		if strings.Contains(lowerModel, "kimi") && strings.Contains(lowerModel, "k2") {
			requestBody["temperature"] = 1.0
		} else {
			requestBody["temperature"] = temperature
		}
	}

	// Prompt caching: pass a stable cache key so OpenAI can bucket requests
	// with the same key and reuse prefix KV cache across calls.
	// Prompt caching is only supported by OpenAI-native endpoints.
	// Non-OpenAI providers reject unknown fields with 422 errors.
	if cacheKey, ok := options["prompt_cache_key"].(string); ok && cacheKey != "" {
		if supportsPromptCacheKey(p.endpoint) {
			requestBody["prompt_cache_key"] = cacheKey
		}
	}

	p.applyThinkingControl(requestBody, model, options)

	// Merge extra body fields configured per-provider/model.
	// These are injected last so they take precedence over defaults.
	maps.Copy(requestBody, p.extraBody)

	return requestBody
}

func (p *Provider) applyThinkingControl(requestBody map[string]any, model string, options map[string]any) {
	level, ok := normalizedThinkingLevel(options)
	if !ok {
		return
	}

	if p.SupportsThinking() {
		p.applyDeepSeekThinkingControl(requestBody, level)
		return
	}

	if level != "off" {
		return
	}

	switch p.thinkingControlKind(model) {
	case "thinking_type":
		requestBody["thinking"] = map[string]any{"type": "disabled"}
	case "enable_thinking":
		requestBody["enable_thinking"] = false
	}
}

func (p *Provider) applyDeepSeekThinkingControl(requestBody map[string]any, level string) {
	switch level {
	case "off":
		requestBody["thinking"] = map[string]any{"type": "disabled"}
	case "low", "medium", "high":
		requestBody["thinking"] = map[string]any{"type": "enabled"}
		requestBody["reasoning_effort"] = "high"
	case "xhigh":
		requestBody["thinking"] = map[string]any{"type": "enabled"}
		requestBody["reasoning_effort"] = "max"
	case "adaptive":
		logger.WarnCF("provider.openai_compat",
			`DeepSeek does not support thinking_level="adaptive"; using provider default thinking behavior`,
			map[string]any{
				"provider":       p.providerName,
				"endpoint":       p.endpoint,
				"thinking_level": level,
			},
		)
	}
}

func normalizedThinkingLevel(options map[string]any) (string, bool) {
	raw, ok := options["thinking_level"].(string)
	if !ok {
		return "", false
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "off", "low", "medium", "high", "xhigh", "adaptive":
		return strings.ToLower(strings.TrimSpace(raw)), true
	default:
		return "", false
	}
}

func (p *Provider) thinkingControlKind(model string) string {
	providerName := strings.ToLower(strings.TrimSpace(p.providerName))
	lowerModel := strings.ToLower(strings.TrimSpace(model))

	switch providerName {
	case "volcengine":
		return "thinking_type"
	case "zhipu", "zai":
		return "thinking_type"
	case "qwen-portal", "qwen-intl", "qwen-us":
		return "enable_thinking"
	case "modelscope":
		if strings.Contains(lowerModel, "qwen") {
			return "enable_thinking"
		}
	}

	if providerName == "openai" || providerName == "" {
		if isVolcengineHost(p.endpoint) || strings.Contains(lowerModel, "doubao") {
			return "thinking_type"
		}
		if isDashScopeHost(p.endpoint) || strings.Contains(lowerModel, "qwen") {
			return "enable_thinking"
		}
	}

	return ""
}

func isVolcengineHost(apiBase string) bool {
	host := normalizedHostname(apiBase)
	return host == "volcengine.com" || strings.HasSuffix(host, ".volcengine.com") ||
		host == "volces.com" || strings.HasSuffix(host, ".volces.com")
}

func isDashScopeHost(apiBase string) bool {
	host := normalizedHostname(apiBase)
	return host == "dashscope.aliyuncs.com" || strings.HasSuffix(host, ".dashscope.aliyuncs.com")
}

func normalizedHostname(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(parsed.Hostname()))
}

func (p *Provider) SetProviderName(providerName string) {
	p.providerName = strings.ToLower(strings.TrimSpace(providerName))
}

func (p *Provider) SupportsThinking() bool {
	return strings.EqualFold(strings.TrimSpace(p.providerName), "deepseek") || isDeepSeekHost(p.endpoint)
}

func (p *Provider) prepareMessagesForRequest(messages []Message) []Message {
	if len(messages) == 0 {
		return nil
	}

	if p.requiresToolRoundReasoningReplay() {
		return filterReasoningReplayMessages(messages)
	}
	return stripReasoningMessages(messages)
}

func (p *Provider) requiresToolRoundReasoningReplay() bool {
	return p.providerName == "deepseek" ||
		p.providerName == "mimo" ||
		isDeepSeekHost(p.endpoint) ||
		isMiMoHost(p.endpoint)
}

func isDeepSeekHost(apiBase string) bool {
	parsed, err := url.Parse(strings.TrimSpace(apiBase))
	if err != nil {
		return false
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	return host == "deepseek.com" || strings.HasSuffix(host, ".deepseek.com")
}

func isMiMoHost(apiBase string) bool {
	parsed, err := url.Parse(strings.TrimSpace(apiBase))
	if err != nil {
		return false
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	return host == "xiaomimimo.com" || strings.HasSuffix(host, ".xiaomimimo.com")
}

func filterReasoningReplayMessages(messages []Message) []Message {
	out := make([]Message, 0, len(messages))
	start := 0

	flush := func(end int) {
		if end <= start {
			return
		}
		out = append(out, filterReasoningReplayTurn(messages[start:end])...)
		start = end
	}

	for i := 1; i < len(messages); i++ {
		if messages[i].Role == "user" {
			flush(i)
		}
	}
	flush(len(messages))

	return out
}

func filterReasoningReplayTurn(messages []Message) []Message {
	hasToolInteraction := false
	for _, msg := range messages {
		if msg.Role == "tool" || (msg.Role == "assistant" && len(msg.ToolCalls) > 0) {
			hasToolInteraction = true
			break
		}
	}

	out := make([]Message, 0, len(messages))
	for _, msg := range messages {
		if messageutil.IsTransientAssistantThoughtMessage(msg) {
			continue
		}

		cloned := msg
		// DeepSeek and MiMo only require reasoning_content replay for turns
		// that participate in a tool interaction round. For plain assistant
		// turns between two user messages, the reasoning trace is ignored on
		// replay, so we strip it here.
		if cloned.Role == "assistant" && strings.TrimSpace(cloned.ReasoningContent) != "" && !hasToolInteraction {
			cloned.ReasoningContent = ""
		}
		if assistantMessageEmpty(cloned) {
			continue
		}
		out = append(out, cloned)
	}

	return out
}

func stripReasoningMessages(messages []Message) []Message {
	out := make([]Message, 0, len(messages))
	for _, msg := range messages {
		if messageutil.IsTransientAssistantThoughtMessage(msg) {
			continue
		}

		cloned := msg
		cloned.ReasoningContent = ""
		if assistantMessageEmpty(cloned) {
			continue
		}
		out = append(out, cloned)
	}
	return out
}

func assistantMessageEmpty(msg Message) bool {
	return msg.Role == "assistant" &&
		strings.TrimSpace(msg.Content) == "" &&
		strings.TrimSpace(msg.ReasoningContent) == "" &&
		len(msg.ToolCalls) == 0 &&
		len(msg.Media) == 0 &&
		len(msg.Attachments) == 0 &&
		strings.TrimSpace(msg.ToolCallID) == ""
}

func (p *Provider) Chat(
	ctx context.Context,
	messages []Message,
	tools []ToolDefinition,
	model string,
	options map[string]any,
) (*LLMResponse, error) {
	if p.streamedChat {
		return p.ChatStreamEvents(ctx, messages, tools, model, options, nil)
	}
	requestBody := p.buildRequestBody(messages, tools, model, options)
	resp, err := p.post(ctx, requestBody, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return common.ReadAndParseResponse(resp, p.endpoint)
}

// post sends one Chat Completions request through the client's transport.
func (p *Provider) post(ctx context.Context, requestBody map[string]any, stream bool) (*http.Response, error) {
	if p.httpClient == nil {
		return nil, fmt.Errorf("provider has no transport")
	}
	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatCompletionsURL, bytes.NewReader(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		// http.Client wraps the transport's error in a *url.Error whose text
		// names the placeholder URL. The transport's error is the provider's
		// classified core error, or the context's, and is what callers see.
		var urlErr *url.Error
		if errors.As(err, &urlErr) && urlErr.Err != nil {
			err = urlErr.Err
		}
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		// The transport reports failures as errors, never as responses.
		_ = resp.Body.Close()
		return nil, fmt.Errorf("chat completions transport answered HTTP %d", resp.StatusCode)
	}
	return resp, nil
}

// ChatStream implements streaming via OpenAI-compatible SSE (stream: true).
// onChunk receives the accumulated text so far on each text delta.
func (p *Provider) ChatStream(
	ctx context.Context,
	messages []Message,
	tools []ToolDefinition,
	model string,
	options map[string]any,
	onChunk func(accumulated string),
) (*LLMResponse, error) {
	return p.ChatStreamEvents(
		ctx,
		messages,
		tools,
		model,
		options,
		func(chunk StreamChunk) {
			if onChunk != nil && strings.TrimSpace(chunk.Content) != "" {
				onChunk(chunk.Content)
			}
		},
	)
}

func (p *Provider) ChatStreamEvents(
	ctx context.Context,
	messages []Message,
	tools []ToolDefinition,
	model string,
	options map[string]any,
	onChunk func(StreamChunk),
) (*LLMResponse, error) {
	requestBody := p.buildRequestBody(messages, tools, model, options)
	requestBody["stream"] = true
	resp, err := p.post(ctx, requestBody, true)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return parseStreamResponse(ctx, withStreamingReadIdleTimeout(resp.Body, defaultStreamingReadIdleTimeout), onChunk)
}

func withStreamingReadIdleTimeout(body io.ReadCloser, timeout time.Duration) io.ReadCloser {
	if body == nil || timeout <= 0 {
		return body
	}
	return &streamingReadIdleTimeoutBody{
		body:    body,
		timeout: timeout,
	}
}

type streamingReadIdleTimeoutBody struct {
	body    io.ReadCloser
	timeout time.Duration
}

func (b *streamingReadIdleTimeoutBody) Read(p []byte) (int, error) {
	timedOut := make(chan struct{})
	timer := time.AfterFunc(b.timeout, func() {
		close(timedOut)
		_ = b.body.Close()
	})
	n, err := b.body.Read(p)
	if !timer.Stop() {
		<-timedOut
		return n, &core.ProviderError{
			Message: fmt.Sprintf("stream idle timeout after %s", b.timeout),
			Class:   core.ProviderErrorTransport,
			Classification: core.ProviderErrorClassification{
				Retryable: true, FailoverEligible: true, CircuitFailure: true,
			},
		}
	}
	return n, err
}

func (b *streamingReadIdleTimeoutBody) Close() error {
	return b.body.Close()
}

// streamToolCall is one tool-call delta of a stream chunk.
type streamToolCall struct {
	// Index orders the calls of a message. Some upstreams omit it.
	Index    *int   `json:"index"`
	ID       string `json:"id"`
	Function *struct {
		Name string `json:"name"`
		// Arguments is a fragment of the call's JSON arguments as a JSON
		// string, or, from some upstreams, the complete arguments as an
		// object.
		Arguments        json.RawMessage `json:"arguments"`
		ThoughtSignature string          `json:"thought_signature"`
	} `json:"function"`
	ExtraContent *struct {
		Google *struct {
			ThoughtSignature string `json:"thought_signature"`
		} `json:"google"`
	} `json:"extra_content"`
}

// streamMessage is what a chunk's choice adds to the answer: its delta, or
// the complete message some upstreams put in their last chunk.
type streamMessage struct {
	Content          json.RawMessage   `json:"content"`
	ReasoningContent string            `json:"reasoning_content"`
	Reasoning        string            `json:"reasoning"`
	ReasoningDetails []ReasoningDetail `json:"reasoning_details"`
	ToolCalls        []streamToolCall  `json:"tool_calls"`
}

type streamChoice struct {
	Index        int            `json:"index"`
	Delta        streamMessage  `json:"delta"`
	Message      *streamMessage `json:"message"`
	FinishReason *string        `json:"finish_reason"`
}

// streamToolCalls assembles the tool calls of a stream from their deltas.
//
// A call arrives as OpenAI streams it - its id and name first, then its
// arguments in fragments that share its index - or whole, in one delta,
// even in the chunk that carries the finish reason. A delta joins the call
// with its id, else the call its choice and index name, else, without
// either, the call before it. Under an index already taken, a delta that
// names another id, or another tool once the call is complete, starts a new
// call, as does one without an index that names a tool once the call before
// it is complete.
type streamToolCalls struct {
	calls []*streamToolCallAccum
	byKey map[[2]int]*streamToolCallAccum
	byID  map[string]*streamToolCallAccum
	last  *streamToolCallAccum
}

type streamToolCallAccum struct {
	id, name, signature string
	arguments           strings.Builder
}

// complete reports whether the arguments assembled so far are a whole JSON
// value.
func (c *streamToolCallAccum) complete() bool {
	arguments := strings.TrimSpace(c.arguments.String())
	return arguments != "" && json.Valid([]byte(arguments))
}

func (a *streamToolCalls) add(choice int, delta streamToolCall) {
	call := a.find(choice, delta)
	if id := strings.TrimSpace(delta.ID); id != "" && call.id == "" {
		call.id = id
		a.byID[id] = call
	}
	if delta.Function != nil {
		if name := strings.TrimSpace(delta.Function.Name); name != "" {
			call.name = name
		}
		call.addArguments(delta.Function.Arguments)
		if signature := strings.TrimSpace(delta.Function.ThoughtSignature); signature != "" {
			call.signature = signature
		}
	}
	if extra := delta.ExtraContent; extra != nil && extra.Google != nil {
		if signature := strings.TrimSpace(extra.Google.ThoughtSignature); signature != "" {
			call.signature = signature
		}
	}
	a.last = call
}

func (a *streamToolCalls) find(choice int, delta streamToolCall) *streamToolCallAccum {
	id := strings.TrimSpace(delta.ID)
	if call, ok := a.byID[id]; ok && id != "" {
		return call
	}
	name := ""
	if delta.Function != nil {
		name = strings.TrimSpace(delta.Function.Name)
	}
	if delta.Index != nil {
		key := [2]int{choice, *delta.Index}
		call, ok := a.byKey[key]
		// A complete call is never continued under another tool's name.
		renamed := ok && name != "" && call.name != "" && name != call.name && call.complete()
		if ok && (id == "" || call.id == "") && !renamed {
			return call
		}
		call = a.start()
		a.byKey[key] = call
		return call
	}
	if id == "" && a.last != nil && !(name != "" && a.last.name != "" && a.last.complete()) {
		return a.last
	}
	return a.start()
}

func (a *streamToolCalls) start() *streamToolCallAccum {
	call := &streamToolCallAccum{}
	a.calls = append(a.calls, call)
	return call
}

// addArguments adds a delta's arguments to the call's. A JSON object is the
// complete arguments. A string fragment is appended, except that one opening
// a new object replaces arguments that are already complete: an upstream
// that sends the whole arguments again, or sends "{}" before the real ones,
// must not double them.
func (c *streamToolCallAccum) addArguments(raw json.RawMessage) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return
	}
	if raw[0] != '"' {
		c.arguments.Reset()
		c.arguments.Write(raw)
		return
	}
	var fragment string
	if err := json.Unmarshal(raw, &fragment); err != nil || fragment == "" {
		return
	}
	if c.complete() && strings.HasPrefix(strings.TrimSpace(fragment), "{") {
		c.arguments.Reset()
	}
	c.arguments.WriteString(fragment)
}

// toolCalls returns the assembled calls in the order they began.
func (a *streamToolCalls) toolCalls() []ToolCall {
	var calls []ToolCall
	for _, call := range a.calls {
		if call.id == "" && call.name == "" && call.arguments.Len() == 0 {
			continue
		}
		args := make(map[string]any)
		if raw := strings.TrimSpace(call.arguments.String()); raw != "" {
			if err := json.Unmarshal([]byte(raw), &args); err != nil {
				logger.WarnCF("openai_compat", "stream: failed to decode tool call arguments", map[string]any{
					"tool":  call.name,
					"error": err.Error(),
				})
				args = map[string]any{"raw": raw}
			}
		}
		toolCall := ToolCall{
			ID:        common.ToolCallID(call.id, len(calls)),
			Name:      call.name,
			Arguments: args,
		}
		if call.signature != "" {
			toolCall.ThoughtSignature = call.signature
			toolCall.ExtraContent = &ExtraContent{Google: &GoogleExtra{ThoughtSignature: call.signature}}
		}
		calls = append(calls, toolCall)
	}
	return calls
}

// parseStreamResponse parses an OpenAI-compatible SSE stream.
//
// Every chunk counts until the stream ends, including the one that carries
// the finish reason and any after it, and so does every choice of a chunk:
// some upstreams send a whole tool call in the finishing chunk, or split one
// message's text and tool calls across choices. The answer's tool calls are
// what the stream sent, whatever its finish reason says (see
// common.NormalizeFinishReason).
func parseStreamResponse(
	ctx context.Context,
	reader io.Reader,
	onChunk func(StreamChunk),
) (*LLMResponse, error) {
	var textContent strings.Builder
	var reasoningContent strings.Builder
	var reasoning strings.Builder
	var reasoningDetails []ReasoningDetail
	var finishReason string
	var usage *UsageInfo
	var finalMessage *streamMessage
	var finalMessageChoice int

	tools := &streamToolCalls{
		byKey: map[[2]int]*streamToolCallAccum{},
		byID:  map[string]*streamToolCallAccum{},
	}

	processEvent := func(data string) error {
		if strings.TrimSpace(data) == "" {
			return nil
		}
		if strings.TrimSpace(data) == "[DONE]" {
			return io.EOF
		}

		var chunk struct {
			Choices []streamChoice  `json:"choices"`
			Usage   *UsageInfo      `json:"usage"`
			Error   json.RawMessage `json:"error"`
		}

		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return fmt.Errorf("failed to decode stream event: %w", err)
		}

		if chunk.Usage != nil {
			usage = chunk.Usage
		}

		if len(chunk.Choices) == 0 {
			if message := common.AnswerErrorMessage(chunk.Error); message != "" {
				return common.AnswerError(message)
			}
			return nil
		}

		for _, choice := range chunk.Choices {
			delta := choice.Delta
			if delta.ReasoningContent != "" {
				reasoningContent.WriteString(delta.ReasoningContent)
				if onChunk != nil {
					onChunk(StreamChunk{ReasoningContent: reasoningContent.String()})
				}
			}
			if delta.Reasoning != "" {
				reasoning.WriteString(delta.Reasoning)
				if onChunk != nil {
					onChunk(StreamChunk{ReasoningContent: reasoning.String()})
				}
			}
			if len(delta.ReasoningDetails) > 0 {
				reasoningDetails = append(reasoningDetails, delta.ReasoningDetails...)
			}
			// Accumulate text content after reasoning so UIs can show thought first
			// when a provider sends both fields in the same event.
			if text := common.ContentText(delta.Content); text != "" {
				textContent.WriteString(text)
				if onChunk != nil {
					onChunk(StreamChunk{Content: textContent.String()})
				}
			}
			for _, call := range delta.ToolCalls {
				tools.add(choice.Index, call)
			}
			if choice.Message != nil {
				finalMessage, finalMessageChoice = choice.Message, choice.Index
			}
			if choice.FinishReason != nil && strings.TrimSpace(*choice.FinishReason) != "" {
				finishReason = *choice.FinishReason
			}
		}

		return nil
	}

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024) // 1MB initial, 10MB max
	var eventData strings.Builder
	for scanner.Scan() {
		// Check for context cancellation between chunks
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		line := scanner.Text()
		if line == "" {
			err := processEvent(eventData.String())
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			eventData.Reset()
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}

		data := strings.TrimPrefix(line, "data:")
		data = strings.TrimPrefix(data, " ")
		if eventData.Len() > 0 {
			eventData.WriteByte('\n')
		}
		eventData.WriteString(data)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("streaming read error: %w", err)
	}
	if eventData.Len() > 0 {
		err := processEvent(eventData.String())
		if err != nil && err != io.EOF {
			return nil, err
		}
	}

	// A complete message in the last chunk only fills in what the deltas
	// did not send.
	toolCalls := tools.toolCalls()
	if finalMessage != nil {
		if textContent.Len() == 0 {
			textContent.WriteString(common.ContentText(finalMessage.Content))
		}
		if reasoningContent.Len() == 0 {
			reasoningContent.WriteString(finalMessage.ReasoningContent)
		}
		if reasoning.Len() == 0 {
			reasoning.WriteString(finalMessage.Reasoning)
		}
		if len(reasoningDetails) == 0 {
			reasoningDetails = finalMessage.ReasoningDetails
		}
		if len(toolCalls) == 0 && len(finalMessage.ToolCalls) > 0 {
			for _, call := range finalMessage.ToolCalls {
				tools.add(finalMessageChoice, call)
			}
			toolCalls = tools.toolCalls()
		}
	}

	return &LLMResponse{
		Content:          textContent.String(),
		ReasoningContent: reasoningContent.String(),
		Reasoning:        reasoning.String(),
		ReasoningDetails: reasoningDetails,
		ToolCalls:        toolCalls,
		FinishReason:     common.NormalizeFinishReason(finishReason, len(toolCalls) > 0),
		Usage:            usage,
	}, nil
}

func buildToolsList(tools []ToolDefinition, nativeSearch bool) []any {
	result := make([]any, 0, len(tools)+1)
	for _, t := range tools {
		if nativeSearch && strings.EqualFold(t.Function.Name, "web_search") {
			continue
		}
		result = append(result, t)
	}
	if nativeSearch {
		result = append(result, map[string]any{"type": "web_search_preview"})
	}
	return result
}

func (p *Provider) SupportsNativeSearch() bool {
	return isNativeSearchHost(p.endpoint)
}

// isNativeOpenAIOrAzureEndpoint reports whether the given API base points to
// OpenAI's own API or an Azure OpenAI deployment.
func isNativeOpenAIOrAzureEndpoint(apiBase string) bool {
	u, err := url.Parse(apiBase)
	if err != nil {
		return false
	}
	host := u.Hostname()
	return host == "api.openai.com" || strings.HasSuffix(host, ".openai.azure.com")
}

func isNativeSearchHost(apiBase string) bool {
	return isNativeOpenAIOrAzureEndpoint(apiBase)
}

// supportsPromptCacheKey reports whether the given API base is known to
// support the prompt_cache_key request field. Currently only OpenAI's own
// API and Azure OpenAI support this. All other OpenAI-compatible providers
// (Mistral, Gemini, DeepSeek, Groq, etc.) reject unknown fields with 422 errors.
func supportsPromptCacheKey(apiBase string) bool {
	return isNativeOpenAIOrAzureEndpoint(apiBase)
}
