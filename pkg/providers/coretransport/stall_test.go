package coretransport

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/xibodev/llmgw-core"
)

// stallingFrames yields its frames and then blocks in Next until it is
// closed, as a stream reading an HTTP body that stopped sending does. With
// ignoreClose, Close does not unblock it either; only the test's end does.
type stallingFrames struct {
	frames      [][]byte
	ignoreClose bool
	release     chan struct{}
	end         chan struct{}
	closeOnce   sync.Once
	closed      atomic.Bool
}

func newStallingFrames(t *testing.T, ignoreClose bool, frames ...string) *stallingFrames {
	s := &stallingFrames{ignoreClose: ignoreClose, release: make(chan struct{}), end: make(chan struct{})}
	for _, frame := range frames {
		s.frames = append(s.frames, []byte(frame))
	}
	t.Cleanup(func() { close(s.end) })
	return s
}

func (s *stallingFrames) Next() ([]byte, error) {
	if len(s.frames) > 0 {
		frame := s.frames[0]
		s.frames = s.frames[1:]
		return frame, nil
	}
	if s.ignoreClose {
		<-s.end
	} else {
		select {
		case <-s.release:
		case <-s.end:
		}
	}
	return nil, errors.New("read on closed body")
}

func (s *stallingFrames) Close() error {
	s.closed.Store(true)
	s.closeOnce.Do(func() { close(s.release) })
	return nil
}

func streamRequest(ctx context.Context) *http.Request {
	return httptest.NewRequest(http.MethodPost, "http://core.invalid/v1/chat/completions",
		strings.NewReader(`{"model":"m","stream":true}`)).WithContext(ctx)
}

// within fails the test unless fn returns within limit.
func within(t *testing.T, limit time.Duration, fn func()) time.Duration {
	t.Helper()
	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
		return time.Since(start)
	case <-time.After(limit):
		t.Fatalf("still waiting after %s", limit)
		return 0
	}
}

func TestStreamThatSendsNothingFailsAfterTheFirstFrameTimeout(t *testing.T) {
	for _, ignoreClose := range []bool{false, true} {
		iter := newStallingFrames(t, ignoreClose)
		provider := &fakeProvider{stream: func(core.Request) (core.StreamIter, error) { return iter, nil }}
		transport := &Transport{Provider: provider, FirstFrameTimeout: 100 * time.Millisecond}

		var err error
		elapsed := within(t, 5*time.Second, func() { _, err = transport.RoundTrip(streamRequest(context.Background())) })

		var netErr net.Error
		classification := core.ClassifyError(err)
		if err == nil || !errors.As(err, &netErr) || !netErr.Timeout() || !classification.FailoverEligible || !classification.CircuitFailure {
			t.Fatalf("ignoreClose=%v: error = %v (%+v), want a stall classified as a transport timeout", ignoreClose, err, classification)
		}
		if elapsed < 100*time.Millisecond {
			t.Fatalf("ignoreClose=%v: failed after %s, before the timeout", ignoreClose, elapsed)
		}
		if !ignoreClose {
			deadline := time.Now().Add(2 * time.Second)
			for !iter.closed.Load() && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if !iter.closed.Load() {
				t.Fatal("the stalled stream was not closed")
			}
		}
	}
}

func TestCanceledRequestStopsTheWaitForTheFirstFrame(t *testing.T) {
	iter := newStallingFrames(t, true)
	provider := &fakeProvider{stream: func(core.Request) (core.StreamIter, error) { return iter, nil }}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)

	var err error
	within(t, 5*time.Second, func() { _, err = (&Transport{Provider: provider}).RoundTrip(streamRequest(ctx)) })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestCloseInterruptsAReadOnAStalledStream(t *testing.T) {
	for _, ignoreClose := range []bool{false, true} {
		iter := newStallingFrames(t, ignoreClose, "data: {\"a\":1}\n\n")
		provider := &fakeProvider{stream: func(core.Request) (core.StreamIter, error) { return iter, nil }}
		resp, err := (&Transport{Provider: provider}).RoundTrip(streamRequest(context.Background()))
		if err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 64)
		if n, err := resp.Body.Read(buf); err != nil || string(buf[:n]) != "data: {\"a\":1}\n\n" {
			t.Fatalf("first read = %q, %v", buf[:n], err)
		}

		readErr := make(chan error, 1)
		go func() {
			_, err := resp.Body.Read(buf)
			readErr <- err
		}()
		time.Sleep(50 * time.Millisecond)
		within(t, 2*time.Second, func() { _ = resp.Body.Close() })
		select {
		case err := <-readErr:
			if !errors.Is(err, io.ErrClosedPipe) {
				t.Fatalf("ignoreClose=%v: read error = %v, want io.ErrClosedPipe", ignoreClose, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("ignoreClose=%v: the read blocked on the stalled stream survived Close", ignoreClose)
		}
		within(t, time.Second, func() { _ = resp.Body.Close() }) // a second Close returns at once
	}
}

func TestCanceledRequestInterruptsARead(t *testing.T) {
	iter := newStallingFrames(t, true, "data: {\"a\":1}\n\n")
	provider := &fakeProvider{stream: func(core.Request) (core.StreamIter, error) { return iter, nil }}
	ctx, cancel := context.WithCancel(context.Background())
	resp, err := (&Transport{Provider: provider}).RoundTrip(streamRequest(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	time.AfterFunc(50*time.Millisecond, cancel)
	var body []byte
	within(t, 5*time.Second, func() { body, err = io.ReadAll(resp.Body) })
	if string(body) != "data: {\"a\":1}\n\n" || !errors.Is(err, context.Canceled) {
		t.Fatalf("body = %q err = %v, want the frame then context.Canceled", body, err)
	}
}

// slowFrames sends a frame every interval.
type slowFrames struct {
	frames   [][]byte
	interval time.Duration
}

func (s *slowFrames) Next() ([]byte, error) {
	if len(s.frames) == 0 {
		return nil, io.EOF
	}
	time.Sleep(s.interval)
	frame := s.frames[0]
	s.frames = s.frames[1:]
	return frame, nil
}

func (s *slowFrames) Close() error { return nil }

func TestSlowStreamWithinTheFirstFrameTimeoutIsDelivered(t *testing.T) {
	iter := &slowFrames{interval: 60 * time.Millisecond, frames: [][]byte{
		[]byte("data: {\"a\":1}"), []byte("data: {\"b\":2}"), []byte("data: [DONE]"),
	}}
	provider := &fakeProvider{stream: func(core.Request) (core.StreamIter, error) { return iter, nil }}
	resp, err := (&Transport{Provider: provider, FirstFrameTimeout: 100 * time.Millisecond}).RoundTrip(streamRequest(context.Background()))
	if err != nil {
		t.Fatalf("RoundTrip() error = %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || string(body) != "data: {\"a\":1}\n\ndata: {\"b\":2}\n\ndata: [DONE]\n\n" {
		t.Fatalf("body = %q err = %v", body, err)
	}
}

func unauthorized() error {
	return &core.ProviderError{Message: "token expired", Class: core.ProviderErrorAuth,
		Classification: core.ProviderErrorClassification{StatusCode: http.StatusUnauthorized}}
}

func TestRejectedTokenIsRefreshedAndTheRequestReplayedOnce(t *testing.T) {
	var tokens []string
	provider := &fakeProvider{invoke: func(request core.Request) (core.Response, error) {
		tokens = append(tokens, request.Credential.Token)
		if request.Credential.Token == "old" {
			return core.Response{}, unauthorized()
		}
		return core.Response{Body: []byte(`{"ok":true}`)}, nil
	}}
	refreshes := 0
	transport := &Transport{
		Provider:   provider,
		Credential: StaticCredential(&core.Credential{Token: "old"}),
		Refresh: func(_ context.Context, rejected *core.Credential) (*core.Credential, error) {
			refreshes++
			if rejected.Token != "old" {
				t.Errorf("refresh got %q, want the rejected token", rejected.Token)
			}
			return &core.Credential{Token: "new"}, nil
		},
	}
	resp := post(t, transport, "/v1/chat/completions", `{"model":"m"}`)
	body, _ := io.ReadAll(resp.Body)
	if string(body) != `{"ok":true}` || refreshes != 1 || strings.Join(tokens, ",") != "old,new" {
		t.Fatalf("body = %s, refreshes = %d, tokens = %v", body, refreshes, tokens)
	}
}

func TestRejectedStreamTokenIsRefreshedBeforeAnythingIsSent(t *testing.T) {
	provider := &fakeProvider{stream: func(request core.Request) (core.StreamIter, error) {
		if request.Credential.Token == "old" {
			return nil, unauthorized()
		}
		return &frames{items: [][]byte{[]byte("data: [DONE]\n\n")}}, nil
	}}
	transport := &Transport{
		Provider:   provider,
		Credential: StaticCredential(&core.Credential{Token: "old"}),
		Refresh: func(context.Context, *core.Credential) (*core.Credential, error) {
			return &core.Credential{Token: "new"}, nil
		},
	}
	resp := post(t, transport, "/v1/chat/completions", `{"model":"m","stream":true}`)
	defer resp.Body.Close()
	if body, _ := io.ReadAll(resp.Body); string(body) != "data: [DONE]\n\n" || len(provider.requests) != 2 {
		t.Fatalf("body = %q after %d requests", body, len(provider.requests))
	}
}

func TestRejectionIsNotRefreshedForAnAPIKeyOrTwice(t *testing.T) {
	for name, credential := range map[string]*core.Credential{
		"api key": {APIKey: "k", TokenType: core.TokenTypeAPIKey},
		"token":   {Token: "old"},
	} {
		t.Run(name, func(t *testing.T) {
			calls, refreshes := 0, 0
			provider := &fakeProvider{invoke: func(core.Request) (core.Response, error) {
				calls++
				return core.Response{}, unauthorized()
			}}
			transport := &Transport{
				Provider:   provider,
				Credential: StaticCredential(credential),
				Refresh: func(context.Context, *core.Credential) (*core.Credential, error) {
					refreshes++
					return &core.Credential{Token: "still rejected"}, nil
				},
			}
			err := roundTripError(t, transport, context.Background(), `{"model":"m"}`)
			wantCalls, wantRefreshes := 2, 1
			if credential.TokenType == core.TokenTypeAPIKey {
				wantCalls, wantRefreshes = 1, 0
			}
			if core.ClassifyError(err).StatusCode != http.StatusUnauthorized || calls != wantCalls || refreshes != wantRefreshes {
				t.Fatalf("err = %v, calls = %d, refreshes = %d; want %d and %d", err, calls, refreshes, wantCalls, wantRefreshes)
			}
		})
	}
}

func TestClientClosesTheProvidersIdleConnections(t *testing.T) {
	closed := 0
	Client(&Transport{Provider: &fakeProvider{}, CloseIdle: func() { closed++ }}).CloseIdleConnections()
	if closed != 1 {
		t.Fatalf("CloseIdle ran %d times, want once", closed)
	}
}
