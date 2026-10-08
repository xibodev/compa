package channels

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/xibodev/compa/v3/pkg/bus"
	"github.com/xibodev/compa/v3/pkg/config"
)

// A notification reaches its chat and every webhook; a notify-only one, the
// webhooks alone; any other message, its chat alone.
func TestNotificationsReachTheWebhooks(t *testing.T) {
	m := newTestManager()
	m.config = &config.Config{Channels: config.ChannelsConfig{
		"slack_hook": {Type: config.ChannelSlackWebHook, Enabled: true},
		"teams_hook": {Type: config.ChannelTeamsWebHook, Enabled: true},
		"chat":       {Type: config.ChannelWhatsApp, Enabled: true},
	}}
	var mu sync.Mutex
	sent := map[string][]string{}
	record := func(name string) *mockChannel {
		return &mockChannel{sendFn: func(_ context.Context, msg bus.OutboundMessage) error {
			mu.Lock()
			defer mu.Unlock()
			sent[name] = append(sent[name], msg.Content+"@"+msg.ChatID)
			return nil
		}}
	}
	for _, name := range []string{"slack_hook", "teams_hook", "chat"} {
		m.channels[name] = record(name)
	}
	if err := m.StartAll(t.Context()); err != nil {
		t.Fatalf("StartAll() error = %v", err)
	}
	t.Cleanup(func() { _ = m.StopAll(context.Background()) })

	for _, msg := range []bus.OutboundMessage{
		{Context: bus.NewOutboundContext("chat", "owner", ""), Content: "job done", Notify: true},
		{Content: "heartbeat", Notify: true},
		{Context: bus.NewOutboundContext("chat", "owner", ""), Content: "a reply"},
	} {
		if err := m.bus.PublishOutbound(t.Context(), testOutboundMessage(msg)); err != nil {
			t.Fatalf("PublishOutbound() error = %v", err)
		}
	}

	want := map[string]string{
		"slack_hook": "job done@default,heartbeat@default",
		"teams_hook": "job done@default,heartbeat@default",
		"chat":       "job done@owner,a reply@owner",
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		got := map[string]string{}
		for name, contents := range sent {
			got[name] = join(contents)
		}
		mu.Unlock()
		if got["slack_hook"] == want["slack_hook"] && got["teams_hook"] == want["teams_hook"] && got["chat"] == want["chat"] {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("sent = %v, want %v", got, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func join(items []string) string {
	out := ""
	for i, s := range items {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out
}

// Slack runs in Socket Mode, which needs the app-level token: a channel with
// only its bot token is not ready.
func TestSlackNeedsBothTokens(t *testing.T) {
	for name, tc := range map[string]struct {
		settings string
		ready    bool
	}{
		"bot token only": {`{"bot_token":"xoxb-1"}`, false},
		"both tokens":    {`{"bot_token":"xoxb-1","app_token":"xapp-1"}`, true},
	} {
		bc := &config.Channel{Type: config.ChannelSlack, Enabled: true, Settings: config.RawNode(tc.settings)}
		m := &Manager{config: &config.Config{Channels: config.ChannelsConfig{"slack": bc}}}
		if err := config.InitChannelList(m.config.Channels); err != nil {
			t.Fatalf("%s: InitChannelList() error = %v", name, err)
		}
		if _, ready := m.getChannelConfigAndEnabled("slack"); ready != tc.ready {
			t.Errorf("%s: ready = %v, want %v", name, ready, tc.ready)
		}
	}
}
