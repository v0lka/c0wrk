# ADR-032: Offline-First Tool Reconciliation

## Status

Accepted

Amends [ADR-010](./010-tool-manager.md): every ADR-010 decision remains in force except decision point 8 (fatal modal on tool-install failure), which this ADR replaces.

## Context

Startup of a desktop app must not require network access. Users work offline or behind restricted networks where GitHub Releases and PyPI may be unreachable or black-holed. ADR-010's failure handling (point 8) showed a fatal Exit modal and quit the app whenever `EnsureCriticalTools` could not install a managed tool — making the whole application unusable offline, including on machines where the tools were already installed or downloadable from the local cache. The hard requirement is the opposite: the app must start correctly and fully in autonomous mode; there are no conscious tradeoffs here.

Two implementation details made offline failures destructive:

- The reconciliation loop aborted on the first per-tool error, so one unreachable download could prevent other tools from being installed even when their archives were already cached.
- Stale binaries and Python environments were removed *before* the replacement download, so an offline reinstall attempt could leave the machine with less than it had.

## Decision

1. **Two-pass reconciliation.** The synchronous startup pass runs `EnsureCriticalTools(ctx, EnsureOptions{AllowNetwork: false})` — strictly local work: `--version` probes of existing binaries and installs from already-cached SHA256-verified archives (`DownloadMode.DownloadCacheOnly`; the downloader fails fast with `ErrCacheUnavailable` and performs no network I/O in this mode). Anything left not Ready is retried by a background goroutine running `EnsureCriticalTools(ctx, EnsureOptions{AllowNetwork: true})` after startup has completed. The two passes are sequenced, never concurrent (the Manager remains single-goroutine).

2. **Per-tool failure isolation.** Reconciliation returns a `ToolStatus` per tool (`Ready`/`Installed`/`Err`) instead of aborting on the first error. One unavailable tool never prevents the remaining tools from being reconciled. Structural failures (directory creation, disk-space guard, registry resolution) are returned as the run-level error.

3. **No destructive operations without secured replacement bytes.** The stale static binary is removed only after the verified archive is on disk; the Python wrapper+venv are removed immediately before the bootstrap runs. An offline failure therefore never leaves the machine with less than it had before.

4. **Fail-closed verification is retained, non-fatally.** The post-install `--version` probe still rejects an install that does not report the pinned version (`.versions` stays untouched), and the broken binary is removed so it cannot masquerade as a working tool. The tool is reported not Ready; startup continues.

5. **Startup never dies because of tools.** `initTools` always prepends `tools/bin/` to PATH and always emits `tool_manager:done` (so the splash resolves). Tools still unavailable after the background pass surface exactly once as a `runtime_error` toast (`error_code: "tool_install_failed"`). The background pass never emits `tool_manager:start` (the frontend transitions to the splash on that event).

## Consequences

- Offline is a fully supported steady state: with tools installed (or valid cached archives present), startup performs zero network I/O and completes in milliseconds; a version bump whose archive was fetched while online completes fully offline on the next startup.
- Late installs are picked up transparently: `exec.CommandContext` resolves PATH per-exec, so tools installed by the background pass become usable without an app restart.
- First-run offline users get a running app with degraded features and a single clear toast, instead of an Exit wall — plus automatic completion if connectivity returns during the session (one background attempt per launch; no endless retry loop against captive portals).
- The `Downloader` interface gains a `DownloadMode` parameter, and `EnsureCriticalTools` changes signature — both are internal to the c0wrk/sp4rk boundary.
- `NeedsInstall` remains stat-based (no probes), so the splash may briefly show for tools that the offline pass then satisfies from cache; harmless by design.

## Alternatives Considered

- **Keep the fatal modal, gate it on "online but broken install".** Rejected: correct, verified installs must also start without network (probe hiccups, quarantine, sandboxing); a modal cannot distinguish these reliably and the requirement admits no exceptions.
- **Retry downloads inline at startup with a short timeout.** Rejected: bounded timeouts either break slow first-run downloads or still stall offline startups; a background pass gives both correctness and instant startup.
- **Warn-and-skip stale binaries offline (no reinstall attempt).** Rejected: the cached-archive path can fully fix a stale binary offline; attempting it first is strictly better and costs nothing when the cache is absent (fail-fast sentinel).
