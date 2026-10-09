package agent_test

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/xibodev/compa/v4/pkg/agent"
	"github.com/xibodev/compa/v4/pkg/bus"
	"github.com/xibodev/compa/v4/pkg/config"
	"github.com/xibodev/compa/v4/pkg/session"
	"github.com/xibodev/compa/v4/pkg/tools"
)

// These examples have no Output comment on purpose: `go test` compiles them
// but never runs them, because a real turn calls a model provider.
// docs/embedding.md shows the same code; keep the two in sync.

// ExampleNewAgentLoop runs one turn on the models connected in Compa.
func ExampleNewAgentLoop() {
	// The config the Compa web UI saves: $COMPA_HOME/config.json, by default
	// ~/.compa/config.json. A missing file loads the defaults, which connect
	// no provider.
	cfg, err := config.LoadConfig(filepath.Join(config.GetHome(), "config.json"))
	if err != nil {
		fmt.Println("load config:", err)
		return
	}

	// Optional: run on another model than the default model.
	// cfg.Agents.Defaults.ModelName = "openai/gpt-5.4" // exact target: instance-id/model-id
	// cfg.Agents.Defaults.ModelName = "everyday"       // or the name of a model route

	msgBus := bus.NewMessageBus()
	defer msgBus.Close()

	// A nil provider resolves the model selection against the providers
	// connected in Compa.
	loop := agent.NewAgentLoop(cfg, msgBus, nil)
	defer loop.Close()

	if _, err := loop.DefaultModelStatus(); err != nil {
		fmt.Println(err) // for example: no default model is selected
		return
	}

	// Turns with the same key share one conversation. The loop keeps a key
	// the caller supplies only when it is opaque, so build it with
	// session.BuildOpaqueSessionKey.
	sessionKey := session.BuildOpaqueSessionKey("my-app:demo")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	reply, err := loop.ProcessDirect(ctx, "Say hello in five words.", sessionKey)
	if err != nil {
		fmt.Println("turn failed:", err)
		return
	}
	fmt.Println(reply)
}

// hostClock is a tool the host program provides. The agent calls it like a
// built-in tool.
type hostClock struct{}

func (hostClock) Name() string        { return "host_time" }
func (hostClock) Description() string { return "Returns the current date and time on this computer." }
func (hostClock) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

func (hostClock) Execute(context.Context, map[string]any) *tools.ToolResult {
	return tools.NewToolResult(time.Now().Format(time.RFC1123))
}

// hostTools gives every agent of the loop the host's tools.
type hostTools struct{}

func (hostTools) RegisterTools(
	workspace string,
	register func(agent.Tool),
) ([]string, func(string) (string, string, []string)) {
	register(hostClock{})
	return nil, nil // no module summaries and no knowledge loader
}

// ExampleWithToolProviders adds the host program's own tools to the agent.
func ExampleWithToolProviders() {
	cfg, err := config.LoadConfig(filepath.Join(config.GetHome(), "config.json"))
	if err != nil {
		fmt.Println("load config:", err)
		return
	}

	msgBus := bus.NewMessageBus()
	defer msgBus.Close()

	loop := agent.NewAgentLoop(cfg, msgBus, nil, agent.WithToolProviders(hostTools{}))
	defer loop.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	reply, err := loop.ProcessDirect(ctx, "What time is it?", session.BuildOpaqueSessionKey("my-app:demo"))
	if err != nil {
		fmt.Println("turn failed:", err)
		return
	}
	fmt.Println(reply)
}
