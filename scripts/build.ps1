# -Force installs even while jobs run; they are interrupted (marked INTERRUPTED).
param([switch]$Test, [switch]$Force, [string]$DataDir = (Join-Path $env:USERPROFILE '.tidalbridge'))
$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$goPath = Join-Path $projectRoot '.tools\go\bin\go.exe'
# A release ships its programs ready-built in prebuilt\; a source checkout
# (it has .git) always compiles them, so a leftover prebuilt\ never installs
# programs older than the code.
$prebuilt = Join-Path $projectRoot 'prebuilt'
$usePrebuilt = (Test-Path -LiteralPath (Join-Path $prebuilt 'tidalbridge-task.exe')) -and -not (Test-Path -LiteralPath (Join-Path $projectRoot '.git'))
if (-not $usePrebuilt -and -not (Test-Path -LiteralPath $goPath)) { python (Join-Path $PSScriptRoot 'bootstrap-go.py'); if ($LASTEXITCODE -ne 0) { throw 'Go bootstrap failed' } }

# Replace an executable that may be running: Windows allows renaming a running
# image, so the old file moves aside and the new one takes its name.
function Replace-Binary([string]$Source, [string]$Target) {
    if (Test-Path -LiteralPath $Target) {
        $aside = "$Target.old-" + [Guid]::NewGuid().ToString('N').Substring(0, 8)
        try { Copy-Item -LiteralPath $Source -Destination $Target -Force; return }
        catch { Rename-Item -LiteralPath $Target -NewName (Split-Path -Leaf $aside) }
    }
    Copy-Item -LiteralPath $Source -Destination $Target -Force
}

Push-Location $projectRoot
try {
    if ($usePrebuilt) {
        New-Item -ItemType Directory -Force -Path bin | Out-Null
        foreach ($name in 'tidalbridge', 'tidalbridge-service', 'tidalbridge-task') {
            Copy-Item -LiteralPath (Join-Path $prebuilt "$name.exe") -Destination "bin/$name-next.exe" -Force
        }
        Copy-Item -LiteralPath (Join-Path $prebuilt 'tidalbridge-desktop.exe') -Destination 'bin/tidalbridge-desktop.exe' -Force
    } else {
        & $goPath build -trimpath -ldflags '-s -w' -o bin/tidalbridge-next.exe ./cmd/tidalbridge
        if ($LASTEXITCODE -ne 0) { throw 'Build failed' }
        & $goPath build -trimpath -ldflags '-s -w -H windowsgui' -o bin/tidalbridge-service-next.exe ./cmd/tidalbridge-service
        if ($LASTEXITCODE -ne 0) { throw 'Service build failed' }
        & $goPath build -trimpath -ldflags '-s -w -H windowsgui' -o bin/tidalbridge-desktop.exe ./cmd/tidalbridge-desktop
        if ($LASTEXITCODE -ne 0) { throw 'Desktop launcher build failed' }
        & $goPath build -trimpath -ldflags '-s -w' -o bin/tidalbridge-task-next.exe ./cmd/tidalbridge-task
        if ($LASTEXITCODE -ne 0) { throw 'Task adapter build failed' }
    }

    $binDir = [IO.Path]::GetFullPath((Join-Path $projectRoot 'bin'))
    $hosts = @(Get-CimInstance Win32_Process | Where-Object {
        $_.ExecutablePath -and ([IO.Path]::GetDirectoryName($_.ExecutablePath) -eq $binDir) -and
        ($_.Name -eq 'tidalbridge-service.exe' -or ($_.Name -eq 'tidalbridge.exe' -and $_.CommandLine -match '\bserve\b'))
    })
    foreach ($hostProcess in $hosts) {
        $hostDataDir = if ($hostProcess.CommandLine -match '--data-dir\s+"([^"]+)"') { $Matches[1] } else { $DataDir }
        $hostStatus = & (Join-Path $binDir 'tidalbridge-next.exe') --data-dir $hostDataDir status | ConvertFrom-Json
        if (-not $Force -and ($LASTEXITCODE -ne 0 -or $hostStatus.active_jobs -ne 0 -or $hostStatus.queue_depth -ne 0)) { throw 'Host has active or queued work. Leave it running; rebuild after it is idle (or pass -Force).' }
    }
    $serviceInstalled = $false
    & (Join-Path $env:SystemRoot 'System32\schtasks.exe') /Query /TN 'Tidal Bridge Host' *> $null
    if ($LASTEXITCODE -eq 0) { $serviceInstalled = $true; & (Join-Path $env:SystemRoot 'System32\schtasks.exe') /End /TN 'Tidal Bridge Host' *> $null }
    foreach ($hostProcess in $hosts) {
        Stop-Process -Id $hostProcess.ProcessId -Force -ErrorAction SilentlyContinue
        Wait-Process -Id $hostProcess.ProcessId -Timeout 10 -ErrorAction SilentlyContinue
    }
    Replace-Binary 'bin\tidalbridge-next.exe' 'bin\tidalbridge.exe'
    Replace-Binary 'bin\tidalbridge-service-next.exe' 'bin\tidalbridge-service.exe'
    Replace-Binary 'bin\tidalbridge-task-next.exe' 'bin\tidalbridge-task.exe'
    # Installed command adapters are copies of tidalbridge-task.exe.
    $shimRoot = Join-Path $DataDir 'shims'
    if (Test-Path -LiteralPath (Join-Path $shimRoot 'shims.json')) {
        Get-ChildItem -LiteralPath $shimRoot -Filter '*.exe' | ForEach-Object { Replace-Binary 'bin\tidalbridge-task.exe' $_.FullName }
        Get-ChildItem -LiteralPath $shimRoot -Filter '*.old-*' | ForEach-Object { try { [IO.File]::Delete($_.FullName) } catch {} }
    }
    Get-ChildItem -LiteralPath $binDir -Filter '*.old-*' | ForEach-Object { try { [IO.File]::Delete($_.FullName) } catch {} }
    if ($serviceInstalled -or $hosts.Count) {
        & (Join-Path $binDir 'tidalbridge.exe') --data-dir $DataDir service start
        if ($LASTEXITCODE -ne 0) { throw 'Host did not restart after the update' }
    }
    if ($Test) { & $goPath test ./...; if ($LASTEXITCODE -ne 0) { throw 'Tests failed' }; python tests/e2e/run.py; if ($LASTEXITCODE -ne 0) { throw 'E2E failed' } }
    $global:LASTEXITCODE = 0
} finally { Pop-Location }
