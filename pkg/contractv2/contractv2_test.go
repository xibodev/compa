package contractv2

import (
	"strings"
	"testing"

	"github.com/xibodev/compa/v3/pkg/modproto"
)

// THE FALSIFICATION TEST the operator ruling requires: a wrong contract_version
// must be refused BEFORE any v2 behavioural guarantee is relied upon.
//
// This is the gate the whole conformance suite sits behind. If it can be passed
// by a counterparty speaking a different behavioural contract, every guarantee
// checked afterwards is being checked against something that never agreed to
// them -- which is precisely how v1's inert version list let two sibling lanes
// build confident beliefs on a promise nothing enforced.
func TestAWrongContractVersionIsRefused(t *testing.T) {
	for _, declared := range []string{
		"xibodev.module/v1",
		"xibodev.module/v3",
		"xibodev.module/v2-beta",
		"XIBODEV.MODULE/V2",
		" xibodev.module/v2",
		"xibodev.module/v2 ",
		"something.else/v2",
	} {
		res := CheckPin(declared)
		if res.MayRelyOnV2() {
			t.Errorf("a module declaring %q passed the contract pin, so v2"+
				" guarantees would be relied upon against a counterparty that"+
				" never agreed to them", declared)
		}
		if res.Served() {
			t.Errorf("a module declaring the WRONG version %q was still served;"+
				" a declared contract this host does not implement is a"+
				" disagreement about meaning, not a fallback", declared)
		}
		if res.Error() == nil {
			t.Errorf("%q was refused but produced no error for a caller to"+
				" report", declared)
		}
	}
}

// The exact match, and only the exact match, proceeds.
func TestTheExactVersionIsAccepted(t *testing.T) {
	res := CheckPin(ContractID)
	if !res.MayRelyOnV2() {
		t.Fatalf("the host refused its own contract version %q: %v",
			ContractID, res.Error())
	}
	if res.Error() != nil {
		t.Errorf("an accepted pin still produced an error: %v", res.Error())
	}
}

// A refusal must carry a REASON and a REMEDY, not merely fail.
//
// The v1 lesson this encodes: a validator that refuses without saying what to
// do sends the reader to source, and two lanes then infer different things from
// what it happens to accept.
func TestARefusalNamesBothSidesAndSaysWhatToDo(t *testing.T) {
	res := CheckPin("xibodev.module/v3")

	if res.Reason == "" || res.Remedy == "" {
		t.Fatalf("a refusal is missing reason or remedy: %+v", res)
	}
	// It must name what the module declared AND what the host implements.
	// Naming only one leaves the reader guessing which end to change.
	for _, want := range []string{"xibodev.module/v3", ContractID} {
		if !strings.Contains(res.Reason, want) {
			t.Errorf("the reason does not name %q, so the operator cannot see"+
				" which side to change: %q", want, res.Reason)
		}
	}
}

// An ABSENT version is SERVED AS v1, not refused. This is the case that makes
// three outcomes necessary rather than two.
//
// A single OK boolean made "serve under v1" and "refuse" indistinguishable, so
// wiring the gate would have refused every module shipped today -- breaking the
// frozen contract's own compatibility rule (§10: v1 modules keep working;
// absent v2 fields fall back to v1 behaviour, never to a more permissive
// default).
//
// The sibling lane reached the same conclusion from the other side: they permit
// an absent contract_version and refuse present-and-wrong. Their message is
// what prompted the re-read that found this.
func TestAnAbsentVersionIsServedAsV1AndNeverGrantsV2(t *testing.T) {
	absent := CheckPin("")

	if absent.MayRelyOnV2() {
		t.Fatal("a module declaring no contract version was granted v2" +
			" guarantees, which is exactly what the pin exists to prevent")
	}
	if !absent.Served() {
		t.Fatal("a module declaring no contract version was REFUSED. Every" +
			" module shipped today declares nothing, so wiring this gate" +
			" would break all of them and violate §10")
	}
	if absent.Outcome != OutcomeV1 {
		t.Errorf("absent resolved to %v, want v1", absent.Outcome)
	}
	// Being served is not an error condition.
	if absent.Error() != nil {
		t.Errorf("a served v1 module produced an error: %v", absent.Error())
	}
}

// An absent version and a WRONG one must not be confused: one is served, the
// other refused, and they need different remedies.
//
// Telling a v1 module's author "you declared the wrong version" is accurate
// and useless -- they declared nothing, correctly.
func TestAnAbsentVersionIsDistinguishedFromAWrongOne(t *testing.T) {
	absent := CheckPin("")
	wrong := CheckPin("xibodev.module/v3")

	if absent.Outcome == wrong.Outcome {
		t.Fatal("an absent version and a wrong version produce the same" +
			" outcome, so the host cannot tell a v1 module from a" +
			" misdeclared v2 one")
	}
	if absent.Reason == wrong.Reason {
		t.Error("absent and wrong produce the same reason text")
	}
	// The v1 remedy must not send a correct author to change something.
	if !strings.Contains(absent.Remedy, "nothing to fix") {
		t.Errorf("the absent-version remedy does not tell a v1 author their"+
			" declaration is already correct: %q", absent.Remedy)
	}
	if wrong.Served() {
		t.Error("a wrong version was served rather than refused")
	}
}

// Declaring the WIRE identity as a behavioural contract is refused, and gets
// its own remedy because it is a different mistake.
//
// The generic remedy says "declare contract_version v2", which for this author
// is actively wrong: it instructs a v1 caller to claim guarantees they do not
// implement, so the host would then rely on v2 semantics against a module that
// never agreed to them -- exactly what this gate prevents.
//
// The sibling lane found this from the other side. Their refusal said "install
// a module implementing xibodev.module/v1", which is impossible advice since no
// such behavioural contract exists. Both messages sent an author who was right
// about their own intent to do something they should not do.
func TestDeclaringTheWireIdentityGetsItsOwnRemedy(t *testing.T) {
	wire := CheckPin("xibodev.module/v1")
	unknown := CheckPin("xibodev.module/v3")

	if wire.Served() {
		t.Fatal("the wire identity was served as a behavioural contract, so a" +
			" conformance record would name a contract with no guarantees")
	}
	if wire.Reason == unknown.Reason || wire.Remedy == unknown.Remedy {
		t.Fatal("declaring the wire identity and declaring an unknown version" +
			" produce the same message, so two different mistakes get one fix")
	}
	// It must tell this author to OMIT the field, never to declare v2.
	if !strings.Contains(wire.Remedy, "omit contract_version") {
		t.Errorf("the remedy does not tell a v1 caller to omit the field: %q",
			wire.Remedy)
	}
	// And it must explain WHICH axis they confused, or the advice looks arbitrary.
	if !strings.Contains(wire.Reason, "WIRE-FORMAT") {
		t.Errorf("the reason does not say the value names the wire format"+
			" rather than a behavioural contract: %q", wire.Reason)
	}
}

// The wire identity must track modproto rather than being a second literal.
//
// Two copies of one constant is the duplicated-decision family this codebase
// keeps finding; if ProtocolID ever changes, a hardcoded "xibodev.module/v1"
// here would silently stop matching and the special remedy would vanish.
func TestTheWireIdentityCaseTracksModproto(t *testing.T) {
	if CheckPin(modproto.ProtocolID).Served() {
		t.Fatal("the wire identity is served, so the special case is not" +
			" keyed on modproto.ProtocolID")
	}
	if !strings.Contains(CheckPin(modproto.ProtocolID).Remedy, "omit contract_version") {
		t.Error("modproto.ProtocolID does not reach the wire-identity remedy," +
			" so the two have drifted apart")
	}
}

// The refusal must state that this host does NOT negotiate.
//
// Under v1 a doc comment claimed the host "selects the highest it also
// supports" over what was a membership test against one constant. A sibling
// lane built a versioning story on that sentence. The remedy text is where a
// reader forms the same expectation, so it has to close it explicitly.
func TestTheRemedySaysThisHostDoesNotNegotiate(t *testing.T) {
	remedy := CheckPin("xibodev.module/v3").Remedy

	for _, want := range []string{"negotiate", "downgrade", "fall back"} {
		if !strings.Contains(strings.ToLower(remedy), want) {
			t.Errorf("the remedy does not rule out %q, leaving a reader to"+
				" assume the host might: %q", want, remedy)
		}
	}
}

// Guard against the shape the ruling forbids reappearing.
//
// ContractID is a single string. If it ever becomes a collection, "declare
// several and pick one" becomes expressible, and the v1 failure -- a structure
// shaped like a choice that can only hold one value -- returns. This asserts
// the pin is a single exact value by demonstrating that nothing else passes,
// including a superstring and a substring of the real one.
func TestOnlyOneValueCanEverPass(t *testing.T) {
	for _, near := range []string{
		ContractID + ",xibodev.module/v3",
		ContractID + " xibodev.module/v3",
		"xibodev.module/v3," + ContractID,
		"xibodev.module",
		"v2",
	} {
		if CheckPin(near).MayRelyOnV2() {
			t.Errorf("%q passed the pin, so more than one value is acceptable"+
				" and the host is negotiating", near)
		}
	}
}

// Every Outcome renders a distinct, correct name.
//
// FOUND BY COVERAGE AUDIT, not by suspicion. String() measured 0% while every
// other function in the package measured 100% -- because it is reached ONLY
// from a t.Errorf diagnostic, so it runs only when another test has already
// failed. A defect in it would therefore be invisible until the exact moment
// someone needs it to read correctly, and would then misname the outcome in
// the message explaining a refusal.
//
// This is the debt disclosed to the sibling lane made concrete: a green suite
// whose coverage is unaudited and a green suite that is complete produce the
// same observable. 71.4% was the difference, and nothing said so.
//
// The names are asserted DISTINCT as well as correct. Two outcomes sharing a
// string would reproduce the conflation the three-valued type exists to
// prevent -- a served v1 module and a refusal reading identically in the one
// place a human looks.
func TestEveryOutcomeRendersADistinctName(t *testing.T) {
	want := map[Outcome]string{
		OutcomeV2:     "v2",
		OutcomeV1:     "v1",
		OutcomeRefuse: "refuse",
	}

	seen := map[string]Outcome{}
	for outcome, name := range want {
		got := outcome.String()
		if got != name {
			t.Errorf("Outcome(%d).String() = %q, want %q", int(outcome), got, name)
		}
		if prior, dup := seen[got]; dup {
			t.Errorf("Outcome(%d) and Outcome(%d) both render %q, so a refusal"+
				" and a served module are indistinguishable in the one place a"+
				" human reads them", int(prior), int(outcome), got)
		}
		seen[got] = outcome
	}

	// An unknown value must not render as a SERVED outcome. The default arm
	// returns "refuse", which is the safe direction: a value this package does
	// not recognise should never read as "v1" or "v2" in a conformance record.
	if got := Outcome(99).String(); got == "v1" || got == "v2" {
		t.Errorf("an unrecognised Outcome rendered as %q, so an unknown state"+
			" would be reported as a served one", got)
	}
}
