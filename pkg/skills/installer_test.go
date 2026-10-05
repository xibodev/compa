package skills

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestParseGitHubRef(t *testing.T) {
	tests := []struct {
		name           string
		repo           string
		wantOwner      string
		wantRepoName   string
		wantRef        string
		wantSubPath    string
		wantErr        bool
		wantErrContain string
	}{
		// The fork rename sipeed -> xibodev updated the `repo` inputs of the
		// URL cases and not their `wantOwner`, so three cases asserted that
		// parsing "github.com/xibodev/..." yields "sipeed". The code was right
		// and the expectation was stale. The bare owner/repo cases below still
		// say sipeed deliberately: they test that the parser echoes whatever
		// owner it is given, whoever that is.
		{
			name:         "simple owner/repo",
			repo:         "sipeed/compa",
			wantOwner:    "sipeed",
			wantRepoName: "compa",
			wantRef:      "main",
			wantSubPath:  "",
		},
		{
			name:         "owner/repo with subpath",
			repo:         "sipeed/compa/skills/test",
			wantOwner:    "sipeed",
			wantRepoName: "compa",
			wantRef:      "main",
			wantSubPath:  "skills/test",
		},
		{
			name:         "full URL with tree",
			repo:         "https://github.com/xibodev/compa/tree/dev/skills/test",
			wantOwner:    "xibodev",
			wantRepoName: "compa",
			wantRef:      "dev",
			wantSubPath:  "skills/test",
		},
		{
			name:         "full URL with blob",
			repo:         "https://github.com/xibodev/compa/blob/main/README.md",
			wantOwner:    "xibodev",
			wantRepoName: "compa",
			wantRef:      "main",
			wantSubPath:  "README.md",
		},
		{
			name:         "full URL without ref",
			repo:         "https://github.com/xibodev/compa",
			wantOwner:    "xibodev",
			wantRepoName: "compa",
			wantRef:      "main",
			wantSubPath:  "",
		},
		{
			name:           "invalid format - single part",
			repo:           "sipeed",
			wantErr:        true,
			wantErrContain: "expected 'owner/repo'",
		},
		{
			name:           "invalid URL",
			repo:           "http://[invalid",
			wantErr:        true,
			wantErrContain: "invalid URL",
		},
		{
			name:           "invalid GitHub URL - only one path part",
			repo:           "https://github.com/sipeed",
			wantErr:        true,
			wantErrContain: "invalid GitHub URL",
		},
		{
			name:         "with whitespace",
			repo:         "  sipeed/compa  ",
			wantOwner:    "sipeed",
			wantRepoName: "compa",
			wantRef:      "main",
			wantSubPath:  "",
		},
		{
			name:           "invalid non github host",
			repo:           "https://gitlab.com/sipeed/compa/-/tree/main/skills/test",
			wantErr:        true,
			wantErrContain: `invalid GitHub URL host "gitlab.com"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ref, err := parseGitHubRef(tt.repo)

			if tt.wantErr {
				if err == nil {
					t.Errorf("parseGitHubRef() error = nil, wantErr = true")
					return
				}
				if tt.wantErrContain != "" && !strings.Contains(err.Error(), tt.wantErrContain) {
					t.Errorf("parseGitHubRef() error = %v, want error containing %v", err, tt.wantErrContain)
				}
				return
			}

			if err != nil {
				t.Errorf("parseGitHubRef() unexpected error = %v", err)
				return
			}

			if ref.Owner != tt.wantOwner {
				t.Errorf("parseGitHubRef() owner = %v, want %v", ref.Owner, tt.wantOwner)
			}
			if ref.RepoName != tt.wantRepoName {
				t.Errorf("parseGitHubRef() repoName = %v, want %v", ref.RepoName, tt.wantRepoName)
			}
			if ref.Ref != tt.wantRef {
				t.Errorf("parseGitHubRef() ref = %v, want %v", ref.Ref, tt.wantRef)
			}
			if ref.SubPath != tt.wantSubPath {
				t.Errorf("parseGitHubRef() subPath = %v, want %v", ref.SubPath, tt.wantSubPath)
			}
		})
	}
}

func TestParseGitHubRefWithBaseURL(t *testing.T) {
	ref, err := parseGitHubRefWithBaseURL(
		"https://ghe.example.com/git/org/repo/tree/dev/skills/test",
		"https://ghe.example.com/git",
		"main",
	)
	if err != nil {
		t.Fatalf("parseGitHubRefWithBaseURL() unexpected error = %v", err)
	}
	if ref.Owner != "org" {
		t.Fatalf("owner = %q, want org", ref.Owner)
	}
	if ref.RepoName != "repo" {
		t.Fatalf("repo = %q, want repo", ref.RepoName)
	}
	if ref.Ref != "dev" {
		t.Fatalf("ref = %q, want dev", ref.Ref)
	}
	if ref.SubPath != "skills/test" {
		t.Fatalf("subPath = %q, want skills/test", ref.SubPath)
	}

	dirName, err := githubInstallDirNameWithBaseURL(
		"https://ghe.example.com/git/org/repo/tree/dev/skills/test",
		"https://ghe.example.com/git",
	)
	if err != nil {
		t.Fatalf("githubInstallDirNameWithBaseURL() unexpected error = %v", err)
	}
	if dirName != "test" {
		t.Fatalf("dirName = %q, want test", dirName)
	}

	dirName, err = githubInstallDirNameWithBaseURL(
		"https://ghe.example.com/git/org/repo/blob/dev/skills/test/SKILL.md",
		"https://ghe.example.com/git",
	)
	if err != nil {
		t.Fatalf("githubInstallDirNameWithBaseURL() unexpected error for blob skill url = %v", err)
	}
	if dirName != "test" {
		t.Fatalf("dirName for nested blob skill = %q, want test", dirName)
	}

	dirName, err = githubInstallDirNameWithBaseURL(
		"https://ghe.example.com/git/org/repo/blob/dev/SKILL.md",
		"https://ghe.example.com/git",
	)
	if err != nil {
		t.Fatalf("githubInstallDirNameWithBaseURL() unexpected error for repo root blob skill = %v", err)
	}
	if dirName != "repo" {
		t.Fatalf("dirName for repo root blob skill = %q, want repo", dirName)
	}

	ref, err = parseGitHubRefWithBaseURL("https://ghe.example.com/git/org/repo", "https://ghe.example.com/git", "")
	if err != nil {
		t.Fatalf("parseGitHubRefWithBaseURL() unexpected error = %v", err)
	}
	if ref.Ref != "" {
		t.Fatalf("ref = %q, want empty", ref.Ref)
	}

	ref, err = parseGitHubRefWithBaseURL(
		"https://github.com/org/repo/tree/feature/skills-registry/.agents/skills/pr-review",
		"",
		"main",
	)
	if err != nil {
		t.Fatalf("parseGitHubRefWithBaseURL() unexpected error for slash branch = %v", err)
	}
	if ref.Ref != "feature/skills-registry" {
		t.Fatalf("ref = %q, want feature/skills-registry", ref.Ref)
	}
	if ref.SubPath != ".agents/skills/pr-review" {
		t.Fatalf("subPath = %q, want .agents/skills/pr-review", ref.SubPath)
	}

	_, err = parseGitHubRefWithBaseURL(
		"https://gitlab.example.com/org/repo/-/tree/dev/skills/test",
		"https://ghe.example.com/git",
		"main",
	)
	if err == nil {
		t.Fatal("parseGitHubRefWithBaseURL() error = nil, want invalid host error")
	}
	if !strings.Contains(err.Error(), `invalid GitHub URL host "gitlab.example.com"`) {
		t.Fatalf("unexpected error = %v", err)
	}

	_, err = parseGitHubRefWithBaseURL(
		"http://ghe.example.com/git/org/repo/tree/dev/skills/test",
		"https://ghe.example.com/git",
		"main",
	)
	if err == nil {
		t.Fatal("parseGitHubRefWithBaseURL() error = nil, want invalid host error for scheme mismatch")
	}
	if !strings.Contains(err.Error(), `invalid GitHub URL host "ghe.example.com"`) {
		t.Fatalf("unexpected scheme mismatch error = %v", err)
	}

	_, err = parseGitHubRefWithBaseURL(
		"https://github.com/org/repo/pull/2442",
		"",
		"main",
	)
	if err == nil {
		t.Fatal("parseGitHubRefWithBaseURL() error = nil, want invalid repository URL path error")
	}
	if !strings.Contains(err.Error(), `invalid GitHub repository URL path "/org/repo/pull/2442"`) {
		t.Fatalf("unexpected PR URL error = %v", err)
	}

	_, err = parseGitHubRefWithBaseURL(
		"https://github.com/org/repo/tree",
		"",
		"main",
	)
	if err == nil {
		t.Fatal("parseGitHubRefWithBaseURL() error = nil, want invalid tree URL path error")
	}
	if !strings.Contains(err.Error(), `invalid GitHub tree URL path "/org/repo/tree"`) {
		t.Fatalf("unexpected short tree URL error = %v", err)
	}
}

func TestParseGitHubTargetWithBaseURLPreservesSourceEndpoints(t *testing.T) {
	target, err := parseGitHubTargetWithBaseURL(
		"https://github.com/org/repo/tree/main/.agents/skills/pr-review",
		"https://ghe.example.com/git",
		"",
	)
	if err != nil {
		t.Fatalf("parseGitHubTargetWithBaseURL() unexpected error = %v", err)
	}
	if target.Endpoints.WebBaseURL != "https://github.com" {
		t.Fatalf("web base = %q, want https://github.com", target.Endpoints.WebBaseURL)
	}
	if target.Endpoints.APIBaseURL != "https://api.github.com" {
		t.Fatalf("api base = %q, want https://api.github.com", target.Endpoints.APIBaseURL)
	}
	if target.Endpoints.RawBaseURL != "https://raw.githubusercontent.com" {
		t.Fatalf("raw base = %q, want https://raw.githubusercontent.com", target.Endpoints.RawBaseURL)
	}
	if target.Ref.Owner != "org" || target.Ref.RepoName != "repo" {
		t.Fatalf("unexpected ref = %+v", target.Ref)
	}
	if target.Ref.Ref != "main" {
		t.Fatalf("ref = %q, want main", target.Ref.Ref)
	}
	if target.Ref.SubPath != ".agents/skills/pr-review" {
		t.Fatalf("subPath = %q, want .agents/skills/pr-review", target.Ref.SubPath)
	}
}

func TestParseGitHubTargetWithBaseURLPreservesSlashBranchForRepoRootBlobSkill(t *testing.T) {
	target, err := parseGitHubTargetWithBaseURL(
		"https://github.com/org/repo/blob/feature/skills-registry/SKILL.md",
		"",
		"",
	)
	if err != nil {
		t.Fatalf("parseGitHubTargetWithBaseURL() unexpected error = %v", err)
	}
	if target.Ref.Ref != "feature/skills-registry" {
		t.Fatalf("ref = %q, want feature/skills-registry", target.Ref.Ref)
	}
	if target.Ref.SubPath != "SKILL.md" {
		t.Fatalf("subPath = %q, want SKILL.md", target.Ref.SubPath)
	}
}

func TestSkillInstallerResolveGitHubRefUsesDefaultBranch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/repos/org/repo":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"default_branch":"master"}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	installer, err := NewSkillInstallerWithBaseURL(t.TempDir(), server.URL, "", "")
	if err != nil {
		t.Fatalf("NewSkillInstallerWithBaseURL() error = %v", err)
	}

	target, err := installer.resolveGitHubTarget(context.Background(), "org/repo/skills/test", "")
	if err != nil {
		t.Fatalf("resolveGitHubTarget() error = %v", err)
	}
	ref := target.Ref
	if ref.Ref != "master" {
		t.Fatalf("ref = %q, want master", ref.Ref)
	}
	if ref.SubPath != "skills/test" {
		t.Fatalf("subPath = %q, want skills/test", ref.SubPath)
	}
}

func TestSkillInstallerInstallFromGitHubToDirSupportsBlobSkillURL(t *testing.T) {
	tmpDir := t.TempDir()
	const commit = "0123456789abcdef0123456789abcdef01234567"
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/repos/org/repo/commits/main":
			if r.Header.Get("Accept") != "application/vnd.github.sha" {
				t.Errorf("commit lookup Accept = %q", r.Header.Get("Accept"))
			}
			_, _ = w.Write([]byte(commit))
		case "/api/v3/repos/org/repo/contents/.agents/skills/pr-review":
			if got := r.URL.Query().Get("ref"); got != commit {
				t.Errorf("listing ref = %q, want the pinned commit", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[
				{"type":"file","name":"SKILL.md","download_url":"` + server.URL + `/raw/org/repo/` + commit + `/.agents/skills/pr-review/SKILL.md"},
				{"type":"dir","name":"scripts","url":"` + server.URL + `/api/v3/repos/org/repo/contents/.agents/skills/pr-review/scripts?ref=` + commit + `"}
			]`))
		case "/api/v3/repos/org/repo/contents/.agents/skills/pr-review/scripts":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[
				{"type":"file","name":"check.sh","download_url":"` + server.URL + `/raw/org/repo/` + commit + `/.agents/skills/pr-review/scripts/check.sh"}
			]`))
		case "/raw/org/repo/" + commit + "/.agents/skills/pr-review/SKILL.md":
			_, _ = w.Write([]byte("---\nname: pr-review\ndescription: PR review skill\n---\n# PR Review\n"))
		case "/raw/org/repo/" + commit + "/.agents/skills/pr-review/scripts/check.sh":
			_, _ = w.Write([]byte("#!/bin/sh\nexit 0\n"))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	installer, err := NewSkillInstallerWithBaseURL(tmpDir, server.URL, "", "")
	if err != nil {
		t.Fatalf("NewSkillInstallerWithBaseURL() error = %v", err)
	}

	targetDir := filepath.Join(tmpDir, "skills", "pr-review")
	result, err := installer.InstallFromGitHubToDir(
		context.Background(),
		server.URL+"/org/repo/blob/main/.agents/skills/pr-review/SKILL.md",
		"",
		targetDir,
	)
	if err != nil {
		t.Fatalf("InstallFromGitHubToDir() error = %v", err)
	}
	if result.Version != "main" {
		t.Fatalf("version = %q, want main", result.Version)
	}
	if result.Commit != commit {
		t.Fatalf("commit = %q, want the pinned %q", result.Commit, commit)
	}

	content, err := os.ReadFile(filepath.Join(targetDir, "SKILL.md"))
	if err != nil {
		t.Fatalf("ReadFile(SKILL.md) error = %v", err)
	}
	if !strings.Contains(string(content), "name: pr-review") {
		t.Fatalf("SKILL.md content = %q, want skill metadata", string(content))
	}

	scriptPath := filepath.Join(targetDir, "scripts", "check.sh")
	if _, err := os.Stat(scriptPath); err != nil {
		t.Fatalf("Stat(scripts/check.sh) error = %v", err)
	}
}

func TestShouldDownload(t *testing.T) {
	tests := []struct {
		name string
		file string
		root bool
		want bool
	}{
		{"SKILL.md at root", "SKILL.md", true, true},
		{"other file at root", "README.md", true, false},
		{"script at root", "script.py", true, false},
		{"SKILL.md not at root", "SKILL.md", false, true},
		{"any file not at root", "any.txt", false, true},
		{"script not at root", "script.py", false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldDownload(tt.file, tt.root)
			if got != tt.want {
				t.Errorf("shouldDownload(%q, %v) = %v, want %v", tt.file, tt.root, got, tt.want)
			}
		})
	}
}

func TestIsSkillDirectory(t *testing.T) {
	tests := []struct {
		name string
		dir  string
		want bool
	}{
		{"scripts dir", "scripts", true},
		{"references dir", "references", true},
		{"assets dir", "assets", true},
		{"templates dir", "templates", true},
		{"docs dir", "docs", true},
		{"other dir", "other", false},
		{"src dir", "src", false},
		{"empty string", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isSkillDirectory(tt.dir)
			if got != tt.want {
				t.Errorf("isSkillDirectory(%q) = %v, want %v", tt.dir, got, tt.want)
			}
		})
	}
}

func TestNewSkillInstaller(t *testing.T) {
	tmpDir := t.TempDir()
	installer, err := NewSkillInstaller(tmpDir, "test-token", "")
	if err != nil {
		t.Fatalf("NewSkillInstaller() error = %v", err)
	}

	if installer == nil {
		t.Fatal("NewSkillInstaller() returned nil")
	}

	if installer.workspace != tmpDir {
		t.Errorf("workspace = %v, want %v", installer.workspace, tmpDir)
	}

	if installer.githubToken != "test-token" {
		t.Errorf("githubToken = %v, want 'test-token'", installer.githubToken)
	}

	if installer.githubBaseURL != "https://github.com" {
		t.Errorf("githubBaseURL = %v, want https://github.com", installer.githubBaseURL)
	}
	if installer.githubAPIBaseURL != "https://api.github.com" {
		t.Errorf("githubAPIBaseURL = %v, want https://api.github.com", installer.githubAPIBaseURL)
	}
	if installer.githubRawBaseURL != "https://raw.githubusercontent.com" {
		t.Errorf("githubRawBaseURL = %v, want https://raw.githubusercontent.com", installer.githubRawBaseURL)
	}

	if installer.proxy != "" {
		t.Errorf("proxy = %v, want empty", installer.proxy)
	}

	if installer.client == nil {
		t.Error("client is nil")
	} else if installer.client.Timeout != 15*time.Second {
		t.Errorf("client.Timeout = %v, want 15s", installer.client.Timeout)
	}
}

func TestNewSkillInstaller_WithProxy(t *testing.T) {
	tmpDir := t.TempDir()
	installer, err := NewSkillInstaller(tmpDir, "test-token", "http://127.0.0.1:7890")
	if err != nil {
		t.Fatalf("NewSkillInstaller() error = %v", err)
	}

	if installer.proxy != "http://127.0.0.1:7890" {
		t.Errorf("proxy = %v, want 'http://127.0.0.1:7890'", installer.proxy)
	}

	if installer.client == nil {
		t.Fatal("client is nil")
	}

	// Verify the transport has proxy configured
	transport, ok := installer.client.Transport.(*http.Transport)
	if !ok {
		t.Fatal("client.Transport is not *http.Transport")
	}

	if transport.Proxy == nil {
		t.Error("transport.Proxy is nil, expected non-nil")
	}
}

func TestNewSkillInstaller_WithBaseURL(t *testing.T) {
	tmpDir := t.TempDir()
	installer, err := NewSkillInstallerWithBaseURL(tmpDir, "https://github.example.com", "test-token", "")
	if err != nil {
		t.Fatalf("NewSkillInstallerWithBaseURL() error = %v", err)
	}

	if installer.githubBaseURL != "https://github.example.com" {
		t.Errorf("githubBaseURL = %v, want https://github.example.com", installer.githubBaseURL)
	}
	if installer.githubAPIBaseURL != "https://github.example.com/api/v3" {
		t.Errorf("githubAPIBaseURL = %v, want https://github.example.com/api/v3", installer.githubAPIBaseURL)
	}
	if installer.githubRawBaseURL != "https://github.example.com/raw" {
		t.Errorf("githubRawBaseURL = %v, want https://github.example.com/raw", installer.githubRawBaseURL)
	}
}

func TestNewSkillInstaller_InvalidProxy(t *testing.T) {
	tmpDir := t.TempDir()
	installer, err := NewSkillInstaller(tmpDir, "test-token", "://invalid-proxy")
	if err == nil {
		t.Error("NewSkillInstaller() expected error for invalid proxy, got nil")
	}
	if installer != nil {
		t.Error("expected nil installer on error")
	}
}

func TestSkillInstaller_DownloadFile(t *testing.T) {
	// Create a test server that serves files
	content := "test file content for skill download"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(content))
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	installer, err := NewSkillInstaller(tmpDir, "", "")
	if err != nil {
		t.Fatalf("NewSkillInstaller() error = %v", err)
	}

	t.Run("successful download", func(t *testing.T) {
		localPath := filepath.Join(tmpDir, "test-skill", "SKILL.md")
		err := installer.downloadFile(context.Background(), server.URL, localPath)
		if err != nil {
			t.Errorf("downloadFile() error = %v", err)
			return
		}

		// Verify file was downloaded
		data, err := os.ReadFile(localPath)
		if err != nil {
			t.Errorf("failed to read downloaded file: %v", err)
			return
		}

		if string(data) != content {
			t.Errorf("downloaded content = %q, want %q", string(data), content)
		}

		// Check file permissions
		info, err := os.Stat(localPath)
		if err != nil {
			t.Errorf("failed to stat file: %v", err)
			return
		}

		// Windows has no POSIX permission bits: a 0o600 file reports 0o666
		// because Chmod there can only toggle read-only. Asserting the exact
		// mode tested the OS rather than the code, and every other assertion
		// in this test passed.
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Errorf("file permissions = %o, want %o", info.Mode().Perm(), 0o600)
		}
	})

	t.Run("http error", func(t *testing.T) {
		errorServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte("not found"))
		}))
		defer errorServer.Close()

		localPath := filepath.Join(tmpDir, "error-test", "SKILL.md")
		err := installer.downloadFile(context.Background(), errorServer.URL, localPath)
		if err == nil {
			t.Error("downloadFile() expected error for 404, got nil")
		}
	})
}

func TestSkillInstaller_DownloadRaw(t *testing.T) {
	content := "raw skill content"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(content))
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	installer, err := NewSkillInstaller(tmpDir, "", "")
	if err != nil {
		t.Fatalf("NewSkillInstaller() error = %v", err)
	}

	// Replace the client with one that points to our test server
	// We need to modify the URL in the function, so we'll test indirectly

	localDir := filepath.Join(tmpDir, "raw-test")
	ctx := context.Background()

	// Create a simple test by calling downloadFile directly since downloadRaw
	// constructs its own URL
	testFile := filepath.Join(localDir, "SKILL.md")
	err = installer.downloadFile(ctx, server.URL, testFile)
	if err != nil {
		t.Errorf("downloadFile() error = %v", err)
	}

	// Verify file content
	data, err := os.ReadFile(testFile)
	if err != nil {
		t.Errorf("failed to read file: %v", err)
		return
	}

	if string(data) != content {
		t.Errorf("content = %q, want %q", string(data), content)
	}
}

func TestSkillInstaller_Uninstall(t *testing.T) {
	tmpDir := t.TempDir()
	skillsDir := filepath.Join(tmpDir, "skills")
	os.MkdirAll(skillsDir, 0o755)

	installer, err := NewSkillInstaller(tmpDir, "", "")
	if err != nil {
		t.Fatalf("NewSkillInstaller() error = %v", err)
	}

	t.Run("uninstall existing skill", func(t *testing.T) {
		skillName := "test-skill"
		skillDir := filepath.Join(skillsDir, skillName)

		// Create skill directory with a file
		os.MkdirAll(skillDir, 0o755)
		os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("test"), 0o644)

		if err := installer.Uninstall(skillName); err != nil {
			t.Errorf("Uninstall() error = %v", err)
		}

		// Verify directory was removed
		if _, err := os.Stat(skillDir); !os.IsNotExist(err) {
			t.Error("skill directory still exists after uninstall")
		}
	})

	t.Run("uninstall non-existent skill", func(t *testing.T) {
		if err := installer.Uninstall("non-existent-skill"); err == nil {
			t.Error("Uninstall() expected error for non-existent skill, got nil")
		} else if !strings.Contains(err.Error(), "not found") {
			t.Errorf("error message = %q, want 'not found'", err.Error())
		}
	})

	t.Run("uninstall with path separator", func(t *testing.T) {
		skillName := "owner/repo/skill-name"
		skillDir := filepath.Join(skillsDir, "skill-name")

		// Create skill directory
		os.MkdirAll(skillDir, 0o755)
		os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("test"), 0o644)

		if err := installer.Uninstall(skillName); err != nil {
			t.Errorf("Uninstall() error = %v", err)
		}

		if _, err := os.Stat(skillDir); !os.IsNotExist(err) {
			t.Error("skill directory still exists after uninstall")
		}
	})

	t.Run("uninstall with trailing slash", func(t *testing.T) {
		skillName := "skill-name/"
		skillDir := filepath.Join(skillsDir, "skill-name")

		// Create skill directory
		os.MkdirAll(skillDir, 0o755)
		os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("test"), 0o644)

		if err := installer.Uninstall(skillName); err != nil {
			t.Errorf("Uninstall() error = %v", err)
		}

		if _, err := os.Stat(skillDir); !os.IsNotExist(err) {
			t.Error("skill directory still exists after uninstall")
		}
	})
}

func TestSkillInstaller_InstallFromGitHub_SkillAlreadyExists(t *testing.T) {
	tmpDir := t.TempDir()
	skillsDir := filepath.Join(tmpDir, "skills")
	os.MkdirAll(skillsDir, 0o755)

	installer, err := NewSkillInstaller(tmpDir, "", "")
	if err != nil {
		t.Fatalf("NewSkillInstaller() error = %v", err)
	}

	// Create an existing skill directory
	existingSkill := filepath.Join(skillsDir, "compa")
	os.MkdirAll(existingSkill, 0o755)
	os.WriteFile(filepath.Join(existingSkill, "SKILL.md"), []byte("existing"), 0o644)

	// Try to install the same skill - should fail
	err = installer.InstallFromGitHub(context.Background(), "sipeed/compa")
	if err == nil {
		t.Error("InstallFromGitHub() expected error for existing skill, got nil")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error message = %q, want 'already exists'", err.Error())
	}
}

func TestGitHubContent_Struct(t *testing.T) {
	// Test that GitHubContent struct can be properly unmarshaled
	jsonData := `{
		"name": "test.md",
		"path": "skills/test.md",
		"type": "file",
		"download_url": "https://example.com/download",
		"url": "https://api.github.com/contents/skills/test.md"
	}`

	var content GitHubContent
	err := json.Unmarshal([]byte(jsonData), &content)
	if err != nil {
		t.Errorf("failed to unmarshal GitHubContent: %v", err)
	}

	if content.Name != "test.md" {
		t.Errorf("Name = %q, want 'test.md'", content.Name)
	}
	if content.Type != "file" {
		t.Errorf("Type = %q, want 'file'", content.Type)
	}
	if content.DownloadURL != "https://example.com/download" {
		t.Errorf("DownloadURL = %q, want 'https://example.com/download'", content.DownloadURL)
	}
}

func TestSkillInstaller_GetGithubDirAllFiles(t *testing.T) {
	tmpDir := t.TempDir()
	installer, err := NewSkillInstaller(tmpDir, "", "")
	if err != nil {
		t.Fatalf("NewSkillInstaller() error = %v", err)
	}

	// Create a test server that mimics GitHub API
	fileContent := "skill file content"
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check for authorization header
		authHeader := r.Header.Get("Authorization")
		if authHeader != "" && !strings.HasPrefix(authHeader, "Bearer ") {
			t.Errorf("expected Bearer token, got: %s", authHeader)
		}

		// Return different responses based on path
		if strings.Contains(r.URL.Path, "/contents") {
			// API response for directory listing
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)

			items := []map[string]any{
				{
					"name":         "SKILL.md",
					"path":         "SKILL.md",
					"type":         "file",
					"download_url": serverURL + "/download/SKILL.md",
				},
				{
					"name": "scripts",
					"path": "scripts",
					"type": "dir",
					"url":  serverURL + "/api/scripts",
				},
			}
			json.NewEncoder(w).Encode(items)
		} else if strings.Contains(r.URL.Path, "/api/scripts") {
			// API response for scripts subdirectory
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)

			items := []map[string]any{
				{
					"name":         "test.py",
					"path":         "scripts/test.py",
					"type":         "file",
					"download_url": serverURL + "/download/test.py",
				},
			}
			json.NewEncoder(w).Encode(items)
		} else if strings.Contains(r.URL.Path, "/download/") {
			// Raw file download
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(fileContent))
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	serverURL = server.URL
	defer server.Close()

	localDir := filepath.Join(tmpDir, "test-skill")

	t.Run("download from GitHub API", func(t *testing.T) {
		err := installer.getGithubDirAllFiles(context.Background(), server.URL+"/contents", localDir, true)
		if err != nil {
			t.Errorf("getGithubDirAllFiles() error = %v", err)
			return
		}

		// Verify SKILL.md was downloaded
		skillMd := filepath.Join(localDir, "SKILL.md")
		data, err := os.ReadFile(skillMd)
		if err != nil {
			t.Errorf("failed to read SKILL.md: %v", err)
			return
		}
		if string(data) != fileContent {
			t.Errorf("SKILL.md content = %q, want %q", string(data), fileContent)
		}

		// Verify scripts directory and file
		scriptFile := filepath.Join(localDir, "scripts", "test.py")
		data, err = os.ReadFile(scriptFile)
		if err != nil {
			t.Errorf("failed to read test.py: %v", err)
			return
		}
		if string(data) != fileContent {
			t.Errorf("test.py content = %q, want %q", string(data), fileContent)
		}
	})

	t.Run("http error response", func(t *testing.T) {
		errorServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		defer errorServer.Close()

		err := installer.getGithubDirAllFiles(
			context.Background(),
			errorServer.URL,
			filepath.Join(tmpDir, "error-test"),
			true,
		)
		if err == nil {
			t.Error("getGithubDirAllFiles() expected error for 403, got nil")
		}
	})
}

func TestSkillInstaller_InstallFromGitHub_WithToken(t *testing.T) {
	tmpDir := t.TempDir()
	skillsDir := filepath.Join(tmpDir, "skills")
	os.MkdirAll(skillsDir, 0o755)

	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Capture the authorization header
		authHeader := r.Header.Get("Authorization")
		if authHeader != "" {
			tokenReceived := strings.TrimPrefix(authHeader, "Bearer ")
			t.Fatalf("github token is %s", tokenReceived)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		items := []map[string]any{
			{
				"name":         "SKILL.md",
				"path":         "SKILL.md",
				"type":         "file",
				"download_url": serverURL + "/download/SKILL.md",
			},
		}
		json.NewEncoder(w).Encode(items)
	}))
	serverURL = server.URL
	defer server.Close()

	installer, err := NewSkillInstaller(tmpDir, "test-github-token", "")
	if err != nil {
		t.Fatalf("NewSkillInstaller() error = %v", err)
	}

	// We need to test the token is passed - the actual install will fail
	// because we're not fully mocking the download, but we can verify
	// the token is sent in the request

	// Use a simple context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// The install will fail because download URL isn't properly set up,
	// but the token should be sent in the API request
	_ = installer.InstallFromGitHub(ctx, "owner/repo")

	// Note: We can't easily intercept the download request since it's a different URL,
	// but the fact that the API request was made verifies the token flow
	// In a real scenario, the token would be sent to both API and raw downloads
}

func TestSkillInstaller_ContextCancellation(t *testing.T) {
	tmpDir := t.TempDir()
	installer, err := NewSkillInstaller(tmpDir, "", "")
	if err != nil {
		t.Fatalf("NewSkillInstaller() error = %v", err)
	}

	// Create a slow server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("response"))
	}))
	defer server.Close()

	// Create a canceled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	localPath := filepath.Join(tmpDir, "cancel-test", "file.txt")
	err = installer.downloadFile(ctx, server.URL, localPath)

	if err == nil {
		t.Error("downloadFile() expected error for canceled context, got nil")
	}
}

func TestValidateGitHubItemName(t *testing.T) {
	for _, bad := range []string{"", ".", "..", `..\..\evil`, `a\b`, "a/b", "c:evil", "x..y"} {
		if err := validateGitHubItemName(bad); err == nil {
			t.Errorf("validateGitHubItemName(%q) = nil, want an error", bad)
		}
	}
	for _, ok := range []string{"SKILL.md", "check.sh", ".hidden", "a-b_c.txt"} {
		if err := validateGitHubItemName(ok); err != nil {
			t.Errorf("validateGitHubItemName(%q) = %v", ok, err)
		}
	}
}

func TestGetGithubDirAllFilesRejectsUnsafeListings(t *testing.T) {
	tokenLeaked := false
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			tokenLeaked = true
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer other.Close()

	listings := map[string]string{
		"backslash name": `[{"type":"file","name":"..\\..\\evil.sh","download_url":"{{server}}/download/x"}]`,
		"foreign subdir": `[{"type":"dir","name":"scripts","url":"` + other.URL + `/api/scripts"}]`,
		"foreign file":   `[{"type":"file","name":"SKILL.md","download_url":"` + other.URL + `/download/SKILL.md"}]`,
	}
	for name, listing := range listings {
		t.Run(name, func(t *testing.T) {
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/download/") {
					_, _ = w.Write([]byte("content"))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, strings.ReplaceAll(listing, "{{server}}", server.URL))
			}))
			defer server.Close()

			tmpDir := t.TempDir()
			installer, err := NewSkillInstaller(tmpDir, "secret-token", "")
			if err != nil {
				t.Fatalf("NewSkillInstaller() error = %v", err)
			}
			localDir := filepath.Join(tmpDir, "skills", "x")
			if err := installer.getGithubDirAllFiles(context.Background(), server.URL+"/contents", localDir, true); err == nil {
				t.Fatal("getGithubDirAllFiles() accepted an unsafe listing")
			}
			if tokenLeaked {
				t.Fatal("the GitHub token was sent to another host")
			}
			if _, err := os.Stat(filepath.Join(tmpDir, "evil.sh")); err == nil {
				t.Fatal("a file was written outside the skill folder")
			}
		})
	}
}

func TestInstallFromGitHubToDirSurfacesListingFailure(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	rawRequested := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/commits/main"):
			_, _ = w.Write([]byte(commit))
		case strings.Contains(r.URL.Path, "/contents"):
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
		default:
			rawRequested = true
			_, _ = w.Write([]byte("---\nname: repo\ndescription: d\n---\n"))
		}
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	installer, err := NewSkillInstallerWithBaseURL(tmpDir, server.URL, "", "")
	if err != nil {
		t.Fatalf("NewSkillInstallerWithBaseURL() error = %v", err)
	}
	targetDir := filepath.Join(tmpDir, "skills", "repo")
	_, err = installer.InstallFromGitHubToDir(context.Background(), "org/repo", "main", targetDir)
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("InstallFromGitHubToDir() error = %v, want the listing failure", err)
	}
	if rawRequested {
		t.Fatal("installer fell back to downloading SKILL.md alone")
	}
	if _, statErr := os.Stat(filepath.Join(targetDir, "SKILL.md")); statErr == nil {
		t.Fatal("a partial skill was installed")
	}
}

func TestInstallFromGitHubToDirFallsBackToRefWhenCommitUnresolved(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/repos/org/repo/contents/skills/demo":
			if got := r.URL.Query().Get("ref"); got != "main" {
				t.Errorf("listing ref = %q, want the unresolved ref", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"type":"file","name":"SKILL.md","download_url":"` +
				server.URL + `/raw/org/repo/main/skills/demo/SKILL.md"}]`))
		case "/raw/org/repo/main/skills/demo/SKILL.md":
			_, _ = w.Write([]byte("---\nname: demo\ndescription: d\n---\n# Demo\n"))
		default:
			// Includes the commits endpoint, as on a host that doesn't serve it.
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	installer, err := NewSkillInstallerWithBaseURL(tmpDir, server.URL, "", "")
	if err != nil {
		t.Fatalf("NewSkillInstallerWithBaseURL() error = %v", err)
	}
	targetDir := filepath.Join(tmpDir, "skills", "demo")
	result, err := installer.InstallFromGitHubToDir(context.Background(), "org/repo/skills/demo", "main", targetDir)
	if err != nil {
		t.Fatalf("InstallFromGitHubToDir() error = %v", err)
	}
	if result.Commit != "" || !result.Unpinned || result.Version != "main" {
		t.Fatalf("result = %+v, want an unpinned install of main", result)
	}
	if _, err := os.Stat(filepath.Join(targetDir, "SKILL.md")); err != nil {
		t.Fatalf("SKILL.md not installed: %v", err)
	}
}

func TestDownloadFileLimitedCapsSize(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, 2048))
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	installer, err := NewSkillInstaller(tmpDir, "", "")
	if err != nil {
		t.Fatalf("NewSkillInstaller() error = %v", err)
	}
	localPath := filepath.Join(tmpDir, "big", "file.bin")
	if _, err := installer.downloadFileLimited(context.Background(), server.URL, localPath, 1024); err == nil {
		t.Fatal("downloadFileLimited() accepted a file over the limit")
	}
	if _, err := os.Stat(localPath); err == nil {
		t.Fatal("an oversized file was written")
	}
}

func TestSkillInstaller_UninstallRejectsTraversal(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(tmpDir, "keep")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}

	installer, err := NewSkillInstaller(tmpDir, "", "")
	if err != nil {
		t.Fatalf("NewSkillInstaller() error = %v", err)
	}
	for _, name := range []string{`..\keep`, `..\..\keep`, "..", ".", `skills\..\..\keep`, "c:keep"} {
		if err := installer.Uninstall(name); err == nil {
			t.Errorf("Uninstall(%q) = nil, want an error", name)
		}
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("a folder outside skills/ was removed: %v", err)
	}
}

func TestSkillDir(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	dir, err := SkillDir(root, "weather")
	if err != nil || dir != filepath.Join(root, "weather") {
		t.Fatalf("SkillDir() = %q, %v", dir, err)
	}
	for _, bad := range []string{"", "..", `..\x`, "a/b", "/abs"} {
		if _, err := SkillDir(root, bad); err == nil {
			t.Errorf("SkillDir(%q) = nil error", bad)
		}
	}
}
