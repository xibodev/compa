// Package coretransport hands the surface requests Compa's chat client makes
// to an llmgw-core provider. Transport is an http.RoundTripper: it reads the
// surface from the request path and the model and stream flag from the body,
// calls the provider's Invoke or Stream, and turns a successful answer back
// into an HTTP response - a JSON body or an SSE body built from the
// provider's frames.
//
// A failure is never turned into an HTTP error response. RoundTrip returns
// the provider's own error, a classified core error that carries its routing
// metadata (core.ClassifyError), and a stream that fails later fails its
// body's Read with that error. The client unwraps the *url.Error http.Client
// adds, so errors.As finds the core error. Once the request's context ends,
// the error is the context's, so cancellation stays distinct from a provider
// failure. A stream that sends nothing within FirstFrameTimeout fails as a
// transport timeout, and closing a stream's body interrupts a Read waiting
// on it.
package coretransport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	core "github.com/xibodev/llmgw-core"
	"github.com/xibodev/llmgw-core/translation"
)

// CredentialSource returns the credential for one request, or nil for a
// provider that needs none. It runs per request, so a refreshable credential
// is always current.
type CredentialSource func(ctx context.Context) (*core.Credential, error)

// CredentialRefresher returns the credential to use instead of rejected,
// which the upstream refused with 401, such as a refreshed OAuth token.
type CredentialRefresher func(ctx context.Context, rejected *core.Credential) (*core.Credential, error)

// Transport sends surface requests to one core provider.
type Transport struct {
	// Provider serves the requests.
	Provider core.Provider
	// Credential returns each request's credential. Nil sends none.
	Credential CredentialSource
	// Refresh, when set, replaces a credential other than an API key that
	// the upstream rejected with 401, and the request is sent once more
	// with the new one. A stream is sent again only while none of it has
	// been delivered.
	Refresh CredentialRefresher
	// FirstFrameTimeout bounds the wait for a stream's first frame; zero
	// uses DefaultFirstFrameTimeout.
	FirstFrameTimeout time.Duration
	// CloseIdle, when set, closes the idle connections of the provider's
	// HTTP client (see CloseIdleConnections).
	CloseIdle func()
}

// DefaultFirstFrameTimeout is how long a stream may take to send its first
// frame before the request fails.
const DefaultFirstFrameTimeout = 2 * time.Minute

// CloseIdleConnections closes the idle connections of the provider's HTTP
// client, so a client of t that is no longer used holds none open.
// http.Client.CloseIdleConnections calls it.
func (t *Transport) CloseIdleConnections() {
	if t.CloseIdle != nil {
		t.CloseIdle()
	}
}

// maxRequestBody bounds how much of a surface request the transport buffers.
const maxRequestBody = 64 << 20

// RoundTrip performs req's surface operation on the provider.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		defer req.Body.Close()
	}
	if t.Provider == nil {
		return nil, errors.New("core transport: provider is required")
	}
	if req.Method != http.MethodPost {
		return nil, fmt.Errorf("core transport: unsupported method %s", req.Method)
	}
	surface := core.ParseSurfacePath(req.URL.Path)
	if surface == "" {
		return nil, fmt.Errorf("core transport: %q is not a model surface", req.URL.Path)
	}

	var body []byte
	if req.Body != nil {
		var err error
		body, err = io.ReadAll(io.LimitReader(req.Body, maxRequestBody+1))
		if err != nil {
			return nil, fmt.Errorf("core transport: reading request: %w", err)
		}
		if len(body) > maxRequestBody {
			return nil, errors.New("core transport: request body too large")
		}
	}
	var probe struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return nil, fmt.Errorf("core transport: request body is not JSON: %w", err)
	}

	request := core.Request{Surface: surface, Model: probe.Model, Body: body, ContentType: req.Header.Get("Content-Type")}
	if request.ContentType == "" {
		request.ContentType = "application/json"
	}
	ctx := req.Context()
	if t.Credential != nil {
		credential, err := t.Credential(ctx)
		if err != nil {
			return nil, credentialFailure(ctx, err)
		}
		request.Credential = credential
	}

	if !probe.Stream {
		response, err := t.Provider.Invoke(ctx, request)
		if retry, ok := t.refreshed(ctx, request, err); ok {
			request = retry
			response, err = t.Provider.Invoke(ctx, request)
		}
		if err != nil {
			return nil, failure(ctx, err)
		}
		contentType := response.ContentType
		if contentType == "" {
			contentType = "application/json"
		}
		return newResponse(req, http.StatusOK, contentType, io.NopCloser(bytes.NewReader(response.Body))), nil
	}

	stream, replay, err := t.openStream(ctx, request)
	if retry, ok := t.refreshed(ctx, request, err); ok {
		request = retry
		stream, replay, err = t.openStream(ctx, request)
	}
	if err != nil {
		if !replay {
			return nil, failure(ctx, err)
		}
		// The provider serves Chat Completions but does not stream it, as
		// Google does not yet and translation.Adapter does not over
		// Responses: answer once and replay it as one chunk.
		return t.replayAsStream(req, request)
	}
	return newResponse(req, http.StatusOK, "text/event-stream", stream), nil
}

// openStream opens request's stream and waits for its first frame, at most
// FirstFrameTimeout, so a stream that stalls before sending anything fails
// the request instead of holding it, and the request's context ends the wait.
// replay reports a Chat Completions stream the provider refused to open that
// one non-streamed answer can stand in for.
func (t *Transport) openStream(ctx context.Context, request core.Request) (*frameBody, bool, error) {
	stream, err := t.Provider.Stream(ctx, request)
	if err != nil {
		return nil, request.Surface == core.ModelSurfaceChatCompletions && replayable(t.Provider, request.Model, err), err
	}
	body := newFrameBody(ctx, stream)
	timeout := t.FirstFrameTimeout
	if timeout <= 0 {
		timeout = DefaultFirstFrameTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	first, err := body.next(timer.C, timeout)
	switch {
	case err == nil:
		body.pending = first
	case errors.Is(err, io.EOF):
		body.done = true
	case ctx.Err() != nil || isStall(err):
		// The stream may still be blocked in Next: close it without
		// waiting for it.
		go func() { _ = body.Close() }()
		return nil, false, err
	default:
		_ = body.Close()
		return nil, false, err
	}
	return body, false, nil
}

// refreshed returns request carrying a replacement for its credential when
// err is the upstream's 401 for a credential Refresh can replace: one that
// is not an API key.
func (t *Transport) refreshed(ctx context.Context, request core.Request, err error) (core.Request, bool) {
	if t.Refresh == nil || err == nil || ctx.Err() != nil || request.Credential == nil ||
		request.Credential.TokenType == core.TokenTypeAPIKey ||
		core.ClassifyError(err).StatusCode != http.StatusUnauthorized {
		return request, false
	}
	credential, refreshErr := t.Refresh(ctx, request.Credential)
	if refreshErr != nil || credential == nil {
		return request, false
	}
	request.Credential = credential
	return request, true
}

// replayAsStream invokes request without streaming and returns its Chat
// Completions answer as the SSE stream it would have been.
func (t *Transport) replayAsStream(req *http.Request, request core.Request) (*http.Response, error) {
	var payload map[string]any
	if err := json.Unmarshal(request.Body, &payload); err != nil {
		return nil, fmt.Errorf("core transport: request body is not JSON: %w", err)
	}
	payload["stream"] = false
	delete(payload, "stream_options")
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("core transport: encoding request: %w", err)
	}
	request.Body = body
	response, err := t.Provider.Invoke(req.Context(), request)
	if retry, ok := t.refreshed(req.Context(), request, err); ok {
		response, err = t.Provider.Invoke(req.Context(), retry)
	}
	if err != nil {
		return nil, failure(req.Context(), err)
	}
	frames, err := completionFrames(response.Body)
	if err != nil {
		return nil, fmt.Errorf("core transport: %w", err)
	}
	return newResponse(req, http.StatusOK, "text/event-stream", io.NopCloser(bytes.NewReader(frames))), nil
}

// completionFrames turns a chat.completion into the SSE frames of the
// equivalent stream: one chunk per choice carrying its whole message as the
// delta, then [DONE].
func completionFrames(raw []byte) ([]byte, error) {
	var completion map[string]any
	if err := json.Unmarshal(raw, &completion); err != nil {
		return nil, fmt.Errorf("answer is not JSON: %w", err)
	}
	chunk := map[string]any{"object": "chat.completion.chunk"}
	for _, field := range []string{"id", "created", "model", "usage"} {
		if value, ok := completion[field]; ok {
			chunk[field] = value
		}
	}
	rawChoices, _ := completion["choices"].([]any)
	choices := make([]any, 0, len(rawChoices))
	for index, rawChoice := range rawChoices {
		choice, _ := rawChoice.(map[string]any)
		delta, _ := choice["message"].(map[string]any)
		if delta == nil {
			delta = map[string]any{}
		}
		if calls, ok := delta["tool_calls"].([]any); ok {
			indexed := make([]any, 0, len(calls))
			for callIndex, rawCall := range calls {
				call, _ := rawCall.(map[string]any)
				copied := make(map[string]any, len(call)+1)
				for key, value := range call {
					copied[key] = value
				}
				copied["index"] = callIndex
				indexed = append(indexed, copied)
			}
			delta["tool_calls"] = indexed
		}
		out := map[string]any{"index": index, "delta": delta, "finish_reason": choice["finish_reason"]}
		if value, ok := choice["index"]; ok {
			out["index"] = value
		}
		choices = append(choices, out)
	}
	chunk["choices"] = choices
	encoded, err := json.Marshal(chunk)
	if err != nil {
		return nil, err
	}
	var frames bytes.Buffer
	frames.WriteString("data: ")
	frames.Write(encoded)
	frames.WriteString("\n\ndata: [DONE]\n\n")
	return frames.Bytes(), nil
}

// replayable reports a Chat Completions stream that failed only because the
// provider does not stream what it answers: a native surface it refuses to
// stream, or a translation that answers without streaming, which the
// translation.Adapter refuses with a SurfaceError before anything is sent.
func replayable(provider core.Provider, model string, err error) bool {
	if unsupported(err) {
		return true
	}
	var surfaceErr *core.SurfaceError
	if !errors.As(err, &surfaceErr) {
		return false
	}
	native := provider.NativeSurfaces(model)
	return translation.ServesChat(native...) && !translation.ServesStream(core.ModelSurfaceChatCompletions, native...)
}

func unsupported(err error) bool {
	var surfaceErr *core.SurfaceError
	if errors.As(err, &surfaceErr) {
		return false
	}
	var providerErr *core.ProviderError
	return errors.As(err, &providerErr) && providerErr.Class == core.ProviderErrorUnsupported
}

// frameBody is an SSE response body read from a provider stream. Each frame
// is one complete SSE record; a frame that does not end its record with a
// blank line gets one, so records never run together.
//
// The stream's Next runs on a goroutine of its own, so neither Close nor the
// end of the request's context waits for a stalled stream: a Read blocked on
// one returns at once, and Close, which never takes the Read lock, closes the
// stream, which unblocks its Next.
type frameBody struct {
	ctx    context.Context
	stream core.StreamIter

	// mu serializes Reads and guards the fields below.
	mu       sync.Mutex
	pending  []byte
	buffer   []byte
	done     bool
	err      error            // the error every later Read returns
	inflight chan frameResult // the Next a Read started, until it returns

	closeOnce sync.Once
	closed    chan struct{}
}

type frameResult struct {
	frame []byte
	err   error
}

func newFrameBody(ctx context.Context, stream core.StreamIter) *frameBody {
	return &frameBody{ctx: ctx, stream: stream, closed: make(chan struct{})}
}

func (b *frameBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for len(b.buffer) == 0 {
		if b.isClosed() {
			return 0, io.ErrClosedPipe
		}
		if b.err != nil {
			return 0, b.err
		}
		if b.pending != nil {
			b.buffer = terminateRecord(b.pending)
			b.pending = nil
			continue
		}
		if b.done {
			return 0, io.EOF
		}
		frame, err := b.next(nil, 0)
		if errors.Is(err, io.EOF) {
			b.done = true
			continue
		}
		if errors.Is(err, io.ErrClosedPipe) && b.isClosed() {
			return 0, err
		}
		if err != nil {
			b.err = failure(b.ctx, err)
			return 0, b.err
		}
		b.buffer = terminateRecord(frame)
	}
	n := copy(p, b.buffer)
	b.buffer = b.buffer[n:]
	return n, nil
}

// next returns the stream's next frame, or an error once the body is closed,
// the request's context ends, or timeout fires, whichever comes first. A
// Next still running then is picked up by the following call. mu is held.
func (b *frameBody) next(timeout <-chan time.Time, after time.Duration) ([]byte, error) {
	if b.inflight == nil {
		result := make(chan frameResult, 1)
		b.inflight = result
		go func() {
			frame, err := b.stream.Next()
			result <- frameResult{frame: frame, err: err}
		}()
	}
	select {
	case result := <-b.inflight:
		b.inflight = nil
		return result.frame, result.err
	case <-b.closed:
		return nil, io.ErrClosedPipe
	case <-b.ctx.Done():
		return nil, b.ctx.Err()
	case <-timeout:
		return nil, stallFailure(after)
	}
}

func (b *frameBody) isClosed() bool {
	select {
	case <-b.closed:
		return true
	default:
		return false
	}
}

// Close closes the stream. Only the first call does, and only it waits for
// the stream to close: a later one returns at once.
func (b *frameBody) Close() error {
	first := false
	b.closeOnce.Do(func() {
		close(b.closed)
		first = true
	})
	if !first {
		return nil
	}
	return b.stream.Close()
}

// stallError is the cause of a stream that sent nothing in time. It is a
// timeout, as a net.Error reports one.
type stallError struct{ after time.Duration }

func (e *stallError) Error() string {
	return fmt.Sprintf("the provider sent nothing within %s", e.after)
}

func (e *stallError) Timeout() bool   { return true }
func (e *stallError) Temporary() bool { return true }

// stallFailure is the failure of a stream that sent nothing within after:
// a transport failure another try or target may get past.
func stallFailure(after time.Duration) error {
	cause := &stallError{after: after}
	return &core.ProviderError{
		Message: cause.Error(),
		Class:   core.ProviderErrorTransport,
		Classification: core.ProviderErrorClassification{
			Retryable: true, FailoverEligible: true, CircuitFailure: true,
		},
		Cause: cause,
	}
}

func isStall(err error) bool {
	var stall *stallError
	return errors.As(err, &stall)
}

func terminateRecord(frame []byte) []byte {
	switch {
	case len(frame) == 0:
		return nil
	case bytes.HasSuffix(frame, []byte("\n\n")), bytes.HasSuffix(frame, []byte("\r\n\r\n")):
		return frame
	case bytes.HasSuffix(frame, []byte("\n")):
		return append(frame, '\n')
	}
	return append(frame, '\n', '\n')
}

// failure is the error RoundTrip, or a stream body's Read, reports for the
// provider's err: err itself, keeping its classification, or the context's
// error once the request's context has ended, so a caller that gave up
// never sees its cancellation reported as a provider failure.
func failure(ctx context.Context, err error) error {
	if ctx != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
	}
	return err
}

// credentialFailure reports a credential that could not be obtained. An
// error that classifies itself keeps its classification; any other is a
// configuration error of this instance, which permits failover to another
// instance but says nothing about the instance's health.
func credentialFailure(ctx context.Context, err error) error {
	if err = failure(ctx, err); errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var classified core.ProviderErrorClassifier
	if errors.As(err, &classified) {
		return err
	}
	return core.NewConfigurationError("credential unavailable: "+err.Error(), err)
}

func newResponse(req *http.Request, status int, contentType string, body io.ReadCloser) *http.Response {
	header := http.Header{}
	header.Set("Content-Type", contentType)
	return &http.Response{
		Status:        strconv.Itoa(status) + " " + http.StatusText(status),
		StatusCode:    status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        header,
		Body:          body,
		ContentLength: -1,
		Request:       req,
	}
}

// Client returns an HTTP client that sends every request through t. It has
// no timeout of its own: the provider's HTTP client bounds each request.
func Client(t *Transport) *http.Client {
	return &http.Client{
		Transport:     t,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// StaticCredential returns a source for a credential that never changes.
// A nil credential yields none.
func StaticCredential(credential *core.Credential) CredentialSource {
	return func(context.Context) (*core.Credential, error) { return credential, nil }
}

// BaseURL is the placeholder base the chat client addresses: the transport
// keeps only the request path's surface.
const BaseURL = "http://core.invalid/v1"
