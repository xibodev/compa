package main

import (
	"os/exec"
	"strings"
	"testing"
)

// The shell must not link the agent kernel.
//
// WHY THIS IS A TEST AND NOT A NOTE. The edge this guards against already
// existed once, and nothing failed when it did: adding one import to
// internal/moduletools -- a package the shell legitimately uses to LIST and
// INSTALL modules -- pulled pkg/agent and six other packages into a binary that
// never runs an agent. Every test stayed green, both binaries built, and the
// product behaved identically. The only observable was the dependency graph.
//
// That is the shape this codebase keeps finding: two states, one observable. A
// shell that links the kernel and a shell that does not are indistinguishable
// from the outside, so the check has to look at the thing that actually differs.
//
// The split is what makes the kernel shippable on its own. If the shell links
// the kernel, "distribute them separately" is a packaging claim with nothing
// underneath it.
func TestTheShellDoesNotLinkTheAgentKernel(t *testing.T) {
	const kernel = "github.com/xibodev/compa/pkg/agent"

	out, err := exec.Command("go", "list", "-deps",
		"-tags", "goolm,stdjson", ".").Output()
	if err != nil {
		// A failure to MEASURE is not a passing architecture. Skipping here
		// would report "no edge found" for a command that never ran -- the
		// zero-results-from-zero-work defect, in the test built to prevent it.
		t.Fatalf("could not compute the dependency graph, so this test proves"+
			" nothing either way: %v", err)
	}

	for _, dep := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(dep) == kernel {
			t.Fatalf("the web shell links %s.\n\n"+
				"The shell supervises the harness as a separate process and"+
				" talks to it over a socket; it must not also contain a copy of"+
				" the runtime. This usually arrives TRANSITIVELY -- an import"+
				" added to a package the shell already uses (internal/moduletools"+
				" is the one that did it before) -- so check what you imported"+
				" rather than looking for pkg/agent in this directory.\n\n"+
				"Wiring that binds modules to the kernel belongs in"+
				" internal/moduleagent, which only composition roots import.", kernel)
		}
	}
}
