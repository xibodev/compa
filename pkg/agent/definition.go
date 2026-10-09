package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/gomarkdown/markdown/parser"
	"gopkg.in/yaml.v3"

	"github.com/xibodev/compa/v4/pkg/logger"
)

// Workspace files that define an agent. AGENT.md carries YAML frontmatter
// with machine-readable settings followed by the prompt body; SOUL.md and
// USER.md are plain markdown composed after it.
const (
	agentDefinitionFile = "AGENT.md"
	soulDefinitionFile  = "SOUL.md"
	userDefinitionFile  = "USER.md"
)

// Boolean AGENT.md frontmatter keys an embedding host sets, read from
// AgentFrontmatter.Fields. name and description are the typed fields.
const (
	frontmatterMemory           = "memory"
	frontmatterPrivateWorkspace = "privateWorkspace"
	frontmatterRequireTools     = "requireTools"
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

// agentIdentity is what AGENT.md frontmatter asks of the kernel identity at
// the top of the system prompt.
type agentIdentity struct {
	// name and description replace Compa's own identity when name is set.
	name        string
	description string
	// memory keeps the memory rule, the memory paths and the memory context.
	memory bool
	// privateWorkspace leaves out the workspace paths; the host describes
	// its own tool workspace.
	privateWorkspace bool
}

// identity returns the kernel identity the definition asks for. The name
// "compa" keeps Compa's own identity: Compa's workspace template uses it.
func (d AgentContextDefinition) identity() agentIdentity {
	identity := agentIdentity{
		memory:           d.frontmatterFlag(frontmatterMemory, true),
		privateWorkspace: d.frontmatterFlag(frontmatterPrivateWorkspace, false),
	}
	if d.Agent == nil {
		return identity
	}
	name := strings.Join(strings.Fields(d.Agent.Frontmatter.Name), " ")
	if name != "" && !strings.EqualFold(name, "compa") {
		identity.name = name
		identity.description = strings.Join(strings.Fields(d.Agent.Frontmatter.Description), " ")
	}
	return identity
}

// requiresTools reports whether AGENT.md sets requireTools: a model that
// rejects tool calls then fails the turn instead of answering without tools.
func (d AgentContextDefinition) requiresTools() bool {
	return d.frontmatterFlag(frontmatterRequireTools, false)
}

// frontmatterFlag returns a boolean AGENT.md frontmatter key, or fallback
// when the key is absent or not a boolean.
func (d AgentContextDefinition) frontmatterFlag(key string, fallback bool) bool {
	if d.Agent == nil {
		return fallback
	}
	value, ok := frontmatterBool(d.Agent.Frontmatter.Fields[key])
	if !ok {
		return fallback
	}
	return value
}

// frontmatterBool reads a YAML boolean, including the quoted and YAML 1.1
// spellings that yaml.v3 keeps as strings in an untyped map.
func frontmatterBool(raw any) (value, ok bool) {
	switch v := raw.(type) {
	case bool:
		return v, true
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "yes", "on":
			return true, true
		case "false", "no", "off":
			return false, true
		}
	}
	return false, false
}

// warnOnInvalidAgentFrontmatterFlags logs boolean AGENT.md keys whose value
// is not a boolean; they keep their defaults.
func warnOnInvalidAgentFrontmatterFlags(agentID, workspace string, definition AgentContextDefinition) {
	if definition.Agent == nil {
		return
	}
	for _, key := range []string{frontmatterMemory, frontmatterPrivateWorkspace, frontmatterRequireTools} {
		raw, present := definition.Agent.Frontmatter.Fields[key]
		if !present {
			continue
		}
		if _, ok := frontmatterBool(raw); !ok {
			logger.WarnCF("agent", "AGENT.md key is not a boolean; using its default", map[string]any{
				"agent_id":  agentID,
				"workspace": workspace,
				"key":       key,
				"value":     fmt.Sprint(raw),
			})
		}
	}
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
