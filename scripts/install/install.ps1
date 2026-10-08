# Compa installer for Windows (Windows PowerShell 5.1 or PowerShell 7).
#
#   irm https://github.com/xibodev/compa/releases/latest/download/install.ps1 | iex
#
# Installs compa, the launcher, and compa-kernel, the harness it runs, with the
# release's license files, for the current user in %LOCALAPPDATA%\Programs\Compa.
# It needs no administrator rights; run it as yourself, not from an elevated
# PowerShell. It adds that folder to your user PATH and a "Compa" Start Menu
# shortcut, then starts Compa, which opens your browser to finish setting up;
# under CI or in a remote session it prints how to start Compa instead. Your
# settings and data live in %USERPROFILE%\.compa; this script never touches
# them.
#
# Options are environment variables, or parameters: give them to a downloaded
# copy (.\install.ps1 -NoStart) or to the downloaded script as a script block:
#   & ([scriptblock]::Create((irm https://github.com/xibodev/compa/releases/latest/download/install.ps1))) -NoStart
#
#   COMPA_VERSION=v1.2.3      -Version v1.2.3     install that release
#   COMPA_INSTALL_DIR=<dir>   -InstallDir <dir>   install somewhere else
#   COMPA_NO_PATH=1           -NoPath             leave the user PATH alone
#   COMPA_NO_SHORTCUT=1       -NoShortcut         leave the Start Menu alone
#   COMPA_NO_START=1          -NoStart            do not start Compa
#   COMPA_UNINSTALL=1         -Uninstall          remove Compa, keep your data
#   COMPA_ALLOW_ROOT=1                            install from an elevated session anyway
#
# COMPA_RELEASE_BASE_URL replaces https://github.com/xibodev/compa/releases/download,
# for example with a mirror. It must be an https:// address unless
# COMPA_INSTALL_TEST=1, which the installer tests set to serve a fake release.
#
# Everything runs in the script block below and the TLS setting is restored at
# the end, so "irm | iex" leaves no variables, functions or settings behind in
# your session; only its PATH gains the install folder.

& {
    [CmdletBinding(PositionalBinding = $false)]
    param(
        [string]$Version = $env:COMPA_VERSION,
        [string]$InstallDir = $env:COMPA_INSTALL_DIR,
        [switch]$NoPath,
        [switch]$NoShortcut,
        [switch]$NoStart,
        [switch]$Uninstall
    )

    $ErrorActionPreference = 'Stop'
    # Windows PowerShell's progress bar slows downloads to a crawl.
    $ProgressPreference = 'SilentlyContinue'

    # The release workflow replaces this placeholder with the release tag.
    $stampedTag = '__COMPA_VERSION__'
    $repo = 'xibodev/compa'
    $defaultPort = 18800
    $programs = @('compa-kernel.exe', 'compa.exe')
    $installedFiles = @('compa-kernel.exe', 'compa.exe', 'LICENSE', 'NOTICE', 'THIRD_PARTY_NOTICES', 'compa.ico')

    function Test-Flag([string]$Value) {
        return @('1', 'true', 'yes', 'on') -contains "$Value".Trim().ToLowerInvariant()
    }

    function Write-Step([string]$Message) {
        Write-Host "  $Message"
    }

    function Get-InstallDir([string]$Dir) {
        if (-not $Dir) {
            if (-not $env:LOCALAPPDATA) { throw 'LOCALAPPDATA is not set; set COMPA_INSTALL_DIR to choose where to install Compa.' }
            $Dir = Join-Path (Join-Path $env:LOCALAPPDATA 'Programs') 'Compa'
        }
        # Resolved against the current PowerShell location, like any path you type.
        $full = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath([Environment]::ExpandEnvironmentVariables($Dir))
        return $full.TrimEnd('\')
    }

    function Get-DataDir {
        if ($env:COMPA_HOME) { return $env:COMPA_HOME }
        return Join-Path $HOME '.compa'
    }

    function Test-Elevated {
        $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
        $principal = New-Object Security.Principal.WindowsPrincipal $identity
        return $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
    }

    function Get-ReleaseBase {
        if (-not $env:COMPA_RELEASE_BASE_URL) { return 'https://github.com/' + $repo + '/releases/download' }
        $base = $env:COMPA_RELEASE_BASE_URL.TrimEnd('/')
        if ($base -notmatch '^https://' -and -not (Test-Flag $env:COMPA_INSTALL_TEST)) {
            throw "COMPA_RELEASE_BASE_URL must be an https:// address, not '$base'."
        }
        return $base
    }

    function Resolve-Tag([string]$Requested) {
        $tag = "$Requested".Trim()
        if (-not $tag -and $stampedTag -match '^v\d+\.\d+\.\d+') { $tag = $stampedTag }
        if (-not $tag) {
            if ($env:COMPA_RELEASE_BASE_URL) { throw 'Set COMPA_VERSION to the release to install from COMPA_RELEASE_BASE_URL.' }
            # A copy not stamped by a release, e.g. run from a source checkout.
            try {
                $latest = Invoke-RestMethod -Uri "https://api.github.com/repos/$repo/releases/latest" -UseBasicParsing -UserAgent 'compa-installer'
                $tag = "$($latest.tag_name)"
            } catch {
                throw "Could not look up the latest Compa release: $($_.Exception.Message)"
            }
        }
        if ($tag -notmatch '^v?\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$') { throw "'$tag' is not a Compa version such as v1.2.3." }
        return 'v' + $tag.TrimStart('v')
    }

    function Get-Arch {
        # Ask Windows for the real processor: an emulated PowerShell reports
        # the architecture it is emulating in PROCESSOR_ARCHITECTURE.
        try {
            $cpu = @(Get-CimInstance -ClassName Win32_Processor -ErrorAction Stop)[0]
            switch ([int]$cpu.Architecture) {
                9 { return 'amd64' }
                12 { return 'arm64' }
            }
        } catch { }
        $reported = $env:PROCESSOR_ARCHITECTURE
        if ($env:PROCESSOR_ARCHITEW6432) { $reported = $env:PROCESSOR_ARCHITEW6432 }
        switch ($reported) {
            'AMD64' { return 'amd64' }
            'ARM64' { return 'arm64' }
        }
        throw "Compa needs 64-bit Windows on an x64 or ARM64 processor; this one reports '$reported'."
    }

    function Save-Download([string]$Url, [string]$OutFile) {
        try {
            Invoke-WebRequest -Uri $Url -OutFile $OutFile -UseBasicParsing
        } catch {
            $status = ''
            $response = $_.Exception.PSObject.Properties['Response']
            if ($response -and $response.Value) { $status = " (HTTP $([int]$response.Value.StatusCode))" }
            throw "Could not download $Url$status. $($_.Exception.Message)"
        }
    }

    function Assert-Checksum([string]$File, [string]$SumsFile, [string]$Name) {
        $expected = $null
        foreach ($line in Get-Content -LiteralPath $SumsFile) {
            $fields = @("$line".Trim() -split '\s+')
            if ($fields.Count -eq 2 -and $fields[1].TrimStart('*') -eq $Name) {
                $expected = $fields[0].ToLowerInvariant()
                break
            }
        }
        if (-not $expected) { throw "SHA256SUMS does not list $Name, so it cannot be verified." }
        if ($expected -notmatch '^[0-9a-f]{64}$') { throw "SHA256SUMS has a malformed entry for $Name." }
        $actual = (Get-FileHash -LiteralPath $File -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($actual -ne $expected) {
            throw "Checksum mismatch for ${Name}: SHA256SUMS lists $expected but the download is $actual. Nothing was installed."
        }
    }

    function Stop-Compa([string]$Dir) {
        # Only programs running from this folder; another copy of Compa
        # elsewhere, a folder inside this one included, is left alone.
        $running = @(Get-Process -Name 'compa', 'compa-kernel' -ErrorAction SilentlyContinue | Where-Object {
                $path = $null
                try { $path = $_.Path } catch { }
                $path -and ([IO.Path]::GetDirectoryName($path).TrimEnd('\') -ieq $Dir)
            })
        if ($running.Count -eq 0) { return }
        Write-Step 'Stopping the running Compa...'
        foreach ($p in $running) {
            try { Stop-Process -Id $p.Id -Force -ErrorAction Stop } catch { }
        }
        foreach ($p in $running) {
            try { [void]$p.WaitForExit(15000) } catch { }
        }
    }

    function Remove-Leftover([string]$Dir, [string]$Pattern) {
        Get-ChildItem -LiteralPath $Dir -Force -File -ErrorAction SilentlyContinue |
            Where-Object { $_.Name -match $Pattern } |
            ForEach-Object { Remove-Item -LiteralPath $_.FullName -Force -ErrorAction SilentlyContinue }
    }

    # Files the in-app updater leaves while an old program still runs, and
    # those of an installer run that was cut short.
    $updaterLeftovers = '^\.?compa(-kernel)?\.exe(\.[0-9]+)?\.(old|new)$'
    $stagedFiles = '^\..+\.install-new$'
    $replacedFiles = '^\..+\.install-old$'

    function Copy-File([string]$From, [string]$To) {
        for ($attempt = 1; ; $attempt++) {
            try {
                Copy-Item -LiteralPath $From -Destination $To -Force
                return
            } catch {
                # A scanner may briefly lock a freshly written program.
                if ($attempt -ge 5) { throw "Could not write ${To}: $($_.Exception.Message)" }
                Start-Sleep -Seconds 1
            }
        }
    }

    function Install-File([string]$Stage, [string]$Dir) {
        $names = @($installedFiles | Where-Object { Test-Path -LiteralPath (Join-Path $Stage $_) -PathType Leaf })
        New-Item -ItemType Directory -Force -Path $Dir | Out-Null
        Remove-Leftover $Dir $updaterLeftovers
        Remove-Leftover $Dir $stagedFiles
        Remove-Leftover $Dir $replacedFiles

        # Every file is copied in beside its final name first, so a folder
        # that can't be written fails before the running Compa is stopped.
        try {
            foreach ($name in $names) { Copy-File (Join-Path $Stage $name) (Join-Path $Dir ".$name.install-new") }
        } catch {
            Remove-Leftover $Dir $stagedFiles
            throw "$($_.Exception.Message) Nothing was changed."
        }

        Stop-Compa $Dir
        # Renaming works even on a program that is still running. If one
        # fails, the files already replaced are put back.
        $replaced = New-Object System.Collections.Generic.List[string]
        try {
            foreach ($name in $names) {
                $target = Join-Path $Dir $name
                if (Test-Path -LiteralPath $target) { [IO.File]::Move($target, (Join-Path $Dir ".$name.install-old")) }
                $replaced.Add($name)
                [IO.File]::Move((Join-Path $Dir ".$name.install-new"), $target)
            }
        } catch {
            $failure = $_.Exception.Message
            foreach ($name in $replaced) {
                $target = Join-Path $Dir $name
                $old = Join-Path $Dir ".$name.install-old"
                if (Test-Path -LiteralPath $old) {
                    Remove-Item -LiteralPath $target -Force -ErrorAction SilentlyContinue
                    try { [IO.File]::Move($old, $target) } catch { }
                } elseif (-not (Test-Path -LiteralPath (Join-Path $Dir ".$name.install-new"))) {
                    # New in this install.
                    Remove-Item -LiteralPath $target -Force -ErrorAction SilentlyContinue
                }
            }
            Remove-Leftover $Dir $stagedFiles
            throw "Could not replace the programs in ${Dir}: $failure The previous version was kept; close Compa and run the installer again."
        }
        # One still in use is removed by the next install.
        Remove-Leftover $Dir $replacedFiles
        # A file another release left that this one lacks goes, such as the
        # THIRD_PARTY_NOTICES of a newer release when installing an older one.
        foreach ($name in $installedFiles) {
            if ($names -notcontains $name) { Remove-Item -LiteralPath (Join-Path $Dir $name) -Force -ErrorAction SilentlyContinue }
        }
    }

    function Test-SameDir([string]$Entry, [string]$Dir) {
        $e = [Environment]::ExpandEnvironmentVariables("$Entry".Trim()).TrimEnd('\')
        return [bool]$e -and ($e -ieq "$Dir".TrimEnd('\'))
    }

    function Add-PathEntry([string]$PathValue, [string]$Dir) {
        foreach ($entry in "$PathValue" -split ';') {
            if (Test-SameDir $entry $Dir) { return $PathValue }
        }
        if (-not "$PathValue".Trim(';')) { return $Dir }
        return "$PathValue".TrimEnd(';') + ';' + $Dir
    }

    function Remove-PathEntry([string]$PathValue, [string]$Dir) {
        # Only this folder goes; every other entry stays exactly as it was.
        $kept = @("$PathValue" -split ';' | Where-Object { -not (Test-SameDir $_ $Dir) })
        return ($kept -join ';')
    }

    function Get-UserPath {
        $key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment')
        if ($null -eq $key) { return '' }
        try {
            # Unexpanded, so entries such as %USERPROFILE%\bin survive the rewrite.
            return [string]$key.GetValue('Path', '', [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
        } finally { $key.Close() }
    }

    function Set-UserPath([string]$Value) {
        $key = [Microsoft.Win32.Registry]::CurrentUser.CreateSubKey('Environment')
        try {
            if ($Value) { $key.SetValue('Path', $Value, [Microsoft.Win32.RegistryValueKind]::ExpandString) }
            else { $key.DeleteValue('Path', $false) }
        } finally { $key.Close() }
        # Setting a user variable through .NET broadcasts the change, so new
        # terminals and Explorer see the new PATH without signing out.
        $marker = 'COMPA_INSTALLER_' + [guid]::NewGuid().ToString('N')
        [Environment]::SetEnvironmentVariable($marker, '1', 'User')
        [Environment]::SetEnvironmentVariable($marker, $null, 'User')
    }

    function Get-ShortcutPath {
        return Join-Path ([Environment]::GetFolderPath('Programs')) 'Compa.lnk'
    }

    function New-Shortcut([string]$LinkPath, [string]$Dir) {
        # compa.exe is a GUI program that lives in the tray, so the shortcut
        # starts it directly; no console window opens.
        $shell = New-Object -ComObject WScript.Shell
        $link = $shell.CreateShortcut($LinkPath)
        $link.TargetPath = Join-Path $Dir 'compa.exe'
        $link.WorkingDirectory = $Dir
        $link.Description = 'Compa'
        $icon = Join-Path $Dir 'compa.ico'
        if (Test-Path -LiteralPath $icon) { $link.IconLocation = "$icon,0" }
        $link.Save()
    }

    function Remove-LoginItem([string]$Dir) {
        # The launch-at-login setting in Compa writes this value; drop it only
        # when it starts this copy.
        $runKey = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run'
        $item = Get-ItemProperty -LiteralPath $runKey -Name 'Compa' -ErrorAction SilentlyContinue
        if (-not $item) { return }
        $command = "$($item.Compa)".Replace('\\', '\')
        if ($command.IndexOf("$Dir\compa.exe", [StringComparison]::OrdinalIgnoreCase) -ge 0) {
            Remove-ItemProperty -LiteralPath $runKey -Name 'Compa' -ErrorAction SilentlyContinue
            Write-Step 'Removed Compa from startup at sign-in.'
        }
    }

    function Test-CanStart {
        # Someone at this desktop finishes setting up in the browser Compa
        # opens; nobody does under CI, in a remote session or a script whose
        # output is redirected.
        if ((Test-Flag $env:CI) -or $env:SSH_CONNECTION -or -not [Environment]::UserInteractive) { return $false }
        try { return -not [Console]::IsOutputRedirected } catch { return $true }
    }

    function Get-LauncherPort {
        # The Service Port setting, kept in launcher-config.json beside config.json.
        try {
            $config = $env:COMPA_CONFIG
            if (-not $config) { $config = Join-Path (Get-DataDir) 'config.json' }
            $settings = Join-Path (Split-Path -Parent $config) 'launcher-config.json'
            $port = [int](Get-Content -LiteralPath $settings -Raw | ConvertFrom-Json).port
            if ($port -ge 1 -and $port -le 65535) { return $port }
        } catch { }
        return $defaultPort
    }

    # Get-NewLine FILE SIZE: the last lines FILE gained after it was SIZE bytes.
    function Get-NewLine([string]$File, [long]$Size) {
        if (-not (Test-Path -LiteralPath $File -PathType Leaf)) { return @() }
        $stream = [IO.File]::Open($File, 'Open', 'Read', 'ReadWrite')
        try {
            if ($stream.Length -le $Size) { return @() }
            [void]$stream.Seek($Size, 'Begin')
            $text = (New-Object IO.StreamReader $stream).ReadToEnd()
        } finally { $stream.Dispose() }
        return @($text -split "`r?`n" | Where-Object { $_ } | Select-Object -Last 8)
    }

    function Start-Compa([string]$Dir) {
        $exe = Join-Path $Dir 'compa.exe'
        $log = Join-Path (Join-Path (Get-DataDir) 'logs') 'launcher.log'
        # launcher.log keeps earlier runs, so only what this start adds is shown.
        $logSize = 0
        if (Test-Path -LiteralPath $log -PathType Leaf) { $logSize = (Get-Item -LiteralPath $log).Length }
        $process = Start-Process -FilePath $exe -WorkingDirectory $Dir -WindowStyle Hidden -PassThru
        Start-Sleep -Seconds 2
        if (-not $process.HasExited) {
            Write-Host 'Compa is running and opens your browser to finish setting up.'
            Write-Host "If it doesn't, open http://localhost:$(Get-LauncherPort)"
            return
        }
        Write-Host "Compa stopped right after starting. The end of $log says:" -ForegroundColor Yellow
        foreach ($line in Get-NewLine $log $logSize) { Write-Host "    $line" }
        Write-Host "Start it again from the Start Menu, or with: & '$exe'"
    }

    function Install-Compa([string]$Dir, [string]$RequestedVersion) {
        if ((Test-Elevated) -and -not (Test-Flag $env:COMPA_ALLOW_ROOT)) {
            throw 'Run the installer as yourself, not from a PowerShell running as administrator: Compa would run elevated. To do that anyway, set COMPA_ALLOW_ROOT=1.'
        }
        $base = Get-ReleaseBase
        $tag = Resolve-Tag $RequestedVersion
        $arch = Get-Arch
        $archive = "compa_$($tag.Substring(1))_windows_$arch.zip"

        Write-Host "Installing Compa $tag (windows/$arch) into $Dir"
        $tmp = Join-Path ([IO.Path]::GetTempPath()) ('compa-install-' + [guid]::NewGuid().ToString('N'))
        New-Item -ItemType Directory -Path $tmp | Out-Null
        try {
            $zip = Join-Path $tmp $archive
            $sums = Join-Path $tmp 'SHA256SUMS'
            Write-Step "Downloading $archive..."
            Save-Download "$base/$tag/SHA256SUMS" $sums
            Save-Download "$base/$tag/$archive" $zip
            Assert-Checksum $zip $sums $archive
            Write-Step 'Verified its SHA-256 checksum.'

            $stage = Join-Path $tmp 'files'
            Expand-Archive -LiteralPath $zip -DestinationPath $stage -Force
            foreach ($name in $programs) {
                if (-not (Test-Path -LiteralPath (Join-Path $stage $name) -PathType Leaf)) { throw "$archive does not contain $name." }
            }
            Install-File $stage $Dir
        } finally {
            Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
        }
        Write-Step "Installed compa.exe and compa-kernel.exe."

        if ($skipPath) {
            Write-Step 'Left PATH alone (COMPA_NO_PATH).'
        } else {
            $userPath = Get-UserPath
            $newPath = Add-PathEntry $userPath $Dir
            if ($newPath -ne $userPath) {
                Set-UserPath $newPath
                Write-Step "Added $Dir to your PATH; new terminals can run 'compa'."
            }
            $env:Path = Add-PathEntry $env:Path $Dir
        }

        if ($skipShortcut) {
            Write-Step 'Left the Start Menu alone (COMPA_NO_SHORTCUT).'
        } else {
            New-Shortcut (Get-ShortcutPath) $Dir
            Write-Step 'Added Compa to the Start Menu.'
        }

        Write-Host ''
        Write-Host "Compa $tag is installed." -ForegroundColor Green
        $exe = Join-Path $Dir 'compa.exe'
        if ($skipStart) {
            Write-Host "Start it with: & '$exe'"
        } elseif (Test-CanStart) {
            Start-Compa $Dir
        } else {
            Write-Host 'Compa was not started: this looks like CI, a script or a remote session.'
            Write-Host "  On this computer's desktop, start Compa from the Start Menu or run: & '$exe'"
            Write-Host '  Without a desktop, set the password, then run Compa in the terminal:'
            Write-Host "    & '$exe' -password 'your-password'"
            Write-Host "    & '$exe' -console -no-browser"
        }
        Write-Host "Your settings and data live in $(Get-DataDir)."
    }

    function Uninstall-Compa([string]$Dir) {
        Write-Host "Removing Compa from $Dir"
        if (Test-Path -LiteralPath $Dir -PathType Container) {
            Stop-Compa $Dir
            Remove-Leftover $Dir $updaterLeftovers
            Remove-Leftover $Dir $stagedFiles
            Remove-Leftover $Dir $replacedFiles
            foreach ($name in $installedFiles) {
                $file = Join-Path $Dir $name
                if (Test-Path -LiteralPath $file) {
                    try { Remove-Item -LiteralPath $file -Force }
                    catch { throw "Could not remove ${file}: $($_.Exception.Message) Close Compa and try again." }
                }
            }
            if (-not (Get-ChildItem -LiteralPath $Dir -Force -ErrorAction SilentlyContinue)) {
                Remove-Item -LiteralPath $Dir -Force -ErrorAction SilentlyContinue
            }
            Write-Step 'Removed the programs.'
        } else {
            Write-Step 'The programs were not there.'
        }

        if (-not $skipPath) {
            $userPath = Get-UserPath
            $newPath = Remove-PathEntry $userPath $Dir
            if ($newPath -ne $userPath) {
                Set-UserPath $newPath
                Write-Step 'Removed it from your PATH.'
            }
            $env:Path = Remove-PathEntry $env:Path $Dir
        }
        if (-not $skipShortcut) {
            $link = Get-ShortcutPath
            if (Test-Path -LiteralPath $link) {
                Remove-Item -LiteralPath $link -Force
                Write-Step 'Removed the Start Menu shortcut.'
            }
        }
        Remove-LoginItem $Dir

        Write-Host ''
        Write-Host 'Compa is uninstalled.' -ForegroundColor Green
        Write-Host "Your settings and data are still in $(Get-DataDir); delete that folder to remove them too."
    }

    $skipPath = [bool]$NoPath -or (Test-Flag $env:COMPA_NO_PATH)
    $skipShortcut = [bool]$NoShortcut -or (Test-Flag $env:COMPA_NO_SHORTCUT)
    $skipStart = [bool]$NoStart -or (Test-Flag $env:COMPA_NO_START)
    $removing = [bool]$Uninstall -or (Test-Flag $env:COMPA_UNINSTALL)

    $savedProtocol = [Net.ServicePointManager]::SecurityProtocol
    try {
        if ($PSVersionTable.PSEdition -eq 'Core' -and -not $IsWindows) { throw 'This installer is for Windows; on macOS and Linux use install.sh.' }
        # Windows PowerShell on an older .NET offers only SSL 3 and TLS 1.0,
        # and GitHub needs TLS 1.2. SystemDefault (0) already lets Windows
        # choose, TLS 1.3 included, so it is left alone.
        if ([int]$savedProtocol -ne 0 -and -not ($savedProtocol -band [Net.SecurityProtocolType]::Tls12)) {
            [Net.ServicePointManager]::SecurityProtocol = $savedProtocol -bor [Net.SecurityProtocolType]::Tls12
        }
        $dir = Get-InstallDir $InstallDir
        if ($removing) { Uninstall-Compa $dir } else { Install-Compa $dir $Version }
    } catch {
        $message = "Compa installer failed: $($_.Exception.Message)"
        Write-Host ''
        if ($PSCommandPath) {
            # A script file fails its caller with exit code 1.
            Write-Host $message -ForegroundColor Red
            exit 1
        }
        # Piped into iex, a terminating error stops a calling script too, but
        # leaves the window open.
        $PSCmdlet.ThrowTerminatingError((New-Object Management.Automation.ErrorRecord (
                    (New-Object Exception $message), 'CompaInstallFailed',
                    [Management.Automation.ErrorCategory]::NotSpecified, $null)))
    } finally {
        [Net.ServicePointManager]::SecurityProtocol = $savedProtocol
    }
} @args
