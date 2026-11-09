# ADR-120: Vector Store Self-Healing — Publication Invariant, Repair Ladder, Honest Failure Reason

## Status

Accepted

## Context

The vector index repeatedly surfaced as a dead end: the status bar said `Index unavailable`, the hover and the search panel explained nothing, and the remedies users tried — switching branch, reopening the project, pressing reindex — either did nothing or produced the same failure again. An audit of the store, the pass, the hygiene sweep, the status mapping and the root registry found six distinct producers of that experience, all sharing one shape: a failure was reported without enough information to act on, and the available actions could not distinguish a broken store from an unlucky moment.

1. **A publication could succeed while an open-critical leg was absent.** The v1 `corpus.json` record could be written next to a missing corpus lexical index, so `published` was true while every open failed. On the §3 path a table could be published while a chunk record it referenced was torn or swept.
2. **A rebuild could fail identically.** A pass that indexed zero files still published an empty corpus, and a pass whose walk lost a subtree could not tell "the tree genuinely has no indexable files" from "this pass did not see the tree". Repeating the rebuild reproduced the same empty result.
3. **Transient faults and proven store defects were one class.** `EACCES`, `ENOSPC`, `EMFILE`, `ENFILE`, `EIO`, cancellation and a genuinely torn corpus all looked like "the store is broken", so any repair policy either destroyed healthy state or refused to act.
4. **Hygiene could race a live build.** The orphan-chunk sweep and the disk-budget eviction could remove count-zero chunk records while a corpus build held a build lease and was about to publish a table referencing them — manufacturing the tear the next open would report.
5. **Status collapsed distinct states into `unavailable`.** The embedder still loading, an LRU-eviction drain and a shutting-down registry all rendered as a failure, and a failure could be emitted with no cause at all (a pass-level report carries only a state).
6. **Registry identity was lexical.** An aliased or symlinked spelling of a project directory produced a different manager key and a failed project lookup (`not a registered project checkout or managed worktree`), and when duplicate project rows claimed one path the winner depended on listing order.

[ADR-104](104-content-addressed-vector-store.md) already established the shared store's publication protocol and a first repair path for attach; this decision completes that protocol into a whole-system contract: publication is a promise the open can keep, repair is a bounded ladder, and every non-ready status explains itself.

## Decision

### D1 — Publication means "every leg an open reads is present", on write and on read

- `PublishCorpus` refuses to write a publication whose open would fail: a corpus lexical leg that is not live yields `ErrCorpusLegMissing`, wrapped around the recorded `lexical.Open` cause and naming the directory. A store without lexical persistence (the in-memory No Project service) is exempt because its open does not read a lexical leg.
- A damaged or missing chunk record is re-embedded from the build store's own document in the same pass (`writeSnapshotTable`'s repair hook), then re-verified. A transient fault defers publication without touching the record.
- An empty index is never published — neither the §3 table nor the v1 manifest. `ErrCorpusNoIndexableFiles` names the actionable cause (`check exclude patterns and ignore files`); when the emptiness came from an incomplete walk, `ErrCorpusWalkIncomplete` names the skipped entries instead.
- On read, a corpus missing any leg the open requires is treated as UNPUBLISHED. The next `EnsureCorpus` finds the target occupied, resets exactly that snapshot's legs and rebuilds from source, cache-spared. The v1 lexical leg is covered by the same rule as the §3 table.

### D2 — The repair ladder has exactly one automatic step

1. **Transient rung — no repair.** A fault that proves nothing about the store leaves the publication byte-identical and reports `unavailable` with kind `transient`. Retrying is a later attempt, not a repair.
2. **Attach rung — one self-heal.** An attach failure with a proven store defect (`Repairable`) quarantines exactly the store its failing STAGE names — the shared corpus or the root's private overlay — and retries exactly once. `QuarantineScopeOf` reads the scope from the stage tags (`ErrCorpusUnopenable` / `ErrOverlayUnopenable`), never from message text.
3. **User rung — explicit rebuild.** `ReindexVectorIndex` is the escape hatch for a terminal `unavailable`: with no live collection the manager re-attaches the snapshot view, the publication predicate unpublishes the unopenable corpus, and the corpus is rebuilt from source (cache-spared). In legacy (corpus-disabled) mode the action remains a logged no-op because there is no shared store to rebuild; the ordinary path cancels the in-flight pass and reconciles incrementally, falling back to a full build only for a legacy empty collection.
4. **No further rungs.** A defect the one retry cannot clear fails closed, binds nothing (no other snapshot's corpus, no foreign-era data) and reports its cause. The loop is structurally impossible: the attach pass contains no repair logic and is invoked at most twice.

### D3 — Transient ≠ defect; quarantine only what is proven

The store layer owns one taxonomy: `Repairable(err)` is true for proven defects (`fs.ErrNotExist`, `io.EOF`, `io.ErrUnexpectedEOF`, torn chunk records and the corpus sentinels) and false for OS-level transients (`context.Canceled`, `context.DeadlineExceeded`, `fs.ErrPermission`/`EACCES`, `ENOSPC`, `EMFILE`, `ENFILE`, `EIO`) and `nil`. `Transient` is the same checklist exposed for status classification; `Repairable` consumes its first tier so the two never disagree. Cancellation destroys nothing. Degenerate-workspace outcomes (`ErrCorpusNoIndexableFiles`, `ErrCorpusWalkIncomplete`) map to `QuarantineNoStore`: rebuilding the store cannot change what the tree contains, so a second attempt would only repeat the first.

### D4 — Quarantine is selective

The corpus and overlay scopes are disjoint:

| Scope | Removes | Never touches |
| ----- | ------- | ------------- |
| Corpus | the §3 `table.json` (the removal IS the unpublish), its occurrence table, the snapshot store and lexical leg, the v1 chromem root (including `corpus.json`), the per-key lexical index, the file-hash sidecar and the content-less marker | `chunks/`, `embedding_cache/`, any other snapshot, `refs/` |
| Overlay | that root's branch root, lexical index, sidecar, content-less marker and §3 `roots/<fp>` state directory | the shared corpus, `chunks/`, `embedding_cache/`, other roots |

`chunks/` records are content-addressed and shared by every snapshot and overlay; `embedding_cache/` entries are re-derivable inference work. Neither can be one store's private damage, so no scope deletes either. The corpus scope runs under the corpus build lease, so a repair never races the single writer.

### D5 — Walk skip semantics: gaps are data, not failures

An error below the walk root (a subtree deleted or unreadable mid-walk) is recorded as a gap and the walk continues. Only an unreadable root aborts the pass. Per pass, the service records the gap report (only if the working store is still the one the pass belongs to) and logs exactly one aggregated WARN naming the skipped count, the first path and the cause. The publication decision reads that report: a clean empty walk is `ErrCorpusNoIndexableFiles`, an empty result with gaps is `ErrCorpusWalkIncomplete`. The gap cause is rendered as text, never wrapped with `%w`, so `errors.Is(fs.ErrNotExist)` cannot reclassify a property of the WORKING TREE as damage to the STORE.

### D6 — Hygiene never reaps under a live build

The §6 orphan-chunk sweep and the count-zero half of the disk-budget eviction are suppressed while any corpus build lease is live; the lease set is sampled before the sweep and re-sampled before eviction. The ledger reports the skip honestly (`ChunkReapSkipped`, zero orphans that pass). Budget eviction still evicts owner-unpinned snapshot directories (derived state, re-derived by the next attach) but leaves the count-zero chunk records and their bytes accounted, so the ledger stays walk-honest.

### D7 — Every non-ready status explains itself

- `Manager.IndexStatus` carries `LastError` and `ErrorKind`; the wire `VectorIndexStatus` carries `last_error` / `error_kind` (both `omitempty`). `notifyInitFailure` records the pair before invoking the failure callback, and `wrapProgress` fills a defensive `unknown` reason when an `unavailable` progress report carries none. A successful attach, a `loading` publication or a `ready` report clears the pair — a stale cause never outlives its failure.
- `ClassifyInitError` maps the taxonomy onto `repairable` (a store rebuild is the classifier's remedy) / `transient` (retry later) / `fatal` (neither is promised) / `unknown` (no error accompanied the state). The registry adds `no_target` (nothing to search: foreign or absent root, No Project), `shutting_down`, and the explicit NON-failure windows `loading` and `restarting`. A degenerate-workspace reason never earns a destructive repair even when its sentinel sits in the store family — `QuarantineScopeOf`'s no-store rule (D3) decides that, and the actionable reason is what the surface shows.
- `unavailable` always carries a non-empty reason and a kind; `loading` and `restarting` are never `unavailable` (the index was never broken); `disabled` carries no cause because the master switch IS the explanation. No Project reports `unavailable` with `no_target` and the shared No-Project reason.

### D8 — Registry identity is physical, and duplicate records resolve deterministically

- The per-root registry keys roots through `canonicalRoot`: `filepath.Clean` plus a best-effort `filepath.EvalSymlinks`, falling back to the lexical form when the path cannot be resolved yet. Every spelling of one existing directory folds onto ONE key and therefore ONE manager.
- Project lookup compares resolved forms on both sides: `canonicalRoot(project.WorkspacePath)` against the resolved root for the checkout pass, and the managed-worktree name derived from the resolved forms for the worktree pass.
- When several project records claim one root, `pickRootProject` gathers all claimants and applies a total order independent of listing order: checkout over managed tree, exact path spelling over resolved-only, freshest `last_active_at`, project ID as the final tie-break. The choice is logged at WARN with the full `ID=path` candidate list.
- A root that is neither a registered checkout nor one of its managed trees still returns `errVectorNoTarget` (`no_target`), so foreign paths stay out rather than routing to a guess.

## Consequences

- The user-visible dead end is gone: every broken index names its cause and its kind, and the reindex action is a real repair that needs no branch switch or project reopen. A proven store defect heals automatically once per attach; a transient fault stays non-destructive.
- Expensive shared state is protected: a single store's damage costs one snapshot or overlay rebuild, never a full re-embed of every root, and never a shared chunk or embedding-cache wipe.
- Costs and limits: `canonicalRoot` now performs filesystem IO (one stat chain) on registry paths; a repairable defect pays an extra attach attempt; a fatal defect is not auto-retried, so the user must act; quarantine may delete derived snapshot/overlay state and the rebuild time is real.
- The invariant makes corruption detectable and recoverable; it does not prevent the producer of corruption. A recurring damage source still deserves a root-cause fix rather than reliance on repair.

## Alternatives Considered

- **Retry-with-backoff on every attach failure.** Rejected: a proven store defect is deterministic, so retries multiply expensive rebuilds and delay the honest reason; a transient fault is a "try later", not "try again now", and backoff hides the cause the user needs. Escalation is one user-triggered repair, not an automatic retry loop.
- **Auto-delete-always (quarantine any failing store).** Rejected: an OS-level fault (`EACCES`, `ENOSPC`, `EMFILE`, `ENFILE`, `EIO`, cancellation) proves nothing about the store; deleting a healthy publication on a transient fault destroys shared, expensive state and can hide a real environmental problem.
- **Silent no-op (keep `ready`, or do nothing on a failed attach/reindex).** Rejected: that is the defect being fixed — a stuck `unavailable` index with a disabled remedy and no explanation. A no-op reindex leaves the user without an escape hatch and without a reason.
- **"Switch branch / reopen project" as the remedy.** Rejected: for a cached manager neither action re-runs the attach, and a durable corpus defect survives both. Presenting them as a fix is an illusion and disrupts the workspace; the explicit rebuild is the repair.
- **Full wipe including `embedding_cache/` (and `chunks/`).** Rejected: chunk records are content-addressed and shared by every snapshot and overlay, and embedding-cache entries are re-derivable inference work. Wiping them charges every other consumer a full re-embed for one store's damage. Quarantine deletes only the private leg and rebuilds cache-spared.
- **Automatic full wipe and reindex at startup.** Rejected: turns every launch into a multi-minute rebuild, hides the underlying cause, and destroys shared data on transient faults — the same objection as auto-delete-always with a periodic trigger.

## Related

- [ADR-104](104-content-addressed-vector-store.md) — the shared store's identity model, publication protocol, attach contract and §3/§5/§6 machinery this decision completes
- [ADR-081](081-vector-per-root-registry.md) — the per-root manager registry whose keys and project lookup D8 makes physical
- [ADR-064](064-branch-scoped-lazy-vector-index-open.md) — the pre-open `loading` state and the honesty rule every non-ready status obeys
- [ADR-103](103-vector-index-master-switch.md) — the `disabled` state, which is a user choice rather than a failure
- [ADR-049](049-vector-index-memory-reduction.md) — the content-less store and the content-addressed embedding cache that quarantine deliberately spares
- [../domains/workspace.md](../domains/workspace.md) — the vector-index behavior and invariants
- [../contracts/desktop-frontend.md](../contracts/desktop-frontend.md) — `VectorIndexStatus`, `GetVectorIndexStatus`, `ReindexVectorIndex`
- [../domains/frontend/loading-states.md](../domains/frontend/loading-states.md) — the status-bar pill, the inline reason and the repair action
