package agent_test

import (
	"os/exec"
	"strings"
	"testing"
)

// THE KERNEL MUST NOT DEPEND ON THE MODULE HOST, THE WEB SHELL, OR ANY COMMAND.
//
// This is the property that makes pkg/agent independently consumable, and it is
// checked mechanically because it is invisible by reading: the offending import
// was ONE LINE in one file, and it dragged in six Layer-2/3 packages
// transitively -- the module host, subprocess execution, both wire protocols,
// the contract gate, and the cockpit's artefact view vocabulary.
//
// It is also disqualifying in a way that no amount of care prevents: internal/
// packages CANNOT be imported by an external module, so a sibling embedding the
// kernel would fail to compile with an error pointing at THIS repo. The failure
// would surface in their build, not ours.
//
// A comment saying "keep this clean" would not have caught it, because the
// import was added by someone wiring a feature, not by someone weakening a
// boundary.
func TestTheKernelDependsOnNoHostOrShellPackage(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "github.com/xibodev/compa/v2/pkg/agent").Output()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}

	// Each forbidden prefix, with WHY it is forbidden -- so a future failure
	// explains the architecture rather than just naming a string.
	forbidden := []struct{ prefix, why string }{
		{"github.com/xibodev/compa/v2/internal/",
			"internal/ cannot be imported by an external module at all, so any" +
				" such dependency makes the kernel unembeddable"},
		{"github.com/xibodev/compa/v2/web/",
			"the browser shell is Layer 3; a kernel that needs it cannot be" +
				" embedded in a product with its own UI"},
		{"github.com/xibodev/compa/v2/cmd/",
			"a command is a composition root; depending on one inverts the" +
				" direction the whole architecture rests on"},
		{"github.com/xibodev/compa/v2/pkg/modproto",
			"the module wire protocol is Layer 2; the kernel must not know" +
				" that detached modules exist"},
		{"github.com/xibodev/compa/v2/pkg/contractv2",
			"the contract gate is Layer 2 and only means something to a host" +
				" that invokes external modules"},
	}

	var violations []string
	for _, dep := range strings.Fields(string(out)) {
		for _, f := range forbidden {
			if strings.HasPrefix(dep, f.prefix) {
				violations = append(violations, "  "+dep+"\n      "+f.why)
			}
		}
	}

	if len(violations) > 0 {
		t.Fatalf("the kernel imports %d package(s) it must not:\n%s\n\n"+
			"Capabilities reach the kernel through agent.ToolProvider, which the"+
			" COMPOSITION ROOT supplies. If you are adding a feature that needs"+
			" one of these, inject it as a provider rather than importing it"+
			" here.", len(violations), strings.Join(violations, "\n"))
	}
}

// The scan must be able to FAIL, or a clean result proves nothing.
//
// A `go list` that returns nothing -- wrong package name, tooling missing,
// output format changed -- produces zero violations and reads exactly like a
// clean kernel. This asserts the scan actually saw a realistic dependency
// graph, so "no violations" means "checked and clean" rather than "checked
// nothing".
func TestTheBoundaryScanActuallyInspectsDependencies(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "github.com/xibodev/compa/v2/pkg/agent").Output()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}

	deps := strings.Fields(string(out))
	if len(deps) < 20 {
		t.Fatalf("the dependency scan returned only %d packages, which is far"+
			" fewer than the kernel really has. A near-empty result yields zero"+
			" violations and is indistinguishable from a clean boundary", len(deps))
	}

	// And it must contain packages the kernel genuinely needs, or it is
	// scanning something other than the kernel.
	var sawProviders bool
	for _, d := range deps {
		if strings.HasPrefix(d, "github.com/xibodev/compa/v2/pkg/providers") {
			sawProviders = true
		}
	}
	if !sawProviders {
		t.Fatal("the scan did not see pkg/providers, which the kernel certainly" +
			" depends on -- so it is not inspecting the kernel's real graph")
	}
}
