# Use Compa

Compa's web UI is at http://127.0.0.1:18800. The sidebar has **Chat**,
**Models** and **Channels**; the **Agent** group (**Skill hub**, **Skills**,
**Modules**, **Tools**); and the **Services** group (**Config**, **Voice**,
**Logs**).

## First run and your password

The first time, Compa asks you to set a password of at least 8 characters.
After that you sign in with it; when Compa opens your browser itself on this
computer, it signs you in for you. Restarting Compa signs every browser out.

- Change the password: **Config** → **Compa app** → **Login Password**.
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
a restart, choose **Restart gateway** in the status menu at the top. WeChat and
WeCom connect by scanning a QR code.

Fill in **Allow From** with your own user ID before you turn a channel on.
When it's empty, anyone who can message the bot can use Compa and its tools.
Because **Allow Remote Commands** is on by default, that includes running
commands on your computer. To keep chat apps from running commands at all,
turn off **Allow Remote Commands** under **Config** → **Run Commands**.

## Skills and modules

**Skills** are instructions the agent follows for particular tasks. **Skill
hub** searches skill registries (ClawHub and GitHub by default) and installs
skills into the workspace. Registry skills are third-party content, so read one
before you install it. **Skills** lists the installed ones; **Import Skill**
adds a Markdown or ZIP file of up to 1 MB. The agent can also find and install
skills itself; turn off `find_skills` and `install_skill` on the **Tools** page
if you don't want that.

**Modules** are separate programs that add capabilities. On **Modules**, enter
the path to a module program and press **Install**, or drop the program into
`~/.compa/modules` and press **Refresh**. **Disable** and **Remove** keep the
module's stored data. In Chat, choose a module in the **Module** menu. A
capability that needs approval, such as one that may cost money, runs only when
you press **Approve and run** on the Modules page; the agent can't approve for
you.

## Logs

**Logs** shows the gateway's recent output, with a level menu and **Clear
logs**. The log files are in `~/.compa/logs`; see
[Troubleshooting](troubleshooting.md#where-the-logs-are).
