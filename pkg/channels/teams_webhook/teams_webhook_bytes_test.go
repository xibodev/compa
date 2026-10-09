package teamswebhook

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	goteamsnotify "github.com/atc0005/go-teams-notify/v2"

	"github.com/xibodev/compa/v4/pkg/bus"
	"github.com/xibodev/compa/v4/pkg/channels"
	"github.com/xibodev/compa/v4/pkg/config"
)

// sendAndCollectPayloads sends content through a channel whose client records
// each post's encoded payload.
func sendAndCollectPayloads(t *testing.T, content string) []string {
	t.Helper()

	cfg := config.TeamsWebhookSettings{
		Webhooks: map[string]config.TeamsWebhookTarget{
			"default": {WebhookURL: *config.NewSecureString("https://example.com/webhook-default")},
		},
	}
	ch, err := NewTeamsWebhookChannel(&config.Channel{Type: config.ChannelTeamsWebHook, Enabled: true}, &cfg, bus.NewMessageBus())
	if err != nil {
		t.Fatalf("NewTeamsWebhookChannel: %v", err)
	}
	var payloads []string
	ch.client = &mockTeamsClient{
		sendFunc: func(_ context.Context, _ string, message goteamsnotify.TeamsMessage) error {
			encoded, err := json.Marshal(message)
			if err != nil {
				t.Fatalf("Marshal(message): %v", err)
			}
			payloads = append(payloads, string(encoded))
			return nil
		},
	}
	ctx := context.Background()
	_ = ch.Start(ctx)
	defer ch.Stop(ctx)

	if _, err := ch.Send(ctx, bus.OutboundMessage{Content: content}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	return payloads
}

func TestTeamsWebhookChannel_SendKeepsEachPostUnderThePayloadLimit(t *testing.T) {
	tests := []struct {
		name  string
		unit  string
		count int
	}{
		// Within the manager's character limit, yet three bytes per character.
		{name: "multi-byte text", unit: "漢", count: 20000},
		// One byte each, six once JSON escapes them.
		{name: "escaped text", unit: "<", count: 20000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := strings.Repeat(strings.Repeat(tt.unit, 99)+"\n", tt.count/100)
			payloads := sendAndCollectPayloads(t, content)

			if len(payloads) < 2 {
				t.Fatalf("posts = %d, want the text split over several", len(payloads))
			}
			carried := 0
			for i, payload := range payloads {
				if len(payload) > maxPayloadBytes {
					t.Fatalf("post %d is %d bytes, over the %d-byte limit", i, len(payload), maxPayloadBytes)
				}
				carried += strings.Count(payload, tt.unit) + strings.Count(payload, `\u003c`)
			}
			if want := strings.Count(content, tt.unit); carried != want {
				t.Fatalf("posts carry %d of the %d characters", carried, want)
			}
		})
	}
}

func TestTeamsWebhookChannel_SendPostsAShortMessageOnce(t *testing.T) {
	payloads := sendAndCollectPayloads(t, "Hello Teams!")
	if len(payloads) != 1 || !strings.Contains(payloads[0], "Hello Teams!") {
		t.Fatalf("payloads = %q, want one post with the text", payloads)
	}
	if payloads := sendAndCollectPayloads(t, ""); len(payloads) != 1 || !strings.Contains(payloads[0], "(empty message)") {
		t.Fatalf("payloads = %q, want one post for an empty message", payloads)
	}
}

// A message sent in several posts that fails after a post was delivered is
// not sent again: that would repeat the delivered posts. A failure of the
// first post can still be retried.
func TestTeamsWebhookChannel_SendFailingAfterADeliveredPostIsNotRetried(t *testing.T) {
	cfg := config.TeamsWebhookSettings{
		Webhooks: map[string]config.TeamsWebhookTarget{
			"default": {WebhookURL: *config.NewSecureString("https://example.com/webhook-default")},
		},
	}
	ch, err := NewTeamsWebhookChannel(&config.Channel{Type: config.ChannelTeamsWebHook, Enabled: true}, &cfg, bus.NewMessageBus())
	if err != nil {
		t.Fatalf("NewTeamsWebhookChannel: %v", err)
	}
	posts := 0
	ch.client = &mockTeamsClient{
		sendFunc: func(context.Context, string, goteamsnotify.TeamsMessage) error {
			posts++
			if posts == 2 {
				return errors.New("error on notification: 503 Service Unavailable")
			}
			return nil
		},
	}
	ctx := context.Background()
	_ = ch.Start(ctx)
	defer ch.Stop(ctx)

	long := strings.Repeat(strings.Repeat("漢", 99)+"\n", 200)
	if _, err := ch.Send(ctx, bus.OutboundMessage{Content: long}); !errors.Is(err, channels.ErrSendFailed) ||
		errors.Is(err, channels.ErrTemporary) {
		t.Fatalf("Send() error = %v, want a failure that is not retried", err)
	}
	if posts != 2 {
		t.Fatalf("posts = %d, want the delivered one and the failed one", posts)
	}

	posts = 1 // the next post fails
	if _, err := ch.Send(ctx, bus.OutboundMessage{Content: "short"}); !errors.Is(err, channels.ErrTemporary) {
		t.Fatalf("Send() error = %v, want a failed first post to be retried", err)
	}
}
