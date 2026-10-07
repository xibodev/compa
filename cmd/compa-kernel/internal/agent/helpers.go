package agent

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ergochat/readline"

	"github.com/xibodev/compa/v3/cmd/compa-kernel/internal"
	"github.com/xibodev/compa/v3/internal/moduleagent"
	"github.com/xibodev/compa/v3/pkg/agent"
	"github.com/xibodev/compa/v3/pkg/bus"
	"github.com/xibodev/compa/v3/pkg/config"
	"github.com/xibodev/compa/v3/pkg/logger"
	"github.com/xibodev/compa/v3/pkg/session"
)

func agentCmd(message, sessionKey, model, workspace string, debug bool) error {
	sessionKey = cliSessionKey(sessionKey)

	cfg, err := internal.LoadConfig()
	if err != nil {
		return fmt.Errorf("error loading config: %w", err)
	}

	if workspace != "" {
		cfg.Agents.Defaults.Workspace = workspace
	}

	logger.ConfigureFromEnv()

	if debug {
		logger.SetLevel(logger.DEBUG)
		fmt.Println("🔍 Debug mode enabled")
	}

	// --model is a model selection — an exact target instance-id/model-id or
	// a model route name — that replaces the default model for this run.
	if model = strings.TrimSpace(model); model != "" {
		if err := config.ValidateSelectionSyntax(model); err != nil {
			return fmt.Errorf("invalid --model: %w", err)
		}
		cfg.Agents.Defaults.ModelName = model
	}

	msgBus := bus.NewMessageBus()
	defer msgBus.Close()
	// Each agent resolves its model selection against the saved provider
	// connections and catalogs through the default model resolver.
	agentLoop := agent.NewAgentLoop(cfg, msgBus, nil,
		// Compa is the composition root that turns installed modules into
		// agent tools. The KERNEL no longer knows the module host exists --
		// see agent.ToolProvider. A standalone product wires its own native
		// provider here instead, or none at all.
		agent.WithToolProviders(
			moduleagent.NewProvider(config.GetHome()),
		),
	)
	defer agentLoop.Close()

	if _, err := agentLoop.DefaultModelStatus(); err != nil {
		// Turns fail with the same message; say it once up front too.
		fmt.Printf("⚠ %v\n", err)
	}

	// Print agent startup info (only for interactive mode)
	startupInfo := agentLoop.GetStartupInfo()
	toolsInfo, ok := startupInfo["tools"].(map[string]any)
	if !ok {
		toolsInfo = nil
	}
	skillsInfo, ok := startupInfo["skills"].(map[string]any)
	if !ok {
		skillsInfo = nil
	}
	logFields := map[string]any{}
	if toolsInfo != nil {
		logFields["tools_count"] = toolsInfo["count"]
	}
	if skillsInfo != nil {
		logFields["skills_total"] = skillsInfo["total"]
		logFields["skills_available"] = skillsInfo["available"]
	}
	logger.InfoCF("agent", "Agent initialized", logFields)

	if message != "" {
		ctx := context.Background()
		response, err := agentLoop.ProcessDirect(ctx, message, sessionKey)
		if err != nil {
			return fmt.Errorf("error processing message: %w", err)
		}
		fmt.Printf("\n%s %s\n", internal.Logo, response)
		return nil
	}

	fmt.Printf("%s Interactive mode (Ctrl+C to exit)\n\n", internal.Logo)
	interactiveMode(agentLoop, msgBus, sessionKey)

	return nil
}

// cliHistoryFile returns the interactive history file: private to the user,
// under the Compa home (a shared temp folder would expose it to other users).
// It is created 0600 first, because readline would create it world-readable.
// "" disables history when the file can't be prepared.
func cliHistoryFile(home string) string {
	if strings.TrimSpace(home) == "" {
		return ""
	}
	path := filepath.Join(home, "state", "cli_history")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return ""
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return ""
	}
	_ = f.Close()
	_ = os.Chmod(path, 0o600)
	return path
}

// startTerminalChat returns the chat that runs the terminal's turns and prints
// to out, printing what the agent posts in the chat until the bus closes. As
// the terminal then shows them, the owner's approvals are asked there.
func startTerminalChat(agentLoop *agent.AgentLoop, msgBus *bus.MessageBus, sessionKey string, out io.Writer) *terminalChat {
	chat := newTerminalChat(func(input string) (string, error) {
		return agentLoop.ProcessDirect(context.Background(), input, sessionKey)
	}, out)
	go chat.printPosted(context.Background(), msgBus.OutboundChan())
	agentLoop.SetTerminalChat(true)
	return chat
}

func interactiveMode(agentLoop *agent.AgentLoop, msgBus *bus.MessageBus, sessionKey string) {
	prompt := fmt.Sprintf("%s You: ", internal.Logo)

	rl, err := readline.NewEx(&readline.Config{
		Prompt:          prompt,
		HistoryFile:     cliHistoryFile(config.GetHome()),
		HistoryLimit:    100,
		InterruptPrompt: "^C",
		EOFPrompt:       "exit",
	})
	if err != nil {
		fmt.Printf("Error initializing readline: %v\n", err)
		fmt.Println("Falling back to simple input mode...")
		simpleInteractiveMode(agentLoop, msgBus, sessionKey)
		return
	}
	defer rl.Close()

	// Writing through readline keeps a prompt on screen intact.
	chat := startTerminalChat(agentLoop, msgBus, sessionKey, rl)

	for {
		line, err := rl.Readline()
		if err != nil {
			if err == readline.ErrInterrupt || err == io.EOF {
				fmt.Println("\nGoodbye!")
				return
			}
			fmt.Printf("Error reading input: %v\n", err)
			continue
		}

		input := strings.TrimSpace(line)
		if input == "" {
			continue
		}

		if input == "exit" || input == "quit" {
			fmt.Println("Goodbye!")
			return
		}

		chat.handle(input)
	}
}

func simpleInteractiveMode(agentLoop *agent.AgentLoop, msgBus *bus.MessageBus, sessionKey string) {
	chat := startTerminalChat(agentLoop, msgBus, sessionKey, os.Stdout)
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Print(fmt.Sprintf("%s You: ", internal.Logo))
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				fmt.Println("\nGoodbye!")
				return
			}
			fmt.Printf("Error reading input: %v\n", err)
			continue
		}

		input := strings.TrimSpace(line)
		if input == "" {
			continue
		}

		if input == "exit" || input == "quit" {
			fmt.Println("Goodbye!")
			return
		}

		chat.handle(input)
	}
}

// cliSessionKey maps the --session flag onto an opaque session key, the only
// kind the agent loop honours from a caller. A value that is already opaque
// (for example a session ID copied from the web UI) is used as-is.
func cliSessionKey(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "cli:default"
	}
	if session.IsOpaqueSessionKey(name) {
		return name
	}
	return session.BuildOpaqueSessionKey(name)
}
