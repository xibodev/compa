package openai_compat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	core "github.com/xibodev/llmgw-core"

	"github.com/xibodev/compa/v3/pkg/providers/coretransport"
)

// scriptedStream yields frames, waiting delay before each, and then either
// ends or, with stall, blocks until it is closed, as a read on an HTTP body
// the upstream stopped sending does.
type scriptedStream struct {
	frames    []string
	delay     time.Duration
	stall     bool
	closed    chan struct{}
	closeOnce sync.Once
}

func (s *scriptedStream) Next() ([]byte, error) {
	if len(s.frames) > 0 {
		select {
		case <-time.After(s.delay):
		case <-s.closed:
			return nil, errors.New("read on closed body")
		}
		frame := s.frames[0]
		s.frames = s.frames[1:]
		return []byte("data: " + frame + "\n\n"), nil
	}
	if s.stall {
		<-s.closed
		return nil, errors.New("read on closed body")
	}
	return nil, io.EOF
}

func (s *scriptedStream) Close() error {
	s.closeOnce.Do(func() { close(s.closed) })
	return nil
}

// scriptedProvider is a core provider whose streams are scripted. It
// records the bodies of the requests it gets.
type scriptedProvider struct {
	mu     sync.Mutex
	bodies []map[string]any
	stream func() *scriptedStream
}

func (p *scriptedProvider) NativeSurfaces(string) []core.ModelSurface {
	return []core.ModelSurface{core.ModelSurfaceChatCompletions}
}

func (p *scriptedProvider) record(request core.Request) {
	var body map[string]any
	_ = json.Unmarshal(request.Body, &body)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bodies = append(p.bodies, body)
}

func (p *scriptedProvider) Invoke(_ context.Context, request core.Request) (core.Response, error) {
	p.record(request)
	return core.Response{Body: []byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)}, nil
}

func (p *scriptedProvider) Stream(_ context.Context, request core.Request) (core.StreamIter, error) {
	p.record(request)
	s := p.stream()
	s.closed = make(chan struct{})
	return s, nil
}

func (p *scriptedProvider) ListModels(context.Context, *core.Credential) ([]core.ModelInfo, error) {
	return nil, nil
}

func (p *scriptedProvider) lastBody(t *testing.T) map[string]any {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.bodies) == 0 {
		t.Fatal("no request reached the provider")
	}
	return p.bodies[len(p.bodies)-1]
}

func scriptedChatClient(provider *scriptedProvider, transport *coretransport.Transport, opts ...Option) *Provider {
	if transport == nil {
		transport = &coretransport.Transport{}
	}
	transport.Provider = provider
	return NewProvider("https://api.example.test/v1", coretransport.Client(transport), opts...)
}

func streamScripted(t *testing.T, client *Provider) (*LLMResponse, error) {
	t.Helper()
	return client.ChatStream(t.Context(), []Message{{Role: "user", Content: "hi"}}, nil, "m", nil, nil)
}

const (
	textFrame   = `{"choices":[{"index":0,"delta":{"content":"hi"}}]}`
	finishFrame = `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`
)

// A stream that stops sending mid-answer fails once the idle watchdog
// fires, instead of hanging until data comes; one that is slow but keeps
// sending within the timeout is delivered whole.
func TestStreamIdleWatchdogThroughCoreTransport(t *testing.T) {
	t.Run("stalled", func(t *testing.T) {
		provider := &scriptedProvider{stream: func() *scriptedStream {
			return &scriptedStream{frames: []string{textFrame}, stall: true}
		}}
		client := scriptedChatClient(provider, nil, WithStreamIdleTimeout(150*time.Millisecond))

		start := time.Now()
		_, err := streamScripted(t, client)
		elapsed := time.Since(start)

		var providerErr *core.ProviderError
		var netErr net.Error
		if !errors.As(err, &providerErr) || !errors.As(err, &netErr) || !netErr.Timeout() ||
			!core.ClassifyError(err).FailoverEligible {
			t.Fatalf("ChatStream() error = %v, want the idle timeout", err)
		}
		if elapsed < 150*time.Millisecond || elapsed > 3*time.Second {
			t.Fatalf("the stalled stream failed after %s, want soon after 150ms", elapsed)
		}
	})

	t.Run("slow but healthy", func(t *testing.T) {
		provider := &scriptedProvider{stream: func() *scriptedStream {
			return &scriptedStream{
				frames: []string{textFrame, textFrame, textFrame, textFrame, finishFrame, "[DONE]"},
				delay:  60 * time.Millisecond,
			}
		}}
		client := scriptedChatClient(provider, &coretransport.Transport{FirstFrameTimeout: 200 * time.Millisecond},
			WithStreamIdleTimeout(200*time.Millisecond))

		start := time.Now()
		out, err := streamScripted(t, client)
		if err != nil {
			t.Fatalf("ChatStream() error = %v after %s, want the slow stream delivered", err, time.Since(start))
		}
		if out.Content != "hihihihi" || out.FinishReason != "stop" {
			t.Fatalf("answer = %#v", out)
		}
		if elapsed := time.Since(start); elapsed < 300*time.Millisecond {
			t.Fatalf("the stream took %s, shorter than its frames' delays", elapsed)
		}
	})

	t.Run("nothing at all", func(t *testing.T) {
		provider := &scriptedProvider{stream: func() *scriptedStream { return &scriptedStream{stall: true} }}
		client := scriptedChatClient(provider, &coretransport.Transport{FirstFrameTimeout: 100 * time.Millisecond})

		start := time.Now()
		_, err := streamScripted(t, client)
		var netErr net.Error
		if !errors.As(err, &netErr) || !netErr.Timeout() || time.Since(start) > 3*time.Second {
			t.Fatalf("ChatStream() error = %v after %s, want the first-frame timeout", err, time.Since(start))
		}
	})
}

func TestStreamAsksForUsage(t *testing.T) {
	frames := func() *scriptedStream {
		return &scriptedStream{frames: []string{
			textFrame, finishFrame,
			`{"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`, "[DONE]",
		}}
	}
	provider := &scriptedProvider{stream: frames}
	out, err := streamScripted(t, scriptedChatClient(provider, nil))
	if err != nil {
		t.Fatal(err)
	}
	options, _ := provider.lastBody(t)["stream_options"].(map[string]any)
	if options["include_usage"] != true {
		t.Fatalf("stream_options = %#v, want include_usage", provider.lastBody(t)["stream_options"])
	}
	if out.Usage == nil || out.Usage.TotalTokens != 5 {
		t.Fatalf("usage = %#v", out.Usage)
	}

	extra := map[string]any{"stream_options": map[string]any{"include_usage": false}}
	if _, err := streamScripted(t, scriptedChatClient(provider, nil, WithExtraBody(extra))); err != nil {
		t.Fatal(err)
	}
	if options, _ := provider.lastBody(t)["stream_options"].(map[string]any); options["include_usage"] != false {
		t.Fatalf("stream_options = %#v, want extra_body's", options)
	}
}

// usageRefusingProvider refuses a stream that names stream_options, as an
// upstream that does not know the field does.
type usageRefusingProvider struct{ scriptedProvider }

func (p *usageRefusingProvider) Stream(_ context.Context, request core.Request) (core.StreamIter, error) {
	p.record(request)
	if strings.Contains(string(request.Body), "stream_options") {
		return nil, &core.ProviderError{
			Message: "upstream returned 400: Unrecognized request argument supplied: stream_options",
			Class:   core.ProviderErrorInvalidRequest, Classification: core.ProviderErrorClassification{StatusCode: 400},
		}
	}
	return &scriptedStream{frames: []string{textFrame, finishFrame, "[DONE]"}, closed: make(chan struct{})}, nil
}

func TestStreamUsageIsDroppedForAnUpstreamThatRefusesIt(t *testing.T) {
	provider := &usageRefusingProvider{}
	client := NewProvider("https://api.example.test/v1", coretransport.Client(&coretransport.Transport{Provider: provider}))
	for i := range 2 {
		if out, err := streamScripted(t, client); err != nil || out.Content != "hi" {
			t.Fatalf("stream %d = %#v, %v; want it served without stream_options", i, out, err)
		}
	}
	if len(provider.bodies) != 3 {
		t.Fatalf("%d requests, want the refused one, its retry and one more without stream_options", len(provider.bodies))
	}
	if _, set := provider.bodies[2]["stream_options"]; set {
		t.Fatal("stream_options sent again after the upstream refused it")
	}
}

func streamFrames(t *testing.T, frames ...string) (*LLMResponse, error) {
	t.Helper()
	provider := &scriptedProvider{stream: func() *scriptedStream { return &scriptedStream{frames: frames} }}
	return streamScripted(t, scriptedChatClient(provider, nil))
}

func TestStreamErrorsFailTheAnswer(t *testing.T) {
	for name, frames := range map[string][]string{
		"error beside choices": {
			textFrame,
			`{"choices":[{"index":0,"delta":{"content":""},"finish_reason":"error"}],"error":{"message":"upstream crashed"}}`,
		},
		"finish reason error": {textFrame, `{"choices":[{"index":0,"delta":{},"finish_reason":"error"}]}`},
		"error on the choice": {textFrame, `{"choices":[{"index":0,"delta":{},"error":{"message":"overloaded"}}]}`},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := streamFrames(t, append(frames, "[DONE]")...)
			var providerErr *core.ProviderError
			if !errors.As(err, &providerErr) || !core.ClassifyError(err).FailoverEligible {
				t.Fatalf("ChatStream() = %#v, %v; want the stream's error", out, err)
			}
		})
	}
}

func TestStreamWithoutAnEndIsTruncated(t *testing.T) {
	out, err := streamFrames(t, textFrame, textFrame)
	if err != nil || out.FinishReason != "truncated" || out.Content != "hihi" {
		t.Fatalf("ChatStream() = %#v, %v; want the partial text, truncated", out, err)
	}
}

func TestStreamCutOffInAToolCallRunsNoTool(t *testing.T) {
	out, err := streamFrames(t,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"exec","arguments":"{\"command\":\"rm -rf /tmp/x"}}]}}]}`,
	)
	if err == nil || out != nil {
		t.Fatalf("ChatStream() = %#v, %v; want an error and no tool call", out, err)
	}
	if !core.ClassifyError(err).FailoverEligible {
		t.Fatalf("error %v is not failover-eligible", err)
	}

	// Complete arguments without a finish reason or [DONE] are not trusted
	// either: more calls may have been coming.
	if out, err := streamFrames(t,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"exec","arguments":"{\"command\":\"ls\"}"}}]}}]}`,
	); err == nil {
		t.Fatalf("ChatStream() = %#v, want an error for a truncated stream with tool calls", out)
	}
}

func TestStreamToolCallWithBrokenArgumentsIsNotRun(t *testing.T) {
	out, err := streamFrames(t,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"exec","arguments":"{not json"}}]},"finish_reason":"tool_calls"}]}`,
		"[DONE]",
	)
	if err == nil || out != nil {
		t.Fatalf("ChatStream() = %#v, %v; want an error", out, err)
	}
}

func TestChatToolCallWithBrokenArgumentsIsNotRun(t *testing.T) {
	_, err := toolCallUpstream(t, nil, `{"choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"exec","arguments":"{not json"}}]},"finish_reason":"tool_calls"}]}`).
		Chat(t.Context(), []Message{{Role: "user", Content: "hi"}}, nil, "m", nil)
	if err == nil || !core.ClassifyError(err).FailoverEligible {
		t.Fatalf("Chat() error = %v, want a failover-eligible error", err)
	}
}

func TestMaxTokensFieldForReasoningModels(t *testing.T) {
	for model, want := range map[string]string{
		"o1":             "max_completion_tokens",
		"o3":             "max_completion_tokens",
		"o3-mini":        "max_completion_tokens",
		"o4-mini":        "max_completion_tokens",
		"openai/o3":      "max_completion_tokens",
		"gpt-5":          "max_completion_tokens",
		"gpt-5-mini":     "max_completion_tokens",
		"glm-4.6":        "max_completion_tokens",
		"gpt-4o":         "max_tokens",
		"omni-moderator": "max_tokens",
		"llama-3.3-70b":  "max_tokens",
	} {
		body := NewProvider("https://api.example.test/v1", nil).buildRequestBody(nil, nil, model, map[string]any{"max_tokens": 100})
		if body[want] != 100 || len(body) != 3 {
			t.Errorf("%s: body = %v, want %s", model, body, want)
		}
	}
	// A configured field wins.
	body := NewProvider("https://api.example.test/v1", nil, WithMaxTokensField("max_tokens")).
		buildRequestBody(nil, nil, "o3", map[string]any{"max_tokens": 100})
	if body["max_tokens"] != 100 {
		t.Fatalf("body = %v, want the configured field", body)
	}
}

func TestCloseClosesTheIdleConnections(t *testing.T) {
	closed := 0
	transport := &coretransport.Transport{Provider: &scriptedProvider{}, CloseIdle: func() { closed++ }}
	NewProvider("https://api.example.test/v1", coretransport.Client(transport)).Close()
	if closed != 1 {
		t.Fatalf("idle connections closed %d times, want once", closed)
	}
	NewProvider("https://api.example.test/v1", nil).Close() // no client: nothing to close
}
