# Tests install.ps1 against fake releases served from a temporary folder:
#
#   pwsh -File scripts/install/test-install.ps1 [-Shell powershell]
#
# -Shell picks the PowerShell that runs the installer: pwsh (the default) or
# powershell, Windows PowerShell 5.1. Needs Go to build testhelper.go, the
# stand-in programs and web server. It installs with COMPA_NO_PATH and
# COMPA_NO_SHORTCUT, so your PATH and Start Menu stay as they are.
param([ValidateSet('pwsh', 'powershell')][string]$Shell = 'pwsh')

$ErrorActionPreference = 'Stop'
$installer = Join-Path $PSScriptRoot 'install.ps1'
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
$elevated = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole(
    [Security.Principal.WindowsBuiltInRole]::Administrator)

$work = Join-Path ([IO.Path]::GetTempPath()) ('compa-install-test-' + [guid]::NewGuid().ToString('N'))
$bin = Join-Path $work 'bin'
$data = Join-Path $work 'home'
$out = Join-Path $work 'out.txt'
New-Item -ItemType Directory -Path $work | Out-Null
$failures = 0

function Pass([string]$Name) { Write-Host "ok   $Name" }
function Fail([string]$Name) {
    Write-Host "FAIL $Name"
    if (Test-Path -LiteralPath $out) { Get-Content -LiteralPath $out | ForEach-Object { Write-Host "     | $_" } }
    $script:failures++
}

function New-Release([string]$Version, [switch]$Corrupt) {
    $files = Join-Path $work "files\$Version"
    $dir = Join-Path $work "releases\v$Version"
    New-Item -ItemType Directory -Force -Path $files, $dir | Out-Null
    foreach ($name in 'compa.exe', 'compa-kernel.exe') {
        Copy-Item -LiteralPath $helper -Destination (Join-Path $files $name)
        [IO.File]::AppendAllText((Join-Path $files $name), $Version)
    }
    $zip = Join-Path $dir "compa_${Version}_windows_$arch.zip"
    Compress-Archive -Path (Join-Path $files '*') -DestinationPath $zip
    $hash = (Get-FileHash -LiteralPath $zip -Algorithm SHA256).Hash.ToLowerInvariant()
    Set-Content -LiteralPath (Join-Path $dir 'SHA256SUMS') -Value "$hash  $(Split-Path -Leaf $zip)"
    if ($Corrupt) { [IO.File]::AppendAllText($zip, 'x') }
}

# Invoke-WithEnvironment VERSION VARS SCRIPT: run SCRIPT with the installer
# settings for VERSION in the environment, VARS overriding them, as under CI.
function Invoke-WithEnvironment([string]$Version, [hashtable]$Vars, [scriptblock]$Script) {
    $settings = @{
        CI = 'true'; COMPA_HOME = $data; COMPA_INSTALL_DIR = $bin; COMPA_RELEASE_BASE_URL = $base
        COMPA_INSTALL_TEST = '1'; COMPA_VERSION = "v$Version"; COMPA_NO_PATH = '1'; COMPA_NO_SHORTCUT = '1'
        COMPA_ALLOW_ROOT = $(if ($elevated) { '1' } else { '' }); FAKE_COMPA = ''
    }
    foreach ($key in $Vars.Keys) { $settings[$key] = $Vars[$key] }
    $saved = @{}
    foreach ($key in $settings.Keys) {
        $saved[$key] = [Environment]::GetEnvironmentVariable($key)
        [Environment]::SetEnvironmentVariable($key, $settings[$key])
    }
    try { & $Script }
    finally {
        foreach ($key in $saved.Keys) { [Environment]::SetEnvironmentVariable($key, $saved[$key]) }
    }
}

function ConvertTo-Encoded([string]$Command) { [Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($Command)) }

# Invoke-Installer VERSION [VARS]: run install.ps1 as a script file, its
# output in $out. Returns whether it succeeded.
function Invoke-Installer([string]$Version, [hashtable]$Vars = @{}) {
    Invoke-WithEnvironment $Version $Vars {
        & $Shell -NoProfile -ExecutionPolicy Bypass -File $installer *> $out
        $LASTEXITCODE -eq 0
    }
}

# Invoke-OnDesktop VERSION [VARS]: the same outside CI, in a console of its
# own as when someone runs it on their desktop; a transcript keeps the output.
function Invoke-OnDesktop([string]$Version, [hashtable]$Vars = @{}) {
    $Vars['CI'] = ''
    # pwsh hands its own module path to programs it starts this way, which
    # hides Windows PowerShell's built-in modules; without it, each picks its own.
    $Vars['PSModulePath'] = ''
    $command = "Start-Transcript -LiteralPath '$out' | Out-Null; & '$installer'; Stop-Transcript | Out-Null"
    Invoke-WithEnvironment $Version $Vars {
        $p = Start-Process -FilePath $Shell -WindowStyle Hidden -PassThru -ArgumentList '-NoProfile', '-ExecutionPolicy', 'Bypass',
        '-EncodedCommand', (ConvertTo-Encoded $command)
        # Not Start-Process -Wait, which also waits for the Compa it starts.
        $p.WaitForExit()
        $p.ExitCode -eq 0
    }
}

function Test-Installed([string]$Version) {
    foreach ($name in 'compa.exe', 'compa-kernel.exe') {
        $have = Join-Path $bin $name
        if (-not (Test-Path -LiteralPath $have)) { return $false }
        $want = Join-Path $work "files\$Version\$name"
        if ((Get-FileHash -LiteralPath $have).Hash -ne (Get-FileHash -LiteralPath $want).Hash) { return $false }
    }
    return $true
}

function Test-NoLeftover { return -not (Get-ChildItem -LiteralPath $bin -Force -Filter '.*.install-*') }
function Test-Output([string]$Text) { return (Get-Content -LiteralPath $out -Raw).Contains($Text) }
function Get-Running { Get-Process -Name compa, compa-kernel -ErrorAction SilentlyContinue | Where-Object { $_.Path -like "$bin\*" } }
function Start-Fake { return Start-Process -FilePath (Join-Path $bin 'compa.exe') -WindowStyle Hidden -PassThru }

$server = $null
try {
    $helper = Join-Path $work 'helper.exe'
    go build -o $helper (Join-Path $PSScriptRoot 'testhelper.go')
    if ($LASTEXITCODE -ne 0) { throw 'could not build testhelper.go' }
    New-Release '1.0.0'
    New-Release '2.0.0'
    New-Release '3.0.0' -Corrupt
    $portFile = Join-Path $work 'port'
    $server = Start-Process -FilePath $helper -WindowStyle Hidden -PassThru -ArgumentList 'serve',
    "`"$(Join-Path $work 'releases')`"", "`"$portFile`""
    for ($i = 0; $i -lt 100 -and -not (Test-Path -LiteralPath $portFile); $i++) { Start-Sleep -Milliseconds 100 }
    $base = "http://127.0.0.1:$(Get-Content -LiteralPath $portFile)"
    Write-Host "Testing install.ps1 under $Shell"

    if (-not (Invoke-Installer '1.0.0' @{ COMPA_INSTALL_TEST = '' }) -and (Test-Output 'must be an https:// address') -and
        -not (Test-Path -LiteralPath $bin)) {
        Pass 'refuses an http:// release address'
    } else { Fail 'refuses an http:// release address' }

    if ($elevated) {
        if (-not (Invoke-Installer '1.0.0' @{ COMPA_ALLOW_ROOT = '' }) -and (Test-Output 'running as administrator') -and
            -not (Test-Path -LiteralPath $bin)) {
            Pass 'refuses to install from an elevated session'
        } else { Fail 'refuses to install from an elevated session' }
    }

    if ((Invoke-Installer '1.0.0') -and (Test-Installed '1.0.0') -and (Test-Output 'Compa was not started') -and (Test-NoLeftover)) {
        Pass 'installs, and does not start Compa under CI'
    } else { Fail 'installs, and does not start Compa under CI' }

    $fake = Start-Fake
    # A copy in a folder inside the install folder is another copy of Compa.
    $inside = Join-Path $bin 'inside'
    New-Item -ItemType Directory -Force -Path $inside | Out-Null
    Copy-Item -LiteralPath (Join-Path $bin 'compa.exe') -Destination $inside
    $other = Start-Process -FilePath (Join-Path $inside 'compa.exe') -WindowStyle Hidden -PassThru
    if ((Invoke-Installer '2.0.0' @{ COMPA_NO_START = '1' }) -and (Test-Installed '2.0.0') -and (Test-NoLeftover) -and
        $fake.WaitForExit(5000) -and -not $other.HasExited) {
        Pass 'upgrades both programs and stops the running Compa, not a copy in a folder inside'
    } else { Fail 'upgrades both programs and stops the running Compa, not a copy in a folder inside' }
    Stop-Process -Id $other.Id -Force -ErrorAction SilentlyContinue
    [void]$other.WaitForExit(5000)
    Remove-Item -LiteralPath $inside -Recurse -Force

    $fake = Start-Fake
    $everyone = '*S-1-1-0'
    icacls $bin /deny "${everyone}:(W)" | Out-Null
    try {
        $ok = -not (Invoke-Installer '1.0.0' @{ COMPA_NO_START = '1' }) -and (Test-Output 'Nothing was changed') -and
        (Test-Installed '2.0.0') -and -not $fake.HasExited
    } finally { icacls $bin /remove:d $everyone | Out-Null }
    if ($ok) { Pass "leaves a folder it can't write, and the running Compa, alone" }
    else { Fail "leaves a folder it can't write, and the running Compa, alone" }
    Get-Running | Stop-Process -Force

    if (-not (Invoke-Installer '3.0.0') -and (Test-Output 'Checksum mismatch') -and (Test-Installed '2.0.0') -and (Test-NoLeftover)) {
        Pass 'installs nothing from a download that fails its checksum'
    } else { Fail 'installs nothing from a download that fails its checksum' }

    New-Item -ItemType Directory -Force -Path $data | Out-Null
    Set-Content -LiteralPath (Join-Path $data 'launcher-config.json') -Value '{ "port": 18888, "public": false }'
    if (-not [Environment]::UserInteractive) {
        Write-Host 'skipped: starting Compa (this session has no desktop)'
    } else {
        if ((Invoke-OnDesktop '2.0.0') -and (Test-Output 'open http://localhost:18888') -and (Get-Running)) {
            Pass 'starts Compa on a desktop, and prints its port'
        } else { Fail 'starts Compa on a desktop, and prints its port' }
        Get-Running | Stop-Process -Force

        if ((Invoke-OnDesktop '2.0.0' @{ FAKE_COMPA = 'fail' }) -and (Test-Output 'Compa stopped right after starting') -and
            (Test-Output 'address already in use')) {
            Pass 'shows launcher.log when Compa stops right after starting'
        } else { Fail 'shows launcher.log when Compa stops right after starting' }
    }

    # As "irm | iex" runs it, in a session with its own $Version and TLS
    # setting: both stay as they were, and the failure reaches the caller.
    $probe = @"
`$Version = 'mine'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls11
try { Get-Content -Raw '$installer' | Invoke-Expression; 'no error' } catch { 'caught: ' + `$_.Exception.Message }
"Version=`$Version tls=`$([Net.ServicePointManager]::SecurityProtocol) functions=`$([bool](Get-Command Install-Compa -ErrorAction SilentlyContinue))"
"@
    Invoke-WithEnvironment '1.0.0' @{ COMPA_INSTALL_TEST = '' } {
        & $Shell -NoProfile -ExecutionPolicy Bypass -EncodedCommand (ConvertTo-Encoded $probe) *> $out
    }
    if ((Test-Output 'caught: Compa installer failed: COMPA_RELEASE_BASE_URL must be an https:// address') -and
        (Test-Output 'Version=mine tls=Tls11 functions=False')) {
        Pass 'leaves the session as it was under iex, and reports the failure'
    } else { Fail 'leaves the session as it was under iex, and reports the failure' }

    if ((Invoke-Installer '2.0.0' @{ COMPA_UNINSTALL = '1' }) -and -not (Test-Path -LiteralPath (Join-Path $bin 'compa.exe')) -and
        (Test-Path -LiteralPath $data)) {
        Pass 'uninstalls the programs and keeps the data'
    } else { Fail 'uninstalls the programs and keeps the data' }
} finally {
    Get-Running | Stop-Process -Force -ErrorAction SilentlyContinue
    if ($server) { Stop-Process -Id $server.Id -Force -ErrorAction SilentlyContinue }
    Start-Sleep -Milliseconds 500
    Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction SilentlyContinue
}

if ($failures) {
    Write-Host "$failures installer test(s) failed"
    exit 1
}
Write-Host 'installer tests passed'
