package agent

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	core "github.com/xibodev/llmgw-core"
	"github.com/xibodev/llmgw-core/execution"
)

// transientProviderError is a core transport failure: retryable, failover
// eligible and a circuit failure.
func transientProviderError(message string) error {
	return &core.ProviderError{
		Message: message, Class: core.ProviderErrorTransport,
		Classification: core.ProviderErrorClassification{Retryable: true, FailoverEligible: true, CircuitFailure: true},
	}
}

func upstreamStatusError(status int, retryAfter time.Duration) error {
	transient := status == http.StatusTooManyRequests || status >= 500
	return &core.ProviderError{
		Message: fmt.Sprintf("upstream returned HTTP %d", status),
		Class:   core.ClassifyProviderFailure(core.ProviderFailure{StatusCode: status}).ErrorClass,
		Classification: core.ProviderErrorClassification{
			StatusCode: status, Retryable: transient, FailoverEligible: transient, CircuitFailure: status >= 500, RetryAfter: retryAfter,
		},
	}
}

func TestFormatProcessingErrorFromCoreClassification(t *testing.T) {
	cases := map[string]struct {
		err  error
		want string
	}{
		"auth": {fmt.Errorf("LLM call failed after retries: %w", upstreamStatusError(http.StatusUnauthorized, 0)),
			"Authentication failed: check the API key or sign-in"},
		"forbidden":                        {upstreamStatusError(http.StatusForbidden, 0), "Access denied"},
		"rate limited":                     {upstreamStatusError(http.StatusTooManyRequests, 90*time.Second), "rate limiting requests. Try again in 1m30s."},
		"rate limited without retry-after": {upstreamStatusError(http.StatusTooManyRequests, 0), "The provider is rate limiting requests."},
		"server":                           {upstreamStatusError(http.StatusServiceUnavailable, 0), "The provider is unavailable (HTTP 503)"},
		"billing":                          {upstreamStatusError(http.StatusPaymentRequired, 0), "requires payment"},
		"bad request":                      {upstreamStatusError(http.StatusBadRequest, 0), "rejected the request (HTTP 400)"},
		"invalid": {&core.ProviderError{Message: "bad", Class: core.ProviderErrorInvalidRequest},
			"rejected the request as invalid"},
		"configuration": {core.NewConfigurationError("no credential", nil), "not configured correctly"},
		"unreachable":   {transientProviderError("connection refused"), "could not be reached"},
		"timeout": {&core.ProviderError{Message: "slow", Class: core.ProviderErrorTransport, Cause: errors.Join(errors.New("x"), contextDeadline{}),
			Classification: core.ProviderErrorClassification{Retryable: true, FailoverEligible: true, CircuitFailure: true}}, "did not answer in time"},
		"every instance unavailable": {&execution.UnavailableError{RetryAfter: 42 * time.Second},
			"temporarily unavailable after recent provider failures or rate limits. Try again in 42s."},
		"after output": {&execution.AfterOutputError{Err: transientProviderError("reset")},
			"could not be reached. Check the instance's endpoint, proxy and network, then try again. The response stopped part way through."},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := formatProcessingError(tc.err)
			if !strings.Contains(got, tc.want) {
				t.Fatalf("formatted error = %q, want it to contain %q", got, tc.want)
			}
			if !strings.Contains(got, "Original error:\n"+tc.err.Error()) {
				t.Fatalf("formatted error missing the original error: %q", got)
			}
		})
	}
}

// contextDeadline is a net.Error timeout.
type contextDeadline struct{}

func (contextDeadline) Error() string   { return "i/o timeout" }
func (contextDeadline) Timeout() bool   { return true }
func (contextDeadline) Temporary() bool { return true }

func TestFormatProcessingErrorKeepsUnclassifiedErrorsPlain(t *testing.T) {
	// Error text is never parsed: an unclassified error that mentions a
	// status or an API key gets no provider explanation.
	for _, err := range []error{
		errors.New("connection reset by peer"),
		errors.New("API request failed: Status: 401 Incorrect API key provided"),
	} {
		if got, want := formatProcessingError(err), "Error processing message: "+err.Error(); got != want {
			t.Fatalf("formatted error = %q, want %q", got, want)
		}
	}
}

func TestLLMRetryReasonFromCoreClassification(t *testing.T) {
	cases := map[error]string{
		upstreamStatusError(http.StatusTooManyRequests, 0): "rate_limit",
		upstreamStatusError(http.StatusBadGateway, 0):      "server_error",
		transientProviderError("reset"):                    "network",
		&core.ProviderError{Class: core.ProviderErrorTransport, Cause: contextDeadline{},
			Classification: core.ProviderErrorClassification{Retryable: true, FailoverEligible: true, CircuitFailure: true}}: "timeout",
	}
	for err, want := range cases {
		if got := llmRetryReason(err); got != want {
			t.Fatalf("llmRetryReason(%v) = %q, want %q", err, got, want)
		}
	}
}
