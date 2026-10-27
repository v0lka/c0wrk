# Terminal (PTY Sessions)

## Role

Per-session PTY-backed shells (`core/terminal.Manager`): one live terminal per session, keyed by session ID, output streamed to the frontend, with a single-owner teardown protocol that separates explicit stops from natural shell exits.

## Key Files

- `core/terminal/manager.go` - `Manager` (Start/Write/Resize/Stop/StopAll), the `readLoop` reader goroutine, and the natural-exit protocol (unix build tag; `manager_windows.go` is the Windows counterpart, `manager_common.go` the shared pieces)
- `internal/shellresolver/` - resolves the login shell the PTY runs
- `backend/frontend_api_terminal.go` - the desktop RPC surface and session-scoped terminal output events

## Behavior

### Natural-exit protocol (`onExit` / `readLoop`)

`onExit` fires only on natural shell exit (the user typed `exit`, or the shell
crashed). Explicit `Stop`/`StopAll` teardown — session deletion, app shutdown,
in-session restart — never fires it: those paths own their follow-up state.

`readLoop` mirrors `Stop`'s teardown for natural exits (`Stop` cannot reclaim
the fd/context for a session it did not remove from the map). A `readLoop`
that finds the map entry missing or mismatched (a replacement shell owns the
session id) stays silent — no end-marker, no `onExit` — so it cannot report a
healthy replacement shell as dead.

`Stop` must never join `readDone`: an `onExit` callback may itself call
`Stop`, which would deadlock. `readDone` is closed only after `readLoop` and
all its callbacks return.

## Error Handling

- `Stop` on an unknown session is an error (`no terminal for session …`); `StopAll` logs per-session stop failures at Warn and continues.
- PTY read errors other than EOF/`os.ErrClosed` are logged at Debug; the loop exits on the first read error and the natural-exit protocol takes over.

## Invariants

- One live PTY per session ID; switching sessions preserves each terminal (the frontend `terminalRegistryStore` mirrors this); session deletion, explicit stop, or app shutdown ends it.
- `onExit` fires only for natural shell exits; `Stop`/`StopAll` never fire it.
- Natural-exit reclamation is single-owner: only the goroutine that removed the session from the map runs `teardown`, so the child is cancelled, the PTY closed, and the process reaped (`cmd.Wait`) exactly once.
- A `readLoop` observing a missing or mismatched map entry emits neither the end-marker nor `onExit`.
- `Stop` never joins `readDone`.

## Related Specs

- [session-lifecycle.md](session-lifecycle.md) - the sessions these terminals are keyed by (delete stops the session's PTY; shutdown stops all)
- [../decisions/029-terminal-env-conventions.md](../decisions/029-terminal-env-conventions.md) - terminal environment-variable conventions
