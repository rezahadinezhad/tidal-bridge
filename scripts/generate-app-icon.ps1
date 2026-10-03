# Draw the existing two-node bridge motif into a multi-size Windows icon.
$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Drawing
$projectRoot = Split-Path -Parent $PSScriptRoot
$assetFolder = Join-Path $projectRoot 'apps\dashboard\assets'
New-Item -ItemType Directory -Path $assetFolder -Force | Out-Null
$frames = @()
foreach ($size in @(16,24,32,48,64,128,256)) {
    $bitmap = New-Object Drawing.Bitmap($size, $size)
    $graphics = [Drawing.Graphics]::FromImage($bitmap)
    $graphics.SmoothingMode = [Drawing.Drawing2D.SmoothingMode]::AntiAlias
    $graphics.Clear([Drawing.Color]::FromArgb(255,11,18,38))
    $scale = $size / 64.0
    $graphics.ScaleTransform($scale,$scale)
    $gradient = New-Object Drawing.Drawing2D.LinearGradientBrush([Drawing.PointF]::new(15,32),[Drawing.PointF]::new(49,32),[Drawing.ColorTranslator]::FromHtml('#42d3ff'),[Drawing.ColorTranslator]::FromHtml('#8b5cf6'))
    $pen = New-Object Drawing.Pen($gradient,4)
    $graphics.DrawBezier($pen,17,32,29,9,35,55,47,32)
    $cyan = New-Object Drawing.SolidBrush([Drawing.ColorTranslator]::FromHtml('#42d3ff'))
    $violet = New-Object Drawing.SolidBrush([Drawing.ColorTranslator]::FromHtml('#8b5cf6'))
    $graphics.FillEllipse($cyan,8,26,12,12)
    $graphics.FillEllipse($violet,44,26,12,12)
    $stream = New-Object IO.MemoryStream
    $bitmap.Save($stream,[Drawing.Imaging.ImageFormat]::Png)
    $frames += @{Size=$size;Bytes=$stream.ToArray()}
    $stream.Dispose(); $cyan.Dispose(); $violet.Dispose(); $pen.Dispose(); $gradient.Dispose(); $graphics.Dispose(); $bitmap.Dispose()
}
$file = [IO.File]::Create((Join-Path $assetFolder 'tidalbridge.ico'))
$writer = New-Object IO.BinaryWriter($file)
try {
    $writer.Write([uint16]0); $writer.Write([uint16]1); $writer.Write([uint16]$frames.Count)
    $offset = 6 + 16 * $frames.Count
    foreach ($frame in $frames) {
        $dimension = if ($frame.Size -eq 256) {0} else {$frame.Size}
        $writer.Write([byte]$dimension); $writer.Write([byte]$dimension); $writer.Write([byte]0); $writer.Write([byte]0)
        $writer.Write([uint16]1); $writer.Write([uint16]32); $writer.Write([uint32]$frame.Bytes.Length); $writer.Write([uint32]$offset)
        $offset += $frame.Bytes.Length
    }
    foreach ($frame in $frames) { $writer.Write([byte[]]$frame.Bytes) }
} finally { $writer.Dispose(); $file.Dispose() }
