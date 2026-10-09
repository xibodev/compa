# Security policy

## Report a vulnerability

Report it privately through GitHub: on this repository's **Security** tab,
choose **Report a vulnerability**
(https://github.com/xibodev/compa/security/advisories/new). Please don't open
a public issue, pull request or discussion about it. Describe what someone can
do, under which settings, and how to reproduce it. We answer in the advisory,
work on the fix there with you, and publish the advisory once a release fixes
the problem.

## Supported versions

Only the latest release gets security fixes. Update with
`compa-kernel update`, or by running the installer again.

## Scope

In scope:

- `compa` and `compa-kernel`, including the web UI, the gateway, the
  channels, tools, skills and module host.
- The installers, `install.sh` and `install.ps1`.
- The updater: `compa-kernel update` and the launcher's `POST /api/update`.
- The release workflow and the files it publishes.

Compa is a single-user assistant that runs as your account, and whoever is
signed in to it is trusted: running commands from the web UI or a chat you
allowed is what it's for. A problem in scope lets someone else get further than
the settings allow. Examples are a chat sender outside **Allow From**, someone
in a group chat with your account, one of your WhatsApp contacts, a website
open in your browser, a device on your network, another account on your
computer, or the author of a skill, module or MCP server you added.

Out of scope are flaws in the model providers, chat apps and other services
Compa connects to, and in skills, modules and MCP servers that aren't part of
this repository; report those to their authors.

## Verify a release

Each release lists the SHA-256 checksum of every other file in `SHA256SUMS`, which
the installers and the updater check. GitHub records which workflow run built
each archive, installer and `SHA256SUMS`:

```sh
gh attestation verify compa_1.2.3_linux_amd64.tar.gz --repo xibodev/compa
```
