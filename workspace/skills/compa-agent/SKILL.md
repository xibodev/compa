---
name: compa-agent
description: "Configure, extend, debug, test, or contribute to Compa itself. Use for Compa CLI, dashboard, gateway, models, credentials, skills, modules, tools, channels, sessions, and repository internals."
metadata: {"compa":{"category":"development"}}
---

# Compa Agent

Use this skill for work on Compa itself. Treat the repository, current CLI help, API behavior, and checked-in documentation as the source of truth.

## Operating Rules

- Prefer Compa names, commands, paths, and configuration keys.
- Inspect the current code before proposing changes.
- Keep credentials out of logs, reports, screenshots, commits, and chat output.
- Test with a temporary `COMPA_HOME` and services bound to localhost.
- Distinguish configured, reachable, authenticated, and inference-verified provider states.
- A successful HTTP response proves little: check what the user sees and what was saved.
- Keep the user's configuration unless they ask to change it.

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

The state root is `~/.compa` (`COMPA_HOME` moves it): settings in `config.json`, secrets in `.security.yml` beside it, and provider keys in `auth.json`. The agent workspace is `workspace` in the state root, unless `agents.defaults.workspace` names another. The web UI has Chat, Models (providers and their keys), Channels, Skill hub, Skills, Modules, Tools, Config, Voice and Logs. Verify changes through the narrowest relevant unit tests and an end-to-end user-visible check.
