package skills

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"

	"github.com/gomarkdown/markdown"
	"github.com/gomarkdown/markdown/ast"
	"github.com/gomarkdown/markdown/parser"
	"gopkg.in/yaml.v3"

	"github.com/xibodev/compa/v4/pkg/logger"
)

var namePattern = regexp.MustCompile(`^[a-zA-Z0-9]+(-[a-zA-Z0-9]+)*$`)

const (
	MaxNameLength        = 64
	MaxDescriptionLength = 1024

	// OriginInstalled marks a skill installed from a registry: third-party
	// instructions rather than ones the user wrote.
	OriginInstalled = "installed"

	// originMetaFile is written next to SKILL.md by the installers.
	originMetaFile = ".skill-origin.json"
)

type SkillMetadata struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// gates holds the platform requirements the skill declares.
	gates skillGates
}

type SkillInfo struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Source      string `json:"source"`
	Description string `json:"description"`
	// Origin is OriginInstalled for a skill installed from a registry.
	Origin string `json:"origin,omitempty"`
	// Unavailable says why the skill doesn't apply on this host (an
	// unsupported OS or a missing binary). Such skills aren't offered to the
	// agent.
	Unavailable string `json:"unavailable,omitempty"`
}

// skillGates is the gating part of a skill's frontmatter metadata, for
// example `metadata: {"compa":{"os":["linux"],"requires":{"bins":["tmux"]}}}`.
type skillGates struct {
	OS       []string `json:"os"`
	Requires struct {
		Bins    []string `json:"bins"`
		AnyBins []string `json:"anyBins"`
	} `json:"requires"`
}

// gateNamespaces are the metadata keys gating is read from: Compa's own and
// the ones skills written for OpenClaw-style registries use.
var gateNamespaces = []string{"compa", "openclaw", "clawdbot"}

// lookPath finds a required binary; a variable so tests can stub it.
var lookPath = exec.LookPath

// unmet returns why the skill doesn't apply on this host, or "".
func (g skillGates) unmet() string {
	if len(g.OS) > 0 && !osMatches(g.OS, runtime.GOOS) {
		return "requires " + strings.Join(g.OS, " or ")
	}
	for _, bin := range g.Requires.Bins {
		if strings.TrimSpace(bin) == "" {
			continue
		}
		if _, err := lookPath(bin); err != nil {
			return "requires " + bin
		}
	}
	if len(g.Requires.AnyBins) > 0 {
		for _, bin := range g.Requires.AnyBins {
			if _, err := lookPath(bin); err == nil {
				return ""
			}
		}
		return "requires one of " + strings.Join(g.Requires.AnyBins, ", ")
	}
	return ""
}

func osMatches(allowed []string, goos string) bool {
	for _, name := range allowed {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case goos:
			return true
		case "macos", "mac", "osx":
			if goos == "darwin" {
				return true
			}
		case "win", "win32":
			if goos == "windows" {
				return true
			}
		case "linux":
			// Android runs a Linux kernel and userland tools (Termux).
			if goos == "android" {
				return true
			}
		}
	}
	return false
}

// parseSkillGates reads gating from the frontmatter `metadata` value, which
// may be a mapping or a JSON string.
func parseSkillGates(metadata any) skillGates {
	if text, ok := metadata.(string); ok {
		var decoded any
		if err := json.Unmarshal([]byte(text), &decoded); err != nil {
			return skillGates{}
		}
		metadata = decoded
	}
	fields, ok := metadata.(map[string]any)
	if !ok {
		return skillGates{}
	}
	for _, ns := range gateNamespaces {
		section, ok := fields[ns]
		if !ok {
			continue
		}
		data, err := json.Marshal(section)
		if err != nil {
			return skillGates{}
		}
		var gates skillGates
		if err := json.Unmarshal(data, &gates); err != nil {
			return skillGates{}
		}
		return gates
	}
	return skillGates{}
}

// isInstalledSkillDir reports whether the installers marked the skill as a
// third-party install.
func isInstalledSkillDir(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, originMetaFile))
	if err != nil {
		return false
	}
	var meta struct {
		OriginKind string `json:"origin_kind"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return false
	}
	return meta.OriginKind == "third_party"
}

var warnedOnce sync.Map

// warnOnce logs a skill problem at warning level once per process, so a
// misconfigured skill is reported without repeating on every prompt build.
func warnOnce(key, message string, fields map[string]any) {
	if _, loaded := warnedOnce.LoadOrStore(key, struct{}{}); loaded {
		return
	}
	logger.WarnCF("skills", message, fields)
}

func (info SkillInfo) validate() error {
	var errs error
	if info.Name == "" {
		errs = errors.Join(errs, errors.New("name is required"))
	} else {
		if err := ValidateSkillName(info.Name); err != nil {
			errs = errors.Join(errs, err)
		}
	}

	if info.Description == "" {
		errs = errors.Join(errs, errors.New("description is required"))
	} else if len(info.Description) > MaxDescriptionLength {
		errs = errors.Join(errs, fmt.Errorf("description exceeds %d character", MaxDescriptionLength))
	}
	return errs
}

type SkillsLoader struct {
	workspace       string
	workspaceSkills string // workspace skills (project-level)
	globalSkills    string // global skills (~/.compa/skills)
	builtinSkills   string // builtin skills
}

// SkillRoots returns all unique skill root directories used by this loader.
// The order follows resolution priority: workspace > global > builtin.
func (sl *SkillsLoader) SkillRoots() []string {
	roots := []string{sl.workspaceSkills, sl.globalSkills, sl.builtinSkills}
	seen := make(map[string]struct{}, len(roots))
	out := make([]string, 0, len(roots))

	for _, root := range roots {
		trimmed := strings.TrimSpace(root)
		if trimmed == "" {
			continue
		}
		clean := filepath.Clean(trimmed)
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}

	return out
}

func NewSkillsLoader(workspace string, globalSkills string, builtinSkills string) *SkillsLoader {
	return &SkillsLoader{
		workspace:       workspace,
		workspaceSkills: filepath.Join(workspace, "skills"),
		globalSkills:    globalSkills, // ~/.compa/skills
		builtinSkills:   builtinSkills,
	}
}

// ListSkills lists the skills of every root, workspace first. A skill's
// identity is its folder name, which LoadSkill resolves too: a frontmatter
// name that differs is only logged, so a folder can't hide or take over
// another skill by declaring its name. When folder names collide (across
// roots, or by case) the first one wins in root order (workspace > global >
// builtin) and then folder order, and the others are skipped with a warning.
func (sl *SkillsLoader) ListSkills() []SkillInfo {
	skills := make([]SkillInfo, 0)
	seen := make(map[string]string)

	addSkills := func(dir, source string) {
		if dir == "" {
			return
		}
		dirs, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, d := range dirs {
			if !d.IsDir() {
				continue
			}
			skillFile := filepath.Join(dir, d.Name(), "SKILL.md")
			if _, err := os.Stat(skillFile); err != nil {
				continue
			}
			info := SkillInfo{
				Name:   d.Name(),
				Path:   skillFile,
				Source: source,
			}
			metadata := sl.getSkillMetadata(skillFile)
			if metadata != nil {
				if metadata.Name != d.Name() {
					logger.DebugCF("skills", "Skill frontmatter name differs from its folder name; using the folder name",
						map[string]any{"path": skillFile, "folder": d.Name(), "declared_name": metadata.Name})
				}
				info.Description = metadata.Description
				info.Unavailable = metadata.gates.unmet()
			}
			if isInstalledSkillDir(filepath.Dir(skillFile)) {
				info.Origin = OriginInstalled
			}
			if err := info.validate(); err != nil {
				slog.Warn("invalid skill from "+source, "name", info.Name, "error", err)
				continue
			}
			// Case-insensitive, so the list is the same on every file system.
			key := strings.ToLower(info.Name)
			if first, ok := seen[key]; ok {
				warnOnce("dup:"+skillFile, "Skill skipped: a skill with the same name comes first",
					map[string]any{"name": info.Name, "path": skillFile, "used": first})
				continue
			}
			seen[key] = skillFile
			skills = append(skills, info)
		}
	}

	// Priority: workspace > global > builtin
	addSkills(sl.workspaceSkills, "workspace")
	addSkills(sl.globalSkills, "global")
	addSkills(sl.builtinSkills, "builtin")

	return skills
}

func (sl *SkillsLoader) LoadSkill(name string) (string, bool) {
	if err := ValidateSkillName(name); err != nil {
		return "", false
	}

	// 1. load from workspace skills first (project-level)
	if sl.workspaceSkills != "" {
		skillFile := filepath.Join(sl.workspaceSkills, name, "SKILL.md")
		if content, err := os.ReadFile(skillFile); err == nil {
			return sl.stripFrontmatter(string(content)), true
		}
	}

	// 2. then load from global skills (~/.compa/skills)
	if sl.globalSkills != "" {
		skillFile := filepath.Join(sl.globalSkills, name, "SKILL.md")
		if content, err := os.ReadFile(skillFile); err == nil {
			return sl.stripFrontmatter(string(content)), true
		}
	}

	// 3. finally load from builtin skills
	if sl.builtinSkills != "" {
		skillFile := filepath.Join(sl.builtinSkills, name, "SKILL.md")
		if content, err := os.ReadFile(skillFile); err == nil {
			return sl.stripFrontmatter(string(content)), true
		}
	}

	return "", false
}

func (sl *SkillsLoader) LoadSkillsForContext(skillNames []string) string {
	if len(skillNames) == 0 {
		return ""
	}

	var parts []string
	for _, name := range skillNames {
		content, ok := sl.LoadSkill(name)
		if ok {
			parts = append(parts, fmt.Sprintf("### Skill: %s\n\n%s", name, content))
		}
	}

	return strings.Join(parts, "\n\n---\n\n")
}

// BuildSkillsSummary lists the skills offered to the agent. Skills that don't
// apply on this host are left out, and installed third-party skills show the
// source "installed".
func (sl *SkillsLoader) BuildSkillsSummary() string {
	allSkills := sl.ListSkills()

	var lines []string
	for _, s := range allSkills {
		if s.Unavailable != "" {
			continue
		}
		escapedName := escapeXML(s.Name)
		escapedDesc := escapeXML(s.Description)
		escapedPath := escapeXML(s.Path)
		source := s.Source
		if s.Origin == OriginInstalled {
			source = OriginInstalled
		}

		lines = append(lines, "  <skill>")
		lines = append(lines, fmt.Sprintf("    <name>%s</name>", escapedName))
		lines = append(lines, fmt.Sprintf("    <description>%s</description>", escapedDesc))
		lines = append(lines, fmt.Sprintf("    <location>%s</location>", escapedPath))
		lines = append(lines, fmt.Sprintf("    <source>%s</source>", source))
		lines = append(lines, "  </skill>")
	}
	if len(lines) == 0 {
		return ""
	}
	lines = append([]string{"<skills>"}, lines...)
	lines = append(lines, "</skills>")

	return strings.Join(lines, "\n")
}

func (sl *SkillsLoader) getSkillMetadata(skillPath string) *SkillMetadata {
	content, err := os.ReadFile(skillPath)
	if err != nil {
		logger.WarnCF("skills", "Failed to read skill metadata",
			map[string]any{
				"skill_path": skillPath,
				"error":      err.Error(),
			})
		return nil
	}

	frontmatter, bodyContent := splitFrontmatter(string(content))
	dirName := filepath.Base(filepath.Dir(skillPath))
	_, bodyDescription := extractMarkdownMetadata(bodyContent)

	// The folder is the skill's identity; the H1 title is only display text.
	metadata := &SkillMetadata{
		Name:        dirName,
		Description: bodyDescription,
	}

	if frontmatter == "" {
		return metadata
	}

	// Frontmatter may be JSON or simple YAML.
	var jsonMeta struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Metadata    any    `json:"metadata"`
	}
	if err := json.Unmarshal([]byte(frontmatter), &jsonMeta); err == nil {
		if jsonMeta.Name != "" {
			metadata.Name = jsonMeta.Name
		}
		if jsonMeta.Description != "" {
			metadata.Description = jsonMeta.Description
		}
		metadata.gates = parseSkillGates(jsonMeta.Metadata)
		return metadata
	}

	// Fall back to simple YAML parsing
	yamlMeta := sl.parseSimpleYAML(frontmatter)
	if name := yamlMeta["name"]; name != "" {
		metadata.Name = name
	}
	if description := yamlMeta["description"]; description != "" {
		metadata.Description = description
	}
	var yamlGates struct {
		Metadata any `yaml:"metadata"`
	}
	if err := yaml.Unmarshal([]byte(frontmatter), &yamlGates); err == nil {
		metadata.gates = parseSkillGates(yamlGates.Metadata)
	}
	return metadata
}

func extractMarkdownMetadata(content string) (title, description string) {
	p := parser.NewWithExtensions(parser.CommonExtensions)
	doc := markdown.Parse([]byte(content), p)
	if doc == nil {
		return "", ""
	}

	ast.WalkFunc(doc, func(node ast.Node, entering bool) ast.WalkStatus {
		if !entering {
			return ast.GoToNext
		}

		switch n := node.(type) {
		case *ast.Heading:
			if title == "" && n.Level == 1 {
				title = nodeText(n)
				if title != "" && description != "" {
					return ast.Terminate
				}
			}
		case *ast.Paragraph:
			if description == "" {
				description = nodeText(n)
				if title != "" && description != "" {
					return ast.Terminate
				}
			}
		}
		return ast.GoToNext
	})

	return title, description
}

func nodeText(n ast.Node) string {
	var b strings.Builder
	ast.WalkFunc(n, func(node ast.Node, entering bool) ast.WalkStatus {
		if !entering {
			return ast.GoToNext
		}

		switch t := node.(type) {
		case *ast.Text:
			b.Write(t.Literal)
		case *ast.Code:
			b.Write(t.Literal)
		case *ast.Softbreak, *ast.Hardbreak, *ast.NonBlockingSpace:
			b.WriteByte(' ')
		}
		return ast.GoToNext
	})
	return strings.Join(strings.Fields(b.String()), " ")
}

// parseSimpleYAML parses YAML frontmatter and extracts known metadata fields.
func (sl *SkillsLoader) parseSimpleYAML(content string) map[string]string {
	result := make(map[string]string)

	var meta struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal([]byte(content), &meta); err != nil {
		return result
	}
	if meta.Name != "" {
		result["name"] = meta.Name
	}
	if meta.Description != "" {
		result["description"] = meta.Description
	}

	return result
}

func (sl *SkillsLoader) extractFrontmatter(content string) string {
	frontmatter, _ := splitFrontmatter(content)
	return frontmatter
}

func (sl *SkillsLoader) stripFrontmatter(content string) string {
	_, body := splitFrontmatter(content)
	return body
}

func splitFrontmatter(content string) (frontmatter, body string) {
	normalized := string(parser.NormalizeNewlines([]byte(content)))
	lines := strings.Split(normalized, "\n")
	if len(lines) == 0 || lines[0] != "---" {
		return "", content
	}

	end := -1
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			end = i
			break
		}
	}
	if end == -1 {
		return "", content
	}

	frontmatter = strings.Join(lines[1:end], "\n")
	body = strings.Join(lines[end+1:], "\n")
	body = strings.TrimLeft(body, "\n")
	return frontmatter, body
}

func escapeXML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}
