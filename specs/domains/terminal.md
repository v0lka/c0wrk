# Terminal (PTY Sessions)

## Role

Per-session PTY-backed shells (`core/terminal.Manager`): one live terminal per session, keyed by session ID, output streamed to the frontend, with a single-owner teardown protocol that separates explicit stops from natural shell exits.

## Key Files

- `core/terminal/manager.go` - `Manager` (Start/Write/Resize/Stop/StopAll/IsActive), the `readLoop` reader goroutine, and the natural-exit protocol (non-Windows `!windows` build tag; `manager_windows.go` is the Windows counterpart, `manager_common.go` the shared pieces — the `Manager` struct, its `emit`/`onExit`/`userEnv` fields and `buildTermEnv`)
- `internal/shellresolver/` - resolves the login shell the Unix PTY runs (`shellresolver.Resolve()`); the Windows ConPTY build resolves its own shell (`resolveWindowsShell` in `manager_windows.go`)
- `backend/frontend_api_terminal.go` - the RPC surface: `StartTerminal(sessionID)`, `StartTerminalInDir(sessionID, workDir)`, `TerminalInput(sessionID, data)`, `TerminalResize(sessionID, cols, rows)`, `StopTerminal(sessionID)`, `GetTerminalHistory(sessionID)`
- `desktop/startup_phases.go` - `initTerminalManager`: constructs the `Manager` on the app-lifecycle context, expands `${VAR}` refs in the config `terminal.env` map, and wires the two session-scoped events — `session:<id>:terminal_output` (base64-encoded PTY output, the `emit` callback) and `session:<id>:terminal_exited` (the natural-exit `onExit` callback, empty-object payload)
- `backend/frontend_api_worktrees.go`, `backend/frontend_api_session_promote.go` - the two explicit-stop exceptions: `stopSessionTerminal` + `emitTerminalExited` (a send-time workspace bind onto a managed worktree) and session promotion each stop a PTY whose session SURVIVES and emit `session:<id>:terminal_exited` so the frontend's surviving instance follows the session into its new workspace
- `frontend/src/stores/terminalRegistryStore.ts`, `frontend/src/components/terminal/TerminalPanel.tsx` - the app-lifetime xterm.js registry (`@xterm/xterm` v6 + `@xterm/addon-fit`) keyed by session ID, and the panel that binds to it

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

### Process environment (`buildTermEnv`)

Every shell process gets the inherited environment with the embedded-terminal
variables applied ([ADR-029](../decisions/029-terminal-env-conventions.md)):

- `TERM` and `COLORTERM` fill only when the inherited environment lacks them
  (`xterm-256color`, `truecolor`).
- `TERM_PROGRAM=c0wrk` and `TERM_PROGRAM_VERSION` (the build's
  `version.Version`) are force-set — the inherited entries are dropped before
  the pin is appended, so a shell rc file can detect the in-app terminal and
  skip own-the-terminal behavior such as tmux auto-attach.
- Config `terminal.env` entries win over both: each key replaces any existing
  entry of the same name, and an empty key is skipped. `desktop` expands
  `${VAR}` references before handing the map to the manager and never logs the
  values, which may carry secrets.
- Name comparison funnels through `sameEnvKey` — byte equality on Unix, case
  folding on Windows — so a differently-cased inherited duplicate cannot slip
  into the child environment block.

### Session-scoped events

`session:<id>:terminal_output` carries base64-encoded PTY bytes (`useTerminalEvents`
decodes them with `atob`; a raw `string(data)` would corrupt invalid UTF-8 split
across read boundaries, which `json.Marshal` replaces with U+FFFD). `session:<id>:terminal_exited` carries an empty object — both
emitters send an empty map rather than `nil` so the frontend's null-payload
filter passes it. Payload contract:
[event-catalog.md](../contracts/event-catalog.md). Natural exits emit it through
the manager's `onExit`; session promotion and a send-time workspace bind emit it
explicitly after stopping a PTY whose session survives.

## Error Handling

- `Stop` on an unknown session is an error (`no terminal for session …`); `StopAll` logs per-session stop failures at Warn and continues.
- PTY read errors other than EOF/`os.ErrClosed` are logged at Debug; the loop exits on the first read error and the natural-exit protocol takes over.

## Invariants

- One live PTY per session ID; switching sessions preserves each terminal (the frontend `terminalRegistryStore` mirrors this); session deletion, explicit stop, or app shutdown ends it.
- Every shell process environment carries the force-set `TERM_PROGRAM=c0wrk` and `TERM_PROGRAM_VERSION`, fills `TERM`/`COLORTERM` only when the inherited environment lacks them, and gives config `terminal.env` entries precedence over both the inherited environment and the built-in defaults.
- `onExit` fires only for natural shell exits; `Stop`/`StopAll` never fire it.
- Natural-exit reclamation is single-owner: only the goroutine that removed the session from the map runs `teardown`, so the child is cancelled, the PTY closed, and the process reaped (`cmd.Wait`) exactly once.
- A `readLoop` observing a missing or mismatched map entry emits neither the end-marker nor `onExit`.
- `Stop` never joins `readDone`.

## Related Specs

- [session-lifecycle.md](session-lifecycle.md) - the sessions these terminals are keyed by (delete stops the session's PTY; shutdown stops all)
- [../decisions/029-terminal-env-conventions.md](../decisions/029-terminal-env-conventions.md) - terminal environment-variable conventions
- [../contracts/event-catalog.md](../contracts/event-catalog.md) - the `terminal_output`/`terminal_exited` payload contract and the two explicit-stop exceptions that also emit `terminal_exited`
