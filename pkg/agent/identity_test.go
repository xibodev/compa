package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xibodev/compa/v3/pkg/bus"
	"github.com/xibodev/compa/v3/pkg/config"
	"github.com/xibodev/compa/v3/pkg/providers"
)

const (
	identityTestMemoryNote = "Synthetic long-term note."
	identityTestDailyNote  = "Synthetic daily note."

	middenAgentMD = `---
name: Midden
description: an assistant that turns recorded AI work into findings, articles, presentations and long-form writing, using the midden tool for session evidence
tools: [read_file, write_file, edit_file, append_file, list_dir, load_image, exec, midden]
memory: false
privateWorkspace: true
requireTools: true
---
This is Midden's private runtime. Outcome guidance comes from the mounted canonical skills. Tool workspaces are supplied by the host.
`
)

// identityTestWorkspace writes a synthetic workspace with memory notes and,
// when agentMD is not empty, an AGENT.md. Skills and the Compa home point
// at empty temporary folders.
func identityTestWorkspace(t *testing.T, agentMD string) string {
	t.Helper()
	t.Setenv(config.EnvHome, t.TempDir())
	t.Setenv(config.EnvBuiltinSkills, t.TempDir())

	workspace := t.TempDir()
	today := time.Now().Format("20060102")
	files := map[string]string{
		filepath.Join("memory", "MEMORY.md"):            identityTestMemoryNote,
		filepath.Join("memory", today[:6], today+".md"): identityTestDailyNote,
	}
	if agentMD != "" {
		files[agentDefinitionFile] = agentMD
	}
	for name, content := range files {
		path := filepath.Join(workspace, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return workspace
}

func identityPart(t *testing.T, cb *ContextBuilder, includeToolUseRule bool) string {
	t.Helper()
	parts := cb.buildSystemPromptParts(systemPromptBuildOptions{
		IncludeSkillCatalog: true,
		IncludeToolUseRule:  includeToolUseRule,
	})
	for _, part := range parts {
		if part.ID == "kernel.identity" {
			return part.Content
		}
	}
	t.Fatal("system prompt has no kernel.identity part")
	return ""
}

func absWorkspace(t *testing.T, workspace string) string {
	t.Helper()
	path, err := filepath.Abs(workspace)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// compaIdentity is Compa's own identity exactly as v1.0.0 renders it.
func compaIdentity(workspacePath string, includeToolUseRule bool) string {
	rules := []string{
		"**Be helpful and accurate** - Briefly explain what you're doing.",
		"**Context summaries** - Conversation summaries provided as context are approximate references only. They may be incomplete or outdated. Always defer to explicit user instructions over summary content.",
	}
	if includeToolUseRule {
		rules = []string{
			toolUseSystemPromptRule(),
			"**Be helpful and accurate** - When using tools, briefly explain what you're doing.",
			rules[1],
			"**Memory** - When interacting with me if something seems memorable, update " + workspacePath + "/memory/MEMORY.md",
		}
	}
	for i, rule := range rules {
		rules[i] = fmt.Sprintf("%d. %s", i+1, rule)
	}
	return fmt.Sprintf(`# Compa (%s)

You are Compa, the user's friendly personal assistant - "compa" means pal. You chat with the user, use tools on their computer to get things done, and your replies can be spoken aloud when they turn voice on.

## Workspace
Your workspace is at: %s
- Memory: %s/memory/MEMORY.md
- Daily Notes: %s/memory/YYYYMM/YYYYMMDD.md
- Skills: %s/skills/{skill-name}/SKILL.md

## Important Rules

%s
`,
		config.FormatVersion(),
		workspacePath,
		workspacePath,
		workspacePath,
		workspacePath,
		strings.Join(rules, "\n\n"),
	)
}

func TestIdentity_DefaultIsUnchanged(t *testing.T) {
	compaTemplate, err := os.ReadFile(filepath.Join("..", "..", "workspace", agentDefinitionFile))
	if err != nil {
		t.Fatalf("read Compa's workspace template: %v", err)
	}
	tests := []struct {
		name    string
		agentMD string
	}{
		{name: "no AGENT.md"},
		{name: "AGENT.md without frontmatter", agentMD: "# Agent\nBody only."},
		{name: "frontmatter without name", agentMD: "---\ndescription: unused without a name\nmodel: test-model\n---\nBody."},
		{name: "Compa's workspace template", agentMD: string(compaTemplate)},
		{name: "named Compa", agentMD: "---\nname: Compa\ndescription: someone else\n---\nBody."},
		{name: "invalid frontmatter", agentMD: "---\nname: [unclosed\n---\nBody."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspace := identityTestWorkspace(t, tt.agentMD)
			// Spoken replies only matter to a named identity.
			cb := NewContextBuilder(workspace).withSpokenReplies(true)
			workspacePath := absWorkspace(t, workspace)

			for _, withTools := range []bool{true, false} {
				if got, want := identityPart(t, cb, withTools), compaIdentity(workspacePath, withTools); got != want {
					t.Fatalf("identity (tool rule %v) changed:\n--- got\n%s\n--- want\n%s", withTools, got, want)
				}
			}
			prompt := cb.BuildSystemPromptWithCache()
			if !strings.HasPrefix(prompt, compaIdentity(workspacePath, true)) {
				t.Fatalf("cached prompt does not start with Compa's identity:\n%s", prompt)
			}
			if !strings.Contains(prompt, identityTestMemoryNote) || !strings.Contains(prompt, identityTestDailyNote) {
				t.Fatalf("cached prompt lost the memory context:\n%s", prompt)
			}
		})
	}
}

func TestIdentity_NamedAgent(t *testing.T) {
	version := config.FormatVersion()
	tests := []struct {
		name      string
		agentMD   string
		wantIntro string
	}{
		{
			name:      "name and description",
			agentMD:   "---\nname: Ledger\ndescription: a bookkeeping assistant for small shops\n---\nBody.",
			wantIntro: "# Ledger (powered by Compa " + version + ")\n\nYou are Ledger, a bookkeeping assistant for small shops. Compa is the agent runtime that powers you.\n\n",
		},
		{
			name:      "name only",
			agentMD:   "---\nname: Ledger\n---\nBody.",
			wantIntro: "# Ledger (powered by Compa " + version + ")\n\nYou are Ledger. Compa is the agent runtime that powers you.\n\n",
		},
		{
			name:      "description ending a sentence",
			agentMD:   "---\nname: Ledger\ndescription: a bookkeeping assistant.\n---\nBody.",
			wantIntro: "# Ledger (powered by Compa " + version + ")\n\nYou are Ledger, a bookkeeping assistant. Compa is the agent runtime that powers you.\n\n",
		},
		{
			name:      "multi-line description",
			agentMD:   "---\nname: \"  Ledger  \"\ndescription: |\n  a bookkeeping\n  assistant\n---\nBody.",
			wantIntro: "# Ledger (powered by Compa " + version + ")\n\nYou are Ledger, a bookkeeping assistant. Compa is the agent runtime that powers you.\n\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspace := identityTestWorkspace(t, tt.agentMD)
			cb := NewContextBuilder(workspace)
			identity := identityPart(t, cb, true)

			if !strings.HasPrefix(identity, tt.wantIntro) {
				t.Fatalf("identity intro:\n--- got\n%s\n--- want prefix\n%s", identity, tt.wantIntro)
			}
			for _, unwanted := range []string{"You are Compa", "friendly personal assistant", "spoken aloud", "voice"} {
				if strings.Contains(identity, unwanted) {
					t.Fatalf("named identity contains %q:\n%s", unwanted, identity)
				}
			}
			// memory and privateWorkspace keep their defaults.
			workspacePath := absWorkspace(t, workspace)
			for _, wanted := range []string{
				"## Workspace\nYour workspace is at: " + workspacePath + "\n",
				"- Memory: " + workspacePath + "/memory/MEMORY.md\n",
				"4. **Memory** - When interacting with me if something seems memorable, update " + workspacePath + "/memory/MEMORY.md",
			} {
				if !strings.Contains(identity, wanted) {
					t.Fatalf("named identity lacks %q:\n%s", wanted, identity)
				}
			}
		})
	}
}

func TestIdentity_NamedAgentMentionsVoiceOnlyWhenConfigured(t *testing.T) {
	const voiceSentence = "Your replies can be spoken aloud when the user turns voice on."
	workspace := identityTestWorkspace(t, "---\nname: Ledger\ndescription: a bookkeeping assistant\n---\nBody.")
	cb := NewContextBuilder(workspace)

	if identity := identityPart(t, cb, true); strings.Contains(identity, voiceSentence) {
		t.Fatalf("identity mentions voice without spoken replies:\n%s", identity)
	}
	before := cb.BuildSystemPromptWithCache()
	cb.withSpokenReplies(true)
	after := cb.BuildSystemPromptWithCache()
	want := "You are Ledger, a bookkeeping assistant. Compa is the agent runtime that powers you. " + voiceSentence + "\n"
	if strings.Contains(before, voiceSentence) || !strings.Contains(after, want) {
		t.Fatalf("spoken replies did not reach the cached prompt:\n--- before\n%s\n--- after\n%s", before, after)
	}
}

func TestSpokenRepliesConfigured(t *testing.T) {
	tests := []struct {
		name  string
		voice config.VoiceConfig
		want  bool
	}{
		{name: "off", voice: config.VoiceConfig{}},
		{name: "target without voice enabled", voice: config.VoiceConfig{TTSTarget: "voice/tts-1"}},
		{name: "dictation only", voice: config.VoiceConfig{Enabled: true, STTTarget: "voice/whisper-1"}},
		{name: "spoken replies", voice: config.VoiceConfig{Enabled: true, TTSTarget: "voice/tts-1"}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := spokenRepliesConfigured(&config.Config{Voice: tt.voice}); got != tt.want {
				t.Fatalf("spokenRepliesConfigured() = %v, want %v", got, tt.want)
			}

			cfg := identityTestConfig(t, identityTestWorkspace(t, ""))
			cfg.Voice = tt.voice
			al := NewAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{})
			defer al.Close()
			if got := al.registry.GetDefaultAgent().ContextBuilder.spokenReplies; got != tt.want {
				t.Fatalf("agent context builder spokenReplies = %v, want %v", got, tt.want)
			}
		})
	}
	if spokenRepliesConfigured(nil) {
		t.Fatal("spokenRepliesConfigured(nil) = true")
	}
}

func TestIdentity_MemoryOff(t *testing.T) {
	tests := []struct {
		name        string
		agentMD     string
		wantHeading string
	}{
		{name: "Compa's identity", agentMD: "---\nmemory: false\n---\nBody.", wantHeading: "# Compa ("},
		{name: "named identity", agentMD: "---\nname: Ledger\nmemory: false\n---\nBody.", wantHeading: "# Ledger (powered by Compa "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspace := identityTestWorkspace(t, tt.agentMD)
			cb := NewContextBuilder(workspace)
			workspacePath := absWorkspace(t, workspace)

			identity := identityPart(t, cb, true)
			if !strings.HasPrefix(identity, tt.wantHeading) {
				t.Fatalf("identity heading:\n%s", identity)
			}
			for _, unwanted := range []string{"**Memory**", "memory/MEMORY.md", "Daily Notes", "4. "} {
				if strings.Contains(identity, unwanted) {
					t.Fatalf("identity contains %q with memory off:\n%s", unwanted, identity)
				}
			}
			for _, wanted := range []string{
				"Your workspace is at: " + workspacePath + "\n- Skills: " + workspacePath + "/skills/{skill-name}/SKILL.md\n",
				"3. **Context summaries**",
			} {
				if !strings.Contains(identity, wanted) {
					t.Fatalf("identity lacks %q with memory off:\n%s", wanted, identity)
				}
			}

			messages := cb.BuildMessagesFromPrompt(PromptBuildRequest{CurrentMessage: "hello"})
			for _, unwanted := range []string{"# Memory", identityTestMemoryNote, identityTestDailyNote} {
				if strings.Contains(messages[0].Content, unwanted) {
					t.Fatalf("system prompt contains %q with memory off:\n%s", unwanted, messages[0].Content)
				}
			}
			for _, part := range messages[0].SystemParts {
				if part.PromptSource == string(PromptSourceMemory) {
					t.Fatalf("system parts include memory context with memory off: %#v", part)
				}
			}
		})
	}
}

func TestIdentity_MemoryKeepsDefaultForOtherValues(t *testing.T) {
	for _, value := range []string{"true", "yes", "maybe", "\"\"", "1"} {
		t.Run(value, func(t *testing.T) {
			workspace := identityTestWorkspace(t, "---\nmemory: "+value+"\n---\nBody.")
			prompt := NewContextBuilder(workspace).BuildSystemPrompt()
			if !strings.Contains(prompt, "**Memory**") || !strings.Contains(prompt, identityTestMemoryNote) {
				t.Fatalf("memory: %s turned memory off:\n%s", value, prompt)
			}
		})
	}
}

func TestIdentity_PrivateWorkspace(t *testing.T) {
	workspace := identityTestWorkspace(t, "---\nname: Ledger\nprivateWorkspace: true\n---\nBody.")
	cb := NewContextBuilder(workspace)
	workspacePath := absWorkspace(t, workspace)

	identity := identityPart(t, cb, true)
	for _, unwanted := range []string{"## Workspace", "Your workspace is at", "- Skills:", "Daily Notes"} {
		if strings.Contains(identity, unwanted) {
			t.Fatalf("identity contains %q with a private workspace:\n%s", unwanted, identity)
		}
	}
	// Memory stays on, so its rule still names the memory file.
	if !strings.Contains(identity, "4. **Memory** - When interacting with me if something seems memorable, update "+workspacePath+"/memory/MEMORY.md") {
		t.Fatalf("identity lost the memory rule:\n%s", identity)
	}
	if !strings.Contains(cb.BuildSystemPrompt(), identityTestMemoryNote) {
		t.Fatal("system prompt lost the memory context with a private workspace")
	}
}

func TestIdentity_EmbedderDefinition(t *testing.T) {
	workspace := identityTestWorkspace(t, middenAgentMD)
	cb := NewContextBuilder(workspace)

	want := fmt.Sprintf(`# Midden (powered by Compa %s)

You are Midden, an assistant that turns recorded AI work into findings, articles, presentations and long-form writing, using the midden tool for session evidence. Compa is the agent runtime that powers you.

## Important Rules

1. %s

2. **Be helpful and accurate** - When using tools, briefly explain what you're doing.

3. **Context summaries** - Conversation summaries provided as context are approximate references only. They may be incomplete or outdated. Always defer to explicit user instructions over summary content.
`, config.FormatVersion(), toolUseSystemPromptRule())
	if got := identityPart(t, cb, true); got != want {
		t.Fatalf("identity:\n--- got\n%s\n--- want\n%s", got, want)
	}

	prompt := cb.BuildSystemPromptWithCache()
	for _, unwanted := range []string{absWorkspace(t, workspace), identityTestMemoryNote, "# Memory", "You are Compa"} {
		if strings.Contains(prompt, unwanted) {
			t.Fatalf("system prompt contains %q:\n%s", unwanted, prompt)
		}
	}
	if !strings.Contains(prompt, "This is Midden's private runtime.") {
		t.Fatalf("system prompt lost the AGENT.md body:\n%s", prompt)
	}
}

func TestIdentity_CachedPromptFollowsFrontmatterChanges(t *testing.T) {
	workspace := identityTestWorkspace(t, "# Agent\nBody only.")
	cb := NewContextBuilder(workspace)

	before := cb.BuildSystemPromptWithCache()
	if !strings.HasPrefix(before, "# Compa (") || !strings.Contains(before, identityTestMemoryNote) {
		t.Fatalf("initial cached prompt:\n%s", before)
	}

	agentPath := filepath.Join(workspace, agentDefinitionFile)
	if err := os.WriteFile(agentPath, []byte(middenAgentMD), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(agentPath, future, future); err != nil {
		t.Fatal(err)
	}

	after := cb.BuildSystemPromptWithCache()
	if !strings.HasPrefix(after, "# Midden (powered by Compa ") {
		t.Fatalf("cached prompt kept the old identity:\n%s", after)
	}
	for _, unwanted := range []string{"## Workspace", identityTestMemoryNote, "**Memory**"} {
		if strings.Contains(after, unwanted) {
			t.Fatalf("cached prompt contains %q after the change:\n%s", unwanted, after)
		}
	}
	if fresh := cb.BuildSystemPrompt(); after != fresh {
		t.Fatalf("cached prompt differs from a fresh build:\n--- cached\n%s\n--- fresh\n%s", after, fresh)
	}
}

func TestFrontmatterBool(t *testing.T) {
	tests := []struct {
		raw    any
		want   bool
		wantOK bool
	}{
		{raw: true, want: true, wantOK: true},
		{raw: false, want: false, wantOK: true},
		{raw: "TRUE", want: true, wantOK: true},
		{raw: " yes ", want: true, wantOK: true},
		{raw: "on", want: true, wantOK: true},
		{raw: "false", want: false, wantOK: true},
		{raw: "No", want: false, wantOK: true},
		{raw: "off", want: false, wantOK: true},
		{raw: "maybe"},
		{raw: ""},
		{raw: 0},
		{raw: nil},
	}
	for _, tt := range tests {
		got, ok := frontmatterBool(tt.raw)
		if got != tt.want || ok != tt.wantOK {
			t.Errorf("frontmatterBool(%#v) = %v, %v; want %v, %v", tt.raw, got, ok, tt.want, tt.wantOK)
		}
	}
}

// toolsRejectingProvider rejects every call that offers tools, as a model
// without tool calling does, and answers calls without tools.
type toolsRejectingProvider struct {
	mu         sync.Mutex
	toolCounts []int
}

func (p *toolsRejectingProvider) Chat(
	_ context.Context,
	_ []providers.Message,
	tools []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	p.mu.Lock()
	p.toolCounts = append(p.toolCounts, len(tools))
	p.mu.Unlock()
	if len(tools) > 0 {
		return nil, errors.New("this model does not support tools")
	}
	return &providers.LLMResponse{Content: "answer without tools", FinishReason: "stop"}, nil
}

func (p *toolsRejectingProvider) GetDefaultModel() string { return "no-tools-model" }

func (p *toolsRejectingProvider) calls() []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]int(nil), p.toolCounts...)
}

func identityTestConfig(t *testing.T, workspace string) *config.Config {
	t.Helper()
	return &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         workspace,
				ModelName:         "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 3,
			},
		},
	}
}

func TestRequireTools_TurnFailsWhenTheModelRejectsTools(t *testing.T) {
	entryPoints := map[string]func(al *AgentLoop) (string, error){
		"ProcessDirect": func(al *AgentLoop) (string, error) {
			return al.ProcessDirect(context.Background(), "hello", "require-tools")
		},
		"ProcessDirectWithChannel": func(al *AgentLoop) (string, error) {
			return al.ProcessDirectWithChannel(context.Background(), "hello", "require-tools", "test", "chat1")
		},
	}
	for name, run := range entryPoints {
		t.Run(name, func(t *testing.T) {
			workspace := identityTestWorkspace(t, "---\nname: Ledger\nrequireTools: true\n---\nBody.")
			provider := &toolsRejectingProvider{}
			al := NewAgentLoop(identityTestConfig(t, workspace), bus.NewMessageBus(), provider)
			defer al.Close()
			al.RegisterTool(&echoTextTool{})

			reply, err := run(al)
			if !errors.Is(err, ErrToolsRequired) {
				t.Fatalf("error = %v, want ErrToolsRequired", err)
			}
			if err.Error() != "The selected model doesn't support tool calls, and this agent requires them. Choose a model that supports tool calling." {
				t.Fatalf("error message = %q", err.Error())
			}
			if reply != "" {
				t.Fatalf("reply = %q, want none", reply)
			}
			if calls := provider.calls(); len(calls) != 1 || calls[0] == 0 {
				t.Fatalf("provider calls (tools offered per call) = %v, want one call with tools", calls)
			}
		})
	}
}

func TestRequireTools_OffKeepsTheRetryWithoutTools(t *testing.T) {
	for name, agentMD := range map[string]string{
		"key absent":  "---\nname: Ledger\n---\nBody.",
		"key false":   "---\nname: Ledger\nrequireTools: false\n---\nBody.",
		"no AGENT.md": "",
	} {
		t.Run(name, func(t *testing.T) {
			workspace := identityTestWorkspace(t, agentMD)
			provider := &toolsRejectingProvider{}
			al := NewAgentLoop(identityTestConfig(t, workspace), bus.NewMessageBus(), provider)
			defer al.Close()
			al.RegisterTool(&echoTextTool{})

			reply, err := al.ProcessDirect(context.Background(), "hello", "retry-without-tools")
			if err != nil {
				t.Fatalf("ProcessDirect() error = %v", err)
			}
			if reply != "answer without tools" {
				t.Fatalf("reply = %q, want the answer without tools", reply)
			}
			if calls := provider.calls(); len(calls) != 2 || calls[0] == 0 || calls[1] != 0 {
				t.Fatalf("provider calls (tools offered per call) = %v, want one with tools, then one without", calls)
			}
		})
	}
}
