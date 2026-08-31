<#
.SYNOPSIS
    Downloads and installs the jina embedding model + tokenizer for Windows.

.DESCRIPTION
    Downloads the jina-embeddings-v2-small-en ONNX model and its tokenizer from
    Hugging Face, caches them under .cache/models/, and installs them into
    build/bin/models/ so that resolveModelPath() finds them at runtime (flat
    layout: models/ next to the binary, used by Linux/Windows).

    Every artifact is SHA256-verified before use (Get-FileHash), fail-closed: a
    checksum mismatch removes the bad file and aborts the script with a
    non-zero exit. This covers the installed copies, the cached copies, and the
    freshly downloaded files. EMBEDDING_MODEL_SHA256 is the official Hugging
    Face LFS oid (from the repository's raw pointer file); tokenizer.json is a
    plain git blob there, so its digest is trust-on-first-use. When invoked
    from the Makefile, the digests are passed in from EMBEDDING_MODEL_SHA256 /
    EMBEDDING_TOKENIZER_SHA256 (the Makefile is the single source of truth);
    recompute them whenever the model/tokenizer URL changes.

    Uses the OS TEMP directory — no Unix-only tooling is required on the
    Windows execution path.

.PARAMETER OutputDir
    Directory where the model files are installed. Default: build/bin/models.
    When invoked from the Makefile (Windows fetch-embedding-model target),
    this is passed from APP_MODELS_DIR so the Makefile remains the single
    source of truth.

.PARAMETER CacheDir
    Cache directory for the downloaded model files. Default: .cache/models.
    When invoked from the Makefile, this is passed from MODELS_CACHE_DIR.

.PARAMETER ModelUrl
    URL of the quantized ONNX model. Default: jina-v2-small-en model.onnx.
    When invoked from the Makefile, this is passed from EMBEDDING_MODEL_URL.

.PARAMETER TokenizerUrl
    URL of the tokenizer.json. Default: jina-v2-small-en tokenizer.json.
    When invoked from the Makefile, this is passed from EMBEDDING_TOKENIZER_URL.

.PARAMETER ModelName
    Local filename for the downloaded model. Default: jina-v2-small.onnx.
    When invoked from the Makefile, this is passed from EMBEDDING_MODEL_NAME.

.PARAMETER TokenizerName
    Local filename for the downloaded tokenizer. Default:
    jina-v2-small-tokenizer.json. When invoked from the Makefile, this is
    passed from EMBEDDING_TOKENIZER_NAME.

.PARAMETER ModelSha256
    Expected SHA256 of the model.onnx file (installed, cached, and downloaded
    copies are all checked against it). When invoked from the Makefile, passed
    from EMBEDDING_MODEL_SHA256.

.PARAMETER TokenizerSha256
    Expected SHA256 of the tokenizer.json file (installed, cached, and
    downloaded copies are all checked against it). When invoked from the
    Makefile, passed from EMBEDDING_TOKENIZER_SHA256.

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File scripts/fetch-embedding-model.ps1
#>
[CmdletBinding()]
param(
    [string]$OutputDir    = "build/bin/models",
    [string]$CacheDir     = ".cache/models",
    [string]$ModelUrl     = "https://huggingface.co/jinaai/jina-embeddings-v2-small-en/resolve/main/model.onnx",
    [string]$TokenizerUrl = "https://huggingface.co/jinaai/jina-embeddings-v2-small-en/resolve/main/tokenizer.json",
    [string]$ModelName    = "jina-v2-small.onnx",
    [string]$TokenizerName = "jina-v2-small-tokenizer.json",
    [string]$ModelSha256     = "974fdefe71fc9889258f569132b35acae6278874c8d09dbdf7806d23ad0b4497",
    [string]$TokenizerSha256 = "e9f999ac74497843ed9f4303246a8f43d9f100ee8aab8e133667903f447ceb48"
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

$OutputModelPath    = Join-Path $OutputDirR $ModelName
$OutputTokenizerPath = Join-Path $OutputDirR $TokenizerName
$CacheModelPath     = Join-Path $CacheDirR $ModelName
$CacheTokenizerPath = Join-Path $CacheDirR $TokenizerName

# 1. Already installed in the output directory? Verified against the pinned
#    digests before trusting them.
if ((Test-Path $OutputModelPath) -and (Test-Path $OutputTokenizerPath)) {
    Write-Host "Embedding model already exists at $OutputDirR"
    Assert-FileSha256 -Path $OutputModelPath    -Expected $ModelSha256     -Label "installed embedding model"
    Assert-FileSha256 -Path $OutputTokenizerPath -Expected $TokenizerSha256 -Label "installed tokenizer"
    return
}

New-Item -ItemType Directory -Force -Path $OutputDirR | Out-Null

# 2. Available in the local cache? Verified against the pinned digests first.
if ((Test-Path $CacheModelPath) -and (Test-Path $CacheTokenizerPath)) {
    Write-Host "Using cached embedding model..."
    Assert-FileSha256 -Path $CacheModelPath     -Expected $ModelSha256     -Label "cached embedding model"
    Assert-FileSha256 -Path $CacheTokenizerPath -Expected $TokenizerSha256 -Label "cached tokenizer"
    Copy-Item -Path $CacheModelPath     -Destination $OutputModelPath     -Force
    Copy-Item -Path $CacheTokenizerPath -Destination $OutputTokenizerPath -Force
    Write-Host "Embedding model installed to $OutputDirR"
    return
}

# 3. Download (into the cache), verify, then install.
New-Item -ItemType Directory -Force -Path $CacheDirR | Out-Null

function Download-File([string]$Url, [string]$Destination, [string]$Label) {
    Write-Host "Downloading $Label..."
    Invoke-WebRequest -Uri $Url -OutFile $Destination -UseBasicParsing
}

# Fetch whichever asset is missing from the cache.
if (-not (Test-Path $CacheModelPath)) {
    Download-File -Url $ModelUrl -Destination $CacheModelPath -Label "embedding model"
}
Assert-FileSha256 -Path $CacheModelPath -Expected $ModelSha256 -Label "downloaded embedding model"
if (-not (Test-Path $CacheTokenizerPath)) {
    Download-File -Url $TokenizerUrl -Destination $CacheTokenizerPath -Label "tokenizer"
}
Assert-FileSha256 -Path $CacheTokenizerPath -Expected $TokenizerSha256 -Label "downloaded tokenizer"

Copy-Item -Path $CacheModelPath     -Destination $OutputModelPath     -Force
Copy-Item -Path $CacheTokenizerPath -Destination $OutputTokenizerPath -Force
Write-Host "Embedding model installed to $OutputDirR"
