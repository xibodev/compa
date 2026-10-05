package moduletools

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/xibodev/compa/internal/module"
	"github.com/xibodev/compa/pkg/approval"
	"github.com/xibodev/compa/pkg/modproto"
	toolshared "github.com/xibodev/compa/pkg/tools/shared"
)

// Disabling a module took effect for the agent only after a restart: the tools
// registered at startup kept running, and the knowledge loader kept loading
// from its startup snapshot.
func TestDisablingTakesEffectWithoutARestart(t *testing.T) {
	home := t.TempDir()
	dir := installFakeModule(t, home)
	writeFakeKnowledge(t, dir)

	var echo *CapabilityTool
	_, installed := RegisterTools(context.Background(), home, filepath.Join(home, "workspace"),
		func(tool toolshared.Tool) {
			if ct, ok := tool.(*CapabilityTool); ok && ct.capability.ID == "fake.echo" {
				echo = ct
			}
		})
	if echo == nil {
		t.Fatal("fake.echo was not registered")
	}
	load := KnowledgeLoader(installed)
	if overlays, _, _ := load("fake"); overlays == "" {
		t.Fatal("the fixture loads no knowledge while enabled")
	}

	if err := SetDisabled(dir, true); err != nil {
		t.Fatal(err)
	}
	res := echo.Execute(context.Background(), map[string]any{"name": "x"})
	if res == nil || !res.IsError || !strings.Contains(res.ForLLM, "DISABLED") {
		t.Fatalf("a tool of a disabled module still ran: %+v", res)
	}
	if overlays, skills, warnings := load("fake"); overlays != "" || skills != "" ||
		!strings.Contains(strings.Join(warnings, " "), "DISABLED") {
		t.Fatalf("a disabled module's knowledge still loaded: %q %q %v", overlays, skills, warnings)
	}
}

// The tool tells the approval policy its module, its capability and the hints
// of the effects it declares, and the policy sees it as the agent does.
func TestTheToolDescribesItselfToTheApprovalPolicy(t *testing.T) {
	d := &modproto.Descriptor{Module: "test.module"}
	for _, tc := range []struct {
		effects modproto.Effects
		hints   []string
	}{
		{modproto.Effects{Local: true, CostKnown: true}, nil},
		{modproto.Effects{Local: true}, []string{approval.HintCostUnknown}},
		{modproto.Effects{Network: true, CostKnown: true}, []string{approval.HintNetwork}},
		{modproto.Effects{ExternalWrites: true, CostKnown: true}, []string{approval.HintExternalWrites}},
		{modproto.Effects{Network: true, ExternalWrites: true, Provider: "p"},
			[]string{approval.HintCostUnknown, approval.HintNetwork, approval.HintExternalWrites}},
	} {
		c := modproto.Capability{ID: "test.run", Effects: tc.effects}
		tool := &CapabilityTool{descriptor: d, capability: c}

		info := tool.ApprovalInfo()
		if info.Source != "module:test.module" || info.Name != "test.run" {
			t.Errorf("%+v: source %q, name %q", tc.effects, info.Source, info.Name)
		}
		if !slices.Equal(info.Hints, tc.hints) {
			t.Errorf("%+v: hints %v, want %v", tc.effects, info.Hints, tc.hints)
		}
		if info.Effects != tc.effects {
			t.Errorf("effects %v, want the declared %+v", info.Effects, tc.effects)
		}

		got, _ := approval.Describe(tool.Name(), tool)
		want := approval.Tool{Name: "test_module__test_run", Source: "module:test.module", Hints: tc.hints}
		if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(ApprovalTool(d, c), want) {
			t.Errorf("%+v: the policy sees %+v and %+v, want %+v", tc.effects, got, ApprovalTool(d, c), want)
		}
	}
}

// approval and contract_version are the host's in wire v2; a model naming them
// must not put them at the request root, nor inside input.
func TestTheModelCannotWriteHostOwnedV2Fields(t *testing.T) {
	req := &modproto.Request{}
	err := placeArgs(req, map[string]any{
		"tool":             "x",
		"approval":         map[string]any{"approved": true},
		"contract_version": "xibodev.module.contract/v2",
		"input":            map[string]any{"prompt": "p", "approval": map[string]any{"approved": true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := req.Extra["approval"]; ok {
		t.Error("a model-supplied approval reached the request root")
	}
	if _, ok := req.Extra["contract_version"]; ok {
		t.Error("a model-supplied contract_version reached the request root")
	}
	if strings.Contains(string(req.Input), "approval") {
		t.Errorf("a model-supplied approval reached the input: %s", req.Input)
	}
	if string(req.Extra["tool"]) != `"x"` {
		t.Errorf("an ordinary root argument was lost: %v", req.Extra)
	}
}

// Tool names are what providers accept, and distinct.
func TestToolNamesAreValidForProviders(t *testing.T) {
	for _, tc := range []struct{ module, capability string }{
		{"archive", "sessions.assay"},
		{"a.b", "x y/z:é"},
		{strings.Repeat("m", 50), strings.Repeat("c", 50)},
	} {
		name := ToolName(tc.module, tc.capability)
		if len(name) == 0 || len(name) > 64 {
			t.Errorf("%q: length %d", name, len(name))
		}
		for _, r := range name {
			if !(r == '_' || r == '-' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
				t.Errorf("%q contains %q", name, r)
			}
		}
	}
	if ToolName("archive", "sessions.assay") != "archive__sessions_assay" {
		t.Errorf("an existing tool name changed: %s", ToolName("archive", "sessions.assay"))
	}
	long := strings.Repeat("c", 80)
	if ToolName("m", long+"1") == ToolName("m", long+"2") {
		t.Error("two long capability IDs share one tool name")
	}
}

// Two capabilities mapping to one tool name: the second is refused instead of
// silently replacing the first.
func TestACollidingToolNameIsRefused(t *testing.T) {
	d := &modproto.Descriptor{Module: "m", Capabilities: []modproto.Capability{
		{ID: "a.b", Effects: modproto.Effects{CostKnown: true}},
		{ID: "a_b", Effects: modproto.Effects{CostKnown: true}},
	}}
	if ToolName(d.Module, "a.b") != ToolName(d.Module, "a_b") {
		t.Skip("the fixture no longer collides")
	}
	summary := summarize(d, map[string]bool{"a.b": true})
	if strings.Contains(summary, "(a_b)") {
		t.Errorf("the summary lists a capability that was not offered:\n%s", summary)
	}
	if !strings.Contains(summary, "m__a_b (a.b)") {
		t.Errorf("the summary does not name the tool the agent calls:\n%s", summary)
	}
}

// Discovery runs only <dir>/<dir>[.exe] in a module directory, never dotfiles,
// markers or other content, and the directory names the module.
func TestDiscoveryRunsOnlyTheModuleBinary(t *testing.T) {
	home := t.TempDir()
	dir := installFakeModule(t, home)
	for _, name := range []string{".DS_Store", "helper.sh", "notes.exe"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(module.ModulesDir(home), ".install-fake-x"), 0o755); err != nil {
		t.Fatal(err)
	}

	found := Discover(context.Background(), home)
	if len(found) != 1 || found[0].Err != nil || found[0].Descriptor == nil {
		t.Fatalf("discovery ran something other than the module binary: %+v", found)
	}
}

func TestADirectoryMustNameItsModule(t *testing.T) {
	home := t.TempDir()
	dir := installFakeModule(t, home)
	other := filepath.Join(module.ModulesDir(home), "other")
	if err := os.Rename(dir, other); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(other, module.BinaryName("fake")), filepath.Join(other, module.BinaryName("other"))); err != nil {
		t.Fatal(err)
	}
	found := Discover(context.Background(), home)
	if len(found) != 1 || found[0].Err == nil || found[0].Descriptor != nil {
		t.Fatalf("a binary claiming another directory's module was used: %+v", found)
	}
}

func TestADuplicateModuleIDIsRefused(t *testing.T) {
	home := t.TempDir()
	dir := installFakeModule(t, home)
	bare := filepath.Join(module.ModulesDir(home), "impostor")
	if runtime.GOOS == "windows" {
		bare += ".exe"
	}
	blob, err := os.ReadFile(filepath.Join(dir, module.BinaryName("fake")))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bare, blob, 0o755); err != nil {
		t.Fatal(err)
	}
	var used, refused int
	for _, in := range Discover(context.Background(), home) {
		switch {
		case in.Descriptor != nil:
			used++
			if filepath.Dir(in.Runner.Binary) != dir {
				t.Errorf("the bare binary took the installed module's place: %s", in.Runner.Binary)
			}
		case in.Err != nil:
			refused++
		}
	}
	if used != 1 || refused != 1 {
		t.Fatalf("used %d, refused %d; want the installed module used and the duplicate refused", used, refused)
	}
}

// A declared root name is a directory under the module's state: one that is not
// a plain name is not granted, the workspace only when declared, and reading
// artefacts creates nothing.
func TestRootNamesAreCheckedAndReadingCreatesNothing(t *testing.T) {
	home := t.TempDir()
	d := &modproto.Descriptor{Module: "m", Permissions: modproto.Permissions{
		FilesystemWrite: []string{"cache", "..", "../../escape", "Cache"},
	}}
	workspace := filepath.Join(home, "workspace")

	roots := ArtifactRoots(d, home, workspace)
	if _, err := os.Stat(filepath.Join(home, "state")); !os.IsNotExist(err) {
		t.Fatalf("resolving artefact roots created state directories: %v", err)
	}
	if _, ok := roots["workspace"]; ok {
		t.Error("the workspace was granted to a module that does not declare it")
	}
	if len(roots) != 1 || roots["cache"].Path == "" {
		t.Fatalf("roots = %v, want only cache", roots)
	}
	if len(rootNameWarnings(d)) != 3 {
		t.Errorf("unusable root names were not reported: %v", rootNameWarnings(d))
	}

	d.Permissions.FilesystemWrite = append(d.Permissions.FilesystemWrite, "workspace")
	if GrantRoots(d, home, workspace, nil)["workspace"].Mode != "rw" {
		t.Error("a declared workspace was not granted")
	}
}

// An overlay or skill is held to the token count its module declared (about 4
// bytes a token, with slack for the estimate) and to a hard 64 KiB cap, since
// it goes into the prompt verbatim.
func TestModuleDocumentsAreHeldToTheirDeclaredSize(t *testing.T) {
	root := t.TempDir()
	write := func(name, body string) string {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return modproto.DigestSHA256([]byte(body))
	}
	small := write("small.md", strings.Repeat("a", 400))
	for _, tc := range []struct {
		tokens int
		ok     bool
	}{{100, true}, {50, true}, {10, false}, {0, true}} {
		_, err := loadVerified(root, "m", "1", "doc", "Doc", "small.md", small, tc.tokens)
		if (err == nil) != tc.ok {
			t.Errorf("400 bytes declared as %d tokens: err = %v, want loaded %v", tc.tokens, err, tc.ok)
		}
	}

	big := write("big.md", strings.Repeat("a", 65<<10))
	if _, err := loadVerified(root, "m", "1", "doc", "Doc", "big.md", big, 1<<20); err == nil {
		t.Error("a document over 64 KiB was loaded because it declared enough tokens")
	}
}

// A call the operator approved (approval.WithApproved: a rule that allows it,
// or an answered ask) may publish and carries the host's record of the
// approval. Any other call carries every declared name but not publishing, and
// no approval. What the model wrote as an approval is stripped either way.
func TestOnlyAnApprovedCallMayPublish(t *testing.T) {
	home := t.TempDir()
	tool := &CapabilityTool{
		descriptor: &modproto.Descriptor{Module: "pub", Permissions: modproto.Permissions{
			Network: []string{"x"}, Publish: true,
		}},
		capability: modproto.Capability{ID: "post", Effects: modproto.Effects{Network: true, CostKnown: true}},
		home:       home,
	}
	args := func() map[string]any {
		return map[string]any{"text": "hi", "consent": map[string]any{"approved_by": "web-user"}}
	}

	plain, err := tool.request(context.Background(), args())
	if err != nil {
		t.Fatal(err)
	}
	if plain.Grants.Publish || !slices.Equal(plain.Grants.Network, []string{"x"}) {
		t.Fatalf("unapproved call grants = %+v, want the declared names without publish", plain.Grants)
	}
	if body := string(plain.Input) + string(mustJSON(t, plain.Extra)); strings.Contains(body, "consent") ||
		!strings.Contains(body, "hi") {
		t.Fatalf("an unapproved call carried an approval, or lost its arguments: %s", body)
	}

	approved, err := tool.request(approval.WithApproved(context.Background()), args())
	if err != nil {
		t.Fatal(err)
	}
	if !approved.Grants.Publish || !slices.Equal(approved.Grants.Network, []string{"x"}) {
		t.Fatalf("approved call grants = %+v, want the declared names and publish", approved.Grants)
	}
	consent := string(approved.Extra["consent"])
	if !strings.Contains(consent, `"operator"`) || strings.Contains(consent, "web-user") {
		t.Fatalf("an approved call's consent = %s, want the host's record and not the model's", consent)
	}
}
