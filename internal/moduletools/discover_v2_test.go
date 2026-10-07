package moduletools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/xibodev/compa/v3/pkg/contractv2"
	"github.com/xibodev/compa/v3/pkg/modprotov2"
)

// THE TRAP: contract_version lives inside envelope.result, so evaluating raw
// describe stdout returns v1 for EVERY module.
//
// It is silent, because "no contract_version" is a legitimate answer meaning
// "this is a v1 module". The wrong input and a correct v1 module produce the
// same outcome, so nothing distinguishes a miswired host from a v1 estate.
//
// A sibling lane hit this on their first probe against this gate and nearly
// reported "no problem, we interoperate" from a v1 result that was an artifact
// of their test input. Pinned so a future caller cannot reintroduce it.
func TestTheGateReadsTheDescriptorNotTheEnvelope(t *testing.T) {
	descriptor := `{"module":"m","protocol_versions":["xibodev.module/v1"],
	  "contract_version":"xibodev.module/v2","operations":[],"capabilities":[]}`
	envelope := []byte(`{"protocol":"xibodev.module/v1","module":"m",
	  "operation":"describe","ok":true,"result":` + descriptor + `}`)

	// The WRONG input, which is what makes the trap silent.
	wrong, err := modprotov2.Evaluate(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if wrong.MayRelyOnV2() {
		t.Fatal("the envelope form somehow passed the pin; this test no longer" +
			" describes the trap it was written for")
	}

	// The RIGHT input: the descriptor bytes, which is what discovery passes.
	right := evaluateV2([]byte(descriptor))
	if !right.Decision.MayRelyOnV2() {
		t.Fatalf("a v2 module was read as v1, so the host is evaluating the"+
			" envelope rather than the descriptor: %v", right.Decision.Pin.Reason)
	}
}

// A v2 module that CONFORMS may be relied upon; one that passes the pin and
// FAILS conformance may not.
//
// The conjunction is the point. A module that claims v2 and then weakens its
// projection is MORE dangerous than a v1 module, not less: it has been admitted
// to the v2 path where the gate reads capability effects, while declaring
// capability effects weaker than its own Operations.
func TestPassingThePinIsNotEnoughToBeReliedUpon(t *testing.T) {
	weakening := []byte(`{"module":"m","contract_version":"xibodev.module/v2",
	  "operations":[{"id":"op","effects":{"may_charge":true}}],
	  "capabilities":[{"id":"c","projects":["op"],"effects":{"may_charge":false}}]}`)

	v := evaluateV2(weakening)

	if !v.Decision.MayRelyOnV2() {
		t.Fatal("the pin refused a well-formed v2 declaration; this test needs" +
			" a module that PASSES the pin to be meaningful")
	}
	if v2Refusal(v) == "" {
		t.Fatal("a module that passed the pin but weakens its projection was" +
			" accepted. It would be registered with capability effects weaker" +
			" than its own Operations -- worse than being treated as v1")
	}
	if v.Conformance == nil || v.Conformance.Conforms() {
		t.Fatal("conformance did not run, or reported no findings")
	}
}

// A v1 module produces NO v2 warnings.
//
// A warning on every v1 module would train people to ignore the panel that
// also reports real refusals -- the same reason staleContentWarnings stays
// silent when content matches.
func TestAV1ModuleIsQuietRatherThanWarnedAbout(t *testing.T) {
	v1 := evaluateV2([]byte(`{"module":"m","protocol_versions":["xibodev.module/v1"],
	  "capabilities":[]}`))

	if v1.Decision.Pin.Outcome != contractv2.OutcomeV1 {
		t.Fatalf("a module declaring no contract version resolved to %v, want v1",
			v1.Decision.Pin.Outcome)
	}
	if r := v2Refusal(v1); r != "" {
		t.Errorf("a v1 module was refused: %v", r)
	}
	if v1.Decision.MayRelyOnV2() {
		t.Error("a v1 module was marked v2-reliable")
	}
	if v1.Conformance != nil {
		t.Error("conformance ran on a v1 module. There is no Operation layer to" +
			" compare a capability against, so a verdict here would be about" +
			" nothing")
	}
}

// A REFUSAL and a NON-CONFORMANCE produce different warnings.
//
// They need different remedies: the first means the module named a contract
// this host does not implement, the second means it named the right one and
// then contradicted itself. Collapsing them sends an author to fix the wrong
// thing, which is what the reason/remedy split exists to prevent.
func TestARefusalAndANonConformanceAreDistinguishable(t *testing.T) {
	refused := v2Refusal(evaluateV2([]byte(`{"module":"m","contract_version":"xibodev.module/v3"}`)))
	nonConforming := v2Refusal(evaluateV2([]byte(`{"module":"m","contract_version":"xibodev.module/v2",
	  "operations":[{"id":"op","effects":{"may_charge":true}}],
	  "capabilities":[{"id":"c","projects":["op"],"effects":{"may_charge":false}}]}`)))

	if refused == "" || nonConforming == "" {
		t.Fatal("one of the two produced no refusal at all")
	}
	if refused == nonConforming {
		t.Fatal("a refused contract and a non-conforming projection produced" +
			" identical text, so an author cannot tell which mistake they made")
	}
}

// Undecodable describe output is NOT reported as a contract refusal.
//
// The remedy for "declare a different contract_version" is useless to someone
// whose module emitted malformed JSON, and would send them to change a field
// that is not the problem.
func TestMalformedOutputIsNotReportedAsAContractRefusal(t *testing.T) {
	v := evaluateV2([]byte(`{not json`))

	if v.Decision.MayRelyOnV2() {
		t.Fatal("malformed output was marked v2-reliable")
	}
	w := v2Refusal(v)
	if contains(w, "declare contract_version") {
		t.Errorf("malformed JSON produced a contract-version remedy: %q", w)
	}
	// It used to render the empty refusal of a zero Decision: ". ".
	if !contains(w, "could not be checked") || !contains(w, "not JSON") {
		t.Errorf("malformed JSON is not reported as itself: %q", w)
	}
}

func contains(h, n string) bool {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return true
		}
	}
	return false
}

// A module whose declared contract the host refuses is not registered: it is
// reported as refused, contributes no tools, and cannot be found by its ID.
// It used to be registered and run, with the refusal only a warning.
func TestARefusedV2ModuleIsNotRegistered(t *testing.T) {
	home := t.TempDir()
	dir := installFakeModule(t, home)
	if err := os.WriteFile(filepath.Join(dir, "contract_version"), []byte("xibodev.module/v3"), 0o644); err != nil {
		t.Fatal(err)
	}

	installed := Discover(context.Background(), home)
	if len(installed) != 1 {
		t.Fatalf("discovered %d modules, want 1", len(installed))
	}
	in := installed[0]
	if in.Err == nil || !contains(in.Err.Error(), "refused") || in.Descriptor != nil {
		t.Fatalf("a refused module was registered: err = %v, descriptor = %v", in.Err, in.Descriptor)
	}
	if contains(in.Err.Error(), ": . ") {
		t.Errorf("the refusal has no reason: %v", in.Err)
	}
	if tools, summaries := countRegistered(t, home); tools != 0 || summaries != 0 {
		t.Fatalf("a refused module contributed %d tools and %d summaries", tools, summaries)
	}
}
