package channels

import (
	"errors"
	"fmt"
	"time"
)

var (
	// ErrNotRunning indicates the channel is not running.
	// Manager will not retry.
	ErrNotRunning = errors.New("channel not running")

	// ErrRateLimit indicates the platform returned a rate-limit response (e.g. HTTP 429).
	// Manager waits before it retries: as long as a RateLimitError says, or
	// a fixed delay.
	ErrRateLimit = errors.New("rate limited")

	// ErrTemporary indicates a transient failure (e.g. network timeout, 5xx).
	// Manager will use exponential backoff and retry.
	ErrTemporary = errors.New("temporary failure")

	// ErrSendFailed indicates a permanent failure (e.g. invalid chat ID, 4xx non-429).
	// Manager will not retry.
	ErrSendFailed = errors.New("send failed")
)

// RateLimitError is a rate-limit response that says how long to wait before
// the next attempt (an HTTP Retry-After, or a platform's retry_after). It
// matches ErrRateLimit with errors.Is.
type RateLimitError struct {
	RetryAfter time.Duration
	Err        error
}

// NewRateLimitError wraps err as a rate-limit response that asks to wait
// retryAfter before the next attempt.
func NewRateLimitError(retryAfter time.Duration, err error) *RateLimitError {
	return &RateLimitError{RetryAfter: retryAfter, Err: err}
}

func (e *RateLimitError) Error() string {
	msg := fmt.Sprintf("%s (retry after %s)", ErrRateLimit.Error(), e.RetryAfter)
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *RateLimitError) Unwrap() error { return e.Err }

func (e *RateLimitError) Is(target error) bool { return target == ErrRateLimit }

// maxRetryAfter caps how long one rate-limit response may hold a channel's
// outbound messages.
const maxRetryAfter = 2 * time.Minute

// retryDelay is how long to wait before the next attempt after err, the
// failure of attempt (counted from 0).
func retryDelay(err error, attempt int) time.Duration {
	if errors.Is(err, ErrRateLimit) {
		var limited *RateLimitError
		if errors.As(err, &limited) && limited.RetryAfter > 0 {
			return min(limited.RetryAfter, maxRetryAfter)
		}
		return rateLimitDelay
	}
	return min(baseBackoff<<attempt, maxBackoff)
}
