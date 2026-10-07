package slack

import (
	"errors"
	"testing"
	"time"

	"github.com/slack-go/slack"

	"github.com/xibodev/compa/v3/pkg/channels"
)

func TestClassifySendError(t *testing.T) {
	var limited *channels.RateLimitError
	err := classifySendError(&slack.RateLimitedError{RetryAfter: 4 * time.Second})
	if !errors.As(err, &limited) || limited.RetryAfter != 4*time.Second {
		t.Fatalf("classifySendError(rate limit) = %v, want a 4s RateLimitError", err)
	}
	if err := classifySendError(errors.New("timeout")); !errors.Is(err, channels.ErrTemporary) {
		t.Fatalf("classifySendError(other) = %v, want ErrTemporary", err)
	}
}
