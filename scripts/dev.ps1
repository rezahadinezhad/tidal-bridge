param([switch]$Mock)
$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
& (Join-Path $PSScriptRoot 'build.ps1')
if ($Mock) { python (Join-Path $PSScriptRoot 'mock-pool.py') } else { & (Join-Path $projectRoot 'bin\tidalbridge.exe') serve }
