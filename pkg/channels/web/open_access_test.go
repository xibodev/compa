package web

import (
	"testing"

	"github.com/xibodev/compa/pkg/bus"
	"github.com/xibodev/compa/pkg/config"
)

// The built-in web chat is reached only through the password-protected
// dashboard, so an empty allow_from does not make it open access; a web
// chat client reaching out to another server stays an external channel.
func TestWebChatIsNotOpenAccess(t *testing.T) {
	if ch := newTestWebChannel(t); ch.OpenToEveryone() {
		t.Fatal("the built-in web chat reports open access")
	}
	client, err := NewWebClientChannel(&config.Channel{Type: config.ChannelWebClient, Enabled: true},
		&config.WebChatClientSettings{URL: "ws://127.0.0.1:1/ws"}, bus.NewMessageBus())
	if err != nil {
		t.Fatalf("NewWebClientChannel() error = %v", err)
	}
	if !client.OpenToEveryone() {
		t.Fatal("a web chat client without allow_from does not report open access")
	}
}
