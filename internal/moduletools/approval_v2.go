package moduletools

import (
	"github.com/xibodev/compa/pkg/modprotov2"
)

// ApprovalDecision is why a v2 invocation needs approval, or why it does not.
//
// Distinct from the v1 MayBill boolean on purpose. v1 could say only
// "possibly-billing", so a free-but-irreversible step was ungated entirely and
// a cost-checking tool was gated as though it spent money. v2 carries the
// REASON (section 6), and the reason is what a person is actually being asked
// about.
type ApprovalDecision struct {
	Required bool

	// Reasons are drawn from the frozen legal set: chargeable, irreversible,
	// external_write, product_checkpoint.
	Reasons []string
}

// NeedsApprovalV2 decides approval for ONE invocation of a v2 capability,
// against the request that is about to be sent.
//
// THE INPUT MATTERS, AND THAT IS THE POINT. v1's gate read a static boolean, so
// a capability that charges for 12 of 19 kinds was gated for all 19 --
// permanently over-gating the 7 free ones. Section 2a rule 2 evaluates the
// capability's condition against the actual request, so the free kinds stop
// being gated at the layer that acts.
//
// IT READS CAPABILITY EFFECTS, NOT OPERATION EFFECTS (section 2). The Operation
// is where the semantics live; the capability is where the gate reads. That
// ordering is why the no-weakening check exists at all: a capability weaker
// than its Operation would disable this gate while the Operation still read
// correct to a reviewer. So this function assumes conformance has already run,
// and the caller is responsible for that -- see HostConformance.
//
// A capability projecting NO Operation is still gated. A registry read
// transforms no product material and is still invocable, so its declared
// effects are still the thing the host acts on.
func NeedsApprovalV2(
	d *modprotov2.Descriptor,
	c *modprotov2.Capability,
	input map[string]any,
) ApprovalDecision {
	if c == nil {
		// An unknown capability is refused by requiring approval rather than
		// waved through. The alternative -- returning "not required" for
		// something the host cannot find -- makes a typo in a capability ID
		// indistinguishable from a genuinely free operation.
		return ApprovalDecision{Required: true, Reasons: []string{"unknown_capability"}}
	}

	var reasons []string

	// CHARGEABILITY, evaluated per invocation. Note this is may_charge and NOT
	// cost_known: the two are independent, and reading cost_known as a spending
	// signal is the v1 proxy this replaces. A capability may know its price
	// exactly and still charge; another may have no number and never bill.
	if c.Effects.MayCharge.Evaluate(input) {
		reasons = append(reasons, "chargeable")
	}

	// EXTERNAL WRITES, which v1 declared and never gated on. An external write
	// cannot be un-done by an error, so it is not recoverable by retrying --
	// which is precisely the case section 6 was written for.
	if c.Effects.ExternalWrites {
		reasons = append(reasons, "external_write")
	}

	// Reasons the OPERATIONS declare that the capability's effects cannot
	// carry. `irreversible` and `product_checkpoint` are product statements,
	// not effects, so they have no effect field to read -- they are only ever
	// declared on an Operation.
	//
	// This is the one place the gate consults Operation-level declarations, and
	// it is not a contradiction of "the gate reads capability effects": those
	// two reasons are not effects. Taking the UNION across projected Operations
	// is deliberate -- if any Operation reachable through this capability is
	// irreversible, the invocation may be irreversible.
	seen := map[string]bool{}
	for _, r := range reasons {
		seen[r] = true
	}
	for _, opID := range c.Projects {
		for i := range d.Operations {
			if d.Operations[i].ID != opID {
				continue
			}
			for _, r := range d.Operations[i].Approval.RequiredWhen {
				if r != "irreversible" && r != "product_checkpoint" {
					continue // chargeable/external_write come from effects above
				}
				if !seen[r] {
					seen[r] = true
					reasons = append(reasons, r)
				}
			}
		}
	}

	return ApprovalDecision{Required: len(reasons) > 0, Reasons: reasons}
}

// ApprovalSatisfied reports whether an approval record covers a decision.
//
// EVERY reason must be covered. A record granting "chargeable" does not
// authorise an irreversible step: the person approved a charge, and approving
// one reason is not approving another they were never shown. Treating any
// approval as blanket is how a gate with reasons becomes a gate with one bit
// again.
func ApprovalSatisfied(dec ApprovalDecision, rec *modprotov2.ApprovalRecord) bool {
	if !dec.Required {
		return true
	}
	if rec == nil || !rec.Granted {
		return false
	}
	// An approval minted by a model is not an approval. The host strips these
	// before this point; refusing here too means a stripped-check regression
	// fails closed rather than silently granting.
	if rec.GrantedBy == "model" || rec.GrantedBy == "" {
		return false
	}
	for _, need := range dec.Reasons {
		if rec.Reason != need {
			return false
		}
	}
	return true
}
