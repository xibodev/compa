# Use Compa

Compa's web UI is at http://localhost:18800. The sidebar has **Chat**,
**Models** and **Channels**; under **Agent**, **Skill hub**, **Skills**,
**Modules** and **Tools**; under **Services**, **Config**, **Voice** and
**Logs**.

Settings written like `tools.approval` are keys in `~/.compa/config.json`,
which **Config** → **Raw Config** edits.

## Password

The first time, Compa asks you to set a password of at least 8 characters. Only
the setup link Compa opens, or prints when it runs in a terminal, can set it;
if you closed the page, choose **Open Console** in the tray menu. When Compa
opens your browser itself, it signs you in. Restarting Compa signs every
browser out.

- Change it: **Config** → **Compa app** → **Login Password**.
- Sign out every browser: the sign-out button in the top bar → **Sign out
  everywhere**.
- Forgot it: see [Troubleshooting](troubleshooting.md#forgot-the-password).

## Models

Compa needs at least one model. On **Models** → **Providers**:

- **Try free providers** (under **Models in Chat**) tests the free providers
  that need no key, such as Kilo Code, LLM7.io and Pollinations.ai, and adds
  those that answer. They are third-party services: what you send goes to them,
  and they can be busy or stop answering.
- **API key:** pick a provider such as OpenAI, Anthropic, Google Gemini,
  OpenRouter, Groq or Mistral AI, paste your key and press **Connect
  provider**.
- **Local & self-hosted:** Ollama, LM Studio or LocalAI. Start the server, pick
  its card, check the address and connect.
- **Custom OpenAI-compatible** or **Custom Anthropic-compatible:** any other
  server that speaks one of those APIs.
- **Extension:** a separate app on your computer that serves more providers.
  Under **Extension**, press **Connect** and enter the app's address and its
  shared secret. Its providers then appear as cards, where you **Sign in** or
  **Paste token**.

Open a connected card to **Add to Chat** its models, **Test connection**,
**Refresh models**, **Edit** or **Remove** it. **Advanced options** on a
connection set its proxy, request timeout, rate limit, thinking level and
more.

**Default model.** The default model answers every chat that doesn't pick one.
Set it with **Set as default** next to a model or a route. The first model you
add to Chat becomes the default if none is set.

**Routes.** A route is an ordered list of models: when one fails, the next one
answers. On **Models & Routes**, press **Add route**, add models and order
them. A route can be the default model.

## Chat

- **New Chat** starts a conversation; **History** reopens or deletes earlier
  ones.
- The menu at the top picks this chat's model. The **Module** menu points the
  agent at an installed module.
- Attach images with **Add images**, or drop or paste them.
- **Reasoning and tool calls** shows or hides the model's reasoning and the
  tools it called.
- Enter sends; Shift + Enter starts a new line.

### Tools and the workspace

Compa reads, writes and edits files, runs shell commands, searches the web and
reads pages, schedules reminders and recurring jobs, and finds and installs
skills. Scheduled jobs run while Compa runs. The **Tools** page turns each tool
on or off.

File tools work in the workspace, `~/.compa/workspace` by default (**Config** →
**Agent** → **Workspace Directory**). With **Restrict to Workspace** on, the
default, they refuse paths outside it, except Compa's attachment and skill
folders and the paths `tools.allow_read_paths` and `tools.allow_write_paths`
match (regular expressions on the full path). Commands start in the workspace
and run as your user account: the restriction is a guard, not a sandbox.
**Config** → **Run Commands** controls commands.

The `message` tool sends to any chat on a connected channel, not only the one
the agent is answering: for example, a WhatsApp contact, from your account. To
keep it to the current chat, set **Send To** on its card on the **Tools** page
to **Current chat** (`tools.message.targets: "current_chat"`). To be asked
first, add an `ask` rule for `message` (see [Approvals](#approvals)).

### MCP servers

MCP servers add tools from other programs. Turn on **Config** → **MCP** →
**Enable MCP** and add a server there, or run
[`compa-kernel mcp add`](cli.md#mcp). A new or changed server starts when the
gateway restarts. Two settings are only in `config.json` and on the command
line:

- `trusted` (off by default): Compa believes the server's tool annotations,
  such as read-only, so [approval rules](#approvals) can match them.
- `cwd`: the folder a stdio server starts in; by default, the workspace.

The agent sees a server's tools as `mcp_<server>_<tool>`. A call fails after 5
minutes; `tools.mcp.call_timeout_seconds`, or a server's own
`call_timeout_seconds`, changes that.

### Approvals

`tools.approval` decides each tool call: run it, ask you, refuse it, or hide
the tool from the agent. The defaults:

```json
"approval": {
  "default": "allow",
  "rules": [
    {"source": "module:*", "hints": ["cost_unknown", "network", "external_writes"], "action": "ask"},
    {"tool": "install_skill", "action": "ask"}
  ]
}
```

Compa asks before a module capability that may cost money, use the network or
write outside your computer, and before it installs a skill. The first
matching rule decides; a call no rule matches gets `default`. A rule matches
when all the fields it sets match:

| Field | Matches |
|---|---|
| `tool` | The tool's name, such as `exec` or `install_skill`. |
| `source` | `builtin`, `mcp:<server>` or `module:<id>`. |
| `origin` | Any of `web` (browser chat), `cli` (`compa-kernel agent`), `chat` (chat apps) and `cron` (scheduled jobs and the heartbeat). |
| `hints` | MCP tool annotations (`read_only`, `destructive`, `idempotent`, `open_world`) and module effects (`cost_unknown`, `network`, `external_writes`). |

`tool` and `source` take `*` and `?` wildcards. Annotations count only from a
`trusted` server; other servers' tools count as `destructive` and
`open_world`.

`action` is `allow`, `ask`, `deny` or `hide` (the agent isn't offered the
tool). For `ask`, Compa posts the request in a chat, such as ``Approve running:
`df -h`? Reply /approve k7m2qp or /deny k7m2qp``, and waits up to 10 minutes.
A request from your message is asked in that chat; one from a scheduled job or
the heartbeat in the chat you last wrote to Compa from. A copy also goes to
the enabled webhooks, but you answer in your chat. Interactive
`compa-kernel agent` asks in the terminal.

**Config** → **Approvals** edits the rules; saving applies them at once. A
`rules` list you write replaces the defaults, so keep the ones you want:

```json
"rules": [
  {"tool": "exec", "origin": ["chat", "cron"], "action": "ask"},
  {"source": "mcp:github", "origin": ["chat"], "action": "hide"},
  {"source": "module:*", "hints": ["cost_unknown", "network", "external_writes"], "action": "ask"},
  {"tool": "install_skill", "action": "ask"}
]
```

## Voice

On **Voice**, choose a **Speech-to-text model** and a **Text-to-speech
model** from your connected providers, turn on **Enable voice**, pick
**Push-to-talk** or **Hands-free**, and save. Your recordings and the replies
to be spoken go to those providers.

In Chat, press the microphone to talk; the speaker button reads replies
aloud. Hands-free listens, answers aloud and listens again until you press
**End call**. Voice messages sent from chat apps are transcribed too.

## Channels

Talk to Compa from outside the browser:

- **Web chat:** the **Chat** page. No setup.
- **WhatsApp:** Compa is a linked device of your account.
- **Slack:** a Slack app's bot that you message directly.
- **Slack webhook** and **Teams webhook:** send only, for notifications.

On **Channels**, pick one, fill in its fields, turn on **Enable channel** and
save. If Compa says **Gateway restart required**, choose **Restart gateway**
in the status menu at the top.

### Who Compa answers

Compa answers only you: the accounts in a channel's **Allow From**, in direct
messages. It ignores group chats, rooms and threads.

To add your account, leave **Allow From** empty and message the bot. Compa
doesn't answer; your account shows under **Pairing Requests** on the channel's
page (at most 3 requests, kept 1 hour). **Approve** makes it the only entry in
**Allow From**. After that, Compa ignores everyone else. To add another account
of your own, edit **Allow From**.

Whoever can write from an account in **Allow From** can use Compa and its
tools, including commands (**Config** → **Run Commands** → **Allow Remote
Commands**, on by default). To be asked first, add an `ask` rule for `exec`
from `chat`.

### WhatsApp

Link your account in either way:

- On **Channels** → **WhatsApp**, press **Link WhatsApp** and scan the QR code.
- In a terminal, run `compa-kernel auth whatsapp`, scan the QR code, then
  restart the gateway.

To scan, in WhatsApp on your phone choose **Settings** → **Linked devices** →
**Link a device**.

Compa answers your "message yourself" chat. If you link it to a separate
number, it also answers the numbers in **Allow From**, written like
`15550003333`. Your own messages in other chats are never taken as
instructions. Photos, voice notes, audio, video and documents up to 50 MB reach
the agent; replies are text. Until an account is linked, the channel stays
idle and the log says so.

### Slack

Compa uses Socket Mode, so it needs no public address. It needs both tokens
from your Slack app:

- **Bot Token**, starting with `xoxb-`.
- **App Token**, starting with `xapp-`, with the `connections:write` scope.

Message the bot directly, then approve yourself under **Pairing Requests**.

### Webhooks and notifications

Results of scheduled jobs, heartbeat messages and approval requests go to your
chat, and a copy goes to every enabled Slack or Teams webhook. Add a webhook in
`config.json` under `channel_list`, with an `https` URL as its `default`
target:

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

For Teams, use `teams_webhook` and a Teams workflow webhook URL.

## Skills and modules

**Skills** are instructions for particular tasks. **Skill hub** searches
registries (ClawHub and GitHub by default) and installs skills; read a skill
before you install it. **Skills** lists the installed ones; **Import Skill**
adds a Markdown or ZIP file up to 1 MB. The agent can find and install skills
itself, after asking you.

**Modules** are separate programs that add capabilities. On **Modules**, enter
a module program's path and press **Install**, or put it in `~/.compa/modules`
and press **Refresh**. A capability the approval policy asks about runs only
after **Approve and run**, `/approve` in chat, or
`compa-kernel module-invoke --approve`.

## Logs

**Logs** shows the gateway's recent output. Log files:
[Where the logs are](troubleshooting.md#where-the-logs-are).
