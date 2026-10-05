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

The installer checks the download against the release's SHA-256 checksums,
installs two programs, `compa` and `compa-kernel`, and, in a terminal on your
desktop, starts Compa. Compa runs on Windows 10 or later, macOS 12 or later
and Linux. Where it installs, options, updating and uninstalling:
[docs/install.md](docs/install.md).

## Your first chat

1. Compa opens http://localhost:18800 in your browser (open it yourself if it
   doesn't). Set a password.
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
- Chat apps: talk to Compa from Telegram, Discord, Slack, WhatsApp, Matrix and
  more.
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

`compa-kernel` is the agent runtime and works on its own in a terminal; see
[docs/cli.md](docs/cli.md). The same runtime is a Go package you can run inside
your own program; see [docs/embedding.md](docs/embedding.md).

### Build from source

You need Go 1.26.6, Node.js 22 and pnpm. The web UI is built first, because
`compa` embeds it when it links.

```sh
cd web/frontend
pnpm install --frozen-lockfile
pnpm run build:backend
cd ../..
go build -tags goolm,stdjson -o build/compa-kernel ./cmd/compa-kernel
go build -tags goolm,stdjson -o build/compa ./web/backend
```

On Windows, name the outputs `compa-kernel.exe` and `compa.exe`. Keep both
programs in one folder: `compa` runs the `compa-kernel` beside it. More in
[docs/install.md](docs/install.md#run-from-source); tests, checks and how to
contribute are in [CONTRIBUTING.md](CONTRIBUTING.md), and how to report a
security problem in [SECURITY.md](SECURITY.md). Changes are listed in
[CHANGELOG.md](CHANGELOG.md).

## License

MIT. See [LICENSE](LICENSE), and [NOTICE](NOTICE) for upstream attribution and
for the Compa name and artwork, which the MIT License doesn't cover.
