package modproto

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// These pin the CORRECTED v1 documentation against the behaviour that actually
// exists.
//
// Three doc comments in this package were written as contract and were false:
// version negotiation, artifact validation, and what cost_known means. Two
// sibling lanes each built on one of them and had to retract. The comments were
// not lying about anything subtle -- nobody had checked them since the code
// underneath changed.
//
// A comment cannot be compiled, so the only way to keep one honest is to assert
// on it. These tests fail if the false claims come back.

func protocolSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("protocol.go")
	if err != nil {
		t.Fatalf("read protocol.go: %v", err)
	}
	return string(b)
}

// validate.go performs a MEMBERSHIP TEST. It does not select a version, and
// there is nothing to select from: the host speaks exactly one protocol ID.
//
// The old comment claimed the host "selects the highest it also supports". A
// sibling inferred a whole versioning story from that sentence.
func TestTheDocsDoNotClaimVersionNegotiation(t *testing.T) {
	src := protocolSource(t)
	for _, banned := range []string{
		"selects the highest",
		"highest it also supports",
	} {
		if strings.Contains(src, banned) {
			t.Errorf("protocol.go claims %q, but validate.go is a membership"+
				" test against one frozen constant -- there is no negotiation", banned)
		}
	}
}

// Proof rather than assertion: a descriptor declaring a HIGHER version the host
// does not know is refused, not negotiated down to a common one. If negotiation
// existed, this would succeed.
func TestDeclaringAnotherVersionIsInertNotNegotiated(t *testing.T) {
	descriptorSpeaking := func(versions ...string) *Descriptor {
		d := &Descriptor{
			Module: "fake", Name: "Fake", Version: "1.0.0",
			ProtocolVersions: versions,
			Capabilities: []Capability{{
				ID: "fake.echo", Title: "Echo", Summary: "one line",
				RequestSchema: "fake.echo.request/v1",
				Effects:       Effects{Local: true, CostKnown: true},
			}},
			RequestSchemas: map[string]json.RawMessage{
				"fake.echo.request/v1": json.RawMessage(
					`{"$id":"fake.echo.request/v1","type":"object"}`),
			},
		}
		d.Normalize()
		return d
	}

	// Listing the host's ID alongside others is accepted -- the extras are
	// simply ignored, which is what "inert" means.
	if err := ValidateDescriptor(descriptorSpeaking("xibodev.module/v99", ProtocolID)); err != nil {
		t.Fatalf("a descriptor listing the host's ID alongside another was"+
			" refused: %v", err)
	}

	// And without the host's exact ID, no amount of other versions helps. A
	// negotiating host would find a common version or explain the mismatch;
	// this one just fails the membership test.
	if err := ValidateDescriptor(descriptorSpeaking("xibodev.module/v99", "xibodev.module/v2")); err == nil {
		t.Fatal("a module speaking only OTHER versions was accepted, which" +
			" would mean the host negotiated -- it does not")
	}
}

// Nothing validates produced artifacts against artifact_schemas. The old
// comment said the host does it "before rendering a card"; a sibling was
// repairing its declarations toward that guarantee.
func TestTheDocsDoNotClaimArtifactValidation(t *testing.T) {
	src := protocolSource(t)
	if strings.Contains(src, "validates produced artifacts") {
		t.Error("protocol.go claims produced artifacts are validated against" +
			" artifact_schemas; there are no readers of that map outside" +
			" validate.go's ID-presence check")
	}
}

// cost_known means "is a number known". It does not mean "may spend money".
// The host's approval policy keying on it is a HOST policy, not the field's
// meaning, and the corrected comment must keep those separate.
func TestTheDocsSeparateCostKnowledgeFromChargeability(t *testing.T) {
	src := protocolSource(t)
	i := strings.Index(src, "CostKnown bool")
	if i < 0 {
		t.Fatal("CostKnown field not found")
	}
	start := strings.LastIndex(src[:i], "\n\n")
	if start < 0 {
		start = 0
	}
	doc := src[start:i]

	if !strings.Contains(doc, "NUMERIC MONETARY COST IS KNOWN") {
		t.Error("the CostKnown comment no longer states that the field is" +
			" about whether an AMOUNT is known")
	}
	if !strings.Contains(doc, "HOST POLICY") {
		t.Error("the CostKnown comment no longer distinguishes this host's" +
			" gating policy from the meaning of the field, which is the" +
			" conflation that made the original false")
	}
}

// A declared provider of "varies" is NOT a contradiction, and every other
// mismatch still is.
//
// FOUND BY A SIBLING LANE RUNNING A CAPABILITY WITH EFFECTS through the real
// launcher -- the registry reads we had both been invoking have no provider to
// contradict, so this was invisible until something actually dispatched.
//
// Their creative.tools.run reaches 35 tools across twelve providers, and the
// capability description is read BEFORE the request selects which. So "varies"
// can never equal any reported value and the comparison warned on every
// non-local run, permanently, against a correct module.
//
// The asymmetry was mine: this rule already exempted an unclaimed REPORTED
// value ("") and did not exempt an unclaimed DECLARED one. The defect it
// exists to catch is UNDERSTATEMENT -- declare harmless, reach something that
// bills -- and "varies" understates nothing.
func TestADeclaredProviderOfVariesIsNotAContradiction(t *testing.T) {
	descriptorWithProvider := func(declared string) *Descriptor {
		return &Descriptor{
			Module:           "example.render",
			ProtocolVersions: []string{ProtocolID},
			Capabilities: []Capability{{
				ID: "creative.tools.run", Title: "Run", Summary: "one line",
				RequestSchema: "a", ResultSchema: "b",
				ArtifactSchemas: []string{}, Skills: []string{},
				Effects: Effects{Provider: declared, CostKnown: true},
			}},
			RequestSchemas: map[string]json.RawMessage{"a": json.RawMessage(`{}`)},
			ResultSchemas:  map[string]json.RawMessage{"b": json.RawMessage(`{}`)},
		}
	}
	exec := func(reported string) *Execution {
		zero := 0.0
		return &Execution{
			Local: true, Provider: reported,
			EstimatedCost: &zero, ActualCost: &zero,
			Artifacts: []Artifact{},
		}
	}
	providerWarning := func(d *Descriptor, e *Execution) bool {
		w, err := CheckExecutionAgainstDeclared(d, "creative.tools.run", e)
		if err != nil {
			t.Fatalf("unexpected hard error: %v", err)
		}
		for _, f := range w {
			if strings.Contains(f.Path, "provider") {
				return true
			}
		}
		return false
	}

	t.Run("varies against a real provider is silent", func(t *testing.T) {
		if providerWarning(descriptorWithProvider(ProviderVaries), exec("ffprobe")) {
			t.Fatalf("a capability declaring %q was warned about reporting"+
				" ffprobe. It names no provider to contradict -- the reported"+
				" value is MORE specific than declared, not less",
				ProviderVaries)
		}
	})

	t.Run("a NAMED provider still has to match", func(t *testing.T) {
		if !providerWarning(descriptorWithProvider("local-ffmpeg"), exec("elevenlabs")) {
			t.Fatal("a capability declaring one provider reported a DIFFERENT" +
				" one and was not warned. The exemption must cover exactly the" +
				" varies token, or any vague string silences the check")
		}
	})

	t.Run("varies is exempt only on the DECLARED side", func(t *testing.T) {
		// A module REPORTING "varies" is making a nonsense claim about a call
		// that already happened -- by then the provider is known. It must not
		// inherit the declaration-side exemption.
		if !providerWarning(descriptorWithProvider("elevenlabs"), exec(ProviderVaries)) {
			t.Fatal("a completed invocation REPORTING \"varies\" was accepted." +
				" The provider is known once the call has run, so this is not" +
				" the same as declining to claim one in advance")
		}
	})
}
