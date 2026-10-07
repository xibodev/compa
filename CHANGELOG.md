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

## 3.0.0 - 2026-10-06

First release.
