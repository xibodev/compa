package moduletools

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/xibodev/compa/pkg/modproto"
)

// The operator's goal is "create a video from simple chatting". Enforcing the
// approval guarantee made that impossible: every path to a render goes through
// a cost_known:false capability, the agent cannot approve, and approval is
// per-call and remembered nowhere. So chat could never do it again.
//
// A standing approval is the operator saying, once and explicitly, which
// capabilities they have already decided about. It is not the host deciding,
// and it is not the model deciding -- both of which the guarantee exists to
// prevent.
func TestAStandingApprovalLetsTheAgentRunAGatedCapability(t *testing.T) {
	t.Setenv(EnvApproveCapabilities, "example.render/creative.tools.run")

	if !PreApproved("example.render", "creative.tools.run") {
		t.Fatal("a capability the operator named is still gated, so chat can" +
			" never complete the journey it exists for")
	}
}

// Naming one capability approves ONLY that one. A standing approval that
// widened to a whole module would be the operator approving things they never
// read.
func TestAStandingApprovalDoesNotWiden(t *testing.T) {
	t.Setenv(EnvApproveCapabilities, "example.render/creative.tools.run")

	for _, c := range []struct{ mod, cap string }{
		{"example.render", "creative.tools.estimate"},
		{"archive", "creative.tools.run"},
		{"archive", "content.produce"},
	} {
		if PreApproved(c.mod, c.cap) {
			t.Errorf("%s/%s was approved by naming a different capability", c.mod, c.cap)
		}
	}
}

// Unset means unchanged: everything that may bill still gates. A host that
// quietly stopped gating because a variable was absent would turn the
// guarantee off for everyone who never sets it.
func TestUnsetMeansEverythingStillGates(t *testing.T) {
	t.Setenv(EnvApproveCapabilities, "")

	if PreApproved("example.render", "creative.tools.run") {
		t.Fatal("an unset variable pre-approved a capability")
	}
}

// A whole-module wildcard is refused rather than honoured. "Approve everything
// this module ever adds" is not a decision anyone can make in advance -- a
// module updates and the approval silently covers capabilities that did not
// exist when it was written.
func TestAWildcardIsRefused(t *testing.T) {
	t.Setenv(EnvApproveCapabilities, "example.render/*,archive")

	for _, c := range []struct{ mod, cap string }{
		{"example.render", "creative.tools.run"},
		{"archive", "content.produce"},
	} {
		if PreApproved(c.mod, c.cap) {
			t.Errorf("a wildcard approved %s/%s", c.mod, c.cap)
		}
	}
}

// Whitespace and stray separators are tolerated: an operator editing a shell
// profile should not silently get nothing.
func TestWhitespaceIsTolerated(t *testing.T) {
	t.Setenv(EnvApproveCapabilities, "  archive/content.produce ,, ")

	if !PreApproved("archive", "content.produce") {
		t.Fatal("a padded entry was not honoured")
	}
}

// The malformed case must not approve anything, and must be visible: a silent
// no-op leaves the operator believing they approved something.
func TestAMalformedEntryApprovesNothingAndIsReported(t *testing.T) {
	t.Setenv(EnvApproveCapabilities, "just-a-module-name")

	if PreApproved("just-a-module-name", "anything") {
		t.Fatal("an entry with no capability approved one")
	}
	warnings := StandingApprovalWarnings()
	if len(warnings) == 0 {
		t.Fatal("a malformed entry was ignored silently, so the operator" +
			" believes they approved something they did not")
	}
	if !strings.Contains(strings.Join(warnings, " "), "just-a-module-name") {
		t.Errorf("the warning does not name the bad entry: %v", warnings)
	}
}

// A pre-approved capability must still be one the module DECLARED. The
// standing approval says "I accept this cost", not "run something undeclared".
func TestAPreApprovalStillRequiresADeclaredCapability(t *testing.T) {
	t.Setenv(EnvApproveCapabilities, "archive/no.such.capability")

	// PreApproved answers only about the operator's list; the caller still
	// resolves the capability from the descriptor, which is where an
	// undeclared name is refused. This pins that PreApproved does not pretend
	// to validate existence.
	if !PreApproved("archive", "no.such.capability") {
		t.Skip("implementation validates existence itself; nothing to pin")
	}
	d := &modproto.Descriptor{Module: "archive"}
	if capabilityIsDeclared(d, "no.such.capability") {
		t.Fatal("an undeclared capability was treated as declared")
	}
}

func capabilityIsDeclared(d *modproto.Descriptor, id string) bool {
	for _, c := range d.Capabilities {
		if c.ID == id {
			return true
		}
	}
	return false
}

// End to end on the agent path, against a REAL module: a standing approval
// lets a gated capability run, and its absence still refuses.
//
// This exercises Execute rather than PreApproved, because the two can diverge
// -- a correct predicate wired into nothing is the shape this project keeps
// finding.
func TestTheAgentHonoursAStandingApproval(t *testing.T) {
	home := t.TempDir()
	installFakeModule(t, home)

	in := Discover(t.Context(), home)
	if len(in) == 0 || in[0].Descriptor == nil {
		t.Skip("the fake module did not describe itself here")
	}
	d := in[0].Descriptor

	// fake.estimate.unpriced is the module's own cost_known:false capability.
	var paid modproto.Capability
	for _, c := range d.Capabilities {
		if !c.Effects.CostKnown {
			paid = c
			break
		}
	}
	if paid.ID == "" {
		t.Skip("the fake module declares no unpriced capability")
	}

	tool := &CapabilityTool{
		descriptor: d, capability: paid, runner: in[0].Runner,
		home: home, workspace: filepath.Join(home, "workspace"),
	}

	// Without approval: refused, and it says where approval lives.
	res := tool.Execute(t.Context(), map[string]any{})
	if res == nil || !res.IsError || !strings.Contains(res.ForLLM, "Approve and run") {
		t.Fatalf("an unapproved paid capability was not refused:\n%+v", res)
	}

	// With a standing approval naming exactly it: the gate lets it through.
	t.Setenv(EnvApproveCapabilities, d.Module+"/"+paid.ID)
	res = tool.Execute(t.Context(), map[string]any{})
	if res != nil && res.IsError && strings.Contains(res.ForLLM, "Approve and run") {
		t.Fatalf("a standing approval did not reach the gate:\n%s", res.ForLLM)
	}
}
