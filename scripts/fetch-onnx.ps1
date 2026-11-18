<#
.SYNOPSIS
    Downloads and installs the ONNX Runtime shared library (onnxruntime.dll) for Windows.

.DESCRIPTION
    Downloads the onnxruntime-win-x64 release archive (a .zip) from the npmmirror
    binary mirror (https://registry.npmmirror.com/-/binary/onnxruntime — the npm
    ecosystem's binary host mirroring the official release archives), extracts
    onnxruntime.dll (located at <archive-top>/lib/onnxruntime.dll inside the
    archive), caches it under .cache/, and installs it next to the executable in
    build/bin/ so that resolveONNXLibPath() finds it at runtime (the library is
    looked up next to os.Executable()).

    Every artifact is SHA256-verified before use (Get-FileHash), fail-closed: a
    checksum mismatch removes the bad file and aborts the script with a non-zero
    exit. This covers the downloaded archive, the DLL extracted from it, and the
    cached copy. No checksum files are attached to the release archives (true on
    the removed GitHub releases and on the mirror alike), so the digests are
    pinned trust-on-first-use — originally from the microsoft/onnxruntime GitHub
    releases (that repository, and every release asset URL on it, was removed
    from GitHub on 2026-11-17) and re-verified byte-identical against a fresh
    download from the mirror. When invoked from the Makefile, the digests are
    passed in from ONNX_SHA256 / ONNX_LIB_SHA256 (the Makefile is the single
    source of truth); recompute them whenever ONNX_VERSION is bumped.

    The installed DLL is additionally guarded by a version stamp file
    (.onnxruntime-version next to the DLL, mirroring the Makefile's ONNX_STAMP):
    an ONNX_VERSION bump replaces a stale installed library instead of skipping
    on the "already installed" short-circuit. A stale CACHE entry from another
    version is likewise replaced (NOTE + re-download) instead of failing the
    first fetch after a bump, mirroring the Makefile recipe.

    Uses Expand-Archive and the OS TEMP directory — no Unix-only tooling
    is required on the Windows execution path (the release archive is already a
    .zip, so Expand-Archive consumes it directly).

.PARAMETER Version
    ONNX Runtime version to fetch. Default: 1.28.1. When invoked from the
    Makefile (Windows fetch-onnx target), this is passed explicitly from
    ONNX_VERSION so the Makefile remains the single source of truth.

.PARAMETER OutputDir
    Directory where onnxruntime.dll is installed. Default: build/bin. When
    invoked from the Makefile, this is passed from APP_BUNDLE_DIR.

.PARAMETER CacheDir
    Cache directory for the downloaded library. Default: .cache. When
    invoked from the Makefile, this is passed from ONNX_CACHE_DIR.

.PARAMETER ArchiveSha256
    Expected SHA256 of the downloaded onnxruntime-win-x64 <version> .zip.
    When invoked from the Makefile, passed from ONNX_SHA256.

.PARAMETER LibSha256
    Expected SHA256 of onnxruntime.dll as extracted from the archive (and of
    the cached copy). When invoked from the Makefile, passed from
    ONNX_LIB_SHA256.

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File scripts/fetch-onnx.ps1
#>
[CmdletBinding()]
param(
    [string]$Version      = "1.28.1",
    [string]$OutputDir    = "build/bin",
    [string]$CacheDir     = ".cache",
    [string]$ArchiveSha256 = "e46ac7652def5da0e5223372be21185ffff553e0419459f66e0114d460c38162",
    [string]$LibSha256    = "ab48e807eb96ad3d399c72e5f67dd93fe9c8b452e051fbf27f72d546e1882f4a"
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

# Fail-closed checksum gate: compares the SHA256 of $Path against $Expected.
# An unset/empty $Expected is an error too — nothing unverified is installed.
# On mismatch the bad file is removed and the script aborts (non-zero exit).
function Assert-FileSha256([string]$Path, [string]$Expected, [string]$Label) {
    if ([string]::IsNullOrWhiteSpace($Expected)) {
        throw "No SHA256 digest pinned for $Label ($Path) - refusing unverified install."
    }
    $Actual = (Get-FileHash -Path $Path -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($Actual -ne $Expected.ToLowerInvariant()) {
        Remove-Item -Force -Path $Path -ErrorAction SilentlyContinue
        throw "SHA256 mismatch for $Label ($Path): expected $($Expected.ToLowerInvariant()), got $Actual - bad file removed."
    }
    Write-Host "SHA256 verified: $Path"
}

$OutputDirR = Resolve-RepoPath $OutputDir
$CacheDirR  = Resolve-RepoPath $CacheDir

$ArchiveName   = "onnxruntime-win-x64-$Version.zip"
$ArchiveTopDir = "onnxruntime-win-x64-$Version"
$ArchiveUrl    = "https://registry.npmmirror.com/-/binary/onnxruntime/v$Version/$ArchiveName"
$LibOut        = "onnxruntime.dll"

$OutputLibPath = Join-Path $OutputDirR $LibOut
$CacheLibPath  = Join-Path $CacheDirR $LibOut
# Version stamp written next to the installed library (mirrors the Makefile's
# ONNX_STAMP): guards against a stale DLL surviving an ONNX_VERSION bump —
# without it the "already installed" short-circuit below would keep the old
# library in place forever.
$VersionStamp = Join-Path $OutputDirR ".onnxruntime-version"

# 1. Already installed in the output directory, for THIS version?
if ((Test-Path $OutputLibPath) -and (Test-Path $VersionStamp) -and ((Get-Content -Path $VersionStamp -Raw).Trim() -eq $Version)) {
    Write-Host "ONNX Runtime $Version already installed at $OutputLibPath"
    return
}

New-Item -ItemType Directory -Force -Path $OutputDirR | Out-Null

# 2. Available in the local cache? Verified against the pinned digest first.
# An empty pinned digest can never match here, so a cache hit with no pin is
# impossible — the flow falls through to the download branch, where the empty
# digest fails the archive verification (fail-closed either way).
if (Test-Path $CacheLibPath) {
    $CacheDigest = (Get-FileHash -Path $CacheLibPath -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($CacheDigest -eq $LibSha256.ToLowerInvariant()) {
        Write-Host "Using cached ONNX Runtime library..."
        Copy-Item -Path $CacheLibPath -Destination $OutputLibPath -Force
        Set-Content -Path $VersionStamp -Value $Version -NoNewline
        Write-Host "ONNX Runtime library installed to $OutputLibPath"
        return
    }
    Write-Host "NOTE: cached library digest does not match version $Version - removing the stale cache entry and downloading instead"
    Remove-Item -Force -Path $CacheLibPath
}

# 3. Download, verify, extract, verify, and install.
New-Item -ItemType Directory -Force -Path $CacheDirR | Out-Null

# Unique temp staging directory under the OS temp path. $env:TEMP is the
# Windows convention; on other hosts (e.g. running this script under pwsh for
# testing) fall back to the .NET per-OS temp resolution.
$BaseTemp = if ($env:TEMP) { $env:TEMP } else { [System.IO.Path]::GetTempPath() }
$TempDir = Join-Path $BaseTemp "c0wrk-fetch-onnx-$([System.Guid]::NewGuid())"
New-Item -ItemType Directory -Force -Path $TempDir | Out-Null
try {
    $ArchivePath = Join-Path $TempDir $ArchiveName
    Write-Host "Downloading ONNX Runtime $Version (npmmirror binary mirror)..."
    Invoke-WebRequest -Uri $ArchiveUrl -OutFile $ArchivePath -UseBasicParsing
    Assert-FileSha256 -Path $ArchivePath -Expected $ArchiveSha256 -Label "downloaded ONNX Runtime archive"

    $ExtractDir = Join-Path $TempDir "extract"
    Write-Host "Extracting ONNX Runtime library..."
    Expand-Archive -Path $ArchivePath -DestinationPath $ExtractDir -Force

    $ExtractedLib = Join-Path $ExtractDir (Join-Path $ArchiveTopDir "lib/$LibOut")
    if (-not (Test-Path $ExtractedLib)) {
        throw "Expected library not found inside archive: $ExtractedLib"
    }
    Assert-FileSha256 -Path $ExtractedLib -Expected $LibSha256 -Label "extracted ONNX Runtime library"

    # Populate the cache, then install next to the executable.
    Copy-Item -Path $ExtractedLib -Destination $CacheLibPath  -Force
    Copy-Item -Path $ExtractedLib -Destination $OutputLibPath -Force
    Set-Content -Path $VersionStamp -Value $Version -NoNewline
    Write-Host "ONNX Runtime library installed to $OutputLibPath"
}
finally {
    Remove-Item -Recurse -Force -Path $TempDir -ErrorAction SilentlyContinue
}
