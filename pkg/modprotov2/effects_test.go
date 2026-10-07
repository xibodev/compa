package modprotov2

import (
	"encoding/json"
	"testing"
)

// Evaluate is what the HOST GATE CALLS to decide whether one invocation may
// charge (section 2a rule 2). It measured 0% while the no-weakening tests were
// green -- the comparison was exercised and the decision was not.
//
// Found by the input-class audit, not by suspicion. That is the distinction
// this session kept hitting: line coverage proves execution, and a suite can be
// green while the function that decides whether money may be spent has never
// run.
func TestEvaluateAnswersOneInvocation(t *testing.T) {
	conditional := MayCharge{
		Field:   "kind",
		WhenIn:  []string{"tutorial", "adr", "slides"},
		Default: false,
	}

	for _, tc := range []struct {
		name  string
		m     MayCharge
		input map[string]any
		want  bool
		why   string
	}{
		{"listed value charges", conditional,
			map[string]any{"kind": "tutorial"}, true,
			"a value in when_in is the charging case"},

		{"unlisted value falls to the default", conditional,
			map[string]any{"kind": "retrieval_pack"}, false,
			"this is the whole point of section 3a: seven free kinds stop being" +
				" permanently over-gated at the layer that acts"},

		{"MISSING field is TRUE", conditional,
			map[string]any{}, true,
			"section 2a rule 5. A module author verified the sequencing rather than" +
				" assuming it: over-gate, then the module refuses deterministically." +
				" The opposite default would send an UNAPPROVED request whose kind" +
				" might have been chargeable"},

		{"NON-STRING value is TRUE", conditional,
			map[string]any{"kind": 42}, true,
			"an unresolvable value is unevaluable, and unevaluable means charge." +
				" Reading 42 as 'not in when_in' would silently take the free path"},

		{"nil input is TRUE", conditional, nil, true,
			"no request to evaluate against is the unevaluable case"},

		{"unconditional true ignores input", MayCharge{Always: true},
			map[string]any{"kind": "retrieval_pack"}, true,
			"the collapse stays legal and is maximal"},

		{"unconditional false ignores input", MayCharge{Always: false},
			map[string]any{"kind": "tutorial"}, false,
			"a capability that genuinely never charges says so"},
	} {
		if got := tc.m.Evaluate(tc.input); got != tc.want {
			t.Errorf("%s: got %v want %v -- %s", tc.name, got, tc.want, tc.why)
		}
	}
}

// The default must not be reachable in a way that skips the unevaluable rule.
//
// Written as a separate assertion because the table above could pass with
// Evaluate returning `m.Default` for a missing field IF Default happened to be
// true. This pins the rule independent of the declaration's own default.
func TestAnUnevaluableConditionChargesEvenWhenTheDefaultIsFalse(t *testing.T) {
	m := MayCharge{Field: "kind", WhenIn: []string{"tutorial"}, Default: false}

	if !m.Evaluate(map[string]any{}) {
		t.Fatal("a missing field resolved to the declaration's default (false)" +
			" rather than to charge. Rule 5 is not 'use the default when" +
			" unevaluable' -- it is 'unevaluable means charge', and the" +
			" difference is only visible when the default is false")
	}
}

// may_charge crosses the wire in two forms and must survive the round trip.
//
// UNTESTED UNTIL THE AUDIT. Both MarshalJSON and UnmarshalJSON measured 0%,
// which means every no-weakening test above was operating on structs built in
// Go and never on a declaration that had been serialised. A module's
// declaration reaches this host as bytes.
func TestMayChargeSurvivesTheWireInBothForms(t *testing.T) {
	t.Run("plain boolean stays a boolean", func(t *testing.T) {
		b, err := json.Marshal(MayCharge{Always: true})
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != "true" {
			t.Errorf("unconditional may_charge serialised as %s, want true."+
				" Emitting an object here would make every v1-shaped reader"+
				" fail to parse a declaration it should understand", b)
		}
		var back MayCharge
		if err := json.Unmarshal(b, &back); err != nil {
			t.Fatal(err)
		}
		if !back.Always || back.IsConditional() {
			t.Errorf("a boolean round-tripped into %+v", back)
		}
	})

	t.Run("conditional keeps its condition", func(t *testing.T) {
		orig := MayCharge{Field: "kind", WhenIn: []string{"tutorial", "adr"}, Default: true}
		b, err := json.Marshal(orig)
		if err != nil {
			t.Fatal(err)
		}
		var back MayCharge
		if err := json.Unmarshal(b, &back); err != nil {
			t.Fatal(err)
		}
		if !back.IsConditional() || back.Field != "kind" ||
			len(back.WhenIn) != 2 || !back.Default {
			t.Fatalf("a conditional round-tripped into %+v (wire: %s)", back, b)
		}

		// The decisive check: it must still EVALUATE the same after the trip.
		// Structural equality is not the property that matters -- the answer is.
		for _, kind := range []string{"tutorial", "retrieval_pack"} {
			in := map[string]any{"kind": kind}
			if orig.Evaluate(in) != back.Evaluate(in) {
				t.Errorf("kind=%q evaluates differently after a round trip:"+
					" %v then %v", kind, orig.Evaluate(in), back.Evaluate(in))
			}
		}
	})
}

// A conditional declaration whose field differs from its Operation's cannot be
// compared pointwise, and must be REFUSED rather than assumed safe.
//
// The tempting implementation returns true (nothing proven wrong). That is the
// same shape as skipping an unresolvable projection: an unprovable claim would
// pass as a proven one.
func TestConditionsOnDifferentFieldsAreRefusedNotAssumed(t *testing.T) {
	op := MayCharge{Field: "kind", WhenIn: []string{"tutorial"}, Default: false}
	cap := MayCharge{Field: "format", WhenIn: []string{"tutorial"}, Default: false}

	if cap.NoWeakerThan(op) {
		t.Fatal("two conditionals on DIFFERENT fields were certified as" +
			" no-weakening. They cannot be compared pointwise, so the honest" +
			" answer is to refuse rather than to report a check that did not" +
			" happen")
	}
}

// Every Resolution state is valid, and an unrecognised one is NOT quietly
// treated as unknown.
//
// State.Valid measured 0%. The distinction it encodes is load-bearing:
// "unknown" is a deliberate claim about a probe that could not complete, while
// an unparseable value is a module speaking a vocabulary this host lacks.
// Collapsing the second into the first lets a typo read as a considered answer.
func TestEveryResolutionStateIsValidAndATypoIsNot(t *testing.T) {
	for _, s := range []State{StateSatisfied, StateUnsatisfied, StateUnknown} {
		if !s.Valid() {
			t.Errorf("%q is one of the three states and was rejected", s)
		}
	}
	for _, s := range []State{"", "available", "satisfied ", "SATISFIED", "unkown"} {
		if State(s).Valid() {
			t.Errorf("%q was accepted as a Resolution state. An unrecognised"+
				" value must not read as a considered claim", s)
		}
	}
}

// A Finding renders with its reason AND its remedy.
//
// Finding.String measured 0%, and it is reached only when something has already
// failed -- the same shape as contractv2's Outcome.String: the function whose
// whole job is explaining a failure is the one no passing run exercises.
func TestAFindingRendersBothWhatIsWrongAndWhatToDo(t *testing.T) {
	f := Finding{
		Code: "may_charge_weakened", Where: "capability x",
		Reason: "REASON-TEXT", Remedy: "REMEDY-TEXT",
	}
	got := f.String()
	for _, want := range []string{"may_charge_weakened", "capability x", "REASON-TEXT", "REMEDY-TEXT"} {
		if !contains(got, want) {
			t.Errorf("the rendered finding omits %q: %q", want, got)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) &&
		(haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}

// A v2 declaration with NO Operations is reported, not refused.
//
// The distinction took checking rather than instinct: a capability projecting
// no Operation is explicitly legal, so a module whose every capability is a
// registry read legitimately has zero Operations. Refusing that would reject a
// shape the frozen contract permits -- a contract change wearing an
// implementation's clothes.
//
// What the finding says instead is that two states look identical here: no
// Operations because there are none, and no Operations because the payload is
// unbuilt. In the second case the no-weakening check passes WITHOUT CHECKING
// ANYTHING, which is the false-green shape this contract exists to close.
func TestAV2DeclarationWithNoOperationsIsReported(t *testing.T) {
	d := &Descriptor{
		Module:       "render",
		Capabilities: []Capability{{ID: "creative.tools.list"}, {ID: "creative.tools.run"}},
	}

	f := Validate(d)
	if len(f) == 0 {
		t.Fatal("a v2 module declaring no Operations produced no finding, so" +
			" 'has none' and 'not built yet' are indistinguishable and the" +
			" no-weakening check silently checks nothing")
	}
	if f[0].Code != "no_operations_declared" {
		t.Fatalf("wrong finding: %s", f[0].Code)
	}
	// It must name BOTH readings, or it reads as an accusation of a defect
	// when the shape may be entirely correct.
	if !contains(f[0].Remedy, "registry read") || !contains(f[0].Remedy, "not built yet") {
		t.Errorf("the remedy does not offer both readings: %q", f[0].Remedy)
	}
}

// A module WITH Operations produces no such finding.
func TestADescriptorWithOperationsIsNotFlagged(t *testing.T) {
	if f := Validate(archiveLike()); len(f) != 0 {
		t.Fatalf("a normal descriptor was flagged: %s", codes(f))
	}
}

// An artifact kind that NO Operation produces is reported.
//
// THE REVERSE DIRECTION, which was missing: `produces` -> artifact_kinds was
// checked, artifact_kinds -> `produces` was not. So a declared kind nothing
// points at looked correct indefinitely -- the UNRESOLVED REFERENCE class a
// sibling lane named after finding two in one code path. A duplicate has a
// second copy to disagree with; an unresolved reference has nothing to
// disagree with at all.
func TestAnArtifactKindNoOperationProducesIsReported(t *testing.T) {
	d := archiveLike()
	d.ArtifactKinds["orphan_pdf"] = ArtifactKind{Kind: KindMedia, MediaType: "application/pdf"}

	f := Validate(d)
	if !containsCode(f, "artifact_kind_unreferenced") {
		t.Fatalf("a declared artifact kind that no Operation produces was not"+
			" reported: %s", codes(f))
	}
	// It must name WHICH kind, or an author with several cannot act on it.
	var found bool
	for _, x := range f {
		if x.Code == "artifact_kind_unreferenced" && contains(x.Where, "orphan_pdf") {
			found = true
		}
	}
	if !found {
		t.Error("the finding does not name the unreferenced kind")
	}
}

// A referenced kind produces nothing, so the check above fails for the
// REFERENCE and not merely for existing.
func TestAReferencedArtifactKindIsNotReported(t *testing.T) {
	if f := Validate(archiveLike()); containsCode(f, "artifact_kind_unreferenced") {
		t.Fatalf("a kind named in produces was reported as unreferenced: %s", codes(f))
	}
}

// With ZERO Operations the unreferenced findings are suppressed in favour of
// the bigger fact.
//
// Otherwise every declared kind is reported as unreferenced -- N copies of one
// consequence burying the finding that actually explains it.
func TestWithNoOperationsTheBiggerFindingIsNotBuried(t *testing.T) {
	d := &Descriptor{
		Module:       "render",
		Capabilities: []Capability{{ID: "c1"}},
		ArtifactKinds: map[string]ArtifactKind{
			"a": {Kind: KindMedia, MediaType: "video/mp4"},
			"b": {Kind: KindMedia, MediaType: "audio/mpeg"},
			"c": {Kind: KindText, MediaType: "text/plain"},
		},
	}

	f := Validate(d)
	if containsCode(f, "artifact_kind_unreferenced") {
		t.Error("with no Operations, each kind was reported as unreferenced," +
			" burying the half-published finding under copies of its own" +
			" consequence")
	}
	if !containsCode(f, "v2_payload_half_published") {
		t.Fatalf("a descriptor with artifact kinds and NO Operations was not"+
			" reported as half-published: %s", codes(f))
	}
}

// Half-published is UNAMBIGUOUS and must not be reported as the ambiguous case.
//
// Artifact kinds are named by an Operation's produces; with no Operations
// nothing can produce them. So "this module genuinely has no Operations" cannot
// be true here, and offering it as a reading would be offering a false one.
func TestHalfPublishedIsDistinguishedFromGenuinelyHavingNoOperations(t *testing.T) {
	ambiguous := Validate(&Descriptor{
		Module: "registry_only", Capabilities: []Capability{{ID: "c1"}},
	})
	halfBuilt := Validate(&Descriptor{
		Module: "render", Capabilities: []Capability{{ID: "c1"}},
		ArtifactKinds: map[string]ArtifactKind{"a": {Kind: KindMedia, MediaType: "video/mp4"}},
	})

	if !containsCode(ambiguous, "no_operations_declared") {
		t.Fatalf("the ambiguous case lost its finding: %s", codes(ambiguous))
	}
	if !containsCode(halfBuilt, "v2_payload_half_published") {
		t.Fatalf("the half-built case was not distinguished: %s", codes(halfBuilt))
	}
	if containsCode(halfBuilt, "no_operations_declared") {
		t.Error("a half-published payload was ALSO offered the 'genuinely has" +
			" no Operations' reading, which cannot be true when artifact kinds" +
			" are declared")
	}
}

func containsCode(f []Finding, code string) bool {
	for _, x := range f {
		if x.Code == code {
			return true
		}
	}
	return false
}

// The ambiguous finding must SAY it is ambiguous.
//
// I told the sibling lane their build was classified as half-published. It is
// not: they publish zero v2 artifact_kinds, so the discriminator never fires
// for them, and they land in exactly the bucket the host cannot resolve. A
// finding that offers two readings without saying the host cannot choose
// between them reads as though the host had chosen.
func TestTheAmbiguousFindingAdmitsTheHostCannotDecide(t *testing.T) {
	f := Validate(&Descriptor{Module: "m", Capabilities: []Capability{{ID: "c"}}})
	if len(f) == 0 || f[0].Code != "no_operations_declared" {
		t.Fatalf("wrong finding: %s", codes(f))
	}
	if !contains(f[0].Reason, "CANNOT TELL") {
		t.Errorf("the reason does not say the host cannot distinguish the two"+
			" cases: %q", f[0].Reason)
	}
	if !contains(f[0].Remedy, "no action") {
		t.Errorf("the remedy does not tell a legitimately Operation-free module"+
			" that nothing is wrong: %q", f[0].Remedy)
	}
}

// The half-published finding is reachable ONLY when artifact kinds are present,
// which is the limit the sibling identified.
//
// Pinned so nobody later "improves" it into firing on the bare case -- that
// would classify a mid-migration module as half-built when the wire says no
// such thing, which is inventing a signal rather than reporting one.
func TestHalfPublishedNeverFiresWithoutArtifactKinds(t *testing.T) {
	bare := Validate(&Descriptor{Module: "m", Capabilities: []Capability{{ID: "c"}}})
	if containsCode(bare, "v2_payload_half_published") {
		t.Fatal("a descriptor with no Operations AND no artifact kinds was" +
			" classified as half-published. Nothing on the wire supports that" +
			" conclusion: a pre-migration module has neither field, so the" +
			" claim would be invented rather than measured")
	}
}
