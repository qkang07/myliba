param(
    [ValidateSet('windows','darwin','linux')][string]$TargetOS = 'windows',
    [ValidateSet('amd64','arm64')][string]$TargetArch = 'amd64'
)

$ErrorActionPreference = 'Stop'
$root = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..')).Path
$outputDir = Join-Path $root ".runtime/storage-build/$TargetOS-$TargetArch"
New-Item -ItemType Directory -Path $outputDir -Force | Out-Null
$name = if ($TargetOS -eq 'windows') { 'myliba-storage.exe' } else { 'myliba-storage' }
$spaceFile = if ($TargetOS -eq 'windows') { 'cloud_space_windows.go' } else { 'cloud_space_unix.go' }
$files = @('cloud_protocol.go','cloud_store.go','cloud_stats.go',$spaceFile,'cloud_hub.go','cloud_node.go','cloud_repair.go','cloud_backend.go','cloud_s3.go','cloud_storage_entry.go') |
    ForEach-Object { Join-Path $root "cmd/myliba/$_" }

$env:GOOS = $TargetOS
$env:GOARCH = $TargetArch
$env:CGO_ENABLED = '0'
$env:GOCACHE = Join-Path $root '.runtime/gocache'
Push-Location $root
try {
    & go build -trimpath -buildvcs=false -o (Join-Path $outputDir $name) @files
    if ($LASTEXITCODE -ne 0) { throw 'Storage build failed' }
} finally {
    Pop-Location
}
Write-Output (Join-Path $outputDir $name)
