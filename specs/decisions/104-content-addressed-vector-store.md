# ADR-104: Content-Addressed Vector Store — Identity, Shared Ownership, Residency

## Status

Accepted (identity model IMPLEMENTED — root-relative DocumentID/lexical coordinates, branch generation manifests with sequential-era reset, sidecar v6 root scoping, per-request content hydration, per-root ignore/eligibility; owner/view stage 2 IMPLEMENTED — shared snapshot corpora keyed by commit OID, per-root overlays, single-flight builds, read-only corpus handles, pre-fusion visibility filtering, fail-closed attach, legacy promotion by rename; migration stage 3 IMPLEMENTED — the unified background legacy-layout sweep (`store_migration.json` v1: incremental per-unit progress, terminal skip records with reasons, done marker only after the recognized volume settles, replay-safe against crashes/cancellations/transient failures) drains the pre-ADR-064 multi-collection root, the ADR-064 per-branch stores of the shared root and every legacy ADR-081 per-worktree store into the stage-2 corpus+overlay shape by zero-embedding renames under per-snapshot build leases, with honest rebuild-fallback reasons (era mismatch, missing sidecar hash map, missing content-less marker, absent branch, unpublished-target collision) and unknown/corrupted directories left in place and inventoried; residency stage 4 IMPLEMENTED over the stage-2 shape — the owner's count+byte-bounded retention LRU of zero-reference corpus handles (`vector_index.corpus_residency_capacity` / `corpus_residency_budget_mb`, default off), refcount reader pinning, oldest-eligible eviction with safe reopen, memory-free outside the lock, DeleteProjectData/Close teardown, no parallel branch cache; reachability-GC stage 5 IMPLEMENTED over the stage-2 shape — on-disk ownership records under `refs/` (root markers `root_<fp>.ref` written as intent pins by every view attach, branch references `bref_<sanitized>.ref` written by tip-attaches and the migration's promotions), the switch sweep and the delete-triggered purges assemble shared-data lifetime from OWNER REACHABILITY (live markers ∪ surviving branch refs ∪ the registry's held handles and build leases; `ReclaimKeys` closes doomed corpora's residency slots first — Windows handle ordering), detaches remove exactly their own records and delta (branch → its `bref_` + artifacts; root → its marker + overlay), a `refs/`-less store keeps everything (conservative upgrade posture), unknown shapes and `_orphan` are never auto-deleted, and the release paths carry a resurrection guard; verification stage 6 EXECUTED — the §8 measurement contract is published with its first full baseline in `core/vectorindex/BENCHMARKS.md` (2026-11-03); §3/§5 COMPLETION IMPLEMENTED — the full chunk-record format is the store's canonical durable tree (`store/generations/<n>` with `format:2` manifests and the atomic-rename CURRENT pointer, content-addressed `chunks/<aa>/<C>.chunk` records carrying dims×f32-LE vectors with no text, per-snapshot `occurrences.tab`/`table.json` where the header is the completion marker, per-snapshot `lexical/` directories, `refs/`, and root `overlay.tab`/`overlay-lexical`/`sidecar.json`/`view.json` state; chunks-before-tables write ordering everywhere; an unknown manifest format reads the whole tree as ABSENT; the §3 read path opens a published snapshot with zero chromem decode and the v1 corpus remains the fallback), the §5 disk ledger is walk-computed truth charged once per identity level (unique chunk bytes counted once regardless of reference count, per-snapshot table+lexical bytes, per-root overlay bytes, whole superseded generations; surfaced in `GetVectorIndexStatus.store`; `vector_index.store_disk_budget_mb`, default off, evicts LRU owner-unpinned snapshot directories under pressure), the §6 orphan-chunk refcount sweep rides the switch/delete/migration slots silently and single-flighted (referenced set = published snapshot tables ∪ overlay tables, an mtime grace window protects in-flight writers' deferred flushes, superseded generations are removed as atomic units guarded by open handles, unreachable §3 snapshots are reclaimed leg-together with their v1 stores, and the migrated v1 chromem legs are reaped once the chunk-record migration's project marker closes the rollback window), and the v1→v2 chunk-record migration COMPLETES the upgrade (per-snapshot, marker-gated by table.json, replay-safe: each published v1 corpus converts with ZERO inferences — every legacy document's vector MOVES from `DocumentID(path, i)` to the chunk record keyed by the re-chunked text's `C` under the corpus manifest's blob-hash and chunker-fingerprint gates; a source mismatch defers the whole snapshot — the all-or-nothing strengthening of §3's per-file drop that keeps a §3 table from ever covering fewer files than the v1 corpus served; the project `store_chunk_records.done` marker lands only after every published corpus settled). All roadmap stages of this ADR are implemented; nothing remains deferred)

## Context

Three accepted ADRs shaped the current store, each fixing the worst problem of its
day, and each leaving a structural multiplier behind:

- [ADR-049](049-vector-index-memory-reduction.md) made documents content-less and
  bounded the park LRU by bytes — but a parked state is still a **whole chromem
  DB per branch**, so N branches of the same corpus hold N copies of the same
  vectors.
- [ADR-064](064-branch-scoped-lazy-vector-index-open.md) made the open
  branch-scoped, killing the ×branches open cost — but the branch remains the
  STORE unit: a new branch created from an already-indexed commit re-walks the
  whole corpus (the embedding cache spares inference, not the walk, re-chunk,
  commit, and per-branch disk copy), identical bytes at two paths are embedded
  once but stored as two unrelated documents, and a detached HEAD is bolted on
  as a pseudo-branch name (`rev-parse --short=12`).
- [ADR-081](081-vector-per-root-registry.md) gave every workspace root its own
  Manager — but worktrees get a **fully isolated storage root** precisely
  because two chromem persistent stores cannot share a directory, so a managed
  worktree of the same repository re-indexes the same content from scratch.

The residual multipliers are all one disease: **the store keys data by place
(branch name, root path), not by content**. Everything the subsystem wants to
deduplicate — chunks, vectors, whole tree states — is a pure function of bytes
plus a small set of parameters, and the current layout has no unit that says so.

This ADR defines the replacement store: a content-addressed chunk store with
per-snapshot occurrence tables, one shared owner per project store, read leases
for concurrent roots, and honest count/byte accounting. It is written BEFORE any
code change; the roadmap implements it in later steps. The reproducible
measurement contract lives in [`core/vectorindex/BENCHMARKS.md`](../../core/vectorindex/BENCHMARKS.md)
(multi-branch/multi-root protocol section).

## Decision

### 1. Four identity levels (the store's type system)

Every artifact in the store is keyed at exactly one of four levels; nothing is
keyed by branch name or root path at storage level:

| Level | Identity | Key | Mutable | Examples |
|---|---|---|---|---|
| **L1 content/chunk** | normalized chunk text | `C = sha256(normalized chunk text)` | never (immutable, deduplicated globally per store) | vector record, embedding-cache entry (already keyed this way) |
| **L2 file occurrence** | (snapshot S, path P) | `(S, P)` | per-snapshot immutable | `P → blob hash + [(C, startLine, endLine, language)]` row |
| **L3 snapshot/view** | indexed tree state | `S = commit OID` (git) / synthetic opaque ID (non-git) | immutable once published; aliases are cheap | occurrence table, per-snapshot lexical index, branch ref pointer |
| **L4 root-local state** | (project, workspace root) | sanitized root path | root-owned, always | dirty/untracked overlay, sidecar fast-path bookkeeping (size/mtime), watcher, pinned ref + view epoch |

Rules:

- **L1 is the only duplication boundary.** Two occurrences of identical bytes —
  at two paths in one snapshot, on two branches, in the checkout and a managed
  worktree, committed vs dirty — reference the SAME chunk records. The vector
  exists once; every occurrence remains individually searchable (both lexical
  entries exist, both score, both resolve).
- **L2 is preserved, not deduplicated away.** A search hit is an occurrence
  (`(S, P, chunk range)`), never a bare chunk: the UI and the content
  reconstruction (ADR-049's best-effort resolver) need a path and line range.
  Content addressing deduplicates STORAGE; it never collapses USER-VISIBLE
  occurrences.
- **L3 is keyed by commit OID, not branch name.** A branch is a REF: a tiny
  pointer file `refs/<sanitized-name> → commit OID`. Consequences:
  - **A new branch created from an already-indexed commit is indexed by writing
    one pointer file** — no walk, no chunk reads, no corpus pass. First correct
    search on the fork costs one occurrence-table open (the table already
    exists under the commit's key).
  - **Detached HEAD is first-class:** the checked-out commit IS the snapshot;
    no pseudo-branch name is fabricated (today's `rev-parse --short=12` name
    forks identity and survives as a stale branch directory after the checkout
    moves on).
  - Two branches at the same commit share tables and lexical state exactly.
- **L4 holds everything that genuinely is root-local.** The working tree is
  usually dirty: uncommitted edits and untracked files are overlay entries OWNED
  BY THE ROOT (`roots/<name>/overlay.tab`), never written into the immutable
  commit snapshot. A root's effective view = `snapshot(S_ref) ∪ overlay(root)`.
  The commit snapshot therefore stays immutable and shareable across roots,
  branches, and sessions while each root's dirty state stays private and
  crash-rebuildable (re-derived by a stat+hash diff against the snapshot table).

### 2. Fingerprints and compatibility boundaries

Three fingerprints partition the parameter space; each invalidates exactly one
layer:

| Fingerprint | Covers | Baked into | On change |
|---|---|---|---|
| **E** (embedding) | model bytes, tokenizer bytes, max sequence length, output dims, normalization algorithm — the existing `EmbeddingFingerprint`, unchanged | store generation manifest (L1) | new store generation; occurrences and lexical survive; re-embed cost only (cache is already E-keyed) |
| **K** (chunker) | max chunk size, overlap, content-filter policy — the existing sidecar v5 chunker fingerprint, promoted to the table level | each snapshot/overlay table header (L2/L3) | tables re-derived by re-chunking sources; chunk records whose text still hashes to an existing `C` are reused as-is |
| **I** (ignore policy) | honoured ignore-file set, exclude patterns, submodule flag, size/chunk caps | each snapshot/overlay table header (L2/L3) | membership re-walk (the live `UpdateIndexingSettings` semantics of ADR-103 are preserved); occurrences of still-walked files survive |

Boundary rule: **cross-fingerprint reuse is by content hash only.** A chunk
record never serves a query under a mismatched E; a table never loads under a
mismatched K or I — it is rebuilt. The store never mixes generations on a read
path. Because E changes are app-update events (the model ships with the build),
they are rare and explicit; K/I changes are user settings and cheap.

### 3. Storage format and versioning

Layout under the project's existing storage root `~/.c0wrk/projects/<id>/vector_index/`
(all roots of the project share it; `safeio.MkdirAllReal`/`ensureRealDir` —
real-directories-only — governs every level):

```
store/
  generations/<n>/                 # E-scoped; exactly one CURRENT generation
    manifest.json                  # {format:2, embedding_fp E, dims, created}
    chunks/<aa>/<C>.chunk          # L1: C → vector (dims×f32 LE) — no text (ADR-049 content-less contract preserved)
    snapshots/<oid16>/             # L3, immutable
      occurrences.tab              # L2 rows: path → blobHash, [(C, s, e, lang)]
      table.json                   # {chunker_fp K, ignore_fp I, counts, bytes}
      lexical/                     # per-snapshot bleve index (per-view BM25 statistics)
    refs/<sanitized>.ref           # branch name → commit OID (atomic rewrite)
    roots/<sanitized-root>/        # L4
      overlay.tab + overlay-lexical/   # dirty/untracked occurrences, same row grammar
      sidecar.json                 # path → hash|size|mtime|chunkerFP|chunkset (v5 grammar preserved)
      view.json                    # {pinned_ref, epoch, last_seen}
CURRENT                            # atomic-rename pointer to the live generation
```

- **`format: 2`** in the manifest is the version. An unknown format version (a
  future binary's store read by an older one) makes the store read as ABSENT:
  nothing is served, nothing is deleted; the older binary re-indexes into the
  layout it knows. Forward compatibility is deliberately refused — silent
  partial reads are worse than a re-index.
- Per-snapshot lexical state is required because BM25 statistics are
  corpus-shape-dependent; sharing vectors across views does not license sharing
  lexical scores.
- Write ordering everywhere: **chunk records land before any table referencing
  them; tables land by temp-file + atomic rename; refs by atomic rewrite.**
  A crash can leave unreferenced chunks (garbage) but never a table row whose
  chunk is missing.

**One-time v1 → v2 migration** (per project, background, marker-gated — the
ADR-049 migration pattern): for each legacy branch root, the sidecar's
`hash|size|mtime|chunkerFP|chunkset` rows plus readable source files re-derive
occurrence rows; each legacy document's vector moves from
`DocumentID(path, i)` to the chunk record keyed by the re-chunked text's `C`
(no ONNX). Files whose source is missing or hash-mismatched are dropped —
re-indexable on demand. The marker is written only after a branch's tables are
complete; a cancelled migration replays. Legacy branch directories are deleted
only after the whole project's migration is marked done (rollback window).

**Stage-3 implementation (the unified legacy-layout sweep).** The migration
landed as a single background sweep per physical store
(`core/vectorindex/layout.go`, `CorpusRegistry.RunStoreMigration`) that drains
EVERY legacy layout directly into the implemented v2 subset — the stage-2
corpus+overlay shape — ahead of the chunk-record format:

- **Units.** Pre-ADR-064 root-level collection directories (re-homed into
  per-branch stores first), every legacy per-branch store
  (`branches/<collectionName(branch)>/`) in the shared root AND inside every
  legacy per-worktree store (`worktrees/<name>/`, ADR-081), with their
  sidecar pairs, lexical directories and content-less markers as move legs.
- **Reuse is provable, fallback is diagnosable.** A promotable store —
  compatible generation manifest (embedding era), a usable sidecar hash map,
  and the ADR-049 content-less marker — moves by RENAME into the shared
  corpus keyed by its branch's resolved commit OID: zero embeddings, zero
  chunking, publication (`corpus.json`) lands last. Every non-reuse outcome
  is a recorded reason (era mismatch, no sidecar hash map, marker absent,
  branch absent, target already exists) — a store with pre-049 fat documents
  is never promoted (a read-only shared decode would pin its content in
  every process); it falls back to a cache-spared rebuild.
- **Versioned completion state.** `store_migration.json` (schema version 1)
  holds the incremental per-unit progress: `completed` unit ids, terminal `skipped`
  records with reasons, atomic rewrite after every unit, and the `done`
  marker written ONLY after the full recognized volume settled. Unknown
  entries — unrecognized directory shapes, the `_orphan` root, worktree
  containers without branch stores — are excluded from the recognized volume,
  reported as skip records, and left in place. A state file from a newer
  binary (higher version) is refused, not rewritten.
- **Replay-safety and convergence.** Crashes, cancellations and transient
  failures (git facts unavailable, rename I/O errors, an in-flight corpus
  build lease) leave `status=running` with the settled prefix recorded; the
  next sweep resumes at the first unsettled unit — no document is lost (every
  move is a rename; nothing is published half), no false done marker is
  written. A legacy store whose snapshot corpus is ALREADY PUBLISHED is a
  validated duplicate: its legs are removed and the unit recorded completed.
  An existing but unpublished target is never overwritten — the source stays
  with a recorded collision reason.
- **Lock discipline.** The sweep holds NO service or registry lock across
  its run; each promotion takes only the target snapshot key's single-flight
  build lease (short-bounded wait — the sweep defers to its next run, the
  opened view's attach keeps the full drain window), which is exactly the
  serialization point between layout migration, corpus builds and the
  sidecar/content-less passes on live working stores; the stage-5
  reachability keep sets (prune.go) protect live corpora/overlays from the
  stale-branch pruner, and in-flight promotions ride the build-lease
  protection. Managers kick the sweep after a successful view attach; it
  joins Shutdown bounded and replays from its state on the next attach.

**Chunk-record completion (the v1→v2 pass, IMPLEMENTED).** The sweep's
rename-promotions land the corpus in the stage-2 chromem shape; the
chunk-record completion (`core/vectorindex/chunkstore_migration.go`, riding
the same sweep after the layout volume settles and on every later kick)
converts each published v1 corpus into the §3 durable form with ZERO
inferences: per file, the corpus manifest's blob hash gates a source read
from a live root's tree, the sanitized bytes are re-chunked with the live
chunker (a chunker-fingerprint mismatch defers to the rebuild path), and
the legacy document at `DocumentID(path, i)` contributes its vector to the
chunk record keyed by the re-chunked text's `C` — the vector MOVES, it is
never re-embedded (a chunk index with no legacy document is a
poison-dropped chunk, absent from both sides). One deliberate
strengthening of the paragraph above: a missing or hash-mismatched source
defers the WHOLE snapshot (all-or-nothing) instead of dropping the file —
a §3 table that covered fewer files than the v1 corpus would silently
regress search, so the snapshot stays on the v1 read path until its
sources match and the next sweep replays it (the paragraph's "re-indexable
on demand" is exactly this deferral's permanent form). The per-branch
marker is the snapshot's `table.json` (it lands last — the completion
marker IS the replay gate); the project marker is
`store_chunk_records.done`, written only after every published corpus
settled, and only then does the hygiene pass reap the migrated v1 chromem
legs (the lexical legs stay: §3 opens fall back to them until a rebuild
lands the lexical index inside the snapshot directory).

**Separate migration/residency ADR: not warranted.** The migration is one
mechanical pass whose rules are exactly this section; splitting it out would
force two documents to share one vocabulary for no decision surface of its own.
Recorded here as decided, not deferred.

### 4. Single-writer shared owner, read leases, lock ordering

- **One store owner per project store per process.** All mutations — chunk
  writes, snapshot publishes, overlay updates, ledger updates, GC — are
  serialized through one actor (a mailbox; it never calls back into managers
  while holding its own state). Root Managers and the embedder path SUBMIT work;
  they never write store files. The existing single-instance app lock
  ([ADR-075](075-single-instance-lock.md)) is the cross-process guard: no second
  process may hold the owner role.
- **Read leases.** A reader (a search, a status snapshot, a RAG hint) acquires a
  lease on a VIEW (snapshot + overlay pair) — an epoch-stamped refcount held for
  the call's duration. Leases are registered under a dedicated leaf mutex that
  no other lock may be held while acquiring, so **status reads and search
  admission never block behind indexing, decode, or publish** (this closes
  ADR-064's documented negative: `GetVectorIndexStatus` blocking on the Service
  write lock during a branch open).
- **Lock ordering (total order, cycle-free by construction):**
  `VectorRoots.mu → Manager.mu → Service.mu → lease registry mutex` (leaf) and
  `store-owner mailbox` (asynchronous — nobody holds the above while submitting,
  submissions are non-blocking). Acquiring in the reverse direction is a bug.
  The `indexAvailability` lock-free atomic stays lock-free.
- **publish.** A new view becomes visible atomically: tables fully written and
  renamed → view epoch bumped → subsequent lease acquisitions observe the new
  epoch. In-flight leases keep their old epoch valid until released (readers
  never observe a torn view).
- **cancel.** Every background pass carries the view epoch it started with (the
  `openGen`/`readyGen` discipline generalized): a stale pass — branch switched,
  project switched, eviction, shutdown — publishes nothing; its epoch check
  under the lock makes check-then-publish atomic with the invalidation point
  (the init publication protocol of workspace.md, restated for store epochs).
- **reopen.** After eviction or process restart, opening a root's view reads
  manifest + ref + tables (no corpus walk when the table is current — the
  ADR-064 laziness goal, now O(one table) instead of O(branch's documents));
  the overlay re-derives by diffing disk against the snapshot table (bounded by
  changed files, the existing sidecar stat fast-path and its 20-pass re-hash
  backstop unchanged in spirit).

### 5. Residency: ledgers, reader pinning, eviction

Two ledgers, both maintained by the store owner:

- **Disk ledger** (bytes on disk, per generation): unique chunk bytes (counted
  ONCE regardless of occurrence/branch/root reference count), per-snapshot
  table+lexical bytes, per-root overlay bytes, embedding-cache bytes (existing
  accounting). The park budget's role for disk becomes an explicit cap knob
  (successor of `park_budget_mb`'s spirit; knob naming is an implementation
  step).
- **Resident ledger** (bytes in RAM): per-view table maps + per-view lexical
  handles, each charged at its OWN marginal cost; the chunk-vector pool is one
  SHARED pool charged once across all views.

Eviction rules:

- **Views** are evicted LRU (focus root's view pinned; ADR-081's focus-pinning
  preserved), but a view with live read leases is skipped — the lease, not the
  lock, is what makes eviction safe. Eviction of a leased view defers to the
  next candidate; shutdown waits bounded (the existing 10 s / 25 s drain
  family).
- **Chunk pool** entries are refcounted by in-flight searches; a pinned entry is
  never evicted. The pool is bounded by the shared byte budget — the SAME
  budget across every view, so opening four views of the same corpus cannot
  multiply the corpus's resident vectors.
- **The invariant the ledgers enforce — LRU cannot hide corpus duplicates:**
  because L1 storage is content-addressed, N views of the same corpus share one
  set of chunk records, and the ledger reports corpus bytes ONCE plus N table
  bytes. The ADR-049 park LRU's failure mode (three monorepos → 4 × ~1 GB
  copies, count-bounded) is structurally impossible: there are no duplicate
  copies to hide, and the accounting cannot double-count what sharing has
  collapsed. A budget breach is therefore always a REAL breach (distinct
  content), never an accounting illusion.
- **Disk eviction** (budget pressure) removes LRU snapshot directories that no
  root currently pins and no ref needs for correctness; removed snapshots are
  re-derivable by a corpus pass (honest cost, paid lazily, surfaced in status).

#### Stage-4 implementation note: owner-level handle residency (IMPLEMENTED)

The residency half of §5 is implemented over the stage-2 owner/view shape
(`core/vectorindex/corpus_residency.go`). What shipped:

- **The unit of retention is the shared `CorpusHandle`** (one chromem decode +
  one read-only lexical reader), NOT a view: the per-root overlays stay
  bounded by the ADR-049 park budget and the ADR-081 manager LRU exactly as
  before — root-local deltas are a separate ledger, so nothing is
  double-counted between roots, parking, and view slots (a handle's estimate
  is charged once, regardless of reference count).
- **Bounds**: `vector_index.corpus_residency_capacity` (handles) and
  `vector_index.corpus_residency_budget_mb` (estimated bytes,
  `docs × (dims×4 + 1 KiB)` per handle — the ADR-049 shape applied to the
  shared corpus). Either bound alone applies; both apply together; **both
  zero (the default) keeps retention of inactive views OFF** — a zero-ref
  handle closes at its last release, the pre-residency behavior. The knobs
  carry no sentinel semantics: 0 is documented, negatives are rejected at
  config load (the park knobs' -1/zero quirks are deliberately not inherited).
- **Pinning**: the handle refcount IS the read lease — an attached handle is
  structurally un-evictable (the LRU lists only zero-ref keys). Eviction never
  closes a handle another root or reader holds; a pinned corpus survives
  unlimited LRU pressure and re-enters retention only at its own last release.
- **Eviction** is oldest-eligible, enforced at the state change that overflowed
  the bound (release, policy re-apply), reusing the park mechanics; a retained
  handle whose estimate alone exceeds the byte budget is evicted (never retain
  what cannot fit), while an ATTACHED oversized corpus is never closed — the
  bounds govern retention, not admission, and **no hard RSS cap is promised**:
  an active working set above budget stays resident and no surface may report
  budget compliance as one.
- **Reopen after eviction** is the ordinary fail-closed open: manifest +
  decode + read-only lexical, O(one corpus), honest re-decode counted per
  corpus. A→B→A under retention re-attaches the SAME handle — no decode, no
  lexical reopen — with exactly one decode per corpus, not "one total".
- **Memory-free seam**: every actual eviction schedules `freeOSMemory`
  (registry-level, `runtime/debug` by default) in its own goroutine — never
  under the store mutex.
- **Teardown**: `Service.DeleteProjectData` drops the storage root's whole
  residency (retained handles close; still-attached ones are retired — their
  last release closes instead of retaining); `VectorRoots.ShutdownAll` closes
  the registry after the managers drain (a `Disable()` deliberately does not —
  the restartable enable cycle reuses the retained corpora); No-Project never
  creates a corpus store (no persistent, no LRU data).
- **No parallel cache**: the handle map is the only corpus cache; the LRU is
  an ordering over its zero-ref entries — there is no branch-keyed cache to
  drift against (the ADR-064 stretch idea stays superseded).

### 6. Recovery

- Crash windows are enumerated, not discovered: (a) mid-chunk-write → orphan
  chunk file, unreferenced, swept; (b) mid-table-write → `*.tmp` ignored,
  previous table still current; (c) mid-ref-rewrite → old ref still valid;
  (d) mid-overlay-write → overlay discarded and re-derived from disk;
  (e) mid-migration → no marker, replay on next open. No store-level WAL, no
  marker replay beyond migrations: atomic rename + ref-before-use ordering
  cover the rest.
- Orphan chunk records (write completed, table never published; evicted table's
  chunks) are collected by a refcount sweep — the same silent,
  switch-riding, single-flighted hygiene contract as the existing stale-branch
  sweep in workspace.md (Debug-only logs, never blocks a switch).
- The stale-BRANCH cleanup story simplifies: a deleted branch deletes ONE ref
  file; snapshot directories become unreferenced and the sweep collects them by
  refcount (the sanitizer-collision caveat of ADR-064's identity disappears —
  collision-prone branch names no longer key storage; commit OIDs are
  collision-free by construction). The synthetic `default`/non-git snapshot is
  root-owned (L4 holds its ID), never ref-pruned.

**Stage-5 implementation note (reachability GC, IMPLEMENTED):** the
stale-data half of this section ships through on-disk ownership records
under `refs/` (root markers written as attach intent pins, branch
references) — the switch sweep and delete-triggered purges keep exactly
what owner reachability (live markers ∪ surviving branch refs ∪ held
corpus handles and build leases) still reaches
([domains/workspace.md](../domains/workspace.md), Shared-store
reachability GC).

**§5/§6 completion note (disk ledger + orphan-chunk sweep, IMPLEMENTED):**
the disk ledger is walk-computed truth (`core/vectorindex/store_gc.go`)
charged once per identity level — unique chunk bytes counted once,
per-snapshot table+lexical bytes, per-root overlay bytes, whole superseded
generations — cached by the hygiene pass and surfaced in
`GetVectorIndexStatus.store`; `vector_index.store_disk_budget_mb` (default
off) evicts LRU owner-unpinned snapshot directories under pressure. The
orphan-chunk refcount sweep rides the same silent, switch-riding,
single-flighted slots as the stale-branch sweep: the referenced set is
every published snapshot table ∪ overlay table of the current generation,
an mtime grace window protects an in-flight writer's deferred table flush,
superseded generations are removed as atomic units guarded by the
generations open handles were decoded from, and unreachable §3 snapshots
are reclaimed leg-together with their v1 stores.

### 7. What this supersedes vs preserves (049 / 064 / 081)

**Preserved** (carry forward verbatim or in spirit):

- **ADR-049:** the GC soft limit + scavenge program; content-less storage (chunk
  records carry no text — reconstruction stays best-effort from sources); the
  byte-budget intuition (now two honest ledgers); observability (RSS indicator,
  telemetry stage enums); the atomic-window commit discipline; the embedding
  cache in its entirety.
- **ADR-064:** the PROBLEM statement (open cost must not scale with branches) —
  this ADR is its completion, not its reversal; the honest status contract
  ("preparing" ≠ "indexing"); stable directories, never swapped under a live
  reader; laziness as the default posture.
- **ADR-081:** one Manager per root; context-borne agent routing and focus-borne
  user routing; single-flight creation; focus pinning; bounded shutdown;
  cache-roots-never-nest; the master-switch teardown choreography of ADR-103
  rides on unchanged.

**Superseded** (this ADR replaces the mechanism, keeps the intent):

- **049-C's park LRU of whole chromem DBs** → view residency + shared chunk pool
  (§5). The estimate heuristic (`docs × (dims×4 + 1 KiB)`) is replaced by ledger
  truth.
- **049's content-less migration machinery** → unnecessary against format v2
  (content-less by design); the migration code retires after the v1→v2 pass.
- **064's per-branch chromem DB roots** (`branches/<collectionName>/`) →
  commit-keyed snapshot tables + ref pointers (§1, §3). The open-LRU stretch
  idea is superseded by view residency.
- **064's sanitize-collision identity** → commit OIDs (collision-free).
- **081's isolated worktree storage roots** (`worktrees/<name>/vector_index`) →
  per-root overlays over the shared project store (§1 L4). "Two managers never
  open the same chromem storage" becomes "two managers never WRITE the same
  store — one owner; readers lease". Cross-root embedding dedup, deliberately
  reduced by 081, is restored at the store level (worktrees of one repository
  share chunk records).

**Also superseded — the historical non-goals of [epic #85](https://github.com/v0lka/c0wrk/issues/85):**
the original issue text scoped branch/worktree sharing under the assumption
that the file-hash sidecar and the document/lexical identity could stay
unchanged. That boundary is superseded by this ADR's identity model: occurrence
paths become root-relative (§1 L2/L4), the sidecar becomes root-local state
whose corpus-current entries are adoption records (stage 2), and the corpus
manifest — not a sidecar — is the sharing boundary between roots. The epic's
consolidated scope (which carries this ADR as its design stage) takes
precedence over those historical paragraphs, exactly as it does for the
superseded mechanics of 049/064/081 above.

### 8. Measurement contract

The reproducible protocol — corpus shape, branch/root topology, cold/warm
scenarios, repetitions, **time-to-first-correct-search** as the primary metric,
per-scenario latency budgets in seconds, and the cold-ONNX-init measured
separately from warm attach — is specified in
[`core/vectorindex/BENCHMARKS.md`](../../core/vectorindex/BENCHMARKS.md) and is
the acceptance instrument for every later roadmap step. Owner approval of the
protocol (budgets included) is a gate BEFORE implementation begins.

## Consequences

**Positive:**

- The ×branches and ×roots content multipliers become ×distinct-commits-indexed
  and ×distinct-dirty-bytes — bounded by real diversity, not by naming.
- `git checkout -b` from an indexed commit is a pointer write: sub-second first
  correct search, zero corpus reads (asserted by the benchmark, not hoped).
- Identical bytes at two paths keep both occurrences and share one vector;
  storage and inference both deduplicate.
- Status and search never block behind a decode/publish (leaf lease mutex).
- Accounting truth: budgets see unique bytes; eviction is honest.

**Negative / trade-offs:**

- A new store format: a one-time migration pass per project (background,
  replayable, source-dependent — files missing at migration time re-index
  later), and a rollback window that briefly holds both layouts on disk.
- The store owner is new critical infrastructure: its mailbox, epochs, and
  ledger must be tested for concurrency exactly like the manager's
  init-publication and indexing-seal protocols were.
- Per-snapshot lexical directories cost disk per indexed commit (charged to the
  disk ledger, evictable) — the price of correct per-view BM25.
- Legacy binaries reading a v2 store see "absent" and re-index (refused forward
  compatibility, deliberate).
- Ref-counted GC adds a sweep pass (silent, switch-riding) and a class of
  bugs (lost refs) that the immutable-table design must make impossible.

## Alternatives Considered

- **Keep ADR-064's layout, add a branch-open LRU (the 064 stretch issue)** —
  rejected: it caches the decoded symptom, leaves the ×branches storage
  multiplier, gives no fork-without-walk, and keeps path-keyed documents.
- **sqlite-vec / usearch single store, per-branch tables (ADR-049 option G)** —
  rejected for the same reason it was in 049: a persistence rewrite that still
  keys by branch solves none of the identity problems; the content-addressed
  layer is the actual requirement, and chromem-style files remain adequate
  beneath it.
- **Content-addressed chunks with per-BRANCH occurrence tables (commit not
  promoted to a level)** — rejected: branch-keyed tables re-introduce the
  fork-rewrite cost and the collision-prone name identity; commits are the
  natural immutable unit git already provides.
- **Share only the embedding cache across roots (no store sharing)** — rejected:
  spares inference while duplicating every vector, table, and lexical entry per
  root; the disk and residency multipliers survive.
- **Copy-on-write snapshot dirs per branch** — rejected: filesystem-level
  dedup tricks are opaque to the ledger and to Windows; the dedup belongs in
  the store's own addressing, where it is countable.

## Related

- [ADR-049](049-vector-index-memory-reduction.md) — memory program; superseded
  in part here (§7).
- [ADR-064](064-branch-scoped-lazy-vector-index-open.md) — branch-scoped open;
  mechanism superseded, goal completed here (§7).
- [ADR-081](081-vector-per-root-registry.md) — per-root registry; storage
  isolation amended to shared-store-plus-overlays (§7).
- [ADR-103](103-vector-index-master-switch.md) — master switch; teardown and
  live-apply semantics ride on unchanged.
- [domains/workspace.md](../domains/workspace.md) — Vector Index behavior,
  invariants, and storage-layout statements to be updated by later roadmap
  steps per this ADR.
