# Contributing to Compa

Thanks for helping. Open an issue to discuss a larger change before you start
on it; report security problems as [SECURITY.md](SECURITY.md) describes.

## Tools

- Go 1.26.6, the version in `go.mod`.
- Node.js 22.13 or later, and pnpm, the version `packageManager` pins in
  `web/frontend/package.json`.
- For the checks: [golangci-lint](https://golangci-lint.run) v2.14.0,
  shellcheck and PSScriptAnalyzer; PowerShell 7 runs the Windows installer's
  tests.

## Build

Every Go command takes the build tags `goolm,stdjson`.

Build the web UI first, as [Run from source](docs/install.md#run-from-source)
shows: `compa` embeds `web/backend/dist` when it links, and a stale or missing
UI there isn't reported. `make product` builds both programs into `build/`;
`make help` lists the other targets.

## Test

```sh
go test -tags goolm,stdjson -p 1 ./...
```

Run the Go tests one package at a time (`-p 1`), as CI does. CI runs them on
Linux, Windows and macOS, and again with `-race` on Linux (`CGO_ENABLED=1`).

The web UI, in `web/frontend`:

```sh
pnpm test
pnpm run build
pnpm exec playwright install chromium   # once
pnpm run test:e2e                       # uses the build above
```

The installers have their own tests: `bash scripts/install/test-install.sh` on
Linux and `pwsh scripts/install/test-install.ps1` on Windows, where
`-Shell powershell` runs them under Windows PowerShell 5.1.

## Check

`make check` runs, without changing any file: `go mod tidy -diff`, formatting,
`go vet`, golangci-lint with `.golangci.yml`, the doc links, and the Go and
web UI tests (run `pnpm install --frozen-lockfile` in `web/frontend` first).
CI also runs `govulncheck`; in `web/frontend`, `npx tsc -b`, `pnpm run lint`
and `pnpm run format`, which checks the formatting with Prettier;
`node brand/check.mjs` for the brand assets; and PSScriptAnalyzer on
`install.ps1`. `make fmt` formats the Go code, and `pnpm run format:write` in
`web/frontend` the web UI's.

CI also writes the `THIRD_PARTY_NOTICES` a release ships, on Linux, Windows and
macOS, with `scripts/notices.sh` (see `cmd/notices`). It fails when a Go module
compiled into `compa` or `compa-kernel` has no license file, so check that a
new dependency has one.

## Pull requests

- Keep a change to one concern, with a test for each behavior it changes.
- Title the pull request like the existing commits, `fix: what changed`, with
  `feat`, `fix`, `docs` or `chore` first: it becomes the title of the squashed
  commit.
- Name the issue it fixes, as `Fixes #12`, so merging closes the issue.
- Add a line under **Unreleased** in [CHANGELOG.md](CHANGELOG.md) for a change
  users notice.
- Update the docs in `docs/` when you change what they describe.
