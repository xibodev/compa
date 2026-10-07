package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xibodev/compa/v3/pkg/bus"
	"github.com/xibodev/compa/v3/pkg/config"
	"github.com/xibodev/compa/v3/pkg/providers"
	"github.com/xibodev/compa/v3/pkg/session"
)

const (
	identityTestMemoryNote = "Synthetic long-term note."
	identityTestDailyNote  = "Synthetic daily note."

	// embedderAgentMD is the AGENT.md of a host program that embeds Compa
	// with an identity, tools and workspace of its own.
	embedderAgentMD = `---
name: Ledger
description: a bookkeeping assistant for small shops, using the ledger tool for the shop's books
tools: [read_file, write_file, edit_file, list_dir, exec, ledger]
memory: false
privateWorkspace: true
requireTools: true
---
This is Ledger's private runtime. The host supplies the tool workspaces.
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

// compaIdentity is Compa's own identity, as it renders without these keys.
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
	workspace := identityTestWorkspace(t, embedderAgentMD)
	cb := NewContextBuilder(workspace)

	want := fmt.Sprintf(`# Ledger (powered by Compa %s)

You are Ledger, a bookkeeping assistant for small shops, using the ledger tool for the shop's books. Compa is the agent runtime that powers you.

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
	if !strings.Contains(prompt, "This is Ledger's private runtime.") {
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
	if err := os.WriteFile(agentPath, []byte(embedderAgentMD), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(agentPath, future, future); err != nil {
		t.Fatal(err)
	}

	after := cb.BuildSystemPromptWithCache()
	if !strings.HasPrefix(after, "# Ledger (powered by Compa ") {
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

// providerShapedError mirrors provider errors from llmgw-core: a generic
// message whose cause carries the upstream (sanitized) reason.
type providerShapedError struct{ cause error }

func (e *providerShapedError) Error() string {
	return "OpenAI-compatible returned HTTP 400 (type=invalid_request_error)"
}
func (e *providerShapedError) Unwrap() error { return e.cause }

type causeToolsRejectingProvider struct{ toolsRejectingProvider }

func (p *causeToolsRejectingProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	options map[string]any,
) (*providers.LLMResponse, error) {
	response, err := p.toolsRejectingProvider.Chat(ctx, messages, tools, model, options)
	if err != nil {
		return nil, &providerShapedError{cause: errors.New("openai-compatible: upstream returned 400: This model does not support tool use.")}
	}
	return response, nil
}

func TestRequireTools_RecognizesTheUpstreamReasonInAnErrorCause(t *testing.T) {
	workspace := identityTestWorkspace(t, "---\nname: Ledger\nrequireTools: true\n---\nBody.")
	provider := &causeToolsRejectingProvider{}
	al := NewAgentLoop(identityTestConfig(t, workspace), bus.NewMessageBus(), provider)
	defer al.Close()
	al.RegisterTool(&echoTextTool{})

	if _, err := al.ProcessDirect(context.Background(), "hello", "require-tools-cause"); !errors.Is(err, ErrToolsRequired) {
		t.Fatalf("error = %v, want ErrToolsRequired", err)
	}
	if calls := provider.calls(); len(calls) != 1 || calls[0] == 0 {
		t.Fatalf("provider calls (tools offered per call) = %v, want one call with tools", calls)
	}
}

// contextThenToolsRejectingProvider rejects its first call's prompt as too
// long, and the tools of every later call.
type contextThenToolsRejectingProvider struct{ toolsRejectingProvider }

func (p *contextThenToolsRejectingProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	options map[string]any,
) (*providers.LLMResponse, error) {
	if len(p.calls()) == 0 {
		p.mu.Lock()
		p.toolCounts = append(p.toolCounts, len(tools))
		p.mu.Unlock()
		return nil, errors.New("context_length_exceeded")
	}
	return p.toolsRejectingProvider.Chat(ctx, messages, tools, model, options)
}

// A turn that compacted the session to retry a too-long prompt, then failed
// because the model rejects tools, puts the history back, as other failed
// retries do.
func TestRequireTools_FailedTurnKeepsTheHistoryARetryCompacted(t *testing.T) {
	workspace := identityTestWorkspace(t, "---\nname: Ledger\nrequireTools: true\n---\nBody.")
	provider := &contextThenToolsRejectingProvider{}
	al := NewAgentLoop(identityTestConfig(t, workspace), bus.NewMessageBus(), provider)
	defer al.Close()
	al.RegisterTool(&echoTextTool{})
	sessions := al.GetRegistry().GetDefaultAgent().Sessions
	sessionKey := session.BuildOpaqueSessionKey("require-tools-compacted")
	sessions.SetHistory(sessionKey, alternatingHistory(6))

	if _, err := al.ProcessDirect(context.Background(), "hello", sessionKey); !errors.Is(err, ErrToolsRequired) {
		t.Fatalf("error = %v, want ErrToolsRequired", err)
	}
	if calls := provider.calls(); !offeredToolsEachTime(calls, 2) {
		t.Fatalf("provider calls (tools offered per call) = %v, want two with tools", calls)
	}
	if got := strings.Join(contents(sessions.GetHistory(sessionKey)), ","); !strings.HasPrefix(got, "q1,a1,q2,a2") {
		t.Fatalf("history = %s, want it whole from q1", got)
	}
	if summary := sessions.GetSummary(sessionKey); summary != "" {
		t.Fatalf("summary = %q, want none", summary)
	}
}

// routeTestModel is an OpenAI-compatible model server. A model without tool
// calling answers a request that offers tools with HTTP 400, as such models do.
type routeTestModel struct {
	name         string
	acceptsTools bool
	mu           sync.Mutex
	toolCounts   []int
}

func (m *routeTestModel) serve(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Tools []json.RawMessage `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("%s: decode request: %v", m.name, err)
		}
		m.mu.Lock()
		m.toolCounts = append(m.toolCounts, len(request.Tools))
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if len(request.Tools) > 0 && !m.acceptsTools {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
				"message": "This model does not support tool use.", "type": "invalid_request_error",
			}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{
			"message":       map[string]any{"role": "assistant", "content": "answer from " + m.name},
			"finish_reason": "stop",
		}}})
	}))
	t.Cleanup(server.Close)
	return server.URL
}

// calls returns how many tools each request offered.
func (m *routeTestModel) calls() []int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]int(nil), m.toolCounts...)
}

// routeTestLoop runs the agent agentMD defines on a route over first, then second.
func routeTestLoop(t *testing.T, agentMD string, first, second *routeTestModel) *AgentLoop {
	t.Helper()
	cfg := newModelTestConfig(t, "chain")
	cfg.Agents.Defaults.Workspace = identityTestWorkspace(t, agentMD)
	cfg.Agents.Defaults.MaxToolIterations = 3
	addHTTPInstance(cfg, "first", first.serve(t), nil)
	addHTTPInstance(cfg, "second", second.serve(t), nil)
	addTestRoute(cfg, "chain", "first/model-a", "second/model-b")
	al := NewAgentLoop(cfg, bus.NewMessageBus(), nil, WithModelResolver(httpModelResolver(map[string][]string{
		"first": {"model-a"}, "second": {"model-b"},
	})))
	t.Cleanup(al.Close)
	al.RegisterTool(&echoTextTool{})
	return al
}

// offeredToolsEachTime reports want calls that each offered tools.
func offeredToolsEachTime(calls []int, want int) bool {
	if len(calls) != want {
		return false
	}
	for _, count := range calls {
		if count == 0 {
			return false
		}
	}
	return true
}

func TestRequireTools_RouteOffersToolsToTheNextCandidate(t *testing.T) {
	first, second := &routeTestModel{name: "first"}, &routeTestModel{name: "second", acceptsTools: true}
	al := routeTestLoop(t, "---\nname: Ledger\nrequireTools: true\n---\nBody.", first, second)

	// A refusal says nothing about the first instance's health, so the
	// second turn tries it again.
	for turn := 1; turn <= 2; turn++ {
		reply, err := al.ProcessDirect(context.Background(), "hello", "route-require-tools")
		if err != nil || reply != "answer from second" {
			t.Fatalf("turn %d: reply = %q, error = %v", turn, reply, err)
		}
	}
	if calls := first.calls(); !offeredToolsEachTime(calls, 2) {
		t.Fatalf("first candidate calls (tools offered per call) = %v, want two with tools", calls)
	}
	if calls := second.calls(); !offeredToolsEachTime(calls, 2) {
		t.Fatalf("second candidate calls (tools offered per call) = %v, want two with tools", calls)
	}
}

func TestRequireTools_RouteFailsWhenNoCandidateTakesTools(t *testing.T) {
	first, second := &routeTestModel{name: "first"}, &routeTestModel{name: "second"}
	al := routeTestLoop(t, "---\nname: Ledger\nrequireTools: true\n---\nBody.", first, second)

	if _, err := al.ProcessDirect(context.Background(), "hello", "route-no-tools"); !errors.Is(err, ErrToolsRequired) {
		t.Fatalf("error = %v, want ErrToolsRequired", err)
	}
	for _, model := range []*routeTestModel{first, second} {
		if calls := model.calls(); !offeredToolsEachTime(calls, 1) {
			t.Fatalf("%s candidate calls (tools offered per call) = %v, want one with tools", model.name, calls)
		}
	}
}

func TestRequireTools_OffKeepsTheRouteRetryWithoutTools(t *testing.T) {
	first, second := &routeTestModel{name: "first"}, &routeTestModel{name: "second", acceptsTools: true}
	al := routeTestLoop(t, "---\nname: Ledger\n---\nBody.", first, second)

	reply, err := al.ProcessDirect(context.Background(), "hello", "route-retry-without-tools")
	if err != nil || reply != "answer from first" {
		t.Fatalf("reply = %q, error = %v", reply, err)
	}
	if calls := first.calls(); len(calls) != 2 || calls[0] == 0 || calls[1] != 0 {
		t.Fatalf("first candidate calls (tools offered per call) = %v, want one with tools, then one without", calls)
	}
	if calls := second.calls(); len(calls) != 0 {
		t.Fatalf("second candidate calls = %v, want none", calls)
	}
}

func TestRequireTools_StreamingRouteOffersToolsToTheNextCandidate(t *testing.T) {
	workspace := identityTestWorkspace(t, "---\nname: Ledger\nrequireTools: true\n---\nBody.")
	first := &instanceStreamingProvider{instanceID: "first", err: errors.New("this model does not support tools")}
	second := &instanceStreamingProvider{instanceID: "second", chunks: []string{"streamed answer"},
		response: &providers.LLMResponse{Content: "streamed answer"}}
	streamer := &instanceStreamingRecorder{}
	al, _ := newInstanceStreamingLoopIn(t, workspace, map[string]*instanceStreamingProvider{"first": first, "second": second}, streamer)

	response, err := al.processMessage(context.Background(), selectedWebMessage("stream-require-tools", "route", "answer"))
	if err != nil || response != "streamed answer" {
		t.Fatalf("response = %q, error = %v", response, err)
	}
	if first.streamCalls.Load() != 1 || second.streamCalls.Load() != 1 || first.chatCalls.Load() != 0 || second.chatCalls.Load() != 0 {
		t.Fatalf("stream/chat calls first=%d/%d second=%d/%d", first.streamCalls.Load(), first.chatCalls.Load(), second.streamCalls.Load(), second.chatCalls.Load())
	}
	if len(streamer.finalized) != 1 || streamer.finalized[0] != "streamed answer" {
		t.Fatalf("finalized = %v", streamer.finalized)
	}
}
