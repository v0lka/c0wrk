# ADR-055: Commit Hooks & Signing Gate (Withhold-and-Decide at the Commit Boundary)

## Status

Accepted

## Context

ADR-033 hardened every git process c0wrk spawns: the baseline pins
`core.hooksPath` to an empty directory and `commit.gpgsign=false`, so in an
untrusted repository **no hook executes and no signing program runs** — on any
operation, including `git commit`. ADR-034 then made *Trust this repo* the real
opt-out: a trusted root runs raw git, its own hooks and signing included.

That model left a gap at exactly one operation — the Git panel's Commit RPC:

1. **Trust is unreachable from the workflow for hook-only and signing-only
   repositories.** The intake warning (`project:git_config_risk`) fires on
   *config* findings. Installed hook files (husky, pre-commit frameworks) are
   not config keys — a repository whose only "armed" surface is
   `.git/hooks/pre-commit` scans clean and never warns, so the *Trust this
   repo* affordance (offered on the toast) is never presented; the user has no
   signal that there is a decision to make. Signing keys were worse: before
   this decision the scanner declared `commit.gpgsign` / `gpg.*` out of scope
   (baseline-covered, therefore not reported), so a signing-armed repository
   was invisible at intake too. Trust remained reachable only by knowing to
   dig into Settings → Security → Trusted repos.
2. **The commit silently diverges from the repository's own policy.** In such
   an untrusted-but-armed repository the Commit RPC spawned the hardened
   commit — and the commit *landed*: unhooked and unsigned. Outside c0wrk the
   same user's commit would be hook-checked and signed. c0wrk thus wrote
   history that violates the repository's own conventions (an unsigned commit
   in a signed-commits repository can only be repaired by history rewriting).
   "Strip-and-warn" was silent at precisely the operation where the strip
   changes the *persistent artifact* rather than a transient command outcome.

The unreachable-trust gap and the silent-divergence gap share one root: at the
commit boundary, skipping a repo-installed program is a **decision**, not a
neutralization detail — and nobody was asked.

## Decision

The commit boundary becomes an explicit gate: detect (exec-free) what a commit
*would* execute, withhold the commit when something is armed, and let the user
decide. Four cooperating pieces:

### 1. Commit gate — withhold-and-decide, never silently stripped

The Commit RPC (`backend/frontend_api_git_commit.go`,
`Commit(message, force) (CommitResult, error)`) resolves the repository root
and checks trust:

- **Untrusted + armed (commit-family hooks or armed signing detected) +
  `force=false`** → no spawn, no commit, no event: the RPC returns
  `CommitResult{Suppressed: …}` with a **nil error**. Suppressed is a
  decision request, not a failure — the RPC boundary distinguishes
  "withheld, decide" (non-error, actionable) from real git failures (error
  with stderr surfaced).
- **`force=true`** → the commit proceeds through the same hardened spawn —
  hooks and signing are still neutralized. Force is the explicit
  "I know, commit anyway" override, not a trust grant.
- **Detection error** → the commit fails closed (error): no commit is made in
  a repository whose commit surface cannot be inspected.
- **Trusted repository** → straight to the commit spawn. `GitCmdInRepo`
  resolves trust itself (raw git for a trusted root), so the repository's own
  hooks and signing **execute** — that is what trust means (ADR-034) — proven
  by the canary suite in `core/workspace`. The gate's own trust check keys on
  the same form the trust store does: the WORK-TREE ROOT
  (`workspace.ResolveWorkTreeRoot`), not the raw workspace path — a workspace
  opened at a subdirectory of a trusted repository commits as trusted instead
  of re-triggering the gate (where "Trust & commit" would loop forever, and
  force would run raw git through the gate's blind side). The suppression
  detection needs no resolution: it walks up to the discovered repository's
  hooks/config itself, so its result is independent of the workspace path.
- **Untrusted + clean (nothing armed)** → the plain hardened commit, exactly
  as before; clean repositories see no new friction.

The frontend renders a Suppressed result as a warning notice plus an explicit
"Commit hardened (skip hooks)" button (re-commit with `force=true`); the
notice clears on message edit or the next attempt. Never an error banner.

### 2. Full detection — `workspace.DetectCommitSuppression`, exec-free

One pure function
(`core/workspace/gitcommitsuppress.go`,
`DetectCommitSuppression(repoPath) (hooks []string, signingRepo, signingGlobal bool, err error)`)
answers "what would a commit here execute?" without spawning anything
(pinned by a structural no-`os/exec` test guard):

- **Commit-family hooks** — the four hooks `git commit` runs
  (`pre-commit`, `prepare-commit-msg`, `commit-msg`, `post-commit`) listed
  from the *common* hooks directory (`commondir`-resolved, so linked
  worktrees are covered from a worktree path); executable-bit required on
  POSIX, file presence on Windows; `*.sample` skeletons never count;
  symlinks resolve to their targets (git executes the target), directories
  and dangling links do not. Result sorted, empty → nil.
- **`core.hooksPath`** — a single marker
  (`CommitSuppressionHooksPathMarker`) instead of listing the redirected
  directory: git does not read the default `hooks/` when redirected, and the
  custom directory's contents are irrelevant to the decision (something
  armed = withhold).
- **Repository signing** — an *armed* (truthy: `true`/`yes`/`on`/`1`/bare)
  `commit.gpgsign` in the repository config (the `config.worktree` overlay
  included via the scanner's merge semantics). Siblings
  (`gpg.format`, `gpg.program`, `user.signingkey`) alone do not raise the
  flag — without `gpgsign` git does not invoke the signing program on
  commit.
- **Global signing** — an armed `commit.gpgsign` in the configuration files
  git reads outside the repository, resolved the way git resolves them:
  `$GIT_CONFIG_GLOBAL` replaces both default locations when set (an empty
  value disables the global config entirely); otherwise `~/.gitconfig` and
  the XDG global config — `$XDG_CONFIG_HOME/git/config` when
  `XDG_CONFIG_HOME` is set (git then does not read `~/.config/git/config`),
  `~/.config/git/config` otherwise. `$GIT_CONFIG_SYSTEM` joins the scan when
  set (git reads it on every invocation); the platform system config
  (`/etc/gitconfig`) stays out of scope — this flag describes the *user's*
  config. A malformed/oversized *user-level* config
  reads as clean: it is the user's own environment (git itself refuses to
  work with it), not repository-controlled input, so fail-open is deliberate
  here — the inverse of the repo-config direction, where unscannable always
  fails closed.
- **Known blind spot — include directives.** The scanner parses and records
  `include`/`includeIf` directives but deliberately never follows them (the
  standing ADR-033 posture: an included file's contents are unknown, and
  `ResolveIncludes` exists for trust fingerprinting only). A commit-surface
  setting that lives only inside an included file — an armed
  `commit.gpgsign` above all — is therefore invisible to the gate:
  `signingRepo` stays false and the hardened commit lands without the
  decision dialog. This is accepted, not overlooked: the baseline still
  neutralizes the commit either way, so nothing untrusted executes and the
  gap is silent hardening rather than exposure; the same directive already
  fires the intake `(include directive)` warning at project open; and
  following includes at the commit boundary would either re-implement git's
  condition evaluation (a wrong `wildmatch` would SKIP a file git actually
  reads — the dangerous direction) or reintroduce the hostile-repo I/O
  amplification ADR-033 keeps off the hot path. A repository that enforces
  signing or hooks through included configs relies on the intake warning
  and the trust flow.

Detection is a fresh scan per commit (same no-cache rationale as ADR-033:
the mid-session planting vector makes any cached verdict a TOCTOU liability).

### 3. Signing keys join the scanner and the intake picture

`ScanGitConfig` gained a `signing` finding kind (task scope of this decision):
`commit.gpgsign` (truthy-gated — `gpgsign=false` stays clean, so ordinary
init fixtures and disabled keys do not warn), `gpg.format`, `gpg.program`,
`user.signingkey`. All are `BaselineCovered=true` with empty overrides —
the baseline `-c commit.gpgsign=false` already neutralizes them; they are
reported **so the intake picture is complete** and the trust decision becomes
reachable for signing-armed repositories at intake, before the first commit.
This is a deliberate extension of the intake toast to repo-level signing;
hook *files* still produce no intake finding (they are not config) — the
commit gate is what surfaces them, at the moment that matters.

The sibling keys (`gpg.format`, `gpg.program`, `user.signingkey`) are marked
**inert** by the scan when no config layer arms `commit.gpgsign`: git runs a
signing program only on an armed `commit.gpgsign`, so the siblings alone are
dead configuration. `Clean()` ignores inert findings — a repository that
merely documents a signing setup stays warning-free, matching
`DetectCommitSuppression`, which never raised the gate for them. Inert
findings still render in the intake payload when a warning fires for another
reason.

### 4. Timeout and output — the commit spawn gets real budgets and visibility

- **`timeouts.gitCommitTimeout`** (seconds, default 300, `0`/absent → 300;
  `backend/config`) bounds the `git commit` spawn only — hooks, GPG signing,
  and large repositories legitimately need minutes. Quick probes
  (`rev-parse HEAD` for the new SHA, etc.) keep the fast 30 s budget.
- **Combined stdout+stderr capture**, bounded by a 64 KiB `limitedBuffer`
  with an explicit `[output truncated at 64 KiB]` marker: a trusted
  repository's hook and signing output reaches the UI through
  `CommitResult.Output`; `Sha` carries the new commit's SHA.
- **`cmd.WaitDelay = 1 s`**: a hook's orphaned children inherit the output
  pipes, and without this bound `cmd.Run` would stall until they exit —
  voiding the timeout (a sleeping hook's grandchild held a 1 s budget open
  for 10 s until this was pinned; covered by the timeout test).
- `git:status_changed` is emitted only on an actual successful commit — a
  withheld commit emits nothing (nothing happened).

### Standing notice verified

The `gitConfigRiskNotice` wording ("Repository-defined git hooks do not run
inside c0wrk: the config-driven programs listed below are blocked or
neutralized on every git invocation c0wrk makes, remote operations
(pull, push, fetch) included. Continue only if you trust this repository.")
remains accurate under the gate:
the notice only ever renders for *untrusted* repositories (a trusted repo with
a matching snapshot emits nothing; drift evicts trust before any re-warning),
and in an untrusted repository hooks indeed never execute. The gate changes
what happens at the commit boundary — the commit is withheld for a decision
instead of landing stripped — not the truth of the notice's claim. No
rewording required.

## Consequences

**Positive**

- No silent divergence: a commit that would silently skip repo-installed
  programs is withheld while it is still fully reversible — the two real
  choices (force the hardened commit, or trust the repository) are surfaced
  at the moment of the artifact's creation. Unsigned/hook-unchecked commits
  can no longer land unnoticed in a repository whose policy expects them.
- Trust becomes reachable from the workflow for both gap populations:
  signing-armed repos at intake (the new findings), hook-only repos at commit
  time (the gate's Suppressed notice points at the same decision).
- Trusted repositories keep fully native behavior with the visibility and
  liveness they were missing before: hook/signing output is captured and
  shown, and the spawn is bounded by an honest timeout that orphaned
  grandchildren cannot outlive.
- Fail-closed where input is untrusted (detection error blocks the commit),
  fail-open only where input is the user's own environment (global config).

**Negative / accepted trade-offs**

- One extra interaction for untrusted armed repositories: the first commit is
  withheld until the user picks. This is the price of not guessing; the
  override is one click and per-repo trust removes the gate permanently
  (snapshot-bound, ADR-034).
- The intake warning population widens: repositories with armed signing that
  previously stayed silent now warn. Deliberate — the findings are marked
  baseline-covered, and the alternative is the invisible trust gap this ADR
  closes.
- One additional exec-free scan per commit attempt (microseconds, same
  bounded parse the spawn layer already runs per invocation).
- The gate's detection does not follow `include`/`includeIf` directives, so
  commit-surface settings defined only inside an included file — armed
  signing above all — bypass the withhold-and-decide dialog (the commit
  lands hardened and unasked). Accepted as documented above: no untrusted
  code executes either way, and the intake `(include directive)` warning
  plus per-repo trust remain the intended paths for include-driven setups.

## Alternatives Considered

- **One-off raw-git commit for armed repositories** (spawn raw git for the
  commit only, restoring native behavior without a trust decision). Rejected:
  it executes untrusted repository programs with no user decision — the exact
  threat ADR-033 exists to stop — and it is a per-commit implicit trust with
  none of ADR-034's snapshot binding or drift recheck: strictly worse than
  either established mechanism.
- **Warn-only** (commit anyway through the hardened spawn, surface a warning
  that hooks/signing were skipped). Rejected: the divergence lives in the
  persistent artifact. An unsigned or hook-unchecked commit that has already
  landed can only be repaired by history rewriting, and a toast scrolls away.
  The withheld state is the only one in which nothing irreversible has
  happened; "warn after the fact" is not recoverable, "withhold and ask" is.
- **Detect armed hooks at intake instead** (extend the intake scanner to hook
  files, keep commits silent). Rejected: installed hooks are not config;
  intake-time listing of `.git/hooks` would warn on the common benign case
  (a first-party husky repo the user opens daily), and intake-once still
  misses mid-session hook planting. The commit boundary is where the artifact
  is created, so that is where the decision belongs; the gate detects with a
  fresh scan on every attempt.
- **Return an error instead of a Suppressed result.** Rejected: an error
  banner is a dead end — it offers no action. Suppressed-as-data lets the
  frontend present the two real choices (force hardened / trust the repo) as
  an actionable notice, and keeps the RPC boundary honest about the
  difference between "withheld, decide" and failure.
- **Auto-trust or heuristic trust for armed repositories.** Rejected: trust
  is an explicit, snapshot-bound user decision (ADR-034); automating it
  reopens the drift-blind "trust forever" gap that decision closed.

## Related

- [ADR-033](./033-git-subprocess-hardening.md) — the hardening layers the
  gate builds on (baseline hooks/signing neutralization; amended by this ADR
  at the commit boundary).
- [ADR-034](./034-git-trust-opt-out.md) — the trust opt-out the gate routes
  decisions to (snapshot-bound, recheck-with-diff, fail-closed).
- [../domains/workspace.md](../domains/workspace.md) — Git Integration and
  Git Subprocess Hardening: the gate, the detection surface, and the intake
  findings in their domain context.
- [../architecture/security-model.md](../architecture/security-model.md) —
  Git Subprocess Hardening in the layered control model.
- [../contracts/event-catalog.md](../contracts/event-catalog.md) —
  `project:git_config_risk` findings now include the signing keys.
- [../contracts/desktop-frontend.md](../contracts/desktop-frontend.md) — the
  `Commit(message, force) (CommitResult, error)` RPC contract.
