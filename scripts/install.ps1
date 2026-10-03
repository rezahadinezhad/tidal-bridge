# One-step installation: build, background service, agent integration, the
# phone worker and universal mode. Safe to run again: finished steps are
# quick, and nothing is written into your projects.
#
#   pwsh -File scripts\install.ps1 [-Serial SERIAL] [-SkipPhone] [-NoShortcuts]
param(
    [string]$DataDir = (Join-Path $env:USERPROFILE '.tidalbridge'),
    [string]$Serial,
    [switch]$SkipPhone,
    [switch]$NoShortcuts
)
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$cli = Join-Path $root 'bin\tidalbridge.exe'
function Step([string]$text) { Write-Host "`n== $text" -ForegroundColor Cyan }

Step 'Requirements'
if ($PSVersionTable.PSVersion.Major -lt 7) { throw 'Run this with PowerShell 7 (pwsh): https://aka.ms/powershell' }
if (-not (Get-Command python -ErrorAction SilentlyContinue)) { throw 'Python 3.11 or newer is required: https://www.python.org/downloads/' }
$pythonVersion = & python -c "import sys; print('%d.%d' % sys.version_info[:2])"
if ([version]$pythonVersion -lt [version]'3.11') { throw "Python 3.11 or newer is required (found $pythonVersion)." }
Write-Host "PowerShell $($PSVersionTable.PSVersion), Python $pythonVersion"

Step 'Android platform tools'
$adb = (Get-Command adb -ErrorAction SilentlyContinue).Source
foreach ($candidate in @("$env:LOCALAPPDATA\Android\Sdk\platform-tools\adb.exe", (Join-Path $DataDir 'platform-tools\adb.exe'))) {
    if (-not $adb -and (Test-Path -LiteralPath $candidate)) { $adb = $candidate }
}
if (-not $adb) {
    Write-Host 'Downloading Android SDK Platform-Tools from Google; using them means accepting the Android SDK terms.'
    New-Item -ItemType Directory -Force $DataDir | Out-Null
    $zip = Join-Path $DataDir 'platform-tools.zip'
    Invoke-WebRequest 'https://dl.google.com/android/repository/platform-tools-latest-windows.zip' -OutFile $zip
    Expand-Archive -LiteralPath $zip -DestinationPath $DataDir -Force
    Remove-Item -LiteralPath $zip
    $adb = Join-Path $DataDir 'platform-tools\adb.exe'
}
Write-Host "adb: $adb"

Step 'Build (downloads Go into .tools on first use)'
& (Join-Path $PSScriptRoot 'build.ps1') -DataDir $DataDir
if (-not (Test-Path -LiteralPath $cli)) { throw 'Build failed.' }

Step 'Background service (Task Scheduler, no administrator rights)'
& (Join-Path $PSScriptRoot 'install-startup.ps1') -DataDir $DataDir
if (-not $NoShortcuts) { & (Join-Path $PSScriptRoot 'install-desktop.ps1') }

Step 'Agent integration (Claude Code, Codex)'
& (Join-Path $PSScriptRoot 'install-automation.ps1') -EnableCodex
& (Join-Path $PSScriptRoot 'setup-agents.ps1') -DataDir $DataDir
& $cli --data-dir $DataDir automation universal on | Out-Null

if (-not $SkipPhone) {
    Step 'Phone'
    $deadline = (Get-Date).AddMinutes(5)
    $told = $false
    while ($true) {
        $lines = & $adb devices | Select-Object -Skip 1 | Where-Object { $_.Trim() }
        if ($Serial) { $lines = $lines | Where-Object { $_ -like "$Serial*" } }
        if ($lines | Where-Object { $_ -match '\sdevice$' }) { break }
        if (-not $told) {
            Write-Host 'Connect the phone by USB, enable Developer options > USB debugging, then unlock it and allow debugging for this computer. Waiting...'
            $told = $true
        }
        if ((Get-Date) -gt $deadline) { throw 'No authorized phone found; run this script again once the phone is connected.' }
        Start-Sleep -Seconds 3
    }
    $arguments = @((Join-Path $PSScriptRoot 'install-adb-worker.py'), '--data-dir', $DataDir, '--adb', $adb)
    if ($Serial) { $arguments += @('--serial', $Serial) }
    Write-Host 'Installing the phone worker (the first time downloads Debian, Node and Python tools: several minutes)...'
    & python @arguments
    if ($LASTEXITCODE -ne 0) { throw 'Phone worker installation failed (see above).' }
    & $cli --data-dir $DataDir service stop | Out-Null
    & $cli --data-dir $DataDir service start | Out-Null
}

Step 'Check'
Start-Sleep -Seconds 5
$doctor = & $cli --data-dir $DataDir doctor | ConvertFrom-Json
$doctor.findings | ForEach-Object { Write-Host "- $_" }
Write-Host "`nDone. Open a new Claude Code or Codex session; ordinary commands now use the phone when it helps."
