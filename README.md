<p align="center">
  <img src="docs/compa-logo.svg" alt="Compa" width="240">
</p>

Compa is a personal AI assistant that runs on your computer. You chat with it
in your browser, pick the models it uses, and it can work with files, run
commands and search the web for you. "Compa" is Mexican Spanish slang for pal.

## Install

Windows (PowerShell):

```powershell
irm https://github.com/xibodev/compa/releases/latest/download/install.ps1 | iex
```

macOS and Linux:

```sh
curl -fsSL https://github.com/xibodev/compa/releases/latest/download/install.sh | sh
```

Compa runs on Windows 10 or later, macOS 12 or later and Linux. Where it
installs, options, updating and uninstalling: [docs/install.md](docs/install.md).

## Your first chat

1. On your desktop the installer starts Compa, which opens its setup page in
   your browser: set a password. If you closed the page, choose **Open
   Console** in Compa's tray menu.
2. Go to **Models**. Press **Try free providers**, or connect a provider with
   an API key or a model server on your computer.
3. Go to **Chat** and ask something.

## What it does

- Chat with tools: files in its workspace folder, shell commands, web search
  and web pages, scheduled jobs.
- Models from many providers: API-key providers such as OpenAI, Anthropic,
  Google Gemini, OpenRouter, Groq and Mistral AI; local servers (Ollama,
  LocalAI); your own OpenAI- or Anthropic-compatible endpoints; free keyless
  providers; and more through an optional extension app. A route tries a list
  of models in order until one answers.
- Voice: dictation and spoken replies, push-to-talk or hands-free.
- Chat apps: talk to Compa from Telegram, Discord, Slack and WhatsApp.
- Skills (instructions for particular tasks, with a skill hub to find more) and
  installable modules that add capabilities.
- Compa keeps its settings, chats and keys in `~/.compa` on your computer;
  messages go to the model providers you choose. A password protects the web
  UI, which listens only on this computer unless you turn on LAN access.
- The web UI is in English, Czech, Brazilian Portuguese, Simplified Chinese and
  Bengali.

## Documentation

- [Install](docs/install.md): installers, updates, uninstalling, ports, LAN access.
- [Use Compa](docs/use.md): models, chat, voice, channels, skills, modules, logs.
- [Troubleshooting, privacy and security](docs/troubleshooting.md)
- [Command line](docs/cli.md): `compa` flags and `compa-kernel` commands.
- [Embed the Go runtime](docs/embedding.md)

## For developers

To build Compa from source, see
[Run from source](docs/install.md#run-from-source). Tests, checks and how to
contribute are in [CONTRIBUTING.md](CONTRIBUTING.md), how to report a security
problem in [SECURITY.md](SECURITY.md), and the changes in
[CHANGELOG.md](CHANGELOG.md).

## License

MIT. See [LICENSE](LICENSE), and [NOTICE](NOTICE) for upstream attribution and
for the Compa name and artwork, which the MIT License doesn't cover.
