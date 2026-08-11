# ADR-004: External Binary Dependencies (git, rg)

## Status

Superseded by [ADR-010](./010-tool-manager.md)

> Partial supersession — ripgrep (rg) is now managed by the tool-manager per ADR-010. git is retained as a conditional dependency, checked lazily via `exec.LookPath` on first CODE-mode project switch instead of at startup; CHAT mode (No Project) never requires git.

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
2. Treat `rg` as a hard runtime dependency (managed by the tool-manager per ADR-010). Treat `git` as a conditional dependency — checked via `exec.LookPath` at the entry point of `SwitchProject` for CODE-mode projects. If git is missing, a `runtime_error` event is emitted to the frontend (dismissable toast) and the project switch is rejected. CHAT mode (No Project) never checks for git.
3. Remove every "degrade silently" path in call sites. Errors from `git` or `rg` propagate to the caller. The single permitted non-error empty result is the case where `git` legitimately reports "not a git repository" for a path outside any repo; this is detected by matching stderr and distinguished from every other failure mode.

External binary dependencies managed by the tool-manager are added to the tool registry in `core/toolmanager/registry.go`. There are no startup-hard binary dependencies.

## Consequences

**Positive:**

- Behavior matches what users observe when running `git` / `rg` themselves; no hidden heuristic layer to debug
- Support for new git/ripgrep features comes "for free" when the user upgrades their tools
- Module dependency graph shrinks: `go.mod` / `go.sum` lose go-git, goripgrep, and their transitive deps (crypto, ssh, http transport layers the app did not need)
- Binary size decreases; build is faster
- Errors are actionable: a missing binary is reported up front, a broken repo is reported at the call site with the real stderr
- Agent behavior is deterministic: a failing `git` call returns an error result to the LLM instead of empty data that looks like success

**Negative:**

- c0wrk CODE mode does not run on systems without `git` on PATH; CHAT mode is unaffected
- CODE-mode project switch fails with a toast notification if git is missing
- Every git/rg invocation pays process-startup cost (~few ms on modern systems); acceptable because these calls are user-driven, not hot-loop
- Tests and CI environments must provision both binaries

## Alternatives Considered

**Keep go-git and goripgrep, fix the divergences case by case.** Rejected: the surface area is large and every new user-visible command would need a matching Go implementation. The libraries would stay a source of "almost-but-not-quite" bugs.

**Ship `git` and `rg` inside the app bundle.** Rejected: both tools are large, have system-dependent link requirements (especially `git` on macOS/Linux with OpenSSL, libcurl, etc.), and would duplicate what is already installed on every developer machine. The app's install audience already has these tools.

**Keep libraries but wrap silent failures with explicit errors.** Rejected: still keeps the library-vs-CLI behavior drift. Removing the libraries removes the entire class of drift bugs.

**Make git a hard startup dependency for all modes.** Rejected: CHAT mode (No Project) is a general-purpose assistant that never calls git. Blocking CHAT-mode users who don't have git installed is an unnecessary barrier. The current approach checks git lazily on first CODE-mode switch.
