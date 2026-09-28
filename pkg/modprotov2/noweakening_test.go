package modprotov2

import (
	"strings"
	"testing"
)

// A descriptor modelled on a real module's projection: one Operation charging
// for a subset of kinds, surfaced by one capability, plus a capability that
// projects NOTHING (a registry read).
func archiveLike() *Descriptor {
	charging := MayCharge{
		Field:   "kind",
		WhenIn:  []string{"tutorial", "adr", "slides"},
		Default: true,
	}
	return &Descriptor{
		Protocol:        "xibodev.module/v2",
		ContractVersion: "xibodev.module/v2",
		Module:          "archive",
		Operations: []Operation{{
			ID:      "produce_content",
			Effects: Effects{MayCharge: charging},
			Requirements: []Requirement{
				{Kind: "binary", Name: "d2", Strength: StrengthPreferred},
				{Kind: "credential", Name: "OPENAI_API_KEY", Strength: StrengthMandatory},
			},
			Approval: Approval{RequiredWhen: []string{"chargeable"}},
			Produces: []string{"tutorial_md"},
		}},
		Capabilities: []Capability{
			{ID: "content.produce", Projects: []string{"produce_content"},
				Effects: Effects{MayCharge: charging}},
			// Projects NOTHING and is still declared. Legal (section 2).
			{ID: "content.types", Effects: Effects{MayCharge: MayCharge{Always: false}}},
		},
		ArtifactKinds: map[string]ArtifactKind{
			"tutorial_md": {Kind: KindText, MediaType: "text/markdown"},
		},
	}
}

func codes(f []Finding) string {
	var b []string
	for _, x := range f {
		b = append(b, x.Code)
	}
	return strings.Join(b, ",")
}

// The honest projection passes, and a capability projecting NOTHING is not a
// finding.
//
// This is the baseline every mutation below is measured against. Without it a
// mutation test proves only that SOMETHING fails, not that the mutation caused
// it -- and a validator that rejected everything would pass every mutation.
func TestAnHonestProjectionPassesAndAnEmptyProjectionIsLegal(t *testing.T) {
	if f := Validate(archiveLike()); len(f) != 0 {
		t.Fatalf("an honest descriptor produced findings: %s", codes(f))
	}
}

// MUTATION: may_charge weakened at the capability layer.
//
// THE FAILURE THIS CATCHES IS INVISIBLE AT THE OPERATION LAYER. The Operation
// still declares the honest condition and reads perfectly correct. The gate
// reads CAPABILITY effects, so it fires on nothing and an invocation spends
// money through a surface declaring it cannot.
func TestAWeakenedMayChargeIsCaught(t *testing.T) {
	d := archiveLike()
	d.Capabilities[0].Effects.MayCharge = MayCharge{Always: false}

	f := Validate(d)
	if len(f) == 0 {
		t.Fatal("a capability declaring may_charge:false while projecting a" +
			" charging Operation passed conformance, so the gate would never" +
			" fire and the Operation would still read correct to a reviewer")
	}
	if !strings.Contains(codes(f), "may_charge_weakened") {
		t.Errorf("failed for the wrong reason: %s", codes(f))
	}
	// The remedy must name the legal escape, or an author cannot act on it.
	if !strings.Contains(f[0].Remedy, "collapse it to true") {
		t.Errorf("the remedy does not name the collapse that always satisfies"+
			" no-weakening: %q", f[0].Remedy)
	}
}

// MUTATION: may_charge NARROWED rather than removed -- the subtle case.
//
// MY FIRST VERSION OF THIS TEST WAS WRONG, AND THE CODE WAS RIGHT. I dropped
// "slides" from the capability's when_in while both sides kept default:true --
// and with that default the capability STILL charges for slides, because an
// unlisted value falls through to the default. There was no weakening to
// catch, and the check correctly reported none. I had asserted a failure that
// should not happen: the same "fires for the wrong reason" family as a test
// that passes for the wrong reason, inverted.
//
// A REAL narrowing needs default:false on the capability, so the dropped value
// genuinely stops charging. That is what this now constructs, and it is the
// case §2a rule 3 exists for: both sides are conditional, so a comparison that
// collapsed them to booleans would see conditional-vs-conditional and pass.
func TestANarrowedConditionIsCaughtNotJustAnEmptiedOne(t *testing.T) {
	d := archiveLike()
	d.Capabilities[0].Effects.MayCharge = MayCharge{
		Field:   "kind",
		WhenIn:  []string{"tutorial", "adr"}, // "slides" dropped...
		Default: false,                       // ...and it now genuinely does not charge
	}

	if !strings.Contains(codes(Validate(d)), "may_charge_weakened") {
		t.Fatal("a capability that charges for a SUBSET of the Operation's" +
			" charging inputs passed. Comparing collapses instead of conditions" +
			" would miss exactly this, which is what rule 3 exists to prevent")
	}
}

// The narrowing must be caught for the RIGHT reason, not because default:false
// differs from default:true.
//
// Written after the mistake above, because a check keyed on the default alone
// would pass the test above while missing a when_in narrowing under a shared
// default -- and I would not have noticed, having already been wrong once here.
func TestASharedDefaultStillComparesTheListedValues(t *testing.T) {
	d := archiveLike()
	// Operation charges for {tutorial, adr, slides} with default FALSE.
	d.Operations[0].Effects.MayCharge = MayCharge{
		Field: "kind", WhenIn: []string{"tutorial", "adr", "slides"}, Default: false,
	}
	// Capability shares the default but drops one listed value.
	d.Capabilities[0].Effects.MayCharge = MayCharge{
		Field: "kind", WhenIn: []string{"tutorial", "adr"}, Default: false,
	}

	if !strings.Contains(codes(Validate(d)), "may_charge_weakened") {
		t.Fatal("with identical defaults, a dropped when_in value was not" +
			" caught, so the comparison is keyed on the default rather than on" +
			" the condition")
	}
}

// MUTATION: an effect understated.
func TestAnUnderstatedEffectIsCaught(t *testing.T) {
	d := archiveLike()
	d.Operations[0].Effects.Network = true // capability still says false

	f := Validate(d)
	if !strings.Contains(codes(f), "effect_understated") {
		t.Fatalf("a capability declaring network:false while projecting a"+
			" network Operation passed: %s", codes(f))
	}
}

// The direction must not be inverted: a capability MORE pessimistic than its
// Operation is legal, because over-declaring is always safe.
//
// Written because a no-weakening check with the comparison backwards would pass
// every test above -- they only assert that weakening fails.
func TestAMorePessimisticCapabilityIsLegal(t *testing.T) {
	d := archiveLike()
	d.Capabilities[0].Effects.Network = true // Operation says false
	d.Capabilities[0].Effects.MayCharge = MayCharge{Always: true}

	if f := Validate(d); len(f) != 0 {
		t.Fatalf("a capability declaring MORE effects than its Operation was"+
			" rejected: %s. Pessimism is always safe; only weakening is a"+
			" violation", codes(f))
	}
}

// MUTATION: a mandatory Requirement weakened, or its strength removed.
func TestAWeakenedRequirementStrengthIsCaught(t *testing.T) {
	d := archiveLike()
	d.Operations[0].Requirements[1].Strength = "" // was mandatory

	f := Validate(d)
	if !strings.Contains(codes(f), "requirement_without_strength") {
		t.Fatalf("a requirement with no strength passed: %s", codes(f))
	}
	if !strings.Contains(f[0].Remedy, "BLOCKS") {
		t.Errorf("the remedy does not say what strength is FOR: %q", f[0].Remedy)
	}
}

// MUTATION: an artifact contract misclassified.
//
// Both directions, because they are different mistakes: a document with no
// validator promises a check that does not exist, and media WITH one promises a
// check nothing can perform.
func TestAMisclassifiedArtifactIsCaught(t *testing.T) {
	t.Run("document without a validator", func(t *testing.T) {
		d := archiveLike()
		d.ArtifactKinds["tutorial_md"] = ArtifactKind{
			Kind: KindDocument, MediaType: "application/json",
		}
		f := Validate(d)
		if !strings.Contains(codes(f), "document_without_validator") {
			t.Fatalf("a document naming no validator passed: %s", codes(f))
		}
		if !strings.Contains(f[0].Remedy, "text") {
			t.Errorf("the remedy does not offer text as the honest"+
				" alternative: %q", f[0].Remedy)
		}
	})

	t.Run("media with a validator", func(t *testing.T) {
		d := archiveLike()
		d.ArtifactKinds["render_mp4"] = ArtifactKind{
			Kind: KindMedia, MediaType: "video/mp4",
			Validator: &Validator{Type: "json_schema", Schema: "x/v1"},
		}
		d.Operations[0].Produces = append(d.Operations[0].Produces, "render_mp4")
		if !strings.Contains(codes(Validate(d)), "media_with_validator") {
			t.Fatal("media naming a JSON Schema validator passed. A JSON Schema" +
				" cannot validate an mp4, so the declaration promises a check" +
				" nothing can perform")
		}
	})

	t.Run("an unknown kind is refused, not defaulted to text", func(t *testing.T) {
		d := archiveLike()
		d.ArtifactKinds["tutorial_md"] = ArtifactKind{
			Kind: "txt", MediaType: "text/markdown", // typo for "text"
		}
		if !strings.Contains(codes(Validate(d)), "unknown_artifact_kind") {
			t.Fatal("an unrecognised kind was accepted. Treating it as text" +
				" would let a typo read as a considered claim that no validator" +
				" exists")
		}
	})
}

// MUTATION: determinism overstated. Section 4 requires it false when network or
// may_charge could be true.
func TestDeterminismContradictedByItsOwnSiblingsIsCaught(t *testing.T) {
	t.Run("deterministic over a network call", func(t *testing.T) {
		d := archiveLike()
		d.Capabilities[1].Effects.Network = true
		d.Capabilities[1].Effects.Deterministic = true
		if !strings.Contains(codes(Validate(d)), "determinism_contradicted") {
			t.Fatal("deterministic:true with network:true passed")
		}
	})

	t.Run("deterministic while charging", func(t *testing.T) {
		d := archiveLike()
		d.Operations[0].Effects.Deterministic = true
		f := Validate(d)
		if !strings.Contains(codes(f), "determinism_contradicted") {
			t.Fatalf("deterministic:true with a charging may_charge passed: %s", codes(f))
		}
	})
}

// A projection to an Operation that does not exist must be REFUSED, not
// skipped.
//
// Skipping is the tempting implementation -- there is nothing to compare
// against -- and it is wrong: an unresolvable projection produces the same
// observable as a clean one, so a typo in projects would silently disable the
// no-weakening check for that capability.
func TestAProjectionToAnUnknownOperationIsRefusedNotSkipped(t *testing.T) {
	d := archiveLike()
	d.Capabilities[0].Projects = []string{"produce_contnet"} // typo

	if !strings.Contains(codes(Validate(d)), "unknown_operation") {
		t.Fatal("a projection to an undeclared Operation was skipped rather" +
			" than refused, so a typo in projects silently disables the" +
			" no-weakening check for that capability")
	}
}

// EXTERNAL_WRITES understated at the capability layer.
//
// FOUND BY THE INPUT-CLASS AUDIT, not by suspicion. The branch measured 0%
// while every no-weakening test was green: my mutations covered network and
// may_charge, so the assertions never entered this region. That is the exact
// distinction the operator drew -- line coverage proves execution, and the
// audit asks whether the assertion enters the region a mutation moves.
//
// It matters more than the network case: an external write CANNOT BE UN-DONE
// by an error, so an ungated one is not recoverable by retrying. The network
// case at least fails safe if the call never happens.
func TestExternalWritesUnderstatedIsCaught(t *testing.T) {
	d := archiveLike()
	d.Operations[0].Effects.ExternalWrites = true // capability still says false

	f := Validate(d)
	if !strings.Contains(codes(f), "effect_understated") {
		t.Fatalf("a capability declaring external_writes:false while projecting"+
			" an Operation that writes outside the host passed: %s", codes(f))
	}
	// Pin the REASON, not just the code: this and the network case share a
	// code, so asserting the code alone cannot tell which effect was caught.
	var found bool
	for _, x := range f {
		if x.Code == "effect_understated" && contains(x.Reason, "writes outside the host") {
			found = true
		}
	}
	if !found {
		t.Error("the finding does not name external writes, so it is" +
			" indistinguishable from the network case that shares its code")
	}
}

// DETERMINISM overstated at the capability layer.
//
// Also 0% before the audit. Distinct from validateEffects' determinism check,
// which catches a claim contradicted by its OWN siblings (network or charge).
// This catches a capability claiming determinism its OPERATION does not claim
// -- a projection defect rather than an internal contradiction, and only
// visible by comparing the two layers.
func TestDeterminismOverstatedAtTheCapabilityLayerIsCaught(t *testing.T) {
	d := archiveLike()
	// Neither layer charges or networks, so validateEffects stays silent and
	// only the PROJECTION comparison can catch this.
	d.Operations[0].Effects.MayCharge = MayCharge{Always: false}
	d.Capabilities[0].Effects.MayCharge = MayCharge{Always: false}
	d.Capabilities[0].Effects.Deterministic = true // Operation does not claim it

	f := Validate(d)
	if !strings.Contains(codes(f), "determinism_overstated") {
		t.Fatalf("a capability claiming determinism its Operation does not claim"+
			" passed: %s. Re-execution is then offered as free recovery for"+
			" something the product does not guarantee is repeatable", codes(f))
	}
	// It must NOT be reported as the internal-contradiction case, which has a
	// different code and a different fix.
	if strings.Contains(codes(f), "determinism_contradicted") {
		t.Error("a projection defect was reported as an internal contradiction:" +
			" nothing here networks or charges, so the sibling-check is not what" +
			" fired and an author would look in the wrong place")
	}
}
