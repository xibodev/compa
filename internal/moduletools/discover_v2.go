package moduletools

import (
	"fmt"
	"strings"

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

	// Err is set when the descriptor could not be evaluated at all, which is
	// a different failure from a refused contract and is reported as itself.
	Err error
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
// Pinned by test so a future caller cannot reintroduce it.
func evaluateV2(descriptorJSON []byte) V2 {
	dec, err := modprotov2.Evaluate(descriptorJSON)
	if err != nil {
		// Undecodable describe output is not a v2 refusal and must not be
		// reported as one: the remedy for "declare a different contract" is
		// useless to someone whose module emitted malformed JSON.
		return V2{Err: err}
	}

	out := V2{Decision: dec}
	if dec.V2 != nil {
		conf := CheckHostConformance(*dec.V2)
		out.Conformance = &conf
	}
	return out
}

// v2Refusal is why the host refuses this module under its declared contract,
// or "" when it does not. A refused module is not registered: it contributes
// no tools, and the Modules page shows the refusal in its place.
//
// A REFUSAL AND A NON-CONFORMANCE ARE DIFFERENT AND SAY SO. The first means the
// module named a contract this host does not implement; the second means it
// named the right one and then contradicted itself. They need different
// remedies, and collapsing them would send an author to fix the wrong thing.
//
// A v1 module produces nothing here: it is not a defect.
func v2Refusal(v V2) string {
	if v.Err != nil {
		return fmt.Sprintf("its descriptor could not be checked against its declared contract: %v", v.Err)
	}
	if !v.Decision.Served() {
		var parts []string
		for _, s := range []string{v.Decision.Pin.Reason, v.Decision.Pin.Remedy} {
			if s = strings.TrimSpace(s); s != "" {
				parts = append(parts, strings.TrimSuffix(s, "."))
			}
		}
		if len(parts) == 0 {
			return "it declares a module contract this host does not implement"
		}
		return strings.Join(parts, ". ")
	}
	if v.Conformance != nil && !v.Conformance.Conforms() {
		return v.Conformance.Refusal()
	}
	return ""
}
