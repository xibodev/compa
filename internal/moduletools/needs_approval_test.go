package moduletools

import (
	"testing"

	"github.com/xibodev/compa/pkg/modproto"
)

// NeedsApproval is the ONE place the approval policy is composed, and these
// pin the composition rather than restate it.
//
// The reason this exists: five decisions in this codebase were duplicated
// across the CLI, the agent and the cockpit, and every one that diverged
// produced a bug. The two approval gates had not diverged yet -- they both
// called MayBillUnder and then each rebuilt "and nobody approved" separately.
// Sharing it is what keeps a later fix from landing on one caller only.

func unpriced() modproto.Capability {
	return modproto.Capability{ID: "x.paid", Effects: modproto.Effects{CostKnown: false}}
}

func priced() modproto.Capability {
	return modproto.Capability{ID: "x.free", Effects: modproto.Effects{CostKnown: true}}
}

// The whole point of the gate: something that may bill, that nobody approved,
// is refused BEFORE it runs.
func TestSomethingThatMayBillAndIsUnapprovedNeedsApproval(t *testing.T) {
	if !NeedsApproval(nil, unpriced(), false) {
		t.Fatal("an unapproved capability that may bill was allowed to run")
	}
}

// Approval is what lifts the gate. Both callers supply this differently -- the
// agent from the operator's standing decision, the cockpit from a button press
// -- and the composition must treat them identically.
func TestApprovalLiftsTheGateWhoeverSuppliedIt(t *testing.T) {
	if NeedsApproval(nil, unpriced(), true) {
		t.Fatal("an approved capability was still gated, so approving does nothing")
	}
}

// A capability declaring a KNOWN cost is not gated. A gate that stopped free
// work would train the operator to click through the ones that matter.
func TestAPricedCapabilityIsNotGated(t *testing.T) {
	if NeedsApproval(nil, priced(), false) {
		t.Fatal("a capability declaring a known cost was gated")
	}
}

// The gate must key on chargeability, not on approval alone: approving
// something free must not be required, and NOT approving something free must
// not refuse it.
func TestTheGateKeysOnTheCapabilityNotOnlyOnApproval(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cap      modproto.Capability
		approved bool
		want     bool
	}{
		{"unpriced, unapproved", unpriced(), false, true},
		{"unpriced, approved", unpriced(), true, false},
		{"priced, unapproved", priced(), false, false},
		{"priced, approved", priced(), true, false},
	} {
		if got := NeedsApproval(nil, tc.cap, tc.approved); got != tc.want {
			t.Errorf("%s: NeedsApproval = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Consolidation must not have changed the answer. NeedsApproval has to agree
// with the expression both callers used before it existed, for every
// combination -- otherwise "preserve behaviour exactly" is a claim rather than
// a fact.
func TestConsolidationPreservedTheOriginalBehaviourExactly(t *testing.T) {
	d := &modproto.Descriptor{Module: "m"}
	for _, c := range []modproto.Capability{unpriced(), priced()} {
		for _, approved := range []bool{true, false} {
			// The agent path's original expression.
			agentBefore := MayBillUnder(d, c) && !approved
			// The cockpit path's original expression, negated to match sense:
			// it returned nil (no gate) when `approved || !MayBillUnder`.
			cockpitBefore := !(approved || !MayBillUnder(d, c))

			got := NeedsApproval(d, c, approved)
			if got != agentBefore {
				t.Errorf("%s approved=%v: differs from the agent path's old"+
					" behaviour: got %v, was %v", c.ID, approved, got, agentBefore)
			}
			if got != cockpitBefore {
				t.Errorf("%s approved=%v: differs from the cockpit path's old"+
					" behaviour: got %v, was %v", c.ID, approved, got, cockpitBefore)
			}
		}
	}
}

// A nil descriptor must not panic. The cockpit calls approvalGate with a
// capability looked up by name, and an undeclared name yields a zero value --
// which gates, because the host cannot read the effects of something it was
// never told about.
func TestAnUndeclaredCapabilityGatesRatherThanPanicking(t *testing.T) {
	var zero modproto.Capability
	if !NeedsApproval(nil, zero, false) {
		t.Fatal("a capability the descriptor never declared was allowed to run")
	}
}
