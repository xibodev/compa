package moduletools

import (
	"strings"
	"testing"

	"github.com/xibodev/compa/pkg/modproto"
)

func toolWithEffects(e modproto.Effects, summary string) *CapabilityTool {
	return &CapabilityTool{
		descriptor: &modproto.Descriptor{Module: "test.module", Version: "1.0.0"},
		capability: modproto.Capability{ID: "test.run", Summary: summary, Effects: e},
	}
}

// The bug this exists for: the description said a paid capability "requires
// human approval", and the model read that as something it could satisfy.
// Asked directly whether "I approve the cost" in chat would let it run a paid
// tool, it reasoned about filling in approved_by and paid_generation_approved.
//
// It cannot. The host strips approval claims arriving through the agent, so a
// consent the model constructs is discarded however the user phrases their
// permission. This is what the model reads while DECIDING -- the failure
// message only arrives after it has already promised the user a result.
func TestUnpricedCapabilitySaysWhoCanApprove(t *testing.T) {
	tool := toolWithEffects(modproto.Effects{Network: true}, "Run a creative tool.")

	got := tool.Description()

	for _, want := range []string{
		"COST UNKNOWN",
		"only the user can give",
		"Modules page",
		"must not construct a consent field",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("description does not contain %q:\n%s", want, got)
		}
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
