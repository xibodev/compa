# Contributing to Compa

Thanks for helping. Open an issue to discuss a larger change before you start
on it; report security problems as [SECURITY.md](SECURITY.md) describes.

## Tools

- Go 1.26.6, the version in `go.mod`.
- Node.js 22 and pnpm, the version `packageManager` pins in
  `web/frontend/package.json`.
- For the checks: [golangci-lint](https://golangci-lint.run) v2.14.0 and
  shellcheck; PowerShell 7 runs the Windows installer's tests.

## Build

Every Go command takes the build tags `goolm,stdjson`. The `whatsapp_native`
tag adds the WhatsApp channel that links your own account; it links GPL-3.0
code (see [NOTICE](NOTICE)), so releases leave it out.

Build the web UI before the Go programs: `compa` embeds `web/backend/dist`
when it links, and a stale or missing UI there isn't reported.

```sh
cd web/frontend
pnpm install --frozen-lockfile
pnpm run build:backend
cd ../..
go build -tags goolm,stdjson ./...
```

`make product` builds both programs into `build/`; `make help` lists the
other targets.

A build you run keeps its data in `~/.compa`, as an installed one does; set
`COMPA_HOME` to a folder of its own to keep a development build's data apart.

## Test

```sh
go test -tags goolm,stdjson -p 1 ./...
```

Run the Go tests one package at a time (`-p 1`), as CI does. CI also runs
them with `-race` on Linux (`CGO_ENABLED=1`), and on Windows and macOS.

The web UI, in `web/frontend`:

```sh
pnpm test
pnpm run build
pnpm exec playwright install chromium   # once
pnpm run test:e2e                       # uses the build above
```

The installers have their own tests: `bash scripts/install/test-install.sh` on
Linux and `pwsh scripts/install/test-install.ps1` on Windows.

## Check

`make check` runs what CI checks for Go, without changing any file:
`go mod tidy -diff`, formatting, `go vet`, golangci-lint with `.golangci.yml`,
the doc links and the tests. CI also runs `govulncheck`; in `web/frontend`,
`npx tsc -b`, `pnpm run lint` and `pnpm run format`, which checks the
formatting with Prettier; and `node brand/check.mjs` for the brand assets.
`make fmt` formats the Go code, and `pnpm run format:write` in `web/frontend`
the web UI's.

## Pull requests

- Keep a change to one concern, with a test for each behavior it changes.
- Write commit messages like the existing ones: `area: what changed`.
- Add a line under **Unreleased** in [CHANGELOG.md](CHANGELOG.md) for a change
  users notice.
- Update the docs in `docs/` when you change what they describe.
