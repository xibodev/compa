package modproto

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// TestZeroValuesMarshalWithoutNull is the regression guard for the protocol's
// "every collection is [] or {}, never null" rule.
//
// It matters because Go makes the wrong thing the default: a nil slice marshals
// as null and a nil map marshals as null, so a module that simply forgets to
// populate a field ships a protocol violation while passing every type check.
// A module author raised this against the first draft, which documented the
// rule without enforcing it.
func TestZeroValuesMarshalWithoutNull(t *testing.T) {
	t.Run("descriptor", func(t *testing.T) {
		var d Descriptor
		d.Normalize()
		assertNoNull(t, mustMarshal(t, d))
	})

	t.Run("envelope", func(t *testing.T) {
		var e Envelope
		e.Normalize()
		assertNoNull(t, mustMarshal(t, e))
	})

	t.Run("envelope with error", func(t *testing.T) {
		e := Envelope{Error: &Error{Code: ErrInvalidRequest}}
		e.Normalize()
		assertNoNull(t, mustMarshal(t, e))
	})

	t.Run("request", func(t *testing.T) {
		var r Request
		r.Normalize()
		// Request.Input is json.RawMessage and is legitimately absent when a
		// capability takes no input, so it is exempt from the no-null rule.
		blob := mustMarshal(t, r)
		var generic map[string]json.RawMessage
		if err := json.Unmarshal(blob, &generic); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		for key, raw := range generic {
			if key == "input" {
				continue
			}
			if string(raw) == "null" {
				t.Errorf("field %q marshalled as null, want [] or {} or a value", key)
			}
		}
	})
}

// TestNormalizeNeverInventsACost guards the single most consequential rule in
// the protocol: unknown cost stays unknown. Normalize fills empty collections,
// and it must not "helpfully" fill a nil cost, because null (unknown) and 0
// (free) drive different approval decisions in the host.
func TestNormalizeNeverInventsACost(t *testing.T) {
	var e Envelope
	e.Normalize()

	if e.Execution.EstimatedCost != nil {
		t.Errorf("Normalize set EstimatedCost to %v, want nil (unknown)", *e.Execution.EstimatedCost)
	}
	if e.Execution.ActualCost != nil {
		t.Errorf("Normalize set ActualCost to %v, want nil (unknown)", *e.Execution.ActualCost)
	}

	blob := string(mustMarshal(t, e))
	if !strings.Contains(blob, `"estimated_cost":null`) {
		t.Errorf("unknown estimated_cost must serialize as null, got: %s", blob)
	}
	if !strings.Contains(blob, `"actual_cost":null`) {
		t.Errorf("unknown actual_cost must serialize as null, got: %s", blob)
	}
}

// TestCostZeroIsDistinctFromUnknown proves the two states are actually
// distinguishable on the wire, in both directions.
func TestCostZeroIsDistinctFromUnknown(t *testing.T) {
	free := 0.0
	withZero := Envelope{Execution: Execution{ActualCost: &free}}
	withZero.Normalize()

	if !strings.Contains(string(mustMarshal(t, withZero)), `"actual_cost":0`) {
		t.Error("a genuinely free call must serialize actual_cost as 0, not null")
	}

	var roundTrip Envelope
	if err := json.Unmarshal([]byte(`{"execution":{"actual_cost":null}}`), &roundTrip); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if roundTrip.Execution.ActualCost != nil {
		t.Error("null actual_cost must decode to nil, not to a zero value")
	}

	if err := json.Unmarshal([]byte(`{"execution":{"actual_cost":0}}`), &roundTrip); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if roundTrip.Execution.ActualCost == nil {
		t.Fatal("actual_cost 0 must decode to a non-nil zero, not to nil")
	}
	if *roundTrip.Execution.ActualCost != 0 {
		t.Errorf("actual_cost = %v, want 0", *roundTrip.Execution.ActualCost)
	}
}

func TestDigest(t *testing.T) {
	got := DigestSHA256([]byte("compa"))
	if !strings.HasPrefix(got, DigestPrefix) {
		t.Errorf("digest %q lacks the mandatory %q prefix", got, DigestPrefix)
	}
	if !ValidDigest(got) {
		t.Errorf("DigestSHA256 produced %q, which ValidDigest rejects", got)
	}

	rejected := []struct{ name, digest string }{
		{"bare hex without prefix", strings.TrimPrefix(got, DigestPrefix)},
		{"uppercase hex", DigestPrefix + strings.ToUpper(strings.TrimPrefix(got, DigestPrefix))},
		{"truncated", got[:20]},
		{"empty", ""},
		{"wrong algorithm", "md5:d41d8cd98f00b204e9800998ecf8427e"},
	}
	for _, tc := range rejected {
		if ValidDigest(tc.digest) {
			t.Errorf("ValidDigest accepted %s (%q), want rejection", tc.name, tc.digest)
		}
	}
}

func TestProtocolIDIsFrozenValue(t *testing.T) {
	// Operator-frozen 2026-09-07, vendor-neutral by decision. Changing this
	// is a cross-lane protocol break requiring every lane to acknowledge it,
	// so it is asserted rather than left to drift.
	const want = "xibodev.module/v1"
	if ProtocolID != want {
		t.Errorf("ProtocolID = %q, want %q (frozen cross-lane identifier)", ProtocolID, want)
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	blob, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return blob
}

// assertNoNull fails if any top-level field marshalled as null.
func assertNoNull(t *testing.T, blob []byte) {
	t.Helper()
	var generic map[string]json.RawMessage
	if err := json.Unmarshal(blob, &generic); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for key, raw := range generic {
		if string(raw) == "null" {
			t.Errorf("field %q marshalled as null, want [] or {} or a value", key)
		}
	}
}

// TestNoNullAnywhereInDescribeResponse is the regression guard for the hole a
// module author found in the first Normalize implementation.
//
// Envelope.Result is json.RawMessage, so by the time Envelope.Normalize runs
// the payload is already opaque bytes. Normalizing only the envelope produced a
// document whose warnings and artifacts were correctly [] while the descriptor
// inside result was still full of nulls -- a fully "normalized" envelope
// wrapping a protocol violation. This walks the ENTIRE document recursively so
// a null at any depth fails.
func TestNoNullAnywhereInDescribeResponse(t *testing.T) {
	// A zero descriptor is the worst case: every collection is nil.
	env, err := NewDescribeEnvelope(&Descriptor{Module: "fake"})
	if err != nil {
		t.Fatalf("NewDescribeEnvelope: %v", err)
	}

	blob := mustMarshal(t, env)

	var tree any
	if err := json.Unmarshal(blob, &tree); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Cost pointers are the deliberate exception: null means unknown. Describe
	// sets them to explicit zero, so nothing here should be null at all.
	assertNoNullDeep(t, tree, "$")
}

// TestNoNullAnywhereInResultEnvelope covers the same hole for capability
// results, which are the far more common case: any module result struct with a
// nil slice hits it.
func TestNoNullAnywhereInResultEnvelope(t *testing.T) {
	// A payload that does NOT implement Normalizer and carries a nil slice.
	// The constructor cannot fix this one -- it proves the constructor does not
	// give false confidence, and that a module must still normalize its own
	// result types or implement Normalizer.
	type resultWithNilSlice struct {
		Items []string `json:"items"`
	}

	free := 0.0
	env, err := NewResultEnvelope("fake", "invoke", "req-1", resultWithNilSlice{}, Execution{
		Local:         true,
		Provider:      "local",
		EstimatedCost: &free,
		ActualCost:    &free,
	})
	if err != nil {
		t.Fatalf("NewResultEnvelope: %v", err)
	}

	// The envelope's own collections must be clean regardless.
	blob := mustMarshal(t, env)
	var generic map[string]json.RawMessage
	if err := json.Unmarshal(blob, &generic); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(generic["warnings"]) == "null" {
		t.Error("warnings marshalled as null")
	}

	// And a payload that DOES implement Normalizer is fully clean, which is the
	// path modules are told to take.
	env2, err := NewResultEnvelope("fake", "invoke", "req-2", &Descriptor{Module: "fake"}, Execution{
		Local: true, Provider: "local", EstimatedCost: &free, ActualCost: &free,
	})
	if err != nil {
		t.Fatalf("NewResultEnvelope: %v", err)
	}
	var tree any
	if err := json.Unmarshal(mustMarshal(t, env2), &tree); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	assertNoNullDeep(t, tree, "$")
}

// TestErrorEnvelopeReportsUnknownCostNotFree guards the safe default: a failure
// with no known cost must report null, never zero, because a failed paid call
// may still have billed.
func TestErrorEnvelopeReportsUnknownCostNotFree(t *testing.T) {
	env := NewErrorEnvelope("fake", "invoke", "req-3", Error{
		Code:    ErrProviderFailure,
		Message: "provider rejected the request",
	}, Execution{Network: true, Provider: "someprovider"})

	if env.OK {
		t.Error("error envelope must not report ok:true")
	}
	if env.Error.Details == nil {
		t.Error("Error.Details must be {} rather than null")
	}
	if env.Execution.ActualCost != nil {
		t.Error("a failure with unknown cost must report null, not a number")
	}

	blob := string(mustMarshal(t, env))
	if !strings.Contains(blob, `"actual_cost":null`) {
		t.Errorf("unknown cost must serialize as null, got: %s", blob)
	}
	if !strings.Contains(blob, `"details":{}`) {
		t.Errorf("details must serialize as {}, got: %s", blob)
	}
}

func TestConstructorsStampProtocolAndEchoRequestID(t *testing.T) {
	free := 0.0
	env, err := NewResultEnvelope("fake", "invoke", "req-verbatim-123", &Descriptor{}, Execution{
		Local: true, EstimatedCost: &free, ActualCost: &free,
	})
	if err != nil {
		t.Fatalf("NewResultEnvelope: %v", err)
	}
	if env.Protocol != ProtocolID {
		t.Errorf("Protocol = %q, want %q", env.Protocol, ProtocolID)
	}
	// Host-generated, echoed verbatim: the host correlates on this without
	// trusting the module to generate a unique value.
	if env.RequestID != "req-verbatim-123" {
		t.Errorf("RequestID = %q, want it echoed verbatim", env.RequestID)
	}

	d := &Descriptor{Module: "fake"}
	denv, err := NewDescribeEnvelope(d)
	if err != nil {
		t.Fatalf("NewDescribeEnvelope: %v", err)
	}
	if denv.Operation != "describe" {
		t.Errorf("Operation = %q, want %q", denv.Operation, "describe")
	}
	if denv.RequestID != "" {
		t.Errorf("describe RequestID = %q, want empty", denv.RequestID)
	}
	if denv.Execution.ActualCost == nil || *denv.Execution.ActualCost != 0 {
		t.Error("describe is genuinely free and must report 0, not null")
	}
}

// assertNoNullDeep walks a decoded JSON tree and fails on any null, reporting
// the path so a failure names the offending field.
func assertNoNullDeep(t *testing.T, node any, path string) {
	t.Helper()
	switch v := node.(type) {
	case nil:
		t.Errorf("null at %s, want [] or {} or a value", path)
	case map[string]any:
		for key, child := range v {
			assertNoNullDeep(t, child, path+"."+key)
		}
	case []any:
		for i, child := range v {
			assertNoNullDeep(t, child, fmt.Sprintf("%s[%d]", path, i))
		}
	}
}

// TestSchemaKeyMustAgreeWithDocumentID guards a silent failure mode that a
// resolve-by-key check cannot see.
//
// A descriptor can key its schemas by one scheme while the documents declare
// another. Every capability reference then resolves, and the host validates
// requests against a schema describing something else entirely -- producing
// confident wrong answers rather than a loud failure. A module author found
// this gap in their own validator; the host had it too.
func TestSchemaKeyMustAgreeWithDocumentID(t *testing.T) {
	newDescriptor := func(doc string) *Descriptor {
		d := &Descriptor{
			Module: "fake", Name: "Fake", Version: "1.0.0",
			ProtocolVersions: []string{ProtocolID},
			Capabilities: []Capability{{
				ID: "fake.echo", Title: "Echo", Summary: "one line",
				RequestSchema: "fake.echo.request/v1",
				Effects:       Effects{Local: true, CostKnown: true},
			}},
			RequestSchemas: map[string]json.RawMessage{
				"fake.echo.request/v1": json.RawMessage(doc),
			},
		}
		d.Normalize()
		return d
	}

	// A NAMESPACED $id is accepted: the document's own identity may live in
	// the module's namespace while the key is how this descriptor references
	// it. Requiring equality rejected a real rendering module, whose schemas are
	// keyed "render_report" and declare $id "openmontage/artifacts/render_report".
	if err := ValidateDescriptor(newDescriptor(`{"$id":"openmontage/artifacts/fake.echo.request/v1","type":"object"}`)); err != nil {
		t.Errorf("a namespaced $id naming the same schema must be accepted: %v", err)
	}

	// Disagreeing $id: rejected.
	err := ValidateDescriptor(newDescriptor(`{"$id":"totally.different/v9","type":"object"}`))
	if err == nil {
		t.Fatal("a schema whose $id disagrees with its map key was accepted; references would resolve to the wrong schema")
	}
	if !strings.Contains(err.Error(), "$id") {
		t.Errorf("error should name the offending field, got: %v", err)
	}

	// Agreeing $id: accepted.
	if err := ValidateDescriptor(newDescriptor(`{"$id":"fake.echo.request/v1","type":"object"}`)); err != nil {
		t.Errorf("a schema whose $id matches its key must be accepted: %v", err)
	}

	// Absent $id: accepted. A schema is legitimately identified by its key
	// alone, so requiring $id would reject valid descriptors.
	if err := ValidateDescriptor(newDescriptor(`{"type":"object"}`)); err != nil {
		t.Errorf("a schema without $id must be accepted; the key identifies it: %v", err)
	}
}

// TestNestedCollectionsAreNeverNull checks the no-null rule where it actually
// fails.
//
// The rule is stated about the DOCUMENT, but Go enforces it per-STRUCT. Anyone
// verifying "my descriptor has no nulls" inspects the outer struct and the
// check passes, while nested collections inside capabilities marshal as null.
// This shape has now appeared three times across three lanes, which makes it
// the rule's default failure rather than anyone's carelessness. The only place
// the difference is visible is the marshalled JSON, so that is what this walks.
func TestNestedCollectionsAreNeverNull(t *testing.T) {
	withCapability := func() *Descriptor {
		return &Descriptor{
			Module: "fake", Name: "Fake", Version: "1.0.0",
			ProtocolVersions: []string{ProtocolID},
			Capabilities: []Capability{{
				ID: "fake.echo", Title: "Echo", Summary: "one line",
				Effects: Effects{Local: true, CostKnown: true},
				// artifact_schemas and skills deliberately left nil.
			}},
		}
	}

	// The inverse assertion: without it, this test would pass whether or not
	// Normalize did anything at all -- green for a reason unrelated to the
	// property under test.
	unnormalized := mustMarshal(t, withCapability())
	if !strings.Contains(string(unnormalized), `"artifact_schemas":null`) {
		t.Fatal("an un-normalized descriptor must produce nested nulls, or this test proves nothing about Normalize")
	}

	d := withCapability()
	d.Normalize()

	var tree any
	if err := json.Unmarshal(mustMarshal(t, d), &tree); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	assertNoNullDeep(t, tree, "$")
}

// TestValidatorAcceptsAMinimalPeerDescriptor guards against house rules
// leaking into protocol validation.
//
// The distinction, raised by a module author after their own validator
// rejected a legal descriptor: a validator must separate "what THIS module
// publishes" from "what the PROTOCOL accepts". The first may be as strict as an
// author likes; only the second may reject another module's work. Enforcing a
// house rule as a protocol rule rejects valid peers -- the mirror image of
// accepting whatever a caller supplies.
//
// So this asserts the FLOOR: the least a conforming module can send and still
// be accepted. Anything failing here is the host being over-strict, which is a
// bug even though it looks like rigour.
func TestValidatorAcceptsAMinimalPeerDescriptor(t *testing.T) {
	d := &Descriptor{
		Module: "peer", Name: "Peer Module", Version: "0.1.0",
		ProtocolVersions: []string{ProtocolID},
		Capabilities: []Capability{{
			ID: "peer.do", Title: "Do", Summary: "one line",
			Effects: Effects{Local: true, CostKnown: true},
			// No schemas, no skills, no artifact schemas: a module offering a
			// schema-less capability is unusual, not illegal.
		}},
		// No overlays and no skills: a module contributing no agent knowledge
		// is perfectly conforming.
	}
	d.Normalize()

	if err := ValidateDescriptor(d); err != nil {
		t.Errorf("the minimal conforming descriptor was rejected; the host is imposing a house rule: %v", err)
	}
}

// TestValidatorAcceptsMinimalArtifacts asserts the same floor for artifacts:
// optional fields are optional, and a legitimately empty file is legal.
func TestValidatorAcceptsMinimalArtifacts(t *testing.T) {
	free := 0.0
	build := func(mutate func(*Artifact)) *Envelope {
		a := Artifact{
			ID: "a", Kind: "peer.thing/v1", Path: "out.json", Root: "ws",
			MediaType: "application/json", Bytes: 1,
			Digest: DigestSHA256([]byte("x")),
		}
		mutate(&a)
		env, err := NewResultEnvelope("peer", OperationInvoke, "req_x",
			map[string]any{"x": 1},
			Execution{
				Local: true, Provider: "local",
				EstimatedCost: &free, ActualCost: &free,
				Artifacts: []Artifact{a},
			})
		if err != nil {
			t.Fatalf("NewResultEnvelope: %v", err)
		}
		return &env
	}

	cases := map[string]func(*Artifact){
		"no title":                func(a *Artifact) { a.Title = "" },
		"no media type":           func(a *Artifact) { a.MediaType = "" },
		"zero bytes (empty file)": func(a *Artifact) { a.Bytes = 0 },
	}
	for name, mutate := range cases {
		if err := ValidateEnvelope(build(mutate), OperationInvoke, "req_x"); err != nil {
			t.Errorf("%s was rejected; optional fields must stay optional: %v", name, err)
		}
	}
}
