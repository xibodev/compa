package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xibodev/compa/v3/pkg/bus"
	"github.com/xibodev/compa/v3/pkg/config"
	"github.com/xibodev/compa/v3/pkg/tools"
)

const clearedReply = "Chat history cleared!"

func newCommandAccessLoop(t *testing.T, ownerOnly bool) (*AgentLoop, *bus.MessageBus) {
	t.Helper()
	cfg := &config.Config{
		Agents: config.AgentsConfig{Defaults: config.AgentDefaults{
			Workspace: t.TempDir(), ModelName: "test-model", MaxTokens: 4096, MaxToolIterations: 3,
		}},
		Commands: config.CommandsConfig{OwnerOnly: ownerOnly},
	}
	msgBus := bus.NewMessageBus()
	al := NewAgentLoop(cfg, msgBus, &simpleMockProvider{response: "model answer"})
	t.Cleanup(al.Close)
	return al, msgBus
}

func chatMessage(channel, content string, owner bool) bus.InboundMessage {
	return bus.InboundMessage{
		Context: bus.InboundContext{
			Channel: channel, ChatID: "chat-1", ChatType: "direct", SenderID: "someone", SenderIsOwner: owner,
		},
		Channel: channel, ChatID: "chat-1", SenderID: "someone", Content: content,
	}
}

func TestOwnerOnlyCommandsRunOnlyForTheOwner(t *testing.T) {
	cases := []struct {
		name      string
		ownerOnly bool
		msg       bus.InboundMessage
		want      string
	}{
		{"chat app sender who is not the owner", true, chatMessage("telegram", "/clear", false), "model answer"},
		{"chat app owner", true, chatMessage("telegram", "/clear", true), clearedReply},
		{"web UI", true, chatMessage("web", "/clear", false), clearedReply},
		{"terminal", true, chatMessage("cli", "/clear", false), clearedReply},
		{"anyone when owner_only is off", false, chatMessage("telegram", "/clear", false), clearedReply},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			al, _ := newCommandAccessLoop(t, tc.ownerOnly)
			got, err := al.processMessage(context.Background(), tc.msg)
			if err != nil || got != tc.want {
				t.Fatalf("processMessage() = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestScheduledTurnsNeverRunCommands(t *testing.T) {
	al, _ := newCommandAccessLoop(t, false)

	// A cron job replays its stored message: "/clear" is text, every run.
	got, err := al.ProcessDirectWithChannel(context.Background(), "/clear", "cron-session", "cli", "direct")
	if err != nil || got != "model answer" {
		t.Fatalf("scheduled turn = %q, %v; want the command treated as text", got, err)
	}
	// The terminal's own turns still run them.
	got, err = al.ProcessDirect(context.Background(), "/clear", "cli-session")
	if err != nil || got != clearedReply {
		t.Fatalf("terminal turn = %q, %v; want the command run", got, err)
	}
}

func TestCommandsDuringAnActiveTurnRunInsteadOfSteering(t *testing.T) {
	al, msgBus := newCommandAccessLoop(t, true)
	ctx := context.Background()

	owner := chatMessage("telegram", "/clear", true)
	sessionKey, _, ok := al.resolveSteeringTarget(owner)
	if !ok {
		t.Fatal("expected a routable message")
	}
	al.activeTurnStates.Store(sessionKey, &turnState{turnID: "active-turn", phase: TurnPhaseTools})

	if !al.dispatchCommandDuringTurn(ctx, owner, sessionKey) {
		t.Fatal("/clear from the owner during a turn must run, not steer the turn")
	}
	select {
	case out := <-msgBus.OutboundChan():
		if out.Content != clearedReply || out.Context.Channel != "telegram" || out.Context.ChatID != "chat-1" {
			t.Fatalf("reply = %+v", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the command's reply")
	}

	for _, msg := range []bus.InboundMessage{
		chatMessage("telegram", "/clear", false),                // not the owner: text for the model
		chatMessage("telegram", "please also check x", true),    // plain steering
		chatMessage("telegram", "/use someskill do this", true), // starts a turn of its own
		chatMessage("telegram", "/reload", true),                // not a command for this moment
	} {
		if al.dispatchCommandDuringTurn(ctx, msg, sessionKey) {
			t.Fatalf("%q must join the active turn as steering", msg.Content)
		}
	}
}

func TestSteeringQueueFullIsReported(t *testing.T) {
	sq := newSteeringQueue(SteeringOneAtATime)
	for range MaxQueueSize {
		if err := sq.pushScope("s", userPromptMessage("x", nil)); err != nil {
			t.Fatal(err)
		}
	}
	if err := sq.pushScope("s", userPromptMessage("x", nil)); !errors.Is(err, errSteeringQueueFull) {
		t.Fatalf("pushScope() error = %v, want errSteeringQueueFull", err)
	}
}

func TestRunKeepsProcessingWhenHooksAndMCPFailToStart(t *testing.T) {
	cfg := &config.Config{
		Agents: config.AgentsConfig{Defaults: config.AgentDefaults{
			Workspace: t.TempDir(), ModelName: "test-model", MaxTokens: 4096, MaxToolIterations: 3,
		}},
		Tools: config.ToolsConfig{MCP: config.MCPConfig{
			ToolConfig: config.ToolConfig{Enabled: true},
			Servers: map[string]config.MCPServerConfig{
				"broken": {Enabled: true, Command: "compa-command-that-does-not-exist-for-mcp-tests"},
			},
		}},
		Hooks: config.HooksConfig{Enabled: true, Processes: map[string]config.ProcessHookConfig{
			"guard": {Enabled: true, Command: []string{"compa-hook-that-does-not-exist"}, Intercept: []string{"approve_tool"}},
		}},
	}
	msgBus := bus.NewMessageBus()
	al := NewAgentLoop(cfg, msgBus, &simpleMockProvider{response: "still answering"})
	defer al.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- al.Run(ctx) }()

	if err := msgBus.PublishInbound(ctx, chatMessage("web", "hello", false)); err != nil {
		t.Fatal(err)
	}
	select {
	case out := <-msgBus.OutboundChan():
		if out.Content != "still answering" {
			t.Fatalf("outbound = %+v", out)
		}
	case err := <-runDone:
		t.Fatalf("Run returned %v before processing the message", err)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out: the message was not processed")
	}

	// Both failures are recorded; the MCP servers connect in the background.
	deadline := time.Now().Add(10 * time.Second)
	for al.hookRuntime.getInitErr() == nil || al.mcp.getInitErr() == nil {
		if time.Now().After(deadline) {
			t.Fatalf("init errors: hooks %v, MCP %v; want both failures",
				al.hookRuntime.getInitErr(), al.mcp.getInitErr())
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The approval hook that did not start fails closed.
	decision, decided := al.hooks.ApproveTool(ctx, &ToolApprovalRequest{Tool: "exec"})
	if !decided || decision.Approved || !strings.Contains(decision.Reason, "guard") {
		t.Fatalf("ApproveTool() = %+v, %v; want a denial naming the hook", decision, decided)
	}

	cancel()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context ended")
	}
}

func TestNewProcessHookGivesUpWhenHelloIsNotAnswered(t *testing.T) {
	previous := processHookHelloTimeout
	processHookHelloTimeout = 300 * time.Millisecond
	defer func() { processHookHelloTimeout = previous }()

	start := time.Now()
	_, err := NewProcessHook(context.Background(), "silent", ProcessHookOptions{
		Command: processHookHelperCommand(),
		Env:     processHookHelperEnv("silent", ""),
		Observe: true,
	})
	if err == nil || !strings.Contains(err.Error(), "did not answer hello") {
		t.Fatalf("NewProcessHook() error = %v, want a hello timeout", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("NewProcessHook took %v", elapsed)
	}
}

func TestSharedToolsFollowMessageTargets(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Tools.Message.Targets = config.MessageTargetsCurrentChat
	al := NewAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{})
	defer al.Close()

	tool, ok := al.GetRegistry().GetDefaultAgent().Tools.Get("message")
	if !ok {
		t.Fatal("expected the message tool")
	}
	ctx := tools.WithToolContext(context.Background(), "telegram", "chat-1")
	result := tool.Execute(ctx, map[string]any{"content": "hi", "channel": "telegram", "chat_id": "someone-else"})
	if result == nil || !result.IsError {
		t.Fatalf("message to another chat = %+v, want it refused under current_chat", result)
	}
}

type shadowingToolProvider struct{}

func (shadowingToolProvider) RegisterTools(_ string, register func(Tool)) ([]string, func(string) (string, string, []string)) {
	register(&allowlistTestTool{name: "read_file"})
	register(&allowlistTestTool{name: "module_only_tool"})
	return nil, nil
}

func TestModuleToolsCannotReplaceBuiltInTools(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	al := NewAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{}, WithToolProviders(shadowingToolProvider{}))
	defer al.Close()

	agentTools := al.GetRegistry().GetDefaultAgent().Tools
	if tool, ok := agentTools.Get("read_file"); !ok {
		t.Fatal("expected read_file")
	} else if _, shadowed := tool.(*allowlistTestTool); shadowed {
		t.Fatal("a module tool replaced the built-in read_file")
	}
	if _, ok := agentTools.Get("module_only_tool"); !ok {
		t.Fatal("a module tool with its own name must register")
	}
}

func TestEvolutionBridgeLearnsOnlyFromTheOwner(t *testing.T) {
	tmpDir := t.TempDir()
	al := newEvolutionTestLoop(t, tmpDir, config.EvolutionConfig{Enabled: true, Mode: "observe"},
		&simpleMockProvider{response: "ok"})
	defer al.Close()
	bridge := al.currentEvolutionBridge()
	if bridge == nil {
		t.Fatal("expected an evolution bridge")
	}

	payload := TurnEndPayload{Status: TurnEndStatusCompleted, Workspace: tmpDir, UserMessage: "hi", FinalContent: "ok"}
	if bridge.handleTurnEndAsync(HookMeta{AgentID: "main", TurnID: "t1", SessionKey: "s1"}, payload) {
		t.Fatal("a turn from someone other than the owner must not be recorded")
	}
	payload.FromOwner = true
	if !bridge.handleTurnEndAsync(HookMeta{AgentID: "main", TurnID: "t2", SessionKey: "s2"}, payload) {
		t.Fatal("the owner's turn must be recorded")
	}

	if inboundFromOwner(&bus.InboundContext{Channel: "telegram"}) ||
		!inboundFromOwner(&bus.InboundContext{Channel: "telegram", SenderIsOwner: true}) ||
		!inboundFromOwner(&bus.InboundContext{Channel: "web"}) ||
		!inboundFromOwner(&bus.InboundContext{Channel: "cli"}) {
		t.Fatal("inboundFromOwner disagrees with the owner definition")
	}
}

func TestAgentWorkspaceExpandsWindowsStyleHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	got := resolveAgentWorkspace(&config.AgentConfig{Workspace: `~\work`}, &config.AgentDefaults{})
	if got != filepath.Join(home, "work") {
		t.Fatalf("workspace = %q, want %q (never the bare home directory)", got, filepath.Join(home, "work"))
	}
}

func TestBuiltinSkillsDoNotComeFromTheWorkingDirectory(t *testing.T) {
	t.Setenv(config.EnvBuiltinSkills, "")
	cwd := t.TempDir()
	skillDir := filepath.Join(cwd, "skills", "planted")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: planted\ndescription: planted\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwd)

	cb := NewContextBuilder(t.TempDir())
	for _, root := range cb.skillRoots() {
		if filepath.Clean(root) == filepath.Join(cwd, "skills") {
			t.Fatalf("skill roots %v include the working directory's skills folder", cb.skillRoots())
		}
	}
	for _, name := range cb.ListSkillNames() {
		if name == "planted" {
			t.Fatal("a skill from the working directory joined the built-ins")
		}
	}
}
