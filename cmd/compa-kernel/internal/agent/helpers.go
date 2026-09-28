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

	"github.com/xibodev/compa/cmd/compa-kernel/internal"
	"github.com/xibodev/compa/internal/moduleagent"
	"github.com/xibodev/compa/pkg/agent"
	"github.com/xibodev/compa/pkg/bus"
	"github.com/xibodev/compa/pkg/config"
	"github.com/xibodev/compa/pkg/logger"
	"github.com/xibodev/compa/pkg/session"
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
	interactiveMode(agentLoop, sessionKey)

	return nil
}

func interactiveMode(agentLoop *agent.AgentLoop, sessionKey string) {
	prompt := fmt.Sprintf("%s You: ", internal.Logo)

	rl, err := readline.NewEx(&readline.Config{
		Prompt:          prompt,
		HistoryFile:     filepath.Join(os.TempDir(), ".compa_history"),
		HistoryLimit:    100,
		InterruptPrompt: "^C",
		EOFPrompt:       "exit",
	})
	if err != nil {
		fmt.Printf("Error initializing readline: %v\n", err)
		fmt.Println("Falling back to simple input mode...")
		simpleInteractiveMode(agentLoop, sessionKey)
		return
	}
	defer rl.Close()

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

		ctx := context.Background()
		response, err := agentLoop.ProcessDirect(ctx, input, sessionKey)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			continue
		}

		fmt.Printf("\n%s %s\n\n", internal.Logo, response)
	}
}

func simpleInteractiveMode(agentLoop *agent.AgentLoop, sessionKey string) {
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

		ctx := context.Background()
		response, err := agentLoop.ProcessDirect(ctx, input, sessionKey)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			continue
		}

		fmt.Printf("\n%s %s\n\n", internal.Logo, response)
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
