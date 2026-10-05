package logger

import (
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

// redactedText replaces a secret in a log line.
const redactedText = "[REDACTED]"

const (
	// minSecretLength keeps very short values, which would mask ordinary
	// words, out of the registry.
	minSecretLength = 4
	// maxSecrets bounds the registry; the oldest value goes first.
	maxSecrets = 512
)

var (
	// redactionOff is false by default: every log line is redacted until
	// SetRedaction(false) turns it off.
	redactionOff atomic.Bool

	// urlUserinfo matches the scheme and user info of a URL such as
	// https://user:password@host.
	urlUserinfo = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://)[^\s/?#@"'<>\\]+@`)
	// secretQueryValue matches a query parameter named like a key, token,
	// secret or password, and its name.
	secretQueryValue = regexp.MustCompile(`(?i)([?&][a-z0-9_.\-]*(?:key|token|secret|password|passwd|pwd)=)[^&\s"'#<>\\]+`)
	// botToken matches a Telegram-style bot token, such as the one in
	// /bot<id>:<secret>/ API paths, keeping its public bot id.
	botToken = regexp.MustCompile(`(bot\d+:)[A-Za-z0-9_\-]{10,}`)
)

// secretSet is the registry of values no log line may show.
type secretSet struct {
	mu       sync.Mutex
	values   map[string]struct{}
	order    []string // registration order, for eviction
	replacer atomic.Pointer[strings.Replacer]
}

var secrets = &secretSet{values: map[string]struct{}{}}

// RegisterSecret adds value, such as an API key or a token, to the values
// masked in every log line. Other packages feed it the secrets they load.
// Values shorter than four bytes are ignored.
func RegisterSecret(value string) {
	value = strings.TrimSpace(value)
	if len(value) < minSecretLength {
		return
	}
	secrets.mu.Lock()
	defer secrets.mu.Unlock()
	if _, ok := secrets.values[value]; ok {
		return
	}
	secrets.values[value] = struct{}{}
	secrets.order = append(secrets.order, value)
	if len(secrets.order) > maxSecrets {
		delete(secrets.values, secrets.order[0])
		secrets.order = slices.Delete(secrets.order, 0, 1)
	}
	// Longest first, so a secret that contains another is masked whole.
	values := slices.Clone(secrets.order)
	slices.SortFunc(values, func(a, b string) int {
		if len(a) != len(b) {
			return len(b) - len(a)
		}
		return strings.Compare(a, b)
	})
	pairs := make([]string, 0, 2*len(values))
	for _, secret := range values {
		pairs = append(pairs, secret, redactedText)
	}
	secrets.replacer.Store(strings.NewReplacer(pairs...))
}

// SetRedaction turns log redaction on or off. It is on until turned off.
func SetRedaction(enabled bool) {
	redactionOff.Store(!enabled)
}

func redactionEnabled() bool {
	return !redactionOff.Load()
}

// Redact returns s with what a log line must not show masked: every value
// registered with RegisterSecret, the user info of URLs, the values of query
// parameters named like keys, tokens, secrets and passwords, and the secret
// part of Telegram-style bot tokens. It returns s unchanged while redaction
// is off.
func Redact(s string) string {
	if s == "" || !redactionEnabled() {
		return s
	}
	if replacer := secrets.replacer.Load(); replacer != nil {
		s = replacer.Replace(s)
	}
	if strings.Contains(s, "://") {
		s = urlUserinfo.ReplaceAllString(s, "${1}"+redactedText+"@")
	}
	if strings.Contains(s, "=") && strings.ContainsAny(s, "?&") {
		s = secretQueryValue.ReplaceAllString(s, "${1}"+redactedText)
	}
	if strings.Contains(s, "bot") {
		s = botToken.ReplaceAllString(s, "${1}"+redactedText)
	}
	return s
}
