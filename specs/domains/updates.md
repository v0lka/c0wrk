# Updates

## Purpose

Owns the in-app self-update: discovering a newer GitHub release for the running platform and packaging flavor, deciding whether it is offered, downloading and integrity-verifying it, and handing it to the detached two-process updater that swaps the install tree. The security decisions behind this flow are recorded in [ADR-023](../decisions/023-auto-update.md); this spec fixes the runtime behavior, its configuration, and the RPC/event surface.

## Key Files

- `core/updater/checker.go` — `Checker`/`Config`/`Result`: the GitHub `releases/latest` query, semver comparison, skip gate, and asset selection; `NewCheckerWithProxy` builds the proxy-aware client and every client is wrapped with `securehttp.SecureHTTPClient`
- `core/updater/assets.go` — the supported platform/flavor matrix (`AssetNameFor`, `SelectAsset`, `CurrentPlatform`), kept in step with the release workflow by a matrix-sync test
- `core/updater/flavor.go` — `CurrentFlavor`: the packaging flavor is read from marker libraries beside the executable at check time (never persisted)
- `core/updater/checksums.go` — `SHA256SUMS` parsing (`ParseChecksums`, `FindChecksum`)
- `core/updater/verifier.go` — the `Verifier` seam and `SHA256Verifier` (constant-time, case-insensitive digest comparison, `ErrChecksumMismatch`)
- `core/updater/downloader.go` — staged download of the asset plus its `SHA256SUMS`, progress throttling, atomic per-file writes, and the fail-closed "remove on any verification failure" rule
- `core/updater/installer.go` — `DiscoverInstallRoot` (location safety), `PrepareSelfUpdate`, `SelfUpdateArgs`, `ApplySelfUpdate` (the second-process apply), `CleanupStaleUpdaters`
- `core/updater/installer_*.go` — per-platform extraction/swap/relaunch and the macOS staging bundle + ad-hoc re-sign
- `backend/updater_detach_darwin.go`, `backend/updater_detach_linux.go`, `backend/updater_detach_windows.go` — the platform launch helpers (`launchStagedUpdater`): LaunchServices on macOS, a setsid-detached child on Linux, a plain detached child on Windows, each with its liveness probe before the app commits to quitting
- `core/updater/state.go` — `State` (`skipped_version`, `last_check`), atomic JSON persistence, `ShouldCheck`
- `backend/frontend_api_updater.go` — the frontend-callable surface (`CheckForUpdates`, `GetLastUpdateCheck`, `DownloadUpdate`, `ApplyUpdate`, `SkipVersion`, `GetUpdateSettings`, `SetUpdateSettings`), the result cache, and the single automatic check path `RunBackgroundUpdateCheck`
- `backend/startup_phases.go` — launches the background check once, in a goroutine, after the backend is ready
- `main.go` — the `--self-update` entry point (dispatched before Wails starts) and the startup `CleanupStaleUpdaters` reap
- `frontend/src/stores/updateStore.ts`, `frontend/src/hooks/useUpdateChecker.ts`, `frontend/src/components/UpdateToast.tsx` — the UI state machine and its toast

## Core Types

```go
// The outcome of one check. Available is true only when the release is
// strictly newer, not skipped, and ships an asset for this platform+flavor.
type Result struct {
    Available      bool
    LatestVersion  string // tag_name, e.g. "v1.2.4"
    CurrentVersion string // core/version.Version ("dev" for local builds)
    AssetURL       string
    AssetName      string
    ReleaseNotes   string
    PublishedAt    string
    HTMLURL        string
}

// Persisted runtime state (~/.c0wrk/update_state.json) — deliberately
// separate from config.yaml, which holds only operator preferences.
type State struct {
    SkippedVersion string    `json:"skipped_version,omitempty"`
    LastCheck      time.Time `json:"last_check"`
}

// The packaging flavor read from the install tree at check time.
type Flavor string // "cpu" (default) | "cuda13" | "openvino"
```

## Flow

```
manual: CheckForUpdates ─┐
                         ├─ operator gate (updates.enabled)
background (startup) ────┘        │
   RunBackgroundUpdateCheck       ├─ auto_check gate (background only)
     → interval gate (ShouldCheck)│
                                  ▼
                         checkAndCache(skippedVersion)
                           → Checker.Check → GitHub releases/latest (HTTPS)
                           → semver compare + skip + asset selection
                           → cache lastCheckResult
                                  │
                 ┌────────────────┴─────────────────┐
        available │                                  │ none / error
        update:available                       update:none | update:error
                 ▼
        DownloadUpdate → staging dir
          → asset + SHA256SUMS (HTTPS only)
          → SHA256 verify (fail-closed, archive removed on failure)
          → update:downloaded
                 ▼
        ApplyUpdate
          → DiscoverInstallRoot (location safety)
          → PrepareSelfUpdate (stage a copy of this binary)
          → launch detached: --self-update --pid <pid> --stage <dir> --target <root>
          → 1s startup probe (abort the quit if the updater died)
          → coordinated graceful quit (Wails Shutdown hooks run)
                 ▼
        second process (main.go, before Wails)
          → wait for --pid to exit → extract → swap with <root>.old backup
          → relaunch the new build → clean staging
```

### Check Semantics

The checker queries `GET https://api.github.com/repos/v0lka/c0wrk/releases/latest` with the GitHub JSON accept header, a dedicated user agent, a 4 MiB response cap, and a 15-second default request timeout; the API origin is a constant and cannot be downgraded to plain HTTP. Non-200 responses map to sentinels: 404 → `ErrNoRelease`, 403 with `X-RateLimit-Remaining: 0` → `ErrRateLimited`, any other 403 → `ErrForbidden`.

Availability is a three-way conjunction:

1. **Version** — the latest tag is strictly greater than the running version after normalizing both to strict semver (a missing leading `v` is added, missing minor/patch components are padded, so `v0.7-beta` compares as `v0.7.0-beta`). A running version that is not valid semver (the `dev` default of a local build) counts as unknown and any valid release is offered; an invalid latest tag never offers an update.
2. **Skip** — the latest tag compared on canonical semver form must differ from `skipped_version`; `1.2.0` and `v1.2.0` are the same skip. An empty or missing skip suppresses nothing.
3. **Asset** — the release must carry the archive for the running GOOS/GOARCH and packaging flavor; a missing asset is an error, not "no update".

The platform matrix is `darwin/arm64`, `linux/amd64` (cpu, cuda13, openvino), `linux/arm64`, and `windows/amd64`, with the canonical archive name per entry. The flavor is detected at check time from the install tree beside the executable (`libonnxruntime_providers_cuda.so` → cuda13, `libonnxruntime_providers_openvino_plugin.so` → openvino); GPU flavors exist only on `linux/amd64`, CUDA wins if both markers are present, and every error or unknown platform degrades to `cpu`.

### Download and Integrity

`DownloadUpdate` uses the result cached by the most recent check and fails with "call CheckForUpdates first" when none is cached or no update is available. The asset name from the release API is attacker-influenceable and is therefore required to be a plain basename before it becomes a path component.

Both the asset URL and the derived `SHA256SUMS` URL (same release directory) must pass `RequireHTTPS`, and the client carries the per-hop redirect policy. Downloads land in `config.UpdateStagingDir` — created with `MkdirAllReal` so a swapped link cannot redirect the verified archive out of the agent directory — and each file is written to an atomic sibling temp then renamed. Progress callbacks are throttled to ~100 ms.

Verification is fail-closed: a missing per-asset entry (`ErrChecksumNotFound`) or a digest mismatch (`ErrChecksumMismatch`) removes the downloaded archive and returns an error. Digests are compared case-insensitively through `crypto/subtle`. A failed download therefore never leaves a partially trusted artifact in staging.

### Apply and Recovery

`DiscoverInstallRoot` refuses an install root inside an OS temp directory or a Downloads folder, a root or parent that is not writable (the swap renames in both), and a directory that carries no install marker — a bare development binary is never "validated" into having its folder swapped out. Failures are `ErrNonStandardLocation` and leave the tree untouched.

`PrepareSelfUpdate` copies the running executable into a fresh staging directory and launches **that copy** as `--self-update --pid <pid> --stage <dir> --target <root>`, so the running process never overwrites its own executable. Launch is platform-specific and deliberately detached: a minimal `LSUIElement` bundle started through LaunchServices on macOS (a bare child keeps the dying app as its responsible process and is killed at teardown), a setsid-detached child on Linux, and a plain detached child on Windows (which has no Unix session to detach from). On macOS the staged executable is re-signed ad hoc because AMFI rejects a copied Mach-O whose signature is sealed to the original bundle.

The updater's stdio is captured in `<staging>/updater-output.log`, and `ApplyUpdate` runs a **bounded liveness probe** before committing to the quit: on Linux and Windows a 1-second post-start window in which an immediate exit proves the child died before doing anything; on macOS a LaunchServices poll — the updater process must appear within 5 seconds and stay observable for 1.5 seconds, inside a 15-second launch budget — because `open` returning success only means LaunchServices accepted the launch. A dead updater aborts the quit with `update:error` instead of closing the app on a no-op update. Only after a live updater is confirmed does the app perform a **coordinated graceful quit** (the Wails quit path, never `os.Exit`), so the shutdown hooks the waiting updater relies on actually run.

The second process detects `--self-update` before Wails starts, waits for the recorded PID to exit, extracts the staged archive beside the install root (zip-slip/tar-slip guarded; symlink entries are preserved through a containment-checked path on the zip/macOS path and skipped on tar), moves the current tree to `<root>.old`, moves the new tree into place (rolling back from `.old` if that move fails), relaunches the app, and removes staging. The `.old` tree is retained until the next attempt so a build that fails to start can be rolled back manually.

`CleanupStaleUpdaters` runs at every normal startup and reaps orphaned updater artifacts older than 24 hours, dated on the entry and its direct children; unreadable entries count as live (never delete what cannot be dated), except regular files, which are dated on their own mtime so the Windows updater executable cannot become immortal.

## RPC and Event Surface

| RPC | Behavior |
| --- | -------- |
| `CheckForUpdates` | Manual check. Honours `updates.enabled` (a disabled subsystem reports "not available" without touching the network) but bypasses `updates.auto_check` and the interval. Caches the result and emits `update:available`, `update:none`, or `update:error` |
| `GetLastUpdateCheck` | Recovery pull of the cached result when an update is available; lets the toast survive an event that raced the frontend's subscription |
| `DownloadUpdate` | Downloads and verifies the cached update, emits `update:progress` (throttled) then `update:downloaded` or `update:error` |
| `ApplyUpdate` | Stages and launches the updater, then quits; every preparation failure emits `update:error` and keeps the app running |
| `SkipVersion` | Persists the dismissed tag in `update_state.json` (an empty value clears it) and invalidates the cached result |
| `GetUpdateSettings` / `SetUpdateSettings` | Read / persist `updates.auto_check`, returning the resolved settings plus the running version and the operator gate; a failed persist restores the prior value |

Events are catalogued in [../contracts/event-catalog.md](../contracts/event-catalog.md): `update:available`, `update:none`, `update:progress`, `update:downloaded`, `update:error`.

`RunBackgroundUpdateCheck` is the **only** automatic path. It runs once at startup in a goroutine and applies three gates in order: `updates.enabled`, then `updates.auto_check`, then the interval (`updates.check_interval`, default 6 h; a missing, unparseable, or non-positive value falls back to 6 h) against `last_check`. The check is bounded at 20 s, any failure is swallowed at Debug (never a user-facing error), and the attempt is recorded even when it fails so a transient outage cannot cause a startup retry storm — the state is re-read immediately before saving so a `SkipVersion` landing during the check is preserved. Only a discovered update emits an event.

## Invariants

- The update check, the archive fetch, and the checksum fetch are HTTPS-only, and no redirect hop can downgrade them.
- An archive is never applied before a successful digest verification; every verification failure removes the artifact.
- The running application never overwrites its own executable: staging plus a second process performs every swap.
- The app commits to quitting only after the staged updater has provably started; a dead updater leaves the app running with an actionable `update:error`.
- The install root is swapped only when it is a writable, standard, marker-bearing installation; every other location is refused without modification.
- `<root>.old` survives a successful swap until the next attempt, and a failed new-tree move restores the previous tree.
- `updates.enabled` is the operator master switch: with it off neither the manual nor the automatic check touches the network, while `updates.auto_check` suppresses only the automatic path.
- A skipped release suppresses exactly that canonical version; skipping is persisted in `update_state.json`, never in `config.yaml`.
- The background check is silent on every failure and records each attempt; the manual path is the only one that surfaces errors.
- The update result cache is invalidated by a new check, a skip change, and a settings change, so `DownloadUpdate` can only act on a result the user is currently seeing.

## Configuration

| Key | Default | Meaning |
| --- | ------- | ------- |
| `updates.enabled` | `true` (`*bool`) | Operator master switch; `false` disables the whole update subsystem, including the automatic check |
| `updates.auto_check` | `true` (`*bool`) | Suppresses only the automatic startup check; manual checks still work |
| `updates.check_interval` | `"6h"` | Minimum time between automatic checks; unparseable or non-positive falls back to 6 h |

Runtime state lives in `~/.c0wrk/update_state.json` (`skipped_version`, `last_check`), written atomically with mode `0600`. Downloads stage in `config.UpdateStagingDir(agentDir)`. Neither the state file nor the staging tree is a configuration surface.

## Extension Points

- **A new platform or packaging flavor** — extend `supportedPlatforms` in `core/updater/assets.go` together with the release workflow's build matrix; the matrix-sync test fails when the two drift, and `flavorFor` needs the marker-library rule for a new GPU flavor.
- **A stronger integrity scheme** — implement `updater.Verifier` and pass it to `NewDownloader`; the interface exists so a signature check can replace SHA256 without touching the download or apply stages. Any change here is a supply-chain decision and needs an ADR.
- **A new update surface** — go through `checkAndCache`/`DownloadUpdate` rather than calling `Checker` directly, so result caching, proxy awareness, and event emission stay single-sourced.

## Related Specs

- [../decisions/023-auto-update.md](../decisions/023-auto-update.md) — the self-update decision: threat model, SHA256-only integrity, unsigned artifacts, and the rejected signature/notarization alternatives
- [../contracts/desktop-frontend.md](../contracts/desktop-frontend.md) — the updater RPC rows
- [../contracts/event-catalog.md](../contracts/event-catalog.md) — the `update:*` events
- [frontend/stores.md](frontend/stores.md), [frontend/events.md](frontend/events.md) — `updateStore` and its event wiring
- [networking.md](networking.md) — the proxy-aware client and the HTTPS-only download policy this surface uses
- [configuration.md](configuration.md) — how `updates.*` is loaded, defaulted, validated, and saved
- [cli.md](cli.md) — the `--self-update` re-exec dispatch and its position in startup ordering
