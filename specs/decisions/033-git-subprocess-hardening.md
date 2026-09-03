# ADR-033: Git Subprocess Hardening for Untrusted Repositories

## Status

Accepted

## Context

c0wrk runs git constantly in CODE mode: status, diff, log, ignore filtering, branch detection, the git panel. Every one of those invocations executes inside a repository the user pointed the app at — and **a repository is attacker-controlled data**. `.git/config` is effectively a program-invocation configuration file: git executes config-driven external programs during ordinary *read-only* operations (`status`, `diff`, `add`, `checkout`, even `commit`), before any trust decision c0wrk makes. Two concrete vectors motivated this decision:

1. **Repo-arrives-as-files.** A workspace can materialize from an untrusted source — a clone, a zip/tarball/npm drop, a native `files:dropped` payload — already armed with hostile `.git/config` keys (`core.fsmonitor`, `core.hooksPath`, `filter.<name>.process`, `merge.<name>.driver`, `diff.<name>.textconv`, `commit.gpgsign`/`gpg.program`, `core.editor`) plus a matching worktree `.gitattributes`. The first `git status` c0wrk runs can spawn attacker-chosen binaries under the user's credentials.
2. **Mid-session `.git/config` planting.** A payload delivered mid-session (e.g. via injected content steering an allowed command) or another local process can rewrite `.git/config` *after* c0wrk has opened the repository — so any "inspect once, trust for the session" scheme is unsound.

A secondary, agent-side path into the same boundary: c0wrk's own mutating file tools (`write_file`, `edit_file`, `delete_file`, `create_directory`, `delete_directory`) could be steered — e.g. by prompt injection — into planting hooks or filters inside `.git` for later execution **outside** c0wrk, where repository hooks do run. An attacker with direct write access to `.git` is already past the trust boundary (hooks would run on the user's next commit in any tool); the gate exists so the *agent* can never be turned into that attacker.

**Verification method (canary-based, git 2.50.1).** Before deciding the mechanism, every candidate neutralization was verified behaviorally: canary scripts (log marker + exit 1) were planted as hooks, filters, merge drivers, fsmonitor binaries, textconv, and signing programs in scratch repos under a clean-room environment, and a PASS required the canary to NOT execute with a sane command outcome. Findings that shaped the design: `-c key=value` on the command line beats `.git/config`; `core.hooksPath=<empty or nonexistent dir>` silently skips all hooks with no fallback to `.git/hooks`; `attr.tree=<empty-tree SHA>` kills **all** attribute-routed vectors (filters, merge drivers, textconv) on every tested operation; an empty-string `filter.<name>.process=` alone disarms an armed process filter (clean/smudge overrides alone do NOT); `commit.gpgsign=false` beats `gpg.program`; `GIT_EDITOR=true` (plus `-m` commits) removes the editor path. Reachability surprises that widened the scope: `git commit` invokes armed process filters even with `--allow-empty` on a clean tree, and `status`/`diff` consult the clean/process filter whenever hashing is forced (same-size stat-dirty or racy index entries) — so **every** git operation in the lifecycle must carry the neutralization, not only the obviously-mutating ones.

## Decision

Untrusted-repository git execution is neutralized by four cooperating layers. The first two are *prevention* (nothing attacker-chosen executes), the third is *detection* (the user is told what was found), the fourth closes the agent-side write path.

### 1. Global baseline on every git process — `internal/sysproc.GitCmd`

`GitCmd` is the single choke point for spawning git (verified by grep: no direct `exec.Command("git")` bypasses in the module). Every invocation's argv is prepended with:

- `-c core.fsmonitor=false` — never starts an attacker-supplied fsmonitor daemon (spawned by routine `status`/`diff` index refreshes);
- `-c core.hooksPath=<~/.c0wrk/git/safe-hooks>` — an empty directory under the c0wrk agent dir (constant `core.GitSafeHooksRelativePath`, created best-effort via `sync.Once`; a nonexistent dir is equally safe, so creation failure never falls back to `.git/hooks`) — **no repository hook can ever run**;
- `-c commit.gpgsign=false` — never invokes a repository-configured signing binary during commit;

and `GIT_EDITOR=true` is pinned in the environment — replacing, not appending to, any inherited `GIT_EDITOR` (glibc's `getenv` resolves duplicate names to the first entry, so appending after an inherited value would leave the inherited editor effective) — so no configured editor can be spawned (commits pass `-m`; `GIT_EDITOR=true` alone fails closed with an empty-commit-message abort rather than spawning anything). The command-line `-c` form wins over `.git/config`, so the baseline beats repo config **without modifying the repository**. Tests pin the exact argv/env via the `sysproc.UnhardenedGitArgv` escape hatch (tests-only; never for execution).

### 2. Per-repo neutralization with a fresh scan on every invocation — `core/workspace.GitCmdInRepo`

`GitCmdInRepo(ctx, repoPath, args...)` is the repo-scoped layer on top of the baseline. Before every invocation it parses the config of the repository **git itself would discover for repoPath** with the exec-free scanner (`ScanGitConfig`): the `.git` chain is walked up from repoPath (after symlink evaluation, mirroring git's chdir-based discovery), so a workspace rooted at a *subdirectory* of an armed repository is covered too — scanning only `<repoPath>/.git` would silently skip neutralization while `git -C <repoPath>` happily executes the parent repo's config. It then prepends `GitConfigInfo.NeutralizingArgv()`:

- per armed filter name: `-c filter.<name>.process=` together with `clean=cat` / `smudge=cat` (the `process=` override is mandatory — suppressing clean/smudge without it does not disarm the process filter);
- `-c merge.<name>.driver=false %O %A %B` per armed merge driver;
- `-c diff.<name>.textconv=cat` per armed textconv;
- `-c attr.tree=<empty-tree SHA>` whenever any attribute-routed vector exists **or** the config contained `include`/`includeIf` directives — the attribute-routing kill is the only coverage for attacker-chosen *unknown* names (see the no-whitelist rule below).

**Re-parse rationale (no caching).** The scan is deliberately re-run for every git invocation. The mid-session planting vector makes a cached scan a TOCTOU liability: any invalidation scheme (mtime, inode, watcher) races the writer, and a watcher over `.git` adds lifecycle complexity for no gain. The scan is a bounded (4 MiB cap; 4 KiB pointer cap) pure-text parse, so correctness costs microseconds; there is no cache to invalidate and no stale-trust window. Canary integration tests prove the property directly: a filter planted *between* two git calls on one long-lived caller is neutralized on the second call.

Semantics are fail-closed: an unreadable, oversized, non-regular (a FIFO planted as `.git/config` would block the open forever — the scan runs synchronously on the `SwitchProject` RPC path — so anything but a regular file is refused), or malformed-pointer config returns an error and **git is not executed at all**; boolean predicates (`IsGitRepo`, `IsGitTracked`) treat scan failure as "not a repo" so callers fall back to git-free paths (`--no-index` diffs, no ignore filtering) — never to un-neutralized git. A directory with no `.git` anywhere on its chain yields an empty scan and plain baseline behavior (keeps `git diff --no-index` working on non-repos). The helper also defaults `cmd.Dir` to the repository (defense-in-depth for callers that forget it). All repo-scoped call sites route through this helper: `core/workspace/git.go` (8 sites), `core/vectorindex/git.go` (`runGit`), and the backend git-panel wrappers (`backend/frontend_api_git.go`).

### 3. Detection and user warning at intake

`backend/frontend_api_gitconfig_risk.go` `notifyGitConfigRisk` scans a repository when it is **opened** — on project switch (`SwitchProject`, after `project:switched`) and on adding an auxiliary work directory (`AddWorkDirectory`, after `workdirs:changed`) — using the same exec-free parser, and emits the global `project:git_config_risk` event when the config is **not provably clean** (`GitConfigInfo.Clean()` is false: dangerous keys, include directives, parse errors, or an unreadable config). A clean, fully visible config emits nothing. Detection is fail-closed in the same direction as prevention: an unprovable config (includes not followed, malformed constructs git itself would refuse, unreadable file) is *warned about*, never presented as clean. The payload carries the standing notice: *"Repository-defined git hooks do not run inside c0wrk: hooks and the config-driven programs listed below are blocked or neutralized on every git invocation c0wrk makes. Continue only if you trust this repository."* The scanner is intentionally detection-only: neutralization lives in the spawn layers (1 and 2), which hold regardless of whether any UI is listening.

### 4. Agent-side `.git` write gate (sp4rk)

The five mutating file tools (`write_file`, `edit_file`, `delete_file`, `create_directory`, `delete_directory`) all judge through the shared `judgeWriteInSessionRoots` in sp4rk `tools/builtins/file_judge.go`, which now returns a **hard** outcome with the published reason code `git_internal_path` (`tools.ReasonCodeGitInternal`) when the target path contains a `.git` component at or below the workspace root (`isPathInGitDir`, `tools/builtins/paths.go`). The check runs after symlink resolution (a symlink into `.git` is caught like a literal path) and before the soft containment check, so it escalates to interactive confirmation under any group policy — including `allow` — and is never auto-overridable (mirroring `symlink_escape`). Scope: workspace-root `.git` trees only (nested repos, submodules, worktrees, and the `.git` gitdir-pointer file form included; `.gitignore`/`.github` never match). Temp-dir and out-of-roots `.git` paths fall through to the existing containment controls rather than this gate. The shell tools are deliberately untouched: agent-typed `git` mutations are already covered by the `execute`-group SCM blacklist (`security.groups.execute.blacklist`, pinned by `TestApplyDefaults_GitMutatingBlacklist`).

### Rules pinned by this decision

- **No filter-name whitelist as the primary defense.** A whitelist of known command-bearing keys (`filter.lfs.process`, …) can never be complete: the attacker picks the name, and unknown names are only covered by killing attribute routing itself (`attr.tree=<empty tree>`). Known-name neutralization is retained strictly as a fallback/defense-in-depth layer for git versions whose `check-attr` capability probe shows no `attr.tree` support. Never extend a name list and call it coverage.
- **Hooks: strip and warn (never silently strip, never hard-fail).** Hooks are stripped from execution unconditionally (baseline `-c core.hooksPath=<empty dir>`), repo operations proceed normally, and the strip is *announced* — the `gitConfigRiskNotice` travels with every `project:git_config_risk` warning and the scanner's hook findings state that hooks do not run. This preserves the user's workflow while refusing execution, instead of failing the operation or quietly dropping their hooks.
- **LFS trade-off accepted.** Filters are distrusted unconditionally — including legitimate ones. `git-lfs` smudge/clean will never execute inside c0wrk; LFS-pointer files are treated as ordinary content (canary-verified: byte-identical passthrough, raw-content diffs, correct status). c0wrk's git usage is analysis-oriented (status/diff/log), so losing filter side effects is acceptable; users who need LFS materialization run it outside c0wrk. This is pinned by its own canary test (a legit passthrough filter's log stays empty).
- **Never rely on disproved neutralizations.** `attr.tree=` (empty string) is *unsafe* (behaves like unset); an invalid `attr.tree` value is a silent no-op; an empty-string `core.hooksPath` is undocumented. Only the canary-verified forms above may be used.
- **The empty-tree constant is SHA-1 specific** (`4b825dc642cb6eb9a060e54bf8d69288fbee4904`, exported as `core/workspace.EmptyTreeSHA1` with the SHA-256 caveat documented). SHA-256 repositories have a different empty-tree hash — probe via `git hash-object -t tree /dev/null` before relying on the constant there.
- **Verification is git-version-specific.** All semantics were verified on git 2.50.1; the canary suites (`internal/gittest` fixtures, `core/workspace/git_canary_test.go`, and the vectorindex/backend routing canaries) must be re-run on git toolchain bumps and on new entries in the CI OS matrix.

## Consequences

**Positive**

- No repository-defined program executes on any git invocation c0wrk makes, regardless of when the config was written — verified end-to-end by canary integration tests, not by reasoning about git's config grammar.
- The repository is never modified to achieve safety (`-c` wins without writes), so c0wrk leaves no forensic or functional footprint in the user's repo.
- Failure modes are fail-closed in one direction: unscannable config → no git execution; unprovable config → user warning; agent write into `.git` → forced confirmation.
- The user gets an informed-consent moment (intake warning) instead of either silence or blocked workflows.

**Negative / accepted trade-offs**

- Legitimate hook-based workflows (husky, pre-commit frameworks) and LFS materialization never run inside c0wrk; diffs and statuses reflect pointer/raw content for filtered files.
- One bounded config parse per repo-scoped git call (no caching) — deliberate micro-cost buying TOCTOU immunity.
- git's neutralization semantics can drift across versions; the canary suites are the regression net and must be maintained.
- `.git/config` itself, `$GIT_DIR/info/attributes`, and `--no-optional-locks`-class runtime overrides remain above these layers: an attacker who can write `.git` directly is outside this threat model (the agent-side gate in layer 4 ensures c0wrk's agent cannot be steered into becoming that writer).

## Alternatives Considered

- **Known-key blacklist as the primary defense** (parse config, neutralize the keys on a curated list). Rejected as primary: cannot cover unknown filter/driver/textconv names — exactly the property an attacker exploits. Retained only as the layer-2 fallback for git without `attr.tree` support.
- **Sandbox or block git entirely.** Rejected: git is load-bearing for CODE mode (status, diffs, ignore filtering, git panel, branch detection); blocking it removes the product's core function, and OS-level sandboxing is out of scope for a desktop tool (no sandbox/tenant isolation is an accepted, documented trade-off).
- **Neutralize by rewriting `.git/config`** (sanitize then run). Rejected: mutates the user's repository, races concurrent git processes, destroys user intent, and creates a diff the user did not author.
- **Scan once per project switch and cache.** Rejected: unsound against the mid-session planting vector; invalidation races the writer (see re-parse rationale).
- **Follow `include`/`includeIf` directives and scan the targets too.** Rejected: include targets may be unreadable, remote, or unbounded, and a partial read is a false sense of completeness. Instead includes are recorded, warned about, and compensated by the `attr.tree` routing kill (which does not depend on knowing file contents).
- **Refuse to open repositories with dangerous config.** Rejected: hostile-looking configs (includes, `core.fsmonitor`) also occur in legitimate repos; detection + unconditional neutralization achieves safety without locking users out. Only *unprovable* configs fail closed at the spawn layer, and even there the intake path degrades to a warning, not a blocked switch.
- **Rely on the `execute`-group SCM blacklist alone.** Rejected: it governs agent-typed shell commands; it cannot stop git from spawning config-driven programs during c0wrk's own read-only operations (`status`/`diff`), which was the verified primary vector.

## Related

- [SECURITY.md](../../SECURITY.md) — threat model: "App-Spawned Subprocess Hardening (Git)" section; ASI04/ASI05 rules.
- [../architecture/security-model.md](../architecture/security-model.md) — Git Subprocess Hardening in the layered control model.
- [../domains/workspace.md](../domains/workspace.md) — Git Integration: the scanner, per-repo helper, and intake-warning wiring.
- [../contracts/event-catalog.md](../contracts/event-catalog.md) — `project:git_config_risk` event contract.
