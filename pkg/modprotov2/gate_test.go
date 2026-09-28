package modprotov2

import (
	"strings"
	"testing"

	"github.com/xibodev/compa/pkg/contractv2"
	"github.com/xibodev/compa/pkg/modproto"
)

// The settled version matrix, exercised through the REAL entry point rather
// than by calling CheckPin directly.
//
// contractv2 already pins CheckPin's own behaviour. What this pins is the thing
// a unit test of CheckPin cannot see: that the exchange path actually consults
// it, and that a refusal yields NO reachable descriptor of either contract. A
// gate that returns the right answer to a caller who then proceeds anyway is
// not a gate.
func TestTheVersionMatrixHoldsThroughTheExchangePath(t *testing.T) {
	body := func(contract string) []byte {
		if contract == "" {
			return []byte(`{"protocol":"xibodev.module/v1","module":"m",
			  "protocol_versions":["xibodev.module/v1"],"capabilities":[],
			  "request_schemas":{},"result_schemas":{}}`)
		}
		return []byte(`{"protocol":"xibodev.module/v2","contract_version":"` +
			contract + `","module":"m","operations":[],"capabilities":[]}`)
	}

	for _, tc := range []struct {
		declared string
		served   bool
		v2       bool
		why      string
	}{
		{"", true, false,
			"absent is a v1 module: served under v1, and NO v2 guarantee. Refusing" +
				" it would break every module shipped today"},
		{"xibodev.module/v2", true, true,
			"the exact pin, and the only value that may reach v2 semantics"},
		{"xibodev.module/v1", false, false,
			"the WIRE identity is not a behavioural contract; serving it would put" +
				" a name in a conformance record that names no guarantees"},
		{"xibodev.module/v3", false, false,
			"an unknown contract is a disagreement about MEANING, not a fallback"},
		{"xibodev.module/v2-beta", false, false,
			"near-misses are refused; the pin is exact"},
		{" xibodev.module/v2", false, false,
			"whitespace is not trimmed into a match"},
	} {
		dec, err := Evaluate(body(tc.declared))
		if err != nil {
			t.Fatalf("%q: %v", tc.declared, err)
		}

		if dec.Served() != tc.served {
			t.Errorf("%q: served=%v want %v -- %s", tc.declared, dec.Served(), tc.served, tc.why)
		}
		if dec.MayRelyOnV2() != tc.v2 {
			t.Errorf("%q: v2=%v want %v -- %s", tc.declared, dec.MayRelyOnV2(), tc.v2, tc.why)
		}

		// THE STRUCTURAL HALF. Reachability must match the decision: a refused
		// module must expose NO descriptor, and a v1 module must expose no v2
		// one. Otherwise a caller could hold v2 Operations from a counterparty
		// that never agreed to them, which is what the gate exists to prevent.
		if !tc.v2 && dec.V2 != nil {
			t.Errorf("%q: a non-v2 outcome still produced a reachable v2"+
				" descriptor, so v2 semantics are available without the pin", tc.declared)
		}
		if tc.v2 && dec.V2 == nil {
			t.Errorf("%q: the exact pin produced no v2 descriptor", tc.declared)
		}
		if !tc.served && dec.V1 != nil {
			t.Errorf("%q: a REFUSED module produced a reachable v1 descriptor,"+
				" so a refusal is being served after all", tc.declared)
		}
	}
}

// An absent contract_version decodes with the V1 TYPES, never with v2 types
// left zero-valued.
//
// This is section 10's "absent v2 fields fall back to v1 behaviour, never to a
// more permissive default" made mechanical. A zero-valued v2 Effects reads as
// an affirmative declaration of no network, no charge, AND determinism -- three
// claims the module never made, the last of which would offer re-execution as
// free recovery for something that may not be repeatable.
func TestAV1ModuleIsNotDecodedIntoZeroValuedV2Semantics(t *testing.T) {
	raw := []byte(`{"protocol":"xibodev.module/v1","module":"archive",
	  "protocol_versions":["xibodev.module/v1"],
	  "capabilities":[{"id":"content.produce","title":"t","summary":"s",
	    "request_schema":"a","result_schema":"b","artifact_schemas":[],"skills":[],
	    "effects":{"cost_known":false}}],
	  "request_schemas":{"a":{}},"result_schemas":{"b":{}}}`)

	dec, err := Evaluate(raw)
	if err != nil {
		t.Fatal(err)
	}
	if dec.V1 == nil {
		t.Fatal("a v1 module produced no v1 descriptor")
	}
	if dec.V2 != nil {
		t.Fatal("a v1 module produced a v2 descriptor with unset fields. Those" +
			" zero values would read as declarations the module never made --" +
			" deterministic:true among them")
	}
	if _, ok := interface{}(dec.V1).(*modproto.Descriptor); !ok {
		t.Fatal("the v1 path did not use the v1 types")
	}
}

// The ONLY way to obtain a v2 descriptor is through the gate.
//
// Enforced by the type rather than by a comment: Accepted holds an unexported
// field, so no caller outside this package can construct one. A comment saying
// "call CheckPin first" holds only until someone adds a call site that does not
// read it; this holds because the compiler will not allow otherwise.
func TestV2SemanticsAreUnreachableWithoutPassingTheGate(t *testing.T) {
	dec, err := Evaluate([]byte(`{"contract_version":"xibodev.module/v3"}`))
	if err != nil {
		t.Fatal(err)
	}
	if dec.V2 != nil {
		t.Fatal("a refused contract produced a reachable v2 descriptor")
	}

	// And the accessor on a zero Accepted cannot leak a usable descriptor.
	var empty Accepted
	if empty.Descriptor() != nil {
		t.Error("a zero-valued Accepted exposed a descriptor, so v2 semantics" +
			" are reachable without evaluation")
	}
}

// A refusal must carry reason AND remedy through to the caller.
func TestARefusalCarriesReasonAndRemedyThroughTheExchange(t *testing.T) {
	dec, err := Evaluate([]byte(`{"contract_version":"xibodev.module/v3"}`))
	if err != nil {
		t.Fatal(err)
	}
	if dec.Pin.Reason == "" || dec.Pin.Remedy == "" {
		t.Fatalf("a refusal reached the caller without reason or remedy: %+v", dec.Pin)
	}
	// It must rule out negotiation explicitly: that is where a reader forms
	// the wrong expectation, and v1 shipped a doc comment that did exactly that.
	low := strings.ToLower(dec.Pin.Remedy)
	for _, want := range []string{"negotiate", "downgrade", "fall back"} {
		if !strings.Contains(low, want) {
			t.Errorf("the remedy does not rule out %q: %q", want, dec.Pin.Remedy)
		}
	}
}

// The exchange path must not re-derive the outcome; it must delegate.
//
// Two places computing the same three-state answer is the duplicated-decision
// family this codebase keeps finding. This asserts the Decision agrees with
// contractv2 for every input, so a second implementation cannot drift in.
func TestTheExchangeDelegatesToTheGateRatherThanReimplementingIt(t *testing.T) {
	for _, declared := range []string{
		"", "xibodev.module/v2", "xibodev.module/v1", "xibodev.module/v3", "V2",
	} {
		dec, err := Evaluate([]byte(`{"contract_version":"` + declared + `"}`))
		if err != nil {
			t.Fatalf("%q: %v", declared, err)
		}
		want := contractv2.CheckPin(declared)
		if dec.Pin.Outcome != want.Outcome {
			t.Errorf("%q: exchange concluded %v, CheckPin says %v -- two"+
				" implementations of one decision", declared, dec.Pin.Outcome, want.Outcome)
		}
	}
}
