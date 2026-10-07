package mcp

import (
	"bytes"
	"strings"
	"sync"

	"github.com/xibodev/compa/v3/pkg/logger"
)

const (
	// maxStderrTailBytes is how much of a server's latest error output is
	// kept, to explain a server that fails.
	maxStderrTailBytes = 4096
	// maxStderrSuffixChars bounds the error output quoted in an error.
	maxStderrSuffixChars = 1000
	// maxStderrLineBytes bounds one logged line; longer output is logged
	// in pieces.
	maxStderrLineBytes = 1024
)

// stderrTail receives a stdio server's error output: it logs each line at
// debug level and keeps the latest output for error messages. Servers write
// their startup failures there, and before this they were discarded.
type stderrTail struct {
	server string

	mu      sync.Mutex
	tail    []byte
	pending []byte
}

func newStderrTail(server string) *stderrTail {
	return &stderrTail{server: server}
}

func (s *stderrTail) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.tail = append(s.tail, p...)
	if over := len(s.tail) - maxStderrTailBytes; over > 0 {
		s.tail = append([]byte(nil), s.tail[over:]...)
	}

	s.pending = append(s.pending, p...)
	for {
		line, rest, found := bytes.Cut(s.pending, []byte{'\n'})
		if !found {
			break
		}
		s.logLine(line)
		s.pending = rest
	}
	if len(s.pending) > maxStderrLineBytes {
		s.logLine(s.pending)
		s.pending = nil
	}
	s.pending = append([]byte(nil), s.pending...)
	return len(p), nil
}

func (s *stderrTail) logLine(line []byte) {
	text := strings.TrimRight(strings.ToValidUTF8(string(line), "\uFFFD"), "\r")
	if strings.TrimSpace(text) == "" {
		return
	}
	logger.DebugCF("mcp", "MCP server stderr", map[string]any{"server": s.server, "line": text})
}

// String returns the latest error output, trimmed. It is "" for a nil tail.
func (s *stderrTail) String() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.TrimSpace(strings.ToValidUTF8(string(s.tail), "\uFFFD"))
}

// suffix quotes the end of the latest error output for an error message,
// or returns "" when there is none.
func (s *stderrTail) suffix() string {
	text := s.String()
	if text == "" {
		return ""
	}
	if runes := []rune(text); len(runes) > maxStderrSuffixChars {
		text = "..." + string(runes[len(runes)-maxStderrSuffixChars:])
	}
	return "; server stderr: " + text
}
