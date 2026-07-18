<#
.SYNOPSIS
    Downloads and installs the ONNX Runtime shared library (onnxruntime.dll) for Windows.

.DESCRIPTION
    Downloads onnxruntime-win-x64-<version>.zip from the ONNX Runtime GitHub release,
    extracts onnxruntime.dll (located at <archive-top>/lib/onnxruntime.dll inside the
    archive), caches it under .cache/, and installs it next to the executable in
    build/bin/ so that resolveONNXLibPath() finds it at runtime (the library is looked
    up next to os.Executable()).

    Uses Expand-Archive and the OS TEMP directory — no Unix-only tooling
    is required on the Windows execution path.

.PARAMETER Version
    ONNX Runtime version to fetch. Default: 1.24.1. When invoked from the
    Makefile (Windows fetch-onnx target), this is passed explicitly from
    ONNX_VERSION so the Makefile remains the single source of truth.

.PARAMETER OutputDir
    Directory where onnxruntime.dll is installed. Default: build/bin. When
    invoked from the Makefile, this is passed from APP_BUNDLE_DIR.

.PARAMETER CacheDir
    Cache directory for the downloaded library. Default: .cache. When invoked
    from the Makefile, this is passed from ONNX_CACHE_DIR.

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File scripts/fetch-onnx.ps1
#>
[CmdletBinding()]
param(
    [string]$Version   = "1.24.1",
    [string]$OutputDir = "build/bin",
    [string]$CacheDir  = ".cache"
)

$ErrorActionPreference = "Stop"

# Resolve relative paths against the repository root (parent of this scripts dir),
# so the script works regardless of the caller's current directory.
$ScriptRoot = $PSScriptRoot
if (-not $ScriptRoot) { $ScriptRoot = Split-Path -Parent $MyInvocation.MyCommand.Path }
$RepoRoot = Split-Path -Parent $ScriptRoot

function Resolve-RepoPath([string]$Path) {
    if ([System.IO.Path]::IsPathRooted($Path)) { return $Path }
    return (Join-Path $RepoRoot $Path)
}

$OutputDirR = Resolve-RepoPath $OutputDir
$CacheDirR  = Resolve-RepoPath $CacheDir

$ArchiveName   = "onnxruntime-win-x64-$Version.zip"
$ArchiveTopDir = "onnxruntime-win-x64-$Version"
$LibOut        = "onnxruntime.dll"
$Url           = "https://github.com/microsoft/onnxruntime/releases/download/v$Version/$ArchiveName"

$OutputLibPath = Join-Path $OutputDirR $LibOut
$CacheLibPath  = Join-Path $CacheDirR $LibOut

# 1. Already installed in the output directory?
if (Test-Path $OutputLibPath) {
    Write-Host "ONNX Runtime library already exists at $OutputLibPath"
    return
}

New-Item -ItemType Directory -Force -Path $OutputDirR | Out-Null

# 2. Available in the local cache?
if (Test-Path $CacheLibPath) {
    Write-Host "Using cached ONNX Runtime library..."
    Copy-Item -Path $CacheLibPath -Destination $OutputLibPath -Force
    Write-Host "ONNX Runtime library installed to $OutputLibPath"
    return
}

# 3. Download, extract, and install.
New-Item -ItemType Directory -Force -Path $CacheDirR | Out-Null

# Unique temp staging directory under the OS temp path.
$TempDir = Join-Path $env:TEMP "c0wrk-fetch-onnx-$([System.Guid]::NewGuid())"
New-Item -ItemType Directory -Force -Path $TempDir | Out-Null
try {
    $ZipPath = Join-Path $TempDir $ArchiveName
    Write-Host "Downloading ONNX Runtime $Version for Windows..."
    Invoke-WebRequest -Uri $Url -OutFile $ZipPath -UseBasicParsing

    $ExtractDir = Join-Path $TempDir "extract"
    Write-Host "Extracting ONNX Runtime library..."
    Expand-Archive -Path $ZipPath -DestinationPath $ExtractDir -Force

    $ExtractedLib = Join-Path $ExtractDir (Join-Path $ArchiveTopDir "lib/$LibOut")
    if (-not (Test-Path $ExtractedLib)) {
        throw "Expected library not found inside archive: $ExtractedLib"
    }

    # Populate the cache, then install next to the executable.
    Copy-Item -Path $ExtractedLib -Destination $CacheLibPath  -Force
    Copy-Item -Path $ExtractedLib -Destination $OutputLibPath -Force
    Write-Host "ONNX Runtime library installed to $OutputLibPath"
}
finally {
    Remove-Item -Recurse -Force -Path $TempDir -ErrorAction SilentlyContinue
}
