# -RemovePhone also deletes the worker, its Debian userland and the project
# copies (including shared .env files) from every connected phone.
param([string]$DataDir = (Join-Path $env:USERPROFILE '.tidalbridge'), [switch]$RemovePhone)
$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$binaryPath = [IO.Path]::GetFullPath((Join-Path $projectRoot 'bin\tidalbridge.exe'))
# Legacy login entry from earlier versions, then the Task Scheduler service.
Remove-ItemProperty -Path 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run' -Name 'TidalBridge' -ErrorAction SilentlyContinue
if (Test-Path -LiteralPath $binaryPath) { & $binaryPath --data-dir $DataDir service uninstall }
$shortcutFolders = @([Environment]::GetFolderPath('Desktop'), (Join-Path ([Environment]::GetFolderPath('StartMenu')) 'Programs'))
foreach ($shortcutFolder in $shortcutFolders) {
    $shortcutPath = Join-Path $shortcutFolder 'Tidal Bridge.lnk'
    if (Test-Path -LiteralPath $shortcutPath) { Remove-Item -LiteralPath $shortcutPath }
}
python (Join-Path $PSScriptRoot 'configure-agent-automation.py') --remove
if ($LASTEXITCODE -ne 0) { throw 'Codex command environment was edited after installation. Preserve those edits and remove only the Tidal Bridge prefix before uninstalling.' }
python (Join-Path $PSScriptRoot 'configure-claude.py') --remove
if ($LASTEXITCODE -ne 0) { throw 'Claude Code integration removal failed' }
$binDir = [IO.Path]::GetDirectoryName($binaryPath)
Get-CimInstance Win32_Process | Where-Object {
    $_.ExecutablePath -and [IO.Path]::GetDirectoryName($_.ExecutablePath) -eq $binDir -and
    ($_.Name -eq 'tidalbridge-service.exe' -or ($_.Name -eq 'tidalbridge.exe' -and $_.CommandLine -match '\bserve\b'))
} | ForEach-Object { Stop-Process -Id $_.ProcessId -ErrorAction SilentlyContinue }
if ($RemovePhone) {
    $adb = (Get-Command adb -ErrorAction SilentlyContinue).Source
    foreach ($candidate in @("$env:LOCALAPPDATA\Android\Sdk\platform-tools\adb.exe", (Join-Path $DataDir 'platform-tools\adb.exe'))) {
        if (-not $adb -and (Test-Path -LiteralPath $candidate)) { $adb = $candidate }
    }
    if (-not $adb) { Write-Warning 'adb not found; the phone was not cleaned.' }
    else {
        foreach ($line in (& $adb devices | Select-Object -Skip 1 | Where-Object { $_ -match '\sdevice$' })) {
            $serial = ($line -split '\s+')[0]
            # Only Tidal Bridge's own directories; files the userland made read-only are unlocked first.
            & $adb -s $serial shell 'pkill -f "worker[.]py --root /data/local/tmp/tidalbridge"; chmod -R u+rwX /data/local/tmp/tidalbridge 2>/dev/null; rm -rf /data/local/tmp/tidalbridge /data/local/tmp/tb'
            Write-Output "Removed the worker, its userland and project copies from $serial."
        }
    }
}
Write-Output 'Service, shortcuts and agent integrations removed; host stopped. Configuration, secrets, profiles, adapter files and results are preserved.'
