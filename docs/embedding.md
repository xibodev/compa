# Embed the Go runtime

`github.com/xibodev/compa/v4/pkg/agent` is the agent runtime Compa runs: the
agent loop with its tools, skills, sessions and model resolution. You can run
it inside your own Go program, with no web UI and no second process. It needs
Go 1.26.9 or later.

```sh
go get github.com/xibodev/compa/v4
```

## A minimal program

This runs one turn on the models connected in Compa. It is the body of
`ExampleNewAgentLoop` in
[`pkg/agent/example_embed_test.go`](../pkg/agent/example_embed_test.go), which
`go vet` and `go test` compile.

```go
package main

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/xibodev/compa/v4/pkg/agent"
	"github.com/xibodev/compa/v4/pkg/bus"
	"github.com/xibodev/compa/v4/pkg/config"
	"github.com/xibodev/compa/v4/pkg/session"
)

func main() {
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
```

`NewAgentLoop(cfg, msgBus, provider, opts...)` builds the loop;
`ProcessDirect(ctx, content, sessionKey)` runs one turn, tools included, and
returns the reply text. Call `Close` when you're done.

## How the model is chosen

A turn runs on a model selection, which is either:

- an exact target, `instance-id/model-id`: one model of one provider
  connection. The instance ID is the connection's name, such as `openai`; or
- the name of a model route, which tries its models in order.

The loop uses `cfg.Agents.Defaults.ModelName`, the default model set on
Compa's Models page or with `compa-kernel model`. Set that field before
`NewAgentLoop` to use another selection.

With a nil provider, selections resolve against the config's provider
connections and routes (`provider_instances`, `model_routes`), the model lists
Compa saved in `model_catalogs.json`, and the keys in `auth.json`, all in the
same Compa home; an exact target's model must be in its connection's saved
list. Connecting providers in the web UI, or with `compa-kernel auth login` or
`compa-kernel model auto-free`, fills these in. `DefaultModelStatus` reports
early when the selection doesn't resolve; otherwise the turn fails with the
same error.

To bring your own model instead, pass a `providers.LLMProvider` (the
`Chat` and `GetDefaultModel` methods from `pkg/providers`) as the third
argument. It then answers the default selection directly, without resolution.

## Where the config comes from

`config.LoadConfig(path)` reads `config.json`, the secrets in `.security.yml`
beside it, and `COMPA_*` environment overrides; when the file doesn't exist,
it starts from `config.DefaultConfig()` and adds the other two.
`config.GetHome()` is `$COMPA_HOME`, or `~/.compa`. You can also build the
config in code: start from `config.DefaultConfig()` and change its fields.

The loop creates the workspace folder, `cfg.Agents.Defaults.Workspace`
(`~/.compa/workspace` by default), if it's missing, keeps sessions and memory
there, and runs file tools in it. The built-in tools follow `cfg.Tools` and
`cfg.Agents.Defaults.RestrictToWorkspace`, the same settings Compa uses.
Messages the agent sends on its own, with the `message` tool, go to the bus;
read `msgBus.OutboundChan()` if your program should show them.

Tool calls follow the approval policy, `cfg.Tools.Approval`, with the origin
`cli`. A call the policy asks about, by default installing a skill or a module
capability with an unknown cost, network access or external writes, goes to
the `agent.ToolApprover` hooks you mount with
`loop.MountHook(agent.NamedHook(name, hook))`; with none, it is refused.

## Your own tools

A tool is four methods: `Name`, `Description`, `Parameters` (a JSON Schema)
and `Execute`. Hand tools to the loop with `agent.WithToolProviders`. This is
`ExampleWithToolProviders` from the same file:

```go
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
```

```go
loop := agent.NewAgentLoop(cfg, msgBus, nil, agent.WithToolProviders(hostTools{}))
```

`tools` is `github.com/xibodev/compa/v4/pkg/tools`.

## Agent definition keys for embedders

The agent reads `AGENT.md` in its workspace: YAML frontmatter, then the prompt
body. These frontmatter keys let a host program replace Compa's own identity
and kernel instructions. Without them, nothing changes.

| Key | Type, default | Effect |
|---|---|---|
| `name` | string | The identity at the top of the system prompt becomes `# <name> (powered by Compa <version>)` and `You are <name>, <description>. Compa is the agent runtime that powers you.` It mentions spoken replies only when voice is enabled with a text-to-speech target. Without `name`, or with `name: compa`, the agent keeps Compa's own identity. |
| `description` | string | Completes the identity sentence. |
| `memory` | bool, `true` | `false` leaves out the memory rule, the memory and daily-notes paths, and the memory context. |
| `privateWorkspace` | bool, `false` | `true` leaves out the `## Workspace` section with the workspace paths. Describe your own tool workspace with a prompt contributor (`ContextBuilder.RegisterPromptContributor`). |
| `requireTools` | bool, `false` | `true`: when the model rejects tool calls, the turn fails with `agent.ErrToolsRequired` instead of retrying without tools. On a model route, a target that rejects tools passes the turn to the next target, with the same tools; the turn fails only when no target accepts them. |

```yaml
---
name: Ledger
description: a bookkeeping assistant for small shops
memory: false
privateWorkspace: true
requireTools: true
---
Instructions for the agent.
```

Check for `agent.ErrToolsRequired` with `errors.Is` on the error
`ProcessDirect` returns. The system prompt is cached and rebuilt when
`AGENT.md` changes; `requireTools`, like `tools` and `model`, is read when the
loop creates the agent.

## More

- [`pkg/agent/example_embed_test.go`](../pkg/agent/example_embed_test.go): the
  examples above.
- [`pkg/agent/external_consumer_test.go`](../pkg/agent/external_consumer_test.go):
  the tool interfaces used from outside the package.
- [`pkg/agent/artifact_event_test.go`](../pkg/agent/artifact_event_test.go): a
  loop with an injected provider and a runtime event bus
  (`agent.WithRuntimeEvents`).
- [`pkg/agent/kernel_boundary_test.go`](../pkg/agent/kernel_boundary_test.go):
  checks that `pkg/agent` imports nothing from `internal/`, `web/` or `cmd/`,
  so other modules can import it.
- [`cmd/compa-kernel/internal/agent/helpers.go`](../cmd/compa-kernel/internal/agent/helpers.go):
  how `compa-kernel agent` wires the same pieces.
