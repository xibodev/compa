package web

import (
	"testing"

	"github.com/xibodev/compa/pkg/bus"
	"github.com/xibodev/compa/pkg/config"
)

// The built-in web chat is reached only through the password-protected
// dashboard, so an empty allow_from does not make it open access; a web
// chat client reaching out to another server is a chat channel, open only
// when its access policies open it.
func TestWebChatIsNotOpenAccess(t *testing.T) {
	if ch := newTestWebChannel(t); ch.OpenToEveryone() {
		t.Fatal("the built-in web chat reports open access")
	}
	for _, tt := range []struct {
		dmPolicy string
		want     bool
	}{
		{"", false},
		{config.DMPolicyOpen, true},
	} {
		bc := &config.Channel{Type: config.ChannelWebClient, Enabled: true, DMPolicy: tt.dmPolicy}
		client, err := NewWebClientChannel(bc, &config.WebChatClientSettings{URL: "ws://127.0.0.1:1/ws"},
			bus.NewMessageBus())
		if err != nil {
			t.Fatalf("NewWebClientChannel() error = %v", err)
		}
		client.SetAccessPolicy(bc.EffectiveDMPolicy(), bc.EffectiveGroupPolicy())
		if got := client.OpenToEveryone(); got != tt.want {
			t.Fatalf("web chat client with dm_policy %q: OpenToEveryone() = %v, want %v", tt.dmPolicy, got, tt.want)
		}
	}
}
