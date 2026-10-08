# Command line

Compa installs two programs:

- `compa` serves the web UI and runs the gateway for you.
- `compa-kernel` is the agent runtime. The web UI runs it as
  `compa-kernel gateway`, and it also works on its own in a terminal.

Both keep their state in `~/.compa`. Set `COMPA_HOME` to use another folder,
or `COMPA_CONFIG` to use another config file; other variables are under
[Environment variables](#environment-variables).

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
| `-lang en`, `-lang zh` | Language of the tray menu (default: from `LANG`, else English). |
| `-password <password>` | Set the dashboard password (at least 8 characters) and exit. `-password -` asks for it, or reads a line piped to it, which keeps it out of your shell history (macOS and Linux). |
| `-d`, `-debug` | Debug logging. |

`config.json` is the configuration file, `~/.compa/config.json` by default.
When you name one and `COMPA_HOME` isn't set, Compa keeps its other state in
that file's folder. `-port` and `-public` override the **Service Port** and
**Enable LAN Access** settings on the Config page for that run. On Windows,
the released `compa.exe` has no console window of its own, so PowerShell gives
the prompt back at once: with `-console` or `-password`, Compa's output
follows it.

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
| `evolution` | Review the skill changes evolution proposes. |
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

Interactive `compa-kernel agent` asks you for [approvals](use.md#approvals) in
the terminal; its calls have the origin `cli`. With `-m` the terminal can't
ask, so a call the policy asks about is refused unless an approver hook
decides it.

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
| `auth wecom` | Scan a WeCom QR code and set up the WeCom channel. |

### JSON output

For a program that runs `compa-kernel`, `--json` makes `model` (showing,
setting or clearing the default model), `model auto-free`, `model ping`,
`model roster`, `auth login`, `auth logout` and `auth status` print one JSON
document on stdout instead of text. Prompts, progress and logs go to stderr.
A command that fails prints `{"error": "..."}` on stdout and exits with
status 1.

The fields are those of the launcher's API, where it has the same data:

| Command | Document |
|---|---|
| `model` | `selection` (the default model, `""` for none), `active_models` (the chat shortlist) and `routes` (each with `name` and `targets`). |
| `model <selection>`, `model --clear` | `selection` (the new default model, `""` when cleared) and `previous`. |
| `model auto-free` | The answer of `POST /api/provider-instances/auto-connect-free`: `ok`, `total`, `catalog_discovered`, `verified`, `instances`, `default_model`, and `outcomes`, each provider's `status`, `models`, `probe_model`, `latency_ms`, `error_class` and `error`. `ok` false is not a failure. |
| `model ping [instance-id]` | `results`, one `POST /api/provider-instances/{id}/ping` answer per instance (`ok`, `instance_id`, `latency_ms`, `model_count`, `status`, `error`), and `total`. |
| `model roster` | `providers` and `total`, as `GET /api/provider-roster`. |
| `auth login` | `status` (`ok`), `provider`, `instance_id`, `model_count` and `default_model`. The key is read from stdin when it isn't a terminal. |
| `auth logout` | `status` (`ok`) and `providers`, the providers logged out of. |
| `auth status` | `providers`, each stored credential's `provider`, `auth_method`, `status` (`active`, `expired` or `needs_refresh`), `account_id` and `expires_at`, and `total`. Tokens are never printed. |

```sh
echo "$OPENAI_API_KEY" | compa-kernel auth login --provider openai --json
compa-kernel model openai/gpt-5.4 --json
```

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
| `cron add -n <name> -m <message> -c '<cron expression>'` | Add a job, such as `-c '0 9 * * *'`, in local time or the time zone `--tz Europe/Prague` names; `-e <seconds>` (60 or more) repeats every N seconds instead; `--channel` and `--to` choose where the result goes. |
| `cron enable <id>`, `cron disable <id>` | Turn a job on or off. |
| `cron remove <id>` | Remove a job by ID. |

### skills

| Command | What it does |
|---|---|
| `skills list` | List installed skills. |
| `skills list-builtin` | List the skills that come with Compa. |
| `skills install-builtin` | Copy the skills that come with Compa into the workspace; a skill already there is kept. |
| `skills search [query]` | Search available skills. |
| `skills install <owner/repo/path>` | Install a skill from GitHub. |
| `skills install --registry <name> <slug>` | Install a skill from a registry, such as `clawhub`. |
| `skills show <name>` | Show skill details. |
| `skills remove <name>` | Remove installed skill. |

### evolution

In `draft` and `apply` modes (`evolution.mode`), evolution proposes skill
changes as drafts; a draft changes a skill only after you accept it. Evolution
keeps its task and pattern records for 30 days.

| Command | What it does |
|---|---|
| `evolution drafts list [--all]` | List the drafts waiting for review, with the change each one makes; `--all` also lists the written, rejected and quarantined ones. |
| `evolution drafts accept <draft-id>` | Accept a draft. In `apply` mode it is written to its skill at once, the old version backed up; in another mode it is marked `approved` and written when evolution next runs in `apply` mode. |
| `evolution drafts reject <draft-id>` | Reject a draft; it is never written. |

### mcp

| Command | What it does |
|---|---|
| `mcp list [--status]` | List configured MCP servers. |
| `mcp add [flags] <name> <command-or-url> [args...]` | Add or update an MCP server, and turn MCP on. Flags go before the name: `-t stdio`, `http` or `sse`; `-e KEY=value`, or `--env-file <file>` for secrets; `-H 'Name: Value'`; `--trusted` makes Compa believe the server's tool annotations; `--cwd <folder>` sets a stdio server's working folder (see [MCP servers](use.md#mcp-servers)); `--deferred` hides the server's tools until the agent finds them with tool search, `--no-deferred` always offers them; `-f` replaces a server of that name without asking. In `mcp add <name> -- <command> [args...]`, everything after `--` is the command. |
| `mcp show <name>` | Show details and tools for a configured MCP server, including **Trusted** and **Cwd**. |
| `mcp test <name>` | Test connectivity for a configured MCP server. |
| `mcp remove <name>` | Remove an MCP server from config. |
| `mcp edit` | Open the Compa config in `$EDITOR`. |

### Modules

| Command | What it does |
|---|---|
| `modules` | List installed modules and their capabilities. |
| `modules-add <path>` | Install a module from a local program. |
| `modules-enable <id>`, `modules-disable <id>` | Turn a module on or off. A disabled module's capabilities aren't offered to the agent and don't run with `module-invoke`; its files and state stay. |
| `modules-remove <id>` | Remove a module; its state is left intact. |
| `module-invoke <module> <capability> [json]` | Run one capability; `--source-root name=path` grants a folder or file read-only. A capability the [approval policy](use.md#approvals) asks about runs only with `--approve`, your approval of this run; an agent that can run commands can pass it too. One the policy denies or hides doesn't run. The policy sees these runs with the origin `cli`. |

### Other commands

| Command | What it does |
|---|---|
| `onboard [--force]` | Initialize Compa configuration and workspace. Run again, it keeps `config.json` and the workspace files you changed, and adds the missing ones; `--force` replaces the changed files with the defaults, after saving each as `<file>.bak-<time>`. |
| `config reset [-f]` | Reset `config.json` to the defaults, after saving a dated backup beside it. Secrets stay; providers have to be connected again. `-f` skips the question. |
| `status` | Show the config, workspace and model in use. |
| `update [--version v1.2.3 \| -u <release page>] [--allow-downgrade]` | Install a release of `compa` and `compa-kernel`, by default the latest; restart Compa afterwards. A release older than the running one needs `--allow-downgrade`. |
| `version` | Show version information. |

## Environment variables

`compa` and `compa-kernel` read these when they start.

| Variable | What it does |
|---|---|
| `COMPA_HOME` | Folder for settings and data instead of `~/.compa`. |
| `COMPA_CONFIG` | Config file to use instead of `config.json` in that folder. |
| `COMPA_LAUNCHER_HOST` | Address the web UI listens on, as `-host` sets it; `-host` wins when you give both. It overrides `-public` and **Enable LAN Access**, so an address other than localhost opens the web UI to the network; read [LAN access](install.md#lan-access) first. |
| `COMPA_BINARY` | The `compa-kernel` that `compa` runs, instead of the one beside it. |
| `COMPA_GATEWAY_HOST`, `COMPA_GATEWAY_PORT` | Address and port of the gateway, instead of `gateway.host` and `gateway.port`. |
| `COMPA_LOG_LEVEL` | Log level, instead of `gateway.log_level`: `debug`, `info`, `warn` (the default), `error` or `fatal`. |
| `COMPA_LOG_FILE` | `compa-kernel agent` writes its log to this file instead of the terminal. |
| `COMPA_SUBPROCESS_ALLOW` | Program names, separated by commas, that modules may run, out of those each module declares. Unset, every declared program is allowed. |
| `COMPA_DNS_SERVER` | On Linux without `/etc/resolv.conf`, the DNS servers `compa-kernel` asks, separated by `;` (default `8.8.8.8:53;1.1.1.1:53`). |
| `COMPA_CHANNELS_<NAME>_ENABLED` | Turns the `channel_list` entry `<NAME>` on (`true`) or off (`false`) instead of its `enabled`, such as `COMPA_CHANNELS_WEB_ENABLED=true` for the web chat. `<NAME>` is the entry's name in capitals, with `_` for each character other than a letter or digit. |
| `COMPA_CHANNELS_WEB_STREAMING_ENABLED`, `COMPA_CHANNELS_TELEGRAM_STREAMING_ENABLED` | Turn streamed replies on or off in the web chat or on Telegram. The same prefix with `_STREAMING_THROTTLE_SECONDS` and `_STREAMING_MIN_GROWTH_CHARS` sets the channel's `throttle_seconds` and `min_growth_chars`. |

Many `config.json` settings can be set with a variable named after their place
in the file, such as `COMPA_AGENTS_DEFAULTS_WORKSPACE` for
`agents.defaults.workspace`. The variable wins over the file, and saving the
config keeps the file's value unless you change the setting.
