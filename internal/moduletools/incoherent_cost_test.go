package moduletools

import (
	"strings"
	"testing"

	"github.com/xibodev/compa/pkg/modproto"
)

// The bug this exists for: the approval gate I shipped an hour ago broke
// journey B.
//
// A rendering module declares creative.tools.estimate as cost_known:false while
// ALSO declaring local=true, network=false, provider="local". Those cannot all
// be true: a capability that reaches no network and no provider has nothing to
// bill through. Its own summary says "never bills", and the host already
// warned about the contradiction.
//
// So the gate refused a free, local capability, and an operator asking for a
// video in chat was told to go and approve a cost-estimation tool. Correct by
// the letter of the declaration, and wrong.
//
// The host now decides on STRUCTURE rather than prose. Prose is not a contract
// -- "never bills" is a sentence -- but local && !network && provider in
// {"", "local"} is the module's own machine-readable statement that nothing
// leaves the box.
// An unknown cost gates, whatever else the capability declares.
//
// Two structural rules were tried and both failed. Treating local &&
// !network as free let a notes module's content.produce through ungated -- it
// spends by shelling out to an AI CLI, which the module's own effects never
// mention. Adding "the module declares subprocess binaries" re-gated that and
// also gated the rendering module's creative.tools.estimate, because it
// declares ffmpeg and ffprobe, neither of which can charge anything.
//
// The cost of guessing wrong is asymmetric, and that decides it: a wrongly
// gated free capability is an extra click; a wrongly ungated paid one spends
// the user's money without asking.
func TestAnUnknownCostAlwaysGates(t *testing.T) {
	for _, c := range []modproto.Capability{
		{ID: "reaches.net", Effects: modproto.Effects{CostKnown: false, Network: true}},
		{ID: "looks.local", Effects: modproto.Effects{CostKnown: false, Local: true, Provider: "local"}},
		{ID: "shells.out", Effects: modproto.Effects{CostKnown: false, Local: true}},
	} {
		if !MayBill(c) {
			t.Errorf("%s declares an unknown cost and was not gated", c.ID)
		}
	}
}

// A known cost never gates. Free is free, and gating it would train the
// operator to click through the approvals that matter.
func TestAKnownCostNeverGates(t *testing.T) {
	for _, c := range []modproto.Capability{
		{ID: "free.local", Effects: modproto.Effects{CostKnown: true, Local: true}},
		{ID: "free.net", Effects: modproto.Effects{CostKnown: true, Network: true}},
		{ID: "free.provider", Effects: modproto.Effects{CostKnown: true, Provider: "openai"}},
	} {
		if MayBill(c) {
			t.Errorf("%s declares a known cost and was gated", c.ID)
		}
	}
}

// The descriptor-aware form must agree with the plain one. They differ only in
// signature, so a future signal can be added in one place.
func TestBothFormsAgree(t *testing.T) {
	d := &modproto.Descriptor{Module: "m"}
	d.Permissions.Subprocess = []string{"assistant-cli"}
	for _, c := range []modproto.Capability{
		{ID: "a", Effects: modproto.Effects{CostKnown: false, Local: true}},
		{ID: "b", Effects: modproto.Effects{CostKnown: true, Network: true}},
	} {
		if MayBill(c) != MayBillUnder(d, c) {
			t.Errorf("%s: the two forms disagree", c.ID)
		}
	}
}

// The disagreement is still REPORTED, but as a HOST limitation rather than a
// module defect.
//
// This test used to assert the module "should fix its declaration". Under the
// settled meaning of cost_known that is wrong: the field reports whether an
// AMOUNT is known, so a capability whose amount is unknowable and which never
// bills is declaring both facts correctly. v1 simply cannot express
// chargeability separately. The warning must not send an author to correct
// something correct.
func TestTheDisagreementIsStillReportedAsAHostLimitation(t *testing.T) {
	d := &modproto.Descriptor{Capabilities: []modproto.Capability{{
		ID:      "creative.tools.estimate",
		Summary: "Estimate cost. Never bills.",
		Effects: modproto.Effects{CostKnown: false, Local: true, Provider: "local"},
	}}}

	warnings := costClaimWarnings(d)

	if len(warnings) == 0 {
		t.Fatal("the host stopped surfacing that it gates a capability whose" +
			" own summary says it never bills, so nobody learns why the" +
			" approval prompt appears")
	}
	joined := strings.Join(warnings, " ")
	if !strings.Contains(joined, "creative.tools.estimate") {
		t.Errorf("the warning does not name the capability: %v", warnings)
	}
	// It must not accuse the author of a defect that does not exist.
	if strings.Contains(joined, "one of the two is wrong") {
		t.Errorf("the warning still claims the module contradicts itself, which"+
			" sends the author to correct a correct declaration: %v", warnings)
	}
	if !strings.Contains(joined, "Both can be true") {
		t.Errorf("the warning does not say both declarations can be true, so it"+
			" reads as a module defect rather than a v1 gap: %v", warnings)
	}
}
