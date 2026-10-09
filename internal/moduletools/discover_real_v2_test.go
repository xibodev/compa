package moduletools_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xibodev/compa/v4/internal/moduletools"
)

// Discovery against a REAL v2-declaring module binary, named by
// COMPA_TEST_V2_MODULE.
//
// WHY A REAL BINARY AND NOT A FIXTURE. Every other test in this package builds
// its descriptor in Go or as a string literal -- which means they all test an
// understanding of the shape, not the shape a shipped module actually emits.
// A module author once probed this host's gate with a hand-built input, got
// the wrong answer, and nearly reported interoperation that was an artifact
// of the fixture. The only defence is running the real thing.
//
// SKIPS when no binary is named rather than failing: a real module is not a
// dependency of this repo. A skip is honest about not having run; a pass
// would not be.
func TestDiscoveryAgainstARealV2Module(t *testing.T) {
	src := os.Getenv("COMPA_TEST_V2_MODULE")
	if src == "" {
		t.Skip("COMPA_TEST_V2_MODULE names no v2-declaring module binary")
	}
	if _, err := os.Stat(src); err != nil {
		t.Skipf("no v2 module binary at %s", src)
	}

	// Install it the way the host does: <home>/modules/<id>/<id>[.exe], with
	// the ID taken from the binary's name.
	name := filepath.Base(src)
	id := strings.TrimSuffix(name, filepath.Ext(name))
	home := t.TempDir()
	dir := filepath.Join(moduletools.ModulesDir(home), id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	blob, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), blob, 0o755); err != nil {
		t.Fatal(err)
	}

	found := moduletools.Discover(context.Background(), home)
	if len(found) != 1 {
		t.Fatalf("discovered %d modules, want 1", len(found))
	}
	in := found[0]
	if in.Err != nil {
		t.Fatalf("a real v2-declaring module failed discovery: %v.\n"+
			"That matters beyond this test: it would mean declaring"+
			" contract_version breaks the v1 read path, and every module"+
			" adopting v2 would vanish from the Modules page", in.Err)
	}

	// THE WIRE FORMAT IS STILL v1 AND MUST STAY SO. A v2 module declares
	// protocol_versions ["xibodev.module/v1"] and passes v1 descriptor
	// validation; contract_version is a different axis. If this ever fails,
	// the two axes have been conflated somewhere.
	if in.Descriptor == nil || len(in.Descriptor.Capabilities) == 0 {
		t.Fatal("the v1 descriptor did not decode, so the wire path broke while" +
			" adding the behavioural one")
	}

	// AND THE BEHAVIOURAL CONTRACT IS READ.
	if !in.V2.Decision.MayRelyOnV2() {
		t.Fatalf("a module publishing contract_version xibodev.module/v2 did not"+
			" pass the pin during discovery: outcome=%v reason=%q",
			in.V2.Decision.Pin.Outcome, in.V2.Decision.Pin.Reason)
	}

	// Report conformance rather than asserting it passes: a module may
	// publish its v2 declaration before its v2 payload, and that is the
	// module's work in progress, not a defect in this host.
	//
	// ASSERT WHICH FINDING, do not merely log that there was one. This test
	// once logged Refusal() and passed, and the summary line was read instead
	// of the finding: a module that published v1 artifact_schemas and no v2
	// artifact_kinds was told it reported v2_payload_half_published, when it
	// reported no_operations_declared. A LOG LINE IS NOT AN ASSERTION.
	if in.V2.Conformance == nil {
		t.Fatal("conformance did not run against a v2 module")
	}
	var got []string
	for _, f := range in.V2.Conformance.Findings {
		got = append(got, f.Code)
	}
	t.Logf("real module findings: %v", got)

	hasKinds := len(in.V2.Decision.V2.Descriptor().ArtifactKinds) > 0
	for _, f := range in.V2.Conformance.Findings {
		if f.Code == "v2_payload_half_published" && !hasKinds {
			t.Error("classified as half-published with NO artifact kinds. That" +
				" finding is only decidable when kinds are present; without them" +
				" the two readings are genuinely indistinguishable")
		}
		if f.Code == "no_operations_declared" && hasKinds {
			t.Error("offered the 'genuinely has no Operations' reading while" +
				" declaring artifact kinds, which cannot be true")
		}
	}
	t.Logf("real v2 module: pin=%v capabilities=%d operations=%d",
		in.V2.Decision.Pin.Outcome, len(in.Descriptor.Capabilities),
		len(in.V2.Decision.V2.Descriptor().Operations))
}
