param(
    [string]$DataDir = (Join-Path $env:USERPROFILE '.tidalbridge'),
    [switch]$InstructionsOnly
)
$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$binaryPath = Join-Path $projectRoot 'bin\tidalbridge.exe'
if (-not $InstructionsOnly -and (Get-Command codex -ErrorAction SilentlyContinue)) {
    codex mcp add tidalbridge -- $binaryPath --data-dir $DataDir mcp
    if ($LASTEXITCODE -ne 0) { throw 'Codex MCP registration failed' }
}
# Claude Code: MCP server, PATH hooks and instructions (see configure-claude.py).
if (-not $InstructionsOnly) {
    python (Join-Path $PSScriptRoot 'configure-claude.py') --data-dir $DataDir
    if ($LASTEXITCODE -ne 0) { throw 'Claude Code registration failed' }
}
$instructions = @'

<!-- tidalbridge:start -->
Tidal Bridge offloads work from this laptop to the user's USB-connected Android
phone. It is automatic: in enabled projects, supported commands (tests,
type-checks, lint, dev servers) are routed by command adapters. Run commands
normally. A stderr line "[Tidal Bridge] ... -> worker-..." means the command
ran on the phone; its output and exit code are the real results. Do not bypass
adapters with absolute runtime paths. Tidal Bridge alone decides where a
command runs and re-checks doubtful phone failures on this laptop itself, so a
failure it reports is real; do not set TIDALBRIDGE_* variables. Status:
& '__TIDAL_BINARY__' status
Communicate with this user in English.
<!-- tidalbridge:end -->
'@
$instructions = $instructions.Replace('__TIDAL_BINARY__', $binaryPath.Replace("'", "''"))
foreach ($agentFolder in @('.codex','.claude')) {
    $folderPath = Join-Path ([Environment]::GetFolderPath('UserProfile')) $agentFolder
    New-Item -ItemType Directory -Path $folderPath -Force | Out-Null
    $fileName = if ($agentFolder -eq '.codex') { 'AGENTS.md' } else { 'CLAUDE.md' }
    $path = Join-Path $folderPath $fileName
    $content = if (Test-Path -LiteralPath $path) { [IO.File]::ReadAllText($path) } else { '' }
    $content = [regex]::Replace($content, '(?s)\s*<!-- tidalbridge:start -->.*?<!-- tidalbridge:end -->', '')
    [IO.File]::WriteAllText($path, $content.TrimEnd() + "`r`n" + $instructions + "`r`n")
}
