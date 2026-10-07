package moduletools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xibodev/compa/v3/pkg/modproto"
)

func writeContent(t *testing.T, dir, rel, body string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

// The bug this exists for: a module's declared digest described content it no
// longer shipped. The host refused that content when loading it into agent
// context -- correctly -- and the Modules page showed zero warnings. The module
// installed cleanly, its capabilities worked, and only its guidance quietly
// never appeared.
//
// Install checks digests too, but only at install time. Content that drifts
// afterwards is caught nowhere a person looks.
func TestDiscoveryReportsStaleContent(t *testing.T) {
	dir := t.TempDir()
	writeContent(t, dir, "agents/o.md", "the content actually shipped")

	d := &modproto.Descriptor{Module: "test.module"}
	d.AgentOverlays = []modproto.Overlay{{
		ID: "test.overlay", Path: "agents/o.md",
		Digest: "sha256:" + strings.Repeat("0", 64),
	}}

	warnings := staleContentWarnings(dir, d)

	if len(warnings) == 0 {
		t.Fatal("stale content produced no warning: it is refused at load time" +
			" and nothing anywhere says why")
	}
	joined := strings.Join(warnings, " ")
	for _, want := range []string{"does not match its declared digest", "agents/o.md", "REFUSED"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warning does not contain %q:\n%s", want, joined)
		}
	}
}

// Content that matches produces nothing. A warning on every open trains people
// to ignore warnings, which is how a real one gets missed.
func TestDiscoverySilentWhenContentMatches(t *testing.T) {
	dir := t.TempDir()
	body := "the content actually shipped"
	writeContent(t, dir, "agents/o.md", body)
	writeContent(t, dir, "skills/s/SKILL.md", body)

	d := &modproto.Descriptor{Module: "test.module"}
	d.AgentOverlays = []modproto.Overlay{{
		ID: "test.overlay", Path: "agents/o.md", Digest: modproto.DigestSHA256([]byte(body)),
	}}
	d.Skills = []modproto.Skill{{
		ID: "test.skill", Path: "skills/s/SKILL.md", Digest: modproto.DigestSHA256([]byte(body)),
	}}

	if w := staleContentWarnings(dir, d); len(w) != 0 {
		t.Fatalf("matching content warned: %v", w)
	}
}

// Declared content that is missing entirely is reported too: "declared but
// absent" and "declared and wrong" both end with the agent not receiving it.
func TestDiscoveryReportsMissingContent(t *testing.T) {
	dir := t.TempDir()

	d := &modproto.Descriptor{Module: "test.module"}
	d.Skills = []modproto.Skill{{
		ID: "test.skill", Path: "skills/gone/SKILL.md",
		Digest: "sha256:" + strings.Repeat("a", 64),
	}}

	warnings := staleContentWarnings(dir, d)
	if len(warnings) != 1 {
		t.Fatalf("got %d warnings, want one for the missing file: %v", len(warnings), warnings)
	}
	if !strings.Contains(warnings[0], "not readable") {
		t.Fatalf("warning does not say the file is unreadable:\n%s", warnings[0])
	}
}

// Content with no declared digest is not checked: a module that declares no
// digest has made no claim to contradict.
func TestDiscoveryIgnoresUndeclaredDigest(t *testing.T) {
	dir := t.TempDir()
	writeContent(t, dir, "agents/o.md", "anything")

	d := &modproto.Descriptor{Module: "test.module"}
	d.AgentOverlays = []modproto.Overlay{{ID: "test.overlay", Path: "agents/o.md"}}

	if w := staleContentWarnings(dir, d); len(w) != 0 {
		t.Fatalf("content with no declared digest warned: %v", w)
	}
}

// The bug this exists for: a capability declared cost_known:false -- so the
// agent was told it "may bill real money" -- while its
// own summary said it never bills. It was the ESTIMATE capability, whose entire
// purpose is checking cost before committing to a paid run.
//
// Asked directly, the agent answered that the tool may bill real money. Marking
// the cost-checking tool as possibly-billing discourages the one behaviour that
// makes a cost gate work.
func TestCostClaimContradictionIsReported(t *testing.T) {
	d := &modproto.Descriptor{Module: "test.module"}
	d.Capabilities = []modproto.Capability{{
		ID:      "test.estimate",
		Summary: "Validate a request and report cost. Never generates media, never bills.",
	}} // CostKnown defaults to false

	warnings := costClaimWarnings(d)

	if len(warnings) != 1 {
		t.Fatalf("got %d warnings, want one for the contradiction: %v", len(warnings), warnings)
	}
	for _, want := range []string{"test.estimate", "cost_known:false", "never bills"} {
		if !strings.Contains(warnings[0], want) {
			t.Errorf("warning does not contain %q:\n%s", want, warnings[0])
		}
	}
}

// A capability that declares unknown cost and does not claim to be free is
// simply honest about not knowing. That must not warn.
func TestUnknownCostWithoutAFreeClaimIsSilent(t *testing.T) {
	d := &modproto.Descriptor{Module: "test.module"}
	d.Capabilities = []modproto.Capability{{
		ID:      "test.run",
		Summary: "Run a creative tool. May contact a paid provider.",
	}}

	if w := costClaimWarnings(d); len(w) != 0 {
		t.Fatalf("honest unknown-cost capability warned: %v", w)
	}
}

// A capability that declares a KNOWN cost and says it is free is consistent,
// whatever that cost is. No warning.
func TestKnownCostWithFreeClaimIsSilent(t *testing.T) {
	d := &modproto.Descriptor{Module: "test.module"}
	c := modproto.Capability{
		ID:      "test.list",
		Summary: "Inventory sessions. Deterministic; costs nothing.",
	}
	c.Effects.CostKnown = true
	d.Capabilities = []modproto.Capability{c}

	if w := costClaimWarnings(d); len(w) != 0 {
		t.Fatalf("consistent capability warned: %v", w)
	}
}

// The disagreement is reported as a HOST limitation rather than a module
// defect: cost_known reports whether an AMOUNT is known, so a capability whose
// amount is unknowable and which never bills declares both facts correctly. The
// warning must not send an author to correct something correct.
func TestTheDisagreementIsReportedAsAHostLimitation(t *testing.T) {
	d := &modproto.Descriptor{Capabilities: []modproto.Capability{{
		ID:      "creative.tools.estimate",
		Summary: "Estimate cost. Never bills.",
		Effects: modproto.Effects{CostKnown: false, Local: true, Provider: "local"},
	}}}

	joined := strings.Join(costClaimWarnings(d), " ")
	if !strings.Contains(joined, "creative.tools.estimate") {
		t.Errorf("the warning does not name the capability: %s", joined)
	}
	if strings.Contains(joined, "one of the two is wrong") {
		t.Errorf("the warning claims the module contradicts itself: %s", joined)
	}
	if !strings.Contains(joined, "Both can be true") {
		t.Errorf("the warning does not say both declarations can be true: %s", joined)
	}
}

// Merely mentioning money is not a claim of being free: the match is
// deliberately narrow so ordinary prose does not trip it.
func TestMentioningCostIsNotAFreeClaim(t *testing.T) {
	d := &modproto.Descriptor{Module: "test.module"}
	d.Capabilities = []modproto.Capability{{
		ID:      "test.gen",
		Summary: "Generate an image. Cost depends on the provider and may be significant.",
	}}

	if w := costClaimWarnings(d); len(w) != 0 {
		t.Fatalf("ordinary prose about cost warned: %v", w)
	}
}

// The bug this exists for: containment was checked with strings.HasPrefix, so a
// module declaring "../<root>-evil/overlay.md" escaped its own directory and the
// check accepted it -- "/srv/modules/acme-evil" carries the prefix
// "/srv/modules/acme" and is a different module's tree.
//
// The digest check limited the damage, but the containment was genuinely broken:
// a module could name a sibling's file and, if the digests happened to match,
// load another module's content into agent context.
func TestOverlayCannotEscapeIntoAPrefixSibling(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "acme")
	sibling := filepath.Join(base, "acme-evil")

	body := []byte("content from another module's tree")
	for _, d := range []string{root, sibling} {
		if err := os.MkdirAll(filepath.Join(d, "agents"), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(sibling, "agents", "o.md"), body, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Digest deliberately CORRECT, so only containment can refuse this.
	_, err := loadVerified(root, "acme", "1.0.0", "acme.overlay", "Overlay",
		"../acme-evil/agents/o.md", modproto.DigestSHA256(body), 10)

	if err == nil {
		t.Fatal("a module read a sibling's file by prefix-escaping its own root")
	}
	if !strings.Contains(err.Error(), "outside its own directory") {
		t.Fatalf("refused for the wrong reason:\n%v", err)
	}
}

// A legitimate module-relative path still loads.
func TestOverlayInsideItsOwnRootStillLoads(t *testing.T) {
	root := t.TempDir()
	body := []byte("the module's own overlay")
	if err := os.MkdirAll(filepath.Join(root, "agents"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "agents", "o.md"), body, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	doc, err := loadVerified(root, "acme", "1.0.0", "acme.overlay", "Overlay",
		"agents/o.md", modproto.DigestSHA256(body), 10)
	if err != nil {
		t.Fatalf("a legitimate overlay was refused: %v", err)
	}
	if doc.Content != string(body) {
		t.Fatalf("content mismatch: %q", doc.Content)
	}
}

// LoadKnowledge takes a caller-supplied slice, so a half-populated Installed
// must contribute nothing rather than panic inside the host.
//
// Discovery always sets Runner today, so this cannot happen through the normal
// path -- which is exactly why it went unnoticed: the guard above it checked
// Descriptor and not Runner, and a nil dereference in host code is a crash
// rather than a refusal.
func TestLoadKnowledgeSurvivesAHalfPopulatedModule(t *testing.T) {
	installed := []Installed{
		{Descriptor: &modproto.Descriptor{Module: "test.module"}}, // no Runner
		{Runner: nil, Descriptor: nil},                            // nothing at all
	}

	k := LoadKnowledge(installed, []string{"test.module"}, nil)

	if len(k.Overlays) != 0 || len(k.Skills) != 0 {
		t.Fatalf("a module with no runner contributed content: %+v", k)
	}
}

// The descriptor validator refuses an overlay or skill declared without a
// well-formed digest, which is what makes the load-time digest check
// unbypassable: content with no digest never reaches an install.
func TestDescriptorRefusesContentWithoutADigest(t *testing.T) {
	d := &modproto.Descriptor{Module: "test.module"}
	d.AgentOverlays = []modproto.Overlay{{ID: "t.overlay", Path: "agents/o.md"}}

	err := modproto.ValidateDescriptor(d)
	if err == nil {
		t.Fatal("a descriptor declaring an overlay with no digest was accepted")
	}
	if !strings.Contains(err.Error(), "digest") {
		t.Fatalf("refused for the wrong reason:\n%v", err)
	}
}
