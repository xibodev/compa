package modprotov2

import (
	"fmt"
	"sort"
)

// Finding is one conformance violation, carrying reason AND remedy for the
// same reason Error does: a refusal that says only what is wrong sends the
// reader to source.
type Finding struct {
	Code   string
	Where  string
	Reason string
	Remedy string
}

func (f Finding) String() string {
	return fmt.Sprintf("%s [%s]: %s. %s", f.Code, f.Where, f.Reason, f.Remedy)
}

// CheckNoWeakening closes the triangle:
//
//	product semantic declaration -> v2 projection -> host interpretation
//
// THIS IS THE ONE RULE WHOSE VIOLATION IS INVISIBLE AT THE LAYER BEING
// REVIEWED (§9). The Operation reads correct, while the gate -- which reads
// CAPABILITY effects -- fires on nothing. A reviewer looking at the Operation
// sees honest semantics and no problem. So a rule against a silent failure
// that was itself only checked by reading would be the same defect one level
// up, which is why this is mechanical.
//
// It compares field by field in the SAME SHAPE at both layers. Different
// shapes would make this a translation, and a translation is where a weakening
// hides.
func CheckNoWeakening(d *Descriptor) []Finding {
	byID := map[string]*Operation{}
	for i := range d.Operations {
		byID[d.Operations[i].ID] = &d.Operations[i]
	}

	var out []Finding
	for _, c := range d.Capabilities {
		for _, opID := range c.Projects {
			op, ok := byID[opID]
			if !ok {
				out = append(out, Finding{
					Code:  "unknown_operation",
					Where: fmt.Sprintf("capability %q", c.ID),
					Reason: fmt.Sprintf("projects Operation %q, which this"+
						" descriptor does not declare", opID),
					Remedy: "declare the Operation, or remove it from projects." +
						" A projection to an undeclared Operation cannot be" +
						" checked for weakening, so it is refused rather than" +
						" skipped",
				})
				continue
			}
			out = append(out, compareEffects(c.ID, op, c.Effects)...)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Where < out[j].Where })
	return out
}

// compareEffects is the field-by-field no-weakening comparison.
//
// The direction matters and is easy to invert: a capability may declare
// something STRONGER than its Operation (pessimism is always safe), never
// weaker. So the failure is "Operation says true, capability says false".
func compareEffects(capID string, op *Operation, cap Effects) []Finding {
	where := fmt.Sprintf("capability %q -> operation %q", capID, op.ID)
	var out []Finding

	if op.Effects.Network && !cap.Network {
		out = append(out, Finding{
			Code:   "effect_understated",
			Where:  where,
			Reason: "the Operation reaches the network and the capability declares it does not",
			Remedy: "declare network:true on the capability. A projection may be" +
				" more pessimistic than its Operation, never less: the host" +
				" gates on CAPABILITY effects, so a weaker declaration here" +
				" disables the gate while the Operation still reads correct",
		})
	}
	if op.Effects.ExternalWrites && !cap.ExternalWrites {
		out = append(out, Finding{
			Code:   "effect_understated",
			Where:  where,
			Reason: "the Operation writes outside the host and the capability declares it does not",
			Remedy: "declare external_writes:true on the capability. An external" +
				" write cannot be un-done by an error, so an ungated one is not" +
				" recoverable by retrying",
		})
	}

	// §2a rule 3: compare CONDITIONALS, not their collapses.
	if !cap.MayCharge.NoWeakerThan(op.Effects.MayCharge) {
		out = append(out, Finding{
			Code:  "may_charge_weakened",
			Where: where,
			Reason: "the capability does not charge for every input the Operation" +
				" charges for, so an invocation could spend money through a" +
				" surface declaring it cannot",
			Remedy: "widen the capability's may_charge until it covers the" +
				" Operation's, or collapse it to true. Boolean true is the" +
				" maximal condition and always satisfies this; collapsing is" +
				" legal and costs precision, so it is a choice rather than a" +
				" consequence",
		})
	}

	// §4: a determinism claim is falsified by its own siblings. Checked at the
	// capability layer because that is what a host reads, and separately per
	// layer in Validate.
	if cap.Deterministic && !op.Effects.Deterministic {
		out = append(out, Finding{
			Code:  "determinism_overstated",
			Where: where,
			Reason: "the capability claims determinism the Operation does not" +
				" claim, so re-execution is being offered as free recovery when" +
				" the product does not guarantee it",
			Remedy: "drop deterministic on the capability. Determinism is the" +
				" property that makes re-execution free recovery, so overstating" +
				" it converts a retry into a second charge or a different result",
		})
	}
	return out
}
