# One-time shell launcher for tools such as Claude Code. Adapters only route
# commands inside projects explicitly enabled in Automation settings.
[CmdletBinding(PositionalBinding=$false)]
param(
    [string]$DataDir = (Join-Path $env:USERPROFILE '.tidalbridge'),
    [Parameter(Position=0,ValueFromRemainingArguments=$true)][string[]]$Command
)
$ErrorActionPreference = 'Stop'
$adapterDirectory = Join-Path $DataDir 'shims'
if (-not (Test-Path -LiteralPath (Join-Path $adapterDirectory 'shims.json'))) { throw 'Install command adapters first.' }
$previousPath = $env:PATH
try {
    $env:PATH = "$adapterDirectory;$previousPath"
    if (-not $Command.Count) { & pwsh -NoLogo }
    elseif ($Command.Count -eq 1) { & $Command[0] }
    else { & $Command[0] $Command[1..($Command.Count-1)] }
    $result = $LASTEXITCODE
} finally { $env:PATH = $previousPath }
exit $result
