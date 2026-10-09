package moduletools

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/xibodev/compa/v4/pkg/modproto"
)

func pathFailureTool(t *testing.T, home string) *CapabilityTool {
	t.Helper()
	d := installedModule(t, home, "test.module")
	// The workspace is granted only to a module that declares it.
	d.Permissions.FilesystemWrite = []string{"workspace", "project_root"}
	return &CapabilityTool{
		descriptor: d,
		capability: modproto.Capability{ID: "test.run"},
		home:       home,
		workspace:  filepath.Join(home, "workspace"),
	}
}

// A module that cannot find a file gets the granted roots named, because the
// host is the only party that knows them and the agent's alternative is to
// guess at paths.
func TestPathFailureNamesGrantedRoots(t *testing.T) {
	home := t.TempDir()
	tool := pathFailureTool(t, home)

	got := tool.failureGuidance("input_not_found", "input does not exist", "")

	if !strings.Contains(got, "project_root") {
		t.Fatalf("guidance does not name the granted root:\n%s", got)
	}
	if !strings.Contains(got, "workspace") {
		t.Fatalf("guidance does not name the workspace root:\n%s", got)
	}
	if !strings.Contains(got, "absolute path") {
		t.Fatalf("guidance does not say what kind of path works:\n%s", got)
	}
}

// The instruction not to improvise must survive alongside the new hint: the
// blank-video incident happened because an agent treated a module failure as
// permission to do the job another way.
func TestPathFailureStillForbidsImprovising(t *testing.T) {
	home := t.TempDir()
	tool := pathFailureTool(t, home)

	got := tool.failureGuidance("input_not_found", "input does not exist", "")

	if !strings.Contains(got, "Do NOT attempt this task with shell commands") {
		t.Fatalf("lost the do-not-improvise instruction:\n%s", got)
	}
}

// A schema error must still route to the schema rather than to the root list --
// naming directories for a bad field name would send the agent to the wrong
// place.
func TestInvalidRequestStillRoutesToSchema(t *testing.T) {
	home := t.TempDir()
	tool := pathFailureTool(t, home)

	got := tool.failureGuidance(modproto.ErrInvalidRequest, `unknown field "width"`, "")

	if !strings.Contains(got, "Re-read this tool's schema") {
		t.Fatalf("schema error lost its schema advice:\n%s", got)
	}
	if strings.Contains(got, "granted these directories") {
		t.Fatalf("schema error was given root advice instead:\n%s", got)
	}
}

func TestIsPathFailure(t *testing.T) {
	cases := []struct {
		code, message string
		want          bool
	}{
		{"input_not_found", "input does not exist", true},
		{modproto.ErrPathOutsideRoot, "escapes root", true},
		{"command_failed", "no such file or directory", true},
		{"invalid_request", "input path is required", true},
		{"invalid_request", `unknown field "width"`, false},
		{"consent_required", "requires human consent", false},
		{"provider_failure", "upstream returned 500", false},
	}
	for _, c := range cases {
		if got := isPathFailure(c.code, c.message); got != c.want {
			t.Errorf("isPathFailure(%q, %q) = %v, want %v", c.code, c.message, got, c.want)
		}
	}
}

// The cockpit can only read what the ASSISTANT says: tool results are never
// forwarded to the browser, so a marker that stops at the tool result reaches
// the model and nothing else.
//
// Observed before this instruction existed: a seed.create artifact arrived in
// the tool result, the model described it in prose, and no card rendered.
func TestArtifactsAskTheModelToRepeatTheMarker(t *testing.T) {
	home := t.TempDir()
	tool := pathFailureTool(t, home)

	env := &modproto.Envelope{OK: true, Result: []byte(`{"ok":true}`)}
	env.Execution.Artifacts = []modproto.Artifact{{
		ID: "seed", Kind: "seed/v1", Root: "project_root", Path: "s.json",
		MediaType: "application/json", Bytes: 12,
		Digest: "sha256:" + strings.Repeat("a", 64),
	}}

	got := tool.renderForModel(env)

	if !strings.Contains(got, ArtifactMarker) {
		t.Fatalf("no artifact marker emitted:\n%s", got)
	}
	if !strings.Contains(got, "copied into") {
		t.Fatalf("the model was not asked to repeat the marker, so no card"+
			" renders:\n%s", got)
	}
}

// A result with no artifacts must not carry the instruction: telling a model to
// copy lines that do not exist invites it to invent one.
func TestNoArtifactsNoInstruction(t *testing.T) {
	home := t.TempDir()
	tool := pathFailureTool(t, home)

	got := tool.renderForModel(&modproto.Envelope{OK: true, Result: []byte(`{"ok":true}`)})

	if strings.Contains(got, "copied into") {
		t.Fatalf("instruction emitted with no artifacts to copy:\n%s", got)
	}
}

// The bug this exists for: a module's bundled Node renderer had a partially
// extracted package, and the failure surfaced as "Cannot find module
// './dist/index'". The generic guidance told the agent to correct the request
// and retry -- but nothing about the request was wrong, so every retry failed
// identically, and a model that exhausts retries starts looking for another way
// to do the job.
func TestBrokenRuntimeSaysDoNotRetry(t *testing.T) {
	home := t.TempDir()
	tool := pathFailureTool(t, home)

	got := tool.failureGuidance("command_failed",
		"remotion render failed: Error: Cannot find module './dist/index'", "")

	if !strings.Contains(got, "MISSING DEPENDENCY") {
		t.Fatalf("guidance does not name the real cause:\n%s", got)
	}
	if !strings.Contains(got, "Do not retry") {
		t.Fatalf("guidance does not stop the retry loop:\n%s", got)
	}
	if !strings.Contains(got, "Do NOT attempt this task with shell commands") {
		t.Fatalf("lost the do-not-improvise instruction:\n%s", got)
	}
}

// A bad request must still be told to correct itself: routing a schema error to
// "your runtime is broken" would send the agent to the wrong place.
func TestBadRequestStillRoutesToSchema(t *testing.T) {
	home := t.TempDir()
	tool := pathFailureTool(t, home)

	got := tool.failureGuidance(modproto.ErrInvalidRequest, `unknown field "width"`, "")

	if !strings.Contains(got, "Re-read this tool's schema") {
		t.Fatalf("schema error lost its advice:\n%s", got)
	}
	if strings.Contains(got, "MISSING DEPENDENCY") {
		t.Fatalf("schema error misrouted as a runtime failure:\n%s", got)
	}
}

func TestIsBrokenRuntimeFailure(t *testing.T) {
	cases := []struct {
		code, message, stderr string
		want                  bool
	}{
		{"command_failed", "Cannot find module './dist/index'", "", true},
		{"command_failed", "", "ModuleNotFoundError: No module named 'x'", true},
		{"command_failed", "ffmpeg: command not found", "", true},
		{modproto.ErrMissingRequirement, "binary unavailable", "", true},
		{"invalid_request", `unknown field "width"`, "", false},
		{"input_not_found", "input does not exist", "", false},
		{"provider_failure", "upstream returned 500", "", false},
	}
	for _, c := range cases {
		if got := isBrokenRuntimeFailure(c.code, c.message, c.stderr); got != c.want {
			t.Errorf("isBrokenRuntimeFailure(%q,%q,%q) = %v, want %v",
				c.code, c.message, c.stderr, got, c.want)
		}
	}
}

// The bug this exists for: a capability needing approval failed with
// consent_required, the agent read the generic "correct the request and retry"
// advice, asked the user to approve IN CHAT, received an approval the host
// strips by design, and then reported that the module "wants a stricter consent
// format".
//
// A module's consent refusal means the approval policy let the call run without
// the owner's approval. The remedy is a tools.approval rule for the tool, and
// the host is the only party that knows that.
func TestConsentFailureNamesTheApprovalRule(t *testing.T) {
	home := t.TempDir()
	tool := pathFailureTool(t, home)

	got := tool.failureGuidance("consent_required",
		"tool may incur real cost and requires explicit human consent", "")

	for _, want := range []string{tool.Name(), "tools.approval", "test.run", "Approve and run"} {
		if !strings.Contains(got, want) {
			t.Errorf("guidance does not contain %q:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "Do not ask the user to approve here") {
		t.Fatalf("guidance does not stop the chat-approval loop:\n%s", got)
	}
	if !strings.Contains(got, "Do NOT attempt this task with shell commands") {
		t.Fatalf("lost the do-not-improvise instruction:\n%s", got)
	}
}

// A path failure must still get the granted roots, not approval advice.
func TestPathFailureStillRoutesToRoots(t *testing.T) {
	home := t.TempDir()
	tool := pathFailureTool(t, home)

	got := tool.failureGuidance("input_not_found", "input does not exist", "")

	if strings.Contains(got, "Approve and run") {
		t.Fatalf("a missing file was routed to approval advice:\n%s", got)
	}
	if !strings.Contains(got, "granted these directories") {
		t.Fatalf("path failure lost its root list:\n%s", got)
	}
}
