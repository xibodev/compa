package main

import (
	"strings"
	"testing"
)

// The wordmark prints only on an interactive terminal: the gateway the
// launcher runs writes to a pipe that becomes the dashboard's logs.
func TestStartupBannerOnlyOnAnInteractiveTerminal(t *testing.T) {
	if got := startupBanner(false, false); got != "" {
		t.Fatalf("captured output banner = %q, want none", got)
	}
	if got := startupBanner(false, true); got != "" {
		t.Fatalf("captured plain output banner = %q, want none", got)
	}
	if got := startupBanner(true, true); got != plainBanner || strings.Contains(got, "\033[") {
		t.Fatalf("terminal banner without color = %q", got)
	}
	if got := startupBanner(true, false); got != banner || !strings.Contains(got, colorBlue) {
		t.Fatalf("terminal banner = %q", got)
	}
}
