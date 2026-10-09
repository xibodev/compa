package api

import (
	"testing"

	"github.com/xibodev/compa/v4/pkg/modproto"
)

// The bug this exists for: a module returned ok:true with an artifact whose
// path was absolute. The host correctly judged the envelope inadmissible --
// confinement cannot be checked against a root when the path does not sit
// inside one -- and recorded a warning, but the response still said ok:true and
// the cockpit rendered an artifact card for it.
//
// A module reports on itself; the host decides whether that report is
// admissible. Letting the module's ok survive a host refusal lets it overrule
// the host.
func TestHostRefusalOverridesModuleOK(t *testing.T) {
	out := &InvokeResult{OK: true}

	applyHostRefusal(out, errProtocolViolation{})

	if out.OK {
		t.Fatal("module's ok survived a host refusal: the module overruled the host")
	}
	if out.Error == nil {
		t.Fatal("refused invocation reported no error, so nothing says why it failed")
	}
	if out.Error.Code != modproto.ErrHostProtocolViolation {
		t.Fatalf("error code = %q, want %q", out.Error.Code, modproto.ErrHostProtocolViolation)
	}
	if len(out.HostWarnings) == 0 {
		t.Fatal("host refusal left no warning naming the rule that was broken")
	}
}

// A module that failed on its own terms keeps its own error: "the module failed
// AND broke the contract" is more useful than either half alone.
func TestHostRefusalPreservesModuleError(t *testing.T) {
	moduleErr := &modproto.Error{
		Code:    "provider_failure",
		Message: "upstream returned 500",
		Details: map[string]any{},
	}
	out := &InvokeResult{OK: false, Error: moduleErr}

	applyHostRefusal(out, errProtocolViolation{})

	if out.Error.Code != "provider_failure" {
		t.Fatalf("module's own error was replaced: got %q", out.Error.Code)
	}
	if len(out.HostWarnings) == 0 {
		t.Fatal("host's finding was lost when the module had its own error")
	}
}

// A clean invocation is untouched.
func TestNoRefusalLeavesResultAlone(t *testing.T) {
	out := &InvokeResult{OK: true}

	applyHostRefusal(out, nil)

	if !out.OK {
		t.Fatal("a successful invocation was marked failed")
	}
	if out.Error != nil {
		t.Fatalf("a successful invocation gained an error: %+v", out.Error)
	}
}

type errProtocolViolation struct{}

func (errProtocolViolation) Error() string {
	return "host.protocol_violation: 1 contract violation(s): " +
		"execution.artifacts[0].path: must be relative to its declared root, not absolute"
}

// An artifact from a refused envelope must not reach the cockpit.
//
// The host judged the envelope inadmissible, which for an artifact means its
// confinement could not be checked. Passing it on would render a card -- path,
// size, download link -- for a file the host declined to vouch for.
func TestHostRefusalDropsArtifacts(t *testing.T) {
	out := &InvokeResult{OK: true}
	out.Execution.Artifacts = []modproto.Artifact{
		{ID: "leaked.mp4", Root: "project_root", Path: "/absolute/leaked.mp4"},
	}

	applyHostRefusal(out, errProtocolViolation{})

	if len(out.Execution.Artifacts) != 0 {
		t.Fatalf("refused envelope still carried %d artifact(s): the cockpit"+
			" would render a card for a file the host refused to vouch for",
			len(out.Execution.Artifacts))
	}
	if len(out.HostWarnings) == 0 {
		t.Fatal("dropped the artifact without saying why")
	}
}
