package discord

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/xibodev/compa/v2/pkg/channels"
)

func TestClassifySendError(t *testing.T) {
	limited := &discordgo.RateLimitError{RateLimit: &discordgo.RateLimit{
		TooManyRequests: &discordgo.TooManyRequests{RetryAfter: 3 * time.Second},
	}}
	var rl *channels.RateLimitError
	if err := classifySendError(limited); !errors.As(err, &rl) || rl.RetryAfter != 3*time.Second {
		t.Fatalf("classifySendError(rate limit) = %v, want a 3s RateLimitError", err)
	}

	header := http.Header{}
	header.Set("Retry-After", "2")
	tooMany := &discordgo.RESTError{Response: &http.Response{StatusCode: http.StatusTooManyRequests, Header: header}}
	if err := classifySendError(tooMany); !errors.As(err, &rl) || rl.RetryAfter != 2*time.Second {
		t.Fatalf("classifySendError(429) = %v, want a 2s RateLimitError", err)
	}

	forbidden := &discordgo.RESTError{Response: &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{}}}
	if err := classifySendError(forbidden); !errors.Is(err, channels.ErrSendFailed) {
		t.Fatalf("classifySendError(403) = %v, want ErrSendFailed", err)
	}
	if err := classifySendError(errors.New("connection reset")); !errors.Is(err, channels.ErrTemporary) {
		t.Fatalf("classifySendError(network) = %v, want ErrTemporary", err)
	}
}
