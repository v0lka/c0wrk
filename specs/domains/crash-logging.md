# Crash & Exit Logging

## Purpose

Guarantees that the fact and the cause of ANY application termination — crash, kill, signal, or clean quit — leave evidence on disk. Motivated by a real incident (2026-08-28): the app "disappeared" mid-session with an empty debug log; forensics (macOS unified log + SQLite message timeline) later proved a clean window-close quit, but nothing in c0wrk's own logs could distinguish that from a crash. This subsystem removes that ambiguity.

## Coverage matrix

| Cause | Evidence written |
| ----- | ---------------- |
| Panic in any goroutine | Go runtime dump → fd 2 → `logs/stderr.log` |
| Runtime fatal error (OOM, deadlock detector) | Go runtime dump → fd 2 → `logs/stderr.log` |
| SIGSEGV/SIGQUIT/SIGABRT in Go code | runtime dump (stderr.log) + OS crash report (.ips) |
| Native crash (WebKit/ONNX, cgo) | OS crash report only; next-start marker warning points at it |
| SIGTERM/SIGINT/SIGHUP | signal line in `logs/stderr.log` before default-disposition death |
| SIGKILL / OS OOM-kill (uncatchable) | liveness marker survives → WARN at next start |
| Clean quit (close button, Cmd+Q, updater quit) | `application shutdown: starting/complete` INFO in session log + exit-code banner |

## Mechanism

1. **fd redirection (`redirect_unix.go` / `redirect_windows.go`)** — `install` dup2's the capture file's descriptor onto fd 1 and 2 (Unix). This is required (not merely assigning `os.Stdout`/`os.Stderr` variables) because the Go runtime writes panic/fatal dumps with raw `write(2)` on fd 2, and native libraries do the same; none consult Go-level variables. The default `log.Logger` (and therefore `slog.Default()`) is rewired via `log.SetOutput` so pre-Startup slog output is persisted. On Windows, only the Go-level variables are redirected (GUI processes have no C-level stderr); marker detection still covers abnormal exits.
2. **Append-only `stderr.log`** with a startup banner (`pid`, version, commit, start time) and an exit banner (exit code, uptime). Rotation at 4 MiB renames to `stderr.old.log`; content is never truncated. Banners delimit runs in the shared file.
3. **Termination signals** (`SIGTERM`, `SIGINT`, `SIGHUP`) are logged, then re-raised after `signal.Reset` so the process keeps its true exit-by-signal cause. `SIGQUIT` is intentionally NOT intercepted: Go's default handler dumps every goroutine stack to stderr — that dump is the evidence.
4. **Liveness marker `app.running.json`** (pid, version, started_at) is written at startup and removed ONLY on clean exit (`mainImpl` after `wails.Run` returns), and only when the marker still records this process's pid — a marker overwritten by a concurrently started instance stays in place, preserving that instance's evidence. A surviving marker proves abnormal termination even when nothing could be logged (SIGKILL, power loss). At Install a leftover marker is stashed as `app.running.prev.json` instead of being overwritten.
5. **Startup reporting** — `crashlog.ReportUncleanShutdown(log, logDir)` runs in `App.Startup` after the logger reinit (`maybeReinitLogger`), so the report lands in the session log of the current run even when a non-default `log_level` swaps the file. When the stashed pid is still alive (`processAlive`: signal 0 on Unix, `OpenProcess` on Windows), an overlap WARN is logged instead of an unclean-shutdown WARN — two instances ran concurrently or the OS recycled the pid, so a crash cannot be concluded. Otherwise it logs the unclean-shutdown WARN (prev pid/version/start, pointer to `stderr.log`, its rotated copy `stderr.old.log`, and the OS crash-report directory). Either way the stashed marker is consumed.

## Key Files

- `backend/crashlog/crashlog.go` — `Install`, `Capture` (nil-tolerant `RemoveMarker`/`LogExit`), marker stashing, rotation, `ReportUncleanShutdown`
- `backend/crashlog/process_unix.go` — pid liveness probe (signal 0; EPERM = alive; darwin/linux)
- `backend/crashlog/process_windows.go` — pid liveness probe (OpenProcess; ERROR_ACCESS_DENIED = alive)
- `backend/crashlog/redirect_unix.go` — dup2-based fd redirect + signal re-raise (darwin/linux)
- `backend/crashlog/redirect_windows.go` — Go-variable redirect + exit-based re-raise
- `main.go` — installs capture before `NewWailsLogger` (opt-out `C0WRK_DISABLE_CRASH_CAPTURE=1`, exact value); removes marker on clean return; logs exit code before `os.Exit`
- `desktop/startup.go` — `ReportUncleanShutdown` call after the logger reinit; explicit `application shutdown: starting/complete` INFO logs in `App.Shutdown`, with the session log closed after the `complete` record so the closing bracket always persists

## Invariants

- The marker is removed on clean exits only, and only when it records this process's pid; panic paths deliberately skip removal so the next start reports the crash.
- Wails' own quit paths never bypass `App.Shutdown` (OnShutdown hook), and the session log closes after the final `application shutdown: complete` record, so a clean quit always ends with a closing bracket; an abrupt log end means abnormal termination by construction.
- An unclean-shutdown WARN requires the stashed marker's pid to be gone; a live pid yields an overlap WARN (concurrent instances or pid reuse).
- The `--self-update` helper child process never installs the capture (it returns before `Install`), so the updater's lifecycle does not touch the parent's marker or stderr.log.
- `C0WRK_DISABLE_CRASH_CAPTURE=1` (env) skips installation — dev ergonomics for `wails dev` live console output; production builds must never set it.
- Existing layers remain: top-level `recover` in `mainImpl` logs main-goroutine panics via slog before re-panicking; `desktop/wails_logger.go` persists Wails-internal Fatal/Error to `wails.log`.

## Non-goals

- No quit-confirmation dialog on window close with active sessions (a product decision; the close button today quits the app immediately — Wails v2 `windowShouldClose` → `"Q"` message).
- No in-process capture of pure native crashes (WebKit/ONNX) — those are covered by OS crash reports (.ips) plus the next-start marker warning.
