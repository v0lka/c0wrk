# ADR-010: Tool Manager for External Binary Dependencies

## Status

Accepted

## Context

c0wrk needs three external tools at runtime to function correctly:

| Tool | Type | Nature |
|------|------|--------|
| `git` | C binary, complex link deps | Developers always have it |
| `ripgrep` (rg) | Rust static binary | ~5MB, downloaded from GitHub Releases |
| `markitdown` | Python 3.10+ package | ~80-100MB, requires Python runtime + pip deps |

ADR-004 established git and rg as hard PATH dependencies checked at startup with a fatal modal. This was sustainable for 2 tools. At 3 tools — one requiring a full Python runtime — the install burden is too high to push onto the user.

## Decision

1. **git** remains a system dependency but is checked lazily via `exec.LookPath` on the first CODE-mode project switch (not at startup). If missing, a dismissable toast notification is shown and the project switch is rejected. CHAT mode (No Project) does not require git. It is complex to bundle statically (libcurl, OpenSSL, libiconv) and 99.9% of developers already have it.

2. **rg, uv, and markitdown** are managed by a new `core/toolmanager/` package. On first run, tools are downloaded to `~/.c0wrk/tools/`. On subsequent runs, a `.versions` JSON file is checked and download is skipped if versions match.

3. **uv** (Rust static binary from Astral) is used as the Python bootstrapper. It manages portable Python installation, virtual environment creation, and pip package installation in a fully self-contained manner.

4. **`tools/bin/` is prepended to PATH** at startup after Phase 2, before Phase 3. This ensures all `exec.CommandContext("rg", ...)` calls resolve to the managed binary automatically — no call-site changes needed.

5. **Tool registry** is an embedded Go struct in `core/toolmanager/registry.go` — no external YAML/JSON config. Each tool has per-platform download URLs, SHA256 checksums, and archive layout metadata.

6. **Download verification**: archives are SHA256-verified after download. Empty checksum skips verification (for staged rollout). Cache-hit based on matching checksum.

7. **Disk space guard**: before downloading, the tool-manager checks that at least 200MB of free space is available on the volume.

8. **Error handling on failure**: a fatal modal is shown explaining which tool failed and why. The user is asked to check internet connection and disk space, then restart.

## Consequences

### Positive

- Users no longer need to manually install rg, uv, or Python+markitdown. git is only required for CODE mode and is checked on first project switch.
- Version management is automatic and deterministic across installs — every user gets the same tool versions.
- Uninstalling managed tools is as simple as deleting `~/.c0wrk/tools/`.
- Adding a new tool requires only a new entry in the registry — no UX/doc changes.
- Call sites (`exec.CommandContext("rg", ...)`) don't change — PATH prepend handles resolution transparently.

### Negative

- First-run startup time increases by 3–10 minutes (downloading approximately 35MB of archives, plus Python bootstrapping).
- The app now requires HTTP access on first run. After the first run, it can work offline.
- Managed tools consume approximately 100–200MB of disk space in `~/.c0wrk/`.
- Test and CI environments need to manage tool-manager behavior (mock HTTP, or pre-install binaries).

## Alternatives Considered

**Bundle tools inside the .app package.** Rejected: bloats the app bundle from ~100MB to ~300MB, complicates code signing, and Python virtual environments are 200MB+. The download-on-first-run approach mirrors VS Code's extension model and keeps the app bundle lean.

**Keep all tools as manual prerequisites with system-level install instructions.** Rejected: 3 tools plus a Python runtime is too high a barrier for new users. Would cause support burden and negative first impressions.

**Use a shell script installer (`curl | sh`).** Rejected: platform-specific shell scripts are fragile and hard to test. A Go-based downloader gives cross-platform consistency, proper HTTP error handling, checksum verification, and integration with the app's logging system.

**Use Docker as the universal tool runtime.** Rejected: Docker requires ~500MB install on macOS/Windows, adds VM overhead on those platforms, introduces filesystem path translation complexity, and degrades ripgrep's performance (which relies on mmap and direct FS access). For a desktop app targeting developers who may or may not use Docker, this creates more cognitive load than it removes.
