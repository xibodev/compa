package irc

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ergochat/irc-go/ircevent"
	"github.com/ergochat/irc-go/ircmsg"

	"github.com/xibodev/compa/v2/pkg/bus"
	"github.com/xibodev/compa/v2/pkg/config"
)

func TestPrivmsgLinesFitTheProtocolLimit(t *testing.T) {
	nick, user, target := "compa", "compa", "#a-rather-long-channel-name"
	budget := privmsgTextBudget(nick, user, target)

	content := strings.Repeat("word ", 300) + "\n" +
		strings.Repeat("ñandú ", 200) + "\n\n" + // two-byte runes
		strings.Repeat("界", 400) // three-byte runes, no spaces
	lines := privmsgLines(content, budget)
	if len(lines) < 4 {
		t.Fatalf("expected the long lines to be split, got %d lines", len(lines))
	}
	for _, line := range lines {
		relayed := ":" + nick + "!~" + user + "@" + strings.Repeat("h", maxHostLen) +
			" PRIVMSG " + target + " :" + line + "\r\n"
		if len(relayed) > lineLimit {
			t.Fatalf("relayed line is %d bytes, over the %d-byte limit", len(relayed), lineLimit)
		}
		if !utf8.ValidString(line) {
			t.Fatalf("line split inside a UTF-8 sequence: %q", line)
		}
		if strings.TrimSpace(line) == "" {
			t.Fatal("empty line sent")
		}
	}
	if got := strings.Join(lines, ""); strings.Count(got, "界") != 400 {
		t.Fatal("splitting lost text")
	}
}

func TestSplitLinePrefersSpaces(t *testing.T) {
	parts := splitLine("alpha beta gamma delta", 12)
	for _, part := range parts {
		if strings.HasPrefix(part, " ") || strings.HasSuffix(part, " ") {
			t.Fatalf("part %q should break at a space", part)
		}
		if len(part) > 12 {
			t.Fatalf("part %q exceeds 12 bytes", part)
		}
	}
	if strings.Join(parts, " ") != "alpha beta gamma delta" {
		t.Fatalf("parts = %q", parts)
	}
}

func TestSanitizeLineDropsControlCharacters(t *testing.T) {
	if got := sanitizeLine("\x01DCC SEND x\x01 a\rb\x00c"); got != "DCC SEND x abc" {
		t.Fatalf("sanitizeLine = %q", got)
	}
}

func TestFoldCase(t *testing.T) {
	if got := foldCase("Alice[Away]~\\"); got != "alice{away}^|" {
		t.Fatalf("foldCase = %q", got)
	}
	got := foldAllowList([]string{"Alice", "@Bob", "IRC:Carol", "*", ""})
	want := []string{"alice", "@bob", "irc:carol", "*", ""}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("foldAllowList = %q, want %q", got, want)
		}
	}
}

func privmsg(t *testing.T, line string) ircmsg.Message {
	t.Helper()
	msg, err := ircmsg.ParseLine(line)
	if err != nil {
		t.Fatalf("ParseLine(%q): %v", line, err)
	}
	return msg
}

func TestOnPrivmsgIdentifiesSendersByAccount(t *testing.T) {
	msgBus := bus.NewMessageBus()
	bc := &config.Channel{Type: config.ChannelIRC, Enabled: true, AllowFrom: config.FlexibleStringSlice{"Alice"}}
	ch, err := NewIRCChannel(bc, &config.IRCSettings{Server: "irc.example.com:6697", Nick: "compa"}, msgBus)
	if err != nil {
		t.Fatalf("NewIRCChannel: %v", err)
	}
	ch.ctx = context.Background()
	conn := &ircevent.Connection{Nick: "compa"}

	// Someone else holding the nick, not logged in to the listed account.
	ch.onPrivmsg(conn, privmsg(t, "@account=mallory :ALICE!m@host PRIVMSG compa :hi"))
	// The listed user, logged in to "alice", writing under another nick.
	ch.onPrivmsg(conn, privmsg(t, "@account=Alice :alice_away!a@host PRIVMSG compa :hello"))

	select {
	case inbound := <-msgBus.InboundChan():
		if inbound.Content != "hello" {
			t.Fatalf("content = %q: the account, not the nick, identifies the sender", inbound.Content)
		}
		if inbound.Sender.CanonicalID != "irc:alice" || inbound.ChatID != "alice" {
			t.Fatalf("sender = %+v, chat = %q", inbound.Sender, inbound.ChatID)
		}
		if inbound.Context.ChatType != "direct" {
			t.Fatalf("chat type = %q", inbound.Context.ChatType)
		}
	case <-time.After(time.Second):
		t.Fatal("expected the logged-in user's message")
	}
	select {
	case inbound := <-msgBus.InboundChan():
		t.Fatalf("unexpected message: %#v", inbound)
	case <-time.After(50 * time.Millisecond):
	}

	if nick, ok := ch.dmNicks.Load("alice"); !ok || nick != "alice_away" {
		t.Fatalf("replies to the account's chat go to the current nick, got %v", nick)
	}
}

func TestOnPrivmsgFallsBackToFoldedNick(t *testing.T) {
	msgBus := bus.NewMessageBus()
	bc := &config.Channel{Type: config.ChannelIRC, Enabled: true, AllowFrom: config.FlexibleStringSlice{"bob"}}
	ch, err := NewIRCChannel(bc, &config.IRCSettings{Server: "irc.example.com:6697", Nick: "compa"}, msgBus)
	if err != nil {
		t.Fatalf("NewIRCChannel: %v", err)
	}
	ch.ctx = context.Background()

	ch.onPrivmsg(&ircevent.Connection{Nick: "compa"}, privmsg(t, ":BoB!b@host PRIVMSG compa :ping"))

	select {
	case inbound := <-msgBus.InboundChan():
		if inbound.Sender.PlatformID != "bob" || inbound.ChatID != "bob" {
			t.Fatalf("sender = %+v, chat = %q", inbound.Sender, inbound.ChatID)
		}
	case <-time.After(time.Second):
		t.Fatal("a nick matches its allow_from entry regardless of case")
	}
}

func TestNewIRCChannel(t *testing.T) {
	msgBus := bus.NewMessageBus()

	t.Run("missing server", func(t *testing.T) {
		bc := &config.Channel{Type: config.ChannelIRC, Enabled: true}
		cfg := &config.IRCSettings{Nick: "bot"}
		_, err := NewIRCChannel(bc, cfg, msgBus)
		if err == nil {
			t.Error("expected error for missing server, got nil")
		}
	})

	t.Run("missing nick", func(t *testing.T) {
		bc := &config.Channel{Type: config.ChannelIRC, Enabled: true}
		cfg := &config.IRCSettings{Server: "irc.example.com:6667"}
		_, err := NewIRCChannel(bc, cfg, msgBus)
		if err == nil {
			t.Error("expected error for missing nick, got nil")
		}
	})

	t.Run("valid config", func(t *testing.T) {
		bc := &config.Channel{Type: config.ChannelIRC, Enabled: true}
		cfg := &config.IRCSettings{
			Server:   "irc.example.com:6667",
			Nick:     "testbot",
			Channels: []string{"#test"},
		}
		ch, err := NewIRCChannel(bc, cfg, msgBus)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ch.Name() != "irc" {
			t.Errorf("Name() = %q, want %q", ch.Name(), "irc")
		}
		if ch.IsRunning() {
			t.Error("new channel should not be running")
		}
	})
}

func TestExtractHost(t *testing.T) {
	tests := []struct {
		server string
		want   string
	}{
		{"irc.libera.chat:6697", "irc.libera.chat"},
		{"localhost:6667", "localhost"},
		{"irc.example.com", "irc.example.com"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.server, func(t *testing.T) {
			got := extractHost(tt.server)
			if got != tt.want {
				t.Errorf("extractHost(%q) = %q, want %q", tt.server, got, tt.want)
			}
		})
	}
}

func TestNickMentionedAt(t *testing.T) {
	tests := []struct {
		name    string
		content string
		nick    string
		want    int
	}{
		{"colon prefix", "bot: hello", "bot", 0},
		{"comma prefix", "bot, hello", "bot", 0},
		{"case insensitive", "BOT: hello", "bot", 0},
		{"word boundary mid", "hey bot what's up", "bot", 4},
		{"no mention", "hello world", "bot", -1},
		{"substring mismatch", "robotics are cool", "bot", -1},
		{"nick at end", "hello bot", "bot", 6},
		{"empty content", "", "bot", -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nickMentionedAt(tt.content, tt.nick)
			if got != tt.want {
				t.Errorf("nickMentionedAt(%q, %q) = %d, want %d", tt.content, tt.nick, got, tt.want)
			}
		})
	}
}

func TestIsBotMentioned(t *testing.T) {
	tests := []struct {
		name    string
		content string
		nick    string
		want    bool
	}{
		{"colon prefix", "bot: hello", "bot", true},
		{"comma prefix", "bot, hello", "bot", true},
		{"case insensitive", "BOT: hello", "bot", true},
		{"word boundary mid", "hey bot what's up", "bot", true},
		{"no mention", "hello world", "bot", false},
		{"substring mismatch", "robotics are cool", "bot", false},
		{"nick at end", "hello bot", "bot", true},
		{"empty content", "", "bot", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isBotMentioned(tt.content, tt.nick)
			if got != tt.want {
				t.Errorf("isBotMentioned(%q, %q) = %v, want %v", tt.content, tt.nick, got, tt.want)
			}
		})
	}
}

func TestStripBotMention(t *testing.T) {
	tests := []struct {
		name    string
		content string
		nick    string
		want    string
	}{
		{"colon prefix", "bot: hello there", "bot", "hello there"},
		{"comma prefix", "bot, help me", "bot", "help me"},
		{"case insensitive", "BOT: hello", "bot", "hello"},
		{"no prefix match", "hello bot", "bot", "hello bot"},
		{"only prefix", "bot:", "bot", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripBotMention(tt.content, tt.nick)
			if got != tt.want {
				t.Errorf("stripBotMention(%q, %q) = %q, want %q", tt.content, tt.nick, got, tt.want)
			}
		})
	}
}
