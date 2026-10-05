package whatsapp

import (
	"testing"

	"github.com/xibodev/compa/pkg/config"
)

func TestClassifyChat(t *testing.T) {
	tests := []struct {
		name     string
		mode     string
		selfChat bool
		fromMe   bool
		want     chatRole
	}{
		{"self mode: own chat is input", config.WhatsAppChatsSelf, true, true, chatSelf},
		{"self mode: other chat is not input", config.WhatsAppChatsSelf, false, false, chatIgnored},
		{"self mode: own message to a friend", config.WhatsAppChatsSelf, false, true, chatIgnored},
		{"allowed mode: own chat is input", config.WhatsAppChatsAllowed, true, true, chatSelf},
		{"allowed mode: other chat goes to the policies", config.WhatsAppChatsAllowed, false, false, chatOther},
		{"allowed mode: own message to a friend", config.WhatsAppChatsAllowed, false, true, chatIgnored},
		{"all mode: own chat is input", config.WhatsAppChatsAll, true, true, chatSelf},
		{"all mode: other chat is input", config.WhatsAppChatsAll, false, false, chatOther},
		{"all mode: own message to a group", config.WhatsAppChatsAll, false, true, chatIgnored},
		{"unset mode behaves as self", "", false, false, chatIgnored},
		{"someone else in the own chat", config.WhatsAppChatsAll, true, false, chatIgnored},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mode := config.WhatsAppSettings{Chats: tt.mode}.EffectiveChats()
			if got := classifyChat(mode, tt.selfChat, tt.fromMe); got != tt.want {
				t.Fatalf("classifyChat(%q, self=%v, fromMe=%v) = %v, want %v",
					mode, tt.selfChat, tt.fromMe, got, tt.want)
			}
		})
	}
}

func TestIsOwnAddress(t *testing.T) {
	tests := []struct {
		user, server string
		want         bool
	}{
		{"15550001111", phoneServer, true},
		{"98765", lidServer, true},
		{"15550002222", phoneServer, false},
		{"15550001111", lidServer, false},
		{"15550001111", "g.us", false},
		{"", phoneServer, false},
	}
	for _, tt := range tests {
		if got := isOwnAddress(tt.user, tt.server, "15550001111", "98765"); got != tt.want {
			t.Errorf("isOwnAddress(%q, %q) = %v, want %v", tt.user, tt.server, got, tt.want)
		}
	}
	if isOwnAddress("", phoneServer, "", "") {
		t.Error("an unpaired account has no own chat")
	}
}

func TestWhatsAppIdentity(t *testing.T) {
	id := whatsAppIdentity("15550003333@s.whatsapp.net", "Friend")
	if id.Platform != "whatsapp" || id.PlatformID != "15550003333@s.whatsapp.net" ||
		id.CanonicalID != "whatsapp:15550003333@s.whatsapp.net" || id.DisplayName != "Friend" {
		t.Fatalf("whatsAppIdentity = %+v", id)
	}
}

func TestSentMessagesForgetsOldest(t *testing.T) {
	sent := newSentMessages(2)
	sent.add("a")
	sent.add("b")
	sent.add("c")
	if sent.contains("a") {
		t.Error("oldest ID should be forgotten")
	}
	if !sent.contains("b") || !sent.contains("c") {
		t.Error("latest IDs should be remembered")
	}
	if sent.contains("") {
		t.Error("an empty ID is never a sent message")
	}
}
