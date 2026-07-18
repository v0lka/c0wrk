# ADR-018: macOS webview recovery (native process-death hook + deferred wake reload)

## Status

Accepted

## Context

On macOS 26 (Tahoe), the c0wrk window blanks after the system or the displays
wake from sleep. Two distinct failure modes underlie the symptom:

1. **Process suspension** — the WKWebView's rendering surface is invalidated
   while the web-content process stays alive. The Go backend is unaffected,
   so the app shuts down cleanly on close, but the window never repaints.
2. **Process death** — the OS kills the web-content process during resume
   (memory ceiling, crash, or OS-initiated termination). The renderer is
   gone entirely.

Wails v2.12.0 does not forward
`-webViewWebContentProcessDidTerminate:` — the sole `WKNavigationDelegate`
hook WebKit invokes when the web-content process dies. A killed renderer
therefore leaves the window blank with no recovery and no log; the death is
silent. This is tracked upstream in wailsapp/wails#4592 (webview crash on
macOS 26) and wailsapp/wails#4709 (blank screen after the app is
suspended/backgrounded).

ADR-017 was a first attempt: a native wake observer calling
`wailsRuntime.WindowReloadApp(ctx)` inline on
`NSWorkspaceDidWakeNotification` /
`NSWorkspaceScreensDidWakeNotification`. Two defects surfaced in practice:

- **Inline reload races OS resume.** The observer block runs synchronously on
  the main thread mid-resume, while the OS is still restoring the
  web-content process. Calling `WindowReloadApp` synchronously at that point
  races the OS's own process restoration and silently kills the app.
- **Wake-only misses death.** The wake observer catches only suspension. It
  does not detect or recover process death — and a dead process cannot be
  revived by re-navigation triggered from a path that never fires when the
  process is merely killed outside a sleep cycle.

A pre-existing numbering collision exists: two ADR-017 files were created —
`017-goal-mode.md` (the active goal-mode decision, unrelated) and
`017-macos-wake-reload.md` (this recovery's first attempt). The retired
`017-macos-wake-reload.md` keeps its number for traceability; superseded ADRs
are never renumbered. ADR-018 continues the sequence.

Forces:

- Web-content-process death cannot self-heal from JavaScript — the JS
  runtime is gone. Detection and recovery must happen on the native side.
- The recovery must distinguish death (needs process relaunch) from
  suspension (needs render-surface refresh).
- The wake observer callback runs on the main thread during OS resume; any
  synchronous work there races the OS's own process restoration.
- The recovery must be safe during app teardown (never reload a torn-down
  webview) and idempotent (the frontend survives a full reload).

## Decision

Two complementary recovery paths, each matched to a failure mode.

### 1. Native process-death hook — `desktop/powerstate_darwin.go`

A build-tag-guarded (`//go:build darwin`) cgo file attaches the
`-webViewWebContentProcessDidTerminate:` selector — the one
`WKNavigationDelegate` method WebKit invokes when the web-content process
dies (crash, memory ceiling, or OS-initiated kill) and that Wails v2.12.0
omits — to the real `WailsContext` class by **runtime method injection**, not
a compile-time Objective-C category.

- **Runtime injection — `c0wrkInstallWebviewRecovery`.** Resolves the class
  by name via `objc_getClass("WailsContext")` and adds the method via
  `class_addMethod` with a C-function IMP (`c0wrkWebContentProcessDidTerminateIMP`).
  `registerPowerWakeObserver` calls it during Startup, after Wails has loaded
  `WailsContext` and installed it as the webview's navigation delegate.
  `class_addMethod` mutates the class's method table, which the runtime
  consults at every message dispatch, so the hook takes effect on the
  already-created instance immediately. A `class_getInstanceMethod` pre-check
  defers to Wails's own implementation if a future Wails version implements
  the selector natively (returns status 2; status 1 = class not found, hook
  skipped with the wake path as fallback; status 0 = installed).
- **Why runtime injection, not a compile-time category.** A compile-time
  `@implementation WailsContext (Category)` emits a reference to the
  `_OBJC_CLASS_$_WailsContext` link symbol. That class is implemented in
  Wails's `internal/frontend/desktop/darwin/WailsContext.m`, which is linked
  **only** by `wails build` (via the generated main) — never by plain
  `go build ./...` / `go test ./...`. A category therefore leaves the symbol
  unresolved under the standard Go toolchain and breaks macOS CI
  (`.github/workflows/ci.yml` runs `go test ./...` on a macos runner).
  Runtime injection references the class by a name string at runtime,
  emitting no link-time class symbol, so the desktop package links cleanly
  under `go build` / `go test` on darwin while Wails's real class still
  receives the hook. A minimal forward `@interface WailsContext` declares
  only the accessors the IMP touches (`shuttingDown`, `webview`); Objective-C
  property access compiles to dynamic `objc_msgSend` sends, so no class
  symbol is referenced. MRC-friendly property attributes (`assign`/`retain`)
  match Wails's own non-ARC darwin sources.
- **IMP behavior.** (a) Hard-gates on `shuttingDown` (never revive a webview
  the app is tearing down). (b) Debounces via `c0wrkLastRecoveryTime`
  (`c0wrkRecoveryDebounceSeconds = 3.0`) to collapse a burst of terminations
  into a single reload. (c) Surfaces the death through the
  `c0wrkLogWebviewRecovery` cgo export so the previously-silent termination is
  logged. (d) `dispatch_async`s `-[WKWebView reload]` to the main queue,
  re-checking `shuttingDown` inside the block in case teardown began between
  the gate and the dispatch.
- **`reload` over `evaluateJavaScript`.** `-[WKWebView reload]` relaunches
  the web-content process and re-fetches the `wails://` start URL.
  `WindowReloadApp` / `evaluateJavaScript` cannot revive a dead process —
  the JS runtime is gone — so the delegate path must use `reload` directly
  on the webview rather than routing through Wails's JS-based reload.

### 2. Deferred wake reload — `desktop/startup.go` (Phase 6)

The wake observer (`NSWorkspaceDidWakeNotification` +
`NSWorkspaceScreensDidWakeNotification`) is preserved, but the reload is
deferred out of the synchronous observer block.

- **`wakeReloadDelay` (1500ms)** moves the reload past the OS's resume
  window. The delay is essential: calling `WindowReloadApp` inline inside
  the wake observer races the OS's mid-resume web-content process and
  silently kills the app.
- **`deferredWakeReload`** returns immediately to the caller (it spawns a
  goroutine), so the observer callback never blocks. It captures
  `ctx := a.ctx`, waits via `time.NewTimer(wakeReloadDelay)`, and `select`s
  between `<-ctx.Done()` (cancel — app is shutting down) and `<-timer.C`. It
  re-checks `ctx.Err()` once more after the wait, so a shutdown that began
  during the delay never triggers a reload of a torn-down context. Only then
  does it call `wailsRuntime.WindowReloadApp(ctx)`.
- The 10-second `lastWake` debounce (a single wake posts both notifications)
  is preserved in the observer callback and is independent of the 1500ms
  delay.

### Dual-trigger design

- The delegate path (`-webViewWebContentProcessDidTerminate:`) handles
  process **death**.
- The wake path handles process **suspension** (alive but blank render
  surface) — the case ADR-017 originally described.
- If the process died, the delegate has already reloaded via
  `-[WKWebView reload]`; the deferred `WindowReloadApp` is then a harmless
  re-navigation to the same URL.

The darwin implementation is build-tag-guarded; non-darwin builds get a
no-op stub (`desktop/powerstate_other.go`). This adds cgo to c0wrk's own Go
tree (darwin-only), but the darwin build already requires cgo for Wails's
native WKWebView, so the toolchain is unchanged and Linux/Windows builds are
untouched.

## Consequences

Positive:

- Both failure modes recover automatically: process death (native hook) and
  process suspension (deferred wake reload). No manual app restart required.
- Process death is now logged (`c0wrkLogWebviewRecovery`) — previously
  silent.
- No backend state is lost — running tasks, sessions, and the database all
  survive the frontend reload / re-navigation.
- Platform isolation: the Obj-C is confined to one darwin-only file; the
  rest of the codebase is unaffected.

Negative:

- A running task's in-flight streaming UI is interrupted by the reload; the
  user sees the "unfinished task" resume banner. The task itself keeps
  running on the backend.
- cgo is now present in c0wrk's own code (darwin-only). CI must have the
  macOS SDK available for darwin builds — already required by Wails.
- Two recovery paths can both fire on a single sleep/wake cycle (death +
  wake). The dual-trigger design makes this harmless (the second is a no-op
  re-navigation), but it adds reasoning surface.
- If Wails v2 later gains native `webViewWebContentProcessDidTerminate`
  handling, the installer's `class_getInstanceMethod` pre-check detects it
  and defers (status 2), so there is no method conflict or double reload;
  the hook can then be removed.

## Alternatives Considered

- **Compile-time Objective-C category `WailsContext (C0wrkRecovery)`.** The
  natural first attempt. Rejected: an `@implementation WailsContext (Category)`
  emits a reference to the `_OBJC_CLASS_$_WailsContext` link symbol, which is
  defined in Wails's `internal/frontend/desktop/darwin/WailsContext.m` and
  linked only by `wails build`. Under the standard Go toolchain
  (`go build ./...`, `go test ./...`) the symbol is unresolved and the darwin
  link fails — a macOS-CI-blocking regression. Runtime method injection
  (`objc_getClass` + `class_addMethod`) attaches the same selector to the same
  class with no link-time class reference, at the cost of one indirection at
  install time.

- **ADR-017 inline `WindowReloadApp` on wake.** The original approach.
  Rejected because (a) it races the OS's mid-resume process restoration and
  silently kills the app, and (b) it catches only suspension, not death.
  Superseded by this ADR.

- **cgo-free heartbeat watchdog** (carried over from ADR-017). The frontend
  pings Go on a timer; Go reloads when heartbeats stop. Rejected for the
  primary sleep case: the web process is often still alive (JS keeps pinging)
  while the render surface is invalidated, so the watchdog considers the app
  healthy and leaves it blank. It also cannot revive a dead process via JS.

- **Evaluate JavaScript to detect or recover death.** Impossible — a dead
  web-content process has no JS runtime to evaluate.

- **Single path (delegate only).** Would miss the suspension case where the
  process is alive but the render surface is blank (no
  `webViewWebContentProcessDidTerminate:` fires). The wake path is still
  needed.

- **Single path (wake only).** Would miss non-sleep process crashes (memory
  ceiling, WebKit-initiated kill outside a sleep/wake cycle) and races the
  OS resume window when run inline.

## Related

- Supersedes [ADR-017 (macOS wake reload)](017-macos-wake-reload.md).
- A pre-existing numbering collision means two ADR-017 files exist:
  [017-goal-mode.md](017-goal-mode.md) (active, unrelated) and
  [017-macos-wake-reload.md](017-macos-wake-reload.md) (superseded by this
  ADR). The retired file keeps its number for traceability; superseded ADRs
  are never renumbered.
- Upstream tracking: wailsapp/wails#4592 (webview crash on macOS 26),
  wailsapp/wails#4709 (blank screen after suspend/background).
