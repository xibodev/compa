package utils

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
)

const maxRetries = 3

var (
	retryDelayUnit        = time.Second
	maxRetrySleepDuration = 1 * time.Minute
)

// shouldRetry reports a status another try may get past: a timeout, a rate
// limit or a server failure that passes. 501, 505 and the like never do.
func shouldRetry(statusCode int) bool {
	switch statusCode {
	case http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// DoRequestWithRetry sends req, up to maxRetries times in all while it fails
// with a transport error or a status shouldRetry accepts, waiting between
// tries with exponential backoff and jitter, or a 429's Retry-After. Every
// try sends the whole body: it comes from req.GetBody, and a body without
// one is read once, up front.
func DoRequestWithRetry(client *http.Client, req *http.Request) (*http.Response, error) {
	if req.Body != nil && req.Body != http.NoBody && req.GetBody == nil {
		body, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to read request body: %w", err)
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	}

	var resp *http.Response
	var err error

	for i := range maxRetries {
		if i > 0 && resp != nil {
			_ = resp.Body.Close()
		}

		attempt := req
		if i > 0 {
			attempt = req.Clone(req.Context())
			if req.GetBody != nil {
				if attempt.Body, err = req.GetBody(); err != nil {
					return nil, fmt.Errorf("failed to reset request body: %w", err)
				}
			}
		}
		resp, err = client.Do(attempt)
		if err == nil {
			if resp.StatusCode == http.StatusOK {
				break
			}
			if !shouldRetry(resp.StatusCode) {
				break
			}
		}

		if i < maxRetries-1 {
			if err = sleepWithCtx(req.Context(), retryDelayForAttempt(resp, i)); err != nil {
				if resp != nil {
					_ = resp.Body.Close()
				}
				return nil, fmt.Errorf("failed to sleep: %w", err)
			}
		}
	}
	return resp, err
}

// retryDelayForAttempt is the wait before the try after attempt: a 429's
// Retry-After, else retryDelayUnit doubled for each try, plus up to half of
// it again at random, so clients that failed together do not retry together.
func retryDelayForAttempt(resp *http.Response, attempt int) time.Duration {
	backoff := retryDelayUnit << min(attempt, 10)
	fallback := backoff + rand.N(backoff/2+1)
	if resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		return clampRetryDelay(fallback)
	}

	retryAfter := resp.Header.Get("Retry-After")
	if retryAfter == "" {
		return clampRetryDelay(fallback)
	}

	if delay, ok := numericRetryAfterDelay(retryAfter); ok {
		return delay
	}

	if when, err := http.ParseTime(retryAfter); err == nil {
		delay := time.Until(when)
		if serverDate, err := http.ParseTime(resp.Header.Get("Date")); err == nil {
			delay = when.Sub(serverDate)
		}
		if delay < 0 {
			return 0
		}
		return clampRetryDelay(delay)
	}

	return clampRetryDelay(fallback)
}

func numericRetryAfterDelay(retryAfter string) (time.Duration, bool) {
	seconds, err := strconv.ParseInt(retryAfter, 10, 64)
	if err != nil || seconds < 0 {
		return 0, false
	}
	maxSeconds := int64(maxRetrySleepDuration / time.Second)
	if seconds > maxSeconds {
		return maxRetrySleepDuration, true
	}
	return clampRetryDelay(time.Duration(seconds) * time.Second), true
}

func clampRetryDelay(delay time.Duration) time.Duration {
	if delay <= 0 {
		return 0
	}
	if delay > maxRetrySleepDuration {
		return maxRetrySleepDuration
	}
	return delay
}

func sleepWithCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
