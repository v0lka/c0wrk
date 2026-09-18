# Sound Notifications

## Role

Synthesize the app's notification cues in the webview with the Web Audio API and keep them audible despite the desktop webview's repeated autoplay suspensions and OS-level audio interruptions. Tones are generated at runtime from oscillators — no audio assets — so they are byte-identical on macOS WKWebView, Windows WebView2/Edge, and Linux WebKitGTK.

Sound is one of the app's **two notification channels**: the same cued events also raise native OS banners through the system-notification channel ([system-notifications.md](system-notifications.md)). Both channels hang off the same listener-coverage invariant below and carry independent master toggles.

## Key Files

- `frontend/src/lib/sound.ts` — the audio engine: lazy `AudioContext` lifecycle, recovery/replacement logic, tone presets, `playSound(kind)`, `initSoundUnlock()`, `__resetSoundModule()` (test-only).
- `frontend/src/hooks/events/useSoundEvents.ts` — foreground wiring: the pure mapping `classifySessionEvent()` plus the `useSoundEvents()` subscription.
- `frontend/src/App.tsx` — calls `initSoundUnlock()` once at app start (mount effect), so the gesture/visibility recovery listeners exist even with no active session.
- `frontend/src/hooks/useBackgroundSessionWatcher.ts` — replays the same cues for background sessions through `classifySessionEvent()` + `playSound()`.
- `frontend/src/hooks/useTaskFlagRestore.ts` — the switch-time `taskActive` corrector (extracted from ChatArea): writes the backend's authoritative `status.active` in both directions, guarded against reverting fresher live flag transitions (`taskFlagsEventAt`).
- `frontend/src/stores/activeSessionsStore.ts` — the cross-project DB snapshot + `useActiveSessionsRefresh()` triggers (mount, live-set changes, every project/session switch, visible-window safety poll).
- `frontend/src/stores/soundStore.ts` — the master `enabled` toggle (persisted at `c0wrk-sound`, default enabled).
- `frontend/src/components/settings/SoundSettings.tsx` — settings UI; previews an `attention` tone when the toggle is switched on.
- `frontend/src/lib/sound.test.ts` — unit tests pinning the recovery/replacement behaviour (fake `window` + `AudioContext`; vitest runs in a DOM-less `node` environment).
- `frontend/src/hooks/sessionSoundCoverage.test.tsx` — integration test pinning the listener-coverage invariant across CHAT↔CODE toggles (real hooks + stores + a fake Wails bus).

## Listener-coverage invariant (why cues can go missing above the audio engine)

A session's cues — audible **and** banner, both channels — have exactly one owner at a time:

- the **active** session → `useSoundEvents(activeSessionId)` (which also dispatches the banner channel for the active session);
- every **background** session that is busy → `useBackgroundSessionWatcher`, whose watched set is `taskActive ∪ paused ∪ pausing ∪ snapshot(unfinished_task_status ∈ {in_progress, paused})` minus the active id.

**Invariant: for every session with a running backend task, a sound-event listener must exist** (active-subscription or watched-set membership) — and through it, both cues (the tone and the OS banner). The CHAT↔CODE toggle is a project switch that re-runs this handoff; three historical defects broke the invariant and are now closed:

1. **Blind switch-time flag reset (removed).** `useSessionEvents`' reset effect used to write `taskActive[dest] = false` on every switch-TO. The flag was only restored by an async RPC, so toggling away first left a genuinely-running background session flagged idle — un-watched, its completion/HITL events had no listener (no cue, no pending-action card). `useTaskFlagRestore` is now the sole switch-time corrector, writing `status.active` in both directions.
2. **Unguarded/cancellable fast restore (guarded).** The restore RPC is cancelled by a switch-away (correct — its write would target a now-background session) and its resolved snapshot can be older than a live flag transition (`task_resumed`, terminal events). The write is skipped when `taskFlagsEventAt[dest]` is newer than the snapshot read, mirroring `reconcileRuntimeStatus`.
3. **Snapshot refresh triggers derived from the corrupted state (made independent).** `useActiveSessionsRefresh` used to re-read `listAllSessions` only on mount and live-set changes (`liveSessionsSignature(taskActive, paused)`) — a signal derived from the very flags the switch dance could corrupt. After a corruption the live set goes empty and stays empty, so no refresh would ever re-fire and the watched set never self-healed. Now **every project/session switch triggers a refresh** (the DB's `unfinished_task_status` is authoritative and independent of the live flags), and a **visible-window safety poll** (`SNAPSHOT_POLL_INTERVAL_MS`, 30 s) bounds the worst case after a refresh-RPC failure or a stale answer. All triggers ride the same 500 ms debounce funnel.

The Wails event layer itself is not a loss point: `EventsOn` cancellation is per-listener (v2.15 runtime), and Go→webview delivery is unconditional — coverage is decided entirely by which listeners the frontend holds.

## Behavior

### Event → tone pipeline

```
Backend session event  (session:${sessionId}:<event>)
        │  Wails EventsOn (frontend/src/api/runtime.ts onSessionEvent)
        ▼
useSoundEvents()  (foreground)   /   useBackgroundSessionWatcher  (background)
        │  classifySessionEvent(event, data)   ← pure, unit-testable
        │      returns a SoundKind, or null for silent events
        ▼
   playSound(kind)                       (frontend/src/lib/sound.ts)
        │  useSoundStore.enabled?  ── no ──▶ silent no-op
        ▼
   getCtx()  ── lazily builds / returns the single cached AudioContext
        │
        ├─ running ─────────────▶ playCue()  → oscillators → ctx.destination
        │
        └─ not running ─────────▶ resumeCtx() ──▶ running? playCue()
                                       └─ wedged? markWedged() → drop, backoff, replace later
```

`classifySessionEvent(event, data)` is the single source of the event→category mapping (shared by the foreground hook and the background watcher):

| SoundKind   | Session events | Notes |
| ----------- | -------------- | ----- |
| `success`   | `task_complete` | Only when the payload's `success !== false` (a missing flag is treated as success). |
| `attention` | `ask_user`, `step_limit`, `tool_confirm`, `plan_review_ready`, `task_failed_resumable`, `goal_proposal` | Any interactive prompt that needs the user. |
| `error`     | `error`, `task_cancelled`, `task_complete` with `success === false` | Failures, cancellations, and partial/aborted completions. |
| *(silent)*  | everything else | Returns `null`; no cue is played. |

The background watcher only emits a cue for a session the user is **not** currently viewing, so a foreground completion never double-plays.

### Tone presets

| Kind | Notes (equal temperament) | Oscillator |
| ---- | ------------------------- | ---------- |
| `success` | C5 → E5 → G5 ascending major triad (523.251 / 659.255 / 783.991 Hz) | sine |
| `attention` | single A4 chime (440 Hz) | sine |
| `error` | A4 → A3 descending (440 → 220 Hz) | triangle |

Every note uses an attack → exponential-decay gain envelope (peak gain kept ≤ 0.24) so cues are soft and never startle.

### AudioContext states

A cue is scheduled **only** on a context observed `running`; scheduling notes on any other state is silently discarded by the engine, which is the root of the original "cues periodically go missing" defect.

| `ctx.state` | Meaning | Handling |
| ----------- | ------- | -------- |
| `running` | Actively rendering. | Schedule the cue immediately. |
| `suspended` | Autoplay policy locked it, or the OS/webview paused rendering. | Attempt `resume()`; play once it reaches `running`. The persistent gesture listeners recover it on the next interaction. |
| `interrupted` | WebKit-only, non-standard OS audio interruption (display sleep, another app taking audio focus, a call). **Not** covered by a `state === 'suspended'` check. | Attempt `resume()`; a context wedged here is dropped and replaced. |
| `closed` | The context is gone for good. | Drop it at the next `getCtx()` and build a fresh one. |

### Context lifecycle and recovery

- **Lazy, single, reused context.** `getCtx()` builds one `AudioContext` on first use and caches it module-wide (one audio graph / thread pool). It is also attached a one-time `statechange` recovery listener.
- **Leave-`running` recovery.** When the context leaves `running`, the `statechange` listener and the gesture/visibility handlers call `recoverCtx()`, which resumes it. A resume that cannot succeed because the context is wedged (`closed`/`interrupted`) triggers `markWedged()`.
- **Replacement after backoff.** `markWedged()` drops a non-revivable context and arms a `CTX_BACKOFF_MS` (1500 ms) window before a replacement may be built — so a long interruption during which *every* resume fails cannot spin up a new context for each cue. Without this replacement the app stayed silent until a full restart.
- **Bounded resume.** `resumeCtx()` never rejects and always settles: the attempt is wrapped in `withTimeout(..., RESUME_TIMEOUT_MS, false)` (1000 ms), because WebKit can leave `resume()` **pending forever** on an `interrupted` context. A timeout resolves to `false`, which is exactly the signal `recoverCtx()` needs to drop the stuck context.
- **Gesture unlock.** `initSoundUnlock()` attaches **persistent** (not `{ once: true }`) `pointerdown` / `keydown` / `touchstart` listeners plus a `visibilitychange` handler. Desktop webviews start the context suspended until a user gesture, and the webview re-suspends/interrupts it repeatedly — so a one-shot listener would leave no way back. It is idempotent and is registered **once at App start** (`App.tsx` mount effect), independent of any session, so the listeners exist even when no session is open.

### Logging / observability

Audio-engine anomalies log at **warn**, so they are visible at the default log level (`warn`):

- `[sound] replacing non-revivable audio context` — a wedged/`closed` context is being dropped (`markWedged`).
- `[sound] failed to create AudioContext` — the constructor threw (`getCtx`).
- `[sound] context resume failed` / `[sound] context resume threw` / `[sound] context resume timed out` — a resume attempt was rejected, threw, or did not settle within `RESUME_TIMEOUT_MS`.

Informational state transitions (`[sound] audio context left running`) stay at **debug**.

## Error Handling

- **Web Audio unavailable** (`typeof window === 'undefined'`, or no `AudioContext`/`webkitAudioContext` constructor): `getCtx()` returns `null` and `playSound()` is a silent no-op.
- **Construction throws**: caught in `getCtx()`, logged at warn, `null` returned, the cue is dropped.
- **Resume rejects / throws / times out**: logged at warn; if the context is wedged it is dropped and replaced after backoff; the current cue is dropped.
- **Master toggle off**: `playSound()` returns before touching the engine — no context is created, no listeners attached.
- **Best-effort contract**: no failure path throws into the event pipeline; sound is a UX nicety that must never break the task UI.

## Invariants

- **Listener coverage**: every session with a running backend task has a sound-event listener at all times — it is the active session (`useSoundEvents`) or a member of the background watcher's set. The switch-time corrector is `useTaskFlagRestore` alone (no other switch path writes `taskActive`); the snapshot refresh fires on mount, on live-set changes, on every project/session switch, and on the visible-window safety poll. The same listener coverage carries the system-notification banner channel ([system-notifications.md](system-notifications.md)).
- A `running` context is never replaced.
- A `closed` — or `interrupted` (wedged) — context is never revived in place; it is dropped and a replacement is built after `CTX_BACKOFF_MS`.
- At most one `AudioContext` is cached/alive at a time.
- Replacement is bounded: no more than one new context is built per `CTX_BACKOFF_MS` while an interruption persists, no matter how many cues arrive.
- `resumeCtx()` always settles within `RESUME_TIMEOUT_MS` and never rejects.
- A cue is scheduled only on a context observed `running`.
- `classifySessionEvent()` is the single event→tone mapping, shared by the foreground hook and the background watcher.

## Known Limitations

- **The audio runtime is per webview page.** A webview reload — the macOS wake/termination recovery path ([017-macos-wake-reload.md](../../decisions/017-macos-wake-reload.md), [018-macos-webview-recovery.md](../../decisions/018-macos-webview-recovery.md)) — discards all module state (`audioCtx`, `unlockRegistered`, `stateChangeRegistered`, `ctxBackoffUntil`) and recreates the page: the fresh `AudioContext` starts `suspended` again and stays silent until the next user gesture re-runs `initSoundUnlock()`.
- `resume()` cannot be cancelled — the timeout only bounds the caller's observation of it; a late settlement is ignored.
- Cues are synthesized oscillators only; there are no per-kind volume/melody options and no audio assets to swap.
- The `interrupted` state is non-standard (WebKit); other engines only ever report `suspended`/`closed`.

## Related Specs

- [events.md](events.md) — event subscription; `useSoundEvents` is one of the hooks composed by `useSessionEvents`.
- [stores.md](stores.md) — `soundStore` (the persisted master toggle).
- [system-notifications.md](system-notifications.md) — the OS banner channel fed by the same event stream and listener coverage.
- [README.md](README.md) — frontend architecture overview.
- [../../contracts/event-catalog.md](../../contracts/event-catalog.md) — the session events the pipeline listens to.
- [../../decisions/017-macos-wake-reload.md](../../decisions/017-macos-wake-reload.md), [../../decisions/018-macos-webview-recovery.md](../../decisions/018-macos-webview-recovery.md) — the webview reload that resets the audio runtime.
