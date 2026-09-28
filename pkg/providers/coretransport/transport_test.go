package coretransport

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	core "github.com/xibodev/llmgw-core"
	"github.com/xibodev/llmgw-core/translation"
)

type fakeProvider struct {
	requests  []core.Request
	invoke    func(core.Request) (core.Response, error)
	stream    func(core.Request) (core.StreamIter, error)
	surfaces  []core.ModelSurface
	listCalls int
}

func (p *fakeProvider) NativeSurfaces(string) []core.ModelSurface { return p.surfaces }

func (p *fakeProvider) Invoke(_ context.Context, request core.Request) (core.Response, error) {
	p.requests = append(p.requests, request)
	return p.invoke(request)
}

func (p *fakeProvider) Stream(_ context.Context, request core.Request) (core.StreamIter, error) {
	p.requests = append(p.requests, request)
	return p.stream(request)
}

func (p *fakeProvider) ListModels(context.Context, *core.Credential) ([]core.ModelInfo, error) {
	p.listCalls++
	return nil, nil
}

type frames struct {
	items  [][]byte
	closed bool
}

func (f *frames) Next() ([]byte, error) {
	if len(f.items) == 0 {
		return nil, io.EOF
	}
	item := f.items[0]
	f.items = f.items[1:]
	return item, nil
}

func (f *frames) Close() error { f.closed = true; return nil }

func post(t *testing.T, transport *Transport, path, body string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "http://core.invalid"+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip() error = %v", err)
	}
	return resp
}

func TestInvokeCarriesSurfaceModelAndCredential(t *testing.T) {
	provider := &fakeProvider{invoke: func(core.Request) (core.Response, error) {
		return core.Response{Body: []byte(`{"ok":true}`), ContentType: "application/json"}, nil
	}}
	credential := &core.Credential{APIKey: "k"}
	resp := post(t, &Transport{Provider: provider, Credential: StaticCredential(credential)}, "/v1/chat/completions", `{"model":"m1"}`)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != `{"ok":true}` {
		t.Fatalf("response = %d %s", resp.StatusCode, body)
	}
	got := provider.requests[0]
	if got.Surface != core.ModelSurfaceChatCompletions || got.Model != "m1" || got.Credential != credential || string(got.Body) != `{"model":"m1"}` {
		t.Fatalf("request = %+v", got)
	}
}

func TestStreamWritesEveryFrameAsOneSSERecord(t *testing.T) {
	iter := &frames{items: [][]byte{[]byte("data: {\"a\":1}\n\n"), []byte("data: {\"b\":2}"), []byte("data: [DONE]\n")}}
	provider := &fakeProvider{stream: func(core.Request) (core.StreamIter, error) { return iter, nil }}
	resp := post(t, &Transport{Provider: provider}, "/v1/chat/completions", `{"model":"m","stream":true}`)
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type = %q", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if want := "data: {\"a\":1}\n\ndata: {\"b\":2}\n\ndata: [DONE]\n\n"; string(body) != want {
		t.Fatalf("body = %q, want %q", body, want)
	}
	if !iter.closed {
		t.Fatal("stream was not closed with the body")
	}
}

func roundTripError(t *testing.T, transport *Transport, ctx context.Context, body string) error {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "http://core.invalid/v1/chat/completions", strings.NewReader(body)).WithContext(ctx)
	resp, err := transport.RoundTrip(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatalf("RoundTrip() status = %d, want an error", resp.StatusCode)
	}
	return err
}

func TestProviderErrorIsReturnedWithItsClassification(t *testing.T) {
	want := &core.ProviderError{
		Message: "rate limited", Class: core.ProviderErrorRateLimited,
		Classification: core.ProviderErrorClassification{StatusCode: 429, Retryable: true, FailoverEligible: true, RetryAfter: 1500 * time.Millisecond},
	}
	provider := &fakeProvider{invoke: func(core.Request) (core.Response, error) { return core.Response{}, want }}
	err := roundTripError(t, &Transport{Provider: provider}, context.Background(), `{"model":"m"}`)
	var got *core.ProviderError
	if !errors.As(err, &got) || got != want {
		t.Fatalf("error = %#v, want the provider's error", err)
	}
	if classification := core.ClassifyError(err); classification.RetryAfter != 1500*time.Millisecond || classification.StatusCode != 429 {
		t.Fatalf("classification = %+v", classification)
	}
}

func TestClientSurfacesTheCoreErrorThroughURLError(t *testing.T) {
	want := &core.ProviderError{Message: "down", Class: core.ProviderErrorUpstream,
		Classification: core.ProviderErrorClassification{StatusCode: 503, Retryable: true, FailoverEligible: true, CircuitFailure: true}}
	provider := &fakeProvider{invoke: func(core.Request) (core.Response, error) { return core.Response{}, want }}
	resp, err := Client(&Transport{Provider: provider}).Post(BaseURL+"/chat/completions", "application/json", strings.NewReader(`{"model":"m"}`))
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("Post() error = nil")
	}
	var got *core.ProviderError
	if !errors.As(err, &got) || core.ClassifyError(err).Disposition() != core.DispositionRetryable {
		t.Fatalf("error = %#v, want the core error through *url.Error", err)
	}
}

func TestCanceledRequestReportsTheContextError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	provider := &fakeProvider{invoke: func(core.Request) (core.Response, error) {
		cancel()
		return core.Response{}, &core.ProviderError{Message: "transport", Class: core.ProviderErrorTransport,
			Classification: core.ProviderErrorClassification{Retryable: true, FailoverEligible: true, CircuitFailure: true}}
	}}
	err := roundTripError(t, &Transport{Provider: provider}, ctx, `{"model":"m"}`)
	if !errors.Is(err, context.Canceled) || core.ClassifyError(err).Disposition() != core.DispositionTerminal {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestStreamFailureBeforeFirstFrameIsReturned(t *testing.T) {
	want := &core.ProviderError{Message: "overloaded", Class: core.ProviderErrorUpstream,
		Classification: core.ProviderErrorClassification{StatusCode: 503, Retryable: true, FailoverEligible: true}}
	iter := &failingFrames{err: want}
	provider := &fakeProvider{stream: func(core.Request) (core.StreamIter, error) { return iter, nil }}
	err := roundTripError(t, &Transport{Provider: provider}, context.Background(), `{"model":"m","stream":true}`)
	if !errors.Is(err, want) || !iter.closed {
		t.Fatalf("error = %v closed = %v", err, iter.closed)
	}
}

func TestStreamFailureAfterFramesFailsTheBodyRead(t *testing.T) {
	want := &core.ProviderError{Message: "reset", Class: core.ProviderErrorTransport,
		Classification: core.ProviderErrorClassification{Retryable: true, FailoverEligible: true, CircuitFailure: true}}
	iter := &failingFrames{frames: [][]byte{[]byte("data: {\"a\":1}\n\n")}, err: want}
	provider := &fakeProvider{stream: func(core.Request) (core.StreamIter, error) { return iter, nil }}
	resp := post(t, &Transport{Provider: provider}, "/v1/chat/completions", `{"model":"m","stream":true}`)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if string(body) != "data: {\"a\":1}\n\n" || !errors.Is(err, want) {
		t.Fatalf("body = %q err = %v", body, err)
	}
}

func TestCredentialFailureIsAConfigurationError(t *testing.T) {
	provider := &fakeProvider{}
	transport := &Transport{Provider: provider, Credential: func(context.Context) (*core.Credential, error) {
		return nil, errors.New("sign in first")
	}}
	err := roundTripError(t, transport, context.Background(), `{"model":"m"}`)
	var providerErr *core.ProviderError
	if !errors.As(err, &providerErr) || providerErr.Class != core.ProviderErrorConfiguration ||
		core.ClassifyError(err).Disposition() != core.DispositionFailover || len(provider.requests) != 0 {
		t.Fatalf("error = %#v, provider called %d times", err, len(provider.requests))
	}
	if !strings.Contains(err.Error(), "sign in first") {
		t.Fatalf("error = %q, want the credential's reason", err)
	}
}

type failingFrames struct {
	frames [][]byte
	err    error
	closed bool
}

func (f *failingFrames) Next() ([]byte, error) {
	if len(f.frames) == 0 {
		return nil, f.err
	}
	frame := f.frames[0]
	f.frames = f.frames[1:]
	return frame, nil
}

func (f *failingFrames) Close() error { f.closed = true; return nil }

func TestUnstreamedChatIsReplayedAsOneChunk(t *testing.T) {
	provider := &fakeProvider{
		stream: func(core.Request) (core.StreamIter, error) {
			return nil, &core.ProviderError{Message: "no streaming", Class: core.ProviderErrorUnsupported}
		},
		invoke: func(request core.Request) (core.Response, error) {
			if strings.Contains(string(request.Body), `"stream":true`) {
				t.Errorf("replayed request still streams: %s", request.Body)
			}
			return core.Response{Body: []byte(`{"id":"c","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hi","tool_calls":[{"id":"t1","type":"function","function":{"name":"f","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)}, nil
		},
	}
	resp := post(t, &Transport{Provider: provider}, "/v1/chat/completions", `{"model":"m","stream":true}`)
	body, _ := io.ReadAll(resp.Body)
	text := string(body)
	if !strings.HasPrefix(text, "data: ") || !strings.HasSuffix(text, "data: [DONE]\n\n") ||
		!strings.Contains(text, `"delta":{"content":"hi"`) || !strings.Contains(text, `"index":0`) {
		t.Fatalf("replayed stream = %q", text)
	}
}

func TestChatOverResponsesIsReplayedAsOneChunk(t *testing.T) {
	provider := &fakeProvider{
		surfaces: []core.ModelSurface{core.ModelSurfaceResponses},
		stream: func(core.Request) (core.StreamIter, error) {
			t.Error("the adapter streamed Chat over Responses")
			return nil, errors.New("unexpected stream")
		},
		invoke: func(request core.Request) (core.Response, error) {
			if request.Surface != core.ModelSurfaceResponses {
				t.Errorf("replayed request surface = %s, want responses", request.Surface)
			}
			return core.Response{Body: []byte(`{"id":"resp_1","object":"response","model":"m","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}]}`)}, nil
		},
	}
	resp := post(t, &Transport{Provider: translation.Adapter{Provider: provider}}, "/v1/chat/completions", `{"model":"m","stream":true,"messages":[{"role":"user","content":"hello"}]}`)
	body, _ := io.ReadAll(resp.Body)
	if text := string(body); !strings.Contains(text, `"content":"hi"`) || !strings.HasSuffix(text, "data: [DONE]\n\n") || len(provider.requests) != 1 {
		t.Fatalf("replayed stream = %q after %d requests", text, len(provider.requests))
	}
}

func TestStreamRefusedForAnotherSurfaceIsNotReplayed(t *testing.T) {
	provider := &fakeProvider{surfaces: []core.ModelSurface{core.ModelSurfaceEmbeddings}}
	req := httptest.NewRequest(http.MethodPost, "http://core.invalid/v1/chat/completions", strings.NewReader(`{"model":"m","stream":true,"messages":[]}`))
	_, err := (&Transport{Provider: translation.Adapter{Provider: provider}}).RoundTrip(req)
	var surfaceErr *core.SurfaceError
	if !errors.As(err, &surfaceErr) || len(provider.requests) != 0 {
		t.Fatalf("err = %v after %d requests, want the SurfaceError and nothing sent", err, len(provider.requests))
	}
}

func TestRejectsNonSurfacePaths(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "http://core.invalid/v1/files", strings.NewReader(`{}`))
	if _, err := (&Transport{Provider: &fakeProvider{}}).RoundTrip(req); err == nil {
		t.Fatal("RoundTrip() error = nil for a non-surface path")
	}
}
