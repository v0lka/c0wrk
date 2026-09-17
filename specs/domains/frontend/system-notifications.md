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
        └──▶ classifyNotificationContent() → sendSystemNotification() [banner channel]
               └── systemNotificationStore.enabled? ── no ──▶ silent no-op
                      └── App.SendSystemNotification (Go binding — the single transport)
                             └── OS notification center
                                    │ user clicks the banner
                                    ▼
                       Go OnNotificationResponse callback (desktop/notifications.go)
                             ├── showWindow (focus FIRST — never depends on JS)
                             └── emit `notification_clicked` {notification_id, session_id, project_id}
                                    ▼
                       useNotificationClicks (App.tsx, mounted once)
                             └── project switch + session select (focus + navigate)
```

Key points:

- **One owner per event, per channel** — the foreground hook handles the active session; the background watcher handles every watched background session. A session never double-cues on either channel (the watcher excludes the active id).
- **Independent master toggles** — `soundStore` (`c0wrk-sound`) and `systemNotificationStore` (`c0wrk-system-notifications`) gate their own channel only; both default to enabled (opt-out).
- **Go-binding transport only** — every runtime notification call routes through `App.InitNotifications` / `App.SendSystemNotification` / `App.ShowTestNotification` / `App.CheckNotificationAuthorization`, never `window.runtime.SendNotification`. The click round-trip is Go-owned: the `data` map sent with a banner is what the Wails click callback returns as `UserInfo`, and only the notification the Go layer registered the callback against emits `notification_clicked`. Split transports would put ids and init bookkeeping in two places.
- **Focus behavior: always show.** The Go callback calls `showWindow` before emitting, unconditionally — the window reveal must not depend on webview JS involvement (a busy or reloaded webview may drop the event). Navigation, by contrast, is a frontend concern and no-ops for unattributed/unknown banners.

## Key Files

- `desktop/notifications.go` — the Go bridge: `InitNotifications` (memoized, single `OnNotificationResponse` callback, macOS authorization prompt), `SendSystemNotification` (the transport; unique `c0wrk-notification-*` ids), `CheckNotificationAuthorization` (non-prompting permission read for the Settings hint), `ShowTestNotification` (Settings preview, no routing data), `cleanupNotifications` (Shutdown; closes the Linux D-Bus connection).
- `frontend/src/api/notifications.ts` — the single import path for the Go transport: `initSystemNotifications`, `sendSystemNotification`, `showTestNotification`, `checkNotificationAuthorization`, `onNotificationClicked` (payload-validated `notification_clicked` subscription with drop reporting).
- `frontend/src/lib/systemNotifications.ts` — the pure event→content mapping `classifyNotificationContent()` and the best-effort send path (`sendSystemNotification(content, context)` — store gate + `isWailsReady` gate + warn-and-swallow).
- `frontend/src/hooks/useNotificationClicks.ts` — click navigation, mounted once at the app root.
- `frontend/src/stores/systemNotificationStore.ts` — the master `enabled` toggle (persisted at `c0wrk-system-notifications`, default enabled).
- `frontend/src/components/settings/SystemNotificationSettings.tsx` — settings UI (General tab, under Sound): toggle + lazy permission probe + test button.
- `frontend/src/hooks/sessionSoundCoverage.test.tsx` — pins banner coverage for all 9 cued events (both active and background paths, disjoint ownership, malformed-payload silence).
- `desktop/notifications_test.go` — pins the Go-side callback/transport/authorization behavior.

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

Titles are prefixed at the send site with the resolved session name: `"<session> — Task completed"`, falling back to `"c0wrk — Task completed"` when the session is unknown (`resolveSessionNotificationLabel`).

## Click routing

`useNotificationClicks` (mounted once in `App.tsx`) follows the live-sessions radar's exact pattern:

1. Go focuses the window (`showWindow`) **before** emitting `notification_clicked`.
2. Empty `session_id` (the Settings preview banner) → logged no-op (focus-only click).
3. Resolve the owning project: the payload's `project_id` first; otherwise the global session snapshot, with one immediate `refreshNow()` retry when the session is not found.
4. Unknown session (even after the refresh) → logged no-op.
5. Known session: `switchProjectWithState(projectId)` when the project differs (restores that project's UI state), then `selectSession(sessionId, projectId)`. A failed switch surfaces its own toast and selects nothing.

Known Linux quirk: Wails maps reason-2 `NotificationClosed` (the banner's X) to the same `DEFAULT_ACTION` identifier, so an explicit dismiss can navigate too — indistinguishable at the identifier level. Timeout/programmatic closes never fire the callback.

## Settings UI

`SystemNotificationSettings` (General tab, directly under `SoundSettings`, same `Toggle` primitive and `border-t border-border pt-4` wrapper):

- **Master toggle** → `systemNotificationStore.setEnabled`. On enable: fire-and-forget `initSystemNotifications()` (idempotent) + one preview banner ("c0wrk — System notifications enabled"). On disable: no teardown — the OS retires delivered banners on its own schedule; future sends are gated off by the store.
- **Permission hint** (macOS-only concern): on section mount, `checkNotificationAuthorization()` is probed once (lazy — the General tab pays for it, not app startup). `false` renders a muted hint ("Notifications are disabled for c0wrk in macOS Settings — enable them to receive alerts") and suppresses the preview/test banner. Linux/Windows always grant, so no hint renders there. The probe fails open (a transport error resolves to granted) so a transient RPC failure never paints a misleading hint.
- **Test button** ("Send test notification", shown while enabled and authorized) → `showTestNotification()` — the Go-authored banner demonstrating the full click round-trip (focus without navigation).

## Error Handling

- **Best-effort contract** — no failure path throws into the event pipeline. `sendSystemNotification` catches and logs at warn; init failure is logged and left enabled (individual sends fail softly).
- **No Wails runtime** (vitest, SSR, `make dev-frontend`) — `isWailsReady()` gates init/send into silent no-ops; `getApp()` never throws.
- **Authorization** — a denied macOS authorization makes init count as successful (logged at info); the denial surfaces through the Settings hint instead of an error.

## Invariants

- **Listener/cue coverage** — every session with a running backend task has a listener that cues BOTH channels: the active session via `useSoundEvents`, every watched background session via `useBackgroundSessionWatcher`. The switch-time corrector is `useTaskFlagRestore` alone; the snapshot refresh fires on mount, live-set changes, every project/session switch, and the visible-window safety poll (see [sound-notifications.md](sound-notifications.md) § Listener-coverage invariant — the same invariant covers both cues).
- All banner runtime calls go through the Go bindings — never `window.runtime.SendNotification`.
- The Go click callback focuses the window before emitting, unconditionally.
- Exactly one `OnNotificationResponse` callback is registered per app run (init is memoized; a failed init is retried but never double-registers).
- Banner ids are unique within a process (`c0wrk-notification-<GOOS>-<timestamp>-<seq>`).
- `notification_clicked` payloads are validated (`isNotificationClickedData`); malformed ones are dropped and reported, never dispatched.

## Known Limitations

- The webview reload (macOS wake/termination recovery) discards frontend module state — the memoized init promise included — but the Go-side init and callback survive it; the next `initSystemNotifications()` re-runs harmlessly (backend memoization makes it a no-op).
- Linux banner-dismiss navigates like a click (the Wails reason-mapping quirk above).
- No per-event granularity: the master toggle governs all nine cued events (mirroring the sound channel's single-switch design).

## Related Specs

- [sound-notifications.md](sound-notifications.md) — the audible channel; the shared listener-coverage invariant and the foreground/background ownership model.
- [stores.md](stores.md) — `systemNotificationStore` (the persisted master toggle).
- [events.md](events.md) — event subscription; `useSoundEvents` and the background watcher are composed by `useSessionEvents`.
- [../../contracts/event-catalog.md](../../contracts/event-catalog.md) — the `notification_clicked` global event.
