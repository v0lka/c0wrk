# Git Auto-Fetch

## Purpose

Automatic background `git fetch` for the active CODE project, keeping remote-tracking refs (and with them ahead/behind indicators and branch/tag state) fresh without user action. Three triggers — project switch (which covers app startup, because the frontend restores the last project through the same `SwitchProject` call), a periodic ticker, and window focus — all funnel into one gated, quiet-failure background attempt. Manual fetch from the Git panel is a separate RPC path and is never gated by this subsystem's config.

## Key Files

- `backend/frontend_api_git_autofetch.go` - the whole subsystem: `autoFetchOnce` (the single funnel), `autoFetchEnabled` (config gate), `runAutoFetch`/`newAutoFetchCmd` (hardened fetch runner), `pinGitTerminalPrompt` (env pin), `RequestGitRemoteRefresh` (focus RPC), `FrontendAPILifecycle.StartAutoFetch` / `autoFetchLoop` / `stopAutoFetchLoop` (ticker lifecycle)
- `backend/frontend_api.go` - funnel state (`autoFetchMu`/`lastAutoFetchAt`, `autoFetchLoopMu`/`autoFetchLoopCancel`/`autoFetchLoopDone`, test seams `autoFetchIntervalOverride`/`autoFetchTickFn`), `isGitRepo` (30s-cached via `gitRepoCacheTTL`), `Cleanup` (stops the ticker loop first)
- `backend/frontend_api_project.go` - the switch trigger: `go f.autoFetchOnce(autoFetchTriggerSwitch)` on the real-switch path only (the already-active early return skips it)
- `backend/frontend_api_git.go` - the manual remote-op side the funnel serializes against: `remoteOpMu`, `runSerializedRemoteOp` chokepoint, `remoteGitCmdTimeout` (2 min), `emitGitStatusChanged`
- `backend/config/config.go` / `backend/config/defaults.go` - `GitConfig` (`git.auto_fetch`, `git.auto_fetch_interval`) and its defaults
- `desktop/startup_phases.go` / `desktop/startup.go` - `startAutoFetchBackground` starts the ticker exactly once after the backend is ready (mirrors `startUpdateCheckerBackground`)
- `frontend/src/api/git.ts` - `requestRemoteRefresh()` RPC wrapper (frontend talks to the backend only through `@/api/*`)
- `frontend/src/hooks/useGitFocusRefresh.ts` - App-level window-focus hook: subscribes after runtime readiness, unsubscribes on unmount
- `core/workspace/git.go` - `GitCmdInRepo`: the hardened spawn path the fetch runs through (per-invocation config scan + neutralizing `-c` overrides; raw `GitCmdRaw` escape hatch for trusted repos)
- `internal/sysproc/git.go` - the sysproc env baseline (`GIT_EDITOR` pin, `GIT_ATTR_*` strip) that stays in effect underneath the funnel's own env pin

## Core Types

```go
// backend/config/config.go — config layer only stores raw values + defaults;
// the duration is parsed by the consumer (the funnel).
type GitConfig struct {
    AutoFetch         *bool  `yaml:"auto_fetch"`          // pointer-bool; nil = default true
    AutoFetchInterval string `yaml:"auto_fetch_interval"` // duration string; default "2m"
}

// backend/frontend_api_git_autofetch.go
const autoFetchMinInterval    = 60 * time.Second // shared by ALL triggers; stamped per attempt
const autoFetchTriggerSwitch  = "switch"         // SwitchProject hook (covers app startup)
const autoFetchTriggerTicker  = "ticker"         // periodic loop
const autoFetchTriggerFocus   = "focus"          // RequestGitRemoteRefresh RPC
const defaultAutoFetchInterval = 2 * time.Minute // fallback for missing/empty/unparseable interval
```

The `FrontendAPI` state: `autoFetchMu` guards `lastAutoFetchAt` (the shared min-interval stamp); `autoFetchLoopMu` guards `autoFetchLoopCancel`/`autoFetchLoopDone` (ticker lifecycle), deliberately a separate mutex from `autoFetchMu`.

## Flow

Every automatic fetch goes through the same funnel — gates are ordered cheapest-first, and the min-interval stamp is taken before any remote-touching subprocess:

```
trigger ──┐
 switch   │  (SwitchProject real-switch path; startup rides along)
 ticker   ├─▶ autoFetchOnce(trigger)                [fire-and-forget goroutine]
 focus    │     │
          │     ▼
          │  1. git.auto_fetch master gate          (config under configMu; nil config = OFF)
          │  2. active project ≠ No Project         (resolveGitRepoRoot — resolves
          │     the Git-panel focus root; equivalent for fetch: remote-tracking
          │     refs live in the common dir shared by every worktree)
          │  3. isGitRepo(repoPath)                 (30s-cached, local check)
          │  4. 60s min-interval, ALL triggers      (stamp taken HERE, on every attempt)
          │  5. `git remote` non-empty              (local check; empty → quiet skip)
          │  6. remoteOpMu.TryLock()                (busy manual op → instant skip)
          │     ▼
          │  runAutoFetch: hardened `git fetch` via workspace.GitCmdInRepo
          │     + GIT_TERMINAL_PROMPT=0 pin, ≤ remoteGitCmdTimeout (2 min), ctx = f.ctx()
          │     ▼
          └─  success → emitGitStatusChanged(repoPath)   [the ONLY emission]
              failure/skip → Debug log                    [silence]
```

Ticker lifecycle: `desktop` startup → `StartAutoFetch()` (idempotent; a second call is a no-op) → goroutine `autoFetchLoop` with `time.NewTicker(interval)`. On every tick the interval is re-read under `configMu`, so runtime config edits apply without an app restart: a changed positive period re-arms the ticker (that tick does not fetch), and a period `<= 0` parks the loop at the slow re-check cadence (`autoFetchDisabledRecheck`, 1 min) — while parked the loop keeps re-reading the interval, so restoring a positive value re-arms the ticker without an app restart (a fetch never fires while disabled). Shutdown: `FrontendAPILifecycle.Cleanup` calls `stopAutoFetchLoop` first — a non-blocking cancel; the in-flight fetch is already bounded by `remoteGitCmdTimeout` and cancelled through `f.ctx()`. The loop's context derives from `f.ctx()`, so it also dies with the application context even if `Cleanup` were skipped; the `done` channel closes on exit so tests can prove the goroutine terminated.

Focus path: `window focus` → `useGitFocusRefresh` → `requestRemoteRefresh()` (`@/api/git`) → RPC `RequestGitRemoteRefresh` → `go f.autoFetchOnce("focus")` and immediate return (never blocks the UI thread). All gating is server-side; the frontend never decides when a fetch is appropriate and surfaces no errors when the backend quietly skips.

## Invariants

- Every automatic fetch goes through `autoFetchOnce` — no trigger spawns git directly; adding a trigger cannot bypass the gates.
- The background path is silent: every skip and every failure (network, auth, timeout, unscannable config) is a Debug log — no events, no error toasts, no RPC errors. Only a successful fetch emits, exactly once, `git:status_changed` with the repo path (existing event; the catalog is unchanged).
- The min-interval (60s) is shared by all triggers and stamped on every attempt that reaches the gate — success, network failure, and busy-lock attempts alike — so a flapping network or a burst of triggers can never hammer the remote.
- Auto-fetch never queues behind a user operation: `remoteOpMu` is taken with `TryLock`; when a manual pull/push/fetch holds it, the attempt skips instantly. Conversely, manual remote ops keep their blocking `Lock` in `runSerializedRemoteOp` — only one network git operation runs at a time per app instance.
- `GIT_TERMINAL_PROMPT=0` is pinned exactly once via strip-then-append (glibc `getenv` resolves duplicate names to the FIRST entry, so an inherited `=1` would silently void an appended `=0` — same rationale as the `GIT_EDITOR` strip). On the trusted-repo path (`GitCmdRaw` leaves `cmd.Env` nil) the environment is first materialized from `os.Environ()` — the pin is additive and never wipes the inherited environment.
- Spawn hardening is preserved, not relaxed: the fetch runs through `workspace.GitCmdInRepo` (fresh per-invocation config scan, neutralizing `-c` overrides, sysproc baseline, fail-closed on unscannable configs). The env pin is layered on top of that baseline; there is no raw-git bypass for the funnel.
- Shutdown stops the ticker before anything else in `Cleanup`, non-blockingly; an in-flight fetch is bounded by `remoteGitCmdTimeout` (2 min) and the application context — nothing auto-fetch-related outlives shutdown.
- The ticker is infrastructure, never an RPC: `StartAutoFetch` is a `FrontendAPILifecycle` method (not promoted to the Wails surface), started exactly once after backend readiness; `RequestGitRemoteRefresh` is the only frontend-visible method and only enqueues an attempt.
- `git.auto_fetch_interval` of `"0"` (any parsed value `<= 0`) disables ONLY the ticker; the switch and focus triggers keep working because they never consult the interval.
- The switch trigger fires on the real-switch path only; the already-active early return in `SwitchProject` skips it (the project was already fetched-for when it was switched to).
- CHAT mode (No Project) never auto-fetches: the funnel's project gate stops every trigger, and the ticker loop in No Project is a repeated no-op.
- Manual remote operations (`Fetch`/`Pull`/`Push`/tag RPCs) are never gated by `git.*` config — `git.auto_fetch: false` silences only the automatic funnel.

## Configuration

| Key | Default | Meaning |
| --- | --- | --- |
| `git.auto_fetch` | `true` | Master gate for ALL automatic fetch triggers (switch, ticker, focus — and the startup ride-along). `false` = no automatic fetch ever runs; manual Git-panel fetch is unaffected. Pointer-bool: unset = default. A nil (unloaded) config fails closed. |
| `git.auto_fetch_interval` | `"2m"` | Ticker period only, as a duration string (`time.ParseDuration`). `"0"` disables just the ticker. Empty or unparseable falls back to `2m`. Re-read on every tick — runtime edits apply without an app restart. |

Both knobs are documented with a commented example block in `config.example.yaml` (next to the updates section). The config layer stores raw values and defaults; parsing is a consumer concern (`autoFetchInterval`).

## Extension Points

- New trigger: define a `autoFetchTrigger<Name>` constant and call `go f.autoFetchOnce(<name>)` from the new source. Every gate (config, project, repo, min-interval, remote check, TryLock serialization) plus the quiet-failure contract applies automatically.
- Trigger names are constants (not inline strings) so log lines stay greppable — keep that when adding one.
- Test seams: `autoFetchIntervalOverride` (forces the ticker period; mirrors `switchLockTimeoutOverride`), `autoFetchDisabledRecheckOverride` (shortens the parked-state re-check cadence in loop tests), and `autoFetchTickFn` (replaces the funnel call inside the loop so ticker tests need no git/network). `autoFetchLoopDone` proves goroutine termination after `Cleanup`.

## Related Specs

- [workspace.md](workspace.md) - git status/ignored caches that a `git:status_changed` emission invalidates; workspace watcher interplay
- [event-catalog.md](../contracts/event-catalog.md) - `git:status_changed` (reused as-is; auto-fetch introduces no new events)
- [security-model.md](../architecture/security-model.md) - git subprocess hardening the funnel inherits via `GitCmdInRepo`; [033-git-subprocess-hardening.md](../decisions/033-git-subprocess-hardening.md), [034-git-trust-opt-out.md](../decisions/034-git-trust-opt-out.md)
- [desktop-frontend.md](../contracts/desktop-frontend.md) - Wails RPC surface the `RequestGitRemoteRefresh` method joins via embedding
