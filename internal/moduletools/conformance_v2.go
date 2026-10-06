package moduletools

import (
	"fmt"

	"github.com/xibodev/compa/v2/pkg/modprotov2"
)

// HostConformance is the host's verdict on ONE described module, and it is the
// thing a caller must consult before acting on any v2 declaration.
//
// WHY A TYPE RATHER THAN AN ERROR. A conformance run produces per-target
// findings, and the operator ruling is that conformance is evaluated PER TARGET
// independently: a projection may not claim conformance while silently
// weakening a mandatory requirement, and one module's failure says nothing
// about another's. An error return would collapse that into a single boolean
// at the call site.
type HostConformance struct {
	Module   string
	Findings []modprotov2.Finding
}

// Conforms reports whether the host may act on this module's v2 declarations.
func (h HostConformance) Conforms() bool { return len(h.Findings) == 0 }

// Refusal renders the verdict for a person, with reason AND remedy.
//
// Every finding is listed rather than only the first. A validator that reports
// one problem at a time turns a single bad descriptor into as many rebuild
// cycles as it has defects, and the author cannot see whether they are
// unrelated or one cause.
func (h HostConformance) Refusal() string {
	if h.Conforms() {
		return ""
	}
	out := fmt.Sprintf("module %q does not conform to %s and is refused:",
		h.Module, "xibodev.module/v2")
	for _, f := range h.Findings {
		out += "\n  - " + f.String()
	}
	return out
}

// CheckHostConformance closes the triangle at the host:
//
//	product semantic declaration -> v2 projection -> host interpretation
//
// THE HOST MUST FAIL MECHANICALLY IF THE PROJECTION WEAKENS AN ACCEPTED
// SEMANTIC. This is the one rule whose violation is invisible at the layer
// being reviewed: the Operation reads correct while the gate, which reads
// capability effects, fires on nothing. So the check runs here, and discovery
// refuses a module that fails it before registering any of its capabilities.
//
// It takes an Accepted rather than a *Descriptor, which is not a stylistic
// choice: an Accepted can only be produced by modprotov2.Evaluate, so the
// contract pin has necessarily already run. The ordering the operator requires
// -- gate before reliance -- is therefore enforced by the type system rather
// than by this comment.
func CheckHostConformance(a modprotov2.Accepted) HostConformance {
	d := a.Descriptor()
	if d == nil {
		// A zero Accepted cannot occur through Evaluate, but if one is ever
		// constructed some other way it must not read as conforming. "No
		// findings" and "nothing was checked" would otherwise be the same
		// observable.
		return HostConformance{Findings: []modprotov2.Finding{{
			Code:   "nothing_checked",
			Where:  "descriptor",
			Reason: "no descriptor was present to check, so conformance was never evaluated",
			Remedy: "obtain the descriptor through modprotov2.Evaluate, which is" +
				" the only path that runs the contract pin",
		}}}
	}
	return HostConformance{Module: d.Module, Findings: modprotov2.Validate(d)}
}
