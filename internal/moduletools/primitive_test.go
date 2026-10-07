package moduletools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xibodev/compa/v3/pkg/modproto"
	"github.com/xibodev/compa/v3/pkg/view"
)

// The bug this exists for: how an artefact renders was decided twice -- once by
// pkg/view on the host, once by a media-type mapping in the cockpit's
// TypeScript. They disagreed on seven media types. A .docx resolved to
// "document" on the host and rendered as a bare download in the browser; a
// mermaid source resolved to "diagram" and rendered as raw text.
//
// Neither side was wrong about its own rules. There were simply two sets, and
// nothing compared them.
//
// The host now resolves once and sends the answer, so this asserts the answer
// is actually emitted -- the cockpit has nothing else to draw from.
func TestArtifactLineCarriesHostResolvedPrimitive(t *testing.T) {
	cases := []struct {
		mediaType string
		want      view.Primitive
	}{
		{"video/mp4", view.Video},
		{"image/svg+xml", view.Diagram},
		{"application/pdf", view.Document},
		// The seven that used to disagree.
		{"application/vnd.openxmlformats-officedocument.wordprocessingml.document", view.Document},
		{"application/msword", view.Document},
		{"application/rtf", view.Document},
		{"text/vnd.graphviz", view.Diagram},
		{"text/vnd.mermaid", view.Diagram},
		{"application/vnd.openxmlformats-officedocument.presentationml.presentation", view.Slides},
		{"application/vnd.ms-powerpoint", view.Slides},
	}

	home := t.TempDir()
	tool := pathFailureTool(t, home)
	tool.views = view.NewRegistry()

	for _, c := range cases {
		env := &modproto.Envelope{OK: true, Result: []byte(`{"ok":true}`)}
		env.Execution.Artifacts = []modproto.Artifact{{
			ID: "a", Kind: "output", Root: "project_root", Path: "a",
			MediaType: c.mediaType, Bytes: 1,
			Digest: "sha256:" + strings.Repeat("a", 64),
		}}

		got := artifactLine(t, tool.renderForModel(env))
		if got["primitive"] != string(c.want) {
			t.Errorf("%s: primitive = %v, want %q", c.mediaType, got["primitive"], c.want)
		}
	}
}

// A module's presentation hint is honoured when the host supports it -- only
// the module knows a JSON document is a timeline -- and ignored when it names
// something the host cannot draw. A module names a primitive; it cannot invent
// one.
func TestModuleHintHonouredOnlyWhenKnown(t *testing.T) {
	home := t.TempDir()
	tool := pathFailureTool(t, home)
	tool.views = view.NewRegistry()

	build := func(hint string) map[string]any {
		env := &modproto.Envelope{OK: true, Result: []byte(`{"ok":true}`)}
		env.Execution.Artifacts = []modproto.Artifact{{
			ID: "a", Kind: "output", Root: "project_root", Path: "a",
			MediaType: "application/json", Presentation: hint, Bytes: 1,
			Digest: "sha256:" + strings.Repeat("a", 64),
		}}
		return artifactLine(t, tool.renderForModel(env))
	}

	if got := build("timeline")["primitive"]; got != string(view.Timeline) {
		t.Errorf("known hint ignored: primitive = %v, want timeline", got)
	}
	// An invented primitive falls back to what the media type says, never to
	// the module's word.
	if got := build("holograph")["primitive"]; got != string(view.JSON) {
		t.Errorf("invented hint honoured: primitive = %v, want json", got)
	}
}

// A tool built without a registry must still emit a usable artefact: losing a
// renderer is a degraded card, losing the artefact is a lost result.
func TestNilRegistryStillEmitsArtifact(t *testing.T) {
	home := t.TempDir()
	tool := pathFailureTool(t, home) // views deliberately nil

	env := &modproto.Envelope{OK: true, Result: []byte(`{"ok":true}`)}
	env.Execution.Artifacts = []modproto.Artifact{{
		ID: "a", Kind: "output", Root: "project_root", Path: "a",
		MediaType: "video/mp4", Bytes: 1,
		Digest: "sha256:" + strings.Repeat("a", 64),
	}}

	got := artifactLine(t, tool.renderForModel(env))
	if got["primitive"] != string(view.Download) {
		t.Fatalf("primitive = %v, want the download floor", got["primitive"])
	}
	if got["id"] != "a" {
		t.Fatalf("artefact lost its identity: %+v", got)
	}
}

// artifactLine pulls the single marked JSON line out of a rendered result.
func artifactLine(t *testing.T, rendered string) map[string]any {
	t.Helper()
	for _, ln := range strings.Split(rendered, "\n") {
		ln = strings.TrimSpace(ln)
		if !strings.HasPrefix(ln, ArtifactMarker) {
			continue
		}
		var out map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(ln, ArtifactMarker)), &out); err != nil {
			t.Fatalf("artifact line is not valid JSON: %v", err)
		}
		return out
	}
	t.Fatalf("no artifact line in:\n%s", rendered)
	return nil
}

// The bug this exists for: told "I approve any cost", the agent sent
// consent:{approved_by:"web-user", paid_generation_approved:true} -- a field
// it minted from a sentence, naming a user who never saw an approval prompt.
// The host passed it through unchanged.
//
// The only thing between that chat message and a provider charge was that the
// provider happened to be unconfigured. ELEVENLABS_API_KEY is set on this
// machine, so the path is real rather than theoretical.
func TestModelCannotMintConsent(t *testing.T) {
	args := map[string]any{
		"tool": "gflow_image",
		"consent": map[string]any{
			"approved_by":              "web-user",
			"paid_generation_approved": true,
		},
		"paid_generation_approved": true,
		"input":                    map[string]any{"prompt": "a cat", "cost_approved": true},
	}

	req := &modproto.Request{}
	if err := placeArgs(req, args); err != nil {
		t.Fatalf("placeArgs: %v", err)
	}

	body := string(req.Input) + string(mustJSON(t, req.Extra))
	for _, banned := range []string{"paid_generation_approved", "approved_by", "cost_approved", "\"consent\""} {
		if strings.Contains(body, banned) {
			t.Errorf("model-supplied %s survived into the request:\n%s", banned, body)
		}
	}
	// The real argument must survive: this strips authorization, not payload.
	if !strings.Contains(body, "a cat") {
		t.Errorf("stripping consent also removed the actual input:\n%s", body)
	}
	if !strings.Contains(body, "gflow_image") {
		t.Errorf("stripping consent removed the tool selection:\n%s", body)
	}
}

// A request with no consent claim is untouched.
func TestOrdinaryArgumentsAreNotStripped(t *testing.T) {
	args := map[string]any{"tool": "media_probe", "input": map[string]any{"input": "x.wav"}}
	req := &modproto.Request{}
	if err := placeArgs(req, args); err != nil {
		t.Fatalf("placeArgs: %v", err)
	}
	if !strings.Contains(string(req.Input), "x.wav") {
		t.Fatalf("ordinary input was altered: %s", req.Input)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// A person clicking "Approve and run" is not the model's word.
//
// The cockpit shows a capability's declared effects and only then offers the
// button, so a click is a human acting on what they were told. That approval
// must survive into the request, or a capability needing consent can never be
// run at all -- which is what stripping unconditionally did.
func TestHumanApprovalSurvives(t *testing.T) {
	raw := []byte(`{"tool":"gflow_image","consent":{"approved_by":"operator","paid_generation_approved":true},"input":{"prompt":"cat"}}`)

	req := &modproto.Request{}
	if err := PlaceInput(req, raw, true); err != nil {
		t.Fatalf("PlaceInput: %v", err)
	}

	body := string(req.Input) + string(mustJSON(t, req.Extra))
	if !strings.Contains(body, "paid_generation_approved") {
		t.Fatalf("a human approval was stripped, so an approved run cannot"+
			" proceed:\n%s", body)
	}
	if !strings.Contains(body, "operator") {
		t.Fatalf("the approver was lost:\n%s", body)
	}
}

// The same payload arriving WITHOUT a human approval is stripped. The two
// callers look identical and mean opposite things.
func TestUnapprovedInputIsStripped(t *testing.T) {
	raw := []byte(`{"tool":"gflow_image","consent":{"approved_by":"web-user","paid_generation_approved":true},"input":{"prompt":"cat"}}`)

	req := &modproto.Request{}
	if err := PlaceInput(req, raw, false); err != nil {
		t.Fatalf("PlaceInput: %v", err)
	}

	body := string(req.Input) + string(mustJSON(t, req.Extra))
	if strings.Contains(body, "paid_generation_approved") {
		t.Fatalf("an unapproved consent claim survived:\n%s", body)
	}
	if !strings.Contains(body, "cat") {
		t.Fatalf("stripping consent also removed the payload:\n%s", body)
	}
}

// The bug this exists for: the agent handed the user paste-ready JSON for the
// Modules page, the user pasted exactly that, and the run still failed
// consent_required -- because the JSON carried no consent field and
// approved:true only PERMITTED a consent to pass, it did not supply one.
//
// The click is the approval: the page showed the declared effects and the user
// pressed "Approve and run". Requiring them to also hand-write a consent object
// asks them to author the one thing they cannot legitimately author.
func TestApprovedRunRecordsItsOwnConsent(t *testing.T) {
	raw := []byte(`{"tool":"gflow_image","input":{"prompt":"a cat"}}`)

	req := &modproto.Request{}
	if err := PlaceInput(req, raw, true); err != nil {
		t.Fatalf("PlaceInput: %v", err)
	}

	body := string(req.Input) + string(mustJSON(t, req.Extra))
	if !strings.Contains(body, "paid_generation_approved") {
		t.Fatalf("an approved run carried no consent, so a module's gate still"+
			" refuses it:\n%s", body)
	}
	if !strings.Contains(body, "operator") {
		t.Fatalf("the approval names no approver:\n%s", body)
	}
	if !strings.Contains(body, "a cat") {
		t.Fatalf("recording consent lost the payload:\n%s", body)
	}
}

// A consent the caller already supplied is left alone: a module may define
// fields the host does not know about, and overwriting them would be the host
// inventing detail it cannot vouch for.
func TestSuppliedConsentIsNotOverwritten(t *testing.T) {
	raw := []byte(`{"tool":"x","consent":{"approved_by":"someone-else","scope":"one image"},"input":{}}`)

	req := &modproto.Request{}
	if err := PlaceInput(req, raw, true); err != nil {
		t.Fatalf("PlaceInput: %v", err)
	}

	body := string(req.Input) + string(mustJSON(t, req.Extra))
	if !strings.Contains(body, "someone-else") || !strings.Contains(body, "one image") {
		t.Fatalf("the caller's own consent was replaced:\n%s", body)
	}
}

// An UNapproved run gains no consent. The host records approval only where a
// person actually gave one.
func TestUnapprovedRunGainsNoConsent(t *testing.T) {
	raw := []byte(`{"tool":"gflow_image","input":{"prompt":"a cat"}}`)

	req := &modproto.Request{}
	if err := PlaceInput(req, raw, false); err != nil {
		t.Fatalf("PlaceInput: %v", err)
	}

	body := string(req.Input) + string(mustJSON(t, req.Extra))
	if strings.Contains(body, "paid_generation_approved") {
		t.Fatalf("the host minted consent for an unapproved run:\n%s", body)
	}
}
