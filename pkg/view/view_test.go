package view_test

import (
	"testing"

	"github.com/xibodev/compa/v3/pkg/view"
)

func TestViewDefinition_DigestDeterminism(t *testing.T) {
	v1 := &view.ViewDefinition{
		ID:             "render.video_production",
		SchemaVersion:  "1.0",
		Title:          "Video Workbench",
		Module:         "render",
		ArtifactSchema: "example.render.output/v1",
		Sections: []view.SectionDefinition{
			{
				ID:        "preview_tab",
				Title:     "Preview",
				Type:      "tab",
				Primitive: view.Video,
				Binding:   "artifacts.primary_video",
			},
		},
		Actions: []view.ActionDefinition{
			{
				ID:    "rerender",
				Label: "Re-render Scene",
				Tool:  "video_compose",
			},
		},
	}

	d1 := v1.Digest()
	d2 := v1.Digest()

	if d1 == "" || d2 == "" {
		t.Fatal("expected non-empty digest")
	}
	if d1 != d2 {
		t.Errorf("expected deterministic digest, got %q vs %q", d1, d2)
	}

	// Change a field and verify digest changes
	v2 := *v1
	v2.Title = "Changed Title"
	if v2.Digest() == d1 {
		t.Errorf("digest should change when definition changes")
	}
}

func TestViewDefinition_ActionsAndStates(t *testing.T) {
	v := &view.ViewDefinition{
		ID:     "test.view",
		Title:  "Test",
		Module: "test",
		Sections: []view.SectionDefinition{
			{
				ID:        "content_sec",
				Type:      "card",
				Primitive: view.Markdown,
				Binding:   "content.body",
				EmptyText: "No content available yet.",
			},
		},
		Actions: []view.ActionDefinition{
			{
				ID:               "approve_action",
				Label:            "Approve & Render",
				Tool:             "publish_tool",
				RequiresApproval: true,
				KeyboardShortcut: "Ctrl+Enter",
			},
		},
	}

	if len(v.Actions) != 1 {
		t.Fatalf("expected 1 action")
	}
	action := v.Actions[0]
	if action.Tool != "publish_tool" {
		t.Errorf("expected tool 'publish_tool', got %q", action.Tool)
	}
	if !action.RequiresApproval {
		t.Errorf("expected requires_approval=true")
	}

	// Verify standard states exist
	states := []view.ViewState{
		view.StateLoading,
		view.StateEmpty,
		view.StateFailed,
		view.StateAwaitingApproval,
		view.StateCompleted,
		view.StateUnsupported,
	}
	if len(states) != 6 {
		t.Errorf("expected 6 standard states")
	}
}
