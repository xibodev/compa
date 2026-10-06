package identity

import (
	"testing"

	"github.com/xibodev/compa/v2/pkg/bus"
)

func TestBuildCanonicalID(t *testing.T) {
	tests := []struct {
		platform   string
		platformID string
		want       string
	}{
		{"telegram", "123456", "telegram:123456"},
		{"Discord", "98765432", "discord:98765432"},
		{"SLACK", "U123ABC", "slack:U123ABC"},
		{"", "123", ""},
		{"telegram", "", ""},
		{"  telegram  ", "  123  ", "telegram:123"},
	}

	for _, tt := range tests {
		got := BuildCanonicalID(tt.platform, tt.platformID)
		if got != tt.want {
			t.Errorf("BuildCanonicalID(%q, %q) = %q, want %q",
				tt.platform, tt.platformID, got, tt.want)
		}
	}
}

func TestParseCanonicalID(t *testing.T) {
	tests := []struct {
		input        string
		wantPlatform string
		wantID       string
		wantOk       bool
	}{
		{"telegram:123456", "telegram", "123456", true},
		{"discord:98765432", "discord", "98765432", true},
		{"slack:U123ABC", "slack", "U123ABC", true},
		{"nocolon", "", "", false},
		{"", "", "", false},
		{":missing", "", "", false},
		{"missing:", "", "", false},
	}

	for _, tt := range tests {
		platform, id, ok := ParseCanonicalID(tt.input)
		if ok != tt.wantOk || platform != tt.wantPlatform || id != tt.wantID {
			t.Errorf("ParseCanonicalID(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tt.input, platform, id, ok,
				tt.wantPlatform, tt.wantID, tt.wantOk)
		}
	}
}

func TestMatchAllowed(t *testing.T) {
	telegramSender := bus.SenderInfo{
		Platform:    "telegram",
		PlatformID:  "123456",
		CanonicalID: "telegram:123456",
		Username:    "alice",
		DisplayName: "Alice Smith",
	}

	discordSender := bus.SenderInfo{
		Platform:    "discord",
		PlatformID:  "98765432",
		CanonicalID: "discord:98765432",
		Username:    "bob",
		DisplayName: "bob#1234",
	}

	noCanonicalSender := bus.SenderInfo{
		Platform:   "telegram",
		PlatformID: "999",
		Username:   "carol",
	}

	tests := []struct {
		name    string
		sender  bus.SenderInfo
		allowed string
		want    bool
	}{
		// Pure numeric ID matching
		{
			name:    "numeric ID matches PlatformID",
			sender:  telegramSender,
			allowed: "123456",
			want:    true,
		},
		{
			name:    "numeric ID does not match",
			sender:  telegramSender,
			allowed: "654321",
			want:    false,
		},
		{
			name: "negative numeric ID matches PlatformID",
			sender: bus.SenderInfo{
				Platform:   "telegram",
				PlatformID: "-1001234567890",
			},
			allowed: "-1001234567890",
			want:    true,
		},
		// Username matching
		{
			name:    "@username matches Username",
			sender:  telegramSender,
			allowed: "@alice",
			want:    true,
		},
		{
			name: "plain entry does not match username",
			sender: bus.SenderInfo{
				Platform:   "discord",
				PlatformID: "999999",
				Username:   "123456",
			},
			allowed: "123456",
			want:    false,
		},
		{
			name:    "@username does not match",
			sender:  telegramSender,
			allowed: "@bob",
			want:    false,
		},
		// "id|username" is not an allow-list format
		{
			name:    "id|username entry is not split",
			sender:  telegramSender,
			allowed: "123456|alice",
			want:    false,
		},
		// Canonical format "platform:id"
		{
			name:    "canonical matches exactly",
			sender:  telegramSender,
			allowed: "telegram:123456",
			want:    true,
		},
		{
			name:    "canonical case-insensitive platform",
			sender:  telegramSender,
			allowed: "Telegram:123456",
			want:    true,
		},
		{
			name:    "canonical wrong platform",
			sender:  telegramSender,
			allowed: "discord:123456",
			want:    false,
		},
		{
			name:    "canonical wrong ID",
			sender:  telegramSender,
			allowed: "telegram:654321",
			want:    false,
		},
		// Cross-platform canonical
		{
			name:    "discord canonical match",
			sender:  discordSender,
			allowed: "discord:98765432",
			want:    true,
		},
		{
			name:    "telegram canonical does not match discord sender",
			sender:  discordSender,
			allowed: "telegram:98765432",
			want:    false,
		},
		// Sender without canonical ID
		{
			name:    "canonical match falls back to platform+platformID",
			sender:  noCanonicalSender,
			allowed: "telegram:999",
			want:    true,
		},
		{
			name:    "platform mismatch on fallback",
			sender:  noCanonicalSender,
			allowed: "discord:999",
			want:    false,
		},
		// Empty allowed string
		{
			name:    "empty allowed never matches",
			sender:  telegramSender,
			allowed: "",
			want:    false,
		},
		// Whitespace handling
		{
			name:    "trimmed allowed matches",
			sender:  telegramSender,
			allowed: "  123456  ",
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchAllowed(tt.sender, tt.allowed)
			if got != tt.want {
				t.Errorf("MatchAllowed(%+v, %q) = %v, want %v",
					tt.sender, tt.allowed, got, tt.want)
			}
		})
	}
}

func TestMatchAllowedPlatformSpecificEntries(t *testing.T) {
	matrixSender := bus.SenderInfo{
		Platform:    "matrix",
		PlatformID:  "@alice:matrix.org",
		CanonicalID: "matrix:@alice:matrix.org",
		Username:    "@alice:matrix.org",
	}
	telegramSender := bus.SenderInfo{
		Platform:    "telegram",
		PlatformID:  "123456",
		CanonicalID: "telegram:123456",
		Username:    "Alice_Bot",
	}
	discordSender := bus.SenderInfo{
		Platform:    "discord",
		PlatformID:  "98765432",
		CanonicalID: "discord:98765432",
		Username:    "bob",
	}
	slackSender := bus.SenderInfo{
		Platform:    "slack",
		PlatformID:  "U123",
		CanonicalID: "slack:U123",
		Username:    "Carol",
	}
	// DeltaChat crossposting names the sender by the local part only.
	deltaSender := bus.SenderInfo{
		Platform:    "deltachat",
		PlatformID:  "alice@example.org",
		CanonicalID: "deltachat:alice@example.org",
		Username:    "alice",
	}
	ircSender := bus.SenderInfo{
		Platform:    "irc",
		PlatformID:  "nick:with:colons",
		CanonicalID: "irc:nick:with:colons",
	}

	tests := []struct {
		name    string
		sender  bus.SenderInfo
		allowed string
		want    bool
	}{
		// Matrix user IDs contain a colon but are not "platform:id".
		{"matrix user ID", matrixSender, "@alice:matrix.org", true},
		{"matrix canonical user ID", matrixSender, "matrix:@alice:matrix.org", true},
		{"matrix other user", matrixSender, "@alice:evil.org", false},
		{"matrix user ID is case-sensitive", matrixSender, "@Alice:matrix.org", false},
		// Only known channel names are platform prefixes.
		{"unknown prefix is part of the ID", ircSender, "nick:with:colons", true},
		{"unknown prefix does not match canonical form", telegramSender, "telegramx:123456", false},
		{"known platform still canonical", telegramSender, "telegram:123456", true},
		{"other known platform does not match", telegramSender, "discord:123456", false},
		{"numeric prefix is an ID", bus.SenderInfo{Platform: "x", PlatformID: "1:2"}, "1:2", true},
		// Telegram and Discord usernames ignore case.
		{"telegram username any case", telegramSender, "@alice_bot", true},
		{"telegram username upper case", telegramSender, "@ALICE_BOT", true},
		{"telegram username differs", telegramSender, "@alice", false},
		{"discord username any case", discordSender, "@Bob", true},
		{"slack username keeps case", slackSender, "@carol", false},
		{"slack username exact", slackSender, "@Carol", true},
		{"platform-scoped username", telegramSender, "telegram:@alice_bot", true},
		{"platform-scoped username on other platform", telegramSender, "discord:@alice_bot", false},
		// DeltaChat "@name" never matches a bare local part.
		{"deltachat local part only", deltaSender, "@alice", false},
		{"deltachat other domain", deltaSender, "@alice@evil.org", false},
		{"deltachat full address with @", deltaSender, "@alice@example.org", true},
		{"deltachat full address any case", deltaSender, "Alice@Example.org", true},
		{"deltachat canonical", deltaSender, "deltachat:alice@example.org", true},
		{"deltachat plain local part", deltaSender, "alice", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MatchAllowed(tt.sender, tt.allowed); got != tt.want {
				t.Errorf("MatchAllowed(%+v, %q) = %v, want %v", tt.sender, tt.allowed, got, tt.want)
			}
		})
	}
}

func TestIsNumeric(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"123456", true},
		{"0", true},
		{"", false},
		{"abc", false},
		{"12a34", false},
		{"telegram", false},
		{"-1001234567890", true},
		{"-", false},
		{"-12a34", false},
	}

	for _, tt := range tests {
		got := isNumeric(tt.input)
		if got != tt.want {
			t.Errorf("isNumeric(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}
