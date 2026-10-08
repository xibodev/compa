# Changelog

Notable changes to Compa, newest first. Versions follow
[Semantic Versioning](https://semver.org/).

## Unreleased

### Added

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

- For Go programs that embed Compa: `session.SessionStore`'s `AddMessage` and
  `AddFullMessage` return an error.
- The web chat tells its clients when a turn starts and ends. A `turn.start`
  frame comes before the reply, and a `turn.end` frame after all of it, with
  `status` (`completed`, `error` or `aborted`) and, on an error, `error`. Both
  name the `message.send` frames the turn answers in `request_id` and
  `request_ids`, including messages sent while it ran. The chat page uses
  them, so it no longer takes a turn for finished when typing stops early, as
  when you send a message mid-turn.

### Fixed

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

## 3.0.0 - 2026-10-06

First release.
