# Install Compa

Compa runs on Windows 10 or later, macOS 12 or later and Linux, on 64-bit x86
or ARM processors. The installer downloads a release from
[GitHub](https://github.com/xibodev/compa/releases), checks it against the
release's SHA-256 checksums and, in a terminal on your desktop, starts Compa.
Run it as yourself: it needs no administrator rights, and it stops if you run
it as root (`sudo`) or from a PowerShell running as administrator.

## Windows

In PowerShell (Windows PowerShell 5.1 or PowerShell 7):

```powershell
irm https://github.com/xibodev/compa/releases/latest/download/install.ps1 | iex
```

This installs `compa.exe` and `compa-kernel.exe`, with the release's license
files, in `%LOCALAPPDATA%\Programs\Compa`, adds that folder to your user
`PATH`, adds **Compa** to the Start Menu and starts Compa, which opens your
browser and shows an icon in the notification area.

`irm | iex` works whatever PowerShell's execution policy is, but by default
PowerShell won't run a downloaded `install.ps1`. Run such a copy with
`powershell -ExecutionPolicy Bypass -File .\install.ps1`.

## macOS and Linux

```sh
curl -fsSL https://github.com/xibodev/compa/releases/latest/download/install.sh | sh
```

This installs `compa` and `compa-kernel` in `~/.local/bin`, and the release's
license files in `~/.local/share/doc/compa`. When it runs in a terminal on your
desktop, it then starts Compa in the background, with its output in
`~/.compa/logs/launcher.out`; without a terminal, over SSH, under CI or on
Linux without a display, it prints how to start Compa instead. The installer
doesn't edit your shell startup files: if `~/.local/bin` isn't on your `PATH`,
it prints the line to add. It needs `curl` or `wget`, and `sha256sum` or
`shasum`.

## First run

Compa opens http://localhost:18800 (or the port you set) with a setup link and
asks you to set a password of at least 8 characters. In the browser, only that
link, which Compa opens or prints in its console, can set the first password;
if you closed the page, choose **Open Console** in the tray menu. Then connect
a model on the **Models** page; see [Use Compa](use.md).

Compa keeps its settings and data in `~/.compa` (on Windows,
`%USERPROFILE%\.compa`). The installer doesn't change them; on macOS and Linux
it only adds `logs/launcher.out` there, the output of the Compa it starts.

On a macOS or Linux computer without a desktop, set the password first and run
Compa in the terminal; Ctrl+C stops it:

```sh
compa -password -          # asks for the password
compa -console -no-browser
```

`-password -` asks for the password without showing it, or reads a line piped
to it, so the password stays out of your shell history. Then open the web UI
from another computer over SSH; see [LAN access](#lan-access).

## Start and stop

- Windows: start **Compa** from the Start Menu.
- macOS and Linux: run `compa` (or `~/.local/bin/compa`).

Compa's tray icon (in the menu bar on macOS) has **Open Console**, **Restart
Service** and **Quit**. Quitting also stops the gateway, the part that runs
chats, tools, channels and scheduled jobs, when Compa started it; a gateway you
started yourself with `compa-kernel gateway` keeps running.

## Update

```sh
compa-kernel update
```

This downloads the latest release over https, checks it against the release's
SHA-256 checksums and replaces both `compa` and `compa-kernel` in the folder
that holds `compa-kernel`. If the new `compa-kernel` doesn't run, it puts the
old programs back. Restart Compa to use the new version.
`compa-kernel update --version v1.2.3` installs a specific release; one older
than the version you run also needs `--allow-downgrade`.

Running the installer again also updates Compa: it stops the Compa running from
the install folder and replaces both programs, or keeps the old ones if
anything fails. On your desktop it starts Compa again.

Releases aren't signed. To check which GitHub workflow built a file, see
[Verify a release](../SECURITY.md#verify-a-release).

## Uninstall

Windows:

```powershell
& ([scriptblock]::Create((irm https://github.com/xibodev/compa/releases/latest/download/install.ps1))) -Uninstall
```

macOS and Linux:

```sh
curl -fsSL https://github.com/xibodev/compa/releases/latest/download/install.sh | COMPA_UNINSTALL=1 sh
```

This stops Compa and removes the programs and their license files, the
launch-at-login entry if Compa set one, and on Windows the `PATH` entry and the
Start Menu shortcut. Your data stays in `~/.compa`; delete that folder to
remove it too. If you installed with `COMPA_INSTALL_DIR`, set it again when you
uninstall.

The license files are `LICENSE`, `NOTICE` and `THIRD_PARTY_NOTICES`, which
every release archive holds. `THIRD_PARTY_NOTICES` lists the third-party code
in the programs, such as Go modules, npm packages and the Inter font, with its
license texts and where to get its source.

## Installer options

Set these environment variables before the installer runs.

| Variable | Effect |
|---|---|
| `COMPA_VERSION=v1.2.3` | Install that release instead of the latest. |
| `COMPA_INSTALL_DIR=<folder>` | Install somewhere else. |
| `COMPA_NO_START=1` | Don't start Compa afterwards. |
| `COMPA_UNINSTALL=1` | Remove Compa and keep your data. |
| `COMPA_ALLOW_ROOT=1` | Install even as root or from a PowerShell running as administrator; Compa then runs with those rights. |
| `COMPA_NO_PATH=1` | Windows only: leave your `PATH` alone. |
| `COMPA_NO_SHORTCUT=1` | Windows only: leave the Start Menu alone. |

With `curl`, put the variable before `sh`:

```sh
curl -fsSL https://github.com/xibodev/compa/releases/latest/download/install.sh | COMPA_NO_START=1 sh
```

In PowerShell, pass the options as parameters, which leaves no variables set in
your session: `-Version`, `-InstallDir`, `-NoPath`, `-NoShortcut`, `-NoStart`
and `-Uninstall`.

```powershell
& ([scriptblock]::Create((irm https://github.com/xibodev/compa/releases/latest/download/install.ps1))) -NoStart
```

A downloaded `install.ps1` takes the same parameters.

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
installed. [CONTRIBUTING.md](../CONTRIBUTING.md) covers tests and checks.

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

- Set the password first (in the browser before you turn LAN access on, or
  with `compa -password -` on macOS and Linux). With LAN access on, Compa
  doesn't start until a password is set, unless **Allow LAN Without Password**
  (`allow_lan_without_password` in `~/.compa/launcher-config.json`) is on.
- Compa serves plain HTTP, so your password and chats cross the network
  unencrypted. Use LAN access only on a network you trust.
- Only the web UI opens to the network. The gateway, port 18790, stays on this
  computer unless you set `gateway.host`; the web UI passes on what the
  browser needs from it.
- **Allowed Network CIDRs**, in the same section, limits which addresses can
  reach the web UI.
- The web UI answers only requests addressed to this computer's own names and
  addresses. To reach it through another name, such as a reverse proxy's, add
  that name to **Allowed Hosts** (`allowed_hosts`).

To use Compa from another computer without LAN access, forward the port over
SSH from that computer, then open http://127.0.0.1:18800 there:

```sh
ssh -L 18800:127.0.0.1:18800 you@the-computer-running-compa
```
