package agent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/gomarkdown/markdown/parser"
	"gopkg.in/yaml.v3"

	"github.com/xibodev/compa/v3/pkg/logger"
)

// Workspace files that define an agent. AGENT.md carries YAML frontmatter
// with machine-readable settings followed by the prompt body; SOUL.md and
// USER.md are plain markdown composed after it.
const (
	agentDefinitionFile = "AGENT.md"
	soulDefinitionFile  = "SOUL.md"
	userDefinitionFile  = "USER.md"
)

// AgentFrontmatter holds machine-readable AGENT.md configuration.
//
// Known fields are exposed directly for convenience. Fields keeps the full
// parsed frontmatter so future refactors can read additional keys without
// changing the loader contract again.
type AgentFrontmatter struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Tools       []string       `json:"tools,omitempty"`
	Model       string         `json:"model,omitempty"`
	Skills      []string       `json:"skills,omitempty"`
	MCPServers  []string       `json:"mcpServers,omitempty"`
	Fields      map[string]any `json:"-"`
}

// AgentPromptDefinition represents the parsed AGENT.md prompt file.
type AgentPromptDefinition struct {
	Path           string           `json:"path"`
	Raw            string           `json:"raw"`
	Body           string           `json:"body"`
	RawFrontmatter string           `json:"raw_frontmatter,omitempty"`
	Frontmatter    AgentFrontmatter `json:"frontmatter"`
	FrontmatterErr string           `json:"frontmatter_error,omitempty"`
}

// SoulDefinition represents the resolved SOUL.md file linked to the agent.
type SoulDefinition struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// UserDefinition represents the resolved USER.md file linked to the workspace.
type UserDefinition struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// AgentContextDefinition captures the workspace agent definition in a runtime-friendly shape.
type AgentContextDefinition struct {
	Agent *AgentPromptDefinition `json:"agent,omitempty"`
	Soul  *SoulDefinition        `json:"soul,omitempty"`
	User  *UserDefinition        `json:"user,omitempty"`
}

// LoadAgentDefinition parses the workspace agent definition files: AGENT.md,
// SOUL.md and USER.md. Each is optional.
func (cb *ContextBuilder) LoadAgentDefinition() AgentContextDefinition {
	return loadAgentDefinition(cb.workspace)
}

func loadAgentDefinition(workspace string) AgentContextDefinition {
	definition := AgentContextDefinition{
		User: loadUserDefinition(workspace),
	}
	agentPath := filepath.Join(workspace, agentDefinitionFile)
	if content, err := os.ReadFile(agentPath); err == nil {
		prompt := parseAgentPromptDefinition(agentPath, string(content))
		definition.Agent = &prompt
	}
	soulPath := filepath.Join(workspace, soulDefinitionFile)
	if content, err := os.ReadFile(soulPath); err == nil {
		definition.Soul = &SoulDefinition{
			Path:    soulPath,
			Content: string(content),
		}
	}
	return definition
}

// agentDefinitionPaths lists the definition files whose changes invalidate the
// cached system prompt.
func agentDefinitionPaths(workspace string) []string {
	return []string{
		filepath.Join(workspace, agentDefinitionFile),
		filepath.Join(workspace, soulDefinitionFile),
		filepath.Join(workspace, userDefinitionFile),
	}
}

func loadUserDefinition(workspace string) *UserDefinition {
	userPath := filepath.Join(workspace, userDefinitionFile)
	if content, err := os.ReadFile(userPath); err == nil {
		return &UserDefinition{
			Path:    userPath,
			Content: string(content),
		}
	}

	return nil
}

func parseAgentPromptDefinition(path, content string) AgentPromptDefinition {
	frontmatter, body := splitAgentFrontmatter(content)
	parsedFrontmatter, err := parseAgentFrontmatter(path, frontmatter)
	return AgentPromptDefinition{
		Path:           path,
		Raw:            content,
		Body:           body,
		RawFrontmatter: frontmatter,
		Frontmatter:    parsedFrontmatter,
		FrontmatterErr: errorString(err),
	}
}

func parseAgentFrontmatter(path, frontmatter string) (AgentFrontmatter, error) {
	frontmatter = strings.TrimSpace(frontmatter)
	if frontmatter == "" {
		return AgentFrontmatter{}, nil
	}

	rawFields := make(map[string]any)
	if err := yaml.Unmarshal([]byte(frontmatter), &rawFields); err != nil {
		logger.WarnCF("agent", "Failed to parse AGENT.md frontmatter", map[string]any{
			"path":  path,
			"error": err.Error(),
		})
		return AgentFrontmatter{}, err
	}

	var typed struct {
		Name        string   `yaml:"name"`
		Description string   `yaml:"description"`
		Tools       []string `yaml:"tools"`
		Model       string   `yaml:"model"`
		Skills      []string `yaml:"skills"`
		MCPServers  []string `yaml:"mcpServers"`
	}
	if err := yaml.Unmarshal([]byte(frontmatter), &typed); err != nil {
		logger.WarnCF("agent", "Failed to decode AGENT.md frontmatter fields", map[string]any{
			"path":  path,
			"error": err.Error(),
		})
		return AgentFrontmatter{}, err
	}

	return AgentFrontmatter{
		Name:        strings.TrimSpace(typed.Name),
		Description: strings.TrimSpace(typed.Description),
		Tools:       append([]string(nil), typed.Tools...),
		Model:       strings.TrimSpace(typed.Model),
		Skills:      append([]string(nil), typed.Skills...),
		MCPServers:  append([]string(nil), typed.MCPServers...),
		Fields:      rawFields,
	}, nil
}

func splitAgentFrontmatter(content string) (frontmatter, body string) {
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

func relativeWorkspacePath(workspace, path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	relativePath, err := filepath.Rel(workspace, path)
	if err == nil && relativePath != "." && !strings.HasPrefix(relativePath, "..") {
		return filepath.ToSlash(relativePath)
	}
	return filepath.Clean(path)
}

func uniquePaths(paths []string) []string {
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		cleaned := filepath.Clean(path)
		if slices.Contains(result, cleaned) {
			continue
		}
		result = append(result, cleaned)
	}
	return result
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
