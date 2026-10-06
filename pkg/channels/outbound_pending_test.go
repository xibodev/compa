package channels

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/xibodev/compa/v2/pkg/bus"
)

// gatedChannel records what it sends; while gate is open (not closed),
// Send blocks.
type gatedChannel struct {
	*BaseChannel
	gate chan struct{}
	sent chan string
}

func newGatedChannel(name string, gated bool) *gatedChannel {
	ch := &gatedChannel{
		BaseChannel: NewBaseChannel(name, nil, nil, nil),
		gate:        make(chan struct{}),
		sent:        make(chan string, 4*maxPendingOutbound),
	}
	if !gated {
		close(ch.gate)
	}
	return ch
}

func (c *gatedChannel) Start(context.Context) error { return nil }
func (c *gatedChannel) Stop(context.Context) error  { return nil }

func (c *gatedChannel) Send(ctx context.Context, msg bus.OutboundMessage) ([]string, error) {
	select {
	case <-c.gate:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	c.sent <- msg.Content
	return nil, nil
}

func TestDispatcherDoesNotWaitForAFullChannel(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()
	m := &Manager{
		channels: make(map[string]Channel),
		workers:  make(map[string]*channelWorker),
		bus:      mb,
	}
	slow := newGatedChannel("slow", true)
	fast := newGatedChannel("fast", false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.mu.Lock()
	for name, ch := range map[string]Channel{"slow": slow, "fast": fast} {
		m.channels[name] = ch
		m.startWorkerLocked(ctx, name, ch)
		m.workers[name].limiter = rate.NewLimiter(rate.Inf, 1)
	}
	m.mu.Unlock()
	go m.dispatchOutbound(ctx)

	publish := func(channel, content string) {
		t.Helper()
		pubCtx, pubCancel := context.WithTimeout(ctx, time.Second)
		defer pubCancel()
		err := mb.PublishOutbound(pubCtx, testOutboundMessage(bus.OutboundMessage{
			Channel: channel, ChatID: "chat", Content: content,
		}))
		if err != nil {
			t.Fatalf("PublishOutbound(%s): %v", channel, err)
		}
	}

	// Far more than the slow channel's worker queue holds.
	const backlog = 4 * defaultChannelQueueSize
	for i := range backlog {
		publish("slow", fmt.Sprintf("slow-%d", i))
	}
	publish("fast", "fast-0")

	select {
	case got := <-fast.sent:
		if got != "fast-0" {
			t.Fatalf("fast channel sent %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a channel that cannot send held up another channel")
	}

	// The slow channel delivers everything, in order, once it can send.
	close(slow.gate)
	for i := range backlog {
		select {
		case got := <-slow.sent:
			if want := fmt.Sprintf("slow-%d", i); got != want {
				t.Fatalf("slow channel sent %q, want %q", got, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("slow channel delivered only %d of %d messages", i, backlog)
		}
	}
}

func TestPendingQueueDropsBeyondItsLimit(t *testing.T) {
	var q pendingQueue[int]
	stop := make(chan struct{})
	for i := range maxPendingOutbound {
		if got := q.push(i, stop); got != pushQueued {
			t.Fatalf("push(%d) = %v, want queued", i, got)
		}
	}
	if got := q.push(-1, stop); got != pushFull {
		t.Fatalf("push beyond the limit = %v, want full", got)
	}
	if q.len() != maxPendingOutbound {
		t.Fatalf("len = %d, want %d", q.len(), maxPendingOutbound)
	}
	close(stop)
	if got := q.push(-1, stop); got != pushStopped {
		t.Fatalf("push after stop = %v, want stopped", got)
	}
	for i := range maxPendingOutbound {
		if got, ok := q.pop(); !ok || got != i {
			t.Fatalf("pop() = %d, %v; want %d", got, ok, i)
		}
	}
}

func TestRetryDelayRespectsRetryAfter(t *testing.T) {
	limited := NewRateLimitError(7*time.Second, errors.New("429"))
	if !errors.Is(limited, ErrRateLimit) {
		t.Fatal("a RateLimitError must match ErrRateLimit")
	}
	if errors.Is(limited, ErrTemporary) {
		t.Fatal("a RateLimitError must not match ErrTemporary")
	}
	tests := []struct {
		name string
		err  error
		want time.Duration
	}{
		{"retry after", fmt.Errorf("send: %w", limited), 7 * time.Second},
		{"retry after is capped", NewRateLimitError(time.Hour, nil), maxRetryAfter},
		{"plain rate limit", fmt.Errorf("%w: 429", ErrRateLimit), rateLimitDelay},
		{"temporary", ErrTemporary, baseBackoff},
	}
	for _, tt := range tests {
		if got := retryDelay(tt.err, 0); got != tt.want {
			t.Errorf("%s: retryDelay() = %v, want %v", tt.name, got, tt.want)
		}
	}
	if got := retryDelay(ErrTemporary, 10); got != maxBackoff {
		t.Errorf("retryDelay(temporary, 10) = %v, want %v", got, maxBackoff)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tests := []struct {
		value string
		want  time.Duration
		ok    bool
	}{
		{"3", 3 * time.Second, true},
		{"0.5", 500 * time.Millisecond, true},
		{"100000", maxRetryAfter, true},
		{now.Add(30 * time.Second).Format(http.TimeFormat), 30 * time.Second, true},
		{now.Add(-time.Minute).Format(http.TimeFormat), 0, true},
		{"", 0, false},
		{"soon", 0, false},
	}
	for _, tt := range tests {
		got, ok := ParseRetryAfter(tt.value, now)
		if got != tt.want || ok != tt.ok {
			t.Errorf("ParseRetryAfter(%q) = %v, %v; want %v, %v", tt.value, got, ok, tt.want, tt.ok)
		}
	}
	err := ClassifySendErrorRetryAfter(http.StatusTooManyRequests, "2", errors.New("too many"))
	var limited *RateLimitError
	if !errors.As(err, &limited) || limited.RetryAfter != 2*time.Second {
		t.Fatalf("ClassifySendErrorRetryAfter(429) = %v, want a 2s RateLimitError", err)
	}
	if err := ClassifySendErrorRetryAfter(http.StatusBadGateway, "2", errors.New("x")); !errors.Is(err, ErrTemporary) {
		t.Fatalf("ClassifySendErrorRetryAfter(502) = %v, want temporary", err)
	}
}

// retryChannel fails its first Send with err.
type retryChannel struct {
	*BaseChannel
	err   error
	calls []time.Time
}

func (c *retryChannel) Start(context.Context) error { return nil }
func (c *retryChannel) Stop(context.Context) error  { return nil }

func (c *retryChannel) Send(context.Context, bus.OutboundMessage) ([]string, error) {
	c.calls = append(c.calls, time.Now())
	if len(c.calls) == 1 {
		return nil, c.err
	}
	return []string{"ok"}, nil
}

func TestSendWithRetryWaitsForRetryAfter(t *testing.T) {
	ch := &retryChannel{
		BaseChannel: NewBaseChannel("test", nil, nil, nil),
		err:         NewRateLimitError(10*time.Millisecond, errors.New("429")),
	}
	m := &Manager{channels: map[string]Channel{"test": ch}, workers: map[string]*channelWorker{}}
	w := newChannelWorker("test", ch, "test")
	w.limiter = rate.NewLimiter(rate.Inf, 1)

	ids, err := m.sendWithRetry(context.Background(), "test", w,
		testOutboundMessage(bus.OutboundMessage{Channel: "test", ChatID: "1", Content: "hi"}))
	if err != nil || len(ids) != 1 {
		t.Fatalf("sendWithRetry() = %v, %v", ids, err)
	}
	if len(ch.calls) != 2 {
		t.Fatalf("Send called %d times, want 2", len(ch.calls))
	}
	// The platform asked for 10ms, not the fixed one-second delay.
	if wait := ch.calls[1].Sub(ch.calls[0]); wait < 10*time.Millisecond || wait >= rateLimitDelay {
		t.Fatalf("waited %v between attempts, want the 10ms the platform asked for", wait)
	}
}
