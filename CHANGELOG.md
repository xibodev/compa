# Changelog

Notable changes to Compa, newest first. Versions follow
[Semantic Versioning](https://semver.org/).

## Unreleased

### Added

- `compa-kernel auth whatsapp`, and **Link WhatsApp** on the WhatsApp page,
  link a WhatsApp account by QR code and turn the channel on. See
  [WhatsApp](docs/use.md#whatsapp).
- On WhatsApp, photos, voice notes, audio, video and documents reach the
  agent, up to 50 MB per file.
- Results of scheduled jobs, heartbeat messages and approval requests also go
  to every enabled Slack and Teams webhook, to its `default` target. See
  [Webhooks and notifications](docs/use.md#webhooks-and-notifications).
- A program that embeds Compa can give the agent its own identity and leave
  out the memory and workspace parts of the prompt, with the `AGENT.md` keys
  `name`, `description`, `memory` and `privateWorkspace`. With
  `requireTools: true`, a turn whose model rejects tool calls moves to the
  route's next target, or fails with `agent.ErrToolsRequired` when no target
  takes tools, instead of going on without tools. See
  [Embed the Go runtime](docs/embedding.md).
- `COMPA_CHANNELS_<NAME>_ENABLED` turns a `channel_list` entry on or off, such
  as `COMPA_CHANNELS_WEB_ENABLED=true` for the web chat, and
  `COMPA_CHANNELS_WEB_STREAMING_ENABLED` turns its streaming on or off, as
  Telegram's does. Saving the config doesn't write them to `config.json`. See
  [Environment variables](docs/cli.md#environment-variables).

### Changed

- **Breaking:** default builds have only the web chat, WhatsApp, Slack and the
  Slack and Teams webhooks. Telegram, Discord, Delta Chat, DingTalk, Feishu,
  IRC, LINE, MaixCam, Matrix, MQTT, OneBot, QQ, VK, WeCom and WeChat are
  paused: only builds made with the `paused_channels` build tag have them, as
  well as `compa-kernel auth weixin` and `auth wecom`. They aren't maintained
  and may not compile. A config that turns one on still loads, and the gateway
  logs `Factory not registered` for it. See
  [Paused channels](docs/use.md#paused-channels).
- **Breaking:** a channel answers only its owner: the accounts in **Allow
  From**, in direct messages. It ignores group chats, rooms and threads, and
  `*` and group IDs admit no one. While **Allow From** is empty, a direct
  message makes a pairing request (at most 3 per channel, kept 1 hour);
  approving one makes that account the only entry in **Allow From**. After
  that, approving another request is refused. `dm_policy`, `group_policy` and
  `group_trigger` are removed; a config that has them still loads, and they
  are dropped. See [Who Compa answers](docs/use.md#who-compa-answers).
- **Breaking:** WhatsApp is the native channel, a linked device of your
  account. The bridge (`bridge_url`, `use_native`) and the **Chats** setting
  are gone, and so are the `whatsapp_native` build tag and
  `make build-whatsapp-native`. An entry of type `whatsapp_native` is read as
  `whatsapp`. Every build now links go.mau.fi/libsignal, which is GPL-3.0; see
  [NOTICE](NOTICE).
- **Breaking:** the `message` tool sends to any chat on any connected channel
  by default. `tools.message.targets: "current_chat"` keeps it to the chat the
  turn came from.
- **Breaking:** Slack needs the app-level token (`xapp-`) as well as the bot
  token; with only the bot token, the channel isn't set up.
- For Go programs that embed Compa: `session.SessionStore`'s `AddMessage` and
  `AddFullMessage` return an error.
- The web chat tells its clients when a turn starts and ends. A `turn.start`
  frame comes before the reply, and a `turn.end` frame after all of it, with
  `status` (`completed`, `error` or `aborted`) and, on an error, `error`. Both
  name the `message.send` frames the turn answers in `request_id` and
  `request_ids`, including messages sent while it ran. The chat page uses
  them, so it no longer takes a turn for finished when typing stops early, as
  when you send a message mid-turn.
- The gateway serves the web chat's session history to clients with the web
  chat token: `GET /web/sessions` lists the sessions, newest first (`offset`
  and `limit`), and `GET /web/sessions/{session_id}` returns one's transcript,
  with the JSON the launcher's `/api/sessions` answers. A session is found by
  the id the client opened `/web/ws` with, in any case.
- `--json` on `compa-kernel model` (show, set and `--clear`),
  `model auto-free`, `model ping`, `model roster`, `auth login`,
  `auth logout` and `auth status` prints one JSON document on stdout, with
  the launcher's field names, and `{"error": "..."}` with exit status 1 on
  failure. `model auto-free --json` also reports the default model it chose
  and each provider's models, probe model and latency. See
  [JSON output](docs/cli.md#json-output).
- [Gateway interface](docs/gateway.md) describes how programs that run the
  gateway talk to it: the pid file, the health and control routes, and the web
  chat's socket, files and history. `/health` and the pid file give the
  interface's version as `protocol`, now 1.

### Fixed

- `compa-kernel auth status` skips a `null` entry in a hand-edited
  `auth.json`; it crashed. It lists the providers in name order.
- `compa-kernel mcp add`, `mcp remove` and `mcp edit` save their change when
  a server has its own `call_timeout_seconds`; they refused every change.
- On Windows computers whose processor has AMX, such as recent Intel Xeon,
  `compa-kernel` could crash a while after Telegram connected, with
  `fatal error: found pointer to free object`.
- On Telegram, a placeholder that Telegram refuses to edit into the reply is
  deleted when the reply is sent as a new message; it stayed in the chat. A
  tool progress message Telegram refuses to edit stops animating, where it
  was edited again every three seconds.
- A voice message that isn't transcribed reaches the agent with its file's path
  where the message said `[voice]`; the path was put after the text, and
  `[voice]` stayed.
- On Telegram, the path of a file sent in a reply stays with the reply; it
  could take the place of the quoted message's photo, document or voice
  message, which the agent doesn't get.
- OneBot downloads the pictures, videos, files and voice messages of a message
  sent in its string format, with CQ codes, and reads its reply and escaped
  characters; the agent got the codes as text.
- **Gateway restart required** clears once the gateway reloads the saved
  config by itself, with `gateway.hot_reload` on or after `/reload` in a chat;
  it stayed until the gateway restarted.
- When a background sub-agent that another sub-agent started fails on an
  internal error, the sub-agent that started it hears of the failure; it heard
  nothing.
- A command that matches **Config** → **Run Commands** → **Command Whitelist**
  skips the built-in dangerous patterns, as the setting says; the whitelist let
  no command through. The **Command Blacklist** still blocks it, and the
  setting's pattern tester now says so; it called such a command allowed.
- The agent can read the skills in `~/.compa/skills` and in the built-in
  skills folder, as its prompt tells it to; with **Restrict to Workspace** on,
  the default, `read_file` refused them as outside the workspace.
- When the model rejects tool calls, the chat is told that the reply comes
  without tools, `agent.llm.retry` reports the reason `tools_unsupported`, and
  the retried request no longer tells the model to always use tools; only the
  log said so.
- When a message can't be saved to the conversation, for example on a full
  disk, the chat says so, the turn emits `agent.error` with stage
  `session_save`, and gateway.log has the error; only stderr said so.
- On Windows, the chat page finds earlier conversations when the workspace is
  written as `~\folder`; it looked in the home folder instead.

## 3.0.0 - 2026-10-06

First release.
