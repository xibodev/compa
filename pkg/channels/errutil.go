package channels

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ClassifySendError wraps a raw error with the appropriate sentinel based on
// an HTTP status code. Channels that perform HTTP API calls should use this
// in their Send path.
func ClassifySendError(statusCode int, rawErr error) error {
	switch {
	case statusCode == http.StatusTooManyRequests:
		return fmt.Errorf("%w: %w", ErrRateLimit, rawErr)
	case statusCode >= 500:
		return fmt.Errorf("%w: %w", ErrTemporary, rawErr)
	case statusCode >= 400:
		return fmt.Errorf("%w: %w", ErrSendFailed, rawErr)
	default:
		return rawErr
	}
}

// ClassifySendErrorRetryAfter is ClassifySendError for a response whose
// Retry-After header says when a 429 may be retried.
func ClassifySendErrorRetryAfter(statusCode int, retryAfter string, rawErr error) error {
	if statusCode == http.StatusTooManyRequests {
		if delay, ok := ParseRetryAfter(retryAfter, time.Now()); ok {
			return NewRateLimitError(delay, rawErr)
		}
	}
	return ClassifySendError(statusCode, rawErr)
}

// ParseRetryAfter parses an HTTP Retry-After value: a number of seconds
// (fractions allowed, as some platforms send) or an HTTP date.
func ParseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseFloat(value, 64); err == nil {
		seconds = min(max(seconds, 0), maxRetryAfter.Seconds())
		return time.Duration(seconds * float64(time.Second)), true
	}
	if at, err := http.ParseTime(value); err == nil {
		return max(at.Sub(now), 0), true
	}
	return 0, false
}

// ClassifyNetError wraps a network/timeout error as ErrTemporary.
func ClassifyNetError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrTemporary, err)
}
