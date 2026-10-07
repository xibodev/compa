package modproto

import (
	"strings"
	"testing"
)

func capWithProvider(declared string) *Descriptor {
	d := &Descriptor{Module: "test.module"}
	c := Capability{ID: "test.run"}
	c.Effects.Provider = declared
	c.Effects.CostKnown = true
	d.Capabilities = []Capability{c}
	return d
}

// The provider a call reports is shown to the operator on the Modules page,
// beside the cost, and it is how a person tells "this ran locally" from "this
// reached a paid service". It was never compared to what the capability
// declared, so a capability declaring "local" could report one that bills and
// the page would print it as fact.
func TestReportedProviderIsComparedToTheDeclaredOne(t *testing.T) {
	d := capWithProvider("local")

	warnings, err := CheckExecutionAgainstDeclared(d, "test.run", &Execution{
		Local: true, Provider: "openai",
	})
	if err != nil {
		t.Fatalf("a provider mismatch must warn, not refuse: %v", err)
	}
	if len(warnings) == 0 {
		t.Fatal("a capability declaring provider local reported openai with no warning")
	}
	joined := ""
	for _, w := range warnings {
		joined += w.Error() + " "
	}
	if !strings.Contains(joined, "openai") || !strings.Contains(joined, "local") {
		t.Fatalf("the warning does not name both providers:\n%s", joined)
	}
}

// The honest cases must stay silent, or the page fills with warnings nobody
// reads -- which is how a real one gets missed.
func TestHonestProviderReportingIsSilent(t *testing.T) {
	cases := []struct {
		declared, reported, why string
	}{
		{"openai", "openai", "matching providers"},
		{"openai", "OpenAI", "case differences are not a contradiction"},
		{"openai", "local", "running locally is never a contradiction"},
		{"openai", "", "an unreported provider is not a claim"},
		{"", "openai", "a capability declaring none cannot contradict itself"},
	}

	for _, c := range cases {
		w, err := CheckExecutionAgainstDeclared(capWithProvider(c.declared), "test.run",
			&Execution{Local: true, Provider: c.reported})
		if err != nil {
			t.Errorf("%s: unexpected error %v", c.why, err)
		}
		for _, warning := range w {
			if strings.Contains(warning.Error(), "provider") {
				t.Errorf("%s: warned anyway -- %s", c.why, warning.Error())
			}
		}
	}
}
