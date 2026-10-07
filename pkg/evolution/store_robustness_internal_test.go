package evolution

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// A partial line from a crash neither glues onto the next append nor makes
// the store unreadable; the bad line is kept in <file>.corrupt (EV-23).
func TestStoreSurvivesPartialLineFromCrash(t *testing.T) {
	root := t.TempDir()
	paths := NewPaths(root, "")
	store := NewStore(paths)
	rec := func(id string) LearningRecord {
		return LearningRecord{ID: id, Kind: RecordKindTask, WorkspaceID: root, CreatedAt: time.Unix(1700000000, 0).UTC()}
	}
	if err := store.AppendTaskRecord(t.Context(), rec("a")); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash in the middle of writing the next record.
	f, err := os.OpenFile(paths.TaskRecords, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"id":"partial","kind":"ta`); err != nil {
		t.Fatal(err)
	}
	f.Close()

	if err := store.AppendTaskRecord(t.Context(), rec("b")); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendTaskRecord(t.Context(), rec("c")); err != nil {
		t.Fatal(err)
	}

	records, err := store.LoadTaskRecords()
	if err != nil {
		t.Fatalf("LoadTaskRecords: %v", err)
	}
	var ids []string
	for _, r := range records {
		ids = append(ids, r.ID)
	}
	if strings.Join(ids, ",") != "a,b,c" {
		t.Fatalf("ids = %v, want a,b,c", ids)
	}
	quarantined, err := os.ReadFile(paths.TaskRecords + ".corrupt")
	if err != nil || !strings.Contains(string(quarantined), `"id":"partial"`) {
		t.Fatalf("quarantine = %q, %v", quarantined, err)
	}
	// Loading again doesn't duplicate quarantined lines.
	if _, err := store.LoadTaskRecords(); err != nil {
		t.Fatal(err)
	}
	again, _ := os.ReadFile(paths.TaskRecords + ".corrupt")
	if strings.Count(string(again), "partial") != 1 {
		t.Fatalf("quarantine duplicated lines:\n%s", again)
	}
}

// Retention decays with idle time, so an evolved skill that was used a lot
// once still goes cold when it sits unused (EV-22).
func TestLifecycleRetentionDecays(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	profile := SkillProfile{
		SkillName: "x", Origin: "evolved", Status: SkillStatusActive,
		RetentionScore: 1, LastUsedAt: now.Add(-100 * 24 * time.Hour),
	}
	if got := NextLifecycleState(profile, now); got != SkillStatusCold {
		t.Fatalf("idle 100 days with score 1: %q, want cold", got)
	}
	profile.LastUsedAt = now.Add(-10 * 24 * time.Hour)
	if got := NextLifecycleState(profile, now); got != SkillStatusActive {
		t.Fatalf("recently used: %q, want active", got)
	}
	profile.Origin = "manual"
	profile.LastUsedAt = now.Add(-400 * 24 * time.Hour)
	if got := NextLifecycleState(profile, now); got != SkillStatusActive {
		t.Fatalf("manual skill changed state: %q", got)
	}
}

// Descriptions that need YAML quoting stay valid (EV-24).
func TestNormalizeDeployableDescriptionKeepsValidYAML(t *testing.T) {
	for _, line := range []string{
		`description: "Convert documents: PDF to text"`,
		`description: 'It''s a "quoted" helper'`,
		`description: plain helper`,
		`description: "#hashtag helper"`,
	} {
		body := "---\nname: docs\n" + line + "\n---\n# Docs\n"
		got := normalizeDeployableDescription(body)
		frontmatter, _ := splitSkillFrontmatter(got)
		var parsed struct {
			Description string `yaml:"description"`
		}
		if err := yaml.Unmarshal([]byte(frontmatter), &parsed); err != nil {
			t.Fatalf("%s: invalid YAML after normalize: %v\n%s", line, err, got)
		}
		var want struct {
			Description string `yaml:"description"`
		}
		if err := yaml.Unmarshal([]byte(line), &want); err != nil {
			t.Fatal(err)
		}
		if parsed.Description != want.Description {
			t.Fatalf("%s: description = %q, want %q", line, parsed.Description, want.Description)
		}
	}
}

// Excerpts are cut on rune boundaries (EV-24).
func TestReadSkillBodyExcerptIsRuneSafe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "SKILL.md")
	body := "---\nname: x\ndescription: y\n---\n" + strings.Repeat("技能", maxMatchedSkillExcerptChars)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got := readSkillBodyExcerpt(path)
	if !utf8.ValidString(got) {
		t.Fatalf("excerpt is not valid UTF-8")
	}
	if n := utf8.RuneCountInString(strings.TrimSuffix(got, "...")); n != maxMatchedSkillExcerptChars {
		t.Fatalf("excerpt runes = %d, want %d", n, maxMatchedSkillExcerptChars)
	}
}
