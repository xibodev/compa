//go:build paused_channels

package irc

import (
	"strings"
	"unicode/utf8"

	"github.com/ergochat/irc-go/ircmsg"

	"github.com/xibodev/compa/v3/pkg/bus"
	"github.com/xibodev/compa/v3/pkg/identity"
)

const (
	// lineLimit is the IRC protocol's line length in bytes, CRLF included.
	lineLimit = 512
	// maxHostLen is the longest host we expect the server to put into our
	// prefix when it relays a line; the prefix counts toward lineLimit.
	maxHostLen = 63
	// minTextBytes keeps splitting possible with absurdly long nicks or
	// targets.
	minTextBytes = 64
)

// foldCase lower-cases an IRC nick or account under the rfc1459 casemapping,
// the protocol default, where []\~ are the upper case of {}|^.
func foldCase(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '[':
			return '{'
		case ']':
			return '}'
		case '\\':
			return '|'
		case '~':
			return '^'
		}
		if 'A' <= r && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, s)
}

// foldAllowList case-folds allow_from entries the way senders are folded, so
// that "Alice", "alice" and "irc:ALICE" name the same user.
func foldAllowList(entries []string) []string {
	folded := make([]string, 0, len(entries))
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		switch {
		case entry == "" || entry == "*":
		case strings.HasPrefix(entry, "@"):
			entry = "@" + foldCase(entry[1:])
		case strings.HasPrefix(strings.ToLower(entry), "irc:"):
			entry = "irc:" + foldCase(entry[len("irc:"):])
		default:
			entry = foldCase(entry)
		}
		folded = append(folded, entry)
	}
	return folded
}

// senderAccount returns the services account a message's sender is logged in
// to, from the IRCv3 account tag, or "" when the sender is not logged in or the
// server does not tag accounts.
func senderAccount(e ircmsg.Message) string {
	present, account := e.GetTag("account")
	if !present || account == "" || account == "*" {
		return ""
	}
	return account
}

// ircSender is who sent a message: the account the sender is logged in to
// when the server says so, which survives nick changes and cannot be taken by
// someone else, else the nick. Both are case-folded.
func ircSender(nick, account string) bus.SenderInfo {
	id := foldCase(nick)
	if account != "" {
		id = foldCase(account)
	}
	return bus.SenderInfo{
		Platform:    "irc",
		PlatformID:  id,
		CanonicalID: identity.BuildCanonicalID("irc", id),
		Username:    foldCase(nick),
		DisplayName: nick,
	}
}

// privmsgTextBudget is how many bytes of text fit into one PRIVMSG to target
// once the server has prefixed it with ":nick!user@host" for the recipients.
func privmsgTextBudget(nick, user, target string) int {
	overhead := len(":") + len(nick) + len("!~") + len(user) + len("@") + maxHostLen +
		len(" PRIVMSG ") + len(target) + len(" :") + len("\r\n")
	return max(lineLimit-overhead, minTextBytes)
}

// sanitizeLine drops the characters a PRIVMSG parameter cannot carry and the
// CTCP delimiter, which would turn text into a client command.
func sanitizeLine(line string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '\r', '\n', '\x00', '\x01':
			return -1
		}
		return r
	}, line)
}

// splitLine splits one line of text into pieces of at most maxBytes bytes,
// breaking at a space when there is one in the second half of a piece and
// never inside a UTF-8 sequence.
func splitLine(line string, maxBytes int) []string {
	var parts []string
	for len(line) > maxBytes {
		cut := maxBytes
		for cut > 0 && !utf8.RuneStart(line[cut]) {
			cut--
		}
		if cut == 0 {
			// Not valid UTF-8 at the start; cut on bytes.
			cut = maxBytes
		}
		if space := strings.LastIndexByte(line[:cut], ' '); space >= cut/2 {
			parts = append(parts, line[:space])
			line = line[space+1:]
			continue
		}
		parts = append(parts, line[:cut])
		line = line[cut:]
	}
	if line != "" {
		parts = append(parts, line)
	}
	return parts
}

// privmsgLines turns message content into the PRIVMSG lines to send: one per
// non-empty line, each split to fit maxBytes.
func privmsgLines(content string, maxBytes int) []string {
	var lines []string
	for _, line := range strings.Split(content, "\n") {
		line = sanitizeLine(line)
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines = append(lines, splitLine(line, maxBytes)...)
	}
	return lines
}
