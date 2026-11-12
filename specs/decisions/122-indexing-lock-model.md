# ADR-122: Staged Indexing Lock Acquisition — Search Never Freezes Behind a Pass

## Status

Accepted

## Context

The service write lock (`Service.mu`) protected a full indexing pass end to end: `IndexFull` and `IndexIncremental` acquired it before their pipelines and released it on exit. Everything sharing that lock froze for the pass — which on a large workspace is minutes of walk, read, hash, ONNX inference and commit:

1. **Search froze.** Every query takes `mu.RLock` for its whole duration (the lexical index has no internal synchronization, so the search must hold the read lock across its queries), and it first crosses `WaitReady`. A pass made both gates wait.
2. **Readiness added a second freeze layer.** A pass called `MarkNotReady` at entry and `RestoreReady` on exit, so callers that never touch `mu` — the `semantic_search` tool path among them — blocked in `WaitReady` for the entire pass, even over an already-built index.
3. **Close, branch switch, project switch and the No Project reset queued** behind the pass; the background reset applier's whole design exists because those holders were long.

The whole-pass hold was also load-bearing, and any rebuild must re-provide it:

- **Pass-vs-pass exclusion.** The manager's debounce guard explicitly does not serialize passes (its value is coalescing plus cancellation safety); two passes running concurrently — an init pass and a watcher pass, a branch-switch pass and a reindex — could only be serialized by the write lock itself.
- **Displacement safety.** A concurrent `SwitchBranch`, project switch, park eviction or `Close` used to block until the pass finished, so a pass could never commit into a swapped or closed working store. The nil-collection crash barrier in `commitEmbeddedDocuments` documents the one historical incident where a corpus build's store WAS closed underneath a pass — a process-killing SIGSEGV, never an error.
- **Publication ordering.** §3 chunk records land before the chromem commit that references them, a file's sidecar row lands only after every one of its chunks committed (publish-after-commit), and an incremental pass deletes before it re-indexes. The whole-pass hold serialized all of this trivially.
- **The readiness generation.** `MarkNotReady`/`RestoreReady` pair a state flip with a generation capture, so a stale pass from an outgoing project whose defer runs late can never mark a freshly switched project ready.

## Decision

Staged acquisition: a pass holds a NEW dedicated lock — the pass lease — for its whole duration, and takes the service write lock only in short identity-checked windows around its mutating steps. The lock ladder is `passLease → mu`, never the reverse; `Close`, `SwitchBranch`, `SetProject` and the reset applier take `mu` directly and never touch the lease.

- **D1 — The pass lease restores pass-vs-pass exclusion.** `IndexFull`, `IndexIncremental` and the direct accumulator drivers hold `passLease` from before their first write window until exit. A queued pass waits for the lease — exactly the mutual exclusion the whole-pass hold used to provide — while search, status and Close never wait for it. `RebuildLexical` is unchanged: it already ran staged per-window flushes (the pattern this decision generalizes), its writes are idempotent same-content upserts of deterministic IDs, and its per-window lexical identity check already aborts it when its index is replaced.
- **D2 — Every write window re-validates the working-store identity.** The pass captures `{state, collection, lexical}` under an initial short window; each mutating step — one accumulator commit window (§3 chunk records + chromem commit + per-file sidecar publishes land together), a poison-drop sidecar publish, one legacy `AddDocuments` batch, the incremental delete phase (enumeration + chromem/lexical deletion + sidecar drop), the corpus-adoption reconciliation, the final file count — takes a window that re-derives all three pointers under the lock and compares. A mismatch, or a closed service, aborts the pass with `ErrWorkingStoreDisplaced`. Inside a held window no displacement can occur (every displacer takes the same write lock), so `fn` sees a stable working store and the publication ordering is exactly what the whole-pass hold serialized.
- **D3 — Inference leaves the lock.** Embedding runs outside every window through the embedding cache captured with the identity: the cache is internally synchronized and is never torn down by state teardown (`closeStateLocked` releases lexical/db/collection handles, not the cache), and a displacement merely wastes inference — never corrupts anything. Preparation (walk, read, hash, chunk) was already lock-free.
- **D4 — The readiness gate is conditional.** A pass that enters with the service already ready over a queryable collection — the watcher-driven mid-session pass — keeps readiness UP: searches answer from the committed-so-far state between windows, which is the point of staged acquisition. A pass entered NOT-ready (fresh project, post-switch reconciliation, corpus build) keeps the historical `MarkNotReady → RestoreReady` cycle unchanged, including the generation guard and the restore-on-every-exit contract. Readiness never flickers per window.
- **D5 — A displaced pass is not a store defect.** `ErrWorkingStoreDisplaced` classifies as no-store: the repair ladder never quarantines for it (the displacing owner rebuilt or released the store), the attach path reports it honestly with its cause and replays on the next trigger, and the deferred `CloseWorkingStoreIfCurrent` cleanup of a displaced corpus build stays a no-op by its own ownership guard.

## Consequences

- Search, status, `Close`, branch switch and the No Project reset are never frozen by an in-flight pass; the semantic_search tool answers during a slowed pass from the committed-so-far state.
- A displaced pass now aborts mid-flight instead of blocking its displacer; the manager logs the failure and the watcher/next trigger reconciles — the same end state the blocking design reached, reached without freezing anyone.
- A pass over a live collection serves partial progress. That is honest (every committed window is a consistent, queryable prefix; the dual-write invariant and poison-drop bookkeeping are untouched), and the status pill keeps reporting the in-pass phase through the ordinary progress events.
- Costs: window-vs-search RWMutex handoffs add minor scheduling overhead; a commit window still briefly delays one search (the read lock must respect a writer, as before); the legacy per-document path embeds inside its `AddDocuments` window, so its windows span inference of one batch — the batched-embedder path (the production wiring) keeps inference out of every window.
- The lock ladder is now three-deep for the corpus build path (`build lease → passLease → mu`) and two-deep everywhere else; nothing acquires `mu` and then the lease, so no cycle exists.

## Alternatives Considered

- **Keep the whole-pass hold and make search read without the lock.** Rejected: the lexical index has no internal synchronization and its Close nils the underlying index under the write lock — the search's whole-duration RLock is what makes pointer capture safe; weakening it reopens the data race the hold exists to prevent.
- **Lease per window instead of per pass.** Rejected: two passes could then interleave their windows (one pass's commit between another's), and neither the delete-before-reindex ordering nor sidecar publish-after-commit survives interleaving; the lease must span the pass.
- **Drop readiness for windows only (flicker).** Rejected: readiness transitions wake/blocked `WaitReady` waiters; flickering them per window would churn waiters and re-create the freeze per window. The conditional gate keeps the pre-pass contract while letting an already-ready service stay searchable.
- **Unconditionally keep readiness up for every pass.** Rejected: a pass over an empty store (fresh project, corpus build, empty-target branch) would serve empty answers to searches that skipped the wait, and the post-switch reconciliation pass would raise readiness before the incoming project's initial pass completed — reintroducing the stale-readiness class the generation guard was built for.
- **A readers-writer split that lets commits run under RLock.** Rejected: commits mutate the sidecar map, the chromem collection and the lexical index; chromem tolerates concurrent add/query, but the sidecar map and the lexical pointer discipline do not, and duplicating their safety into per-structure locks would trade one audited ladder for several unreviewed ones.

## Related

- [ADR-120](120-vector-store-repair-ladder.md) — the repair ladder a displaced pass must not disturb (no quarantine for `ErrWorkingStoreDisplaced`; publication stays)
- [ADR-104](104-content-addressed-vector-store.md) — the publication protocol and §3 write ordering the windows preserve (chunks before tables, commit before sidecar row)
- [ADR-064](064-branch-scoped-lazy-vector-index-open.md) — the pre-open `loading` state and the honesty rule the conditional readiness gate keeps
- [ADR-049](049-vector-index-memory-reduction.md) — the bounded commit windows and the content-addressed embedding cache D3 relies on
- [ADR-036](036-vector-index-embedding-optimization-policy.md) — the inference/commit decoupling the accumulator implements across the windows
- [../domains/workspace.md](../domains/workspace.md) — the vector-index invariants (readiness generation, indexing-work seal, dual-write) this decision extends
