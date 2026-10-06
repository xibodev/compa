# Troubleshooting

On Windows, `~/.compa` below means `%USERPROFILE%\.compa`.

## The gateway isn't running

The gateway is the `compa-kernel` process that runs chats, tools, channels and
scheduled jobs. Compa starts it for you. When it isn't running, Chat says so
and the status menu at the top of the page shows **Gateway: Stopped** or
**Gateway: Error**.

1. Open the status menu and choose **Start gateway**. The tray icon's
   **Restart Service** does the same.
2. If it stops again, open **Logs**, or read `~/.compa/logs/gateway.log` and
   `~/.compa/logs/launcher.log`. Common causes:
   - `compa-kernel` is missing. Compa runs the `compa-kernel` in its own
     folder, and otherwise looks on your `PATH`; `launcher.log` then warns that
     it found no `compa-kernel` in Compa's folder. Install again, or keep both
     programs in one folder.
   - `config.json` doesn't load, for example after a hand edit. The Config page
     then says **Failed to load configuration**. A key Compa doesn't know fails
     the load too, and the error names it, such as `tools.future_tool`. Fix the
     file, or use **Factory Reset** (see [Start over](#start-over)).
   - Port 18790 is taken; see the next section.

## Port already in use

- **18800, the web UI.** Compa exits right after it starts, and
  `launcher.log` says `Failed to open launcher listener(s)`. Often Compa is
  already running: look for its tray icon and choose **Open Console**.
  Otherwise start Compa on another port, `compa -port 18801`; to keep that
  port, set **Service Port** under **Config** → **Compa app**.
- **18790, the gateway.** The gateway stops soon after it starts, and **Logs**
  or `gateway.log` shows the port error. Close the program using the port, or
  give the gateway another one in `~/.compa/config.json` (or **Config** →
  **Raw Config**), then restart the gateway:

  ```json
  "gateway": { "host": "localhost", "port": 18795 }
  ```

## Forgot the password

Quit Compa (tray icon → **Quit**), delete `~/.compa/launcher-auth.db` and
start Compa again: it opens the setup page, where you set a new password.

On macOS and Linux, `compa -password -` instead asks for the new password,
sets it and exits (`~/.local/bin/compa` if `compa` isn't on your `PATH`).

## Where the logs are

In `~/.compa/logs`:

| File | What's in it |
|---|---|
| `launcher.log` | `compa`: the web UI and how it runs the gateway. |
| `gateway.log` | The gateway: chats, tools, channels and provider errors. |
| `launcher_panic.log`, `gateway_panic.log` | Crash reports, if there were any. |
| `launcher.out` | macOS and Linux: output of the Compa the installer started. |

The **Logs** page shows the gateway's recent output and has a level menu. For
more detail everywhere, start Compa with `compa -d`.

`launcher.log` and `gateway.log` start over in a new file at 10 MB, and Compa
keeps up to 5 of each (`logging.max_size_mb` and `logging.max_files` in
`config.json`). With `logging.redact_secrets`, on by default, Compa masks the
keys and tokens it knows before it writes a log line.

## A model doesn't answer

- **"No model selected"**: set a default model on the Models page, or pick a
  model in the chat's model menu.
- **"Model … is not available"**: its provider was removed or turned off, or
  the model left the provider's list. Open the provider's card and press
  **Refresh models**, or pick another model.
- **The provider returns errors**: open its card and press **Test
  connection**. Replace a wrong or expired key with **Edit**. For Ollama or
  LocalAI, check that the server is running at the address on the card.
- **Free providers** can be busy (rate limited) or stop answering. Press **Try
  free providers** again, or connect another provider.
- A **route** falls back to its next model when one fails, so a route with two
  providers keeps answering when one is down.
- The **Logs** page shows the error the provider returned.

## An approval doesn't arrive

When [`tools.approval`](use.md#approvals) asks about a call:

- If a process hook has `intercept: ["approve_tool"]`, it decides instead of
  you, and no request is posted.
- A request that comes from someone else's message or from a scheduled job
  goes to the conversation you last wrote to Compa from, in the browser or a
  chat app. Until you have written to Compa there, such requests are refused:
  send Compa a message once.
- `compa-kernel agent -m` can't ask, so it refuses such calls; interactive
  `compa-kernel agent` asks in the terminal.
- **"No request … is waiting for an answer"**: the ID is wrong, the request was
  already answered, or 10 minutes passed, which refuses it. Ask again.
- Only you can answer. In a chat app that means a sender listed by ID in the
  channel's **Allow From**.
- A call refused without any request was denied or hidden by the policy, or
  was too long (over 3000 characters) to show you. The Modules page and
  `module-invoke` don't post requests either: there, **Approve and run** or
  `--approve` is the approval.

## A saved change doesn't take effect

Saving the model selections, `tools.approval`, or a channel's **Allow From**,
**DM Policy** or **Group Policy**, and approving a pairing request, applies to
the running gateway at once, together with every other change saved before.
A change to a tool, an MCP server, a channel, the workspace, `isolation`,
`commands` or `hooks` shows **Gateway restart required**: choose **Restart
gateway** in the status menu at the top. Other settings apply the next time
the gateway starts.

## Start over

- **Settings only**: **Config** → **Factory Reset**, or
  `compa-kernel config reset`. This saves a dated backup beside `config.json`,
  then resets it to the defaults. The secrets in `.security.yml` and
  `auth.json` stay, but provider connections are part of `config.json`, so you
  connect providers again.
- **Everything**: quit Compa, then delete `~/.compa`, or rename it to keep a
  copy. That removes settings, keys, the password, chat history, the workspace
  and its files, skills, modules and logs. The next start is a first run.

## Privacy and security

### What leaves your computer

Compa runs on your computer, but it isn't offline:

- Your messages, attached images, and what the agent adds to the conversation,
  such as file contents or command output it read, go to the model provider
  that answers. You choose providers on the Models page. The free providers
  that **Try free providers** adds are third-party services with their own
  terms.
- Voice recordings go to the provider of your speech-to-text model; replies to
  be spoken go to the provider of your text-to-speech model.
- Tools reach the internet when the agent uses them. Web search goes to the
  model's provider when it has its own search, and otherwise to the service set
  on **Tools** → **Web Search**; with no search API key set, that is Sogou.
  Reading a page fetches it from its site, and a command can do whatever it
  does.
- Chat apps you connect carry messages through their own servers. The skill
  hub contacts its registries when you search or install. `compa-kernel update`
  and the installers download from GitHub. MCP servers, modules and an
  extension app you add make their own connections.

Before tool output goes to a model, Compa replaces the keys and tokens it has
stored with `[FILTERED]`.

### Where your data and secrets are

Everything is in `~/.compa`:

| Path | What |
|---|---|
| `config.json` | Settings, provider connections, model routes. |
| `.security.yml` | Channel tokens and other secret settings. |
| `auth.json` | Provider API keys and sign-ins, and the extension's shared secret. |
| `launcher-auth.db` | The dashboard password, as a bcrypt hash. |
| `workspace/` | The agent's files, chat history, memory and skills. |
| `state/` | Scheduled jobs (`state/cron/jobs.json`) and the terminal chat's input history. |
| `pairing.json` | Pairing requests from chat apps waiting for your answer. |
| `modules/`, `logs/` | Installed modules and log files. |

Secrets are stored in plain text in `.security.yml` and `auth.json`; a secret
in `.security.yml` can instead be a `file://` reference to a file beside it.
Compa has no option to encrypt them. `config.json` shows `[NOT_HERE]` in place
of a secret that is set. `config.json`, `.security.yml` and `auth.json` are
created readable only by your account (file mode 600 on macOS and Linux), but
anyone who can read your user files can read them.

### The dashboard password

The password protects the web UI. Compa stores only its bcrypt hash and allows
10 sign-in attempts per minute from each address. A sign-in lasts until you
sign out or Compa restarts, at most 31 days; the one-time link Compa opens on
this computer works for 2 minutes. The password doesn't encrypt your files.

### LAN access is off by default

The web UI listens only on this computer unless you start Compa with `-public`
or `-host`, or turn on **Enable LAN Access**, and the gateway stays on this
computer unless you set `gateway.host`. Read [LAN access](install.md#lan-access)
before you turn it on.

### Tools run as you

- Tools and commands run with your user account's permissions. **Restrict to
  Workspace** (on by default) and the command blacklist check what the agent
  asks for, not what a program it starts does; see
  [Tools and the workspace](use.md#tools-and-the-workspace). With
  `isolation.enabled` in `config.json`, commands, process hooks and stdio MCP
  servers start isolated: on Linux with bubblewrap (`bwrap`), which shows them
  system folders read-only, `~/.compa` and the folders in
  `isolation.expose_paths`; on Windows with a restricted token. It isn't
  available on macOS.
- Turn tools off on the **Tools** page; **Config** → **Run Commands** →
  **Allow Commands** turns commands off.
- **Allow Remote Commands** is on by default, so the browser chat and every
  chat app you connect can run commands. If a channel's **Allow From** has `*`,
  or its `dm_policy` or `group_policy` is `open`, anyone who can message that
  bot can run commands on your computer. List only your own IDs in **Allow
  From** ([Channels](use.md#channels)), turn **Allow Remote Commands** off, or
  add an `ask` rule for `exec` ([Approvals](use.md#approvals)).
- Skills and modules are third-party content; install only ones you trust.
  Modules run as separate programs under your account.
