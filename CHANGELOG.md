# Changelog

Notable changes to Compa, newest first. Versions follow
[Semantic Versioning](https://semver.org/).

## Unreleased

### Fixed

- On Windows, `compa-kernel` looks names up through the system's DNS, so VPN
  and company DNS and local host names work; it asked 8.8.8.8 and 1.1.1.1
  instead.
- **Gateway restart required** shows after a change to any tool setting, such
  as **Allow Remote Commands** or an MCP server, and to the workspaces, the
  restriction to them, `isolation`, `commands` or `hooks`, which the running
  gateway takes up only when it restarts or reloads. It showed only when a
  tool was turned on or off.

## 2.1.0 - 2026-10-05

### Fixed

- Go programs can depend on Compa 2: the module path is
  `github.com/xibodev/compa/v2`. The 2.0.0 tag couldn't be fetched as a Go
  module.
- An MCP tool call fails when the server hasn't answered within 5 minutes, a
  limit 2.0.0 added. `tools.mcp.call_timeout_seconds` sets it for every server,
  and a server's `call_timeout_seconds` for that server.
- The 10-minute limit 2.0.0 put on every tool call no longer cuts short a
  call with a timeout of its own: MCP calls and `exec` runs with a timeout
  last as long as their timeouts allow.

## 2.0.0 - 2026-10-05

Settings below are keys in `~/.compa/config.json` unless noted.

### Channels

- Chat-app channels answer only the IDs in **Allow From**. Other people's
  direct messages become **Pairing Requests** on the channel's page, where you
  **Approve** or **Deny** them (`dm_policy`, default `pairing`); an approval
  takes effect without a gateway restart.
- **Group Policy** (`group_policy`, default `allowlist`) decides who is
  answered in groups, and groups need a mention by default (**Group Mention
  Only**).
- WhatsApp native reads only your "message yourself" chat by default
  (**Chats**: Self, Allowed or All); your own messages elsewhere are never
  taken as instructions.
- MQTT verifies the broker's TLS certificate unless
  `tls_insecure_skip_verify` is on.
- OneBot and the web client refuse unencrypted `ws://` to a host outside this
  computer and its local network; use `wss://` there.

### Approvals and commands

- One approval policy, `tools.approval`, decides every tool call. Its rules
  match the tool, its source (`builtin`, `mcp:<server>`, `module:<id>`), where
  the call came from (`web`, `cli`, `chat`, `cron`) and hints (annotations of
  trusted MCP servers, module effects), and `allow`, `ask`, `deny` or `hide`
  it. By default Compa asks before module capabilities with an unknown cost,
  network access or external writes, and before installing a skill. The
  **Approvals** card under **Config** → **Tools & automation** edits it.
- An `ask` goes to approver hooks, or with none waits for your
  `/approve <id>` or `/deny <id>` in chat; unanswered requests are refused
  after 10 minutes. Scheduled commands are decided as calls from `cron`.
  Approver hooks are asked only about the calls the policy asks about.
- A request shows the whole command or all the arguments; a call too long to
  show in full (over 3000 characters) is refused without asking.
- Requests from other people's messages, scheduled jobs and the heartbeat go
  to your chat, the conversation you last wrote to Compa from.
- Saving `tools.approval`, the model selections, or a channel's **Allow
  From**, **DM Policy** or **Group Policy** applies to the running gateway
  without a restart.
- Slash commands work only for you (`commands.owner_only`); `/help` lists
  `/approve` and `/deny`.
- The agent messages only the chat it is answering
  (`tools.message.targets`).

### Web UI and LAN access

- The first password can only be set through the setup link Compa opens or
  prints. Changing the password asks for the current one; **Sign out
  everywhere** signs out every browser.
- In LAN mode Compa doesn't start without a password unless **Allow LAN
  Without Password** is on, and the gateway stays on this computer.
- **Allowed Hosts** adds names, such as a reverse proxy's, that the web UI
  answers to. Images from the web in chat load on click (**Remote Images**).
- `compa -password -` asks for the password instead of taking it on the
  command line.

### MCP

- When a server loses its session during a call, the call isn't sent again:
  it fails, saying the tool may or may not have run, and Compa reconnects for
  the next call. Only a tool that a trusted server marks read-only or
  idempotent is retried, once.
- Server settings `trusted` (believe the server's tool annotations) and `cwd`
  (a stdio server's working folder, by default the agent workspace);
  `compa-kernel mcp add --trusted --cwd <folder>`.
- Servers get the agents' workspaces as their roots, and the progress they
  report is published as `mcp.tool.call.progress` runtime events, at most once
  a second per call.
- Tool names are `mcp_<server>_<tool>` with case kept; a short hash is added
  only when a name had to change, is too long or collides with another, and a
  tool whose name is still taken isn't added.

### Logs, modules and the rest

- Logs mask known keys and tokens (`logging.redact_secrets`) and rotate at
  10 MB, keeping 5 files (`logging.max_size_mb`, `logging.max_files`).
- Module capabilities with an unknown cost, network access or external writes
  need your approval by default: **Approve and run** on the Modules page,
  `/approve` in chat, or `compa-kernel module-invoke --approve`. Module IDs
  are checked when a module is installed or removed.
- The heartbeat runs for your chat and only writes when something needs your
  attention; without a known chat it is skipped.
- Scheduled jobs are stored in `~/.compa/state/cron`, outside the agent's
  workspace, and failed runs are recorded as errors. Jobs made with 1.0.0, in
  the workspace's `cron` folder, aren't moved there: add them again.
- `compa-kernel onboard` keeps workspace files you changed; `--force` replaces
  them after a backup.
- `compa-kernel update` puts the previous version back when the new kernel
  doesn't run, and installs an older release only with `--allow-downgrade`;
  updating from the web UI never does.
- Evolution drops task and pattern records older than 30 days. Records the
  model leaves out of its clusters are clustered the simple way, so they
  aren't sent to the model again on every run.

### Removed

- `enc://` secret encryption, `compa-kernel onboard --enc`,
  `COMPA_KEY_PASSPHRASE` and `COMPA_SSH_KEY_PATH`. Secrets are plain values in
  `.security.yml` (mode 600), or `file://` references to files beside it.
- `COMPA_APPROVE_CAPABILITIES`, replaced by `tools.approval`.
- `agents.defaults.subturn.default_token_budget`, which every config written
  by 1.0.0 has. Remove it: a config that still has it fails to load, and the
  error names the key.
