package api

import (
	"testing"

	"github.com/xibodev/compa/pkg/modproto"
)

// The bug this exists for: the README promises "anything that may bill needs
// your approval before it runs", and nothing enforced it.
//
// The Approved flag chose whether a consent claim was STRIPPED or RECORDED --
// which is a different question. An unapproved invocation of a cost_known:false
// capability ran and produced an artefact. Reproduced against the running host:
// POST /api/modules/archive/invoke with content.produce and no approval
// returned ok:true with one artifact.
//
// The Modules page already computed needs_approval and showed it to the
// operator. It was advice, not a gate.
func TestAnUnapprovedUnpricedCapabilityIsRefused(t *testing.T) {
	// Network:true so it can genuinely reach out; the gate is about spending,
	// not about the flag alone.
	c := modproto.Capability{
		ID:      "archive.content.produce",
		Effects: modproto.Effects{CostKnown: false, Network: true},
	}

	if err := approvalGate(nil, c, false); err == nil {
		t.Fatal("an unpriced capability ran without approval, so the host's" +
			" own guarantee that anything which may bill needs approval is" +
			" documentation rather than behaviour")
	}
}

// Approved, the same capability runs. A gate that refuses everything is not a
// gate, and this is the assertion that catches an over-broad fix.
func TestAnApprovedUnpricedCapabilityRuns(t *testing.T) {
	// Network:true so it can genuinely reach out; the gate is about spending,
	// not about the flag alone.
	c := modproto.Capability{
		ID:      "archive.content.produce",
		Effects: modproto.Effects{CostKnown: false, Network: true},
	}

	if err := approvalGate(nil, c, true); err != nil {
		t.Fatalf("an approved capability was refused: %v", err)
	}
}

// A capability that declares a KNOWN cost needs no approval, whatever else it
// does. Free is free, and asking for approval on every call would train the
// operator to click through the ones that matter.
func TestAPricedCapabilityNeedsNoApproval(t *testing.T) {
	for _, c := range []modproto.Capability{
		{ID: "archive.sessions.list", Effects: modproto.Effects{CostKnown: true}},
		{ID: "render.output.review", Effects: modproto.Effects{CostKnown: true, ExternalWrites: true}},
		{ID: "render.tools.list", Effects: modproto.Effects{CostKnown: true, Network: true}},
	} {
		if err := approvalGate(nil, c, false); err != nil {
			t.Errorf("%s was refused despite declaring a known cost: %v", c.ID, err)
		}
	}
}

// The refusal must name what to do. "Denied" with no remedy is how a user ends
// up asking the agent to approve it in chat, which the host strips by design.
func TestTheRefusalNamesTheRemedy(t *testing.T) {
	c := modproto.Capability{ID: "x.y", Effects: modproto.Effects{CostKnown: false, Network: true}}

	err := approvalGate(nil, c, false)
	if err == nil {
		t.Fatal("no refusal")
	}
	for _, want := range []string{"Approve and run", "x.y"} {
		if !contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q:\n%v", want, err)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle ||
		len(needle) == 0 || indexOf(haystack, needle) >= 0)
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}
