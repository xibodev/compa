package moduletools

import (
	"github.com/xibodev/compa/pkg/modproto"
)

// MayBill reports whether a capability must be treated as possibly-billing.
//
// It reads CostKnown, and that is a HOST POLICY rather than a reading of what
// the field means. cost_known declares whether a NUMERIC AMOUNT IS KNOWN --
// nothing more. It does not mean "may spend money", and the owning lane proved
// it by running: a tool declaring cost_known TRUE and network TRUE requires no
// consent at all.
//
// So this function is the host over-gating on the only signal v1 carries. The
// fact it actually wants -- "may incur a monetary charge", independent of
// whether the amount is known -- is not expressible in v1 and is a successor
// contract concern (may_charge). Until then, unknown amount is the closest
// available proxy, and it is deliberately the pessimistic one.
//
// This comment previously said MayBill "reports whether a capability could
// actually spend money. It is CostKnown" -- stating the superseded equivalence
// as the definition. The BEHAVIOUR was and is correct; the justification taught
// the wrong rule, in the file someone copies the pattern from.
//
// Two attempts at a structural rule are recorded below because both were wrong
// in ways that mattered, and the next person will be tempted by the same idea.
//
// Attempt one: treat local && !network && a local provider as unable to bill,
// so a rendering module's creative.tools.estimate -- which declares
// cost_known:false while also declaring local, no network, provider "local",
// and whose own summary says "never bills" -- would stop being gated. It
// worked for that case and let a notes module's content.produce through
// UNGATED, which genuinely spends by shelling out to an AI CLI. The module
// never touches the network; the subprocess does.
//
// Attempt two: add "the module declared subprocess binaries" to the test. That
// re-gated content.produce and ALSO gated creative.tools.estimate, because the
// rendering module declares ffmpeg, ffprobe, node and npx -- none of which can
// charge anything. Module-level permissions are too coarse to answer a
// per-capability question.
//
// There is no honest structural rule available. paid_providers does not help
// either: the notes module declares none and spends; the rendering module
// declares five but not on estimate. So the host takes the module at its word.
//
// Note what "at its word" now means, because it changed. Under the settled
// meaning of cost_known, the rendering module's creative.tools.estimate
// declaring cost_known:false while its summary says it never bills is NOT a
// contradiction: the amount really is unknown (their estimate returns a null
// cost), and never billing is a separate fact the field cannot carry. The host
// still gates it, and that is the host being pessimistic on a proxy signal --
// not the module contradicting itself. Any warning phrased as "one of these is
// wrong" is describing a v1 expressiveness gap, not an author error.
//
// The cost of guessing wrong is asymmetric and that decides it: a wrongly
// gated free capability is an annoying extra click, and a wrongly ungated paid
// one spends the user's money without asking.
func MayBill(c modproto.Capability) bool {
	return !c.Effects.CostKnown
}

// MayBillUnder exists so callers can pass the descriptor without caring
// whether the decision uses it. It does not, today, for the reasons above --
// but the two attempts that failed both needed it, and a future signal that
// genuinely answers the question would be per-capability and live here.
func MayBillUnder(_ *modproto.Descriptor, c modproto.Capability) bool {
	return MayBill(c)
}

// NeedsApproval reports whether this capability must be refused before it runs.
//
// It is the ONE place the approval policy is composed. Both callers -- the
// agent path (adapter.go) and the cockpit path (api/modules.go) -- ask this
// rather than each rebuilding "may bill AND nobody approved".
//
// They were not sharing it, and the shape of that divergence is the one this
// codebase keeps finding: five decisions duplicated across the CLI, the agent
// and the cockpit, and every place they diverged had produced a bug (grantRoots,
// ApplyGrants, the seed request shape, DeadlineMS, PartialInstall). The two
// approval gates had not diverged yet. Sharing the composition is what keeps a
// later fix from landing on one caller and not the other.
//
// The approval SOURCE stays with the caller, deliberately, because the two are
// genuinely different questions and collapsing them would be wrong:
//
//   - the agent path has no per-call approval to consult. An approval arriving
//     through the model is stripped by design, so its only source is the
//     operator's standing decision in the host environment.
//   - the cockpit path has a real person pressing a button against the effects
//     the capability declares, which is a per-request fact.
//
// So the caller answers "did someone approve THIS", and this function answers
// "does it need approval at all".
func NeedsApproval(d *modproto.Descriptor, c modproto.Capability, approved bool) bool {
	if approved {
		return false
	}
	return MayBillUnder(d, c)
}
