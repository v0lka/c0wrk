<#
.SYNOPSIS
    Windows PowerShell analog of the repository Makefile.

.DESCRIPTION
    Reproduces every Makefile target for a Windows development host. The two
    platform-specific download targets delegate to the existing PowerShell
    scripts (scripts/fetch-onnx.ps1, scripts/fetch-embedding-model.ps1), exactly
    as the Windows branch of the Makefile does.

    Usage:
        ./build.ps1 <target> [options]

    Targets (run `./build.ps1 help` to list them):
        build                  frontend-deps + `wails build` + fetch-onnx + fetch-embedding-model
        build-gpu              Linux x64 only -> fails closed on Windows
        frontend-deps          `npm install` in frontend/
        test                   `go test ./...` + frontend `npm test`
        bench-startup          startup benchmark (BenchmarkStartupCriticalPath)
        lint                   fmt-check + `golangci-lint run` + frontend `npm run lint`
        fmt-check              fail if `gofmt -l` reports any unformatted Go file
        vulncheck              govulncheck gate (same command as the CI `make vulncheck`)
        dev-desktop            `wails dev` hot-reload loop
        dev-frontend           Vite dev server only (`npm run dev`)
        fetch-onnx             download/install ONNX Runtime (CPU) next to the binary
        fetch-onnx-gpu         Linux x64 only -> fails closed on Windows
        fetch-embedding-model  download/install the jina embedding model + tokenizer
        clean-onnx             remove the fetched ONNX Runtime artifacts
        clean                  remove build/bin, .cache and frontend/dist
        bump                   bump the sp4rk dependency to its remote HEAD
        ps-check               parse-check the bundled PowerShell scripts (CI parity)
        help                   show this help

.PARAMETER Target
    Target to run (default: help).

.PARAMETER Version
    Override the version injected through -ldflags. Falls back to $env:VERSION,
    then `git describe --tags --always --dirty`, then "dev".

.PARAMETER GitCommit
    Override the commit hash injected through -ldflags. Falls back to
    $env:GITCOMMIT, then `git rev-parse --short HEAD`, then "none".

.PARAMETER BuildDate
    Override the build timestamp injected through -ldflags. Falls back to
    $env:BUILDDATE, then the current UTC time (yyyy-MM-ddTHH:mm:ssZ).

.EXAMPLE
    ./build.ps1 build

.EXAMPLE
    ./build.ps1 -Target test

.EXAMPLE
    ./build.ps1 build -Version v1.2.3 -GitCommit abc1234
#>
[CmdletBinding()]
param(
    [Parameter(Position = 0)]
    [string]$Target = "help",

    [string]$Version,
    [string]$GitCommit,
    [string]$BuildDate
)

$ErrorActionPreference = "Stop"

# --- Constants (mirror the Makefile; keep both in lockstep) ----------------------
# govulncheck version pinned for reproducible vulnerability scans.
$GOVULNCHECK_VERSION = "v1.7.0"

# ONNX Runtime version.
$ONNX_VERSION = "1.28.1"

# Windows ONNX digests (Makefile ONNX_SHA256 / ONNX_LIB_SHA256). Recompute both
# whenever $ONNX_VERSION is bumped.
$ONNX_SHA256     = "e46ac7652def5da0e5223372be21185ffff553e0419459f66e0114d460c38162"
$ONNX_LIB_SHA256 = "ab48e807eb96ad3d399c72e5f67dd93fe9c8b452e051fbf27f72d546e1882f4a"

# Embedding model configuration (Makefile EMBEDDING_*).
$EMBEDDING_MODEL_URL        = "https://huggingface.co/jinaai/jina-embeddings-v2-small-en/resolve/main/model.onnx"
$EMBEDDING_TOKENIZER_URL    = "https://huggingface.co/jinaai/jina-embeddings-v2-small-en/resolve/main/tokenizer.json"
$EMBEDDING_MODEL_NAME       = "jina-v2-small.onnx"
$EMBEDDING_TOKENIZER_NAME   = "jina-v2-small-tokenizer.json"
$EMBEDDING_MODEL_SHA256     = "974fdefe71fc9889258f569132b35acae6278874c8d09dbdf7806d23ad0b4497"
$EMBEDDING_TOKENIZER_SHA256 = "e9f999ac74497843ed9f4303246a8f43d9f100ee8aab8e133667903f447ceb48"

# Platform output directories (Windows values of the Makefile APP_BUNDLE_DIR /
# APP_MODELS_DIR). Paths are relative to the repository root.
$APP_BUNDLE_DIR   = "build/bin"
$APP_MODELS_DIR   = "build/bin/models"
$ONNX_CACHE_DIR   = ".cache"
$MODELS_CACHE_DIR = ".cache/models"

# sp4rk dependency (Makefile SP4RK_MODULE / SP4RK_REMOTE). Override the remote
# for a fork via $env:SP4RK_REMOTE.
$SP4RK_MODULE = "github.com/v0lka/sp4rk"
$SP4RK_REMOTE = "https://github.com/v0lka/sp4rk"

# The repository root is the directory that contains this script.
$script:RepoRoot = $PSScriptRoot
if (-not $script:RepoRoot) {
    $script:RepoRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
}

# --- Helpers --------------------------------------------------------------------

function Write-Step([string]$Message) {
    Write-Host "==> $Message" -ForegroundColor Cyan
}

# Runs a native command, aborting the script when it exits non-zero. Optional
# WorkingDirectory is relative to the repository root.
function Invoke-Checked {
    param(
        [Parameter(Mandatory)][string]$Command,
        [string[]]$Arguments = @(),
        [string]$WorkingDirectory
    )

    # Node installs `npm`/`npx` as PowerShell shims (npm.ps1); a machine with the
    # default Restricted ExecutionPolicy blocks them ("running scripts is
    # disabled on this system"). Prefer the .cmd shim so the target works under
    # any script-execution policy.
    if ($Command -eq "npm") { $Command = "npm.cmd" }

    $pushed = $false
    if ($WorkingDirectory) {
        Push-Location -LiteralPath $WorkingDirectory
        $pushed = $true
    }
    # Native tools (npm, go, golangci-lint, wails) legitimately write warnings
    # and progress to stderr. With $ErrorActionPreference = 'Stop' PowerShell
    # turns each such line into a terminating error and aborts the script
    # mid-command (e.g. npm's "allow-scripts" warning), even though the tool
    # succeeded. Relax the preference for the duration of the call and judge
    # success by the exit code instead.
    $prevEap = $ErrorActionPreference
    $code = 1
    try {
        $ErrorActionPreference = 'Continue'
        & $Command @Arguments
        $code = $LASTEXITCODE
    }
    finally {
        $ErrorActionPreference = $prevEap
        if ($pushed) { Pop-Location }
    }
    if ($code -ne 0) {
        throw "command failed with exit code ${code}: $Command $($Arguments -join ' ')"
    }
}

# Runs a git command and returns its stdout lines, or $null when git is missing
# or the command fails (mirrors the Makefile's `... 2>/dev/null || echo <fallback>`).
function Invoke-Git {
    param([Parameter(Mandatory)][string[]]$Arguments)
    try {
        $out = & git @Arguments 2>$null
        if ($LASTEXITCODE -ne 0) { return $null }
        return $out
    }
    catch {
        return $null
    }
}

function Resolve-Version {
    if ($Version) { return $Version }
    if ($env:VERSION) { return $env:VERSION }
    $out = Invoke-Git -Arguments @("describe", "--tags", "--always", "--dirty")
    if ($out) { return ($out | Select-Object -First 1).Trim() }
    return "dev"
}

function Resolve-GitCommit {
    if ($GitCommit) { return $GitCommit }
    if ($env:GITCOMMIT) { return $env:GITCOMMIT }
    $out = Invoke-Git -Arguments @("rev-parse", "--short", "HEAD")
    if ($out) { return ($out | Select-Object -First 1).Trim() }
    return "none"
}

function Resolve-BuildDate {
    if ($BuildDate) { return $BuildDate }
    if ($env:BUILDDATE) { return $env:BUILDDATE }
    return [DateTime]::UtcNow.ToString("yyyy-MM-ddTHH:mm:ss", [System.Globalization.CultureInfo]::InvariantCulture) + "Z"
}

function Get-VersionLdFlags {
    $v = Resolve-Version
    $c = Resolve-GitCommit
    $b = Resolve-BuildDate
    return @(
        "-X github.com/v0lka/c0wrk/core/version.Version=$v"
        "-X github.com/v0lka/c0wrk/core/version.GitCommit=$c"
        "-X github.com/v0lka/c0wrk/core/version.BuildDate=$b"
    ) -join " "
}

# Builds the argument list for `wails build` / `wails dev`, honouring the
# mid-cycle cross-repo go.work flag (-m skips `go mod tidy`, ADR-031). On
# Windows no extra build tags are needed (webkit2_41 is Linux-only).
function Get-WailsBaseArgs([string]$Verb) {
    # Do NOT name the local accumulator $args: that is PowerShell's automatic
    # variable, and a function returning a single-element array is unwrapped to
    # a scalar. A scalar plus `+= @("-ldflags", ...)` then performs STRING
    # concatenation, so `wails` would receive "build -ldflags ..." as one
    # argument and print its top-level help instead of building. `return
    # ,$wargs` forces the array out without unwrapping.
    $wargs = @($Verb)
    if (Test-Path -LiteralPath (Join-Path $script:RepoRoot "go.work")) {
        Write-Host "go.work detected - passing -m (skip 'go mod tidy', ADR-031)" -ForegroundColor Yellow
        $wargs += "-m"
    }
    return ,$wargs
}

# --- Targets --------------------------------------------------------------------

function Target-FrontendDeps {
    Write-Step "Installing frontend dependencies (npm install)"
    Invoke-Checked -Command "npm" -Arguments @("install") -WorkingDirectory "frontend"
}

function Target-FetchOnnx {
    Write-Step "Fetching ONNX Runtime $ONNX_VERSION (Windows CPU)"
    & (Join-Path $script:RepoRoot "scripts/fetch-onnx.ps1") `
        -Version $ONNX_VERSION `
        -OutputDir $APP_BUNDLE_DIR `
        -CacheDir $ONNX_CACHE_DIR `
        -ArchiveSha256 $ONNX_SHA256 `
        -LibSha256 $ONNX_LIB_SHA256
}

function Target-FetchEmbeddingModel {
    Write-Step "Fetching the jina embedding model + tokenizer"
    & (Join-Path $script:RepoRoot "scripts/fetch-embedding-model.ps1") `
        -OutputDir $APP_MODELS_DIR `
        -CacheDir $MODELS_CACHE_DIR `
        -ModelUrl $EMBEDDING_MODEL_URL `
        -TokenizerUrl $EMBEDDING_TOKENIZER_URL `
        -ModelName $EMBEDDING_MODEL_NAME `
        -TokenizerName $EMBEDDING_TOKENIZER_NAME `
        -ModelSha256 $EMBEDDING_MODEL_SHA256 `
        -TokenizerSha256 $EMBEDDING_TOKENIZER_SHA256
}

function Target-Build {
    Target-FrontendDeps
    $ldflags = Get-VersionLdFlags
    Write-Step "wails build"
    $wailsArgs = @(Get-WailsBaseArgs "build")
    $wailsArgs += @("-ldflags", $ldflags)
    Invoke-Checked -Command "wails" -Arguments $wailsArgs
    Target-FetchOnnx
    Target-FetchEmbeddingModel
}

function Target-BuildGpu {
    throw @"
build-gpu is Linux x64 only: the pinned GPU artifact has no Windows build and
no digest can be verified. Run 'build' (CPU flavor) instead.
"@
}

function Target-Test {
    Write-Step "go test ./..."
    Invoke-Checked -Command "go" -Arguments @("test", "./...")
    Write-Step "frontend: npm test"
    Invoke-Checked -Command "npm" -Arguments @("test") -WorkingDirectory "frontend"
}

function Target-BenchStartup {
    Write-Step "startup benchmark (BenchmarkStartupCriticalPath)"
    Invoke-Checked -Command "go" -Arguments @(
        "test", "./desktop",
        "-run", "^$",
        "-bench", "^BenchmarkStartupCriticalPath$",
        "-benchmem"
    )
}

function Target-FmtCheck {
    Write-Step "gofmt -l check"
    # gofmt (a native tool) may emit to stderr; guard it the same way as
    # Invoke-Checked so a warning cannot abort the run under 'Stop'.
    $prevEap = $ErrorActionPreference
    $out = $null
    try {
        $ErrorActionPreference = 'Continue'
        $out = & gofmt -l main.go internal core backend desktop 2>$null
    }
    finally { $ErrorActionPreference = $prevEap }
    if ($out) {
        Write-Host "gofmt violations (run gofmt -w on these files):" -ForegroundColor Red
        $out | ForEach-Object { Write-Host "  $_" }
        throw "gofmt check failed"
    }
    Write-Host "gofmt check passed"
}

function Target-Lint {
    Target-FmtCheck
    Write-Step "golangci-lint run"
    Invoke-Checked -Command "golangci-lint" -Arguments @("run")
    Write-Step "frontend: npm run lint"
    Invoke-Checked -Command "npm" -Arguments @("run", "lint") -WorkingDirectory "frontend"
}

function Target-Vulncheck {
    Write-Step "govulncheck $GOVULNCHECK_VERSION ./..."
    Invoke-Checked -Command "go" -Arguments @(
        "run", "golang.org/x/vuln/cmd/govulncheck@$GOVULNCHECK_VERSION", "./..."
    )
}

function Target-DevDesktop {
    Write-Step "wails dev"
    $wailsArgs = @(Get-WailsBaseArgs "dev")
    Invoke-Checked -Command "wails" -Arguments $wailsArgs
}

function Target-DevFrontend {
    Write-Step "frontend: npm run dev"
    Invoke-Checked -Command "npm" -Arguments @("run", "dev") -WorkingDirectory "frontend"
}

function Target-FetchOnnxGpu {
    throw @"
fetch-onnx-gpu is Linux x64 only: the pinned artifact has no Windows build and
no digest can be verified. Run 'fetch-onnx' (CPU flavor) instead.
"@
}

function Target-CleanOnnx {
    Write-Step "Removing ONNX Runtime artifacts"
    $files = @(
        "$APP_BUNDLE_DIR/libonnxruntime.dylib",
        "$APP_BUNDLE_DIR/libonnxruntime.so",
        "$APP_BUNDLE_DIR/onnxruntime.dll",
        "$APP_BUNDLE_DIR/libonnxruntime_providers_cuda.so",
        "$APP_BUNDLE_DIR/libonnxruntime_providers_shared.so",
        "$APP_BUNDLE_DIR/.onnxruntime-gpu-version",
        "$ONNX_CACHE_DIR/libonnxruntime.dylib",
        "$ONNX_CACHE_DIR/libonnxruntime.so",
        "$ONNX_CACHE_DIR/onnxruntime.dll"
    )
    foreach ($f in $files) {
        if (Test-Path -LiteralPath $f) { Remove-Item -Force -LiteralPath $f }
    }
    $gpuCache = Join-Path $ONNX_CACHE_DIR "onnx-gpu"
    if (Test-Path -LiteralPath $gpuCache) { Remove-Item -Recurse -Force -LiteralPath $gpuCache }
    Write-Host "ONNX Runtime library removed from $APP_BUNDLE_DIR/ and $ONNX_CACHE_DIR/"
}

function Target-Clean {
    Write-Step "Removing build artifacts (build/bin, .cache, frontend/dist)"
    foreach ($d in @("build/bin", ".cache", "frontend/dist")) {
        if (Test-Path -LiteralPath $d) { Remove-Item -Recurse -Force -LiteralPath $d }
    }
    Write-Host "All build artifacts removed"
}

function Target-Bump {
    $remote = if ($env:SP4RK_REMOTE) { $env:SP4RK_REMOTE } else { $SP4RK_REMOTE }
    Write-Step "Resolving HEAD from $remote"
    $out = Invoke-Git -Arguments @("ls-remote", $remote, "HEAD")
    $commit = $null
    if ($out) {
        $commit = ($out | Select-Object -First 1).Trim().Split()[0]
    }
    if (-not $commit) {
        throw @"
bump: could not resolve HEAD from $remote
      check network access or override: `$env:SP4RK_REMOTE = '<url>'
"@
    }
    Write-Host "Bumping $SP4RK_MODULE to $commit ($remote HEAD) ..."
    $env:GOWORK = "off"
    Invoke-Checked -Command "go" -Arguments @("get", "$SP4RK_MODULE@$commit")
    Invoke-Checked -Command "go" -Arguments @("mod", "tidy")
}

# Parse-check the bundled PowerShell scripts (mirrors the CI step that would
# otherwise let a syntax error ship until a Windows build).
function Target-PsCheck {
    Write-Step "Parse-checking PowerShell scripts"
    $scripts = @(
        (Join-Path $script:RepoRoot "build.ps1"),
        (Join-Path $script:RepoRoot "scripts/fetch-onnx.ps1"),
        (Join-Path $script:RepoRoot "scripts/fetch-embedding-model.ps1")
    )
    $failed = $false
    foreach ($s in $scripts) {
        $tokens = $null
        $parseErrors = $null
        [System.Management.Automation.Language.Parser]::ParseFile($s, [ref]$tokens, [ref]$parseErrors) | Out-Null
        if ($parseErrors -and $parseErrors.Count -gt 0) {
            $failed = $true
            $parseErrors | ForEach-Object {
                Write-Host "  [$(Split-Path -Leaf $s):$($_.Extent.StartLineNumber)] $($_.Message)" -ForegroundColor Red
            }
        }
        else {
            Write-Host "  OK: $s"
        }
    }
    if ($failed) { throw "PowerShell parse errors found" }
}

function Show-Usage {
    Write-Host @"
Windows PowerShell analog of the c0wrk Makefile.

Usage: ./build.ps1 <target> [options]

Targets:
  build                  frontend-deps + wails build + fetch-onnx + fetch-embedding-model
  build-gpu              Linux x64 only (fails closed on Windows)
  frontend-deps          npm install (frontend/)
  test                   go test ./... + frontend npm test
  bench-startup          startup benchmark (BenchmarkStartupCriticalPath)
  lint                   fmt-check + golangci-lint run + frontend npm run lint
  fmt-check              gofmt -l check
  vulncheck              govulncheck ./...
  dev-desktop            wails dev (hot-reload)
  dev-frontend           Vite dev server only
  fetch-onnx             install ONNX Runtime (CPU) next to the binary
  fetch-onnx-gpu         Linux x64 only (fails closed on Windows)
  fetch-embedding-model  install the jina embedding model + tokenizer
  clean-onnx             remove ONNX Runtime artifacts
  clean                  remove build/bin, .cache, frontend/dist
  bump                   bump the sp4rk dependency to its remote HEAD
  ps-check               parse-check the bundled PowerShell scripts
  help                   show this help

Options:
  -Version <v>     override the ldflags version (else `$env:VERSION` / git describe / dev)
  -GitCommit <sha> override the ldflags commit  (else `$env:GITCOMMIT` / git rev-parse / none)
  -BuildDate <ts>  override the ldflags date    (else `$env:BUILDDATE` / current UTC)

Examples:
  ./build.ps1 build
  ./build.ps1 test
  ./build.ps1 build -Version v1.2.3
"@
}

# --- Dispatch -------------------------------------------------------------------

$resolved = $Target.ToLowerInvariant()

try {
    Push-Location -LiteralPath $script:RepoRoot
    switch ($resolved) {
        "help"                 { Show-Usage }
        "build"                { Target-Build }
        "build-gpu"            { Target-BuildGpu }
        "frontend-deps"        { Target-FrontendDeps }
        "test"                 { Target-Test }
        "bench-startup"        { Target-BenchStartup }
        "lint"                 { Target-Lint }
        "fmt-check"            { Target-FmtCheck }
        "vulncheck"            { Target-Vulncheck }
        "dev-desktop"          { Target-DevDesktop }
        "dev-frontend"         { Target-DevFrontend }
        "fetch-onnx"           { Target-FetchOnnx }
        "fetch-onnx-gpu"       { Target-FetchOnnxGpu }
        "fetch-embedding-model" { Target-FetchEmbeddingModel }
        "clean-onnx"           { Target-CleanOnnx }
        "clean"                { Target-Clean }
        "bump"                 { Target-Bump }
        "ps-check"             { Target-PsCheck }
        default {
            Write-Host "Unknown target: $Target" -ForegroundColor Red
            Write-Host ""
            Show-Usage
            throw "Unknown target: $Target"
        }
    }
}
finally {
    Pop-Location
}
