<#
.SYNOPSIS
    Downloads and installs the jina embedding model + tokenizer for Windows.

.DESCRIPTION
    Downloads the jina-embeddings-v2-small-en ONNX model and its tokenizer from
    Hugging Face, caches them under .cache/models/, and installs them into
    build/bin/models/ so that resolveModelPath() finds them at runtime (flat
    layout: models/ next to the binary, used by Linux/Windows).

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
    [string]$TokenizerName = "jina-v2-small-tokenizer.json"
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

$OutputModelPath    = Join-Path $OutputDirR $ModelName
$OutputTokenizerPath = Join-Path $OutputDirR $TokenizerName
$CacheModelPath     = Join-Path $CacheDirR $ModelName
$CacheTokenizerPath = Join-Path $CacheDirR $TokenizerName

# 1. Already installed in the output directory?
if ((Test-Path $OutputModelPath) -and (Test-Path $OutputTokenizerPath)) {
    Write-Host "Embedding model already exists at $OutputDirR"
    return
}

New-Item -ItemType Directory -Force -Path $OutputDirR | Out-Null

# 2. Available in the local cache?
if ((Test-Path $CacheModelPath) -and (Test-Path $CacheTokenizerPath)) {
    Write-Host "Using cached embedding model..."
    Copy-Item -Path $CacheModelPath     -Destination $OutputModelPath     -Force
    Copy-Item -Path $CacheTokenizerPath -Destination $OutputTokenizerPath -Force
    Write-Host "Embedding model installed to $OutputDirR"
    return
}

# 3. Download (into the cache), then install.
New-Item -ItemType Directory -Force -Path $CacheDirR | Out-Null

function Download-File([string]$Url, [string]$Destination, [string]$Label) {
    Write-Host "Downloading $Label..."
    Invoke-WebRequest -Uri $Url -OutFile $Destination -UseBasicParsing
}

# Fetch whichever asset is missing from the cache.
if (-not (Test-Path $CacheModelPath)) {
    Download-File -Url $ModelUrl -Destination $CacheModelPath -Label "embedding model"
}
if (-not (Test-Path $CacheTokenizerPath)) {
    Download-File -Url $TokenizerUrl -Destination $CacheTokenizerPath -Label "tokenizer"
}

Copy-Item -Path $CacheModelPath     -Destination $OutputModelPath     -Force
Copy-Item -Path $CacheTokenizerPath -Destination $OutputTokenizerPath -Force
Write-Host "Embedding model installed to $OutputDirR"
