package cliui

import (
	"strings"
	"testing"
)

// Onboarding points to `auth login`, not config.json (EV-27).
func TestOnboardingStepsText(t *testing.T) {
	text := buildOnboardingSteps("/home/u/.compa/config.json") + recommendedBlock()
	for _, bad := range []string{"Add your API key to", "17+"} {
		if strings.Contains(text, bad) {
			t.Errorf("text still says %q:\n%s", bad, text)
		}
	}
	if !strings.Contains(text, "auth login") {
		t.Errorf("no auth login step:\n%s", text)
	}
}
