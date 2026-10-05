package skills

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xibodev/compa/pkg/config"
	"github.com/xibodev/compa/pkg/skills"
)

func TestSkillsInstallFromRegistryWritesOriginMetadata(t *testing.T) {
	workspace := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	const commit = "0123456789abcdef0123456789abcdef01234567"

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/repos/foo/bar":
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"default_branch": "master"}))
		case "/api/v3/repos/foo/bar/commits/master":
			_, _ = w.Write([]byte(commit))
		case "/api/v3/repos/foo/bar/contents/.agents/skills/pr-review":
			assert.Equal(t, "ref="+commit, r.URL.RawQuery)
			require.NoError(t, json.NewEncoder(w).Encode([]map[string]any{{
				"type":         "file",
				"name":         "SKILL.md",
				"download_url": server.URL + "/raw/foo/bar/" + commit + "/.agents/skills/pr-review/SKILL.md",
			}}))
		case "/raw/foo/bar/" + commit + "/.agents/skills/pr-review/SKILL.md":
			_, _ = w.Write([]byte("---\nname: pr-review\ndescription: PR review skill\n---\n# PR Review\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	githubRegistry, ok := cfg.Tools.Skills.Registries.Get("github")
	require.True(t, ok)
	githubRegistry.BaseURL = server.URL
	cfg.Tools.Skills.Registries.Set("github", githubRegistry)

	target := server.URL + "/foo/bar/tree/master/.agents/skills/pr-review"
	require.NoError(t, skillsInstallFromRegistry(cfg, "github", target))

	metaPath := filepath.Join(workspace, "skills", "pr-review", ".skill-origin.json")
	data, err := os.ReadFile(metaPath)
	require.NoError(t, err)

	var meta installedSkillOriginMeta
	require.NoError(t, json.Unmarshal(data, &meta))
	assert.Equal(t, "third_party", meta.OriginKind)
	assert.Equal(t, "github", meta.Registry)
	assert.Equal(t, "foo/bar/.agents/skills/pr-review", meta.Slug)
	assert.Equal(t, server.URL+"/foo/bar/tree/master/.agents/skills/pr-review", meta.RegistryURL)
	assert.Equal(t, "master", meta.InstalledVersion)
	assert.Equal(t, commit, meta.Commit)
	assert.False(t, meta.Unpinned)
	assert.NotZero(t, meta.InstalledAt)
}

func TestSkillsInstallFromRegistryRecordsUnpinnedInstall(t *testing.T) {
	workspace := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace

	// The fake serves no commits endpoint, so the install can't be pinned.
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/repos/foo/bar/contents/.agents/skills/pr-review":
			assert.Equal(t, "ref=master", r.URL.RawQuery)
			require.NoError(t, json.NewEncoder(w).Encode([]map[string]any{{
				"type":         "file",
				"name":         "SKILL.md",
				"download_url": server.URL + "/raw/foo/bar/master/.agents/skills/pr-review/SKILL.md",
			}}))
		case "/raw/foo/bar/master/.agents/skills/pr-review/SKILL.md":
			_, _ = w.Write([]byte("---\nname: pr-review\ndescription: PR review skill\n---\n# PR Review\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	githubRegistry, ok := cfg.Tools.Skills.Registries.Get("github")
	require.True(t, ok)
	githubRegistry.BaseURL = server.URL
	cfg.Tools.Skills.Registries.Set("github", githubRegistry)

	target := server.URL + "/foo/bar/tree/master/.agents/skills/pr-review"
	require.NoError(t, skillsInstallFromRegistry(cfg, "github", target))

	data, err := os.ReadFile(filepath.Join(workspace, "skills", "pr-review", ".skill-origin.json"))
	require.NoError(t, err)
	var meta installedSkillOriginMeta
	require.NoError(t, json.Unmarshal(data, &meta))
	assert.Equal(t, "master", meta.InstalledVersion)
	assert.Empty(t, meta.Commit)
	assert.True(t, meta.Unpinned)
}

func TestSkillsInstallFromRegistryRejectsInvalidSkillArchive(t *testing.T) {
	workspace := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/repos/foo/bar":
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"default_branch": "master"}))
		case "/api/v3/repos/foo/bar/contents/.agents/skills/pr-review":
			require.NoError(t, json.NewEncoder(w).Encode([]map[string]any{{
				"type":         "file",
				"name":         "SKILL.md",
				"download_url": server.URL + "/raw/foo/bar/master/.agents/skills/pr-review/SKILL.md",
			}}))
		case "/raw/foo/bar/master/.agents/skills/pr-review/SKILL.md":
			// No description anywhere: not a valid skill.
			_, _ = w.Write([]byte("---\nname: pr-review\n---\n# Invalid\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	githubRegistry, ok := cfg.Tools.Skills.Registries.Get("github")
	require.True(t, ok)
	githubRegistry.BaseURL = server.URL
	cfg.Tools.Skills.Registries.Set("github", githubRegistry)

	target := server.URL + "/foo/bar/tree/master/.agents/skills/pr-review"
	err := skillsInstallFromRegistry(cfg, "github", target)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not a valid skill")
	_, statErr := os.Stat(filepath.Join(workspace, "skills", "pr-review"))
	assert.True(t, os.IsNotExist(statErr))
}

func TestSkillsRemoveFromWorkspaceRejectsDotTarget(t *testing.T) {
	workspace := t.TempDir()
	skillsDir := filepath.Join(workspace, "skills")
	require.NoError(t, os.MkdirAll(skillsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillsDir, "keep.txt"), []byte("keep"), 0o644))

	err := skillsRemoveFromWorkspace(workspace, config.DefaultConfig().Tools.Skills, ".")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid skill name")

	_, statErr := os.Stat(skillsDir)
	assert.NoError(t, statErr)
	_, fileErr := os.Stat(filepath.Join(skillsDir, "keep.txt"))
	assert.NoError(t, fileErr)
}

func TestSkillsShowCmdFailsForMissingSkill(t *testing.T) {
	loader := skills.NewSkillsLoader(t.TempDir(), "", "")
	err := skillsShowCmd(loader, "missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestSkillsRemoveFromWorkspaceRejectsTraversal(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "skills"), 0o755))
	outside := filepath.Join(workspace, "keep")
	require.NoError(t, os.MkdirAll(outside, 0o755))

	for _, name := range []string{`..\keep`, `..\..\keep`, `x\..\..\keep`, `c:keep`, ".."} {
		err := skillsRemoveFromWorkspace(workspace, config.DefaultConfig().Tools.Skills, name)
		require.Error(t, err, "name %q", name)
		assert.Contains(t, err.Error(), "invalid skill name")
	}
	_, statErr := os.Stat(outside)
	assert.NoError(t, statErr, "a folder outside skills/ was removed")
}

func TestSkillsRemoveFromWorkspaceUsesLastPathSegment(t *testing.T) {
	workspace := t.TempDir()
	targetDir := filepath.Join(workspace, "skills", "pr-review")
	require.NoError(t, os.MkdirAll(targetDir, 0o755))

	err := skillsRemoveFromWorkspace(
		workspace,
		config.DefaultConfig().Tools.Skills,
		"https://github.com/foo/bar/tree/main/.agents/skills/pr-review",
	)
	require.NoError(t, err)

	_, statErr := os.Stat(targetDir)
	assert.True(t, os.IsNotExist(statErr))
}

func TestSkillsRemoveFromWorkspaceSupportsRepoRootGitHubBlobURL(t *testing.T) {
	workspace := t.TempDir()
	targetDir := filepath.Join(workspace, "skills", "bar")
	require.NoError(t, os.MkdirAll(targetDir, 0o755))

	err := skillsRemoveFromWorkspace(
		workspace,
		config.DefaultConfig().Tools.Skills,
		"https://github.com/foo/bar/blob/feature/skills-registry/SKILL.md",
	)
	require.NoError(t, err)

	_, statErr := os.Stat(targetDir)
	assert.True(t, os.IsNotExist(statErr))
}

func TestSkillsRemoveFromWorkspaceSupportsGitHubEnterpriseURL(t *testing.T) {
	workspace := t.TempDir()
	targetDir := filepath.Join(workspace, "skills", "pr-review")
	require.NoError(t, os.MkdirAll(targetDir, 0o755))

	cfg := config.DefaultConfig()
	githubRegistry, ok := cfg.Tools.Skills.Registries.Get("github")
	require.True(t, ok)
	githubRegistry.BaseURL = "https://ghe.example.com/git"
	cfg.Tools.Skills.Registries.Set("github", githubRegistry)

	err := skillsRemoveFromWorkspace(
		workspace,
		cfg.Tools.Skills,
		"https://ghe.example.com/git/foo/bar/tree/main/.agents/skills/pr-review",
	)
	require.NoError(t, err)

	_, statErr := os.Stat(targetDir)
	assert.True(t, os.IsNotExist(statErr))
}

func TestSkillsRemoveFromWorkspaceDoesNotRequireEnabledGitHubRegistry(t *testing.T) {
	workspace := t.TempDir()
	targetDir := filepath.Join(workspace, "skills", "pr-review")
	require.NoError(t, os.MkdirAll(targetDir, 0o755))

	cfg := config.DefaultConfig()
	githubRegistry, ok := cfg.Tools.Skills.Registries.Get("github")
	require.True(t, ok)
	githubRegistry.Enabled = false
	cfg.Tools.Skills.Registries.Set("github", githubRegistry)

	err := skillsRemoveFromWorkspace(
		workspace,
		cfg.Tools.Skills,
		"https://github.com/foo/bar/tree/main/.agents/skills/pr-review",
	)
	require.NoError(t, err)

	_, statErr := os.Stat(targetDir)
	assert.True(t, os.IsNotExist(statErr))
}
