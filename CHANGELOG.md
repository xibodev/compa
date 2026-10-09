# Changelog

Notable changes to Compa, newest first. Versions follow
[Semantic Versioning](https://semver.org/).

## 4.0.0 - 2026-10-08

### Added

- WhatsApp is a linked device of your account: link it with **Link WhatsApp**
  on the WhatsApp page or `compa-kernel auth whatsapp`. Photos, voice notes,
  audio, video and documents up to 50 MB reach the agent. See
  [WhatsApp](docs/use.md#whatsapp).
- Results of scheduled jobs, heartbeat messages and approval requests also go
  to every enabled Slack and Teams webhook. See
  [Webhooks and notifications](docs/use.md#webhooks-and-notifications).
- Clearer chat errors when a provider wants payment, the conversation is longer
  than the model's context window, or the model can't take tools or images.
- Google Gemini replies stream as they're written.
- A program that embeds Compa can give the agent its own identity with the
  `AGENT.md` keys `name`, `description`, `memory` and `privateWorkspace`, and
  require tool calls with `requireTools: true`. See
  [Embed the Go runtime](docs/embedding.md).
- `COMPA_CHANNELS_<NAME>_ENABLED` turns a `channel_list` entry on or off, and
  `COMPA_CHANNELS_WEB_STREAMING_ENABLED` the web chat's streaming. See
  [Environment variables](docs/cli.md#environment-variables).
- `--json` on `compa-kernel model`, `model auto-free`, `model ping`,
  `model roster`, `auth login`, `auth logout` and `auth status`. See
  [JSON output](docs/cli.md#json-output).
- [Gateway interface](docs/gateway.md) documents how programs talk to the
  gateway, as protocol 1. The web chat sends `turn.start` and `turn.end`
  frames, and serves its session history at `/web/sessions`.
- Each release archive holds `THIRD_PARTY_NOTICES`, the licenses of the
  third-party code in the programs. The installers keep it with `LICENSE` and
  `NOTICE`: beside the programs on Windows, in `~/.local/share/doc/compa` on
  macOS and Linux.

### Changed

- **Breaking:** Compa's chat apps are WhatsApp and Slack, plus the Slack and
  Teams webhooks for notifications. Telegram, Discord, Delta Chat, DingTalk,
  Feishu, IRC, LINE, MaixCam, Matrix, MQTT, OneBot, QQ, VK, WeCom and WeChat
  are no longer included. A config that turns one on still loads.
- **Breaking:** Compa answers only its owner: the accounts in a channel's
  **Allow From**, in direct messages. Group chats, rooms and threads are
  ignored. While **Allow From** is empty, your first message makes a pairing
  request to approve. `dm_policy`, `group_policy` and `group_trigger` are
  removed; a config that has them still loads. See
  [Who Compa answers](docs/use.md#who-compa-answers).
- **Breaking:** the WhatsApp bridge (`bridge_url`, `use_native`) is gone. Every
  build links go.mau.fi/libsignal, which is GPL-3.0; see [NOTICE](NOTICE).
- **Breaking:** the `message` tool sends to any chat on a connected channel by
  default; `tools.message.targets: "current_chat"` keeps it to the current
  chat.
- **Breaking:** Slack needs the app token (`xapp-`) as well as the bot token.
- **Breaking** for Go programs that embed Compa: the module path is
  `github.com/xibodev/compa/v4`, and `session.SessionStore`'s `AddMessage`
  and `AddFullMessage` return an error.
- llmgw-core 1.9.1, llm-provider-auth 1.0.1 and llm-translate 0.4.0.

### Fixed

- Built with Go 1.26.9 and golang.org/x/net 0.60.0, which fix vulnerabilities
  in HTTP/2, `net/http`, `crypto/tls` and `os`.
- A `config.json` saved by Compa 1.0.0 loads; its retired
  `default_token_budget` setting is dropped.
- `compa-kernel auth status` no longer crashes on a `null` entry in
  `auth.json`, and lists providers in name order.
- `compa-kernel mcp add`, `mcp remove` and `mcp edit` save their change when a
  server has its own `call_timeout_seconds`.
- **Gateway restart required** clears when the gateway reloads the config by
  itself.
- A failed background sub-agent reports its failure to the sub-agent that
  started it.
- **Command Whitelist** lets matching commands skip the built-in dangerous
  patterns, as the setting says; the **Command Blacklist** still applies.
- The agent can read the skills in `~/.compa/skills` and the built-in skills
  folder with **Restrict to Workspace** on.
- When the model rejects tool calls, the chat says the reply comes without
  tools.
- When a message can't be saved, for example on a full disk, the chat says so.
- On Windows, earlier conversations are found when the workspace is written as
  `~\folder`.

## 3.0.0 - 2026-10-06

First release.
