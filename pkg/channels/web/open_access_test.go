package web

import (
	"testing"

	"github.com/xibodev/compa/v3/pkg/bus"
	"github.com/xibodev/compa/v3/pkg/config"
)

// The built-in web chat is reached only through the password-protected
// dashboard, so every message it gets is from the owner; a web chat client
// reaching out to another server is a chat channel, which admits only the
// owner.
func TestWebChatAdmitsTheOwnerAndTheClientIsOwnerOnly(t *testing.T) {
	stranger := bus.SenderInfo{Platform: "web", PlatformID: "someone", CanonicalID: "web:someone"}
	if ch := newTestWebChannel(t); !ch.Admits("direct", stranger, "chat") {
		t.Fatal("the built-in web chat refuses a message")
	}

	bc := &config.Channel{Type: config.ChannelWebClient, Enabled: true, AllowFrom: config.FlexibleStringSlice{"web:owner"}}
	client, err := NewWebClientChannel(bc, &config.WebChatClientSettings{URL: "ws://127.0.0.1:1/ws"},
		bus.NewMessageBus())
	if err != nil {
		t.Fatalf("NewWebClientChannel() error = %v", err)
	}
	client.RequireOwner()
	if client.Admits("direct", stranger, "chat") {
		t.Fatal("the web chat client admitted a stranger")
	}
	owner := bus.SenderInfo{Platform: "web", PlatformID: "owner", CanonicalID: "web:owner"}
	if !client.Admits("direct", owner, "chat") {
		t.Fatal("the web chat client refused its owner")
	}
}
