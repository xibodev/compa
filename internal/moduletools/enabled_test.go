package moduletools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/xibodev/compa/v2/pkg/modproto"
	toolshared "github.com/xibodev/compa/v2/pkg/tools/shared"
)

// A newly installed module is enabled: installing something and finding it
// silently inert would be worse than not installing it.
func TestAFreshModuleIsEnabled(t *testing.T) {
	dir := t.TempDir()

	if Disabled(dir) {
		t.Fatal("a module with no marker was treated as disabled")
	}
}

func TestDisableAndEnableRoundTrip(t *testing.T) {
	dir := t.TempDir()

	if err := SetDisabled(dir, true); err != nil {
		t.Fatalf("SetDisabled(true): %v", err)
	}
	if !Disabled(dir) {
		t.Fatal("the module is still enabled after being disabled")
	}

	if err := SetDisabled(dir, false); err != nil {
		t.Fatalf("SetDisabled(false): %v", err)
	}
	if Disabled(dir) {
		t.Fatal("the module is still disabled after being enabled")
	}
}

// Disabling must not uninstall. Re-enabling has to restore what the user had,
// not send them to re-fetch it.
func TestDisablingKeepsTheModuleOnDisk(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "mod.exe")
	if err := os.WriteFile(binary, []byte("binary"), 0o755); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	state := filepath.Join(dir, "agents", "overlay.md")
	if err := os.MkdirAll(filepath.Dir(state), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(state, []byte("knowledge"), 0o644); err != nil {
		t.Fatalf("write overlay: %v", err)
	}

	if err := SetDisabled(dir, true); err != nil {
		t.Fatalf("SetDisabled: %v", err)
	}

	for _, p := range []string{binary, state} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("disabling removed %s: %v", filepath.Base(p), err)
		}
	}
}

// Enabling something already enabled must not be an error: the Modules page
// sends the desired state, not a delta.
func TestEnablingAnEnabledModuleIsNotAnError(t *testing.T) {
	if err := SetDisabled(t.TempDir(), false); err != nil {
		t.Fatalf("enabling an already-enabled module failed: %v", err)
	}
}

// The marker must explain itself. A bare dotfile in a module directory is a
// mystery to whoever finds it months later.
func TestTheMarkerSaysWhatItIs(t *testing.T) {
	dir := t.TempDir()
	if err := SetDisabled(dir, true); err != nil {
		t.Fatalf("SetDisabled: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(dir, disabledMarker))
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if len(body) == 0 {
		t.Fatal("the marker is empty, so it explains nothing to whoever finds it")
	}
}

// A module installed as a BARE binary directly in <modules>/ has no directory
// of its own. Writing a marker there would disable every module at once, so
// ModuleDir must refuse to name it.
func TestABareBinaryHasNoDirectoryToDisable(t *testing.T) {
	home := t.TempDir()
	root := ModulesDir(home)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if _, ok := ModuleDir(home, filepath.Join(root, "bare.exe")); ok {
		t.Fatal("a bare binary was given the SHARED modules directory to" +
			" disable, which would turn off every installed module")
	}
}

// A module in its own directory resolves to that directory.
func TestAModuleInItsOwnDirectoryResolves(t *testing.T) {
	home := t.TempDir()
	want := filepath.Join(ModulesDir(home), "some.module")
	if err := os.MkdirAll(want, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	got, ok := ModuleDir(home, filepath.Join(want, "some.module.exe"))
	if !ok {
		t.Fatal("a module in its own directory could not be resolved")
	}
	if filepath.Clean(got) != filepath.Clean(want) {
		t.Fatalf("ModuleDir = %q, want %q", got, want)
	}
}

// The point of the whole feature: a disabled module must contribute no tools
// and no summary line.
//
// A summary without tools would be worse than silence -- it would describe
// abilities to the agent that it cannot reach, so it would try and fail.
//
// This installs a REAL module binary. An earlier version used a bare directory,
// registered nothing in either state, and skipped -- proving only that the test
// could not fail.
func TestADisabledModuleContributesNothingToTheAgent(t *testing.T) {
	home := t.TempDir()
	dir := installFakeModule(t, home)

	enabledTools, enabledSummaries := countRegistered(t, home)
	if enabledTools == 0 {
		t.Fatal("the fixture registered no tools while ENABLED, so this test" +
			" cannot detect the difference disabling makes")
	}

	if err := SetDisabled(dir, true); err != nil {
		t.Fatalf("SetDisabled: %v", err)
	}

	disabledTools, disabledSummaries := countRegistered(t, home)
	if disabledTools != 0 {
		t.Errorf("a disabled module still registered %d tools", disabledTools)
	}
	if disabledSummaries != 0 {
		t.Errorf("a disabled module still contributed %d summary lines, which"+
			" describe abilities the agent cannot reach", disabledSummaries)
	}

	// And re-enabling restores exactly what was there.
	if err := SetDisabled(dir, false); err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	againTools, againSummaries := countRegistered(t, home)
	if againTools != enabledTools || againSummaries != enabledSummaries {
		t.Errorf("re-enabling did not restore the module: %d tools/%d summaries,"+
			" want %d/%d", againTools, againSummaries, enabledTools, enabledSummaries)
	}
}

// installFakeModule builds the real fake module into a home and returns its
// install directory.
func installFakeModule(t *testing.T, home string) string {
	t.Helper()
	dir := filepath.Join(ModulesDir(home), "fake")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	name := "fake"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	bin := filepath.Join(dir, name)
	cmd := exec.Command("go", "build", "-tags", "goolm,stdjson", "-o", bin, "../../cmd/fakemodule")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot build the fake module here: %v: %s", err, out)
	}
	return dir
}

func countRegistered(t *testing.T, home string) (tools, summaries int) {
	t.Helper()
	got, _ := RegisterTools(context.Background(), home, filepath.Join(home, "workspace"),
		func(toolshared.Tool) { tools++ })
	return tools, len(got)
}

// The bug this exists for: /api/tools built its OWN list from Discover and
// hardcoded Status "enabled" for every module capability, so a disabled module
// still advertised six callable tools that the agent did not have.
//
// The agent was right and the page was wrong -- verified by asking the running
// agent, which answered "NO ARCHIVE TOOLS" while the page listed six. A second
// implementation of "what is available" disagreeing with the first is the
// failure this codebase keeps finding, and the comment above it said "there is
// no per-tool switch", which had simply stopped being true.
//
// This pins the flag that both readers must consult.
func TestDiscoveryReportsDisabledSoEveryReaderAgrees(t *testing.T) {
	home := t.TempDir()
	dir := installFakeModule(t, home)

	before := findInstalled(t, home)
	if before.Disabled {
		t.Fatal("a freshly installed module reported itself disabled")
	}

	if err := SetDisabled(dir, true); err != nil {
		t.Fatalf("SetDisabled: %v", err)
	}

	after := findInstalled(t, home)
	if !after.Disabled {
		t.Fatal("Discover did not report the module as disabled, so every" +
			" reader downstream will show it as available")
	}
	// It must still be DISCOVERED, with its capabilities, or there is nothing
	// for a page to offer turning back on.
	if after.Descriptor == nil || len(after.Descriptor.Capabilities) == 0 {
		t.Fatal("a disabled module lost its descriptor, so the Modules page" +
			" has nothing to re-enable")
	}
}

func findInstalled(t *testing.T, home string) Installed {
	t.Helper()
	for _, in := range Discover(context.Background(), home) {
		if in.Descriptor != nil {
			return in
		}
	}
	t.Fatal("no module was discovered")
	return Installed{}
}

// A DISABLED module must contribute no knowledge either.
//
// RegisterTools already skips its tools. LoadKnowledge did not check, so
// selecting a disabled module in the chat box loaded its overlay and skills --
// pages of guidance for tools the agent does not have. That is the worst of
// both: the model is told in detail how to do something, finds no tool for it,
// and improvises with the shell. Observed doing exactly that.
func TestADisabledModuleContributesNoKnowledge(t *testing.T) {
	home := t.TempDir()
	dir := installFakeModule(t, home)

	// The fake module DECLARES an overlay and a skill with digests computed from
	// known bytes, but ships neither. Write exactly those bytes so the loader
	// verifies them and the knowledge really loads -- otherwise this test skips
	// and proves nothing, which is how the bug survived.
	writeFakeKnowledge(t, dir)

	before := LoadKnowledge(Discover(context.Background(), home), []string{"fake"}, nil)
	if len(before.Overlays) == 0 && len(before.Skills) == 0 {
		t.Fatalf("the fixture contributed no knowledge while ENABLED, so this"+
			" test cannot detect it being withheld (warnings: %v)", before.Warnings)
	}

	if err := SetDisabled(dir, true); err != nil {
		t.Fatalf("SetDisabled: %v", err)
	}

	after := LoadKnowledge(Discover(context.Background(), home), []string{"fake"}, nil)
	if len(after.Overlays) != 0 || len(after.Skills) != 0 {
		t.Fatalf("a disabled module contributed %d overlays and %d skills, so"+
			" the agent is told how to use tools it does not have",
			len(after.Overlays), len(after.Skills))
	}
}

// writeFakeKnowledge materialises the overlay and skill the fake module
// declares, byte for byte, so their declared digests verify.
//
// The module compiles these bodies in and computes its digests from them, but
// ships neither file -- so without this the loader refuses both and the test
// above skips, proving nothing.
func writeFakeKnowledge(t *testing.T, dir string) {
	t.Helper()
	const overlay = "# Fake module\n\nUse fake.echo for deterministic checks. Never treat fake output as real work.\n"
	const skill = "# Using fake.echo\n\nCall fake.echo with a `name`. It echoes deterministically and costs nothing.\n"

	for rel, body := range map[string]string{
		"agents/fake.md":             overlay,
		"skills/echo-usage/SKILL.md": skill,
	} {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
}

// KnowledgeLoader must report DISABLED distinctly from NOT INSTALLED.
//
// Both produce "no knowledge", and the prompt turns that into a correction the
// model reads. If they collapse, a user who disabled a module is told to
// install what they already have.
func TestTheLoaderReportsDisabledDistinctlyFromMissing(t *testing.T) {
	home := t.TempDir()
	dir := installFakeModule(t, home)
	writeFakeKnowledge(t, dir)

	if err := SetDisabled(dir, true); err != nil {
		t.Fatalf("SetDisabled: %v", err)
	}

	load := KnowledgeLoader(Discover(context.Background(), home))
	if load == nil {
		t.Fatal("no loader for a tree with one installed module")
	}

	overlays, skills, warnings := load("fake")

	if overlays != "" || skills != "" {
		t.Fatal("a disabled module still contributed knowledge through the loader")
	}
	if len(warnings) == 0 {
		t.Fatal("no warning, so the prompt has nothing to correct with and the" +
			" agent silently improvises")
	}
	joined := strings.Join(warnings, " ")
	if !strings.Contains(joined, "DISABLED") {
		t.Errorf("the warning does not say the module is disabled: %q", joined)
	}
	if strings.Contains(joined, "not installed") {
		t.Errorf("a disabled module was reported as not installed: %q", joined)
	}
}

// And a genuinely absent module still reports as absent.
func TestTheLoaderStillReportsAMissingModule(t *testing.T) {
	home := t.TempDir()
	installFakeModule(t, home)

	load := KnowledgeLoader(Discover(context.Background(), home))
	_, _, warnings := load("no.such.module")

	joined := strings.Join(warnings, " ")
	if !strings.Contains(joined, "not installed") {
		t.Fatalf("an absent module was not reported as absent: %q", joined)
	}
}

// The tool runs what the approval policy let through and refuses nothing on
// its own: an unpriced capability runs as a priced one does. It used to refuse
// it with consent_required whatever the owner had decided.
//
// It runs the REAL Execute against the REAL fake module, so the assertion is
// about the code path rather than about a condition restated in the test.
func TestTheToolLeavesTheDecisionToThePolicy(t *testing.T) {
	home := t.TempDir()
	installFakeModule(t, home)

	in := Discover(context.Background(), home)
	if len(in) == 0 || in[0].Descriptor == nil {
		t.Skip("the fake module did not describe itself here")
	}

	for _, id := range []string{"fake.echo", "fake.estimate.unpriced"} {
		var c modproto.Capability
		for _, declared := range in[0].Descriptor.Capabilities {
			if declared.ID == id {
				c = declared
			}
		}
		if c.ID == "" {
			t.Fatalf("fixture changed: the fake module no longer declares %s", id)
		}
		tool := &CapabilityTool{
			descriptor: in[0].Descriptor,
			capability: c,
			runner:     in[0].Runner,
			home:       home,
			workspace:  filepath.Join(home, "workspace"),
		}

		res := tool.Execute(context.Background(), map[string]any{"name": "hi"})
		if res == nil || res.IsError {
			t.Fatalf("%s did not run through the tool: %+v", id, res)
		}
	}
}
