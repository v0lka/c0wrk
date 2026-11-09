# Command-Line Interface (argv parsing)

## Purpose

One parser for the c0wrk binary's command line: `core/cli` turns raw argv (`os.Args[1:]`) into a `Request` naming exactly one action — run the app, print help, print version, or run the self-update staging apply. The grammar is strict and fail-closed: any argument it does not recognize is an error naming that argument, and `main.go` turns it into a stderr message with exit code 2. `--self-update` and its companion flags are internal re-exec plumbing shared with the updater and are never advertised in help.

## Key Files

- `core/cli/cli.go` - the entire grammar: `Parse`, `PrintHelp`, `PrintVersion`; a pure package (no logger, no global state; stdlib + `core/version` only, so `core/updater` can import it without a cycle)
- `core/cli/cli_test.go` - table-driven contract tests (29 `Parse` cases + print tests)
- `core/version/version.go` - the build metadata `PrintVersion` renders: `Version` (`dev` without ldflags), `GitCommit` (`none`), `BuildDate` (`unknown`). The `Makefile` `build` target and `.github/workflows/release.yml` override all three through `-ldflags "-X github.com/v0lka/c0wrk/core/version.<Var>=…"`, so a plain `go build`/`go test` still yields valid non-empty values
- `main.go` - the only consumer: parses exactly once as the process's first action, dispatches on the action, owns the exit codes, and documents the load-bearing startup ordering
- `core/updater/installer.go` - `SelfUpdateArgs` (builds the re-exec argv) and `ApplySelfUpdate` (consumes `cli.SelfUpdateRequest`); the update-domain logic, no argv grammar — the argv contract is pinned from the updater side by `TestSelfUpdateArgsRoundTrip` in `installer_test.go`

## Core Types

```go
// core/cli/cli.go
type Action int

const (
    ActionRun        Action = iota // zero value: empty argv parses to a plain run request
    ActionHelp                     // -h, --help
    ActionVersion                  // -v, --version
    ActionSelfUpdate               // hidden re-exec plumbing
)

// SelfUpdateRequest carries the companion values of a --self-update invocation.
// It is populated exactly when Action is ActionSelfUpdate.
type SelfUpdateRequest struct {
    PID    int    // parent (old) app process id the updater waits on
    Stage  string // staging directory (update archive + updater binary)
    Target string // install root to replace
}

// Request is the fully parsed command line.
type Request struct {
    Action     Action
    SelfUpdate *SelfUpdateRequest // non-nil exactly when Action == ActionSelfUpdate
}
```

## Flow

`mainImpl` parses once, before anything else runs, and dispatches:

```
os.Args[1:]
   │
   ▼
cli.Parse ──── error ──▶ "c0wrk: <err>" + usage → STDERR, exit 2
   │
   ├─ ActionRun ────────▶ normal startup:
   │                       GPU-relaunch handover wait → single-instance lock (ADR-075)
   │                       → crash capture (fd 1/2 redirect) → wails.Run
   ├─ ActionHelp ───────▶ PrintHelp    → STDOUT, exit 0
   ├─ ActionVersion ────▶ PrintVersion → STDOUT, exit 0
   └─ ActionSelfUpdate ─▶ updater.ApplySelfUpdate (waits for parent PID, swaps the
                           tree, relaunches) → exit 0; failure message
                           "c0wrk self-update failed: …" → STDERR, exit 1
                           (the Wails lifecycle never starts on this path)
```

Inside `Parse` — a single scan with deferred companion validation:

```
scan args ─┬─ action flag (-h/-v/--self-update) → accept(A): FIRST one wins,
           │                                     later action flags ignored
           ├─ companion (--pid/--stage/--target) → consume next arg (last wins),
           │        value-syntax error NOW (missing: "--stage requires a value";
           │        empty: "--stage requires a non-empty value";
           │        bad pid: "invalid --pid %q"); remember first companion
           │        for later
           └─ anything else → error "unknown argument: <arg>"
   │
   ▼
post-scan, only when Action == ActionSelfUpdate:
   missing pid/stage/target → "--self-update requires --pid|--stage|--target"
after that scan, for every other action:
   a companion seen without --self-update → error "unknown argument: <first companion>"
```

## Invariants

- **Parse is the process's first action.** The ordering contract in `main.go` is load-bearing: CLI diagnostics (a parse error, `-h`, `-v`) and the `--self-update` dispatch → GPU-relaunch handover wait → single-instance lock (ADR-075) → crash capture (fd 1/2 redirect). Rationale: (a) diagnostics must reach the REAL stdout/stderr — crash capture redirects fd 1/2 into `<logDir>/stderr.log`, and usage buried in a log file is invisible; (b) a diagnostic exit never touches app state — in particular it happens before the single-instance lock, since a mistyped flag is not a competing app instance; (c) the self-update re-exec path never starts the Wails lifecycle.
- **Strict grammar, fail-closed.** Any unrecognized argv element — an unknown flag, a bare positional, a `--flag=value` form (never split), or a companion flag without `--self-update` — is an error naming that argument. Nothing is silently ignored.
- **First-wins actions, last-wins values.** The first action flag selects the action (`--help --self-update` → Help; `--self-update --help` → SelfUpdate, whose missing companions then error); repeated value flags take the last occurrence; companions may appear anywhere on the line.
- **Companion validation is conditional and deferred.** The `--pid`/`--stage`/`--target` trio is required only when `--self-update` is the selected action; `--pid` must be a strict positive integer (digits only; `+5`/`-5`/`0`/overflow rejected); an explicitly empty value (`--stage ""`) is a value-syntax error at the flag, not a deferred one.
- **`--self-update` is hidden.** Help lists only `-h/--help` and `-v/--version`; the re-exec flag and its companions never appear in user-facing output. The flag is produced only by `updater.SelfUpdateArgs`.
- **Streams and exit codes are fixed.** Help/version → stdout, exit 0. Parse error → `c0wrk: <err>` + usage on stderr, exit 2. `ApplySelfUpdate` failure → `c0wrk self-update failed: …` on stderr, exit 1.
- **Accepted limitations of strictness.** Arguments injected by unsupervised launchers are now rejected with exit 2 where they were previously swallowed: the `wails dev` harness's own arguments, should any reach the binary, and legacy macOS LaunchServices `-psn_<serial>` process-serial arguments, where still injected. Accepted: the error names the offending argument, and both are development/legacy launch paths, not release paths.
- **Pure package.** No logger, no globals, no c0wrk dependencies beyond `core/version` — keeping `core/cli` importable from `core/updater` without import cycles.

## Configuration

None — the grammar is compiled in; there is no config.yaml surface.

## Extension Points

- **New user-facing flag** (e.g. `--minimize`): add a case to `Parse`'s switch (+ a `helpText` line), a new action or a run-option field, a dispatch case in `main.go`, and table-driven cases in `cli_test.go`. Keep the strict default: unrecognized input stays an error.
- **New hidden re-exec plumbing**: follow the `--self-update` pattern — an action flag, companion flags validated only when that action is selected, hidden from `helpText`, argv built by a dedicated builder (the `SelfUpdateArgs` role) and pinned by a round-trip test.
- **Positional arguments**: deliberately unsupported — the default scan branch rejects them. Adding one is a grammar-posture change and needs its own decision, not a quiet case addition.

## Related Specs

- [094-cli-argument-handling.md](../decisions/094-cli-argument-handling.md) - the ADR behind this grammar (single parser, strictness, ordering)
- [023-auto-update.md](../decisions/023-auto-update.md) - the self-update re-exec dance this parser serves; its "main.go detects `--self-update` before the Wails lifecycle starts" wording stays accurate
- [075-single-instance-lock.md](../decisions/075-single-instance-lock.md) - the app-level lock the ordering contract must precede
- [crash-logging.md](crash-logging.md) - the crash capture whose fd 1/2 redirect motivates diagnostics-first ordering
- [webview-gpu-policy.md](webview-gpu-policy.md) - the GPU-relaunch handover wait sitting between CLI dispatch and the lock
