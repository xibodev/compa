# Use Compa

Compa's web UI is at http://localhost:18800. The sidebar has **Chat**,
**Models** and **Channels**; the **Agent** group (**Skill hub**, **Skills**,
**Modules**, **Tools**); and the **Services** group (**Config**, **Voice**,
**Logs**).

Settings below written like `tools.approval` are keys in
`~/.compa/config.json`, which **Config** → **Raw Config** edits; those of the
web UI itself, such as `remote_images`, are in `~/.compa/launcher-config.json`.
The defaults given are those of a new install.

## First run and your password

The first time, Compa asks you to set a password of at least 8 characters.
Only the setup link Compa opens in your browser, or prints when it runs in a
terminal, can set that first password; if you closed the page, choose **Open
Console** in the tray menu. After that you sign in with the password; when
Compa opens your browser itself on this computer, it signs you in for you.
Restarting Compa signs every browser out.

- Change the password: **Config** → **Compa app** → **Login Password**. It
  asks for the **Current Password** too.
- Sign out every browser: the sign-out button in the top bar →
  **Sign out everywhere**.
- Forgot it: see [Troubleshooting](troubleshooting.md#forgot-the-password).
- Change the language with the language button in the top bar: English,
  Čeština, Português (Brasil), 简体中文 or বাংলা.

## Models

Compa needs at least one model. Open **Models**. The **Providers** tab lists
the providers Compa can connect, taken from the registry in
[llmgw-core](https://github.com/xibodev/llmgw-core); the filters narrow it to
connected, free, API-key or local providers.

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
providers through an open protocol, the `extension` package of llmgw-core.
Compa doesn't include one. Under **Extension** at the bottom of the Providers
tab, press **Connect** and enter the app's address and, if it uses one, its
shared secret. Its providers then appear as cards: open a card to **Sign in**
or **Paste token**.

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
- Attach images with **Add images** or by dropping them on the chat.
- The **Reasoning and tool calls** button chooses whether replies show the
  model's reasoning and the tools it called.
- An image from the web in a reply loads when you click it; with
  `remote_images` set to `always`, it loads right away.
- Enter sends; Shift + Enter starts a new line.

### Tools and the workspace

When a task needs it, Compa uses tools: it reads, writes and edits files, runs
shell commands, searches the web and reads pages, schedules reminders and
recurring jobs, and finds and installs skills. Scheduled jobs run while Compa
is running. The **Tools** page turns each tool on or off.

File tools work in the workspace folder, `~/.compa/workspace` by default
(**Config** → **Agent** → **Workspace Directory**). With **Restrict to
Workspace** on, which is the default, file tools refuse paths outside the
workspace, except Compa's attachment folder and paths you allow in
`config.json` (`tools.allow_read_paths`, `tools.allow_write_paths`). Commands
start in the workspace and are blocked when they name paths outside it. They
still run as your user account, so this is a guard, not a sandbox.
**Config** → **Run Commands** controls commands: **Allow Commands**, the
command blacklist and the timeout.

The agent sends messages only to the chat it is answering; set
`tools.message.targets` to `any` to let it message any chat Compa is connected
to.

### MCP servers

MCP servers add tools from other programs. Add one under **Config** →
**Tools & automation** → **MCP**, with
[`compa-kernel mcp add`](cli.md#mcp), or in `tools.mcp.servers.<name>`. Two
settings of a server are only in `config.json` and on the command line:

- `trusted` (off by default): Compa believes the annotations the server
  declares for its tools, such as read-only. [Approval rules](#approvals) can
  then match them, and a read-only or idempotent call is retried after a lost
  session.
- `cwd`: the folder a stdio server starts in. `~` expands, and a relative path
  is taken from the workspace. By default it's the agent's workspace, when that
  folder exists.

A stdio server gets Compa's environment, then the variables in `env_file`, then
those in `env`; a later value wins. Compa gives the servers the agents'
workspaces as their roots, and a reload starts the servers again with the
current ones.

The agent sees a server's tools as `mcp_<server>_<tool>`, with every character
other than letters, digits, `_` and `-` replaced by `_` and case kept. A short
hash is added to the name when that replacing changed it, when it would be
longer than 64 characters, or when two tools would get the same name. A tool
whose name is still taken isn't added; the log says so.

When a server loses its session during a call, Compa doesn't send the call
again: it fails with "the server lost its session during this call; the tool
may or may not have run", and Compa reconnects for the next call. Only a tool
that a trusted server marks read-only or idempotent is called once more.

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
| `source` | Where the tool comes from: `builtin`, `mcp:<server>` or `module:<id>`. To match one MCP server's tools, use `source`: a name glob like `mcp_git_*` also matches the tools of a server named `git_hub`. |
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
a program that embeds Compa. With none, Compa posts the request in a chat with
a short ID, such as
``Approve running: `df -h`? Reply /approve k7m2qp or /deny k7m2qp``, and waits;
other calls, `exec` with another action or a `cwd` included, read
``Approve calling <tool> with `<arguments>`?``, and a scheduled job's request
names the job. The request shows the whole command or all the arguments, in a
code block when they span lines or hold backticks; a call too long to show in
full (over 3000 characters) is refused without asking. Answer `/approve <id>`
to go ahead or `/deny <id>` to refuse. A request nobody answers within 10
minutes is refused. Only you can answer, whatever `commands.owner_only` says.

When the request comes from your own message, Compa asks in that chat. When it
comes from someone else's message or from a scheduled job, Compa asks in your
chat: the conversation you last wrote to Compa from, in the browser or a chat
app. Until you have written to Compa there, such requests are refused, as is
any request with no chat to ask in. The terminal shows requests only in
interactive `compa-kernel agent`.

**Config** → **Tools & automation** → **Approvals**, after **Run Commands**,
shows one row per rule (**Tool**, **Source**, **From**, **Hints**, **Action**),
**Add rule** and **Default**. Saving the policy applies it right away, without
a restart. A `rules` list you write in `config.json` replaces the default
rules, so keep those you still want, as here:

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

Channels let you talk to Compa from chat apps: Telegram, Discord, Slack,
WhatsApp, Matrix, LINE, IRC, Feishu, DingTalk, WeCom, WeChat, QQ and others.
Chat in the browser works without any of them.

Open **Channels**, pick an app, fill in its fields (usually a bot token from
that app), turn on **Enable channel** and save. If Compa says the gateway needs
a restart (**Gateway restart required**), choose **Restart gateway** in the
status menu at the top. WeChat and WeCom connect by scanning a QR code.

**Allow From** lists the user and group IDs that may use a channel; `*` lets
anyone in. Allowing a group by its ID also admits its forum topics and threads
(Telegram topics, Slack threads). Two settings on each channel's page decide
who else gets an answer:

- **DM Policy** (`dm_policy`), for direct messages. With **Pairing**, the
  default, Compa doesn't answer someone outside **Allow From**: their message
  shows up under **Pairing Requests** on the channel's page, where **Approve**
  adds them to **Allow From** and **Deny** drops the request. Compa doesn't
  reply to them meanwhile; a request is kept for 7 days, at most 20 per
  channel. **Allowlist** answers only **Allow From**, **Open** answers anyone,
  and **Disabled** answers no direct messages.
- **Group Policy** (`group_policy`), for groups and rooms. **Allowlist**, the
  default, answers only the senders and groups in **Allow From**; **Open**
  answers everyone in the group; **Disabled** ignores groups. In a group Compa
  answers only when it's mentioned, unless you turn off the channel's **Group
  Mention Only** (**Mention Only** on Discord; `group_trigger.mention_only`).

Slash commands such as `/reload` work only for you (`commands.owner_only`):
in a chat app that means a sender listed by ID in **Allow From**, and in the
browser and the terminal it's always you. `/help` lists the commands.

A channel without `dm_policy` or `group_policy` in `config.json` takes them
from **Allow From**: with `*` both are **Open**; otherwise groups are
**Allowlist**, and direct messages **Allowlist** when it lists IDs and
**Pairing** when it's empty.

Saving **Allow From**, **DM Policy** or **Group Policy**, or approving a
pairing request, applies to the running gateway at once, as saving the model
selections or `tools.approval` does. Such a save applies your other saved
changes with it, channel tokens included. Any other change waits for a
restart of the gateway.

Anyone a channel answers can use Compa and its tools. Because **Allow Remote
Commands** is on by default, that includes running commands on your computer.
To keep chat apps from running commands at all, turn off **Allow Remote
Commands** under **Config** → **Run Commands**.

WhatsApp native, in builds made with the `whatsapp_native` tag, links your own
WhatsApp account. Its **Chats** setting chooses what Compa reads: **Self**, the
default, only your "message yourself" chat; **Allowed**, that chat and the
chats the policies above let in; **All**, every chat, where Compa replies for
you. Your own messages in other chats are never taken as instructions.

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
`~/.compa/modules` and press **Refresh**. A module's ID, which it declares
itself, is 1 to 64 lowercase letters, digits, `.`, `_` or `-`, starting with a
letter or digit; Compa doesn't install a module with any other ID.
**Disable** and **Remove** keep the module's stored data. In Chat, choose a
module in the **Module** menu.

A capability that [`tools.approval`](#approvals) asks about, by default one
with an unknown cost, network access or writes outside your computer, runs
only with your approval: **Approve and run** on the Modules page, `/approve`
in chat when the agent runs it, or `compa-kernel module-invoke` with
`--approve`. The agent's own calls of a capability wait for your answer, but a
command it runs with `exec` acts as you, `compa-kernel module-invoke
--approve` included: add an `ask` rule for `exec` if that matters. A rule that
allows a capability is a standing approval: every call it matches runs as if
you had approved it, so it may spend money or publish if the capability does.
A broad rule such as `{"tool": "*", "action": "allow"}` approves every module
call this way. `deny` or `hide` refuses a capability everywhere, **Approve and
run** and `module-invoke` included. The Modules page's runs have the origin
`web`, and `module-invoke`'s `cli`.

## Logs

**Logs** shows the gateway's recent output, with a level menu and **Clear
logs**. The log files are in `~/.compa/logs`; Compa masks the keys and tokens
it knows in them and rotates them by size (see
[Troubleshooting](troubleshooting.md#where-the-logs-are)).
