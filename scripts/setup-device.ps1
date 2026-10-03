param([Parameter(Mandatory)][string]$Serial, [string]$DataDir = (Join-Path $env:USERPROFILE '.tidalbridge'))
$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$adbCommand = Get-Command adb -ErrorAction SilentlyContinue
$adbPath = if ($adbCommand) { $adbCommand.Source } else { Join-Path $env:LOCALAPPDATA 'Android\Sdk\platform-tools\adb.exe' }
if (-not (Test-Path -LiteralPath $adbPath)) { throw 'ADB is missing. Install Android SDK Platform Tools.' }
$state = & $adbPath -s $Serial get-state
if ($LASTEXITCODE -ne 0 -or $state -ne 'device') { throw 'Connect and authorize the device first.' }
$package = & $adbPath -s $Serial shell pm list packages com.termux
if ($package -notmatch 'package:com.termux\b') { throw 'Install Termux from its official GitHub or F-Droid source, then open it once. See docs/SETUP.md.' }
New-Item -ItemType Directory -Path $DataDir -Force | Out-Null
$tokenPath = Join-Path $DataDir ('pair-' + $Serial + '.token')
if ($Serial -notmatch '^[a-zA-Z0-9_-]+$') { throw 'Serial contains unsupported characters.' }
if (-not (Test-Path -LiteralPath $tokenPath)) {
    $randomBytes = New-Object byte[] 24
    [Security.Cryptography.RandomNumberGenerator]::Fill($randomBytes)
    [IO.File]::WriteAllText($tokenPath, [Convert]::ToHexString($randomBytes).ToLowerInvariant())
}
$permissionTest = & $adbPath -s $Serial shell run-as com.termux id 2>&1
if ($LASTEXITCODE -ne 0) { throw 'This Termux installation does not expose run-as. Use the manual pairing method in docs/SETUP.md; no root is needed.' }
foreach ($stagedFile in @(@('apps\android-worker\worker.py','worker.py'), @('scripts\setup-android.sh','setup-android.sh'))) {
    & $adbPath -s $Serial push (Join-Path $projectRoot $stagedFile[0]) ('/data/local/tmp/tidalbridge-' + $stagedFile[1]) | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'File staging failed' }
}
& $adbPath -s $Serial push $tokenPath '/data/local/tmp/tidalbridge-worker.token' | Out-Null
& $adbPath -s $Serial shell 'run-as com.termux sh -c ''mkdir -p files/home/tidalbridge/worker; cp /data/local/tmp/tidalbridge-worker.py files/home/tidalbridge/worker/worker.py; cp /data/local/tmp/tidalbridge-worker.token files/home/tidalbridge/worker.token; cp /data/local/tmp/tidalbridge-setup-android.sh files/home/tidalbridge/setup-android.sh; chmod 600 files/home/tidalbridge/worker.token'''
if ($LASTEXITCODE -ne 0) { throw 'Termux staging failed' }
& $adbPath -s $Serial shell rm -f /data/local/tmp/tidalbridge-worker.py /data/local/tmp/tidalbridge-worker.token /data/local/tmp/tidalbridge-setup-android.sh
& (Join-Path $projectRoot 'bin\tidalbridge.exe') --data-dir $DataDir approve --serial $Serial --token-file $tokenPath
if ($LASTEXITCODE -ne 0) { throw 'Device approval failed' }
Write-Output 'Files and secret paired. In Termux, run once: bash ~/tidalbridge/setup-android.sh'
