# Install Compa

Compa runs on Windows 10 or later, macOS 12 or later and Linux, on 64-bit x86
or ARM. The installer downloads a release from
[GitHub](https://github.com/xibodev/compa/releases), checks it against the
release's SHA-256 checksums and starts Compa. Run it as yourself: it needs no
administrator rights and refuses to run as root or as administrator.

## Windows

In PowerShell:

```powershell
irm https://github.com/xibodev/compa/releases/latest/download/install.ps1 | iex
```

This installs `compa.exe` and `compa-kernel.exe`, with the release's license
files, in `%LOCALAPPDATA%\Programs\Compa`, adds that folder to your `PATH`,
adds **Compa** to the Start Menu and starts Compa.

## macOS and Linux

```sh
curl -fsSL https://github.com/xibodev/compa/releases/latest/download/install.sh | sh
```

This installs `compa` and `compa-kernel` in `~/.local/bin`, and the release's
license files in `~/.local/share/doc/compa`. On a desktop it then starts
Compa. Without a desktop (over SSH, or Linux without a display) it prints how
to start Compa instead. If `~/.local/bin` isn't on your `PATH`, it prints the
line to add. It needs `curl` or `wget`, and `sha256sum` or
`shasum`.

## First run

Compa opens http://localhost:18800 and asks you to set a password. Then
connect a model on **Models**; see [Use Compa](use.md).

Compa keeps its settings and data in `~/.compa` (on Windows,
`%USERPROFILE%\.compa`).

On a computer without a desktop, set the password, then run Compa in the
terminal:

```sh
compa -password -          # asks for the password
compa -console -no-browser
```

Open the web UI from another computer over SSH; see [LAN access](#lan-access).

## Start and stop

Start **Compa** from the Start Menu on Windows, or run `compa` on macOS and
Linux. The tray icon (the menu bar on macOS) has **Open Console**, **Restart
Service** and **Quit**.

## Update

```sh
compa-kernel update
```

This downloads the latest release, checks its checksums, replaces `compa` and
`compa-kernel`, and puts the old ones back if the new `compa-kernel` doesn't
run. Restart Compa afterwards. `--version v1.2.3` installs a specific release;
an older one also needs `--allow-downgrade`. Running the installer again also
updates.

Releases aren't signed; see [Verify a release](../SECURITY.md#verify-a-release).

## Uninstall

Windows:

```powershell
& ([scriptblock]::Create((irm https://github.com/xibodev/compa/releases/latest/download/install.ps1))) -Uninstall
```

macOS and Linux:

```sh
curl -fsSL https://github.com/xibodev/compa/releases/latest/download/install.sh | COMPA_UNINSTALL=1 sh
```

This removes the programs and their license files, the launch-at-login entry
and, on Windows, the `PATH` entry and Start Menu shortcut. Your data stays in
`~/.compa`; delete it to remove it too.

The license files are `LICENSE`, `NOTICE` and `THIRD_PARTY_NOTICES`, which
lists the third-party code in the programs with its license texts and where to
get its source.

## Installer options

| Variable | PowerShell | Effect |
|---|---|---|
| `COMPA_VERSION=v1.2.3` | `-Version` | Install that release instead of the latest. |
| `COMPA_INSTALL_DIR=<folder>` | `-InstallDir` | Install somewhere else; set it again to uninstall. |
| `COMPA_NO_START=1` | `-NoStart` | Don't start Compa afterwards. |
| `COMPA_UNINSTALL=1` | `-Uninstall` | Remove Compa and keep your data. |
| `COMPA_ALLOW_ROOT=1` | | Install as root or administrator anyway. |
| `COMPA_NO_PATH=1` | `-NoPath` | Windows: leave `PATH` alone. |
| `COMPA_NO_SHORTCUT=1` | `-NoShortcut` | Windows: leave the Start Menu alone. |

```sh
curl -fsSL https://github.com/xibodev/compa/releases/latest/download/install.sh | COMPA_NO_START=1 sh
```

```powershell
& ([scriptblock]::Create((irm https://github.com/xibodev/compa/releases/latest/download/install.ps1))) -NoStart
```

## Ports

| Port | Used by | Change it with |
|---|---|---|
| 18800 | The web UI (`compa`) | `compa -port <port>`, or **Config** → **Compa app** → **Service Port** |
| 18790 | The gateway (`compa-kernel gateway`) | `gateway.port` in `config.json` |

Both listen only on this computer by default.

## LAN access

To open Compa from another device, start it with `compa -public`, or turn on
**Config** → **Compa app** → **Enable LAN Access** and restart. Then open
`http://<this computer's address>:18800` there.

- Set the password first. With LAN access on, Compa doesn't start without one.
- Compa serves plain HTTP: use LAN access only on a network you trust.
- Only the web UI opens to the network; the gateway stays on this computer.
- **Allowed Network CIDRs** limits which addresses can connect; **Allowed
  Hosts** adds names, such as a reverse proxy's.

Without LAN access, forward the port over SSH and open
http://127.0.0.1:18800:

```sh
ssh -L 18800:127.0.0.1:18800 you@the-computer-running-compa
```

## Run from source

You need Go 1.26.9, Node.js 22.13 or later, and pnpm. From the repository
root:

```sh
cd web/frontend
pnpm install --frozen-lockfile
pnpm run build:backend
cd ../..
go build -tags goolm,stdjson -o build/compa-kernel ./cmd/compa-kernel
go build -tags goolm,stdjson -o build/compa ./web/backend
./build/compa
```

On Windows, name them `compa-kernel.exe` and `compa.exe`. Keep both in one
folder. A source build uses the same `~/.compa` as an installed Compa; set
`COMPA_HOME` to keep them apart. [CONTRIBUTING.md](../CONTRIBUTING.md) covers
tests and checks.
