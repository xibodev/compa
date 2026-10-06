package modprotov2

import (
	"encoding/json"
	"fmt"

	"github.com/xibodev/compa/v2/pkg/contractv2"
	"github.com/xibodev/compa/v2/pkg/modproto"
)

// Accepted is a descriptor whose contract pin PASSED. It is the only way to
// obtain v2 semantics from this package.
//
// THE ORDERING IS STRUCTURAL, NOT DOCUMENTED. The operator's requirement is
// that the contract gate execute before the host relies on any v2 semantic
// declaration. A comment saying "call CheckPin first" is exactly the kind of
// prose guarantee this contract exists to stop shipping: it holds until someone
// adds a call site that does not read it.
//
// So the v2 descriptor is unexported inside this type and reachable only
// through a constructor that runs the pin. A caller CANNOT hold v2 Operations
// without having passed the gate, because there is no other way to build one.
type Accepted struct {
	d *Descriptor
}

// Descriptor returns the accepted v2 descriptor. Reaching this value at all
// proves the pin passed.
func (a Accepted) Descriptor() *Descriptor { return a.d }

// Decision is what a host may do with a counterparty, and it carries the
// three-outcome evaluation rather than a boolean.
//
// The three states are EVALUATOR state, never transmitted. The wire carries
// exactly one value -- contract_version -- and the
// outcome is what the host CONCLUDES. A transmitted "state" field would let a
// module assert its own evaluation, which is the thing the evaluation exists to
// decide.
type Decision struct {
	Pin contractv2.PinResult

	// V2 is set only when the pin matched exactly.
	V2 *Accepted

	// V1 is set only when NO contract version was declared. That module is
	// served under v1 with v1 behaviour and NO v2 guarantee.
	V1 *modproto.Descriptor
}

// MayRelyOnV2 reports whether v2 semantics are available. Delegates to the pin
// rather than re-deriving, so the two cannot disagree.
func (d Decision) MayRelyOnV2() bool { return d.Pin.MayRelyOnV2() }

// Served reports whether the host may talk to this module at all.
func (d Decision) Served() bool { return d.Pin.Served() }

// Evaluate is THE entry point for a described module, and the only one.
//
// It reads contract_version from the raw describe output, runs the pin, and
// returns a Decision whose shape makes the version matrix inescapable:
//
//	absent                -> V1 set, V2 nil       served, no v2 guarantee
//	xibodev.module/v2     -> V2 set, V1 nil       served, v2 semantics
//	xibodev.module/v1     -> both nil             REFUSED (wire identity)
//	anything else         -> both nil             REFUSED
//
// No negotiation, no downgrade, no fallback from a wrong explicit value. A
// caller that wants v2 must check V2 != nil; there is no way to obtain the v2
// descriptor from a refusal because the field is simply not set.
func Evaluate(describeOutput []byte) (Decision, error) {
	// Read ONLY the contract field first. The rest of the document is not
	// interpreted until the gate has ruled on which contract governs it --
	// which is what "the gate executes before the host relies on any v2
	// semantic declaration" means operationally.
	var probe struct {
		ContractVersion string `json:"contract_version"`
	}
	if err := json.Unmarshal(describeOutput, &probe); err != nil {
		return Decision{}, fmt.Errorf("describe output is not JSON: %w", err)
	}

	pin := contractv2.CheckPin(probe.ContractVersion)
	dec := Decision{Pin: pin}

	switch pin.Outcome {
	case contractv2.OutcomeV2:
		var d Descriptor
		if err := json.Unmarshal(describeOutput, &d); err != nil {
			return Decision{}, fmt.Errorf("v2 descriptor did not decode: %w", err)
		}
		dec.V2 = &Accepted{d: &d}

	case contractv2.OutcomeV1:
		// A module declaring nothing is a v1 module. It is decoded with the v1
		// types -- not the v2 ones with fields left zero -- because a
		// half-populated v2 struct is exactly the "more permissive default"
		// §10 forbids: absent v2 fields must fall back to v1 BEHAVIOUR, and a
		// zero-valued v2 Effects would read as an affirmative declaration of
		// no network, no charge, and determinism.
		var d modproto.Descriptor
		if err := json.Unmarshal(describeOutput, &d); err != nil {
			return Decision{}, fmt.Errorf("v1 descriptor did not decode: %w", err)
		}
		dec.V1 = &d

	default:
		// Refused. Both fields stay nil, so no semantics of either contract
		// are reachable.
	}
	return dec, nil
}
