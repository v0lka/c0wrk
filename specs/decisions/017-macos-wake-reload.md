# ADR-017: Reload frontend on macOS power-state wake

## Status

Superseded by [ADR-018](018-macos-webview-recovery.md)

## Context

On macOS 26 (Tahoe), after the system or the displays wake from sleep, the
c0wrk window loses all of its rendered controls — the window goes blank while
the application remains alive and shuts down cleanly on close.

Root cause: the WKWebView web content process is suspended/killed by the OS
during display sleep. The Go backend process is unaffected (which is why the
app exits normally), but the webview's rendering surface is invalidated and
never restored. This is a known Wails v2 gap — v2.12.0 does not forward
`webViewWebContentProcessDidTerminate` and provides no built-in recovery. It is
tracked upstream in wailsapp/wails#4592 (webview crash on macOS 26) and
wailsapp/wails#4709 (blank screen after the app is suspended/backgrounded).

Forces:

- The dead web process cannot self-heal from JavaScript — detection must happen
  on the Go/native side.
- The recovery action must be safe and idempotent, since the frontend is
  expected to survive a full reload.

## Decision

Register native macOS observers (`desktop/powerstate_darwin.go`) for both
`NSWorkspaceDidWakeNotification` (system wake) and
`NSWorkspaceScreensDidWakeNotification` (display wake). On either notification,
invoke `wailsRuntime.WindowReloadApp(ctx)` — which re-navigates the webview to
the start URL — to restore the UI.

The recovery is wired in `desktop/startup.go` Phase 6, after `EventBackendReady`,
with a 10-second debounce (a single wake can post both notifications) and a
context-liveness guard (skip if the app is shutting down).

Why this is safe:

- `WindowReloadApp` calls `Window.ExecJS`, which wraps the work in
  `ON_MAIN_THREAD(...)`, so calling it from the NSWorkspace observer block is
  thread-safe.
- A reload re-triggers `OnDomReady`, **not** `OnStartup`; the Go backend
  (database, stores, sessions, running tasks) persists across it.
- The frontend is explicitly designed to survive a full reload:
  `frontend/src/lib/sessionRuntime.ts` reconciles visual state with backend
  runtime status and injects the "A previous task did not finish. You can
  resume it or discard it." banner; `frontend/src/App.tsx` runs an on-mount
  `listProjects()` RPC as a safety net for a missed `backend:ready` event.

The darwin implementation is a build-tag-guarded `//go:build darwin` cgo file;
non-darwin builds get a no-op stub (`desktop/powerstate_other.go`). This is the
first cgo file in c0wrk's own Go tree, but the darwin build already requires
cgo (Wails's native WKWebView via Cocoa), so the toolchain is unchanged and
Linux/Windows builds are untouched.

## Consequences

Positive:

- The UI recovers automatically after display sleep / system wake; no manual
  app restart required.
- No backend state is lost — running tasks, sessions, and the database all
  survive the frontend reload.
- Platform isolation: the Obj-C is confined to one darwin-only file; the rest
  of the codebase is unaffected.

Negative:

- A running task's in-flight streaming UI is interrupted by the reload; the
  user sees the "unfinished task" resume banner. The task itself keeps running
  on the backend.
- cgo is now present in c0wrk's own code (only for darwin). CI must have the
  macOS SDK available for darwin builds — already required by Wails.
- If Wails v2 later gains native `webViewWebContentProcessDidTerminate`
  handling, this wake observer should be re-evaluated to avoid double reloads.

## Alternatives Considered

- **cgo-free heartbeat watchdog**: the frontend pings Go on a timer; Go reloads
  when heartbeats stop. Rejected for the primary sleep case: the web process is
  often still alive (JS keeps pinging) while the render surface is invalidated,
  so the watchdog would consider the app healthy and leave it blank.

- **Native wake observer + heartbeat watchdog**: most robust (also catches
  non-sleep web-process crashes), but more code for a marginal gain. Chosen
  approach can be extended later if needed.
