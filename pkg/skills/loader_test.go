package skills

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSkillsInfoValidate(t *testing.T) {
	testcases := []struct {
		name        string
		skillName   string
		description string
		wantErr     bool
		errContains []string
	}{
		{
			name:        "valid-skill",
			skillName:   "valid-skill",
			description: "a valid skill description",
			wantErr:     false,
		},
		{
			name:        "empty-name",
			skillName:   "",
			description: "description without name",
			wantErr:     true,
			errContains: []string{"name is required"},
		},
		{
			name:        "empty-description",
			skillName:   "skill-without-description",
			description: "",
			wantErr:     true,
			errContains: []string{"description is required"},
		},
		{
			name:        "empty-both",
			skillName:   "",
			description: "",
			wantErr:     true,
			errContains: []string{"name is required", "description is required"},
		},
		{
			name:        "name-with-spaces",
			skillName:   "skill with spaces",
			description: "invalid name with spaces",
			wantErr:     true,
			errContains: []string{"name must be alphanumeric with hyphens"},
		},
		{
			name:        "name-with-underscore",
			skillName:   "skill_underscore",
			description: "invalid name with underscore",
			wantErr:     true,
			errContains: []string{"name must be alphanumeric with hyphens"},
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			info := SkillInfo{
				Name:        tc.skillName,
				Description: tc.description,
			}
			err := info.validate()
			if tc.wantErr {
				assert.Error(t, err)
				for _, msg := range tc.errContains {
					assert.ErrorContains(t, err, msg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestExtractFrontmatter(t *testing.T) {
	sl := &SkillsLoader{}

	testcases := []struct {
		name           string
		content        string
		expectedName   string
		expectedDesc   string
		lineEndingType string
	}{
		{
			name:           "unix-line-endings",
			lineEndingType: "Unix (\\n)",
			content:        "---\nname: test-skill\ndescription: A test skill\n---\n\n# Skill Content",
			expectedName:   "test-skill",
			expectedDesc:   "A test skill",
		},
		{
			name:           "windows-line-endings",
			lineEndingType: "Windows (\\r\\n)",
			content:        "---\r\nname: test-skill\r\ndescription: A test skill\r\n---\r\n\r\n# Skill Content",
			expectedName:   "test-skill",
			expectedDesc:   "A test skill",
		},
		{
			name:           "classic-mac-line-endings",
			lineEndingType: "Classic Mac (\\r)",
			content:        "---\rname: test-skill\rdescription: A test skill\r---\r\r# Skill Content",
			expectedName:   "test-skill",
			expectedDesc:   "A test skill",
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			// Extract frontmatter
			frontmatter := sl.extractFrontmatter(tc.content)
			assert.NotEmpty(t, frontmatter, "Frontmatter should be extracted for %s line endings", tc.lineEndingType)

			// Parse YAML to get name and description (parseSimpleYAML now handles all line ending types)
			yamlMeta := sl.parseSimpleYAML(frontmatter)
			assert.Equal(
				t,
				tc.expectedName,
				yamlMeta["name"],
				"Name should be correctly parsed from frontmatter with %s line endings",
				tc.lineEndingType,
			)
			assert.Equal(
				t,
				tc.expectedDesc,
				yamlMeta["description"],
				"Description should be correctly parsed from frontmatter with %s line endings",
				tc.lineEndingType,
			)
		})
	}
}

// createSkillDir creates a skill directory with a SKILL.md file containing the given frontmatter.
func createSkillDir(t *testing.T, base, dirName, name, description string) {
	t.Helper()
	dir := filepath.Join(base, dirName)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	content := "---\nname: " + name + "\ndescription: " + description + "\n---\n\n# " + name
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644))
}

func TestListSkillsWorkspaceOverridesGlobal(t *testing.T) {
	tmp := t.TempDir()
	ws := filepath.Join(tmp, "workspace")
	global := filepath.Join(tmp, "global")

	createSkillDir(t, filepath.Join(ws, "skills"), "my-skill", "my-skill", "workspace version")
	createSkillDir(t, global, "my-skill", "my-skill", "global version")

	sl := NewSkillsLoader(ws, global, "")
	skills := sl.ListSkills()

	assert.Len(t, skills, 1)
	assert.Equal(t, "workspace", skills[0].Source)
	assert.Equal(t, "workspace version", skills[0].Description)
}

func TestListSkillsGlobalOverridesBuiltin(t *testing.T) {
	tmp := t.TempDir()
	ws := filepath.Join(tmp, "workspace")
	global := filepath.Join(tmp, "global")
	builtin := filepath.Join(tmp, "builtin")

	createSkillDir(t, global, "my-skill", "my-skill", "global version")
	createSkillDir(t, builtin, "my-skill", "my-skill", "builtin version")

	sl := NewSkillsLoader(ws, global, builtin)
	skills := sl.ListSkills()

	assert.Len(t, skills, 1)
	assert.Equal(t, "global", skills[0].Source)
	assert.Equal(t, "global version", skills[0].Description)
}

func TestListSkillsFolderNameIsIdentity(t *testing.T) {
	tmp := t.TempDir()
	ws := filepath.Join(tmp, "workspace")
	builtin := filepath.Join(tmp, "builtin")

	// An installed third-party folder declaring a built-in skill's name keeps
	// its folder name: it neither hides nor replaces the built-in skill.
	createSkillDir(t, filepath.Join(ws, "skills"), "aaa", "github", "impostor")
	require.NoError(t, os.WriteFile(filepath.Join(ws, "skills", "aaa", ".skill-origin.json"),
		[]byte(`{"version":1,"origin_kind":"third_party","registry":"github"}`), 0o600))
	createSkillDir(t, builtin, "github", "github", "real github skill")
	// A user's skill whose frontmatter name differs from its folder stays
	// listed under the folder name.
	createSkillDir(t, filepath.Join(ws, "skills"), "my-notes", "Notes Helper", "my notes")

	sl := NewSkillsLoader(ws, "", builtin)
	byName := map[string]SkillInfo{}
	for _, s := range sl.ListSkills() {
		byName[s.Name] = s
	}

	require.Len(t, byName, 3)
	assert.Equal(t, "impostor", byName["aaa"].Description)
	assert.Equal(t, OriginInstalled, byName["aaa"].Origin)
	assert.Equal(t, "builtin", byName["github"].Source)
	assert.Equal(t, "real github skill", byName["github"].Description)
	assert.Equal(t, "my notes", byName["my-notes"].Description)

	content, ok := sl.LoadSkill("github")
	require.True(t, ok)
	assert.Contains(t, content, "# github")
	assert.NotContains(t, sl.BuildSkillsSummary(), "<name>Notes Helper</name>")
}

func TestListSkillsSameNameInLowerPriorityRootIsSkipped(t *testing.T) {
	tmp := t.TempDir()
	ws := filepath.Join(tmp, "workspace")
	global := filepath.Join(tmp, "global")

	createSkillDir(t, filepath.Join(ws, "skills"), "shared-name", "shared-name", "workspace version")
	createSkillDir(t, global, "Shared-Name", "Shared-Name", "global version")

	sl := NewSkillsLoader(ws, global, "")
	skills := sl.ListSkills()

	assert.Len(t, skills, 1)
	assert.Equal(t, "shared-name", skills[0].Name)
	assert.Equal(t, "workspace", skills[0].Source)
}

func TestListSkillsReadsGatingMetadata(t *testing.T) {
	tmp := t.TempDir()
	ws := filepath.Join(tmp, "workspace")
	skillsDir := filepath.Join(ws, "skills")

	otherOS := "plan9"
	if runtime.GOOS == "plan9" {
		otherOS = "linux"
	}
	write := func(name, metadata string) {
		dir := filepath.Join(skillsDir, name)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		content := "---\nname: " + name + "\ndescription: d\nmetadata: " + metadata + "\n---\n# " + name
		require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644))
	}
	write("other-os", `{"compa":{"os":["`+otherOS+`"]}}`)
	write("missing-bin", `{"compa":{"requires":{"bins":["definitely-not-installed-bin"]}}}`)
	write("present-bin", `{"openclaw":{"requires":{"bins":["present-bin"]}}}`)
	write("this-os", `{"compa":{"os":["`+runtime.GOOS+`"]}}`)

	previous := lookPath
	lookPath = func(file string) (string, error) {
		if file == "present-bin" {
			return "/usr/bin/present-bin", nil
		}
		return "", errors.New("not found")
	}
	t.Cleanup(func() { lookPath = previous })

	sl := NewSkillsLoader(ws, "", "")
	byName := map[string]SkillInfo{}
	for _, s := range sl.ListSkills() {
		byName[s.Name] = s
	}
	require.Len(t, byName, 4, "gated skills stay listed")
	assert.Contains(t, byName["other-os"].Unavailable, otherOS)
	assert.Contains(t, byName["missing-bin"].Unavailable, "definitely-not-installed-bin")
	assert.Empty(t, byName["present-bin"].Unavailable)
	assert.Empty(t, byName["this-os"].Unavailable)

	summary := sl.BuildSkillsSummary()
	assert.NotContains(t, summary, "<name>other-os</name>")
	assert.NotContains(t, summary, "<name>missing-bin</name>")
	assert.Contains(t, summary, "<name>present-bin</name>")
	assert.Contains(t, summary, "<name>this-os</name>")
}

func TestInstalledSkillsShowDistinctSource(t *testing.T) {
	tmp := t.TempDir()
	ws := filepath.Join(tmp, "workspace")
	skillsDir := filepath.Join(ws, "skills")
	createSkillDir(t, skillsDir, "mine", "mine", "written by the user")
	createSkillDir(t, skillsDir, "third", "third", "from a registry")
	require.NoError(t, os.WriteFile(filepath.Join(skillsDir, "third", ".skill-origin.json"),
		[]byte(`{"version":1,"origin_kind":"third_party","registry":"clawhub"}`), 0o600))

	sl := NewSkillsLoader(ws, "", "")
	byName := map[string]SkillInfo{}
	for _, s := range sl.ListSkills() {
		byName[s.Name] = s
	}
	assert.Equal(t, "workspace", byName["mine"].Source)
	assert.Empty(t, byName["mine"].Origin)
	assert.Equal(t, "workspace", byName["third"].Source, "Source keeps naming the root")
	assert.Equal(t, OriginInstalled, byName["third"].Origin)

	summary := sl.BuildSkillsSummary()
	assert.Contains(t, summary, "<name>third</name>\n    <description>from a registry</description>")
	assert.Regexp(t, `<name>third</name>[\s\S]*?<source>installed</source>`, summary)
	assert.Regexp(t, `<name>mine</name>[\s\S]*?<source>workspace</source>`, summary)
}

func TestListSkillsMultipleDistinctSkills(t *testing.T) {
	tmp := t.TempDir()
	ws := filepath.Join(tmp, "workspace")
	global := filepath.Join(tmp, "global")
	builtin := filepath.Join(tmp, "builtin")

	createSkillDir(t, filepath.Join(ws, "skills"), "skill-a", "skill-a", "desc a")
	createSkillDir(t, global, "skill-b", "skill-b", "desc b")
	createSkillDir(t, builtin, "skill-c", "skill-c", "desc c")

	sl := NewSkillsLoader(ws, global, builtin)
	skills := sl.ListSkills()

	assert.Len(t, skills, 3)
	names := map[string]string{}
	for _, s := range skills {
		names[s.Name] = s.Source
	}
	assert.Equal(t, "workspace", names["skill-a"])
	assert.Equal(t, "global", names["skill-b"])
	assert.Equal(t, "builtin", names["skill-c"])
}

func TestListSkillsInvalidSkillSkipped(t *testing.T) {
	tmp := t.TempDir()
	ws := filepath.Join(tmp, "workspace")
	global := filepath.Join(tmp, "global")

	// Invalid name (underscore)
	createSkillDir(t, filepath.Join(ws, "skills"), "bad_skill", "bad_skill", "desc")
	// Valid skill
	createSkillDir(t, global, "good-skill", "good-skill", "desc")

	sl := NewSkillsLoader(ws, global, "")
	skills := sl.ListSkills()

	assert.Len(t, skills, 1)
	assert.Equal(t, "good-skill", skills[0].Name)
}

func TestListSkillsEmptyAndNonexistentDirs(t *testing.T) {
	tmp := t.TempDir()
	ws := filepath.Join(tmp, "workspace")
	emptyDir := filepath.Join(tmp, "empty")
	require.NoError(t, os.MkdirAll(emptyDir, 0o755))

	sl := NewSkillsLoader(ws, emptyDir, filepath.Join(tmp, "nonexistent"))
	skills := sl.ListSkills()

	assert.Empty(t, skills)
}

func TestListSkillsDirWithoutSkillMD(t *testing.T) {
	tmp := t.TempDir()
	ws := filepath.Join(tmp, "workspace")
	global := filepath.Join(tmp, "global")

	// Directory exists but has no SKILL.md
	require.NoError(t, os.MkdirAll(filepath.Join(global, "no-skillmd"), 0o755))
	// Valid skill alongside
	createSkillDir(t, global, "real-skill", "real-skill", "desc")

	sl := NewSkillsLoader(ws, global, "")
	skills := sl.ListSkills()

	assert.Len(t, skills, 1)
	assert.Equal(t, "real-skill", skills[0].Name)
}

func TestStripFrontmatter(t *testing.T) {
	sl := &SkillsLoader{}

	testcases := []struct {
		name            string
		content         string
		expectedContent string
		lineEndingType  string
	}{
		{
			name:            "unix-line-endings",
			lineEndingType:  "Unix (\\n)",
			content:         "---\nname: test-skill\ndescription: A test skill\n---\n\n# Skill Content",
			expectedContent: "# Skill Content",
		},
		{
			name:            "windows-line-endings",
			lineEndingType:  "Windows (\\r\\n)",
			content:         "---\r\nname: test-skill\r\ndescription: A test skill\r\n---\r\n\r\n# Skill Content",
			expectedContent: "# Skill Content",
		},
		{
			name:            "classic-mac-line-endings",
			lineEndingType:  "Classic Mac (\\r)",
			content:         "---\rname: test-skill\rdescription: A test skill\r---\r\r# Skill Content",
			expectedContent: "# Skill Content",
		},
		{
			name:            "unix-line-endings-without-trailing-newline",
			lineEndingType:  "Unix (\\n) without trailing newline",
			content:         "---\nname: test-skill\ndescription: A test skill\n---\n# Skill Content",
			expectedContent: "# Skill Content",
		},
		{
			name:            "windows-line-endings-without-trailing-newline",
			lineEndingType:  "Windows (\\r\\n) without trailing newline",
			content:         "---\r\nname: test-skill\r\ndescription: A test skill\r\n---\r\n# Skill Content",
			expectedContent: "# Skill Content",
		},
		{
			name:            "no-frontmatter",
			lineEndingType:  "No frontmatter",
			content:         "# Skill Content\n\nSome content here.",
			expectedContent: "# Skill Content\n\nSome content here.",
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			result := sl.stripFrontmatter(tc.content)
			assert.Equal(
				t,
				tc.expectedContent,
				result,
				"Frontmatter should be stripped correctly for %s",
				tc.lineEndingType,
			)
		})
	}
}

func TestSkillRootsTrimsWhitespaceAndDedups(t *testing.T) {
	tmp := t.TempDir()
	workspace := filepath.Join(tmp, "workspace")
	global := filepath.Join(tmp, "global")
	builtin := filepath.Join(tmp, "builtin")

	sl := NewSkillsLoader(workspace, "  "+global+"  ", "\t"+builtin+"\n")
	roots := sl.SkillRoots()

	assert.Equal(t, []string{
		filepath.Join(workspace, "skills"),
		global,
		builtin,
	}, roots)
}

func TestGetSkillMetadata_UsesMarkdownParagraphWhenNoFrontmatter(t *testing.T) {
	tmp := t.TempDir()
	skillDir := filepath.Join(tmp, "workspace", "skills", "plain-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))

	content := "# Plain Skill\n\nThis is parsed from markdown paragraph.\n"
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644))

	sl := &SkillsLoader{}
	meta := sl.getSkillMetadata(filepath.Join(skillDir, "SKILL.md"))
	require.NotNil(t, meta)
	assert.Equal(t, "plain-skill", meta.Name)
	assert.Equal(t, "This is parsed from markdown paragraph.", meta.Description)
}

func TestGetSkillMetadata_FrontmatterOverridesMarkdown(t *testing.T) {
	tmp := t.TempDir()
	skillDir := filepath.Join(tmp, "workspace", "skills", "plain-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))

	content := "---\nname: frontmatter-skill\ndescription: frontmatter description\n---\n\n# Plain Skill\n\nBody description.\n"
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644))

	sl := &SkillsLoader{}
	meta := sl.getSkillMetadata(filepath.Join(skillDir, "SKILL.md"))
	require.NotNil(t, meta)
	assert.Equal(t, "frontmatter-skill", meta.Name)
	assert.Equal(t, "frontmatter description", meta.Description)
}

func TestGetSkillMetadata_YAMLMultilineDescription(t *testing.T) {
	tmp := t.TempDir()
	skillDir := filepath.Join(tmp, "workspace", "skills", "plain-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))

	content := "---\nname: frontmatter-skill\ndescription: |\n  line 1: with colon\n  line 2\n---\n\n# Plain Skill\n\nBody description.\n"
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644))

	sl := &SkillsLoader{}
	meta := sl.getSkillMetadata(filepath.Join(skillDir, "SKILL.md"))
	require.NotNil(t, meta)
	assert.Equal(t, "frontmatter-skill", meta.Name)
	assert.Equal(t, "line 1: with colon\nline 2", meta.Description)
}

func TestGetSkillMetadata_InvalidHeadingNameFallsBackToDirName(t *testing.T) {
	tmp := t.TempDir()
	skillDir := filepath.Join(tmp, "workspace", "skills", "valid-name")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))

	content := "# Invalid Heading Name\n\nBody description.\n"
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644))

	sl := &SkillsLoader{}
	meta := sl.getSkillMetadata(filepath.Join(skillDir, "SKILL.md"))
	require.NotNil(t, meta)
	assert.Equal(t, "valid-name", meta.Name)
	assert.Equal(t, "Body description.", meta.Description)
}

func TestGetSkillMetadata_IgnoresHTMLCommentBlocks(t *testing.T) {
	tmp := t.TempDir()
	skillDir := filepath.Join(tmp, "workspace", "skills", "biomed-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))

	content := "<!--\n# COPYRIGHT NOTICE\n# This file is part of the \"Universal Biomedical Skills\" project.\n# Copyright (c) 2026 MD BABU MIA, PhD <md.babu.mia@mssm.edu>\n# All Rights Reserved.\n#\n# This code is proprietary and confidential.\n# Unauthorized copying of this file, via any medium is strictly prohibited.\n#\n# Provenance: Authenticated by MD BABU MIA\n\n-->\n\n# Biomed Skill\n\nSummarize biomedical papers.\n"
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644))

	sl := &SkillsLoader{}
	meta := sl.getSkillMetadata(filepath.Join(skillDir, "SKILL.md"))
	require.NotNil(t, meta)
	assert.Equal(t, "biomed-skill", meta.Name)
	assert.Equal(t, "Summarize biomedical papers.", meta.Description)
}
