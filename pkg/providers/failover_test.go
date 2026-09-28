package providers

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	core "github.com/xibodev/llmgw-core"
	"github.com/xibodev/llmgw-core/execution"
)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock() *testClock { return &testClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)} }

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newTestFailover(clock *testClock, limits *RateLimiterRegistry) *Failover {
	return NewFailoverWith(NewHealthTracker(clock.Now), limits, clock.Now)
}

func instanceCandidate(instance string) FallbackCandidate {
	return FallbackCandidate{
		Provider: "openai", Model: "m", DisplayName: instance + "/m",
		IdentityKey: "provider_instance:" + instance, ConfigKey: "instance_target:" + instance + "/m",
	}
}

func statusError(status int, retryAfter time.Duration) error {
	transient := status == http.StatusTooManyRequests || status >= 500
	return &core.ProviderError{
		Message: http.StatusText(status),
		Class:   core.ClassifyProviderFailure(core.ProviderFailure{StatusCode: status}).ErrorClass,
		Classification: core.ProviderErrorClassification{
			StatusCode: status, Retryable: transient, FailoverEligible: transient, CircuitFailure: status >= 500,
			RetryAfter: retryAfter,
		},
	}
}

func transportError() error {
	return &core.ProviderError{
		Message: "connection reset", Class: core.ProviderErrorTransport,
		Classification: core.ProviderErrorClassification{Retryable: true, FailoverEligible: true, CircuitFailure: true},
	}
}

// scripted runs each candidate with the next error scripted for its display
// name; a candidate with no script left serves.
type scripted struct {
	mu      sync.Mutex
	errs    map[string][]error
	calls   []string
	respond func(FallbackCandidate) *LLMResponse
}

func (s *scripted) run(_ context.Context, candidate FallbackCandidate) (*LLMResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, candidate.DisplayName)
	if queue := s.errs[candidate.DisplayName]; len(queue) > 0 {
		s.errs[candidate.DisplayName] = queue[1:]
		return nil, queue[0]
	}
	return &LLMResponse{Content: candidate.DisplayName}, nil
}

func TestFailoverMovesOnAfterRetryableAndFailoverErrors(t *testing.T) {
	for name, err := range map[string]error{
		"retryable 503":        statusError(http.StatusServiceUnavailable, 0),
		"retryable transport":  transportError(),
		"failover config":      core.NewConfigurationError("no key", nil),
		"failover unavailable": &execution.UnavailableError{},
	} {
		t.Run(name, func(t *testing.T) {
			clock := newTestClock()
			run := &scripted{errs: map[string][]error{"a/m": {err}}}
			result, gotErr := newTestFailover(clock, nil).Execute(context.Background(),
				[]FallbackCandidate{instanceCandidate("a"), instanceCandidate("b")}, run.run)
			if gotErr != nil || result.Candidate.DisplayName != "b/m" || result.Value.Content != "b/m" {
				t.Fatalf("result = %+v err = %v", result, gotErr)
			}
			if len(result.Attempts) != 2 || result.Attempts[0].Err == nil {
				t.Fatalf("attempts = %+v", result.Attempts)
			}
		})
	}
}

func TestFailoverStopsAtTerminalErrors(t *testing.T) {
	for name, err := range map[string]error{
		"invalid request": statusError(http.StatusBadRequest, 0),
		"auth":            statusError(http.StatusUnauthorized, 0),
		"unclassified":    errors.New("boom"),
		"after output":    &execution.AfterOutputError{Err: transportError()},
	} {
		t.Run(name, func(t *testing.T) {
			clock := newTestClock()
			failover := newTestFailover(clock, nil)
			run := &scripted{errs: map[string][]error{"a/m": {err}}}
			_, gotErr := failover.Execute(context.Background(),
				[]FallbackCandidate{instanceCandidate("a"), instanceCandidate("b")}, run.run)
			if gotErr != err || len(run.calls) != 1 {
				t.Fatalf("err = %v calls = %v, want the terminal error from the first candidate only", gotErr, run.calls)
			}
			if available, _ := failover.Health().Available("provider_instance:a"); !available {
				t.Fatal("a terminal failure made the instance unavailable")
			}
		})
	}
}

func TestFailoverCanceledContextStopsUnrecorded(t *testing.T) {
	clock := newTestClock()
	failover := newTestFailover(clock, nil)
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	_, err := failover.Execute(ctx, []FallbackCandidate{instanceCandidate("a"), instanceCandidate("b")},
		func(context.Context, FallbackCandidate) (*LLMResponse, error) {
			calls++
			cancel()
			return nil, transportError()
		})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("err = %v calls = %d", err, calls)
	}
	if state := failover.Health().State("provider_instance:a"); state.Streak != 0 {
		t.Fatalf("a canceled request was recorded: %+v", state)
	}
}

func TestFailoverHonoursRetryAfterAsCooldown(t *testing.T) {
	clock := newTestClock()
	failover := newTestFailover(clock, nil)
	candidates := []FallbackCandidate{instanceCandidate("a"), instanceCandidate("b")}
	run := &scripted{errs: map[string][]error{"a/m": {statusError(http.StatusTooManyRequests, 20*time.Second)}}}
	if _, err := failover.Execute(context.Background(), candidates, run.run); err != nil {
		t.Fatal(err)
	}

	clock.Advance(5 * time.Second)
	run.calls = nil
	result, err := failover.Execute(context.Background(), candidates, run.run)
	if err != nil || result.Candidate.DisplayName != "b/m" || !result.Attempts[0].Unavailable ||
		result.Attempts[0].Until.Sub(clock.Now()) != 15*time.Second {
		t.Fatalf("result = %+v err = %v", result, err)
	}
	if len(run.calls) != 1 || run.calls[0] != "b/m" {
		t.Fatalf("calls = %v, want the cooling instance skipped", run.calls)
	}

	clock.Advance(15 * time.Second)
	run.calls = nil
	if result, err = failover.Execute(context.Background(), candidates, run.run); err != nil || result.Candidate.DisplayName != "a/m" {
		t.Fatalf("result = %+v err = %v, want the instance back after its Retry-After", result, err)
	}
}

func TestFailoverCooldownCoversEveryTargetOfTheInstance(t *testing.T) {
	clock := newTestClock()
	failover := newTestFailover(clock, nil)
	first := instanceCandidate("a")
	second := first
	second.Model, second.DisplayName, second.ConfigKey = "other", "a/other", "instance_target:a/other"
	run := &scripted{errs: map[string][]error{"a/m": {statusError(http.StatusTooManyRequests, time.Minute)}}}
	_, _ = failover.Execute(context.Background(), []FallbackCandidate{first, instanceCandidate("b")}, run.run)
	result, err := failover.Execute(context.Background(), []FallbackCandidate{second, instanceCandidate("b")}, run.run)
	if err != nil || !result.Attempts[0].Unavailable || result.Candidate.DisplayName != "b/m" {
		t.Fatalf("result = %+v err = %v, want the instance's other target skipped", result, err)
	}
}

func TestFailoverNeverHoldsBackASingleCandidate(t *testing.T) {
	clock := newTestClock()
	failover := newTestFailover(clock, nil)
	only := []FallbackCandidate{instanceCandidate("a")}
	run := &scripted{errs: map[string][]error{"a/m": {statusError(http.StatusTooManyRequests, time.Minute)}}}
	if _, err := failover.Execute(context.Background(), only, run.run); err == nil {
		t.Fatal("the scripted rate limit did not fail the call")
	}
	if available, _ := failover.Health().Available("provider_instance:a"); available {
		t.Fatal("the single candidate's Retry-After was not recorded")
	}
	if result, err := failover.Execute(context.Background(), only, run.run); err != nil || result.Candidate.DisplayName != "a/m" {
		t.Fatalf("result = %+v err = %v, want the only candidate tried despite its cooldown", result, err)
	}
}

func TestFailoverCircuitOpensAndSkipsTheUnhealthyInstance(t *testing.T) {
	clock := newTestClock()
	failover := newTestFailover(clock, nil)
	candidates := []FallbackCandidate{instanceCandidate("a"), instanceCandidate("b")}
	run := &scripted{errs: map[string][]error{"a/m": {transportError()}}}
	for range healthFailureThreshold {
		if _, err := failover.Execute(context.Background(), candidates, run.run); err != nil {
			t.Fatal(err)
		}
	}
	state := failover.Health().State("provider_instance:a")
	if state.Streak != healthFailureThreshold || state.OpenUntil.Sub(clock.Now()) != healthOpenDuration {
		t.Fatalf("state = %+v, want an open circuit for %v", state, healthOpenDuration)
	}

	run.calls = nil
	result, err := failover.Execute(context.Background(), candidates, run.run)
	if err != nil || !result.Attempts[0].Unavailable || len(run.calls) != 1 || run.calls[0] != "b/m" {
		t.Fatalf("result = %+v calls = %v err = %v", result, run.calls, err)
	}

	// Half-open once the time has passed: a success closes the circuit.
	clock.Advance(healthOpenDuration)
	if result, err = failover.Execute(context.Background(), candidates, run.run); err != nil || result.Candidate.DisplayName != "a/m" {
		t.Fatalf("result = %+v err = %v", result, err)
	}
	if state := failover.Health().State("provider_instance:a"); state.Streak != 0 {
		t.Fatalf("state after success = %+v", state)
	}
}

func TestHealthPolicyBackoffEscalatesToItsCap(t *testing.T) {
	backoff := HealthPolicy().Backoff
	for streak, want := range map[int]time.Duration{1: time.Minute, 2: 5 * time.Minute, 3: 25 * time.Minute, 4: time.Hour, 40: time.Hour} {
		if got := backoff(execution.Failure{Streak: streak}); got != want {
			t.Fatalf("streak %d: open for %v, want %v", streak, got, want)
		}
	}
}

func TestFailoverEveryInstanceUnavailable(t *testing.T) {
	clock := newTestClock()
	failover := newTestFailover(clock, nil)
	failover.Health().Record("provider_instance:a", statusError(http.StatusTooManyRequests, 30*time.Second))
	failover.Health().Record("provider_instance:b", statusError(http.StatusTooManyRequests, 10*time.Second))
	run := &scripted{}
	_, err := failover.Execute(context.Background(), []FallbackCandidate{instanceCandidate("a"), instanceCandidate("b")}, run.run)
	failure := DescribeFailure(err)
	if !failure.Unavailable || failure.RetryAfter != 10*time.Second || len(run.calls) != 0 {
		t.Fatalf("failure = %+v calls = %v", failure, run.calls)
	}
}

func TestFailoverRateLimitMovesOnThenLastWaits(t *testing.T) {
	clock := newTestClock()
	limits := NewRateLimiterRegistry()
	limits.Register("provider_instance:a", 1)
	failover := newTestFailover(clock, limits)
	candidates := []FallbackCandidate{instanceCandidate("a"), instanceCandidate("b")}
	run := &scripted{}
	if result, err := failover.Execute(context.Background(), candidates, run.run); err != nil || result.Candidate.DisplayName != "a/m" {
		t.Fatalf("first request: result = %+v err = %v", result, err)
	}
	result, err := failover.Execute(context.Background(), candidates, run.run)
	if err != nil || result.Candidate.DisplayName != "b/m" {
		t.Fatalf("saturated request: result = %+v err = %v, want b", result, err)
	}
	if state := failover.Health().State("provider_instance:a"); state.Streak != 0 || !state.CooldownUntil.IsZero() {
		t.Fatalf("a local rate limit was recorded as health: %+v", state)
	}

	// The last candidate waits for its token instead of failing.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = failover.Execute(ctx, candidates[:1], run.run)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the last candidate to wait for its token", err)
	}
}

func TestNilFailoverTriesEachCandidateOnce(t *testing.T) {
	var failover *Failover
	run := &scripted{errs: map[string][]error{"a/m": {transportError()}}}
	result, err := failover.Execute(context.Background(), []FallbackCandidate{instanceCandidate("a"), instanceCandidate("b")}, run.run)
	if err != nil || result.Candidate.DisplayName != "b/m" {
		t.Fatalf("result = %+v err = %v", result, err)
	}
}

func TestDescribeFailure(t *testing.T) {
	cases := map[string]struct {
		err  error
		want Failure
	}{
		"rate limit": {statusError(http.StatusTooManyRequests, 7*time.Second), Failure{
			Class: core.ProviderErrorRateLimited, StatusCode: 429, RetryAfter: 7 * time.Second, Disposition: core.DispositionRetryable,
		}},
		"auth": {statusError(http.StatusUnauthorized, 0), Failure{
			Class: core.ProviderErrorAuth, StatusCode: 401, Disposition: core.DispositionTerminal,
		}},
		"server": {statusError(http.StatusBadGateway, 0), Failure{
			Class: core.ProviderErrorUpstream, StatusCode: 502, Disposition: core.DispositionRetryable,
		}},
		"transport timeout": {&core.ProviderError{Class: core.ProviderErrorTransport, Cause: context.DeadlineExceeded,
			Classification: core.ProviderErrorClassification{Retryable: true, FailoverEligible: true, CircuitFailure: true}},
			Failure{Class: core.ProviderErrorTransport, Disposition: core.DispositionRetryable, Timeout: true}},
		"after output": {&execution.AfterOutputError{Err: statusError(http.StatusServiceUnavailable, 0)}, Failure{
			Class: core.ProviderErrorUpstream, StatusCode: 503, Disposition: core.DispositionTerminal, AfterOutput: true,
		}},
		"unavailable": {&execution.UnavailableError{RetryAfter: time.Minute}, Failure{
			Unavailable: true, RetryAfter: time.Minute, Disposition: core.DispositionFailover,
		}},
		"canceled":     {context.Canceled, Failure{Disposition: core.DispositionTerminal}},
		"unclassified": {errors.New("boom"), Failure{Disposition: core.DispositionTerminal}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := DescribeFailure(tc.err); got != tc.want {
				t.Fatalf("DescribeFailure() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestRetryDelay(t *testing.T) {
	cases := map[string]struct {
		err   error
		delay time.Duration
		retry bool
	}{
		"retryable uses backoff":            {statusError(http.StatusServiceUnavailable, 0), 2 * time.Second, true},
		"retry-after longer than backoff":   {statusError(http.StatusTooManyRequests, 9*time.Second), 9 * time.Second, true},
		"retry-after beyond the wait limit": {statusError(http.StatusTooManyRequests, 5*time.Minute), 0, false},
		"failover only":                     {core.NewConfigurationError("x", nil), 0, false},
		"terminal":                          {statusError(http.StatusBadRequest, 0), 0, false},
		"after output":                      {&execution.AfterOutputError{Err: transportError()}, 0, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			delay, retry := RetryDelay(tc.err, 2*time.Second, time.Minute)
			if delay != tc.delay || retry != tc.retry {
				t.Fatalf("RetryDelay() = %v, %v; want %v, %v", delay, retry, tc.delay, tc.retry)
			}
		})
	}
}
