package moduletools

import (
	"github.com/xibodev/compa/pkg/modprotov2"
)

// V2 is a discovered module's v2 standing: which contract governs it, and
// whether it conforms.
//
// Present on every Installed, including v1 modules and refusals, because the
// alternative -- a nil field meaning "v1, probably" -- makes three outcomes
// share one observable. A caller must be able to tell "no v2 declaration" from
// "declared something this host refuses" from "not evaluated at all".
type V2 struct {
	// Decision carries the pin outcome. Its Pin.Reason and Pin.Remedy explain a
	// refusal to a person.
	Decision modprotov2.Decision

	// Conformance is the host's no-weakening verdict, and is only populated
	// when the pin returned v2. There is nothing to check on a v1 module: its
	// descriptor has no Operation layer to compare a capability against.
	Conformance *HostConformance
}

// MayRelyOnV2 reports whether this module's v2 declarations may be acted upon.
//
// BOTH conditions, and the conjunction is the point. Passing the pin means the
// module claims v2; conforming means its projection does not weaken what it
// claims. A module that passes the pin and fails conformance is MORE dangerous
// than a v1 module, not less: it has been admitted to the v2 path where the
// gate reads capability effects, while declaring capability effects weaker than
// its own Operations.
func (v V2) MayRelyOnV2() bool {
	return v.Decision.MayRelyOnV2() && v.Conformance != nil && v.Conformance.Conforms()
}

// evaluateV2 runs the contract gate and host conformance for one described
// module.
//
// TAKES THE DESCRIPTOR BYTES, NOT THE ENVELOPE, and that distinction is a real
// trap rather than a detail. `module describe --json` emits an envelope whose
// `result` field holds the descriptor, so contract_version sits NESTED. Passing
// raw stdout to Evaluate returns v1 for EVERY module -- silently, because a
// missing contract_version is a legitimate answer meaning "this is a v1
// module". The wrong input and a correct v1 module produce the same outcome.
//
// The sibling lane hit this on their first probe against this gate and reported
// it: they nearly concluded "no problem, we interoperate" from a v1 result that
// was an artifact of their test input. Pinned by test so a future caller cannot
// reintroduce it.
func evaluateV2(descriptorJSON []byte) V2 {
	dec, err := modprotov2.Evaluate(descriptorJSON)
	if err != nil {
		// Undecodable describe output is not a v2 refusal and must not be
		// reported as one: the remedy for "declare a different contract" is
		// useless to someone whose module emitted malformed JSON.
		return V2{Decision: modprotov2.Decision{}}
	}

	out := V2{Decision: dec}
	if dec.V2 != nil {
		conf := CheckHostConformance(*dec.V2)
		out.Conformance = &conf
	}
	return out
}

// v2Warnings renders the host's v2 findings for the Modules page.
//
// A REFUSAL AND A NON-CONFORMANCE ARE DIFFERENT AND SAY SO. The first means the
// module named a contract this host does not implement; the second means it
// named the right one and then contradicted itself. They need different
// remedies, and collapsing them would send an author to fix the wrong thing --
// the failure mode the reason/remedy split exists to prevent.
//
// A v1 module produces NOTHING here. It is not a defect, and a warning on every
// v1 module would train people to ignore the panel that also reports real ones.
func v2Warnings(v V2) []string {
	if !v.Decision.Served() {
		return []string{v.Decision.Pin.Reason + ". " + v.Decision.Pin.Remedy}
	}
	if v.Conformance != nil && !v.Conformance.Conforms() {
		return []string{v.Conformance.Refusal()}
	}
	return nil
}
