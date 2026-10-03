# Testing Environment Conventions

## Purpose

Conventions for running c0wrk's TypeScript and Go test suites with passing assertions and clean diagnostic output, and manual test launches of the full application with a hermetic environment: pinned locale for tests that assert on English CLI output, and an isolated `$HOME` for whole-app runs so they never touch real user state.

## Key Files

- `Makefile` - `test` target (`go test ./...` + `cd frontend && npm test`); neither the Makefile nor CI pins a locale
- `frontend/package.json` - frontend `test` (`vitest run`), `build` (`tsc -b && vite build`) and `lint` (`eslint .`) commands
- `AGENTS.md` - contributor requirements for clean TypeScript/Go test output and explicit verification blockers
- `.github/workflows/ci.yml` - CI test matrix; runners use default (English) locales, so only local machines on non-English locales are affected
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
