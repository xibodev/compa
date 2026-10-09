package moduletools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xibodev/compa/v4/internal/module"
	"github.com/xibodev/compa/v4/pkg/modproto"
)

func TestKnowledgeToolVerifiesAndRechecksEnablement(t *testing.T) {
	root := t.TempDir()
	blob := []byte("verified pilot guidance")
	path := filepath.Join(root, "guide.md")
	if err := os.WriteFile(path, blob, 0600); err != nil {
		t.Fatal(err)
	}
	d := &modproto.Descriptor{Module: "test", Version: "1", Skills: []modproto.Skill{{ID: "guide", Path: "guide.md", Digest: modproto.DigestSHA256(blob)}}}
	tool := &KnowledgeTool{installed: []Installed{{Descriptor: d, Runner: &module.Runner{Binary: filepath.Join(root, "module.exe")}}}}
	if got := tool.Execute(context.Background(), map[string]any{"module": "missing"}); !got.IsError {
		t.Fatal("missing module accepted")
	}
	// Exercise the verified loader with a real advertised file through the tool.
	if got := tool.Execute(context.Background(), map[string]any{"module": "test", "skill": "guide"}); got.IsError || !strings.Contains(got.ForLLM, string(blob)) {
		t.Fatal("verified guidance missing")
	}
	if err := os.WriteFile(path, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := tool.Execute(context.Background(), map[string]any{"module": "test"}); strings.Contains(got.ForLLM, "tampered") || !strings.Contains(got.ForLLM, "refusing") {
		t.Fatal("digest mismatch was not refused")
	}
	if err := SetDisabled(root, true); err != nil {
		t.Fatal(err)
	}
	if got := tool.Execute(context.Background(), map[string]any{"module": "test"}); !got.IsError {
		t.Fatal("disabled module accepted")
	}
}

func knowledgeToolFixture(t *testing.T, overlay string) (*KnowledgeTool, string) {
	t.Helper()
	root := t.TempDir()
	skill := []byte("small verified skill")
	for path, content := range map[string][]byte{"skill.md": skill, "overlay.md": []byte(overlay)} {
		if err := os.WriteFile(filepath.Join(root, path), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	d := &modproto.Descriptor{
		Module: "Test.Module", Version: "1",
		Skills:        []modproto.Skill{{ID: "guide", Path: "skill.md", Digest: modproto.DigestSHA256(skill)}},
		AgentOverlays: []modproto.Overlay{{ID: "overlay", Path: "overlay.md", Digest: modproto.DigestSHA256([]byte(overlay))}},
	}
	return &KnowledgeTool{installed: []Installed{{Descriptor: d, Runner: &module.Runner{Binary: filepath.Join(root, "module.exe")}}}}, root
}

func TestKnowledgeToolRejectsUnknownAndUnscopedSkill(t *testing.T) {
	tool, _ := knowledgeToolFixture(t, "valid unrelated overlay")
	ctx := context.Background()
	if got := tool.Execute(ctx, map[string]any{"module": "test.module"}); got.IsError || !strings.Contains(got.ForLLM, "valid unrelated overlay") {
		t.Fatalf("fixture overlay did not load: %+v", got)
	}
	for _, args := range []map[string]any{
		{"module": " TEST.MODULE ", "skill": "missing"},
		{"skill": "guide"},
		{"module": " \t", "skill": "guide"},
		{"module": "test.module", "skill": "guide", "list": true},
	} {
		got := tool.Execute(ctx, args)
		if !got.IsError || !strings.Contains(got.ForLLM, "skill") || strings.Contains(got.ForLLM, "valid unrelated overlay") {
			t.Fatalf("invalid skill request accepted: %v: %+v", args, got)
		}
	}
}

func TestKnowledgeToolSmallSkillBypassesHugeOverlay(t *testing.T) {
	tool, root := knowledgeToolFixture(t, strings.Repeat("huge overlay\n", 10000))
	ctx := context.Background()
	// An overlay over the 64 KiB document cap is refused on its own, with a
	// warning, and the module's small skill still loads.
	got := tool.Execute(ctx, map[string]any{"module": " TEST.MODULE "})
	if got.IsError || !strings.Contains(got.ForLLM, "larger than 64 KiB") ||
		!strings.Contains(got.ForLLM, "small verified skill") || strings.Contains(got.ForLLM, "huge overlay") {
		t.Fatalf("oversized overlay was not refused on its own: %+v", got)
	}
	got = tool.Execute(ctx, map[string]any{"module": " TEST.MODULE ", "list": true})
	if got.IsError || !strings.Contains(got.ForLLM, "skill guide") {
		t.Fatalf("module catalog recovery failed: %+v", got)
	}
	args := map[string]any{"module": " TEST.MODULE ", "skill": " guide "}
	for _, tamperOverlay := range []bool{false, true} {
		if tamperOverlay {
			if err := os.WriteFile(filepath.Join(root, "overlay.md"), []byte("tampered overlay"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		got = tool.Execute(ctx, args)
		for _, want := range []string{"small verified skill", "module Test.Module v1", "skill.md", tool.installed[0].Descriptor.Skills[0].Digest} {
			if got.IsError || !strings.Contains(got.ForLLM, want) {
				t.Fatalf("skill/provenance missing %q: %+v", want, got)
			}
		}
		if strings.Contains(got.ForLLM, "overlay") || strings.Contains(got.ForLLM, "refusing") {
			t.Fatalf("explicit skill loaded unrelated overlay: %+v", got)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "skill.md"), []byte("tampered skill"), 0600); err != nil {
		t.Fatal(err)
	}
	got = tool.Execute(ctx, args)
	if !got.IsError || !strings.Contains(got.ForLLM, "refusing") || strings.Contains(got.ForLLM, "tampered skill") {
		t.Fatalf("explicit skill bypassed verification: %+v", got)
	}
	if err := SetDisabled(root, true); err != nil {
		t.Fatal(err)
	}
	if got = tool.Execute(ctx, args); !got.IsError || strings.Contains(got.ForLLM, "refusing") {
		t.Fatalf("explicit skill did not recheck disablement: %+v", got)
	}
	if got = tool.Execute(ctx, nil); got.IsError || strings.Contains(got.ForLLM, "Test.Module") {
		t.Fatalf("disabled module leaked into catalog: %+v", got)
	}
}

func TestKnowledgeToolLargeCatalogPagedRecovery(t *testing.T) {
	tool, _ := knowledgeToolFixture(t, "overlay")
	d := tool.installed[0].Descriptor
	d.Skills = nil
	d.AgentOverlays = nil
	for i := 0; i < 240; i++ {
		d.Skills = append(d.Skills, modproto.Skill{ID: fmt.Sprintf("skill-%03d", i), Title: strings.Repeat("title ", 300), Path: "skill.md", Digest: "sha256:catalog-only"})
	}
	// Enough entries to exceed the byte cap even before the default 50-entry limit.
	offset, pages := 0, 0
	for offset < len(d.Skills) {
		args := map[string]any{}
		if pages > 0 {
			args = map[string]any{"module": " test.MODULE ", "list": true, "offset": float64(offset), "limit": float64(50)}
		}
		got := tool.Execute(context.Background(), args)
		if got.IsError || len(got.ForLLM) > 65536 {
			t.Fatalf("catalog page is not bounded/recoverable at %d: %+v", offset, got)
		}
		start := offset
		for _, line := range strings.Split(got.ForLLM, "\n") {
			if !strings.HasPrefix(line, "Test.Module v1: skill ") {
				continue
			}
			if !strings.HasPrefix(line, fmt.Sprintf("Test.Module v1: skill skill-%03d — ", offset)) {
				t.Fatalf("skipped/duplicated catalog entry at %d", offset)
			}
			offset++
		}
		if offset == start {
			t.Fatal("catalog pagination made no progress")
		}
		if offset < len(d.Skills) && !strings.Contains(got.ForLLM, fmt.Sprintf("offset=%d, limit=50", offset)) {
			t.Fatalf("missing usable next offset after %d", offset)
		}
		pages++
	}
	if pages < 2 {
		t.Fatal("fixture did not exercise pagination")
	}
	first := tool.Execute(context.Background(), map[string]any{"limit": 1})
	if first.IsError || strings.Count(first.ForLLM, "Test.Module v1: skill ") != 1 || !strings.Contains(first.ForLLM, "offset=1, limit=1") {
		t.Fatalf("entry limit was not honored: %+v", first)
	}
	got := tool.Execute(context.Background(), map[string]any{"offset": 239, "limit": 1})
	if got.IsError || !strings.Contains(got.ForLLM, "skill-239") || !strings.Contains(got.ForLLM, "End of catalog.") {
		t.Fatalf("last page failed: %+v", got)
	}
	for _, args := range []map[string]any{{"offset": -1}, {"offset": 0.5}, {"limit": 0}, {"limit": 101}, {"limit": "1"}} {
		if got = tool.Execute(context.Background(), args); !got.IsError {
			t.Fatalf("invalid pagination accepted: %v", args)
		}
	}
	d.Skills[0].Title = strings.Repeat("oversized metadata", 5000)
	got = tool.Execute(context.Background(), nil)
	if !got.IsError || len(got.ForLLM) > 65536 || !strings.Contains(got.ForLLM, "offset=1") {
		t.Fatalf("oversized single entry has no bounded skip path: %+v", got)
	}
	got = tool.Execute(context.Background(), map[string]any{"offset": 1, "limit": 1})
	if got.IsError || !strings.Contains(got.ForLLM, "skill-001") {
		t.Fatalf("could not resume after oversized entry: %+v", got)
	}
}
