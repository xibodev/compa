package moduletools

import (
	"strings"
	"testing"

	"github.com/xibodev/compa/v3/pkg/modproto"
)

func toolWithEffects(e modproto.Effects, summary string) *CapabilityTool {
	return &CapabilityTool{
		descriptor: &modproto.Descriptor{Module: "test.module", Version: "1.0.0"},
		capability: modproto.Capability{ID: "test.run", Summary: summary, Effects: e},
	}
}

// The description states what a capability does, and nothing about approval:
// the approval policy decides each call, and the owner is asked when it says
// to, so a model told "needs an approval" only learns to promise less.
func TestDescriptionsStateEffectsWithoutApproval(t *testing.T) {
	for _, e := range []modproto.Effects{
		{Network: true},
		{CostKnown: true, ExternalWrites: true},
		{CostKnown: true, Network: true},
	} {
		got := toolWithEffects(e, "Run a creative tool.").Description()
		for _, banned := range []string{"approv", "Modules page", "consent"} {
			if strings.Contains(got, banned) {
				t.Errorf("%+v: description mentions %q:\n%s", e, banned, got)
			}
		}
	}

	got := toolWithEffects(modproto.Effects{Network: true, Provider: "p"}, "Run a creative tool.").Description()
	for _, want := range []string{"COST UNKNOWN", "reaches the network", "provider p"} {
		if !strings.Contains(got, want) {
			t.Errorf("description does not contain %q:\n%s", want, got)
		}
	}
}

// The agent's summary of a module names its tools and their unknown cost,
// and says nothing about approval either.
func TestSummariesStateCostWithoutApproval(t *testing.T) {
	d := &modproto.Descriptor{Module: "m", Name: "M", Capabilities: []modproto.Capability{
		{ID: "paid", Summary: "Paid.", Effects: modproto.Effects{Network: true}},
		{ID: "writes", Summary: "Writes.", Effects: modproto.Effects{CostKnown: true, ExternalWrites: true}},
	}}
	got := summarize(d, nil)
	if !strings.Contains(got, "m__paid (paid): Paid. [cost unknown]") || strings.Contains(got, "approv") {
		t.Fatalf("summary:\n%s", got)
	}
}

// A capability with a known cost must not carry approval language: telling the
// model a free local tool needs approval discourages the cheap path, which is
// the first line of cost control.
func TestPricedCapabilityCarriesNoApprovalLanguage(t *testing.T) {
	tool := toolWithEffects(
		modproto.Effects{CostKnown: true, Local: true, Provider: "local"},
		"Inventory sessions. Deterministic.",
	)

	got := tool.Description()

	if strings.Contains(got, "approval") || strings.Contains(got, "COST UNKNOWN") {
		t.Fatalf("a known-cost capability was described as needing approval:\n%s", got)
	}
	if !strings.Contains(got, "Inventory sessions") {
		t.Fatalf("the summary was lost:\n%s", got)
	}
}

// Declared effects the model should weigh stay in the description, and the
// module and version are named so a person reading a transcript can trace it.
func TestDescriptionCarriesEffectsAndProvenance(t *testing.T) {
	tool := toolWithEffects(
		modproto.Effects{CostKnown: true, Network: true, ExternalWrites: true, Provider: "openai"},
		"Do a thing.",
	)

	got := tool.Description()

	for _, want := range []string{"reaches the network", "writes files", "provider openai", "test.module", "1.0.0"} {
		if !strings.Contains(got, want) {
			t.Errorf("description does not contain %q:\n%s", want, got)
		}
	}
}
