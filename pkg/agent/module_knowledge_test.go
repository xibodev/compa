package agent

import (
	"strings"
	"testing"

	"github.com/xibodev/compa/v3/pkg/bus"
)

func optsWithModule(moduleID string) processOptions {
	raw := map[string]string{}
	if moduleID != "" {
		raw[bus.MetadataKeySelectedModule] = moduleID
	}
	return processOptions{
		Dispatch: DispatchRequest{
			InboundContext: &bus.InboundContext{Raw: raw},
		},
	}
}

func agentWithKnowledge(overlays, skills string, warnings []string) *AgentInstance {
	return &AgentInstance{
		ModuleKnowledge: func(string) (string, string, []string) {
			return overlays, skills, warnings
		},
	}
}

// The connector gesture's deeper half: selecting a module loads its overlay and
// skills, which is what makes the selection more than a sentence in the prompt.
func TestSelectedModuleLoadsOverlayAndSkills(t *testing.T) {
	a := agentWithKnowledge("OVERLAY BODY", "SKILL BODY", nil)

	parts := moduleKnowledgePromptParts(a, optsWithModule("archive"))

	if len(parts) != 2 {
		t.Fatalf("got %d parts, want overlay + skills", len(parts))
	}
	joined := parts[0].Content + parts[1].Content
	if !strings.Contains(joined, "OVERLAY BODY") || !strings.Contains(joined, "SKILL BODY") {
		t.Fatalf("module content missing from prompt parts: %+v", parts)
	}
	for _, p := range parts {
		if p.Source.ID != PromptSourceModuleKnowledge {
			t.Errorf("part %q is not attributed to the module: %v", p.ID, p.Source.ID)
		}
		if !strings.Contains(p.Source.Name, "archive") {
			t.Errorf("part %q does not name the module it came from: %q", p.ID, p.Source.Name)
		}
	}
}

// The rule that keeps an installed module cheap: with no selection, nothing is
// loaded. A module costs one line of capability summary until someone asks.
func TestNoSelectionLoadsNothing(t *testing.T) {
	a := agentWithKnowledge("OVERLAY BODY", "SKILL BODY", nil)

	if parts := moduleKnowledgePromptParts(a, optsWithModule("")); len(parts) != 0 {
		t.Fatalf("loaded %d parts with no module selected: an unselected module"+
			" must not cost context", len(parts))
	}
}

// A module that contributes nothing produces no parts, rather than empty
// headings the model has to read past.
func TestEmptyKnowledgeProducesNoParts(t *testing.T) {
	a := agentWithKnowledge("", "   ", nil)

	if parts := moduleKnowledgePromptParts(a, optsWithModule("archive")); len(parts) != 0 {
		t.Fatalf("got %d parts from a module with no content", len(parts))
	}
}

// A host with no modules installed has no loader, and must not panic.
func TestNoLoaderIsSafe(t *testing.T) {
	if parts := moduleKnowledgePromptParts(&AgentInstance{}, optsWithModule("archive")); parts != nil {
		t.Fatalf("got parts from an agent with no module loader: %+v", parts)
	}
	if parts := moduleKnowledgePromptParts(nil, optsWithModule("archive")); parts != nil {
		t.Fatalf("got parts from a nil agent: %+v", parts)
	}
}

// A selection with no inbound context resolves to no selection rather than
// panicking: not every turn arrives from a channel.
func TestMissingInboundContextIsNoSelection(t *testing.T) {
	a := agentWithKnowledge("OVERLAY", "SKILLS", nil)

	if parts := moduleKnowledgePromptParts(a, processOptions{}); len(parts) != 0 {
		t.Fatalf("got %d parts with no inbound context", len(parts))
	}
}

// The bug this exists for: the channel states the selection to the model as an
// instruction before anything knows whether the module exists. A stale client,
// a removed module or a typo all produced
// "[Use the ghostmodule module for this request. Prefer its capabilities...]".
//
// Telling a model to prefer the capabilities of something that does not exist
// is an instruction it can only follow by improvising -- exactly the behaviour
// this boundary exists to prevent. A log line does not reach the model.
func TestUnknownModuleIsCorrectedInThePrompt(t *testing.T) {
	a := &AgentInstance{
		ModuleKnowledge: func(string) (string, string, []string) {
			return "", "", []string{`module "ghostmodule" was selected but is not installed`}
		},
	}

	parts := moduleKnowledgePromptParts(a, optsWithModule("ghostmodule"))

	if len(parts) != 1 {
		t.Fatalf("got %d parts, want one correction", len(parts))
	}
	body := parts[0].Content
	for _, want := range []string{"is not installed", "Ignore the instruction", "ghostmodule"} {
		if !strings.Contains(body, want) {
			t.Errorf("correction does not contain %q:\n%s", want, body)
		}
	}
	// It must also tell the agent what to do INSTEAD, or it invites the same
	// improvisation by omission -- including that the shell is not a
	// substitute, which is what an agent actually reached for when a module
	// went away.
	if !strings.Contains(body, "say") {
		t.Errorf("correction does not say to report the gap:\n%s", body)
	}
	if !strings.Contains(body, "shell") {
		t.Errorf("correction does not rule out the shell as a substitute:\n%s", body)
	}
}

// A DISABLED module must be reported as disabled, not as missing.
//
// The two need different actions from the user: one is "install it", the other
// is "turn it back on". Telling someone a module is not installed when they can
// see it on the Modules page sends them to reinstall what they already have.
func TestADisabledModuleIsReportedAsDisabledNotMissing(t *testing.T) {
	a := &AgentInstance{
		ModuleKnowledge: func(string) (string, string, []string) {
			return "", "", []string{`module "archive" is installed but DISABLED,` +
				` so it contributes no capabilities and no guidance; it can be` +
				` re-enabled on the Modules page`}
		},
	}

	parts := moduleKnowledgePromptParts(a, optsWithModule("archive"))

	if len(parts) != 1 {
		t.Fatalf("got %d parts, want one correction", len(parts))
	}
	body := parts[0].Content
	for _, want := range []string{"DISABLED", "Modules page", "archive"} {
		if !strings.Contains(body, want) {
			t.Errorf("the correction does not carry %q, so the user is told the"+
				" wrong remedy:\n%s", want, body)
		}
	}
	if strings.Contains(body, "is not installed") {
		t.Errorf("a disabled module was reported as not installed:\n%s", body)
	}
}

// A module that IS installed but simply has no overlay or skills must not be
// reported as missing: contributing nothing and not existing are different.
func TestInstalledButEmptyModuleIsNotReportedMissing(t *testing.T) {
	a := agentWithKnowledge("", "", nil)

	if parts := moduleKnowledgePromptParts(a, optsWithModule("archive")); len(parts) != 0 {
		t.Fatalf("an installed module with no content was reported as missing: %+v", parts)
	}
}

// A module with content AND a warning (one unreadable skill, say) still loads
// what it has -- a partial failure must not discard the rest.
func TestPartialFailureStillLoadsWhatItHas(t *testing.T) {
	a := agentWithKnowledge("OVERLAY BODY", "", []string{"skill x was unreadable"})

	parts := moduleKnowledgePromptParts(a, optsWithModule("archive"))

	if len(parts) != 1 {
		t.Fatalf("got %d parts, want the overlay that did load", len(parts))
	}
	if !strings.Contains(parts[0].Content, "OVERLAY BODY") {
		t.Fatalf("usable content was discarded because of a warning:\n%s", parts[0].Content)
	}
}
