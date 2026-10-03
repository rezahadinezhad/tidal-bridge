param([string]$DataDir = (Join-Path $env:USERPROFILE '.tidalbridge'))
$ErrorActionPreference = 'Stop'
$binaryPath = Join-Path (Split-Path -Parent $PSScriptRoot) 'bin\tidalbridge.exe'
if (-not (Test-Path -LiteralPath $binaryPath)) { throw 'Build first: scripts/build.ps1' }
# Starts the registered background service (or confirms it is running).
& $binaryPath --data-dir $DataDir service start
if ($LASTEXITCODE -ne 0) { throw "Host did not become ready. See $(Join-Path $DataDir 'service.log')" }
