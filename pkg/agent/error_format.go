package agent

import (
	"fmt"
	"net/http"
	"time"

	core "github.com/xibodev/llmgw-core"

	"github.com/xibodev/compa/v2/pkg/providers"
)

func formatProcessingError(err error) string {
	if err == nil {
		return ""
	}
	if friendly := providerFailureMessage(err); friendly != "" {
		return fmt.Sprintf(
			"Error processing message: %s\n\nOriginal error:\n%s",
			friendly,
			err.Error(),
		)
	}
	return fmt.Sprintf("Error processing message: %v", err)
}

// providerFailureMessage explains a model call's failure from core's
// classification of its error, or returns "" for an error core does not
// classify.
func providerFailureMessage(err error) string {
	failure := providers.DescribeFailure(err)
	if failure.Unavailable {
		return "Every model for this request is temporarily unavailable after recent provider failures or rate limits." +
			retryAfterHint(failure.RetryAfter)
	}
	var message string
	switch failure.Class {
	case core.ProviderErrorAuth:
		message = "Authentication failed: check the API key or sign-in configured for this model's provider instance."
	case core.ProviderErrorForbidden:
		message = "Access denied: the provider refused this request; check the account's permissions and plan for this model."
	case core.ProviderErrorRateLimited:
		message = "The provider is rate limiting requests." + retryAfterHint(failure.RetryAfter)
	case core.ProviderErrorTransport:
		if failure.Timeout {
			message = "The provider did not answer in time. Try again, or raise the instance's request timeout."
		} else {
			message = "The provider could not be reached. Check the instance's endpoint, proxy and network, then try again."
		}
	case core.ProviderErrorUpstream:
		message = upstreamFailureMessage(failure)
	case core.ProviderErrorInvalidRequest:
		message = "The provider rejected the request as invalid for this model."
	case core.ProviderErrorConfiguration:
		message = "The model's provider instance is not configured correctly: check its credentials and settings."
	case core.ProviderErrorUnsupported:
		message = "This model's provider does not support this kind of request."
	default:
		return ""
	}
	if failure.AfterOutput {
		message += " The response stopped part way through."
	}
	return message
}

func upstreamFailureMessage(failure providers.Failure) string {
	switch {
	case failure.StatusCode == http.StatusPaymentRequired:
		return "The provider requires payment: check the account's billing or credits."
	case failure.StatusCode == http.StatusRequestTimeout:
		return "The provider timed out handling the request. Try again."
	case failure.StatusCode >= 500:
		return fmt.Sprintf("The provider is unavailable (HTTP %d). Try again shortly.", failure.StatusCode) +
			retryAfterHint(failure.RetryAfter)
	case failure.StatusCode >= 400:
		return fmt.Sprintf("The provider rejected the request (HTTP %d).", failure.StatusCode)
	}
	return "The provider returned an unusable answer. Try again, or check the instance's endpoint."
}

func retryAfterHint(wait time.Duration) string {
	if wait <= 0 {
		return ""
	}
	return fmt.Sprintf(" Try again in %s.", max(time.Second, wait.Round(time.Second)).String())
}
