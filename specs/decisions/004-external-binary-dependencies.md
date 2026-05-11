# ADR-004: External Binary Dependencies (git, rg)

## Status

Accepted

## Context

c0wrk needs two capabilities that are non-trivial to reimplement correctly in pure Go:

1. Full git plumbing: status with untracked/ignored files, unified diffs (including staged vs. working tree, and `diff --no-index`), `.gitignore` resolution that matches user expectations, and branch detection (including detached HEAD).
2. Fast recursive regex content search across a workspace, respecting `.gitignore`.

Both were originally solved with Go libraries embedded in the binary:

- `github.com/go-git/go-git/v6` for git operations
- `github.com/localrivet/goripgrep` as a Go wrapper around ripgrep internals

In practice these libraries diverged from the user-visible behavior of the real `git` and `rg` command-line tools. Edge cases (submodules, sparse-checkout, ignore precedence, specific flags, binary detection heuristics, UTF-8 handling, large files) required maintenance effort that was out of proportion to the value. Worse, the call sites had accumulated silent fallbacks: when go-git returned an error, the code returned an empty status; when goripgrep failed to initialize, the tool returned "no matches". These fallbacks masked real problems and made the agent's behavior non-deterministic from the user's point of view.

## Decision

1. Remove `github.com/go-git/go-git/v6` and `github.com/localrivet/goripgrep` from the module. All git operations call the `git` CLI via `exec.CommandContext`; all regex search operations call the `rg` CLI via `exec.CommandContext` with `--json` for structured parsing.
2. Treat `git` and `rg` as hard runtime dependencies. At startup, `desktop.verifyExternalDependencies` checks `exec.LookPath` for every required binary. If any is missing it shows a blocking fatal modal ("Missing Required Dependencies") with an Exit button, calls `wailsRuntime.Quit`, and aborts `Startup`.
3. Remove every "degrade silently" path in call sites. Errors from `git` or `rg` propagate to the caller. The single permitted non-error empty result is the case where `git` legitimately reports "not a git repository" for a path outside any repo; this is detected by matching stderr and distinguished from every other failure mode.

New external binaries may be added to `requiredBinaries` in `desktop/prerequisites.go`; every addition requires a spec update to the relevant domain spec's "Key Files" or "Invariants" section.

## Consequences

**Positive:**

- Behavior matches what users observe when running `git` / `rg` themselves; no hidden heuristic layer to debug
- Support for new git/ripgrep features comes "for free" when the user upgrades their tools
- Module dependency graph shrinks: `go.mod` / `go.sum` lose go-git, goripgrep, and their transitive deps (crypto, ssh, http transport layers the app did not need)
- Binary size decreases; build is faster
- Errors are actionable: a missing binary is reported up front, a broken repo is reported at the call site with the real stderr
- Agent behavior is deterministic: a failing `git` call returns an error result to the LLM instead of empty data that looks like success

**Negative:**

- c0wrk no longer runs on systems without `git` and `rg` on PATH; packaging/install docs must say so
- Startup fails fast instead of partially initializing, so install problems surface as a modal rather than a gradually-degraded UI
- Every git/rg invocation pays process-startup cost (~few ms on modern systems); acceptable because these calls are user-driven, not hot-loop
- Tests and CI environments must provision both binaries

## Alternatives Considered

**Keep go-git and goripgrep, fix the divergences case by case.** Rejected: the surface area is large and every new user-visible command would need a matching Go implementation. The libraries would stay a source of "almost-but-not-quite" bugs.

**Ship `git` and `rg` inside the app bundle.** Rejected: both tools are large, have system-dependent link requirements (especially `git` on macOS/Linux with OpenSSL, libcurl, etc.), and would duplicate what is already installed on every developer machine. The app's install audience already has these tools.

**Keep libraries but wrap silent failures with explicit errors.** Rejected: still keeps the library-vs-CLI behavior drift. Removing the libraries removes the entire class of drift bugs.

**Make the dependencies soft with a feature-flag "git disabled" mode.** Rejected: every feature that matters (workspace status, diff view, vector index branch partitioning, ripgrep tool) depends on them. A "git disabled" mode is a different product.
