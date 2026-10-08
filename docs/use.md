# Use Compa

Compa's web UI is at http://localhost:18800. The sidebar has **Chat**,
**Models** and **Channels**; the **Agent** group (**Skill hub**, **Skills**,
**Modules**, **Tools**); and the **Services** group (**Config**, **Voice**,
**Logs**).

Settings below written like `tools.approval` are keys in
`~/.compa/config.json`, which **Config** → **Raw Config** edits. The defaults
given are those of a new install.

## First run and your password

The first time, Compa asks you to set a password of at least 8 characters. In
the browser, only the setup link Compa opens, or prints when it runs in a
terminal, can set it; if you closed the page, choose **Open Console** in the
tray menu. After that you sign in with the password; when Compa opens your
browser itself on this computer, it signs you in for you. Restarting Compa
signs every browser out.

- Change the password: **Config** → **Compa app** → **Login Password**. It
  asks for the **Current Password** too, and signs out every other browser.
- Sign out every browser: the sign-out button in the top bar →
  **Sign out everywhere**.
- Forgot it: see [Troubleshooting](troubleshooting.md#forgot-the-password).
- Change the language with the language button in the top bar: English,
  Čeština, Português (Brasil), 简体中文 or বাংলা.

## Models

Compa needs at least one model. Open **Models**. The **Providers** tab lists
the providers Compa can connect; the filters narrow it to connected, free,
API-key or local providers.

**Try free providers.** Under **Models in Chat**, press **Try free providers**.
Compa sends a short test message to each free provider that needs no key, such
as Kilo Code, LLM7.io and Pollinations.ai, and adds the ones that answer. The
results say why the others didn't. These are third-party services: what you
send goes to them, and they can be busy or stop answering.

**API key.** Pick a card such as OpenAI, Anthropic, Google Gemini, OpenRouter,
Groq or Mistral AI, paste your key and press **Connect provider**. Compa keeps
the key on this computer and loads the provider's models.

**Local servers.** Ollama and LocalAI are under **Local & self-hosted**. Start
the server, pick its card, check the address and connect. No key is needed.

**Custom endpoints.** For any other server that speaks the OpenAI or Anthropic
API, pick **Custom OpenAI-compatible** or **Custom Anthropic-compatible** and
enter a connection name, the API address and, if it needs one, a key.

**Extension.** An extension is a separate app on your computer that adds more
providers; Compa doesn't include one. Under **Extension** at the bottom of the
Providers tab, press **Connect** and enter the app's address and, if it uses
one, its shared secret. Its providers then appear as cards: open a card to
**Sign in** or **Paste token**.

Open a connected card to put its models in the chat menu (**Add to Chat**),
**Test connection**, **Refresh models**, **Edit** the address or key, or
**Remove** the provider.

**Default model.** **Models in Chat** are the models the chat menu offers. The
default model answers every chat that doesn't pick one; the bar at the top of
the Models page shows it. Set it with **Set as default** next to a model or a
route. If no default is set, the first model you add to Chat becomes the
default. A new default applies right away, without a restart.

**Routes.** A route is an ordered list of models: when one fails, the next one
answers. On the **Models & Routes** tab press **Add route**, name it, add
models and order them with **Up** and **Down**. A route can be the default
model, and the chat menu lists it under **Named routes**.

## Chat

- **New Chat** starts a conversation. **History** reopens or deletes earlier
  ones.
- The menu at the top picks the model for this chat: the default, a route, or
  a model in Chat. The **Module** menu next to it points the agent at an
  installed module.
- Attach images with **Add images**, or drop or paste them into the message
  box.
- The **Reasoning and tool calls** button chooses whether replies show the
  model's reasoning and the tools it called.
- An image from the web in a reply loads when you click it; set **Config** →
  **Compa app** → **Remote Images** to **Always** to load them right away.
- Enter sends; Shift + Enter starts a new line.

### Tools and the workspace

When a task needs it, Compa uses tools: it reads, writes and edits files, runs
shell commands, searches the web and reads pages, schedules reminders and
recurring jobs, and finds and installs skills. Scheduled jobs run while Compa
is running. The **Tools** page turns each tool on or off.

File tools work in the workspace folder, `~/.compa/workspace` by default
(**Config** → **Agent** → **Workspace Directory**). With **Restrict to
Workspace** on, which is the default, file tools refuse paths outside the
workspace, except reading Compa's attachment folder, the skill folders outside
the workspace and the paths that `tools.allow_read_paths` and
`tools.allow_write_paths` in `config.json` match;
`agents.defaults.allow_read_outside_workspace` lets them read anywhere.
The path patterns are regular expressions matched against the full path, such
as `^/home/me/notes/`; in JSON, each `\` of a Windows path is written `\\\\`.
Commands start in the workspace and are blocked when they name other paths,
except the attachment and skill folders and those `tools.allow_read_paths`
matches. They still run as your user
account, so this is a guard, not a sandbox. **Config** → **Run Commands**
controls commands: **Allow Commands**, the command blacklist and whitelist, and
the timeout.

With the `message` tool, the agent can send to any chat on any connected
channel, not only the chat it is answering. For example, it can message a
WhatsApp contact by number; on WhatsApp the message comes from your account.
To keep it to the chat the turn came from, set **Send To** on the `message`
tool's card on the **Tools** page to **Current chat**, or
`tools.message.targets` to `current_chat`. To have it check with you first, add
an `ask` rule for the `message` tool (see [Approvals](#approvals)).

### MCP servers

MCP servers add tools from other programs. MCP is off in a new install: turn
on **Config** → **MCP** → **Enable MCP** and add a server there, or run
[`compa-kernel mcp add`](cli.md#mcp), which turns MCP on, or set
`tools.mcp.enabled` and `tools.mcp.servers.<name>` in `config.json`. A server
you add or change starts when you restart the gateway (**Gateway restart
required**). Two settings of a server are only in `config.json` and on the
command line:

- `trusted` (off by default): Compa believes the annotations the server
  declares for its tools, such as read-only, so [approval rules](#approvals)
  can match them.
- `cwd`: the folder a stdio server starts in. `~` expands, and a relative path
  is taken from the workspace. By default it's the agent's workspace, when that
  folder exists.

A stdio server gets Compa's environment, then the variables in `env_file`, then
those in `env`; a later value wins. Compa gives the servers the agents'
workspaces as their roots.

The agent sees a server's tools as `mcp_<server>_<tool>`, with a short hash
added when the name has characters other than letters, digits, `_` and `-`,
is longer than 64 characters or is already taken. A tool whose name is still
taken isn't added; the log says so.

When a server loses its session during a call, Compa doesn't send the call
again: it fails with "the server lost its session during this call; the tool
may or may not have run", and Compa reconnects for the next call. Only a tool
that a trusted server marks read-only or idempotent is called once more.

A call the server hasn't answered within 5 minutes fails, and the server is
told to cancel it. For tools that take longer, such as renders or builds, set
`tools.mcp.call_timeout_seconds` for every server or a server's
`call_timeout_seconds` for that one, in `config.json`.

### Approvals

`tools.approval` decides every tool call: it runs, waits for your approval, is
refused, or the tool isn't offered to the agent at all. These are the defaults:

```json
"approval": {
  "default": "allow",
  "rules": [
    {"source": "module:*", "hints": ["cost_unknown", "network", "external_writes"], "action": "ask"},
    {"tool": "install_skill", "action": "ask"}
  ]
}
```

So Compa asks before a module capability with an unknown cost, network access
or writes outside your computer, and before it installs a skill; everything
else runs. The first rule that matches a call decides it, and a call no rule
matches gets `default` (`allow` when unset). A rule matches when every field it
sets matches:

| Field | Matches |
|---|---|
| `tool` | The tool's name as the agent sees it, such as `exec` or `install_skill`. |
| `source` | Where the tool comes from: `builtin`, `mcp:<server>` or `module:<id>`. To match one MCP server's tools, use `source`, such as `mcp:git`: a `tool` glob like `mcp_git_*` also matches the tools of a server named `git_hub`. |
| `origin` | Any of `web` (the browser chat), `cli` (`compa-kernel agent`), `chat` (chat apps) and `cron` (scheduled jobs and the heartbeat). A subagent's calls have its parent's origin. |
| `hints` | Any of `read_only`, `destructive`, `idempotent` and `open_world`, the annotations of an MCP tool, and `cost_unknown`, `network` and `external_writes`, the effects a module declares for a capability. |

In `tool` and `source`, `*` matches any run of characters and `?` one
character. Annotations count only from a server with `"trusted": true` (see
[MCP servers](#mcp-servers)); the tools of any other server count as
`destructive` and `open_world`. A module's effects are what the module says
about itself: Compa can't check them, any more than the rest of a program you
installed.

The `action` is `allow`, `ask` (wait for an approval, below), `deny` (refuse
the call) or `hide`: the agent isn't offered the tool, not even through tool
search, and a call of it is refused. An `ask` for `exec` also covers its
`write` and `send-keys` actions, since input sent to a process runs things in
it. Only `poll`, `read`, `list` and `kill` are never asked about, since they
look at or stop a process an allowed or approved `run` started; `deny` and
`hide` still apply. The model provider's own web search is used only when the
policy allows `web_search` outright.

With `ask`, approver hooks decide first: process hooks with
`intercept: ["approve_tool"]` (in `hooks.processes`), or the `ToolApprover` of
a program that embeds Compa. With none, Compa posts the request in a chat,
such as ``Approve running: `df -h`? Reply /approve k7m2qp or /deny k7m2qp``,
showing the whole command or all the arguments, and waits. A call too long to
show in full (over 3000 characters) is refused without asking. Answer
`/approve <id>` to go ahead or `/deny <id>` to refuse; a request nobody answers
within 10 minutes is refused. Only you can answer, whatever
`commands.owner_only` says.

When the request comes from your message, Compa asks in that chat. When it
comes from a scheduled job or the heartbeat, Compa asks in your chat: the
conversation you last wrote to Compa from, in the web chat, WhatsApp or Slack.
Until you have written to Compa there, such requests are refused, as is any
request with no chat to ask in. A copy of each request also goes to the
enabled Slack and Teams webhooks (see
[Webhooks and notifications](#webhooks-and-notifications)), but you answer in
your chat, not in a webhook. The terminal shows requests only in interactive
`compa-kernel agent`.

**Config** → **Approvals** shows one row per rule (**Tool**, **Source**,
**From**, **Hints**, **Action**), **Add rule** and **Default**. Saving the
policy applies it right away, without a restart. A `rules` list you write in
`config.json` replaces the default rules, so keep those you still want, as
here:

```json
"rules": [
  {"tool": "exec", "origin": ["chat", "cron"], "action": "ask"},
  {"source": "module:render", "action": "allow"},
  {"source": "mcp:github", "origin": ["chat"], "action": "hide"},
  {"source": "module:*", "hints": ["cost_unknown", "network", "external_writes"], "action": "ask"},
  {"tool": "install_skill", "action": "ask"}
]
```

The first rule asks before commands from chat apps and scheduled jobs, the
second runs the `render` module's capabilities without asking, and the third
hides the `github` server's tools from chat apps; the last two are the
defaults. The `render` rule comes before the default module rule, which would
otherwise decide first.

## Voice

Open **Voice**:

1. Under **Dictation (speech to text)**, choose a **Speech-to-text model**.
   **Test microphone** checks it.
2. Under **Spoken replies (text to speech)**, choose a **Text-to-speech
   model**. **Preview voice** plays a sample. Some providers ask for a **Voice
   name**.
3. Turn on **Enable voice**, choose the **Mode** (**Push-to-talk** or
   **Hands-free**) and press **Save voice settings**.

The models come from the providers you've connected; a provider card shows
whether a model does speech to text or text to speech. Your recordings, and
the replies to be spoken, go to those providers.

In Chat, voice controls sit next to the message box:

- Push-to-talk: press the microphone (**Speak to agent**), talk, then press
  **Done**. Compa transcribes what you said and sends it. The speaker button
  (**Spoken replies**) reads replies aloud.
- Hands-free needs both models. Switch the mode button beside the microphone
  to **Hands-free Voice** and press **Start hands-free**: Compa listens,
  answers aloud and listens again until you press **End call**.

Voice messages sent to Compa in chat apps are transcribed too; **Echo
transcriptions** replies to them with the transcript.

## Channels

Channels let you talk to Compa from outside the browser. These are in every
build:

- **Web chat**: the **Chat** page of the dashboard. It needs no setup.
- **WhatsApp**: Compa is a linked device of your WhatsApp account.
- **Slack**: a Slack app's bot that you message directly.
- **Slack webhook** and **Teams webhook**: they only send, to a Slack or
  Microsoft Teams channel.

Other chat apps are paused; see [Paused channels](#paused-channels).

Open **Channels**, pick Slack, WhatsApp or the web chat, fill in its fields,
turn on **Enable channel** and save. If Compa says the gateway needs a restart
(**Gateway restart required**), choose **Restart gateway** in the status menu
at the top. The webhooks are set up in `config.json`; see
[Webhooks and notifications](#webhooks-and-notifications).

### Who Compa answers

Compa serves one person, you. A channel answers only the accounts its **Allow
From** (`allow_from`) lists, and only in direct messages. It ignores group
chats, rooms and threads before doing any work. `*` and group IDs in **Allow
From** admit no one; for `*`, Compa logs a warning.

To add your account, leave **Allow From** empty and send the bot a direct
message. Compa doesn't answer it. The sender shows under **Pairing Requests**
on the channel's page instead. Each channel keeps at most 3 requests, for 1
hour, in `~/.compa/pairing.json`. **Approve** binds that account: it becomes
the only entry in **Allow From**, the channel's other requests are dropped, and
the change applies without a restart. **Deny** drops a request.

Once **Allow From** lists an account, Compa ignores everyone else and records
no requests, and approving a request is refused. To use another account of
your own, add it to **Allow From** yourself. Saving **Allow From** applies at
once, together with your other saved changes.

There are no DM, group or mention settings. A config that still has
`dm_policy`, `group_policy` or `group_trigger` loads; Compa ignores those keys
and doesn't write them back.

The web chat is reached only through the password-protected dashboard, so
Compa takes everything it receives as from you.

Slash commands such as `/reload` work in every chat Compa answers, since every
sender it answers is you. `/help` lists the commands.

Whoever can write from an account in **Allow From** can use Compa and its
tools. **Allow Remote Commands** (**Config** → **Run Commands**), on by
default, lets the chats run commands on your computer; turned off, chat apps
and the browser chat can't run commands, only the terminal. To be asked first
instead, add an `ask` rule for `exec` from `chat` (see [Approvals](#approvals)).

### WhatsApp

Compa joins your WhatsApp account as a linked device. Link it in one of two
ways:

- In a terminal, run `compa-kernel auth whatsapp`. It prints a QR code. In
  WhatsApp on your phone, choose **Settings** → **Linked devices** → **Link a
  device** and scan it. The command turns the channel on; then restart the
  gateway.
- In the dashboard, open **Channels** → **WhatsApp** and press **Link
  WhatsApp**. Scan the QR code it shows the same way. Compa then turns the
  channel on and restarts the gateway if it's running.

Compa answers your own "message yourself" chat. If you link Compa to a
separate number, it also answers direct messages from the numbers in **Allow
From**, written like `15550003333`, without `+`. WhatsApp makes no pairing
requests: your contacts' messages are your own conversations. Your own
messages in other chats are never taken as instructions, and groups are
ignored.

Photos, voice notes, audio, video and documents reach the agent, up to 50 MB
per file. A file that can't be downloaded is noted as unavailable. Replies are
text.

The session is kept in `whatsapp/` in the workspace, or in the folder
`settings.session_store_path` names. Without a linked account the channel
stays idle and logs a warning; the gateway keeps running. A `channel_list`
entry of type `whatsapp_native` is read as `whatsapp`.

### Slack

Compa connects to Slack in Socket Mode, so it needs no public address. It
needs two tokens from your Slack app, and the channel counts as set up only
with both:

- **Bot Token** (`bot_token`), which starts with `xoxb-`. Slack gives it when
  you install the app to your workspace.
- **App Token** (`app_token`), which starts with `xapp-`: an app-level token
  with the `connections:write` scope, for Socket Mode.

Message the bot directly; Compa ignores channels and their threads. While **Allow
From** is empty, approve yourself under **Pairing Requests**, as above.

### Webhooks and notifications

The Slack webhook (`slack_webhook`) posts to Slack incoming webhooks; the
Teams webhook (`teams_webhook`) posts to Microsoft Teams workflow webhooks.
They only send. Add one under `channel_list` in `config.json` (**Config** →
**Raw Config**). Each needs a target named `default` with an `https`
`webhook_url`; other targets are optional:

```json
"slack_webhook": {
  "enabled": true,
  "type": "slack_webhook",
  "settings": {
    "webhooks": {
      "default": { "webhook_url": "https://hooks.slack.com/services/..." }
    }
  }
}
```

Results of scheduled jobs, heartbeat messages and approval requests go to your
chat. A copy of each also goes to every enabled Slack or Teams webhook, to its
`default` target. Approvals are answered in your chat (the web chat, WhatsApp
or Slack), not in a webhook.

### Paused channels

Telegram, Discord, Delta Chat, DingTalk, Feishu (Lark), IRC, LINE, MaixCam,
Matrix, MQTT, OneBot, QQ, VK, WeCom and WeChat are paused. Only builds made
with the `paused_channels` build tag include them:
`go build -tags goolm,stdjson,paused_channels`, or
`make product GO_BUILD_TAGS=goolm,stdjson,paused_channels`. They aren't
maintained and may not compile. `compa-kernel auth weixin` and `auth wecom`
exist only in those builds.

A config that turns on a paused channel still loads. In a default build, the
gateway logs `Factory not registered` for that channel and starts the others.
The **Channels** page lists only Slack, WhatsApp and the web chat.

## Skills and modules

**Skills** are instructions the agent follows for particular tasks. **Skill
hub** searches skill registries (ClawHub and GitHub by default) and installs
skills into the workspace. Registry skills are third-party content, so read one
before you install it. **Skills** lists the installed ones; **Import Skill**
adds a Markdown or ZIP file of up to 1 MB. The agent can also find and install
skills itself, and asks you in the chat before it installs one (the default
`install_skill` rule in [Approvals](#approvals)); turn off `find_skills` and
`install_skill` on the **Tools** page if you don't want that.

**Modules** are separate programs that add capabilities. On **Modules**, enter
the path to a module program and press **Install**, or drop the program into
`~/.compa/modules` and press **Refresh**. **Disable** and **Remove** keep the
module's stored data. In Chat, choose a module in the **Module** menu.

A capability that [`tools.approval`](#approvals) asks about runs only with
your approval: **Approve and run** on the Modules page (origin `web`),
`/approve` in chat when the agent runs it, or `compa-kernel module-invoke
--approve` (origin `cli`). A command the agent runs with `exec` acts as you,
`module-invoke --approve` included, so add an `ask` rule for `exec` if that
matters. A rule that allows a capability approves every call it matches,
which may spend money or publish if the capability does; `deny` or `hide`
refuses it everywhere.

## Logs

**Logs** shows the gateway's recent output, with a level menu and **Clear
logs**. Where the log files are, and how Compa masks secrets in them and
rotates them: [Where the logs are](troubleshooting.md#where-the-logs-are).
