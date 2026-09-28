package moduletools

import (
	"testing"

	"github.com/xibodev/compa/pkg/modprotov2"
)

func v2Descriptor() *modprotov2.Descriptor {
	charging := modprotov2.MayCharge{
		Field:   "kind",
		WhenIn:  []string{"tutorial", "adr", "slides"},
		Default: false,
	}
	return &modprotov2.Descriptor{
		Operations: []modprotov2.Operation{
			{
				ID:       "produce_content",
				Effects:  modprotov2.Effects{MayCharge: charging},
				Approval: modprotov2.Approval{RequiredWhen: []string{"chargeable"}},
			},
			{
				ID: "publish_release",
				Effects: modprotov2.Effects{
					ExternalWrites: true,
					MayCharge:      modprotov2.MayCharge{Always: false},
				},
				Approval: modprotov2.Approval{
					RequiredWhen: []string{"irreversible", "external_write"},
				},
			},
		},
		Capabilities: []modprotov2.Capability{
			{ID: "content.produce", Projects: []string{"produce_content"},
				Effects: modprotov2.Effects{MayCharge: charging}},
			{ID: "release.publish", Projects: []string{"publish_release"},
				Effects: modprotov2.Effects{
					ExternalWrites: true,
					MayCharge:      modprotov2.MayCharge{Always: false},
				}},
			// Projects nothing. Still invocable, still gated on its own effects.
			{ID: "content.types",
				Effects: modprotov2.Effects{MayCharge: modprotov2.MayCharge{Always: false}}},
		},
	}
}

func capByID(d *modprotov2.Descriptor, id string) *modprotov2.Capability {
	for i := range d.Capabilities {
		if d.Capabilities[i].ID == id {
			return &d.Capabilities[i]
		}
	}
	return nil
}

// THE GAP v1 COULD NOT CLOSE: the same capability is gated or not depending on
// the ARGUMENT, so the free kinds stop being permanently over-gated.
//
// Under v1 this capability declares one boolean and all 19 kinds are gated
// together. Under section 2a rule 2 the condition is evaluated against the
// request the host is about to send.
func TestChargeabilityIsDecidedPerInvocationNotPerCapability(t *testing.T) {
	d := v2Descriptor()
	c := capByID(d, "content.produce")

	paid := NeedsApprovalV2(d, c, map[string]any{"kind": "tutorial"})
	if !paid.Required {
		t.Error("a charging kind was not gated")
	}

	free := NeedsApprovalV2(d, c, map[string]any{"kind": "retrieval_pack"})
	if free.Required {
		t.Errorf("a FREE kind was gated: %v. This is the permanent over-gate"+
			" section 3a exists to remove, and it would be removed at the"+
			" declaration layer while surviving at the layer that acts", free.Reasons)
	}
}

// An unevaluable condition charges (rule 5), even though the declaration's own
// default is false.
//
// The sequencing was verified by the module lane rather than assumed:
// over-gate, then the module refuses the request deterministically. The
// opposite default would send an UNAPPROVED request whose kind might have been
// chargeable.
func TestAMissingArgumentIsGatedRatherThanAssumedFree(t *testing.T) {
	d := v2Descriptor()

	if !NeedsApprovalV2(d, capByID(d, "content.produce"), map[string]any{}).Required {
		t.Fatal("a request with no kind was not gated. Unevaluable means" +
			" charge; falling back to the declaration's default (false) here" +
			" would send an unapproved request whose kind might have charged")
	}
}

// A FREE-BUT-IRREVERSIBLE step is gated, which v1 could not do at all.
//
// v1 had one gate keyed on one field, so this capability -- which spends
// nothing -- was completely ungated while a cost-checking tool was gated as
// though it billed. Exactly inverted from what a person needs.
func TestAFreeButIrreversibleStepIsGatedWithItsRealReasons(t *testing.T) {
	d := v2Descriptor()
	dec := NeedsApprovalV2(d, capByID(d, "release.publish"), nil)

	if !dec.Required {
		t.Fatal("a free but irreversible external write was not gated")
	}

	got := map[string]bool{}
	for _, r := range dec.Reasons {
		got[r] = true
	}
	if got["chargeable"] {
		t.Error("gated as chargeable when it spends nothing. The reason is what" +
			" the person is being asked about, so a wrong one is worse than a" +
			" missing one")
	}
	for _, want := range []string{"external_write", "irreversible"} {
		if !got[want] {
			t.Errorf("missing reason %q: %v", want, dec.Reasons)
		}
	}
}

// The gate reads CAPABILITY effects, not Operation effects (section 2).
//
// Pinned because the tempting implementation reads the Operation -- that is
// where the semantics live, so it feels more authoritative. It is also
// undefined for a many-to-one projection: one sibling's capability reaches any
// of 35 Operations, so "the Operation's condition" names no single thing.
//
// This is the case the no-weakening check exists to prevent reaching
// production. Here it is constructed deliberately to prove the gate's
// read-point.
func TestTheGateReadsCapabilityEffectsNotOperationEffects(t *testing.T) {
	d := v2Descriptor()
	// Capability declares MORE than its Operation: pessimism, always legal.
	capByID(d, "content.produce").Effects.MayCharge = modprotov2.MayCharge{Always: true}

	dec := NeedsApprovalV2(d, capByID(d, "content.produce"),
		map[string]any{"kind": "retrieval_pack"}) // free at the Operation layer

	if !dec.Required {
		t.Fatal("the gate used the OPERATION's condition rather than the" +
			" capability's. The capability is where the gate reads, and reading" +
			" the Operation is undefined when one capability projects many")
	}
}

// A capability projecting NO Operation is still gated on its own effects.
func TestACapabilityProjectingNothingIsStillEvaluated(t *testing.T) {
	d := v2Descriptor()
	dec := NeedsApprovalV2(d, capByID(d, "content.types"), nil)

	if dec.Required {
		t.Errorf("a free registry read was gated: %v", dec.Reasons)
	}

	// And when such a capability DOES declare an effect, it is gated -- the
	// point being that "projects nothing" is not "declares nothing".
	capByID(d, "content.types").Effects.ExternalWrites = true
	if !NeedsApprovalV2(d, capByID(d, "content.types"), nil).Required {
		t.Fatal("a capability projecting no Operation was skipped rather than" +
			" evaluated, so its own declared effects were ignored")
	}
}

// An unknown capability is gated, not waved through.
func TestAnUnknownCapabilityIsGatedRatherThanTreatedAsFree(t *testing.T) {
	if !NeedsApprovalV2(v2Descriptor(), nil, nil).Required {
		t.Fatal("an unknown capability was reported as needing no approval," +
			" making a typo in a capability ID indistinguishable from a" +
			" genuinely free operation")
	}
}

// EVERY reason must be covered by the approval, and a model may not grant one.
func TestAnApprovalCoversOnlyWhatItNames(t *testing.T) {
	d := v2Descriptor()
	dec := NeedsApprovalV2(d, capByID(d, "release.publish"), nil)

	t.Run("an approval for the wrong reason does not satisfy", func(t *testing.T) {
		rec := &modprotov2.ApprovalRecord{
			Granted: true, Reason: "chargeable", GrantedBy: "operator",
		}
		if ApprovalSatisfied(dec, rec) {
			t.Fatal("an approval granted for CHARGEABLE authorised an" +
				" irreversible external write. The person approved a charge;" +
				" approving one reason is not approving another they were" +
				" never shown")
		}
	})

	t.Run("a model-minted approval is refused", func(t *testing.T) {
		rec := &modprotov2.ApprovalRecord{
			Granted: true, Reason: "external_write", GrantedBy: "model",
		}
		if ApprovalSatisfied(dec, rec) {
			t.Fatal("an approval granted by the model was accepted, so the" +
				" thing being gated can authorise itself")
		}
	})

	t.Run("an absent record is not an approval", func(t *testing.T) {
		if ApprovalSatisfied(dec, nil) {
			t.Fatal("a missing approval record satisfied a required approval." +
				" Absent must mean none was obtained, never inferred consent")
		}
	})

	t.Run("no approval needed is satisfied by nothing", func(t *testing.T) {
		free := NeedsApprovalV2(d, capByID(d, "content.types"), nil)
		if !ApprovalSatisfied(free, nil) {
			t.Fatal("an ungated capability demanded an approval record")
		}
	})
}
