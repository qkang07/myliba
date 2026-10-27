param(
    [string]$FFmpegBinDir = '',
    [switch]$SkipWebBuild,
    [switch]$CreateZip
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$repoRoot = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..')).Path

if ($FFmpegBinDir) {
    $binDir = (Resolve-Path -LiteralPath $FFmpegBinDir).Path
} else {
    $binDir = $null
    $essentialsRoot = Join-Path $repoRoot '.runtime/ffmpeg-essentials'
    if (Test-Path -LiteralPath $essentialsRoot -PathType Container) {
        $essentials = Get-ChildItem -LiteralPath $essentialsRoot -Directory -Filter '*-essentials_build' |
            Sort-Object LastWriteTime -Descending | Select-Object -First 1
        if ($essentials) { $binDir = Join-Path $essentials.FullName 'bin' }
    }
    if (-not $binDir) {
        $command = Get-Command ffmpeg.exe -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
        if ($command) { $binDir = Split-Path -Parent $command.Source }
    }
    if (-not $binDir) {
        $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
        foreach ($entry in ($userPath -split ';')) {
            if ($entry -and (Test-Path -LiteralPath (Join-Path $entry 'ffmpeg.exe'))) {
                $binDir = $entry
                break
            }
        }
    }
}
if (-not $binDir) {
    throw 'FFmpeg was not found. Pass -FFmpegBinDir with the folder containing ffmpeg.exe and ffprobe.exe.'
}
foreach ($binary in @('ffmpeg.exe', 'ffprobe.exe')) {
    if (-not (Test-Path -LiteralPath (Join-Path $binDir $binary) -PathType Leaf)) {
        throw "Missing $binary in $binDir"
    }
}
$ffmpegRoot = Split-Path -Parent $binDir
foreach ($document in @('LICENSE', 'README.txt')) {
    if (-not (Test-Path -LiteralPath (Join-Path $ffmpegRoot $document) -PathType Leaf)) {
        throw "Missing FFmpeg $document in $ffmpegRoot"
    }
}

$releaseDir = Join-Path $repoRoot ('.runtime/releases/myliba-windows-' + (Get-Date -Format 'yyyyMMdd-HHmmss-fff'))
$bundledDir = Join-Path $releaseDir 'ffmpeg'
New-Item -ItemType Directory -Path $bundledDir -Force | Out-Null

Push-Location $repoRoot
try {
    if (-not $SkipWebBuild) {
        if (-not (Test-Path -LiteralPath 'node_modules/vite/bin/vite.js' -PathType Leaf)) {
            throw 'Web dependencies are missing. Run pnpm install --frozen-lockfile before packaging.'
        }
        $nativeConfig = Join-Path $repoRoot 'vite.package.config.mjs'
        Copy-Item -LiteralPath (Join-Path $repoRoot 'vite.config.ts') -Destination $nativeConfig
        try {
            & node node_modules/vite/bin/vite.js build --config $nativeConfig --configLoader native
            if ($LASTEXITCODE -ne 0) { throw 'Web build failed' }
        } finally {
            Remove-Item -LiteralPath $nativeConfig -ErrorAction SilentlyContinue
        }
    }
    if (-not (Test-Path -LiteralPath 'cmd/myliba/web/dist/index.html' -PathType Leaf)) {
        throw 'Embedded web build is missing'
    }
    $env:GOCACHE = Join-Path $repoRoot '.gocache'
    & go build -tags sqlite_fts5 -buildvcs=false -ldflags '-H=windowsgui' -o (Join-Path $releaseDir 'myliba.exe') ./cmd/myliba
    if ($LASTEXITCODE -ne 0) { throw 'Go build failed' }
} finally {
    Pop-Location
}

foreach ($binary in @('ffmpeg.exe', 'ffprobe.exe')) {
    Copy-Item -LiteralPath (Join-Path $binDir $binary) -Destination (Join-Path $bundledDir $binary)
}
foreach ($document in @('LICENSE', 'README.txt')) {
    Copy-Item -LiteralPath (Join-Path $ffmpegRoot $document) -Destination (Join-Path $bundledDir $document)
}
@"
Myliba portable Windows build

Run myliba.exe from this folder. FFmpeg and FFprobe are bundled in ffmpeg/ and
are discovered automatically; no separate installation or PATH setup is needed.
Open http://127.0.0.1:4317 and use the 服务端 button to add or switch VPS Hubs.

FFmpeg is a separate third-party program. Its build information, license, and
source reference are in ffmpeg/README.txt and ffmpeg/LICENSE. Review
https://ffmpeg.org/legal.html before redistributing this bundle.
"@ | Set-Content -LiteralPath (Join-Path $releaseDir 'README.txt') -Encoding utf8

if ($CreateZip) {
    $archive = "$releaseDir.zip"
    Compress-Archive -Path (Join-Path $releaseDir '*') -DestinationPath $archive -CompressionLevel Optimal
    Write-Output $archive
}
Write-Output $releaseDir
