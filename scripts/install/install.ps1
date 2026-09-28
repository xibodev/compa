# Compa installer for Windows (Windows PowerShell 5.1 or PowerShell 7).
#
#   irm https://github.com/xibodev/compa/releases/latest/download/install.ps1 | iex
#
# Installs compa, the launcher, and compa-kernel, the harness it runs, for the
# current user in %LOCALAPPDATA%\Programs\Compa. No administrator rights are
# needed. It adds that folder to your user PATH, adds a "Compa" Start Menu
# shortcut, and starts Compa, which opens your browser to finish setting up.
# Your settings and data live in %USERPROFILE%\.compa; this script never
# touches them.
#
# Options: environment variables for "irm | iex", or the same options as
# parameters when you run a downloaded copy, e.g. .\install.ps1 -NoStart
#
#   COMPA_VERSION=v1.2.3      -Version v1.2.3     install that release
#   COMPA_INSTALL_DIR=<dir>   -InstallDir <dir>   install somewhere else
#   COMPA_NO_PATH=1           -NoPath             leave the user PATH alone
#   COMPA_NO_SHORTCUT=1       -NoShortcut         leave the Start Menu alone
#   COMPA_NO_START=1          -NoStart            do not start Compa
#   COMPA_UNINSTALL=1         -Uninstall          remove Compa, keep your data
#
# For tests only: COMPA_RELEASE_BASE_URL replaces
# https://github.com/xibodev/compa/releases/download, so the installer can run
# against a locally served fake release.

param(
    [string]$Version = $env:COMPA_VERSION,
    [string]$InstallDir = $env:COMPA_INSTALL_DIR,
    [switch]$NoPath,
    [switch]$NoShortcut,
    [switch]$NoStart,
    [switch]$Uninstall
)

# Everything runs in this script block, so "irm | iex" leaves no functions or
# preference changes behind in your session.
& {
    $ErrorActionPreference = 'Stop'
    # Windows PowerShell's progress bar slows downloads to a crawl.
    $ProgressPreference = 'SilentlyContinue'

    # The release workflow replaces this placeholder with the release tag.
    $stampedTag = '__COMPA_VERSION__'
    $repo = 'xibodev/compa'
    $programs = @('compa-kernel.exe', 'compa.exe')
    $installedFiles = @('compa-kernel.exe', 'compa.exe', 'LICENSE', 'NOTICE', 'compa.ico')

    function Test-Flag([string]$Value) {
        return @('1', 'true', 'yes', 'on') -contains "$Value".Trim().ToLowerInvariant()
    }

    function Write-Step([string]$Message) {
        Write-Host "  $Message"
    }

    function Get-InstallDir {
        $dir = $InstallDir
        if (-not $dir) {
            if (-not $env:LOCALAPPDATA) { throw 'LOCALAPPDATA is not set; set COMPA_INSTALL_DIR to choose where to install Compa.' }
            $dir = Join-Path (Join-Path $env:LOCALAPPDATA 'Programs') 'Compa'
        }
        # Resolved against the current PowerShell location, like any path you type.
        $full = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath([Environment]::ExpandEnvironmentVariables($dir))
        return $full.TrimEnd('\')
    }

    function Get-DataDir {
        if ($env:COMPA_HOME) { return $env:COMPA_HOME }
        return Join-Path $HOME '.compa'
    }

    function Resolve-Tag {
        $tag = "$Version".Trim()
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
        # elsewhere is left alone.
        $prefix = $Dir + '\'
        $running = @(Get-Process -Name 'compa', 'compa-kernel' -ErrorAction SilentlyContinue | Where-Object {
                $path = $null
                try { $path = $_.Path } catch { }
                $path -and $path.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)
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

    function Remove-UpdateLeftovers([string]$Dir) {
        # Files the in-app updater leaves while an old program still runs.
        Get-ChildItem -LiteralPath $Dir -Force -File -ErrorAction SilentlyContinue |
            Where-Object { $_.Name -match '^\.?compa(-kernel)?\.exe(\.[0-9]+)?\.(old|new)$' } |
            ForEach-Object { Remove-Item -LiteralPath $_.FullName -Force -ErrorAction SilentlyContinue }
    }

    function Copy-File([string]$From, [string]$To) {
        for ($attempt = 1; ; $attempt++) {
            try {
                Copy-Item -LiteralPath $From -Destination $To -Force
                return
            } catch {
                # A scanner may briefly lock a freshly written program.
                if ($attempt -ge 5) { throw "Could not write ${To}: $($_.Exception.Message) Close Compa and run the installer again." }
                Start-Sleep -Seconds 1
            }
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
        # compa.exe is a console program that lives in the tray, so the
        # shortcut starts it hidden instead of leaving a console window open.
        $exe = (Join-Path $Dir 'compa.exe').Replace("'", "''")
        $powershell = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
        $shell = New-Object -ComObject WScript.Shell
        $link = $shell.CreateShortcut($LinkPath)
        $link.TargetPath = $powershell
        $link.Arguments = "-NoProfile -NonInteractive -WindowStyle Hidden -Command ""Start-Process -FilePath '$exe' -WindowStyle Hidden"""
        $link.WorkingDirectory = $Dir
        $link.WindowStyle = 7
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

    function Install-Compa([string]$Dir) {
        $tag = Resolve-Tag
        $arch = Get-Arch
        $archive = "compa_$($tag.Substring(1))_windows_$arch.zip"
        $base = 'https://github.com/' + $repo + '/releases/download'
        if ($env:COMPA_RELEASE_BASE_URL) { $base = $env:COMPA_RELEASE_BASE_URL.TrimEnd('/') }

        Write-Host "Installing Compa $tag (windows/$arch) into $Dir"
        # TLS 1.2 is off by default in older Windows PowerShell; GitHub requires it.
        [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

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

            Stop-Compa $Dir
            New-Item -ItemType Directory -Force -Path $Dir | Out-Null
            Remove-UpdateLeftovers $Dir
            foreach ($name in $installedFiles) {
                $from = Join-Path $stage $name
                if (Test-Path -LiteralPath $from -PathType Leaf) { Copy-File $from (Join-Path $Dir $name) }
            }
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
        if ($skipStart) {
            Write-Host "Start it with: & '$(Join-Path $Dir 'compa.exe')'"
        } else {
            Start-Process -FilePath (Join-Path $Dir 'compa.exe') -WorkingDirectory $Dir -WindowStyle Hidden
            Write-Host 'Compa is starting and opens your browser at http://127.0.0.1:18800 to finish setting up.'
        }
        Write-Host "Your settings and data live in $(Get-DataDir)."
    }

    function Uninstall-Compa([string]$Dir) {
        Write-Host "Removing Compa from $Dir"
        if (Test-Path -LiteralPath $Dir -PathType Container) {
            Stop-Compa $Dir
            Remove-UpdateLeftovers $Dir
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

    try {
        $dir = Get-InstallDir
        if ($removing) { Uninstall-Compa $dir } else { Install-Compa $dir }
    } catch {
        Write-Host ''
        Write-Host "Compa installer failed: $($_.Exception.Message)" -ForegroundColor Red
        # A script file fails its caller; piped into iex, stop without closing
        # the window.
        if ($PSCommandPath) { exit 1 }
    }
}
