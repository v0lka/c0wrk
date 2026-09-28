# ADR-070: Git Config Trust Binds to a Semantic Fingerprint (Inert-Drift Tolerance, Filtered Diff, Fail-Closed Migration)

## Status

Accepted (amends [ADR-034](./034-git-trust-opt-out.md); the raw snapshot and
fingerprint remain the byte-level identity of a trust decision, while the
trust *lifecycle* rebinds to the semantic fingerprint defined here)

## Context

ADR-034 bound a trust decision to the **raw byte snapshot** of every config
source the scanner read — the SHA-256 of the exact bytes the user reviewed —
and evicted the trust on any byte-level drift. That binding is airtight
against hostile drift, but it made the trust lifecycle brittle against
*legitimate* churn, and two real-world problems surfaced in daily use:

1. **Inert churn false-evicts the trust.** Ordinary git usage rewrites
   `.git/config` in ways that cannot change how c0wrk's git operations
   behave: every `git pull` / `git branch --set-upstream-to` refreshes the
   `[branch "<name>"] remote`/`merge` remote-tracking bookkeeping, and users
   edit `[alias]` freely. Both are harmless for c0wrk — it never invokes git
   through an alias name (its spawn layer always invokes git by subcommand
   with a fixed argv), and branch bookkeeping redirects nothing git reads.
   Under the raw binding, any such change evicted the trust, re-hardened the
   repository, and re-emitted `project:git_config_risk` with a diff of pure
   noise. That is approval fatigue ([ASI09]) by construction: a warning that
   fires on every routine `git pull` trains the user to dismiss warnings
   unread — including the one that eventually carries a real, dangerous
   delta.
2. **The drift diff does not separate signal from formatting.** The eviction
   diff diffed raw snapshots — git-normalized, reordered, whitespace-shifted
   — so when the trust *did* evict, the meaningful delta could drown in
   formatting churn, and the diff of an inert change looked exactly like the
   diff of a planted `core.fsmonitor`.

A related, quieter defect sat in ADR-034's migration posture: entries written
by pre-fingerprint builds were carried forward with an empty fingerprint and
"keep suppressing the warning unconditionally until re-trusted" — a fail-open
hole of exactly the kind the snapshot binding existed to close.

This decision rebinds the trust lifecycle to what the user actually reviewed —
the **meaning** of the configuration, not its bytes — while preserving the
fail-closed posture in an even stronger form.

[ASI09]: ../../SECURITY.md

## Decision

The trust decision stores and tracks **two identities** of the configuration:
the raw byte-level fingerprint (unchanged from ADR-034) and a new **semantic
fingerprint** — a stable identity for the *dangerous* content of the config.
The trust lifecycle (recheck, eviction, warning) keys on the semantic
identity; the raw identity rides along as the record of the bytes the user
reviewed.

### 1. Semantic snapshot and fingerprint — `core/workspace`

`GitConfigInfo` gains two primitives alongside `Snapshot`/`Fingerprint`:

- `SemanticSnapshot() []byte` — a canonical, diff-able serialization of the
  **DANGEROUS subset** of the scan, in the same `===== kind (path) =====`
  format as the raw snapshot. Config layers (kinds `config` and
  `config.worktree`) are re-rendered as one canonical single-line record per
  DANGEROUS record (`[section "sub"] key = value`, values escaped so a
  hostile value cannot span or forge lines; every scanned layer keeps its
  header, so diff attribution stays stable even for a layer that currently
  carries no dangerous records). The sources that are dangerous **as a
  whole** — the attribute-routing files (`.git/info/attributes`,
  `core.attributesFile`; no per-key neutralization exists) and the include
  targets (which the scanner deliberately does not key-scan, [ADR-033]) —
  are embedded verbatim.
- `SemanticFingerprint() string` — the SHA-256 hex digest of
  `SemanticSnapshot()`.

The inert/dangerous split is an **explicit, tiny, closed allowlist**:
`semanticInertSections` names exactly `branch` and `alias` (matched
case-insensitively; subsections ignored — `branch.<name>.*` is inert whatever
the branch is called). A record whose section is on the list is INERT and is
left out of the semantic identity; **every other record — every
command-bearing key, the structural `core.worktree`, include/includeif
directives, and any key c0wrk does not know about — classifies DANGEROUS,
fail-closed.** The allowlist must stay closed: it may only shrink to match a
verified inertness argument, never grow casually.

### 2. Trust stores both identities — `backend/config` + `backend/frontend_api_gitconfig_risk.go`

`TrustedGitRepo` gains a `semantic_fingerprint` field alongside the existing
`fingerprint`. Load-time validation (`validateGitSemanticFingerprint`)
requires it to be empty (a to-be-migrated record) or 64-character hex — a
malformed value could never match a computed fingerprint, so it is rejected
at load instead of silently evicting every open. `TrustGitRepo` computes both
fingerprints and writes **both** content-addressed snapshot files (raw +
semantic) under `~/.c0wrk/git-config-snapshots/`; either write failure
refuses the trust (fail-closed, as before).

### 3. Recheck keys on semantics — the three-path recheck

`recheckTrustedGitRepo` (invoked on every open of a trusted root) compares
the fresh scan against the stored identities:

- **Semantic match + raw match** → the configuration is byte-identical to
  what the user trusted → nothing is emitted; the repository stays
  raw-git-eligible.
- **Semantic match, raw differs** → the delta is confined to the inert
  allowlist **by construction** (anything else would have changed the
  semantic fingerprint): the churn cannot affect how c0wrk's git operations
  behave. The trust stands and the stored identity follows the bytes:
  `refreshTrustedGitRepoRawSnapshot` writes the new raw snapshot, updates
  the entry's raw fingerprint, persists the config, and logs at Debug. **No
  event is emitted** — the user trusted a meaning, and the meaning did not
  change. The semantic snapshot is rewritten too (content-addressed, so the
  write is a no-op unless a previous write was lost, in which case it heals
  the store). A concurrent eviction is detected and respected (the refresh
  is a no-op when the entry vanished).
- **Semantic differs, or the config can no longer be scanned** (fail-closed,
  as in ADR-034) → the trust is evicted (`RemoveTrustedGitRepo` + registry
  sync via `evictTrustedGitRepo`, returning the root to hardening) and
  `project:git_config_risk` fires with the drift reason and a diff of the
  **semantic** snapshots (`DiffGitConfigSnapshots` over the stored and
  current semantic snapshots) — inert churn cannot appear in the diff by
  construction, so the diff the user reviews contains only what changed in
  meaning. The drift findings render from the fresh scan (`(config
  unreadable)` when the scan failed, `(config changed)` when the fresh
  config is no longer dangerous — the drift itself is still material because
  the trust was bound to the old meaning — or the scan's dangerous-key
  findings otherwise).

### 4. Fail-closed v1→v2 migration

Records written before the semantic layer (a raw fingerprint but no semantic
fingerprint, or the legacy bare-path form with neither) are migrated on their
next recheck by `migrateTrustedGitRepoRecord`. The semantic fingerprint is
**recovered from the stored raw snapshot, not from the repository**:
`SemanticFingerprintFromSnapshot(raw)` reconstructs the semantic snapshot
from the stored bytes — a pure function of those bytes (config layers
re-parsed by the same `parseGitConfigData` and re-serialized as canonical
DANGEROUS record lines; attribute-routing and include sections passed
through verbatim — the exact split `SemanticSnapshot` applies at scan time,
byte-verified identical to the originating scan) — because the recovered
identity must describe the bytes the user actually trusted, not whatever the
repository holds today. The raw snapshot must still hash to its
content-addressed name (a corrupted store cannot vouch for itself). The
splitter fails closed: a non-empty snapshot must start with a well-formed
scanner-written header and carry only scanner-written kinds; garbage is an
error, never an identity.

- **Success:** the semantic fingerprint is persisted onto the entry
  (`configMu`-guarded, atomic persist) and the semantic snapshot is stored
  content-addressed; the record then behaves as a v2 record (inert drift
  refreshes silently, semantic drift evicts).
- **Failure** — a legacy bare-path record (no snapshot), a missing or
  corrupted snapshot, or content the splitter cannot interpret — **evicts
  the trust** and emits `project:git_config_risk` with the new
  `gitConfigTrustUnverifiableReason` and a `(trust unverifiable)` finding.
  This deliberately **replaces ADR-034 §6's unconditional legacy
  suppression**: a trust whose recorded evidence can no longer be
  interpreted is not carried forward on faith — it re-warns, and the user
  re-trusts against a fresh scan.

## Consequences

**Positive**

- Routine git usage no longer evicts trust: `git pull`, branch tracking
  updates, and alias edits keep a trusted repository trusted, so the drift
  warning regains its signal value — when it fires, something semantic
  changed.
- The eviction diff is a semantic diff by construction: the user reviews
  what changed in meaning, never formatting churn.
- The fail-open legacy-migration hole (unconditional suppression by
  pre-fingerprint records) is closed: unverifiable records evict and
  re-warn, strengthening the fail-closed posture ADR-034 established.
- Load-time validation of the semantic fingerprint converts a silently
  mismatching record (permanent, invisible eviction on every open) into a
  visible config error.
- The semantic snapshot doubles as a human-readable canonical view of the
  dangerous config content, which is exactly what a re-trust decision should
  be made against.

**Negative / accepted trade-offs**

- The silent refresh rebinds the stored raw snapshot without user
  involvement. Accepted: the delta is provably inert for c0wrk's behavior by
  construction of the allowlist, the refresh is Debug-logged, and the
  alternative — re-warning on every `git pull` — is the approval-fatigue
  failure this decision exists to remove.
- Two identities and two snapshot files per trust decision. Accepted: both
  are small, content-addressed, and shared across entries that fingerprint
  identically; the raw identity remains the auditable record of what the
  user saw.
- The migration re-derives the semantic identity from stored bytes through a
  re-parse; a hypothetical divergence between the scan-time and
  migration-time parsers would yield a fingerprint the next scan would not
  reproduce. Mitigated structurally: both paths share the same pure parser
  (`parseGitConfigData`) and serializer, and a byte-exact roundtrip test
  (`TestSemanticFingerprintFromSnapshotRoundTrip`) pins the equivalence
  across the full source mix; any parser change must keep the roundtrip
  green or deliberately migrate (the fail-closed eviction is the fallback).
- The inert allowlist is a trust assertion about whole sections: `alias.*`
  values are arbitrary shell strings. The inertness argument is specific to
  c0wrk — it invokes git by subcommand with a fixed argv and never expands
  user aliases — and the alias values remain visible in the raw snapshot the
  user reviewed. Any future c0wrk behavior that might expand an alias or
  consult branch bookkeeping invalidates the allowlist entry and requires
  revisiting this decision.

## Alternatives Considered

- **Keep the raw-only trust (status quo ante).** Rejected: every legitimate
  `git pull` re-warned, which trains dismissal (ASI09) and makes the drift
  warning indistinguishable from noise — the binding was airtight but
  unusable.
- **Keep the raw fingerprint as the trigger and filter inert sections out of
  the eviction diff only.** Rejected: the false eviction (and its re-hardening
  of the repository) still happens; a filtered diff is cosmetic relief on a
  still-broken lifecycle.
- **A broader inert allowlist (e.g. every non-command-bearing key).**
  Rejected: the allowlist's safety rests on being tiny and closed. Keys whose
  effect c0wrk has not analyzed (and unknown future keys) must classify
  dangerous; a broad allowlist re-opens the "unclassified drift silently
  re-trusted" failure ADR-034 rejected.
- **Re-affirm prompts for inert churn ("the config changed, keep trust?").**
  Rejected: a confirmation the correct answer to which is always "yes, it's
  just a pull" is the same approval fatigue in a different costume.
- **Migrate by re-scanning the repository instead of the stored snapshot.**
  Rejected: the recovered identity must describe the bytes the user trusted.
  The repository may have drifted since the trust decision; re-scanning would
  stamp the *current* config as the trusted meaning without the user having
  reviewed it. Deriving from the stored snapshot keeps the migration honest
  and makes store corruption fail closed.
- **Auto-re-trust on any drift (revisit ADR-034's rejection wholesale).**
  Rejected — and this decision does not do it. The ADR-034 rejection stands
  for every drift class the model cannot *prove* inert (unclassified drift —
  anything outside the closed allowlist, including all semantic drift); only
  the provably-inert class is refreshed, and only because its delta cannot
  affect c0wrk's git behavior by construction.

## Related

- [ADR-033](./033-git-subprocess-hardening.md) — the hardening layers the
  trust opts out of; the include-posture this decision's verbatim embedding
  of include targets inherits.
- [ADR-034](./034-git-trust-opt-out.md) — the trust opt-out this decision
  amends: the raw snapshot/fingerprint as the byte-level record of the trust
  decision, the recheck on every open, and the fail-closed eviction and
  trust-time refusal all remain; the lifecycle key (raw bytes → semantic
  fingerprint), the recheck's shape (two-path → three-path), and the
  migration posture (unconditional legacy suppression → fail-closed
  v1→v2 migration) change.
- [ADR-059](./059-commit-hooks-signing-gate.md) — the commit gate, unchanged;
  trusted repositories keep committing raw.
- [../architecture/security-model.md](../architecture/security-model.md) —
  Git Subprocess Hardening, layer 5 (User trust opt-out).
- [../domains/workspace.md](../domains/workspace.md) — Git Integration: the
  scanner primitives and the recheck wiring.
- [../contracts/event-catalog.md](../contracts/event-catalog.md) —
  `project:git_config_risk`: the `reason`/`diff` payload now carries the
  semantic diff, plus the `(trust unverifiable)` finding marker.
- [../../SECURITY.md](../../SECURITY.md) — threat model "App-Spawned
  Subprocess Hardening (Git)" and the accepted trade-offs table.
- `core/workspace/gitconfig.go` (`SemanticSnapshot`, `SemanticFingerprint`,
  `semanticInertSections`, `classifyGitConfigRecord`,
  `SemanticFingerprintFromSnapshot`),
  `backend/frontend_api_gitconfig_risk.go` (`recheckTrustedGitRepo`,
  `migrateTrustedGitRepoRecord`, `refreshTrustedGitRepoRawSnapshot`,
  `evictTrustedGitRepo`, `gitConfigTrustUnverifiableReason`),
  `backend/config/config.go` (`TrustedGitRepo.SemanticFingerprint`,
  `validateGitSemanticFingerprint`).
