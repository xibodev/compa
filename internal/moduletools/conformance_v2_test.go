package moduletools

import (
	"strings"
	"testing"

	"github.com/xibodev/compa/pkg/modprotov2"
)

// A weakening projection is REFUSED BY THE HOST, end to end, from raw describe
// bytes through the pin to the verdict.
//
// This is the triangle the operator named:
//
//	product semantic declaration -> v2 projection -> host interpretation
//
// The unit tests in modprotov2 prove the comparison works. What this proves is
// that the HOST runs it -- a correct check nothing calls is the defect this
// repo has shipped twice and recorded both times.
func TestTheHostRefusesAWeakeningProjectionEndToEnd(t *testing.T) {
	// Operation charges for two kinds; the capability declares it never
	// charges. The Operation reads perfectly correct in isolation.
	raw := []byte(`{
	  "protocol": "xibodev.module/v2",
	  "contract_version": "xibodev.module/v2",
	  "module": "archive",
	  "operations": [{
	    "id": "produce_content",
	    "effects": {"may_charge": {"field":"kind","when_in":["tutorial","adr"],"default":false}},
	    "approval": {"required_when": ["chargeable"]}
	  }],
	  "capabilities": [{
	    "id": "content.produce",
	    "projects": ["produce_content"],
	    "effects": {"may_charge": false}
	  }]
	}`)

	dec, err := modprotov2.Evaluate(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !dec.MayRelyOnV2() {
		t.Fatalf("the pin refused a correctly-declared v2 module: %v", dec.Pin.Reason)
	}

	conf := CheckHostConformance(*dec.V2)
	if conf.Conforms() {
		t.Fatal("the host accepted a capability declaring may_charge:false while" +
			" projecting a charging Operation. The gate reads capability" +
			" effects, so this would spend money through a surface declaring it" +
			" cannot -- while the Operation still reads correct to a reviewer")
	}

	// The refusal must name the module, the location, and what to do.
	ref := conf.Refusal()
	for _, want := range []string{"archive", "content.produce", "may_charge_weakened", "collapse it to true"} {
		if !strings.Contains(ref, want) {
			t.Errorf("the refusal omits %q, so an author cannot act on it:\n%s", want, ref)
		}
	}
}

// The honest version of the same module passes, so the test above fails for the
// WEAKENING and not for some unrelated schema error.
//
// Without this, a validator that rejected every descriptor would pass the test
// above and look like a working conformance check.
func TestTheSameModuleDeclaredHonestlyConforms(t *testing.T) {
	raw := []byte(`{
	  "protocol": "xibodev.module/v2",
	  "contract_version": "xibodev.module/v2",
	  "module": "archive",
	  "operations": [{
	    "id": "produce_content",
	    "effects": {"may_charge": {"field":"kind","when_in":["tutorial","adr"],"default":false}},
	    "approval": {"required_when": ["chargeable"]}
	  }],
	  "capabilities": [{
	    "id": "content.produce",
	    "projects": ["produce_content"],
	    "effects": {"may_charge": {"field":"kind","when_in":["tutorial","adr"],"default":false}}
	  }]
	}`)

	dec, err := modprotov2.Evaluate(raw)
	if err != nil {
		t.Fatal(err)
	}
	conf := CheckHostConformance(*dec.V2)
	if !conf.Conforms() {
		t.Fatalf("an honest projection was refused:\n%s", conf.Refusal())
	}
}

// A conforming module's gate then answers per invocation, using the same
// declaration that just passed conformance.
//
// The two halves are tested together here because they are only sound as a
// pair: conformance guarantees the capability is no weaker than its Operation,
// which is what makes it safe for the gate to read capability effects alone.
func TestConformanceAndTheGateComposeOnOneRealDeclaration(t *testing.T) {
	raw := []byte(`{
	  "protocol": "xibodev.module/v2",
	  "contract_version": "xibodev.module/v2",
	  "module": "archive",
	  "operations": [{
	    "id": "produce_content",
	    "effects": {"may_charge": {"field":"kind","when_in":["tutorial"],"default":false}},
	    "approval": {"required_when": ["chargeable"]}
	  }],
	  "capabilities": [{
	    "id": "content.produce",
	    "projects": ["produce_content"],
	    "effects": {"may_charge": {"field":"kind","when_in":["tutorial"],"default":false}}
	  }]
	}`)

	dec, err := modprotov2.Evaluate(raw)
	if err != nil {
		t.Fatal(err)
	}
	if conf := CheckHostConformance(*dec.V2); !conf.Conforms() {
		t.Fatalf("setup module does not conform:\n%s", conf.Refusal())
	}

	d := dec.V2.Descriptor()
	c := &d.Capabilities[0]

	if !NeedsApprovalV2(d, c, map[string]any{"kind": "tutorial"}).Required {
		t.Error("a charging invocation was not gated")
	}
	if NeedsApprovalV2(d, c, map[string]any{"kind": "retrieval_pack"}).Required {
		t.Error("a free invocation was gated, so section 3a's benefit does not" +
			" survive to the host")
	}
}

// A zero-valued Accepted must not read as conforming.
//
// "No findings" and "nothing was checked" are otherwise the same observable --
// the defect family this whole contract exists to close, and it would appear
// here as a module that silently conforms because nothing looked at it.
func TestAnUncheckedDescriptorDoesNotReadAsConforming(t *testing.T) {
	var empty modprotov2.Accepted

	conf := CheckHostConformance(empty)
	if conf.Conforms() {
		t.Fatal("an Accepted holding no descriptor reported conformance, so" +
			" 'nothing was checked' is indistinguishable from 'nothing was wrong'")
	}
	if !strings.Contains(conf.Refusal(), "nothing_checked") {
		t.Errorf("the refusal does not say the check never ran: %s", conf.Refusal())
	}
}

// Every finding is reported, not just the first.
//
// A validator that stops at one turns a single bad descriptor into as many
// rebuild cycles as it has defects, and hides whether they share a cause.
func TestAllFindingsAreReportedNotJustTheFirst(t *testing.T) {
	raw := []byte(`{
	  "protocol": "xibodev.module/v2",
	  "contract_version": "xibodev.module/v2",
	  "module": "broken",
	  "artifact_kinds": {
	    "a": {"kind": "document", "media_type": "application/json"},
	    "b": {"kind": "media", "media_type": "video/mp4",
	          "validator": {"type":"json_schema","schema":"x/v1"}}
	  },
	  "operations": [{"id":"op","requirements":[{"kind":"binary","name":"d2"}]}],
	  "capabilities": []
	}`)

	dec, err := modprotov2.Evaluate(raw)
	if err != nil {
		t.Fatal(err)
	}
	conf := CheckHostConformance(*dec.V2)

	if len(conf.Findings) < 3 {
		t.Fatalf("expected at least three distinct findings (document without"+
			" validator, media with validator, requirement without strength),"+
			" got %d:\n%s", len(conf.Findings), conf.Refusal())
	}
}
