# Testing Environment Conventions

## Purpose

Conventions for running c0wrk's TypeScript and Go test suites with passing assertions and clean diagnostic output, and manual test launches of the full application with a hermetic environment: pinned locale for tests that assert on English CLI output, and an isolated `$HOME` for whole-app runs so they never touch real user state.

## Key Files

- `Makefile` - `test` = `test-go` (uncached verbose default Go checks, `-skip '^TestStress'`) + frontend; `test-stress` invokes the audited Go runner
- `internal/teststress/` - cross-platform stress driver; fixed 20 race/shuffle repetitions, buffered JSON parsing with full output printed only for failing runs, and required run/pass counts; missing/skipped/failed coverage is nonzero
- `internal/testtiming/category_test.go` - exact stress inventory and Make/CI driver contract
- `frontend/package.json` - frontend `test` (`vitest run`), `build` (`tsc -b && vite build`) and `lint` (`eslint .`) commands
- `AGENTS.md` - contributor requirements for clean TypeScript/Go test output and explicit verification blockers
- `.github/workflows/ci.yml` - CI test matrix; runners use default (English) locales, so only local machines on non-English locales are affected. Default-test steps pipe the verbose `go test` stream through an awk pass-through filter that drops only pass markers, so a clean run logs nothing while failures, skips and stray diagnostics stay visible (`shell: bash` + pipefail keeps the exit code authoritative on every OS; `internal/testtiming` guards the filter shape)
- `backend/frontend_api_git_test.go` - example of a locale-sensitive test: `TestCheckoutBranch_LocalChangesOverwritten` asserts the English git phrase `local changes` in the error text
- `backend/config/paths.go` - `DefaultAgentDir` (`~/.c0wrk`) resolution via `os.UserHomeDir()`; an isolated `$HOME` relocates the entire agent dir, config, database, and tools
- `specs/domains/tool-manager.md` - layout of `~/.c0wrk/tools/` (stateless, copyable artifact)
- `specs/domains/crash-logging.md` - crash/exit diagnostics records that manual runs write into the real agent dir

## Core Types

```bash
# Locale-pinned test run (tests that match English CLI strings):
LC_ALL=C go test ./backend/

# Hermetic whole-app launch (isolated $HOME; stateless artifacts copied in):
export TEST_HOME=$(mktemp -d)
cp -r "$HOME/.c0wrk/tools" "$TEST_HOME/.c0wrk/"
# config.yaml is regenerated on first start when absent; or copy it in to reuse providers
"$C0WRK_BIN" &
```

## Flow

```
1. go test / npm test (locale-sensitive suites)
   └─ run under LC_ALL=C ── non-English local shell locale (e.g. ru_RU.UTF-8)
                            localizes git/subprocess stderr → string assertions
                            on English phrases fail

2. Manual whole-app test run
   └─ mktemp -d → copy ~/.c0wrk/tools/ (≈538 MiB, stateless)
   └─ set $HOME to the temp dir before launching the binary
   └─ app resolves ~/.c0wrk inside the temp dir (os.UserHomeDir)
   └─ config.yaml / database.db / projects / logs are created fresh there
   └─ real ~/.c0wrk (18 MiB db, 1.6 GiB projects, window state) stays untouched

3. Test-output verification (TypeScript and Go)
   └─ run suite → retain complete stdout/stderr + exit status
   └─ Go: -count=1 with -v or -json → expose passing-package output
   └─ expected diagnostic → test-local interception + assertions + cleanup
   └─ unexpected diagnostic → fix cause → rerun affected tests + full suite
   └─ passing tests + zero emitted errors/warnings → successful test run
```

## Invariants

- A successful TypeScript/Go test run has passing tests and zero emitted errors or warnings across stdout and stderr, including diagnostics from expected failure paths; exit status and pass counts alone establish only assertion success.
- Expected diagnostics are intercepted at their source only within the test that deliberately triggers them. Diagnostics that form part of the tested behavior have assertions on severity, payload/message and occurrence count, alongside the original failure/recovery assertions.
- TypeScript tests use scoped console/logger spies with restoration through test cleanup (`onTestFinished` or `afterEach`); Go tests use a test-local injected logger/handler or test-server error logger with resource cleanup through `t.Cleanup`.
- Unexpected diagnostics receive root-cause fixes, followed by reruns of the affected tests and full suite. React `act()` warnings, unhandled asynchronous failures, races and leaked work are diagnostics to resolve.
- Test-output review retains complete stdout/stderr and preserves unexpected diagnostics: scoped interception replaces global console/logger silencing, broad filters, raised log thresholds, discarded output or weakened assertions. Go verification uses an uncached `-count=1` run with `-v` or `-json`, because ordinary passing-package summaries can hide emitted messages.
- Verification reports distinguish test results from build/lint results and identify unresolved diagnostics or incomplete checks as blockers to a clean-verification claim.

### Deterministic time and lifecycle

- Timer-only unit tests use `testing/synctest` (Go) or scoped fake timers/Date (Vitest). Bubbled channels, timers and workers are created inside the bubble. Real HTTP, syscalls, fsnotify, SQLite, PTYs and processes stay outside it; a fake Now alone does not virtualize AfterFunc firing.
- Ordering tests establish entered → action/cancel/reconfigure → release → done/join → assertions. Release cleanup is registered before any fatal assertion and executes before server Close/Shutdown. Join precedes directory/handle cleanup and negative assertions. State publication and event emission are distinct phases.
- Wall-clock sleep is not a synchronization primitive, distinct-timestamp fixture or microtask flush. Filesystem timestamp fixtures set and verify mtime explicitly; timestamp ordering uses fixed clocks. TS awaits the specific controlled RPC/promise inside async act, not an arbitrary number of microtasks.
- A bounded watchdog fails a hung positive operation; it does not prove absence or timer boundaries. Negative quiet windows and requested-duration tick ceilings are not deterministic proofs. Check no-call/no-event only after the relevant worker/effect lifecycle is drained.
- Real OS integration keeps real kernel/filesystem/transport coverage. Any timing-dependent sampling contract names the specific test, OS behavior, observation limitation and hang bound; it is not a blanket file or subsystem exemption. Pure classification/timer transitions need separate deterministic tests. Contention stress supplements, never replaces, controlled branch/lifecycle regressions.
- `internal/testtiming` runs in default Go checks and ratchets AST Go Sleep/empty timeout-select and direct TS delay-promise candidates. Its exact-expression/count JSON baseline is visible **legacy migration debt, not approved OS exceptions**. Imported testing/synctest.Test direct function-literal bodies receive lexical virtual-Sleep classification (aliases/dot imports included); helper-hidden sleeps and same-named unrelated APIs remain debt. This classification never permits OS I/O in a bubble or a file-level exemption. New expressions or increased counts fail; resolved entries must be removed. Baseline regeneration is explicit and reviewed, never a CI fallback. Detector scope and residual classes are recorded in [the migration ledger](../../docs/development/test-timing-migration.md).
- The bounded probabilistic category is explicitly named `TestStress*`: currently manifest concurrent writers and MCP ProxyClient read/write churn. `make test-go` runs `go test -count=1 -v -skip '^TestStress' ./...`; the CI default step runs the same command with the stream piped through an awk pass-through filter that drops only the verbose pass markers (`=== RUN/PAUSE/CONT/NAME`, `--- PASS` at any depth, bare `PASS`, `ok` package lines) — a clean CI run logs nothing, while every failure, skip and stray test diagnostic stays visible, and bash pipefail keeps the exit code authoritative. Raw `go test ./...` still includes both categories. `make test-stress` (Windows: `go run ./internal/teststress`) executes `go test -race -count=20 -shuffle=on -json -timeout=8m -run '^TestStress' ./core ./core/embeddedllm`, buffers the JSON stream (printing the buffered output of failed/skipped tests only on a failing run) and requires each catalogued test to run/pass exactly 20 times. A failed command, failed/skip event or missing count is a failing exit, never retry-to-green. Linux amd64/arm64, macOS and Windows CI run the same driver as a separate required step, even after a default-test failure unless cancelled; no continue-on-error.
- Default branch/lifecycle coverage remains: manifest promotion fault-injection and Windows sharing classification/held-handle checks; real MCP HTTP lock-release regressions; SQLite WAL snapshot/pool checks with explicitly held transactions/connections. Those SQLite cases are controlled integration, not probabilistic stress. Stress success is scheduling evidence, not exhaustive branch coverage. No build tags, environment guards or short-mode skips were introduced for this split; adding a category member requires updating the exact inventory, required runner coverage and driver contract.
- Unix PTY no-exit-on-Stop/StopAll acceptance holds a real output callback, performs teardown, releases it, then waits for the session's read-loop completion before asserting no callback/ended marker. Cleanup releases before joining the reader and reaping the shell, before directory removal. The watchdog only diagnoses hangs; Stop's public API does not join user callbacks. Windows ConPTY and other remaining lifecycle/timing debt are not claimed migrated by this proof.

- Tests that assert on English strings produced by external CLIs (git and similar) always run under `LC_ALL=C`, because a localized shell environment (e.g. `ru_RU.UTF-8`) changes those strings and breaks the assertions.
- Tests that are locale-independent keep passing under `LC_ALL=C`; pinning the locale for the whole run is the default posture.
- Manual test launches of the whole application always run with an isolated `$HOME`, so they read and write only their own `~/.c0wrk` tree.
- Stateless artifacts (`~/.c0wrk/tools/`) may be copied from the real home into the isolated home to avoid re-downloading managed binaries; stateful state (database, projects, session data, window/update state) stays out of the copy and is created fresh per isolated run.
- The isolated-home rule keeps the real user agent dir free of test-generated sessions, projects, log noise, and crash diagnostics.

## Configuration

No configuration surface. The conventions are runbook-level:

| Concern | Value |
| --- | --- |
| Locale for locale-sensitive test suites | `LC_ALL=C` |
| Isolated home for whole-app test runs | fresh temp dir (`mktemp -d`), exported as `$HOME` before launch |
| Copyable stateless artifact | `~/.c0wrk/tools/` (managed binaries + venv; see [tool-manager.md](tool-manager.md)) |
| Regenerated on first start | `config.yaml`, `database.db`, `projects/`, `logs/` |

## Extension Points

- When adding a test with an expected failure diagnostic, intercept that diagnostic locally and retain assertions on the failure behavior; verify the logging contract when it is part of the tested behavior. Restore every interception through test cleanup so unrelated tests retain diagnostic visibility.
- When reviewing test results, inspect the complete output as well as exit status and pass counts; a diagnostic-bearing run remains unclean until the diagnostic is fixed or intercepted in its responsible test.
- When adding a test that matches English output of an external CLI, run it under `LC_ALL=C` (or make the assertion locale-independent, e.g. by matching exit codes / structured output instead of prose).
- When adding new stateless artifacts under `~/.c0wrk/`, they become candidates for the isolated-home copy step; stateful artifacts always stay out.

## Related Specs

- [tool-manager.md](tool-manager.md) - `~/.c0wrk/tools/` layout and why it is stateless (pinned-version reconciliation, no auto-update)
- [crash-logging.md](crash-logging.md) - crash/exit diagnostics that whole-app runs write into the agent dir
- [session-lifecycle.md](session-lifecycle.md) - session/project storage layout under `~/.c0wrk/projects/` that isolated runs keep out of the real home
- [../architecture/data-flow.md](../architecture/data-flow.md) - config load flow (`~/.c0wrk/config.yaml`) relocated wholesale by an isolated `$HOME`
