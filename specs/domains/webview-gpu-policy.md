# Linux Webview GPU Policy

## Purpose

Chooses the Linux webview's (WebKitGTK) hardware-acceleration policy explicitly instead of relying on Wails' nil-options default, and exposes it to the user. The default `auto` setting resolves to `on-demand` — the WebKit default, which composites trivial CSS animations on the GPU — and a startup watchdog detects the NVIDIA DMABUF grey-window failure (wails#2977) from the captured stderr of the current run, persists a one-way fallback to `never`, and relaunches the app so the fresh instance starts safe. The user's choice lives in `webview_state.json` (Settings → General, Linux-only section) with the `C0WRK_WEBVIEW_GPU_POLICY` env override keeping the highest precedence. The conversion into Wails' enum deliberately compensates for a Go-enum/C-layer mismatch in the pinned Wails v2.16.0, so the policy name the pipeline resolves is the one WebKit actually receives.

## Key Files

- `backend/webview_gpu.go` — the canonical string level: `WebviewGpuPolicyEnvVar`, the policy alphabet constants, `NormalizeWebviewGpuPolicy` (env), `NormalizeWebviewGpuSetting` (setting), `ResolveWebviewGpuPolicy` (setting+fallback → policy+source), `WebviewGpuState` / `LoadWebviewGpuState` / `SaveWebviewGpuState` (webview_state.json), `DeriveWebviewGpuRun` (the shared launch formula), the watchdog fallback reason constant
- `desktop/webview_gpu_policy.go` — the Wails-bound leaf: `webviewGpuPolicy` enum, `wailsWebviewGpuPolicy` (the compensation mapping), `WebviewGpuPolicyOptions` / `webviewGpuPolicyOptions`
- `desktop/webview_gpu_watchdog.go` — the DMABUF-failure watchdog: offset snapshot, bounded startup scan (`scanStderrTail`), detect → persist → relaunch ordering; the relaunch handover (`WaitForGpuRelaunchHandover`, `GpuRelaunchHandoverBudget`); `webview_gpu_watchdog_relaunch_linux.go` / `..._other.go` — the detached self-relaunch
- `desktop/webview_gpu_postmortem.go` — the next-start post-mortem (`ApplyWebviewGpuCrashFallback`): persists the fallback when the previous fresh-auto run died on a fatal Wayland protocol error before DomReady
- `desktop/webview_nvidia_explicit_sync.go` — `ApplyNvidiaExplicitSyncWorkaround`: sets `__NV_DISABLE_EXPLICIT_SYNC=1` on Linux before wails.Run unless the environment already carries the variable
- `desktop/shutdown_watchdog.go` — `EffectiveShutdownDeadline` (the configured `shutdown.hardDeadline` mapped onto the effective teardown deadline) and the cumulative default the handover budget is sized from
- `backend/frontend_api_webview.go` — the Settings RPCs `GetWebviewGpuPolicy` / `SetWebviewGpuPolicy`; `backend/frontend_api.go` derives the running-policy snapshot in Init from the same inputs as the wails.Run options (`FrontendAPIConfig.WebviewGpuStderrObserved`)
- `desktop/singleinstance.go` — `WaitForInstanceLockRelease`, the relaunch ordering primitive the handover wait is built on
- `backend/config/paths.go` — `WebviewStatePath` (webview_state.json, deliberately NOT part of config.yaml)
- `main.go` — the production call sites: `Linux: desktop.WebviewGpuPolicyOptions(agentDir, stderrObserved, slog.Default())`, the watchdog arm (`SetWebviewGpuWatchdog`), the stderr-observation hand-off (`SetWebviewGpuStderrObserved`), and the relaunched-child handover (`C0WRK_GPU_RELAUNCH_WAIT_LOCK` consumption — terminal on failure)
- `desktop/startup.go`, `desktop/app.go` — DomReady scan start (`startWebviewGpuWatchdog`)
- `frontend/src/api/webview.ts`, `frontend/src/components/settings/WebviewGpuPolicySettings.tsx` — the Settings UI (Linux-only section, Settings → General)
- `desktop/webview_gpu_policy_test.go`, `desktop/webview_gpu_watchdog_test.go`, `backend/webview_gpu_test.go`, `backend/frontend_api_webview_test.go` — the pins (see Invariants)

## Core Types

```go
// backend/webview_gpu.go — the canonical alphabet (env var, persisted
// setting, Settings UI, RPCs): auto | always | on-demand | never.
// ResolveWebviewGpuPolicy(setting, fallbackActive) → policy, source:
//   concrete setting → itself, source "setting"
//   auto + fallback  → never,    source "auto-fallback"
//   auto (or "")     → on-demand, source "auto"
// DeriveWebviewGpuRun(goos, agentDir, stderrObserved, logger) wraps that
// formula with the launch-observation input: on Linux (the goos parameter —
// production passes runtime.GOOS; tests pass the platform explicitly, the
// formula never reads runtime.GOOS itself), an unobserved fresh auto
// (crash capture failed or opted out) demotes to
//   never, source "auto-unobserved" — no launch may enable acceleration
// while the failure watchdog has no stderr to watch.

// desktop/webview_gpu_policy.go — the effective policy, deliberately
// decoupled from Wails' enum ordering.
type webviewGpuPolicy int

const (
    gpuPolicyNever    webviewGpuPolicy = iota
    gpuPolicyAlways
    gpuPolicyOnDemand
)

// wailsWebviewGpuPolicy converts to the value handed to Wails.
// v2.16.0 mapping (desired → Go enum → C switch in window.c → WebKit):
//   always    → WebviewGpuPolicyOnDemand (0) → case 0 → POLICY_ALWAYS
//   on-demand → WebviewGpuPolicyAlways  (1)  → case 1 → POLICY_ON_DEMAND
//   never     → WebviewGpuPolicyNever   (2)  → case 2 → POLICY_NEVER

// backend — webview_state.json shape.
type WebviewGpuState struct {
    GPUPolicy    string                  `json:"gpu_policy,omitempty"`     // "" → auto
    AutoFallback *WebviewGpuAutoFallback `json:"auto_fallback,omitempty"`  // nil → none
}
type WebviewGpuAutoFallback struct {
    Policy     string `json:"policy"`      // "never"
    Reason     string `json:"reason"`      // dmabuf_render_failure | wayland_protocol_error
    DetectedAt string `json:"detected_at"` // RFC3339 UTC
}
```

## Flow

```
C0WRK_WEBVIEW_GPU_POLICY (env)  ── valid ──► wins (source "env")
        │ invalid → Warn, falls through
        ▼
webview_state.json (Settings choice; "" → auto)
        │
        ▼
DeriveWebviewGpuRun  [backend]  ── the single formula ──┬─► main.go: wails.Run options
        │                                               │     (desktop: canonical → enum → C int)
        ▼                                               └─► FrontendAPI.Init: running snapshot
ResolveWebviewGpuPolicy                                  for the Settings RPCs
   auto → on-demand (issue #34's fix)
   auto + fallback → never (grey-window protection)
   fresh auto + unobserved stderr (Linux) → never
   (source "auto-unobserved", explicit Warn at the
    wails.Run call site)
        │
        ▼
Watchdog (armed ONLY for fresh auto with an accelerated resolution
 AND a usable stderr capture):
  main.go (after crashlog.Install, before wails.Run)
        │ offset snapshot of stderr.log
        ▼
  DomReady → immediate first scan, bounded rescan (2 s interval, 30 s window)
        │ signature match (Failed to create EGL images for DMABufs /
        │  DRM_IOCTL_NVIDIA_GEM_ALLOC_NVKMS_MEMORY failed /
        │  Failed to create GBM buffer …)
        ▼
  persist auto_fallback → relaunch detached (child waits for the
  advisory lock release via C0WRK_GPU_RELAUNCH_WAIT_LOCK) → quit
        │ setting left auto during the window (a Settings write
        │  landed under the RMW lock) → NO fallback record,
        │  the relaunch still runs
        │ persist failed → NO relaunch (fail-safe against a loop)
        ▼
  fresh instance: auto + fallback → never (software rendering)
```

The relaunched child's handover: it waits for the predecessor's advisory lock
(device: the kernel drops it on exit) for `shutdown.hardDeadline` (the
configured teardown budget, default 66 s) + margin. Success → normal startup.
Expiry → the child does NOT enter Wails startup (it would relay its launch to
the still-quitting instance and exit — a one-shot relay that loses the
replacement window): it logs at Error and exits non-zero; the fallback is
already persisted, so the next manual start applies it.
```

## Invariants

- The env var accepts exactly `always`, `on-demand`, `ondemand` (alias), `never` — case-insensitive, whitespace-tolerant, no `auto`. A valid value wins over everything; an INVALID value logs a Warn and falls through to the persisted setting instead of forcing never — the setting is user-visible now, the env var is an advanced hatch, and a typo must not silently disable GPU compositing.
- The default resolution is `auto` → `on-demand`, the WebKit default; `never` applies only through an explicit choice or the watchdog fallback. Passing the Linux options explicitly pins the rendering against a future Wails default change.
- `wailsWebviewGpuPolicy` is the single place aware of the v2.16.0 Go-enum/C-layer mismatch (`linux.go`: OnDemand=0, Always=1, Never=2 vs `window.c` `SetupWebview`: 0→ALWAYS, 1→ON_DEMAND, 2→NEVER); like-named constants are deliberately NOT used there, and the mapping yields the C-layer integer that selects the WebKit policy matching the canonical name.
- Tests pin the compensation by C-layer numeric value (`0/1/2`), and an enum-order canary (`TestWailsWebviewGpuPolicyEnumOrderCanary`) pins the Go enum's numeric order — so a Wails upgrade that reorders either side fails the desktop test suite loudly instead of silently swapping `always`/`on-demand` at runtime.
- The watchdog arms ONLY for fresh `auto` with an accelerated resolution AND a usable stderr capture: an explicit setting or env value is never second-guessed, an already-fallen-back instance has nothing left to detect, a launch whose crash capture is unavailable has nothing to scan, and non-Linux never arms. An invalid env value keeps detection armed (a typo is not an explicit choice). The fresh-auto condition is re-checked under the RMW lock at write time: a Settings choice landing inside the detection window suppresses the fallback record — writing it anyway would create a record `ResolveWebviewGpuPolicy` ignores while the UI keeps showing the auto-disabled banner. The relaunch still runs: the running instance is stuck on the failed accelerated resolution, and the fresh one applies the explicit choice (its own watchdog disarms for a non-auto setting — no relaunch loop).
- An unobserved launch (crash capture install failed, or `C0WRK_DISABLE_CRASH_CAPTURE=1` opted out) never runs the accelerated fresh-auto default: `DeriveWebviewGpuRun` demotes it to never (source `auto-unobserved`, explicit Warn at the wails.Run call site), because enabling acceleration while the DMABUF watchdog has no stderr to watch trades a working-but-slow UI for a broken grey window with no recovery path. Explicit user choices — env or setting — are never demoted, an auto+fallback launch is already on never, and off Linux the demotion is inert (the machinery does not apply). The Init running snapshot consumes the SAME fact (`FrontendAPIConfig.WebviewGpuStderrObserved`, recorded by main via `SetWebviewGpuStderrObserved`), so the Settings report matches what wails.Run actually applied; the UI annotates the source ("stderr capture unavailable"). `RestartRequired` reads true against it — honest: a healthy capture would resolve on-demand.
- `SetWebviewGpuPolicy`'s read-modify-write of `webview_state.json` runs under the same webview_state lock as the watchdog's fallback persist: both writers rewrite the whole file (`LoadWebviewGpuState` → mutate → `SaveWebviewGpuState`), so an unlocked last-writer-wins could drop either side's change — the Settings write losing a fresh fallback record, or the watchdog losing a just-chosen setting. Any future writer of `webview_state.json` must take the same lock.
- Detection scans only the bytes appended to stderr.log AFTER the arm-time offset (this run's output; the file is append-only and rotates only at install time), within a bounded startup window after DomReady (immediate first scan — the failure prints at the webview's first paint, before DomReady, so the tail already carries it — then a 2 s rescan interval, 30 s deadline). The grey window is a first-paint buffer-allocation failure; an unbounded scan would risk a hostile self-relaunch while the user works. The handover budget covers the whole configured teardown, but the vector index's background sidecar backfill still deliberately runs its embedding-bearing enumeration WITHOUT the service lock so `Close()` can cancel it instead of queuing behind it — a backfill on a large collection used to cost more than the entire relaunch budget by itself (see `migrateFileHashes` in `core/vectorindex/collection.go`).
- NVIDIA explicit sync is disabled for the process on Linux (`__NV_DISABLE_EXPLICIT_SYNC=1`, set in main.go before wails.Run; an operator-set value — including `0` — wins). Root cause it removes: under an accelerated policy WebKitGTK/GTK3 composites through an EGL surface on the toplevel `wl_surface` while GTK3 keeps attaching `wl_shm` frames to the same surface; NVIDIA's EGL attaches a `wp_linux_drm_syncobj_surface_v1` to it, and mutter kills the connection on the next shm attach (`Explicit Sync only supported on dmabuf buffers` → GDK `Error 71` → exit(1) before DomReady). Implicit (dma-buf fence) sync keeps the accelerated path working; the variable is inert for other drivers and on X11.
- Wayland post-mortem (safety net for the same failure class with any other cause): the accelerated policy can make GDK hit `Error 71 (…) dispatching to Wayland display` (EPROTO) and `exit(1)` the process before DomReady — no in-run scan ever runs, so every launch dies identically. `ApplyWebviewGpuCrashFallback` (main.go, after `crashlog.Install`, before the policy resolution and the watchdog arm) splits the stderr.log tail into runs by the crashlog banners (`crashlog.RunBannerPrefix` / `ExitBannerPrefix`) and inspects the PREVIOUS run: no clean exit banner AND a line carrying both `Error 71 (` and `dispatching to Wayland display` (the strerror text is localized, the format is not) → persist the fallback (reason `wayland_protocol_error`) under the same gate as the watchdog (Linux, fresh auto, usable capture, no valid env override). Other errnos (EPIPE when the compositor itself goes away) are not a GPU-policy signal. The crashed launch itself cannot recover; the next manual start does.
- The fallback persists BEFORE the relaunch. A failed persist NEVER relaunches — the fresh instance would resolve the accelerated policy again and grey-window in a loop. A failed relaunch quits nothing: the fallback is already persisted, the next start is safe, and the user keeps a live session (with the record suppressed by a concurrent explicit choice, the next start applies that choice instead — equally safe).
- The relaunched child carries `C0WRK_GPU_RELAUNCH_WAIT_LOCK=1` and waits for the previous instance's advisory lock to release before starting up — the spawn-then-quit ordering cannot race the fresh instance into the Wails single-instance relay. The handover budget is sized from the CONFIGURED shutdown deadline (`shutdown.hardDeadline` → `EffectiveShutdownDeadline`, the same mapping the shutdown watchdog enforces) plus a fixed exit margin, so it always covers the predecessor's worst legitimate teardown (the ~46 s embedded-LLM stop included; the original fixed 15 s timeout did not). A budget expiry is TERMINAL: the child logs at Error and exits instead of starting up — entering Wails startup while the predecessor still holds the lock would relay the launch to the quitting instance and exit it, leaving no replacement window at all. The fallback is already persisted at that point, so the next manual start applies it.
- The fallback is sticky until the user acts: ANY `SetWebviewGpuPolicy` clears it — a concrete value pins itself, re-choosing `auto` is the retry action, and the watchdog re-arms for the next launch.
- `RestartRequired` in the RPC report is always false while the running policy came from the env source: the override wins on the next launch too, so no restart could apply a change.
- The choice persists in `webview_state.json` (agent dir), deliberately NOT in config.yaml: the policy must be readable BEFORE `wails.Run` creates the webview, while config.yaml loads in `app.Startup` only after the window exists — the same constraint that keeps `window_state.json` outside config.yaml. The path comes exclusively from `config.WebviewStatePath`.
- macOS and Windows receive nil Linux options and a disarmed watchdog — the whole subsystem feeds the WebKitGTK frontend only; the Settings section hides off Linux (`isLinuxHost`), and a setting persisted there survives OS moves harmlessly.
- `SetWebviewGpuWatchdog` is deliberately an exported `App` method — internal startup plumbing rides the Wails binding surface by the same precedent as `SetWailsLogger`. The exposure is inert by construction: the bridge cannot construct a meaningful watchdog (a deserialized zero value could at worst replace the armed one before DomReady, disabling detection for that run), and the field is never re-read after `startWebviewGpuWatchdog` has started the original. Accepted residual risk — revisit only if the bridge gains untrusted callers.
- The watchdog's post-relaunch quit arms the close-guard bypass (`gpuFallbackQuit` — the same `exitConfirmed` flag `ConfirmExit` arms, stored in the same breath as the quit): the confirmation modal lives in the very webview being replaced, so an intercepted fallback quit would strand an unanswerable dialog and cancel the auto-recovery, while the sessions the guard protects are auto-resumed in the relaunched instance. A quit that cannot be issued (no Wails context) logs an Error and leaves the bypass unarmed.
- On every Wails upgrade, `window.c` (`SetupWebview`) and `pkg/options/linux/linux.go` are re-verified; if upstream aligns the C switch with the Go enum, the two non-default arms of `wailsWebviewGpuPolicy` are swapped back or dropped and the numeric pins updated to match.

## Known Limitations

- The post-mortem reads only `stderr.log`, never `stderr.old.log`: `crashlog.Install` rotates an oversized stderr.log (past `maxStderrLogBytes`, 4 MiB) to `stderr.old.log` BEFORE the post-mortem runs, so when the log has accumulated past the threshold by the recovery launch, the crashed run's segment rotates away with it and `previousRunDiedOnWaylandProtocolError` finds no previous run to inspect — that launch stays accelerated. The skip self-heals one crash later: the rotation restarts stderr.log small, so the next crash's segment survives into the following start's post-mortem and the fallback persists then. A persistent failure converges in one extra crash cycle; the rotated-away evidence stays readable in stderr.old.log.
- `gpuFailureDetectionApplies` (the shared gate for both detectors — usable capture, fresh auto, no valid env override) carries no platform check: the Linux guard lives in each exported entry point (`newWebviewGpuWatchdog` and `applyWebviewGpuCrashFallback`, which take the platform as an injectable parameter and return early off Linux). A new caller of the shared gate must repeat the Linux check in front of it; the gate itself does not.

## Configuration

| Surface | Default | Meaning |
| --- | --- | --- |
| `C0WRK_WEBVIEW_GPU_POLICY` (env) | unset → fall through to the setting | `always` \| `on-demand` (alias `ondemand`) \| `never`. Linux-only; valid values win over the setting; invalid values Warn and fall through. |
| Settings → General → Webview GPU Acceleration (Linux-only UI) | `auto` | `auto` (recommended) \| `always` \| `on-demand` \| `never`; persisted to webview_state.json, applies on the next start. `GetWebviewGpuPolicy` reports the running policy, the watchdog fallback, and whether a restart is pending. |
| `~/.c0wrk/webview_state.json` | absent → auto, no fallback | `gpu_policy` (the setting) + `auto_fallback` (the watchdog record). Not an operator-editable config surface: a corrupt file degrades silently to the zero state. |

## Extension Points

- New policy values: extend the canonical alphabet in `backend/webview_gpu.go` (`NormalizeWebviewGpuPolicy` + `NormalizeWebviewGpuSetting` + `ResolveWebviewGpuPolicy`) together with the `webviewGpuPolicyFromCanonical` mapping, the `wailsWebviewGpuPolicy` C-mapping and the numeric pins in the desktop test; every accepted value maps to a WebKit policy integer.
- New failure signatures: append to `gpuDmabufFailureSignatures` in `desktop/webview_gpu_watchdog.go`. A miss leaves the grey window up (recoverable via Settings); a false match only falls back to software rendering (also recoverable).
- New pre-DomReady fatal failures (the process dies before any scan): extend `previousRunDiedOnWaylandProtocolError` in `desktop/webview_gpu_postmortem.go` — it only ever looks at the previous run's segment, and only that run's lack of a clean exit banner makes a match actionable.
- New fallback reasons: add a `GpuFallbackReason*` constant in `backend/webview_gpu.go` next to `GpuFallbackReasonDmabufFailure`; the fallback record's reason field carries it to the Settings UI verbatim.
- Platform-scoped variants (e.g. a macOS equivalent): keep `webviewGpuPolicyOptions`'s `goos` switch fail-closed — unknown platforms return nil options — and arm the watchdog behind the same platform gate.

## Related Specs

- [crash-logging.md](crash-logging.md) — the captured stderr.log the watchdog scans, and the sibling env-var escape-hatch pattern (`C0WRK_DISABLE_CRASH_CAPTURE`)
- [087-webview-gpu-auto-policy.md](../decisions/087-webview-gpu-auto-policy.md) — why the default changed from `never` to `auto`
- [018-macos-webview-recovery.md](../decisions/018-macos-webview-recovery.md) - the other platform-specific webview workaround, decision-record form
- [desktop-frontend.md](../contracts/desktop-frontend.md) — the Get/SetWebviewGpuPolicy RPC contract
