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

## 3.0.0 - 2026-10-06

First release.
