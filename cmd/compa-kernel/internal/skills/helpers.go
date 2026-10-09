package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/xibodev/compa/v4"
	"github.com/xibodev/compa/v4/cmd/compa-kernel/internal"
	"github.com/xibodev/compa/v4/pkg/config"
	"github.com/xibodev/compa/v4/pkg/fileutil"
	"github.com/xibodev/compa/v4/pkg/skills"
	"github.com/xibodev/compa/v4/pkg/utils"
)

const skillsSearchMaxResults = 20

type installedSkillOriginMeta struct {
	Version          int    `json:"version"`
	OriginKind       string `json:"origin_kind,omitempty"`
	Registry         string `json:"registry,omitempty"`
	Slug             string `json:"slug,omitempty"`
	RegistryURL      string `json:"registry_url,omitempty"`
	InstalledVersion string `json:"installed_version,omitempty"`
	// Commit is the commit a GitHub install was pinned to; Unpinned marks a
	// GitHub install made from the ref because it couldn't be resolved.
	Commit      string `json:"commit,omitempty"`
	Unpinned    bool   `json:"unpinned,omitempty"`
	InstalledAt int64  `json:"installed_at"`
}

func skillsListCmd(loader *skills.SkillsLoader) {
	allSkills := loader.ListSkills()

	if len(allSkills) == 0 {
		fmt.Println("No skills installed.")
		return
	}

	fmt.Println("\nInstalled Skills:")
	fmt.Println("------------------")
	for _, skill := range allSkills {
		fmt.Printf("  ✓ %s (%s)\n", skill.Name, skill.Source)
		if skill.Description != "" {
			fmt.Printf("    %s\n", skill.Description)
		}
	}
}

// skillsInstallFromRegistry installs a skill from a named registry (e.g. clawhub).
func skillsInstallFromRegistry(cfg *config.Config, registryName, target string) error {
	err := utils.ValidateSkillIdentifier(registryName)
	if err != nil {
		return fmt.Errorf("✗  invalid registry name: %w", err)
	}

	registryMgr := skills.NewRegistryManagerFromToolsConfig(cfg.Tools.Skills)

	registry := registryMgr.GetRegistry(registryName)
	if registry == nil {
		return fmt.Errorf("✗  registry '%s' not found or not enabled. check your config.json.", registryName)
	}

	dirName, err := registry.ResolveInstallDirName(target)
	if err != nil {
		return fmt.Errorf("✗  invalid install target %q: %w", target, err)
	}

	fmt.Printf("Installing skill '%s' from %s registry...\n", target, registryName)

	workspace := cfg.WorkspacePath()
	targetDir := filepath.Join(workspace, "skills", dirName)

	if _, err = os.Stat(targetDir); err == nil {
		return fmt.Errorf("\u2717 skill '%s' already installed at %s", dirName, targetDir)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err = os.MkdirAll(filepath.Join(workspace, "skills"), 0o755); err != nil {
		return fmt.Errorf("\u2717 failed to create skills directory: %w", err)
	}

	result, err := registry.DownloadAndInstall(ctx, target, "", targetDir)
	if err != nil {
		rmErr := os.RemoveAll(targetDir)
		if rmErr != nil {
			fmt.Printf("\u2717 Failed to remove partial install: %v\n", rmErr)
		}
		return fmt.Errorf("✗ failed to install skill: %w", err)
	}

	if result.IsMalwareBlocked {
		rmErr := os.RemoveAll(targetDir)
		if rmErr != nil {
			fmt.Printf("\u2717 Failed to remove partial install: %v\n", rmErr)
		}

		return fmt.Errorf("\u2717 Skill '%s' is flagged as malicious and cannot be installed.\n", target)
	}

	if result.IsSuspicious {
		fmt.Printf("\u26a0\ufe0f  Warning: skill '%s' is flagged as suspicious.\n", target)
	}

	if !workspaceHasValidSkillDirectory(workspace, dirName) {
		_ = os.RemoveAll(targetDir)
		return fmt.Errorf("✗ failed to install skill: registry archive for %q is not a valid skill", target)
	}

	normalizedSlug, registryURL := skills.BuildInstallMetadataForRegistryInstance(registry, target, result.Version)
	installedAt := time.Now().UnixMilli()
	if err := writeInstalledSkillOriginMeta(targetDir, installedSkillOriginMeta{
		Version:          1,
		OriginKind:       "third_party",
		Registry:         registry.Name(),
		Slug:             normalizedSlug,
		RegistryURL:      registryURL,
		InstalledVersion: result.Version,
		Commit:           result.Commit,
		Unpinned:         result.Unpinned,
		InstalledAt:      installedAt,
	}); err != nil {
		_ = os.RemoveAll(targetDir)
		return fmt.Errorf("✗ failed to persist skill metadata: %w", err)
	}

	fmt.Printf("\u2713 Skill '%s' v%s installed successfully!\n", dirName, result.Version)
	if result.Summary != "" {
		fmt.Printf("  %s\n", result.Summary)
	}

	return nil
}

func writeInstalledSkillOriginMeta(targetDir string, meta installedSkillOriginMeta) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return fileutil.WriteFileAtomic(filepath.Join(targetDir, ".skill-origin.json"), data, 0o600)
}

func workspaceHasValidSkillDirectory(workspace, directory string) bool {
	loader := skills.NewSkillsLoader(workspace, "", "")
	for _, skill := range loader.ListSkills() {
		if skill.Source != "workspace" {
			continue
		}
		if filepath.Base(filepath.Dir(skill.Path)) == directory {
			return true
		}
	}
	return false
}

func skillsRemoveFromWorkspace(workspace string, toolsConfig config.SkillsToolsConfig, skillName string) error {
	name := strings.TrimSpace(skillName)
	name = strings.Trim(name, "/")
	if name == "" {
		return fmt.Errorf("skill name is required")
	}
	if strings.Contains(name, "/") {
		dirName, err := skills.GitHubInstallDirNameFromToolsConfig(toolsConfig, name)
		if err != nil || dirName == "" {
			return fmt.Errorf("invalid skill name %q", skillName)
		}
		name = dirName
	}
	// A valid skill name is a single folder name, so the removal stays inside
	// the skills folder (`..\..\x` would leave it on Windows).
	skillDir, err := skills.SkillDir(filepath.Join(workspace, "skills"), name)
	if err != nil {
		return fmt.Errorf("invalid skill name %q: %w", skillName, err)
	}
	if _, err := os.Stat(skillDir); os.IsNotExist(err) {
		return fmt.Errorf("skill '%s' not found", name)
	}
	if err := os.RemoveAll(skillDir); err != nil {
		return fmt.Errorf("failed to remove skill '%s': %w", name, err)
	}
	return nil
}

// builtinSkill is a skill bundled in the binary (the workspace/skills tree
// onboarding installs).
type builtinSkill struct {
	Name        string
	Description string
}

// builtinSkillsFS returns the bundled skills.
func builtinSkillsFS() (fs.FS, error) {
	return fs.Sub(compa.OnboardWorkspace, "workspace/skills")
}

func listBuiltinSkills(fsys fs.FS) ([]builtinSkill, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	var out []builtinSkill
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		data, err := fs.ReadFile(fsys, path.Join(entry.Name(), "SKILL.md"))
		if err != nil {
			continue
		}
		out = append(out, builtinSkill{Name: entry.Name(), Description: frontmatterDescription(string(data))})
	}
	return out, nil
}

// frontmatterDescription returns the description field of a SKILL.md.
func frontmatterDescription(content string) string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	if !strings.HasPrefix(content, "---\n") {
		return ""
	}
	end := strings.Index(content[4:], "\n---")
	if end < 0 {
		return ""
	}
	var meta struct {
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal([]byte(content[4:4+end]), &meta); err != nil {
		return ""
	}
	return strings.TrimSpace(meta.Description)
}

// installBuiltinSkills copies the bundled skills into the workspace. Skills
// already in the workspace are kept as they are, since the user may have
// changed them.
func installBuiltinSkills(w io.Writer, fsys fs.FS, workspace string) error {
	list, err := listBuiltinSkills(fsys)
	if err != nil {
		return fmt.Errorf("read builtin skills: %w", err)
	}
	workspaceSkillsDir := filepath.Join(workspace, "skills")
	var failed []string
	for _, skill := range list {
		target := filepath.Join(workspaceSkillsDir, skill.Name)
		if _, err := os.Stat(target); err == nil {
			fmt.Fprintf(w, "⊘ %s is already installed (kept)\n", skill.Name)
			continue
		}
		if err := copyFSDir(fsys, skill.Name, target); err != nil {
			fmt.Fprintf(w, "✗ %s: %v\n", skill.Name, err)
			failed = append(failed, skill.Name)
			continue
		}
		fmt.Fprintf(w, "✓ %s installed\n", skill.Name)
	}
	if len(failed) > 0 {
		return fmt.Errorf("failed to install builtin skills: %s", strings.Join(failed, ", "))
	}
	return nil
}

// copyFSDir copies dir from fsys to target; shell scripts are made executable.
func copyFSDir(fsys fs.FS, dir, target string) error {
	return fs.WalkDir(fsys, dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(p, dir), "/")
		dst := filepath.Join(target, filepath.FromSlash(rel))
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(p, ".sh") {
			mode = 0o755
		}
		return os.WriteFile(dst, data, mode)
	})
}

func skillsListBuiltin(w io.Writer, fsys fs.FS) error {
	list, err := listBuiltinSkills(fsys)
	if err != nil {
		return fmt.Errorf("read builtin skills: %w", err)
	}
	fmt.Fprintln(w, "\nAvailable Builtin Skills:")
	fmt.Fprintln(w, "-----------------------")
	if len(list) == 0 {
		fmt.Fprintln(w, "No builtin skills available.")
		return nil
	}
	for _, skill := range list {
		fmt.Fprintf(w, "  ✓  %s\n", skill.Name)
		if skill.Description != "" {
			fmt.Fprintf(w, "     %s\n", skill.Description)
		}
	}
	return nil
}

func skillsSearchCmd(query string) error {
	fmt.Println("Searching for available skills...")

	cfg, err := internal.LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	registryMgr := skills.NewRegistryManagerFromToolsConfig(cfg.Tools.Skills)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	results, err := registryMgr.SearchAll(ctx, query, skillsSearchMaxResults)
	if err != nil {
		return fmt.Errorf("failed to fetch skills list: %w", err)
	}

	if len(results) == 0 {
		fmt.Println("No skills available.")
		return nil
	}

	fmt.Printf("\nAvailable Skills (%d):\n", len(results))
	fmt.Println("--------------------")
	for _, result := range results {
		fmt.Printf("  📦 %s\n", result.DisplayName)
		fmt.Printf("     %s\n", result.Summary)
		fmt.Printf("     Slug: %s\n", result.Slug)
		fmt.Printf("     Registry: %s\n", result.RegistryName)
		if result.Version != "" {
			fmt.Printf("     Version: %s\n", result.Version)
		}
		if result.RegistryName == "github" {
			fmt.Printf("     Install: compa-kernel skills install %s\n", result.Slug)
		} else {
			fmt.Printf("     Install: compa-kernel skills install --registry=%s %s\n", result.RegistryName, result.Slug)
		}
		fmt.Println()
	}
	return nil
}

func skillsShowCmd(loader *skills.SkillsLoader, skillName string) error {
	content, ok := loader.LoadSkill(skillName)
	if !ok {
		return fmt.Errorf("skill '%s' not found", skillName)
	}

	fmt.Printf("\n📦 Skill: %s\n", skillName)
	fmt.Println("----------------------")
	fmt.Println(content)
	return nil
}
