param(
    [string]$DataDir = (Join-Path $env:USERPROFILE '.tidalbridge'),
    [switch]$EnableCodex,
    [switch]$RemoveCodex
)
$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$shimRoot = Join-Path $DataDir 'shims'
$adapterBinary = Join-Path $projectRoot 'bin\tidalbridge-task.exe'
$configEditor = Join-Path $PSScriptRoot 'configure-agent-automation.py'
if ($RemoveCodex) { python $configEditor --remove; if ($LASTEXITCODE -ne 0) { throw 'Automation removal failed' }; return }
if (-not (Test-Path -LiteralPath $adapterBinary)) { throw 'Build Tidal Bridge before installing command adapters.' }
New-Item -ItemType Directory -Path $shimRoot -Force | Out-Null
$commands = @{}
$pythonCommand = Get-Command python.exe -ErrorAction SilentlyContinue
$nodeCommand = Get-Command node.exe -ErrorAction SilentlyContinue
if ($pythonCommand -and $pythonCommand.Source -notlike "$shimRoot\*") {
    $commands.python = @($pythonCommand.Source)
    $commands.python3 = @($pythonCommand.Source)
}
if ($nodeCommand -and $nodeCommand.Source -notlike "$shimRoot\*") { $commands.node = @($nodeCommand.Source) }
foreach ($tool in @('pytest','ruff','mypy')) {
    $original = Get-Command "$tool.exe" -ErrorAction SilentlyContinue
    if ($original -and $original.Source -notlike "$shimRoot\*") { $commands[$tool] = @($original.Source) }
}
$nodeEntries = @{tsc='typescript\bin\tsc';eslint='eslint\bin\eslint.js';prettier='prettier\bin\prettier.cjs';vitest='vitest\vitest.mjs';jest='jest\bin\jest.js';npm='npm\bin\npm-cli.js';npx='npm\bin\npx-cli.js'}
if ($commands.node) {
    foreach ($tool in $nodeEntries.Keys) {
        $entry = Join-Path (Split-Path -Parent $commands.node[0]) (Join-Path 'node_modules' $nodeEntries[$tool])
        if (-not (Test-Path -LiteralPath $entry)) {
            $original = Get-Command "$tool.ps1" -ErrorAction SilentlyContinue
            if ($original -and $original.Source -notlike "$shimRoot\*") { $entry = Join-Path (Split-Path -Parent $original.Source) (Join-Path 'node_modules' $nodeEntries[$tool]) }
        }
        if (Test-Path -LiteralPath $entry) { $commands[$tool] = @($commands.node[0], $entry) }
    }
}
# A repeat install preserves previously resolved originals rather than capturing
# this installation's own adapters and creating a recursion loop.
$installationPath = Join-Path $shimRoot 'shims.json'
if (Test-Path -LiteralPath $installationPath) {
    $previous = Get-Content -LiteralPath $installationPath -Raw | ConvertFrom-Json
    foreach ($property in $previous.local_commands.PSObject.Properties) { if (-not $commands.ContainsKey($property.Name)) { $commands[$property.Name] = @($property.Value) } }
}
foreach ($tool in @('python','python3','node','npm','npx','pytest','ruff','mypy','tsc','eslint','prettier','vitest','jest')) {
    Copy-Item -LiteralPath $adapterBinary -Destination (Join-Path $shimRoot "$tool.exe") -Force
}
$installation = @{version=1;data_dir=[IO.Path]::GetFullPath($DataDir);local_commands=$commands}
[IO.File]::WriteAllText($installationPath, ($installation | ConvertTo-Json -Depth 5))
if ($EnableCodex) {
    & $commands.python[0] $configEditor --shim-dir $shimRoot
    if ($LASTEXITCODE -ne 0) { throw 'Codex adapter registration failed' }
}
Write-Output "Installed project-scoped adapters: $shimRoot"
Write-Output 'Universal mode recognizes safe commands in every project (tidalbridge automation universal on|off). New Codex sessions load the registered command environment.'
