# System Notifications

## Role

The visual notification channel: turn the SAME session events the sound pipeline cues into native OS banners (macOS Notification Center, Linux D-Bus `org.freedesktop.Notifications`, Windows Action Center), and route a banner click back to the session that raised it. Two channels, one trigger — sound and banners are dispatched side by side from the same event stream (see [sound-notifications.md](sound-notifications.md) for the audible channel).

## Two-channel architecture

```
Backend session event  (session:${sessionId}:<event>)
        │  Wails EventsOn (api/runtime.ts onSessionEvent → batched envelope fan-out)
        ▼
useSoundEvents()  (active session)   /   useBackgroundSessionWatcher  (background)
        │
        ├──▶ classifySessionEvent() → playSound(kind)                 [sound channel]
        │      └── soundStore.enabled? ── no ──▶ silent no-op
        │
        └──▶ classifyNotificationContent() → notifySessionCue         [banner channel]
               ├── redundant? (window focused AND that session on screen) ──▶ suppressed
               └── sendSystemNotification()
                      └── systemNotificationStore.enabled? ── no ──▶ silent no-op
                             └── App.SendSystemNotification (Go binding — the single transport)
                             └── OS notification center
                                    │ user clicks the banner
                                    ▼
                       Go OnNotificationResponse callback (desktop/notifications.go)
                             ├── showWindow (activate FIRST — never depends on JS)
                             └── emit `notification_clicked` {notification_id, session_id, project_id}
                                    ▼
                       useNotificationClicks (App.tsx, mounted once)
                             └── project switch + session select (activate + navigate)
```

Key points:

- **One owner per event, per channel** — the foreground hook handles the active session; the background watcher handles every watched background session. A session never double-cues on either channel (the watcher excludes the active id).
- **Independent master toggles** — `soundStore` (`c0wrk-sound`) and `systemNotificationStore` (`c0wrk-system-notifications`) gate their own channel only; both default to enabled (opt-out).
- **Go-binding transport only** — every runtime notification call routes through `App.InitNotifications` / `App.SendSystemNotification` / `App.ShowTestNotification` / `App.CheckNotificationAuthorization`, never `window.runtime.SendNotification`. The click round-trip is Go-owned: the `data` map sent with a banner is what the Wails click callback returns as `UserInfo`, and only the notification the Go layer registered the callback against emits `notification_clicked`. Split transports would put ids and init bookkeeping in two places.
- **Banner suppression (Telegram rule)** — `notifySessionCue` drops the banner when the window is focused AND the session the banner is about is the one on screen (`isSessionNotificationRedundant`): you are already looking at it, so the banner carries no information. The TONE is never suppressed by this rule — only the visual banner. Covered by the active-session path (focused + active = redundant) and the background-watcher path (its sessions are by definition not the active one, so only the focus half can fire).
- **Focus behavior: always activate.** The Go callback calls `showWindow` before emitting, unconditionally — the window reveal must not depend on webview JS involvement (a busy or reloaded webview may drop the event). `showWindow` is a reveal-AND-raise on every platform (see [Click → window activation](#click--window-activation)). Navigation, by contrast, is a frontend concern and no-ops for unattributed/unknown banners.

## Key Files

- `desktop/notifications.go` — the Go bridge: `InitNotifications` (memoized, single `OnNotificationResponse` callback, macOS authorization prompt), `SendSystemNotification` (the transport; unique `c0wrk-notification-*` ids; routes through the platform hook below on Linux), `CheckNotificationAuthorization` (non-prompting permission read for the Settings hint), `ShowTestNotification` (Settings preview, no routing data), `cleanupNotifications` (Shutdown; closes c0wrk's own Linux D-Bus connection AND the Wails notification service).
- `desktop/notifications_linux.go` / `desktop/notifications_notlinux.go` — the platform send hook: on Linux c0wrk's own icon-augmented `org.freedesktop.Notifications` transport (see [Notification icon](#notification-icon)); on other platforms a stub straight to the Wails runtime.
- `desktop/notifications_icon.go` + `desktop/icon/appicon.png` — the embedded application icon (byte-identical copy of `build/appicon.png`, guarded by a drift test — `go:embed` cannot reference files above the package dir).
- `frontend/src/api/notifications.ts` — the single import path for the Go transport: `initSystemNotifications`, `sendSystemNotification`, `showTestNotification`, `checkNotificationAuthorization`, `onNotificationClicked` (payload-validated `notification_clicked` subscription with drop reporting).
- `frontend/src/lib/systemNotifications.ts` — the pure event→content mapping `classifyNotificationContent()` and the best-effort send path (`sendSystemNotification(content, context)` — store gate + `isWailsReady` gate + warn-and-swallow).
- `frontend/src/hooks/useNotificationClicks.ts` — click navigation, mounted once at the app root.
- `frontend/src/stores/systemNotificationStore.ts` — the master `enabled` toggle (persisted at `c0wrk-system-notifications`, default enabled).
- `frontend/src/components/settings/SystemNotificationSettings.tsx` — settings UI (General tab, under Sound): toggle + lazy permission probe + banner-lifetime selector (Linux only) + test button.
- `backend/config/config.go` (`NotificationsConfig`) + `backend/config/defaults.go` — the `notifications.banner_timeout_seconds` setting and its pointer-int default; `backend/frontend_api_config.go` exposes it as `GetNotificationBannerTimeout` / `SetNotificationBannerTimeout`. Documented for operators in `config.example.yaml`.
- `frontend/src/hooks/sessionSoundCoverage.test.tsx` — pins banner coverage for all 9 cued events (both active and background paths, disjoint ownership, malformed-payload silence).
- `desktop/notifications_test.go` — pins the Go-side callback/transport/authorization behavior.
- `desktop/window_activation_test.go` — pins the per-platform click activation matrix (Linux pager-then-fallback, darwin show-only, Windows guarded restore).
- `desktop/window_activation_linux.go` / `desktop/window_activation_notlinux.go` — the Linux EWMH pager-source window activation (`x11ActivateOwnWindow`): one long-lived private X connection (`x11Conn`), own-window discovery (`_NET_CLIENT_LIST` + `_NET_WM_PID`, preferring `_NET_WM_WINDOW_TYPE_NORMAL`), fresh server timestamp, `_NET_ACTIVE_WINDOW` source=2 + focus takeover; fail-soft fallback to the Wails present() path. Non-Linux stub returns false.
- `desktop/window_activation_linux_live_test.go` — the opt-in live test (`C0WRK_X11_ACTIVATE_LIVE=1`) running the real activation against the running WM (skips in CI/plain `go test`).

## Event → content mapping

`classifyNotificationContent(event, data)` is the banner counterpart of `classifySessionEvent` — the same nine events, the same `task_complete` success disambiguation, but each event yields its own title/body pair (a banner must say WHAT needs attention, not just that something does). Bodies clip at 200 chars with an ellipsis.

| NotificationKind | Session events | Title / body |
| ---------------- | -------------- | ------------ |
| `success` | `task_complete` (payload `success !== false`) | "Task completed" / task output |
| `attention` | `ask_user` | "Your input is needed" / the agent is waiting |
| `attention` | `step_limit` | "Step limit reached" / waiting for a decision |
| `attention` | `tool_confirm` | "Tool approval required" / a tool call needs confirmation |
| `attention` | `plan_review_ready` | "Plan ready for review" / a plan awaits approval |
| `attention` | `task_failed_resumable` | "Task failed — resumable" / failure message |
| `attention` | `goal_proposal` | "Goal proposal ready" / a proposal needs review |
| `error` | `error` | "Task error" / error message |
| `error` | `task_cancelled` | "Task cancelled" / the task was cancelled |
| `error` | `task_complete` (payload `success === false`) | "Task failed" / task output or failure note |
| *(silent)* | everything else (e.g. `session_paused`, `session_resumed`) | returns `null`; no banner |

Titles are composed at the single dispatch site (`notifySessionCue` in `frontend/src/hooks/events/useSoundEvents.ts`): `"<event title> — <session name>"` — the event leads (it is the scannable part), the session name follows, falling back to `"c0wrk"` when the session is unknown (`resolveSessionNotificationLabel`). Example: `"Tool approval required — my-session"`.

## Click → window activation

A banner click must bring the c0wrk window forward, not merely un-hide it. `desktop/app.go showWindow` implements the reveal-AND-raise per platform (verified against the Wails v2.15 frontends, `internal/frontend/desktop/*`):

| Platform | Calls | Why |
| -------- | ----- | --- |
| Linux | **c0wrk's own EWMH pager-source activation** (`x11ActivateOwnWindow`, `desktop/window_activation_linux.go`); on ANY failure → `WindowUnminimise` | `WindowUnminimise` is `gtk_window_present`, which sends the EWMH activation request with the timestamp of the application's **last user interaction** (0 when there was none) — from a non-focused application that is exactly what focus stealing prevention (KWin's default policy, likewise on other EWMH WMs) rejects: the taskbar entry merely flashes ("demands attention") instead of the window coming forward. The own transport opens a private X connection, locates this process's window in the root's `_NET_CLIENT_LIST` by `_NET_WM_PID` (preferring the `_NET_WM_WINDOW_TYPE_NORMAL` window — a Wails/GTK process owns more than one X window carrying its PID, e.g. the splash), obtains a fresh server timestamp via a PropertyNotify roundtrip (the `gdk_x11_get_server_time` technique), and activates with `_NET_ACTIVE_WINDOW` source indication 2 (pager — taskbar-click semantics, honored against focus stealing prevention) + `XMapRaised` + `XSetInputFocus` + `XRaiseWindow` (the empirically verified xdotool `windowactivate` sequence). Fail-soft everywhere: no `DISPLAY`/Wayland/no matching window → `false` → the Wails present() fallback still runs, so activation never regresses below the pre-fix behavior. |
| macOS | `WindowShow` only | `makeKeyAndOrderFront` + `activateIgnoringOtherApps` — already the full activation; `deminiaturize` adds nothing. |
| Windows | `WindowShow`, then `WindowUnminimise` only when `WindowIsMinimised` | `ShowWindow` maps + activates; the restore step brings the window back from the taskbar — but WPF `Form.Restore()` also UNMAXIMIZES a maximized window, so the `IsMinimised` guard is load-bearing. |

### X connection lifetime

`x11Conn` is opened once, on first activation, and lives for the process — the activation path never calls `XCloseDisplay`. `XCloseDisplay` performs a synchronous round trip, and a window manager holding an X server grab blocks it indefinitely; KWin takes that grab for the un-minimize animation the activation request itself triggers, so a per-call open/close could block forever inside the deferred close. Because `App.notificationCallback` runs the activation, a blocked close stalls whatever goroutine dispatched it — and on Linux that used to be the godbus signal pump, whose stall made godbus discard every later `ActionInvoked` and killed notification clicks for the rest of the run. Two guarantees keep that failure mode closed: the connection is never closed, and the callback never runs on the pump goroutine (see [Click routing](#click-routing)).

The discovered window id is cached (`x11TargetWindow`). A repeat activation re-validates the cache with one property read — the window is still in `_NET_CLIENT_LIST` and still carries this process's `_NET_WM_PID`, the second check covering an XID the server has recycled to another client — and walks every managed window only on a miss. The walk costs up to two property reads per window on the display, and a notification click runs one activation.

`x11ActivationMu` is taken with `TryLock`, never `Lock`: an activation already in flight may be blocked inside Xlib, and queueing behind it would spread the stall to the caller. A contended activation returns `false` and takes the Wails present() fallback.

The X error swallower (`x11_install_swallow`) is installed exactly once per process. `XSetErrorHandler` is process-global and returns the handler it replaced, so re-installing per call makes the saved "previous" handler the swallower itself — an error on any display other than the private one then recurses until the thread's stack is gone.

The activation order in the Go callback stays reveal-first: `showWindow` runs BEFORE `notification_clicked` is emitted, so the raise never depends on webview JS involvement. The branch selection is a package var (`showWindowPlatform`) with per-call seams (`x11PagerActivateFn` / `windowRaiseFn` / `windowUnminimiseFn` / `windowIsMinimisedFn`), so the whole matrix is unit-testable on any platform (`desktop/window_activation_test.go`); the real `x11ActivateOwnWindow` against a live WM is covered by the opt-in live test `TestX11ActivateOwnWindowLive` (`C0WRK_X11_ACTIVATE_LIVE=1`, skips in CI/plain `go test`).

## Click routing

`useNotificationClicks` (mounted once in `App.tsx`) follows the live-sessions radar's exact pattern:

1. Go activates the window (`showWindow` — reveal + raise + focus, see [Click → window activation](#click--window-activation)) **before** emitting `notification_clicked`.
2. Empty `session_id` (the Settings preview banner) → logged no-op (focus-only click).
3. Resolve the owning project: the payload's `project_id` first; otherwise the global session snapshot, with one immediate `refreshNow()` retry when the session is not found.
4. Unknown session (even after the refresh) → logged no-op.
5. Known session: `switchProjectWithState(projectId)` when the project differs (restores that project's UI state), then `selectSession(sessionId, projectId)`. A failed switch surfaces its own toast and selects nothing.

The Linux transport dispatches `App.notificationCallback` on its own goroutine, never inline on the godbus signal pump: the callback activates the window, which makes blocking X round trips, and a stalled pump makes godbus silently discard every subsequent signal — one slow activation would otherwise disable notification clicks until restart. Both the `ActionInvoked` and the reason-2 `NotificationClosed` handlers dispatch this way.

The callback travels with the pump (a `pumpSignals` parameter threaded into the handlers) instead of living on the shared transport state. A redial — teardown after a failed `Notify`, then the next send — would otherwise rewrite that state while the previous pump, signalled by `cancel()` but not yet stopped, is still reading it to route a signal.

Known Linux quirk: both the Wails transport and c0wrk's own map reason-2 `NotificationClosed` (the banner's X) to the same `DEFAULT_ACTION` identifier, so an explicit dismiss can navigate too — indistinguishable at the identifier level. Timeout/programmatic closes never fire the callback.

## Notification icon

Wails v2 (through v2.16) has no icon field in `NotificationOptions` and hard-codes the D-Bus `app_icon` argument to `""` on Linux, so each platform resolves the banner icon differently:

| Platform | Icon source | Transport |
| -------- | ----------- | --------- |
| macOS | the app bundle icon (automatic) | Wails runtime |
| Windows | the app icon Wails extracts and registers under the AppUserModelId for toasts (automatic) | Wails runtime |
| Linux | c0wrk's own transport (below) | `desktop/notifications_linux.go` |

On Linux, `SendSystemNotification` routes through `sendNotificationPlatform`: a c0wrk-owned `org.freedesktop.Notifications` D-Bus call mirroring the Wails frontend's, but with `app_icon` populated. The icon argument resolves fail-soft, first match wins:

1. **Embedded PNG → `file://` URI** — `desktop/icon/appicon.png` is embedded in the binary (byte-identical to `build/appicon.png`, drift-guarded by test) and exported on first use to `<UserCacheDir>/c0wrk/notification-icon.png` (mode 0644 — readable by service-user daemons), passed as a percent-encoded `file://` URI. Works in `wails dev`, standalone binaries, and daemon configurations with no theme awareness.
2. **Theme name** (`"c0wrk"`) — when the cache dir is unwritable; packaged installs (AUR) ship `/usr/share/icons/hicolor/512x512/apps/c0wrk.png`, so theme-resolving daemons still find the icon.
3. **Wails fallback** — any D-Bus failure (no session bus, daemon error, dead connection) drops the connection, logs a warning, and re-sends through the Wails transport: icon-less but delivered, with the identical click path.

The `Notify` call also carries the freedesktop `desktop-entry` hint (`"c0wrk"`), which binds the banner to the installed `c0wrk.desktop`: grouping, the source name, the per-application entries in the desktop's own notification settings, and — on daemons that prefer it over `app_icon` — the icon. A system without that `.desktop` file leaves the hint unresolvable and daemons fall back to `app_name`/`app_icon`, so sending it is never worse than omitting it.

At dial time the transport probes the daemon's `GetCapabilities` once. A daemon that does not advertise `actions` ignores the action list every send carries, so `ActionInvoked` never arrives and clicking a banner does nothing — banners appear, clicks silently fail. The probe turns that into a warning naming the capabilities the daemon did report. It is diagnostic only: any probe failure is a debug line, never a failed send.

Click routing coexists with the Wails transport without double delivery: each notification is tracked by exactly one side's pending map (ours for our sends, Wails' for fallback sends), and both funnel into the same `App.notificationCallback`. Our signal handler subscribes to `ActionInvoked`/`NotificationClosed`, maps the `default` action (and close reason 2, the same dismiss quirk as Wails) to `NotificationResult{ActionIdentifier: "DEFAULT_ACTION"}`, and ignores foreign ids. A failed send tears the connection down so the next send redials. `Shutdown` closes our connection before the Wails cleanup.

The pending map is bounded by `prunePendingLocked`, run on every send: entries older than 24h are dropped, and the map is capped at 256 by evicting the oldest. The bound is load-bearing rather than defensive — an entry is normally consumed by `ActionInvoked` or `NotificationClosed`, but a daemon that retires a banner silently reports neither, leaving its entry behind for the life of the process.

## Banner lifetime

`notifications.banner_timeout_seconds` (config.yaml) sets how long a delivered banner stays on screen. It becomes the freedesktop `expire_timeout` argument of the Linux `Notify` call, resolved by `App.notificationExpireTimeoutMs` and passed into `sendNotificationPlatform`:

| Value | `expire_timeout` | Meaning |
| ----- | ---------------- | ------- |
| `-1` (default) | `-1` | The notification daemon applies its own lifetime. |
| `0` | `0` | The banner never expires — it stays until clicked or dismissed. |
| `1`…`86400` | seconds × 1000 | An explicit lifetime. |

The field is a pointer-int (`*int`) because `0` is a meaningful value here, so the Go zero value cannot double as "unset"; `ApplyDefaults` fills an absent key with `-1`. `GetNotificationBannerTimeout` answers `-1` whenever the config is unreachable — the resolution never falls back to `0`, which would leave every banner on screen forever.

Only `SetNotificationBannerTimeout` range-checks its input, so a hand-edited config.yaml reaches `notificationExpireTimeoutMs` unvalidated; the conversion clamps there and logs every clamp. The upper clamp is load-bearing rather than cosmetic: the seconds → milliseconds multiply overflows `int32` from roughly 2.15e6 seconds up, so a units mix-up (`banner_timeout_seconds: 3600000`, milliseconds written into a seconds field) would otherwise wrap to a negative `expire_timeout` that is neither the `-1` sentinel nor a valid lifetime. Values below `-1` resolve to the daemon default.

Linux only: `sendNotificationPlatform` on macOS/Windows accepts the value for signature parity and ignores it, because those notification centers own banner lifetime themselves and expose no per-notification expiry to the sender. The Settings control is hidden on those platforms rather than shown as an inert knob.

`0` exists because banner expiry is not observable: a daemon is free to retire a banner with no `NotificationClosed` signal and without keeping it in the notification history (KDE Plasma does exactly this), so a user who misses the popup has no way back to it. See also the routing-map bound in [Notification icon](#notification-icon).

## Settings UI

`SystemNotificationSettings` (General tab, directly under `SoundSettings`, same `Toggle` primitive and `border-t border-border pt-4` wrapper):

- **Master toggle** → `systemNotificationStore.setEnabled`. On enable: fire-and-forget `initSystemNotifications()` (idempotent) + one preview banner ("c0wrk — System notifications enabled"). On disable: no teardown — the OS retires delivered banners on its own schedule; future sends are gated off by the store.
- **Permission hint** (macOS-only concern): on section mount, `checkNotificationAuthorization()` is probed once (lazy — the General tab pays for it, not app startup). `false` renders a muted hint ("Notifications are disabled for c0wrk in macOS Settings — enable them to receive alerts") and suppresses the preview/test banner. Linux/Windows always grant, so no hint renders there. The probe fails open (a transport error resolves to granted) so a transient RPC failure never paints a misleading hint.
- **Banner lifetime** (Linux only, shown while enabled) → a segmented selector over Default / 10s / 30s / 1m / Never, reading `GetNotificationBannerTimeout` on mount and writing `SetNotificationBannerTimeout` on click (see [Banner lifetime](#banner-lifetime)). The write is optimistic and rolls the selection back when the RPC rejects, so the control never shows a value the backend did not accept. Host detection is `navigator.platform`/`userAgent`; on macOS and Windows the control is absent.
- **Test button** ("Send test notification", shown while enabled and authorized) → `showTestNotification()` — the Go-authored banner demonstrating the full click round-trip (focus without navigation).

## Diagnostics

A banner that never appears has three very different causes that all used to look identical in the logs — like silence. These two lines tell them apart, both at `DEBUG`:

| Log evidence | Reading |
| ------------ | ------- |
| `Wails EventsEmit called` for the session event, then `system notification sent` | The whole app-side path worked. A missing banner is then the daemon's doing (do-not-disturb, per-application settings, a dropped popup) — check the wire with `dbus-monitor --session "interface='org.freedesktop.Notifications'"`. |
| the event line, but NO `system notification sent` | The frontend never asked for a banner: the master toggle is off, or `isSessionNotificationRedundant` suppressed it (window focused AND that session on screen). |
| neither | The event never reached the cue hooks — a listener-coverage problem, not a notification one. |

`system notification sent` carries the notification id, the session id and the resolved `expire_timeout`. It deliberately carries neither title nor body: a banner body is task output, and SECURITY.md extends the no-secrets rule to every output channel.

Two more lines bound the transport's health, once per run each: `system notifications initialized` (init) and `notification daemon capabilities` (the dial-time capability probe, which warns instead when the daemon does not advertise `actions`). A `linux D-Bus notification transport failed` warning means the send fell back to the Wails transport.

## Error Handling

- **Best-effort contract** — no failure path throws into the event pipeline. `sendSystemNotification` catches and logs at warn; init failure is logged and left enabled (individual sends fail softly).
- **No Wails runtime** (vitest, SSR, `make dev-frontend`) — `isWailsReady()` gates init/send into silent no-ops; `getApp()` never throws.
- **Authorization** — a denied macOS authorization makes init count as successful (logged at info); the denial surfaces through the Settings hint instead of an error.

## Invariants

- **Listener/cue coverage** — every session with a running backend task has a listener that cues BOTH channels: the active session via `useSoundEvents`, every watched background session via `useBackgroundSessionWatcher`. The switch-time corrector is `useTaskFlagRestore` alone; the snapshot refresh fires on mount, live-set changes, every project/session switch, and the visible-window safety poll (see [sound-notifications.md](sound-notifications.md) § Listener-coverage invariant — the same invariant covers both cues).
- All banner runtime calls go through the Go bindings — never `window.runtime.SendNotification`.
- The Go click callback activates the window (reveal + raise + focus) before emitting, unconditionally.
- A banner about the focused window's active session is suppressed (Telegram rule); the tone never is.
- Exactly one `OnNotificationResponse` callback is registered per app run (init is memoized; a failed init is retried but never double-registers).
- Banner ids are unique within a process (`c0wrk-notification-<GOOS>-<timestamp>-<seq>`).
- `notification_clicked` payloads are validated (`isNotificationClickedData`); malformed ones are dropped and reported, never dispatched.
- The Linux icon transport is fail-soft: a banner is never lost to an icon/export/D-Bus failure (fallback to the Wails transport), and exactly one side tracks each notification (no double delivery, no orphan clicks).
- The Linux click callback runs on its own goroutine, leaving the godbus signal pump free to keep reading — window activation and signal delivery never share a goroutine.
- The Linux routing map stays bounded (24h TTL, 256 entries) however many banners a daemon retires without a signal.
- The private X activation connection is opened once per process and stays open; activations serialize through `TryLock`, so a stalled activation is skipped rather than queued behind.
- The resolved banner lifetime falls back to the daemon default (`-1`) whenever the config is unreachable, never to "never expires" (`0`), and every value reaching the D-Bus call is within `[-1, 86400]` seconds however the config was edited.
- Each signal pump routes to the callback it was started with, so the transport holds no dispatch state a redial could overwrite.
- The cached activation target is used only while it is still a managed window carrying this process's pid; otherwise it is discarded and rediscovered.

## Known Limitations

- The webview reload (macOS wake/termination recovery) discards frontend module state — the memoized init promise included — but the Go-side init and callback survive it; the next `initSystemNotifications()` re-runs harmlessly (backend memoization makes it a no-op).
- Linux banner-dismiss navigates like a click (the Wails reason-mapping quirk above).
- No per-event granularity: the master toggle governs all nine cued events (mirroring the sound channel's single-switch design).
- Banner expiry is invisible to the app: a daemon may retire a banner without emitting `NotificationClosed`, so a banner the user never acted on leaves no trace — `banner_timeout_seconds: 0` is the way to keep such a banner reachable.
- A banner sent with `banner_timeout_seconds: 0` stays on screen after the user handles the request inside the app; c0wrk never calls `CloseNotification`, so stale attention banners are dismissed by hand.
- The X11 activation transport is X11-only by construction: on a Wayland session `XOpenDisplay` fails → the fail-soft `WindowUnminimise` fallback runs (GTK/`gtk_window_present` may still be honored by the compositor depending on its focus policy). A native Wayland activation path would require the foreign-toplevel-management protocol and is not implemented.
- On a Wayland session that also runs XWayland, `XOpenDisplay` succeeds and the connection is kept for the process even though the app's toplevel is a Wayland surface that can never appear in `_NET_CLIENT_LIST`. Each activation then costs one property read before falling back. The cache never fills, so the per-window walk is not paid; closing the connection instead is not an option (see [X connection lifetime](#x-connection-lifetime)).
- A notification daemon without the `actions` capability never emits `ActionInvoked`, so on such a desktop only dismissing a banner routes, never clicking it. The dial-time probe reports this at warn rather than working around it.
- The pager-source activation is, by EWMH semantics, a "taskbar click" — a user actively typing in another window at that exact moment keeps their focus (the WM resolves the race; the window still raises).

## Related Specs

- [sound-notifications.md](sound-notifications.md) — the audible channel; the shared listener-coverage invariant and the foreground/background ownership model.
- [stores.md](stores.md) — `systemNotificationStore` (the persisted master toggle).
- [events.md](events.md) — event subscription; `useSoundEvents` and the background watcher are composed by `useSessionEvents`.
- [../../contracts/event-catalog.md](../../contracts/event-catalog.md) — the `notification_clicked` global event.
