package modprotov2

import "encoding/json"

// Effects is the declared effect profile, used at BOTH layers in the SAME
// SHAPE (§2). Identical shape is what makes the no-weakening comparison
// mechanical; different shapes at the two layers would make it a translation,
// and a translation is where a weakening hides.
type Effects struct {
	Network        bool   `json:"network"`
	ExternalWrites bool   `json:"external_writes"`
	Provider       string `json:"provider"`

	// MayCharge is chargeability, INDEPENDENT of whether an amount is known.
	//
	// THE GAP THIS CLOSES. v1 had only cost_known, which means "is a number
	// known", and this host gated on it as a proxy for spending. The two are
	// genuinely independent: one of a rendering module's tools declares
	// cost_known TRUE and network TRUE and requires no consent, and another
	// module's estimate capability -- whose entire purpose is checking cost
	// BEFORE a paid run -- declared cost_known false while its own summary said
	// it never bills. Gating the cost-checking tool as possibly-billing
	// discourages the one behaviour that makes a cost gate work.
	MayCharge MayCharge `json:"may_charge"`

	// CostKnown declares whether a NUMBER is known. Never a proxy for
	// spending; that is what MayCharge is for.
	CostKnown bool `json:"cost_known"`

	// Deterministic: same input, same bytes (§4).
	//
	// MUST be false if Network or MayCharge could be true. Enforced in
	// Validate rather than documented, because a determinism claim that is
	// merely asserted is the kind of guarantee this contract exists to stop
	// shipping as prose.
	Deterministic bool `json:"deterministic"`
}

// MayCharge is either a plain bool or an argument-conditional form (§3a).
//
// WHY CONDITIONAL AT ALL. A notes module's produce_content charges for 12 of
// 19 kinds, resolved from a runtime registry. Without this, expressing that
// honestly would force 19 Operations onto the wire -- a delivery detail
// becoming product semantics, which final ruling 10 forbids -- or permanently
// over-gate 7 free kinds.
//
// AND WHY IT IS LEGAL AT THE CAPABILITY LAYER TOO (§2a). The first draft put
// the conditional on the Operation and a collapsed boolean on the capability.
// For kind=retrieval_pack that yields TWO DECLARED ANSWERS FOR ONE INVOCATION:
// the Operation evaluates false, the capability reads true, and the measured
// truth is false. Worse, since the gate reads capability effects, §3a became
// expressible where nothing reads it and unreachable where something does.
type MayCharge struct {
	// Always is the plain boolean form. When Field is empty this is the whole
	// declaration.
	Always bool

	// Field names a request field to evaluate. Empty means unconditional.
	Field string
	// WhenIn lists values of Field for which this charges.
	WhenIn []string
	// Default is the answer for a value not in WhenIn, or when the field
	// cannot be resolved.
	//
	// §3a requires this to be TRUE for an unlisted or unresolvable value, and
	// Validate enforces it. A module author verified the sequencing rather than
	// assuming it: content.produce REFUSES a request with no kind, so an
	// unevaluable condition resolves true, approval is sought, the request
	// arrives, and the module refuses deterministically -- over-gate then
	// refusal, which is annoying and strictly safe. The opposite default would
	// send an UNAPPROVED request whose kind might have been chargeable.
	Default bool
}

// IsConditional reports whether this declaration depends on a request field.
func (m MayCharge) IsConditional() bool { return m.Field != "" }

// Evaluate answers the chargeability question for ONE invocation, against the
// request the host is about to send (§2a rule 2).
//
// An unevaluable condition is TRUE (§2a rule 5): a missing field, an
// unresolvable value, or a non-string value all mean charge. The safe direction
// is the one that asks a human about something free, never the one that spends
// without asking.
func (m MayCharge) Evaluate(input map[string]any) bool {
	if !m.IsConditional() {
		return m.Always
	}
	raw, present := input[m.Field]
	if !present {
		return true
	}
	s, ok := raw.(string)
	if !ok {
		return true
	}
	for _, v := range m.WhenIn {
		if v == s {
			return true
		}
	}
	return m.Default
}

// NoWeakerThan reports whether m charges for at least every input that other
// charges for (§2a rule 3).
//
// COMPARES CONDITIONALS, NOT THEIR COLLAPSES. A boolean true is the maximal
// condition and always satisfies this; a boolean false never does against a
// charging counterpart. Collapsing to true stays legal (§2a rule 4) -- a
// module author asked for that as a STAGING option, since a capability-layer
// conditional needs matching evaluation semantics on both sides and that is a
// thing to implement after freeze rather than assume during review.
func (m MayCharge) NoWeakerThan(other MayCharge) bool {
	// Unconditional true is maximal: it charges for everything.
	if !m.IsConditional() && m.Always {
		return true
	}
	if !other.IsConditional() {
		if !other.Always {
			return true // other charges for nothing; anything is no weaker
		}
		// other charges for everything; only an unconditional true (handled
		// above) can match that.
		return false
	}
	// Both sides conditional. m must charge wherever other does.
	if !m.IsConditional() {
		// m is unconditional false against a conditional other that charges
		// somewhere.
		return len(other.WhenIn) == 0 && !other.Default
	}
	if m.Field != other.Field {
		// Different fields cannot be compared pointwise. Refuse to certify
		// rather than guess: an unprovable no-weakening claim must not pass as
		// a proven one.
		return false
	}
	for _, v := range other.WhenIn {
		if !m.chargesFor(v) {
			return false
		}
	}
	// Unlisted values fall to Default on both sides.
	return !other.Default || m.Default
}

func (m MayCharge) chargesFor(value string) bool {
	for _, v := range m.WhenIn {
		if v == value {
			return true
		}
	}
	return m.Default
}

// MarshalJSON emits the plain boolean when unconditional and the object form
// otherwise, which is what the frozen shape shows.
func (m MayCharge) MarshalJSON() ([]byte, error) {
	if !m.IsConditional() {
		return json.Marshal(m.Always)
	}
	return json.Marshal(struct {
		Field   string   `json:"field"`
		WhenIn  []string `json:"when_in"`
		Default bool     `json:"default"`
	}{m.Field, m.WhenIn, m.Default})
}

// UnmarshalJSON accepts both forms.
func (m *MayCharge) UnmarshalJSON(b []byte) error {
	var asBool bool
	if err := json.Unmarshal(b, &asBool); err == nil {
		*m = MayCharge{Always: asBool}
		return nil
	}
	var obj struct {
		Field   string   `json:"field"`
		WhenIn  []string `json:"when_in"`
		Default bool     `json:"default"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return err
	}
	*m = MayCharge{Field: obj.Field, WhenIn: obj.WhenIn, Default: obj.Default}
	return nil
}
