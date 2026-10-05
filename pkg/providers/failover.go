package providers

import (
	"context"
	"errors"
	"math"
	"net"
	"net/http"
	"time"

	core "github.com/xibodev/llmgw-core"
	"github.com/xibodev/llmgw-core/execution"
)

// Failover runs a request on its candidates with llmgw-core's execution
// primitives. Core's classification says whether a failure may be repeated,
// may move on to the next candidate or ends the request, and what
// Retry-After the upstream asked for; Classify adjusts it for a few statuses
// (see there).
//
//   - execution.Execute tries the candidates in order. A retryable or
//     failover failure moves on to the next candidate; a terminal one - an
//     invalid request, an unclassified error, or a failure after output
//     reached the caller - ends the request.
//   - A shared execution.HealthTracker keeps each target's health, a model on
//     an instance (FallbackCandidate.HealthKey): a streak of circuit failures
//     opens its circuit, and a Retry-After cools it down. A candidate that is
//     unavailable is skipped, and when every candidate is, the request fails
//     with an *execution.UnavailableError.
//     A request with a single candidate has nowhere else to go, so it is
//     never held back: its outcome is only recorded, and the caller's retry
//     honours the failure's Retry-After.
//   - Streaming pushes output to the caller as it arrives, so a run whose
//     stream failed after visible output returns an *execution.AfterOutputError:
//     terminal and neutral for health, so nothing fails over once output was
//     seen.
//   - Core has no client-side request rate limit, so each instance's RPM limit
//     stays Studio's: a candidate whose limit is saturated moves on unrecorded
//     while another candidate remains, and the last candidate waits for its
//     token.
//
// A nil *Failover tries each candidate once without health or rate limits.
type Failover struct {
	health *execution.HealthTracker
	limits *RateLimiterRegistry
	now    func() time.Time
}

// FailoverResult is a request's outcome: the serving candidate, its
// response, and the trace of every candidate reached.
type FailoverResult = execution.Result[FallbackCandidate, *LLMResponse]

// Health policy of every target, a model on an instance: a streak of three
// circuit failures opens its circuit, for one minute, then 5 and 25 minutes
// and at most an hour as failures follow each other; a streak whose last
// failure is a day old is forgotten. One failure, such as a single 503 of a
// busy aggregator, does not hold the target back.
const (
	healthFailureThreshold = 3
	healthOpenDuration     = time.Minute
	healthMaxOpenDuration  = time.Hour
	healthFailureWindow    = 24 * time.Hour
)

// HealthPolicy is the health policy of every instance. Retry-After is
// honoured: an instance the upstream asked to wait is not tried until then.
func HealthPolicy() execution.HealthPolicy {
	return execution.HealthPolicy{
		FailureThreshold: healthFailureThreshold,
		OpenDuration:     healthOpenDuration,
		FailureWindow:    healthFailureWindow,
		Backoff: func(failure execution.Failure) time.Duration {
			exponent := min(max(failure.Streak-healthFailureThreshold, 0), 3)
			return min(healthMaxOpenDuration, time.Duration(float64(healthOpenDuration)*math.Pow(5, float64(exponent))))
		},
	}
}

// NewHealthTracker returns an instance health tracker with HealthPolicy and
// core's default observation of errors. now is its clock; nil uses time.Now.
func NewHealthTracker(now func() time.Time) *execution.HealthTracker {
	return execution.NewHealthTracker(execution.HealthOptions{
		Policy: func(string) execution.HealthPolicy { return HealthPolicy() },
		Now:    now,
	})
}

// NewFailover returns a Failover with a fresh health tracker and limits,
// which may be nil for no rate limits.
func NewFailover(limits *RateLimiterRegistry) *Failover {
	return &Failover{health: NewHealthTracker(nil), limits: limits}
}

// NewFailoverWith returns a Failover on health and limits with the clock
// now, which must be health's clock; nil uses time.Now.
func NewFailoverWith(health *execution.HealthTracker, limits *RateLimiterRegistry, now func() time.Time) *Failover {
	return &Failover{health: health, limits: limits, now: now}
}

// WithLimits returns a Failover that shares f's instance health and limits
// requests with limits, so a config reload keeps what health has learned.
func (f *Failover) WithLimits(limits *RateLimiterRegistry) *Failover {
	if f == nil {
		return NewFailover(limits)
	}
	return &Failover{health: f.health, limits: limits, now: f.now}
}

// Health returns the instance health tracker, nil for a nil Failover.
func (f *Failover) Health() *execution.HealthTracker {
	if f == nil {
		return nil
	}
	return f.health
}

// Execute runs the request on candidates in order until one serves.
//
// The error is nil when a candidate served. Otherwise it is the context's
// error, the error of the last candidate tried, an *execution.UnavailableError
// when every candidate's instance was unavailable, or execution.ErrNoCandidates.
func (f *Failover) Execute(
	ctx context.Context,
	candidates []FallbackCandidate,
	run func(ctx context.Context, candidate FallbackCandidate) (*LLMResponse, error),
) (FailoverResult, error) {
	var executor execution.Executor[FallbackCandidate]
	if f != nil && f.health != nil {
		executor.Health = f.health
		if len(candidates) == 1 {
			executor.Health = recordOnly{f.health}
		}
		executor.Key = FallbackCandidate.HealthKey
		executor.Now = f.now
	}
	last := len(candidates) - 1
	return execution.Execute(ctx, executor, candidates, func(ctx context.Context, candidate FallbackCandidate) (*LLMResponse, error) {
		isLast := last >= 0 && candidate == candidates[last]
		if err := f.admit(ctx, candidate, isLast); err != nil {
			return nil, err
		}
		response, err := run(ctx, candidate)
		if err != nil && ctx.Err() == nil {
			err = reclassify(err)
		}
		return response, err
	})
}

// Classify returns err's classification as Compa's routing reads it: core's,
// except for these statuses:
//
//   - 401, 402, 403 and 404 move on to the next target, which may hold a
//     valid key, a balance, access or the model, without counting against
//     the instance's health: it answered.
//   - 529, Anthropic's "overloaded", is transient as a 503 is: it may be
//     retried and fail over, and counts against the target.
//
// 400 and 422, a request every target would reject, stay terminal, and a
// failure after output reached the caller stays terminal whatever its status.
func Classify(err error) core.ProviderErrorClassification {
	classification := core.ClassifyError(err)
	var afterOutput *execution.AfterOutputError
	if err == nil || errors.As(err, &afterOutput) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return classification
	}
	switch classification.StatusCode {
	case http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden, http.StatusNotFound:
		classification.Retryable, classification.FailoverEligible, classification.CircuitFailure = false, true, false
	case statusOverloaded:
		classification.Retryable, classification.FailoverEligible, classification.CircuitFailure = true, true, true
	}
	return classification
}

// statusOverloaded is the status Anthropic answers when it is overloaded.
const statusOverloaded = 529

// reclassify returns err carrying Classify's classification when it differs
// from core's, so the executor, health and callers all read the same.
func reclassify(err error) error {
	classification := Classify(err)
	if classification == core.ClassifyError(err) {
		return err
	}
	return &reclassifiedError{err: err, classification: classification}
}

// reclassifiedError is an error with Compa's classification; it reads as and
// unwraps to the error it carries.
type reclassifiedError struct {
	err            error
	classification core.ProviderErrorClassification
}

func (e *reclassifiedError) Error() string { return e.err.Error() }

func (e *reclassifiedError) Unwrap() error { return e.err }

func (e *reclassifiedError) ProviderErrorClassification() core.ProviderErrorClassification {
	return e.classification
}

// recordOnly records outcomes without ever holding a candidate back.
type recordOnly struct{ health execution.Health }

func (recordOnly) Available(string) (bool, time.Time) { return true, time.Time{} }

func (r recordOnly) Record(key string, err error) { r.health.Record(key, err) }

// admit takes a request token of candidate's instance. A saturated limit
// moves on to the next candidate without saying anything about the
// instance's health, unless candidate is the last, which waits.
func (f *Failover) admit(ctx context.Context, candidate FallbackCandidate, last bool) error {
	if f == nil || f.limits == nil {
		return nil
	}
	key := candidate.InstanceKey()
	if f.limits.TryAcquire(key) {
		return nil
	}
	if !last {
		return &core.ProviderError{
			Message:        key + " is waiting for its local rate limit",
			Class:          core.ProviderErrorRateLimited,
			Classification: core.ProviderErrorClassification{FailoverEligible: true},
		}
	}
	return f.limits.Wait(ctx, key)
}

// Failure is what core's classification says about a request's error, for
// the retry decision and the message a user reads.
type Failure struct {
	// Class names the kind of failure: the class the error gives itself,
	// else the one its status or a transport failure implies. It is empty
	// for an error core cannot classify, such as a cancellation.
	Class core.ProviderErrorClass
	// StatusCode is the upstream's HTTP status, zero when none was received.
	StatusCode int
	// RetryAfter is how long the upstream, or health, asked callers to wait.
	RetryAfter time.Duration
	// Disposition is what the error permits: retry, failover or nothing.
	Disposition core.Disposition
	// AfterOutput reports a stream that failed after output reached the
	// caller; the other fields describe that stream's own error, but the
	// disposition is terminal.
	AfterOutput bool
	// Unavailable reports that every candidate's instance was unavailable,
	// its circuit open or cooling down, so none was tried.
	Unavailable bool
	// Timeout reports a transport failure that was a timeout.
	Timeout bool
}

// DescribeFailure reads err's classification.
func DescribeFailure(err error) Failure {
	if err == nil {
		return Failure{}
	}
	failure := Failure{Disposition: Classify(err).Disposition()}
	subject := err
	var afterOutput *execution.AfterOutputError
	if errors.As(err, &afterOutput) && afterOutput.Err != nil {
		failure.AfterOutput = true
		subject = afterOutput.Err
	}
	var unavailable *execution.UnavailableError
	if errors.As(subject, &unavailable) {
		failure.Unavailable = true
		failure.RetryAfter = unavailable.RetryAfter
		return failure
	}
	classification := Classify(subject)
	failure.StatusCode = classification.StatusCode
	failure.RetryAfter = classification.RetryAfter
	failure.Class = errorClass(subject, classification)
	if failure.Class == core.ProviderErrorTransport || failure.StatusCode == http.StatusRequestTimeout {
		var netErr net.Error
		failure.Timeout = failure.StatusCode == http.StatusRequestTimeout ||
			errors.Is(subject, context.DeadlineExceeded) || (errors.As(subject, &netErr) && netErr.Timeout())
	}
	return failure
}

// errorClass names the kind of err as core's executor does: the class a
// *core.ProviderError gives itself, else the one its status implies, else a
// transport failure for a circuit failure without a status.
func errorClass(err error, classification core.ProviderErrorClassification) core.ProviderErrorClass {
	var providerErr *core.ProviderError
	if errors.As(err, &providerErr) && providerErr.Class != "" {
		return providerErr.Class
	}
	switch {
	case classification.StatusCode != 0:
		return core.ClassifyProviderFailure(core.ProviderFailure{StatusCode: classification.StatusCode, Err: err}).ErrorClass
	case classification.CircuitFailure:
		return core.ProviderErrorTransport
	}
	return ""
}

// RetryDelay reports whether a request that failed with err may be repeated
// and how long to wait first: only a retryable failure may be, after the
// larger of backoff and the upstream's Retry-After. A Retry-After longer
// than maxWait is not waited out: the failure stands.
func RetryDelay(err error, backoff, maxWait time.Duration) (time.Duration, bool) {
	failure := DescribeFailure(err)
	if failure.Disposition != core.DispositionRetryable {
		return 0, false
	}
	if maxWait > 0 && failure.RetryAfter > maxWait {
		return 0, false
	}
	return max(backoff, failure.RetryAfter), true
}
