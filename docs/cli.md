# Command line

Compa installs two programs:

- `compa` serves the web UI and runs the gateway for you.
- `compa-kernel` is the agent runtime. The web UI runs it as
  `compa-kernel gateway`, and it also works on its own in a terminal.

Both keep their state in `~/.compa`. Set `COMPA_HOME` to use another folder,
or `COMPA_CONFIG` to use another config file.

## compa

```text
compa [options] [config.json]
```

| Option | What it does |
|---|---|
| `-port <port>` | Port to listen on (default 18800). |
| `-host <host>` | Host to listen on; overrides `-public`. |
| `-public` | Listen on all interfaces instead of localhost only. Read [LAN access](install.md#lan-access) first. |
| `-no-browser` | Don't open the browser on startup. |
| `-console` | Run in the terminal, without the tray icon. |
| `-lang en`, `-lang zh` | Language of the tray menu (default: from the system locale). |
| `-password <password>` | Set the dashboard password (at least 8 characters) and exit. |
| `-d`, `-debug` | Debug logging. |

`config.json` is the configuration file, `~/.compa/config.json` by default.
When you name one and `COMPA_HOME` isn't set, Compa keeps its other state in
that file's folder. `-port` and `-public` override the **Service Port** and
**Enable LAN Access** settings on the Config page for that run.

## compa-kernel

```text
compa-kernel <command> [flags]
```

Every command has `--help`; `--no-color` turns colors off.

| Command | What it does |
|---|---|
| `agent` | Interact with the agent directly. |
| `auth` | Manage authentication (login, logout, status). |
| `completion` | Generate the autocompletion script for the specified shell. |
| `config` | Manage configuration. |
| `cron` | Manage scheduled tasks. |
| `gateway` | Start Compa gateway. |
| `mcp` | Manage MCP server configuration. |
| `model` | Show or change the default model. |
| `module-invoke` | Invoke a module capability as a bounded detached process. |
| `modules` | List installed modules and their capabilities. |
| `modules-add` | Install a detached module from a local binary. |
| `modules-disable` | Disable an installed module without removing its files or state. |
| `modules-enable` | Enable an installed module without removing its files or state. |
| `modules-remove` | Remove an installed module (its state is left intact). |
| `onboard` | Initialize Compa configuration and workspace. |
| `skills` | Manage skills. |
| `status` | Show Compa status. |
| `update` | Update compa and compa-kernel from the latest GitHub release. |
| `version` | Show version information. |

### Chat in a terminal

```sh
compa-kernel onboard                  # first time only: create ~/.compa/config.json and the workspace
compa-kernel model auto-free          # add free providers that answer (like "Try free providers")
compa-kernel agent -m "Hello!"        # send one message and print the reply
compa-kernel agent                    # chat interactively; type exit to quit
```

To use a provider with an API key instead, run
`compa-kernel auth login --provider openai` (or `anthropic`) and paste the key
when asked; other providers are connected on the Models page.

### agent

| Flag | What it does |
|---|---|
| `-m`, `--message <text>` | Send a single message (non-interactive mode). |
| `--model <selection>` | Model for this run: an exact target `instance-id/model-id` or a model route name. |
| `-s`, `--session <key>` | Session key (default `cli:default`). |
| `-w`, `--workspace <dir>`, `-C`, `--dir <dir>` | Working directory / workspace. |
| `-d`, `--debug` | Debug logging. |

### model

| Command | What it does |
|---|---|
| `model` | Show the default model and the choices. |
| `model <selection>` | Set the default model: `instance-id/model-id` or a route name. |
| `model --clear` | Clear the default model. |
| `model auto-free` | Discover and configure working zero-key free model providers. |
| `model ping [instance-id]` | Probe reachability and latency of provider instances. |
| `model roster` | List all curated providers supported by Compa. |

### auth

| Command | What it does |
|---|---|
| `auth login -p <openai\|anthropic>` | Store an API key for a provider. |
| `auth logout [-p <provider>]` | Remove stored credentials (OpenAI and Anthropic without `-p`). |
| `auth status` | Show current auth status. |
| `auth weixin` | Connect a WeChat personal account via QR code. |
| `auth wecom` | Scan a WeCom QR code and configure `channels.wecom`. |

### gateway

`compa-kernel gateway` runs chat, tools, channels and scheduled jobs in the
foreground. Compa starts it for you, so run it yourself only when Compa isn't
running.

| Flag | What it does |
|---|---|
| `-E`, `--allow-empty` | Continue starting even when no default model is configured. |
| `--host <host>` | Host address for gateway binding (overrides `gateway.host` for this run). |
| `-d`, `--debug` | Debug logging. |
| `-T`, `--no-truncate` | Disable string truncation in debug logs (with `-d`). |

### cron

Scheduled jobs run while the gateway runs.

| Command | What it does |
|---|---|
| `cron list` | List all scheduled jobs. |
| `cron add -n <name> -m <message> -c '<cron expression>'` | Add a job, such as `-c '0 9 * * *'`; `-e <seconds>` repeats every N seconds instead; `--channel` and `--to` choose where the result goes. |
| `cron enable <id>`, `cron disable <id>` | Turn a job on or off. |
| `cron remove <id>` | Remove a job by ID. |

### skills

| Command | What it does |
|---|---|
| `skills list` | List installed skills. |
| `skills search [query]` | Search available skills. |
| `skills install <owner/repo/path>` | Install a skill from GitHub. |
| `skills install --registry <name> <slug>` | Install a skill from a registry, such as `clawhub`. |
| `skills show <name>` | Show skill details. |
| `skills remove <name>` | Remove installed skill. |

### mcp

| Command | What it does |
|---|---|
| `mcp list [--status]` | List configured MCP servers. |
| `mcp add <name> <command-or-url> [args...]` | Add or update an MCP server (`-t stdio`, `http` or `sse`; `-e KEY=value`; `-H 'Name: Value'`). |
| `mcp show <name>` | Show details and tools for a configured MCP server. |
| `mcp test <name>` | Test connectivity for a configured MCP server. |
| `mcp remove <name>` | Remove an MCP server from config. |
| `mcp edit` | Open the Compa config in `$EDITOR`. |

### Modules

| Command | What it does |
|---|---|
| `modules` | List installed modules and their capabilities. |
| `modules-add <path>` | Install a module from a local program. |
| `modules-enable <id>`, `modules-disable <id>` | Offer a module's capabilities to the agent, or stop offering them. |
| `modules-remove <id>` | Remove a module; its state is left intact. |
| `module-invoke <module> <capability> [json]` | Run one capability; `--source-root name=path` grants a folder or file read-only. |

### Other commands

| Command | What it does |
|---|---|
| `onboard [--enc]` | Initialize Compa configuration and workspace; `--enc` enables credential encryption (generates an SSH key and prompts for a passphrase). Run again, it keeps `config.json` but rewrites the workspace's template files, including `memory/MEMORY.md`. |
| `config reset [-f]` | Reset configuration to factory defaults, after backing it up. |
| `status` | Show the config, workspace and model in use. |
| `update [--version v1.2.3]` | Install a release of `compa` and `compa-kernel`; restart Compa afterwards. |
| `version` | Show version information. |
