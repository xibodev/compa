# Troubleshooting

On Windows, `~/.compa` below means `%USERPROFILE%\.compa`.

## The gateway isn't running

The gateway is the `compa-kernel` process that runs chats, tools, channels and
scheduled jobs. Compa starts it for you. When it isn't running, Chat says so
and the status menu at the top of the page shows **Gateway: Stopped** or
**Gateway: Error**.

1. Open the status menu and choose **Start gateway** or **Restart gateway**.
   The tray icon's **Restart Service** does the same.
2. If it stops again, open **Logs**, or read `~/.compa/logs/gateway.log` and
   `~/.compa/logs/launcher.log`. Common causes:
   - `compa-kernel` is missing. Compa runs the `compa-kernel` in its own
     folder, and otherwise looks on your `PATH`; `launcher.log` then warns that
     it found no `compa-kernel` in Compa's folder. Install again, or keep both
     programs in one folder.
   - `config.json` doesn't load, for example after a hand edit. The Config page
     then says **Failed to load configuration**. Fix the file, or use
     **Factory Reset** (see [Start over](#start-over)).
   - Port 18790 is taken; see the next section.

## Port already in use

- **18800, the web UI.** Compa exits right after it starts, and
  `launcher.log` says `Failed to open launcher listener(s)`. Often Compa is
  already running: look for its tray icon and choose **Open Console**.
  Otherwise start Compa on another port, `compa -port 18801`, or change
  **Service Port** under **Config** → **Compa app**.
- **18790, the gateway.** The gateway stops soon after it starts, and **Logs**
  or `gateway.log` shows the port error. Close the program using the port, or
  give the gateway another one in `~/.compa/config.json` (or **Config** →
  **Raw Config**), then restart the gateway:

  ```json
  "gateway": { "host": "localhost", "port": 18795 }
  ```

## Forgot the password

Quit Compa (tray icon → **Quit**), then run:

```sh
compa -password "your-new-password"
```

This sets the new password (at least 8 characters) and exits. Start Compa and
sign in with it. If `compa` isn't on your `PATH`, use its full path:
`& "$env:LOCALAPPDATA\Programs\Compa\compa.exe"` in PowerShell, or
`~/.local/bin/compa` on macOS and Linux. Your shell may keep the command in its
history.

## Where the logs are

In `~/.compa/logs`:

| File | What's in it |
|---|---|
| `launcher.log` | `compa`: the web UI and how it runs the gateway. Not written with `-console` unless you also pass `-d`. |
| `gateway.log` | The gateway: chats, tools, channels and provider errors. |
| `launcher_panic.log`, `gateway_panic.log` | Crash reports, if there were any. |
| `launcher.out` | macOS and Linux: output of the Compa the installer started. |

The **Logs** page shows the gateway's recent output and has a level menu. For
more detail everywhere, start Compa with `compa -d`.

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
| `workspace/` | The agent's files, chat history, memory, skills and scheduled jobs. |
| `modules/`, `logs/` | Installed modules and log files. |

The secrets are stored unencrypted by default. `config.json`, `.security.yml`
and `auth.json` are created readable only by your account (file mode 600 on
macOS and Linux), but anyone who can read your user files can read them.

### The dashboard password

The password protects the web UI. Compa stores only its bcrypt hash and allows
10 sign-in attempts per minute from each address. A sign-in lasts until you
sign out or Compa restarts, at most 31 days. When Compa opens your browser
itself, a one-time link that works for 5 minutes signs you in. The password
doesn't encrypt your files.

### LAN access is off by default

The web UI and the gateway listen only on this computer unless you start
Compa with `-public` or `-host`, or turn on **Enable LAN Access**. If you turn
it on, set the password first: until one is set, whoever opens the page first
sets it. Compa serves plain HTTP, so the password and your chats cross the
network unencrypted, and the gateway port 18790 is reachable too. **Allowed
Network CIDRs** limits which addresses can reach the web UI.

To use Compa from another computer without LAN access, forward the port over
SSH from that computer, then open http://127.0.0.1:18800 there:

```sh
ssh -L 18800:127.0.0.1:18800 you@the-computer-running-compa
```

### Tools run as you

- Tools and commands run with your user account's permissions. By default the
  operating system doesn't sandbox them.
- **Restrict to Workspace** is on by default: file tools refuse paths outside
  the workspace, except Compa's attachment folder and paths you allow in
  `config.json`; commands start in the workspace and are blocked when they
  name paths outside it. Commands that match the command blacklist are blocked
  too. These checks look at what the agent asks for; a program it starts can
  still reach anything your account can.
- Turn tools off on the **Tools** page. **Config** → **Run Commands** →
  **Allow Commands** turns commands off entirely.
- **Allow Remote Commands** is on by default. It lets chats other than the
  terminal run commands, which includes the browser chat and every chat app
  you connect. A channel whose **Allow From** is empty answers anyone, so
  anyone who can message a connected bot can then run commands on your
  computer. Fill in **Allow From** on each channel, or turn **Allow Remote
  Commands** off.
- Skills and modules are third-party content; install only ones you trust.
  Modules run as separate programs under your account. A module capability
  that needs approval runs only when you press **Approve and run** on the
  Modules page.
