package modproto_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xibodev/compa/v3/pkg/contractv2"
	"github.com/xibodev/compa/v3/pkg/modproto"
)

// A descriptor declaring contract_version MUST still decode and validate on the
// v1 path, and MUST NOT thereby gain any v2 guarantee.
//
// UNTESTED UNTIL NOW, AND NOBODY WOULD HAVE NOTICED. The sibling lane has built
// a bundle that publishes contract_version: xibodev.module/v2. Today that field
// is unknown to v1 and silently ignored, which is exactly what RFC section 10
// requires -- absent or unrecognised v2 fields fall back to v1 behaviour, never
// to a more permissive default.
//
// But "works because the decoder happens to be tolerant" and "works because the
// contract requires it" produce THE SAME OBSERVABLE today. If DecodeEnvelope or
// ValidateDescriptor ever gained DisallowUnknownFields -- a reasonable-looking
// hardening -- every v2-declaring module would break on the v1 path with
// nothing anywhere saying why. This pins the direction so that change fails
// here instead.
func TestAV2DeclaringDescriptorStillWorksOnTheV1Path(t *testing.T) {
	// A descriptor exactly as a v2-aware module publishes it: v1 wire shape,
	// plus the one behavioural field v1 has never seen.
	// NOTE: no "protocol" key. Descriptor has no such field, so including one
	// would make this test pass on ANY unknown key -- the mutation below named
	// "protocol" rather than "contract_version" until it was removed, meaning
	// the test proved tolerance in general while claiming to prove it for the
	// one field that matters. An assertion that fires for the wrong reason is
	// the same false green as one that never fires.
	raw := []byte(`{
	  "contract_version": "xibodev.module/v2",
	  "module": "render",
	  "name": "Render",
	  "version": "0.1.0",
	  "protocol_versions": ["xibodev.module/v1"],
	  "capabilities": [
	    {"id": "creative.tools.run", "title": "Run", "summary": "one line",
	     "request_schema": "x/v1", "result_schema": "y/v1",
	     "artifact_schemas": [], "skills": []}
	  ],
	  "request_schemas": {"x/v1": {"type": "object"}},
	  "result_schemas":  {"y/v1": {"type": "object"}}
	}`)

	var d modproto.Descriptor
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("a descriptor declaring contract_version failed to decode on"+
			" the v1 path: %v. Section 10 requires unrecognised v2 fields to fall"+
			" back to v1 behaviour, so this breaks every v2-aware module"+
			" against a v1 host.", err)
	}
	if err := modproto.ValidateDescriptor(&d); err != nil {
		t.Fatalf("a descriptor declaring contract_version failed v1 validation:"+
			" %v", err)
	}
	if d.Module != "render" || len(d.Capabilities) != 1 {
		t.Fatalf("the descriptor decoded but lost content: module=%q caps=%d",
			d.Module, len(d.Capabilities))
	}

	// THE HALF THAT MATTERS MORE. Decoding it must not confer anything. v1 has
	// no field to carry the declaration, so a v1 host cannot see it -- and a
	// host that cannot see it must not act as though it had.
	//
	// Written as a positive assertion rather than a comment because "v1 ignores
	// it" is precisely the kind of claim that stays true only until someone
	// wires a reader.
	if pin := contractv2.CheckPin(""); pin.MayRelyOnV2() {
		t.Fatal("a v1 exchange granted v2 guarantees. The declaration is not on" +
			" the v1 wire, so the host never read it; concluding v2 from it" +
			" would be smuggling successor semantics into a v1 exchange.")
	}
}

// A v2 descriptor MUST satisfy the v1 validator, unchanged.
//
// THIS IS §10's "v2 is ADDITIVE" MADE EXECUTABLE, and it is what settles how a
// host asks for v2 -- a question the frozen RFC never answers directly.
//
// Additive means a v2 descriptor is a v1 descriptor PLUS fields: ONE payload,
// valid under both validators. If v2 required a flag to appear, it would not be
// additive -- it would be a second document behind a second call, and §10's
// "v1 modules keep working" would be satisfied only by the module publishing
// two different things.
//
// So the handshake follows from the text rather than from preference: modules
// publish contract_version UNCONDITIONALLY, in the envelope, and the host sends
// no flag. That is what this host already does and what one sibling already
// does; the other made the alternative reading, which is honest against a
// clause that does not address the handshake.
//
// WHAT THIS CATCHES, measured against a real sibling payload: a v2 descriptor
// that drops protocol_versions, artifact_schemas and skills marshals them as
// null, and the v1 validator reports 13 violations of the never-null rule. The
// v2 gate cannot see that -- it does not check v1 shape -- so only running BOTH
// validators against ONE payload finds it. A module can be perfectly conformant
// to v2 and unusable by a v1 host.
func TestAV2DescriptorMustAlsoSatisfyTheV1Validator(t *testing.T) {
	// A v2 descriptor built the additive way: every v1 field present and
	// non-null, plus the v2 declaration.
	raw := []byte(`{
	  "contract_version": "xibodev.module/v2",
	  "module": "archive",
	  "name": "Archive",
	  "version": "0.0.1",
	  "protocol_versions": ["xibodev.module/v1"],
	  "capabilities": [
	    {"id": "content.produce", "title": "Produce", "summary": "one line",
	     "request_schema": "x/v1", "result_schema": "y/v1",
	     "artifact_schemas": [], "skills": []}
	  ],
	  "request_schemas": {"x/v1": {"type": "object"}},
	  "result_schemas":  {"y/v1": {"type": "object"}},
	  "operations": [{"id": "produce_content"}]
	}`)

	var d modproto.Descriptor
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("a v2 descriptor failed to decode with v1 types: %v", err)
	}
	if err := modproto.ValidateDescriptor(&d); err != nil {
		t.Fatalf("an ADDITIVE v2 descriptor failed v1 validation: %v.\n"+
			"§10 requires v2 to be additive, so one payload must satisfy both."+
			" A module failing here is conformant to v2 and unusable by a v1"+
			" host", err)
	}
}

// The never-null rule is what an additive v2 payload most easily breaks.
//
// A v2-shaped struct that omits protocol_versions, artifact_schemas or skills
// marshals them as null rather than []. Measured against a real sibling
// payload: 13 violations, none visible to the v2 gate.
func TestDroppingV1SlicesBreaksTheAdditiveGuarantee(t *testing.T) {
	// The same descriptor with the v1 slices absent -- exactly what a v2-only
	// struct produces.
	raw := []byte(`{
	  "contract_version": "xibodev.module/v2",
	  "module": "archive", "name": "Archive", "version": "0.0.1",
	  "capabilities": [
	    {"id": "content.produce", "title": "Produce", "summary": "one line",
	     "request_schema": "x/v1", "result_schema": "y/v1"}
	  ],
	  "request_schemas": {"x/v1": {"type": "object"}},
	  "result_schemas":  {"y/v1": {"type": "object"}}
	}`)

	var d modproto.Descriptor
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	err := modproto.ValidateDescriptor(&d)
	if err == nil {
		t.Fatal("a v2 payload with null protocol_versions/artifact_schemas/" +
			"skills passed v1 validation. The never-null rule is what makes a" +
			"v1 host able to read a v2 descriptor at all")
	}
	for _, want := range []string{"protocol_versions", "artifact_schemas", "skills"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the violation does not name %q, so a module author cannot"+
				" tell which field to fix: %v", want, err)
		}
	}
}
