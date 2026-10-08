package whatsapp

import "testing"

func TestClassifyChat(t *testing.T) {
	for _, tt := range []struct {
		name             string
		selfChat, fromMe bool
		want             chatRole
	}{
		{"the owner in their own chat", true, true, chatSelf},
		{"someone else in the own chat", true, false, chatIgnored},
		{"the owner writing elsewhere", false, true, chatIgnored},
		{"someone else elsewhere", false, false, chatOther},
	} {
		if got := classifyChat(tt.selfChat, tt.fromMe); got != tt.want {
			t.Errorf("%s: classifyChat = %v, want %v", tt.name, got, tt.want)
		}
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
	id := whatsAppIdentity("15550003333", phoneServer, "Friend")
	if id.Platform != "whatsapp" || id.PlatformID != "15550003333" ||
		id.CanonicalID != "whatsapp:15550003333" || id.DisplayName != "Friend" {
		t.Fatalf("whatsAppIdentity(phone) = %+v", id)
	}
	if id := whatsAppIdentity("98765", lidServer, ""); id.PlatformID != "98765@lid" || id.CanonicalID != "whatsapp:98765@lid" {
		t.Fatalf("whatsAppIdentity(lid) = %+v", id)
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
