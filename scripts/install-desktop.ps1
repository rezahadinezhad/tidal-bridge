param([string]$DataDir = (Join-Path $env:USERPROFILE '.tidalbridge'))
$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$launcherPath = Join-Path $projectRoot 'bin\tidalbridge-desktop.exe'
$iconPath = Join-Path $projectRoot 'apps\dashboard\assets\tidalbridge.ico'
if (-not (Test-Path -LiteralPath $launcherPath)) { throw 'Build first: scripts/build.ps1' }
if (-not (Test-Path -LiteralPath $iconPath)) { & (Join-Path $PSScriptRoot 'generate-app-icon.ps1') }
$desktopPath = [Environment]::GetFolderPath('Desktop')
$startMenuPath = Join-Path ([Environment]::GetFolderPath('StartMenu')) 'Programs'
$shortcutShell = New-Object -ComObject WScript.Shell
foreach ($shortcutFolder in @($desktopPath, $startMenuPath)) {
    New-Item -ItemType Directory -Path $shortcutFolder -Force | Out-Null
    $shortcutPath = Join-Path $shortcutFolder 'Tidal Bridge.lnk'
    $shortcut = $shortcutShell.CreateShortcut($shortcutPath)
    $shortcut.TargetPath = $launcherPath
    $shortcut.Arguments = '--data-dir "' + $DataDir + '"'
    $shortcut.WorkingDirectory = $projectRoot
    $shortcut.Description = 'Open Tidal Bridge'
    $shortcut.IconLocation = $iconPath + ',0'
    $shortcut.Save()
    Write-Output "Installed $shortcutPath"
}
