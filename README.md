<p align="center">
  <img src="docs/compa-logo.svg" alt="Compa" width="240">
</p>

Compa is a personal AI assistant that runs on your computer. You chat with it
in your browser, on WhatsApp or on Slack, pick the models it uses, and it works
with files, runs commands and searches the web for you. "Compa" is Mexican
Spanish slang for pal.

## Install

Windows (PowerShell):

```powershell
irm https://github.com/xibodev/compa/releases/latest/download/install.ps1 | iex
```

macOS and Linux:

```sh
curl -fsSL https://github.com/xibodev/compa/releases/latest/download/install.sh | sh
```

Compa runs on Windows 10 or later, macOS 12 or later and Linux. More in
[docs/install.md](docs/install.md).

## Your first chat

1. The installer starts Compa, which opens its setup page in your browser. Set
   a password. If you closed the page, choose **Open Console** in Compa's tray
   menu.
2. Go to **Models**. Press **Try free providers**, or connect a provider with an
   API key or a model server on your computer.
3. Go to **Chat** and ask something.

## What it does

- **Tools:** files in its workspace folder, shell commands, web search and web
  pages, scheduled jobs, MCP servers.
- **Models:** API-key providers such as OpenAI, Anthropic, Google Gemini,
  OpenRouter, Groq and Mistral AI; local servers such as Ollama, LM Studio and
  LocalAI; any OpenAI- or Anthropic-compatible endpoint; free keyless
  providers; and more through an extension app. A route tries a list of models
  in order until one answers.
- **Voice:** dictation and spoken replies, push-to-talk or hands-free.
- **Chat apps:** WhatsApp, as a linked device of your account, and a Slack bot.
  Compa answers only you. Slack and Teams webhooks receive its notifications.
- **Skills and modules:** instructions for particular tasks, and programs that
  add capabilities.
- **Private by default:** settings, chats and keys stay in `~/.compa`; messages
  go only to the model providers you choose. The web UI is password-protected
  and listens only on this computer unless you turn on LAN access.
- The web UI is in English, Czech, Brazilian Portuguese, Simplified Chinese and
  Bengali.

## Documentation

- [Install](docs/install.md): installers, updates, uninstalling, ports, LAN
  access, building from source.
- [Use Compa](docs/use.md): models, chat, tools, approvals, voice, chat apps,
  skills, modules.
- [Troubleshooting, privacy and security](docs/troubleshooting.md)
- [Command line](docs/cli.md)
- [Gateway interface](docs/gateway.md), for programs that run the gateway.
- [Embed the Go runtime](docs/embedding.md)

Contributing: [CONTRIBUTING.md](CONTRIBUTING.md). Security reports:
[SECURITY.md](SECURITY.md). Changes: [CHANGELOG.md](CHANGELOG.md).

## License

MIT, with exceptions; see [LICENSE](LICENSE) and [NOTICE](NOTICE).
