# ADR-034: Git Config Trust Opt-Out (Snapshot-Bound, Recheck-with-Diff, Fail-Closed)

## Status

Accepted

## Context

ADR-033 established the four-layer git subprocess hardening model, and inside
it pinned one rule about user trust: the *Trust this repo* action was
**warning-suppression only** — it persisted the repository's work-tree root
into `security.trusted_git_repos`, silenced the `project:git_config_risk`
intake toast, but left the spawn-layer neutralization (baseline + per-repo
`-c` overrides) fully in force. Trust "attested the user's assessment of
intent, not a claim that the config is harmless."

That model has two defects, each a real-world pain:

1. **Trust does nothing a user actually wants it to.** A user who *genuinely*
   trusts a repository — a first-party repo with `git-lfs`, a husky/pre-commit
   setup, or a signing workflow — cannot restore native git behavior inside
   c0wrk. The trust decision changes no behavior; it only dismisses a toast.
   The LFS/filter/hook trade-off ADR-033 accepted as unconditional therefore
   cannot be opted out of, even deliberately, for the one class of repository
   the user has already reviewed and accepted.
2. **Trust is fire-and-forget and drift-blind.** A trusted entry suppresses
   the warning *forever*. If the repository's `.git/config` changes after the
   trust decision — whether planted mid-session (the exact vector ADR-033
   documents), edited by the user, or drifted through a remote update — the
   trust still suppresses the warning, so the user is never told that the
   configuration they reviewed is no longer the one on disk. The suppression
   was warning-only so nothing executed, but the lifetime mismatch made the
   trust decision semantically stale the moment the config moved.

The reversal this decision records: make trust a **real, user-controlled
opt-out of hardening** — the trusted repository runs raw git, its own hooks,
filters and signing included — but **bind the trust to the exact configuration
the user reviewed**, re-verify it against that snapshot on every open, and
**fail closed back to hardening** the moment the config drifts. This turns
"trust forever, and it doesn't even do anything" into "trust this *exact*
config; if it changes, you get a diff and a re-warning, and the repo hardens
again."

## Decision

Trust becomes an opt-out with a snapshot-bound lifetime and fail-closed
eviction. The hardening layers ADR-033 defined (global baseline, per-repo
neutralization, intake detection, agent-side `.git` write gate) are unchanged
for **every** repository except the ones the user explicitly trusts; the
trusted path is the only place neutralization is lifted, and only while the
config is byte-for-byte what the user trusted.

### 1. Trust = opt-out of hardening

`TrustGitRepo` (`backend/frontend_api_gitconfig_risk.go`) now does two things:

- **Suppresses the intake warning** for the repository's work-tree root (the
  same `workspace.ResolveWorkTreeRoot`-normalized form the warning is
  attributed to, so trust and attribution always agree — review [52]).
- **Opts the root out of spawn-layer hardening.** The root is mirrored into
  the process-wide `core/gittrust` registry (`syncGitTrustRegistry`), which
  `core/workspace` consults to decide whether a repository may spawn raw git
  (`sysproc.GitCmdRaw`) — its own hooks, filters, merge drivers, textconv, and
  signing apply as they would outside c0wrk. Untrusted and hardened roots keep
  the full neutralization.

This reverses ADR-033's "trust is warning-suppression only" rule: the user's
explicit trust is now the control that lifts hardening, so a deliberate
opt-out is possible for the repositories the user has genuinely reviewed.
Because lifting hardening re-enables exactly the config-driven programs the
four layers exist to stop, the opt-out is gated by the snapshot binding and
recheck below — trust no longer outlives the config it was granted for.

### 2. Snapshot + fingerprint binding

A trust decision is bound to the exact bytes the user reviewed, not to a path
string. `ScanGitConfig` captures, in `GitConfigInfo.rawSources`, the raw bytes
of every source it read — the common config, the linked-worktree
`config.worktree` overlay, `.git/info/attributes`, and the
`core.attributesFile` target — in a stable order. Two derived primitives make
that binding diff-able and hashable:

- `(*GitConfigInfo).Snapshot() []byte` — a canonical, diff-able serialization
  of all raw sources, each under a `===== kind (path) =====` header.
- `(*GitConfigInfo).Fingerprint() string` — the SHA-256 hex digest of
  `Snapshot()`.

`TrustGitRepo` computes the fingerprint, stores the snapshot bytes under
`~/.c0wrk/git-config-snapshots/<fingerprint>` (content-addressed via
`config.GitConfigSnapshotsDir`), and records `TrustedGitRepo{Path,
Fingerprint}` in `security.trusted_git_repos`. The fingerprint covers config,
the worktree overlay, both attribute-routing sources, and every
include/includeIf target the config resolves (see the amendment below) — a
drift in any of them changes the fingerprint.

### 2a. Amendment: include targets are fingerprinted (follow-for-fingerprint only)

ADR-033 rejected *following* `include`/`includeIf` directives because the
included files may be unreadable, remote, or unbounded, and a partial read is a
false sense of completeness for key *neutralization*. That rejection still
holds for the per-invocation spawn scan. It does not extend to *fingerprinting*,
where completeness is not required — the property needed is only "a change to a
file git reads must change the fingerprint".

`GitConfigInfo.ResolveIncludes` therefore follows the config's include
directives to read each target's raw bytes into `includeSources`, which
`Snapshot`/`Fingerprint` serialize alongside `rawSources`. It is invoked only by
the trust/recheck RPCs (never on the spawn path), so a hostile repo cannot
amplify per-invocation I/O. It is bounded (recursion depth, total file count,
per-file size, regular-file-only) and non-fatal (a missing target contributes an
empty source; an unreadable/oversized/non-regular target contributes a marker).
Condition handling is deliberately conservative and fail-closed: both `include`
and `includeIf` targets are fingerprinted regardless of condition, so a change
to a conditionally-inactive file still revokes trust — over-reporting in the
safe direction (never the dangerous direction of skipping a file git actually
reads). This closes the include-hidden drift gap without reimplementing git's
`wildmatch()` for `gitdir`/`onbranch`/`hasconfig` conditions.

### 3. Recheck-with-diff on every open

The trust is not taken on faith after the decision. `notifyGitConfigRisk`, when
it finds the opened root in the trusted list, delegates to
`recheckTrustedGitRepo`, which re-scans the config and compares fingerprints:

- **Fingerprint matches** → the config is unchanged since trust → nothing is
  emitted; the repository stays raw-git eligible.
- **Fingerprint differs, or the config can no longer be read** → the trust is
  stale. The entry is evicted (`RemoveTrustedGitRepo`, which also removes the
  root from the `core/gittrust` registry, returning it to the hardened
  default), and a `project:git_config_risk` event fires carrying `Reason`
  (`gitConfigDriftReason`) and `Diff` — a human-readable unified diff produced
  by `DiffGitConfigSnapshots(previous, current)` (LCS for small inputs,
  memory-safe fallback for large ones, capped at 32 KiB).

The recheck scans the same work-tree-root form the trust was stored under, so
a subdirectory workspace does not false-trigger drift from a differently
anchored relative `core.attributesFile`.

### 4. Fail-closed on both ends of the trust lifecycle

- **At trust time:** a config that cannot be scanned (unreadable, oversized,
  non-regular, malformed pointer) is refused — a trust decision cannot be
  bound to bytes that cannot be read, so `TrustGitRepo` returns an error and
  stores nothing.
- **At recheck time:** a config that can no longer be read is treated as drift
  — the trust is evicted and the repository hardens again, with the
  unreadable-config finding (rather than silently keeping a raw-git root whose
  config c0wrk can no longer see).

### 5. Explicit harden list (inverse of trust)

A new `security.harden_git_repos` list, maintained by
`HardenGitRepo` / `GetHardenGitRepos` / `RemoveHardenGitRepo`, marks a root as
**always** hardened: its intake warning is never suppressed and it is never
raw-git eligible, regardless of anything else. Hardening is the inverse of
trust at the spawn layer. Trust and harden are mutually exclusive in both
directions — config validation rejects a root present in both lists, and the
RPCs remove a root from the opposite list when it is added to one
(`removeTrustedPathLocked` / `removeHardenPathLocked`). All four RPCs are
idempotent and return defensive copies.

### 6. Migration of legacy trust entries

Entries written by pre-fingerprint builds (`trusted_git_repos` as a bare path
string) migrate transparently at load to `TrustedGitRepo{Path}` with an empty
`Fingerprint`. There is no snapshot to diff against, so these entries keep
suppressing the warning unconditionally (the pre-reversal, warning-only
semantics) until the user re-trusts the repository, at which point a snapshot
and fingerprint are captured and the recheck-with-diff lifetime applies.

## Consequences

**Positive**

- The user can deliberately restore native git behavior (hooks, filters,
  signing) for a repository they have reviewed — the LFS/hook trade-off is now
  opt-out-able, per-repository, instead of unconditional.
- The trust decision is honest about its scope and lifetime: it binds to the
  exact config reviewed, and any post-trust change is surfaced as a diff and
  met with fail-closed re-hardening — closing the drift-blind "trust forever"
  gap.
- Fail-closed in both directions: unscannable config at trust time is refused;
  drift (changed or unreadable) at open time evicts and re-hardens. A stale or
  corrupted raw-git root cannot persist silently.

**Negative / accepted trade-offs**

- The trust opt-out re-enables exactly the config-driven programs the hardening
  exists to stop. The whole residual risk of ADR-033 (a config-driven program
  executing under the user's credentials) now rests on the user's explicit,
  per-config trust decision — mitigated by the snapshot recheck, but not
  eliminated: the user is accepting a genuine escalation of trust, not
  dismissing a warning.
- One additional bounded config re-scan per open of a trusted repository (the
  recheck). This is the same exec-free, size-capped parse ADR-033 already runs
  per invocation, so the cost is negligible; correctness (no stale-trust
  window) is what it buys.
- The snapshot store (`~/.c0wrk/git-config-snapshots/`) grows one content-
  addressed file per distinct trusted config; files are small (a few KiB) and
  shared across entries that fingerprint identically.

## Alternatives Considered

- **Keep trust as warning-suppression only (status quo ante).** Rejected: it
  gives the user no way to restore native behavior, and its lifetime is
  fire-and-forget — the two defects this decision exists to close.
- **Trust without a snapshot (path string only, opt-out forever).** Rejected:
  re-introduces the drift-blind gap in a strictly worse form, because now the
  drift leaves a *raw-git* repository silently running a changed config.
- **Recheck by mtime/inode/watcher.** Rejected: any invalidation scheme races
  the writer (the same TOCTOU argument as ADR-033's no-cache re-parse);
  fingerprinting the re-scanned bytes is race-free by construction.
- **Fingerprint only, no diff.** Rejected: eviction must tell the user *what*
  changed so they can re-trust an expected change; a bare "trust revoked"
  toast would drive approval fatigue.
- **Auto-re-trust on drift.** Rejected: silently extending trust across a
  config change defeats the entire point of snapshot binding.
- **Restore behavior via a config rewrite rather than raw git.** Rejected for
  the same reasons ADR-033 rejected config rewriting (mutates the user's
  repository, races concurrent git).

## Related

- [ADR-033](./033-git-subprocess-hardening.md) — the hardening layers this
  decision's trust semantics supersede (layers 1, 2, 4 remain in force).
- [../architecture/security-model.md](../architecture/security-model.md) — Git
  Subprocess Hardening in the layered control model (trust/harden opt-out).
- [../domains/workspace.md](../domains/workspace.md) — Git Integration: the
  scanner, per-repo helper, and intake-warning wiring.
- [../contracts/event-catalog.md](../contracts/event-catalog.md) —
  `project:git_config_risk` event contract (`reason` / `diff` payload fields).
- [../../SECURITY.md](../../SECURITY.md) — threat model "App-Spawned
  Subprocess Hardening (Git)" section and accepted trade-offs.
