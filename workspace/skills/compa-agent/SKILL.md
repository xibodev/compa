---
name: compa-agent
description: "Configure, extend, debug, test, or contribute to Compa itself. Use for Compa CLI, dashboard, gateway, models, credentials, skills, modules, tools, channels, sessions, and repository internals."
metadata: {"compa":{"category":"development"}}
---

# Compa Agent

Use this skill for work on Compa itself. Treat the repository, current CLI help, API behavior, and checked-in documentation as the source of truth.

## Operating Rules

- Prefer Compa names, commands, paths, and configuration keys.
- Inspect current code before proposing changes; do not assume donor-project behavior still applies.
- Keep credentials out of logs, reports, screenshots, commits, and chat output.
- Use temporary state roots and loopback-only services for destructive or automated testing.
- Distinguish configured, reachable, authenticated, and inference-verified provider states.
- A successful HTTP response is not sufficient UAT evidence; verify the user-visible outcome and persisted state.
- Preserve existing user configuration unless a requested migration explicitly changes it.

## Common Commands

```bash
compa-kernel onboard
compa-kernel status
compa-kernel gateway
compa-kernel model roster
compa-kernel model ping <instance>
compa-kernel skills list
compa-kernel version
```

Use `compa-kernel --help` and the relevant subcommand help before relying on command syntax.

## Workspace

The default state root is `~/.compa` and the default agent workspace is `~/.compa/workspace`. The dashboard exposes chat, models, credentials, channels, skills, modules, tools, configuration, and logs. Verify changes through the narrowest relevant unit tests and an end-to-end user-visible check.
