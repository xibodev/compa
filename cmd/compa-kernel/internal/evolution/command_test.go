package evolution

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xibodev/compa/v2/pkg/evolution"
)

const weatherBody = "---\nname: weather\ndescription: weather helper\n---\n# Weather\nUse native names.\n"

// newDraftsTarget stores two candidate drafts in a fresh workspace and
// returns a runner of the evolution command on it; write says whether an
// accepted draft is written at once, as in the "apply" mode.
func newDraftsTarget(t *testing.T, write bool) (target, func(args ...string) (string, error)) {
	t.Helper()
	workspace := t.TempDir()
	tgt := target{workspace: workspace, paths: evolution.NewPaths(workspace, ""), write: write}
	if err := evolution.NewStore(tgt.paths).SaveDrafts([]evolution.SkillDraft{
		{
			ID: "d-create", WorkspaceID: workspace, TargetSkillName: "weather",
			ChangeKind: evolution.ChangeKindCreate, DraftType: evolution.DraftTypeShortcut,
			HumanSummary: "weather helper", BodyOrPatch: weatherBody, Status: evolution.DraftStatusCandidate,
		},
		{
			ID: "d-other", WorkspaceID: workspace, TargetSkillName: "notes",
			ChangeKind: evolution.ChangeKindCreate, HumanSummary: "notes", Status: evolution.DraftStatusCandidate,
			BodyOrPatch: "---\nname: notes\ndescription: notes\n---\n# Notes\nx\n",
		},
	}); err != nil {
		t.Fatal(err)
	}
	load := func() (target, error) { return tgt, nil }
	return tgt, func(args ...string) (string, error) {
		cmd := newEvolutionCommand(load)
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		err := cmd.Execute()
		return out.String(), err
	}
}

func TestDraftsListAcceptReject(t *testing.T) {
	tgt, run := newDraftsTarget(t, true)

	out, err := run("drafts", "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "d-create") || !strings.Contains(out, "+Use native names.") {
		t.Fatalf("list output lacks the draft or its preview:\n%s", out)
	}

	if _, err := run("drafts", "reject", "d-other"); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if out, err = run("drafts", "accept", "d-create"); err != nil {
		t.Fatalf("accept: %v\n%s", err, out)
	}
	if !strings.Contains(out, `Draft d-create accepted: skill "weather" updated (create).`) {
		t.Fatalf("accept output:\n%s", out)
	}
	got, err := os.ReadFile(filepath.Join(tgt.workspace, "skills", "weather", "SKILL.md"))
	if err != nil || strings.TrimSpace(string(got)) != strings.TrimSpace(weatherBody) {
		t.Fatalf("skill = %q, %v; want the accepted body", got, err)
	}
	if _, err := os.Stat(filepath.Join(tgt.workspace, "skills", "notes")); !os.IsNotExist(err) {
		t.Fatalf("rejected draft was written (stat err = %v)", err)
	}
	if out, _ = run("drafts", "list"); !strings.Contains(out, "No drafts waiting for review.") {
		t.Fatalf("list after review:\n%s", out)
	}
	if _, err := run("drafts", "accept", "missing"); err == nil {
		t.Fatal("accept of an unknown draft succeeded")
	}
}

// Outside the apply mode, accept approves the draft without writing it.
func TestDraftsAcceptOutsideApplyMode(t *testing.T) {
	tgt, run := newDraftsTarget(t, false)

	out, err := run("drafts", "accept", "d-create")
	if err != nil {
		t.Fatalf("accept: %v\n%s", err, out)
	}
	want := `Draft d-create accepted; it is written to skill "weather" when evolution runs in apply mode.`
	if !strings.Contains(out, want) {
		t.Fatalf("accept output:\n%s\nwant %q", out, want)
	}
	if _, err := os.Stat(filepath.Join(tgt.workspace, "skills", "weather")); !os.IsNotExist(err) {
		t.Fatalf("the draft was written outside the apply mode (stat err = %v)", err)
	}
	draft, err := evolution.FindDraft(evolution.NewStore(tgt.paths), tgt.workspace, "d-create")
	if err != nil || draft.Status != evolution.DraftStatusApproved {
		t.Fatalf("draft = %+v, %v; want it approved", draft, err)
	}
}
