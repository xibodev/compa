# Install Compa

Compa runs on Windows, macOS and Linux, on 64-bit x86 or ARM processors. The
installer downloads a release from
[GitHub](https://github.com/xibodev/compa/releases), checks it against the
release's SHA-256 checksums and starts Compa. It needs no administrator rights.

## Windows

In PowerShell (Windows PowerShell 5.1 or PowerShell 7):

```powershell
irm https://github.com/xibodev/compa/releases/latest/download/install.ps1 | iex
```

This installs `compa.exe` and `compa-kernel.exe` in
`%LOCALAPPDATA%\Programs\Compa`, adds that folder to your user `PATH`, adds
**Compa** to the Start Menu and starts Compa. Compa opens your browser and
shows an icon in the notification area.

## macOS and Linux

```sh
curl -fsSL https://github.com/xibodev/compa/releases/latest/download/install.sh | sh
```

This installs `compa` and `compa-kernel` in `~/.local/bin` and starts Compa in
the background; its output goes to `~/.compa/logs/launcher.out`. The installer
doesn't edit your shell startup files: if `~/.local/bin` isn't on your `PATH`,
it prints the line to add. It needs `curl` or `wget`, and `sha256sum` or
`shasum`.

## First run

Compa opens http://127.0.0.1:18800 and asks you to set a password of at least 8
characters. Then connect a model on the **Models** page; see
[Use Compa](use.md).

Compa keeps its settings and data in `~/.compa` (on Windows,
`%USERPROFILE%\.compa`). The installer never changes that folder.

## Start and stop

- Windows: start **Compa** from the Start Menu.
- macOS and Linux: run `compa` (or `~/.local/bin/compa`).

Compa's tray icon (in the menu bar on macOS) has **Open Console**, **Restart
Service** and **Quit**. Quitting also stops the gateway, the part that runs
chats, tools, channels and scheduled jobs. On a computer without a desktop, run
`compa -console -no-browser` instead and stop it with Ctrl+C.

## Update

```sh
compa-kernel update
```

This downloads the latest release, checks it and replaces both `compa` and
`compa-kernel` in the folder that holds `compa-kernel`. Restart Compa to use
the new version. `compa-kernel update --version v1.2.3` installs a specific
release.

Running the installer again also updates Compa: it stops the copy running from
its install folder, replaces the programs and starts Compa again.

## Uninstall

Windows:

```powershell
$env:COMPA_UNINSTALL = 1; irm https://github.com/xibodev/compa/releases/latest/download/install.ps1 | iex
```

The variable stays set in that PowerShell window, so close it afterwards.

macOS and Linux:

```sh
curl -fsSL https://github.com/xibodev/compa/releases/latest/download/install.sh | COMPA_UNINSTALL=1 sh
```

This stops Compa and removes the programs, the launch-at-login entry if Compa
set one, and on Windows the `PATH` entry and the Start Menu shortcut. Your data
stays in `~/.compa`; delete that folder to remove it too. If you installed with
`COMPA_INSTALL_DIR`, set it again when you uninstall.

## Installer options

Set these environment variables before the installer runs.

| Variable | Effect |
|---|---|
| `COMPA_VERSION=v1.2.3` | Install that release instead of the latest. |
| `COMPA_INSTALL_DIR=<folder>` | Install somewhere else. |
| `COMPA_NO_START=1` | Don't start Compa afterwards. |
| `COMPA_UNINSTALL=1` | Remove Compa and keep your data. |
| `COMPA_NO_PATH=1` | Windows only: leave your `PATH` alone. |
| `COMPA_NO_SHORTCUT=1` | Windows only: leave the Start Menu alone. |

In PowerShell, set the variable first:

```powershell
$env:COMPA_NO_START = 1; irm https://github.com/xibodev/compa/releases/latest/download/install.ps1 | iex
```

With `curl`, put it before `sh`:

```sh
curl -fsSL https://github.com/xibodev/compa/releases/latest/download/install.sh | COMPA_NO_START=1 sh
```

A downloaded `install.ps1` also takes the options as parameters: `-Version`,
`-InstallDir`, `-NoPath`, `-NoShortcut`, `-NoStart` and `-Uninstall`.

## Run from source

You need Go 1.26.6, Node.js 22 and pnpm. From the repository root:

```sh
cd web/frontend
pnpm install --frozen-lockfile
pnpm run build:backend
cd ../..
go build -tags goolm,stdjson -o build/compa-kernel ./cmd/compa-kernel
go build -tags goolm,stdjson -o build/compa ./web/backend
./build/compa
```

On Windows, name the outputs `compa-kernel.exe` and `compa.exe`. Keep both
programs in one folder: `compa` runs the `compa-kernel` beside it. A build
from source uses the same `~/.compa` as an installed Compa; set `COMPA_HOME` to
another folder to keep them apart. On macOS, the menu-bar icon needs `compa`
built with cgo, which is the default when Xcode's command-line tools are
installed.

## Ports

| Port | Used by | Change it with |
|---|---|---|
| 18800 | The web UI (`compa`) | `compa -port <port>`, or **Config** → **Compa app** → **Service Port** |
| 18790 | The gateway (`compa-kernel gateway`) | `gateway.port` in `~/.compa/config.json` |

Both listen only on this computer by default.

## LAN access

To open Compa from another device on your network, start it with
`compa -public`, or turn on **Config** → **Compa app** → **Enable LAN Access**
and restart Compa. Then open `http://<this computer's IP address>:18800` on the
other device.

Before you do:

- Set the password first. Until one is set, whoever opens the page first sets
  it.
- Compa serves plain HTTP, so your password and chats cross the network
  unencrypted. Use LAN access only on a network you trust.
- The gateway port, 18790, listens on the network too.
- **Allowed Network CIDRs**, in the same section, limits which addresses can
  reach the web UI.
