# ADR-033: Git Subprocess Hardening for Untrusted Repositories

## Status

Superseded by [034](./034-git-trust-opt-out.md)

Amended 2026-09-05 (post-v0.7.3 review findings [1]/[2]): the original claim
that `attr.tree=<empty-tree>` kills **all** attribute-routed vectors
overclaimed — it covers only the in-tree `.gitattributes` source. The
amendment (implemented in `core/workspace/gitconfig.go` and
`internal/sysproc/git.go`): `.git/info/attributes` and `core.attributesFile`
(verified live from repository config on git 2.50.1) are themselves scanned
and every routed driver name is pinned by the same `-c` overrides, which beat
file config wherever the driver is defined (including included files);
`core.attributesFile=` (empty) disables that source; an attribute source that
exists but cannot be scanned fails closed (the attributes mechanism has no
kill-switch). The empty-tree constant is now selected by object format
(`extensions.objectformat=sha256` needs the SHA-256 empty tree — the SHA-1
hash is a verified silent no-op there), unknown formats fail closed, linked
worktrees resolve `commondir` and scan the common config plus the enabled
`config.worktree` overlay with git's merge semantics, and the spawn
environment strips the `GIT_ATTR_*` family so `GIT_ATTR_SOURCE` cannot
reroute the attribute source.

Amended 2026-09-05 (post-v0.7.3 review findings [40]/[55]): the layer-2
reachability analysis had assumed local-only git usage, but the Git panel
ships remote operations (Pull/Push/Fetch RPCs) and the plain `git diff`
porcelain executes external diff drivers **by default** (verified on git
2.50.1: `diff.external` and attribute-routed `diff.<n>.command` fire with no
`--ext-diff` flag — `--no-ext-diff` is what disables them). The scanner now
scans the `credential` section as well and emits per-key neutralizations:
`core.sshCommand=ssh` (restores the default ssh binary so remote operations
keep working; an empty value aborts them), `core.askPass=`,
`credential.helper=` (an empty value resets the accumulated helper list —
the `-c` layer is read after every file, so the reset covers generic and
URL-specific helpers; a per-URL `credential.<url>.helper=` pin re-asserts
it), `diff.external=` and `diff.<n>.command=` (fail-closed kills), while
every porcelain `git diff` call site passes `--no-ext-diff` so its output
stays usable (post-v0.7.4 this covers `GenerateCommitMessage`, both
`GetFileDiffHunks` legs, and `BuildReviewDiff`'s `diff HEAD`/`--no-index`
legs; the one patch producer left without the flag — plumbing `diff-tree
-p` in `GetCommitDiff` — executes no external drivers absent an explicit
`--ext-diff`, verified on git 2.50.1). The intake notice and finding texts were corrected
accordingly (the old "not reachable"/"only with --ext-diff" claims were
factually false).

Amended 2026-09-05 (post-v0.7.3 review finding [56]): the blanket
`attr.tree` kill was narrowed to fail-closed-scan coverage. It used to
engage whenever any attribute-routed vector *or include* existed, which
disabled benign attributes too — empirically confirmed on git 2.50.1, a
clean CRLF-normalized repository (`.gitattributes` `* text=auto`) showed
` M file.txt` and a 2/2 whole-file numstat diff under the kill, so
legitimate filter-configured repositories (e.g. a local `git lfs install`)
got persistently false-modified statuses and inflated diff statistics with
no indication why. Now that the [1] closure scans and name-pins every
routing source `attr.tree` does not cover (`.git/info/attributes`,
`core.attributesFile`) and per-name `-c` pins beat file config wherever the
driver is defined (the in-tree `.gitattributes` routing included), visible
driver names no longer derive the blanket kill: they are neutralized by
name with benign eol/text attributes left intact (a side benefit on git
older than 2.45, where `attr.tree` is a silent no-op but the pins still
work). The kill remains engaged for the one case per-name pins cannot
cover — include directives that may hide driver definitions routed from
the in-tree `.gitattributes` — and while it is active the intake warning
carries an `(attributes disabled)` finding disclosing the collateral
(eol/CRLF normalization off, files may be reported as modified though
unchanged). The decision's no-whitelist rule is unchanged: the name pins
cover only names the scan can see, which is exactly why the kill stays for
includes.

Amended 2026-09-06 (post-v0.7.4 review — four verified gaps): the prior
review re-verified the attack surface behaviorally on git 2.50.1 and found
four live vectors under the then-current override set, now closed in
`core/workspace/gitconfig.go` / `core/workspace/git.go`:

1. **core.gitProxy** executes on every `git://` fetch/push even under the
   full baseline argv, and no value of the key neutralizes it (empty
   included). It is now a command-bearing finding neutralized by
   `-c protocol.git.allow=never` — the transport family is forbidden, the
   operation fails closed with "transport 'git' not allowed" instead of
   executing the repository's proxy (canary-verified; note git resolves
   duplicate `core.gitProxy` values first-wins, which the pin makes moot).
   ext:: and similar exotic transports remain denied by git's own protocol
   defaults.
2. **core.worktree** redirects where `checkout`/`reset --hard` write
   tracked files to any configured absolute path outside the workspace; no
   `-c` form beats it (an empty value is ignored too). It is now a finding
   neutralized by pinning the spawn environment's `GIT_WORK_TREE` to the
   work-tree root git would discover from the repository path (the one
   channel that outranks the config key; canary-verified — writes stay
   inside the repository root). The pin is finding-gated so legitimate
   worktree-using setups are untouched.
3. **Include-hidden transport/diff keys**: a `core.sshCommand` or
   `diff.external` defined in an included file is invisible to the scan,
   and the includes-only pins (attr.tree + core.attributesFile) did not
   stop execution on fetch / `git diff --cached`. Include-bearing configs
   now additionally emit the name-independent pins `core.sshCommand=ssh`,
   `core.askPass=`, `credential.helper=`, `diff.external=` (the exact
   values the finding-driven path uses; `-c` beats file config wherever
   the key is defined). Deliberately NOT unconditional — a `-c` pin beats
   the user's own global config too.
4. **attr.tree version gate**: `attr.tree` exists only since git 2.45
   (Ubuntu 22.04 ships 2.34, RHEL 9 ships 2.43) and older git silently
   ignores it, which would leave include-hidden attribute-routed drivers
   live while the neutralization reports coverage. The git version is now
   resolved once per process (cached, through the same hardened
   `sysproc.GitCmd` chokepoint, injectable in tests) and `GitCmdInRepo`
   **fails closed with a clear error for include-bearing configs when the
   resolved version is < 2.45 or unresolvable** — refusing to run git
   rather than running it with a dead neutralization.

Remaining residuals, enumerated honestly: **(a)** `core.pager` stays the
one command-bearing core key without any override (c0wrk always pipes git
output; a pager runs only on a terminal); **(b)** include-hidden keys
outside the four name-independent pins have no per-key cover — an included
file defining a `filter`/`merge`/`textconv` driver name routed from the
in-tree `.gitattributes` is covered only by `attr.tree`, which is exactly
why the ≥ 2.45 gate refuses to run at all on older git, and an included
file defining `core.pager` (or any future command-bearing key unknown to
the scanner) executes as it would outside c0wrk — includes remain a
detection-grade signal the intake warning surfaces; **(c)** the
`GIT_WORK_TREE` pin is finding-gated, so a `core.worktree` defined only in
an included file is not pinned (the scan cannot see it; the same
include-warning applies); **(d)** usability trade-offs, not security gaps:
`git://` remotes fail closed while `core.gitProxy` is present, and
include-bearing repositories are unusable in c0wrk on git < 2.45.

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

**Re-parse rationale (no caching).** The scan is deliberately re-run for every git invocation. The mid-session planting vector makes a cached scan a TOCTOU liability: any invalidation scheme (mtime, inode, watcher) races the writer, and a watcher over `.git` adds lifecycle complexity for no gain. The scan is a bounded (4 MiB cap; 4 KiB pointer cap) pure-text parse, so correctness costs microseconds; there is no cache to invalidate and no stale-trust window. Canary integration tests prove the property directly: a filter planted *between* two git calls on one long-lived caller is neutralized on the second call. The one sanctioned narrowing (review [15]): a *single logical operation* that spawns git multiple times — `BuildReviewDiff`'s `diff HEAD` + `ls-files` + per-untracked-file `--no-index` legs, formerly N+2 rescans and O(N × 4 MiB) reads with a hostile config — shares one scan taken before its first invocation (`gitScanMemo`). Freshness is preserved per user operation; cross-operation sharing remains forbidden.

Semantics are fail-closed: an unreadable, oversized, non-regular (a FIFO planted as `.git/config` would block the open forever — the scan runs synchronously on the `SwitchProject` RPC path — so anything but a regular file is refused, checked by fstat on the already-open non-blocking descriptor with no Stat→Open TOCTOU window, review [14]), or malformed-pointer config returns an error and **git is not executed at all**; boolean predicates (`IsGitRepo`, `IsGitTracked`) treat scan failure as "not a repo", but the non-repo fallbacks are degraded rather than silently git-free — every git invocation, `--no-index` diffs included, spawns through the same fresh scan (a `--no-index` run inside a repository directory still consults repo config; verified on git 2.50.1: an armed diff.external executes there without `--no-ext-diff`), so the scan failure resurfaces as an error from the fallback itself instead of producing output — never un-neutralized git (review [53]). A directory with no `.git` anywhere on its chain yields an empty scan and plain baseline behavior (keeps `git diff --no-index` working on non-repos). The helper also defaults `cmd.Dir` to the repository (defense-in-depth for callers who forget it). All repo-scoped call sites route through this helper: `core/workspace/git.go` (8 sites), `core/vectorindex/git.go` (`runGit`), and the backend git-panel wrappers (`backend/frontend_api_git.go`).

### 3. Detection and user warning at intake

`backend/frontend_api_gitconfig_risk.go` `notifyGitConfigRisk` scans a repository when it is **opened** — on project switch (`SwitchProject`, after `project:switched`) and on adding an auxiliary work directory (`AddWorkDirectory`, after `workdirs:changed`) — using the same exec-free parser, and emits the global `project:git_config_risk` event when the config is **not provably clean** (`GitConfigInfo.Clean()` is false: dangerous keys, include directives, parse errors, or an unreadable config). A clean, fully visible config emits nothing. Detection is fail-closed in the same direction as prevention: an unprovable config (includes not followed, malformed constructs git itself would refuse, unreadable file) is *warned about*, never presented as clean. The payload carries the standing notice: *"Repository-defined git hooks do not run inside c0wrk: hooks and the config-driven programs listed below are blocked or neutralized on every git invocation c0wrk makes. Continue only if you trust this repository."* The scanner is intentionally detection-only: neutralization lives in the spawn layers (1 and 2), which hold regardless of whether any UI is listening.

**User-controlled trust list (warning suppression only).** The warning carries three actions: *Trust this repo* (persists the repository's work-tree root into `security.trusted_git_repos` via the `TrustGitRepo` RPC — the root is resolved from the given path with `workspace.ResolveWorkTreeRoot`, review [52], so trusting a subdirectory workspace trusts the whole repository the scan walked up to, and the warning payload carries that same root — `notifyGitConfigRisk` then emits nothing for that exact root on future opens, whether reopened at the root or from any subdirectory), *Ignore* (closes the toast; the warning reappears on the next open), and *Fix* (starts a chat task describing the exact findings so the agent can clean the config up; agent edits inside `.git` still hit the layer-4 write gate). The list is managed from Settings → Security → *Trusted repos* (`GetTrustedGitRepos` / `RemoveTrustedGitRepo`). Matching is exact on `filepath.Clean`-ed absolute roots — deliberately no prefix/subtree semantics, so trusting one root never covers a different repository nested inside it — and with no config loaded nothing is trusted (fail-closed). Suppression affects **only the UI warning**: the spawn-layer neutralization (layers 1-2) stays fully in force for trusted and untrusted repositories alike, because the trust decision attests the user's assessment of intent, not a claim that the config is harmless.

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
