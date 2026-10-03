# Updates this checkout: pulls, rebuilds (restarting the service, refreshing
# the command adapters) and pushes the new worker to the connected phone.
param([string]$DataDir = (Join-Path $env:USERPROFILE '.tidalbridge'))
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
git -C $root pull --ff-only
if ($LASTEXITCODE -ne 0) { throw 'git pull failed; commit or stash local changes first.' }
& (Join-Path $PSScriptRoot 'build.ps1') -DataDir $DataDir
python (Join-Path $PSScriptRoot 'install-adb-worker.py') --data-dir $DataDir --skip-provision --no-approve
if ($LASTEXITCODE -ne 0) { Write-Warning 'The phone worker was not updated (is the phone connected?); run this again later.' }
& (Join-Path $root 'bin\tidalbridge.exe') --data-dir $DataDir doctor | ConvertFrom-Json | ForEach-Object { $_.findings } | ForEach-Object { Write-Host "- $_" }
