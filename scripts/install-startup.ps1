param([string]$DataDir = (Join-Path $env:USERPROFILE '.tidalbridge'))
$ErrorActionPreference = 'Stop'
# Task Scheduler starts the host at logon outside any app container. The HKCU
# Run key used before is virtualized for MSIX-packaged agents (Claude, Codex).
$binaryPath = Join-Path (Split-Path -Parent $PSScriptRoot) 'bin\tidalbridge.exe'
if (-not (Test-Path -LiteralPath $binaryPath)) { throw 'Build first: scripts/build.ps1' }
Remove-ItemProperty -Path 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run' -Name 'TidalBridge' -ErrorAction SilentlyContinue
& $binaryPath --data-dir $DataDir service install
if ($LASTEXITCODE -ne 0) { throw 'Service registration failed' }
