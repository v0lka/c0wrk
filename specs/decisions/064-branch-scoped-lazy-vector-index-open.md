# ADR-064: Branch-Scoped Lazy Vector-Index Open

## Status

Accepted

## Context

chromem-go persists a project's vector index as a directory tree in which **each collection is a subdirectory** and **each document is an individual `.gob` file** (`core/vectorindex` uses one collection per git branch — see [ADR-013](../decisions/013-hybrid-search.md) and [domains/workspace.md](../domains/workspace.md)). `chromem.NewPersistentDB(dir)` is all-or-nothing: it enumerates **every** collection subdirectory under the DB root and gob-decodes **every** document file in each, synchronously, on open.

c0wrk opened the whole project root (`<project>/vector_index`) on every project open (`Service.SetProject` → `newPersistentDB`), so the open cost was `O(branches × documents)`. Because most branches are irrelevant to the active session — the Service only ever uses the collection of the current git branch — this work was almost entirely wasted.

Field impact (owner's machine, project `3908f983…`):

- 12 branch collections × ~36k–45k documents ≈ **500k `.gob` files / 2.4 GB** under `~/.c0wrk/projects/<id>/vector_index/`.
- The gap between the ONNX embedder finishing (`background init complete`) and `project set for vector index` grew with the index over days: **2 s (Sep 17) → 74 s (Sep 22)**.
- The open ran synchronously **while holding the Service write lock** (`s.mu`), so it wedged every vector-index RPC for its whole duration, saturated disk/CPU/memory, and made the running app appear frozen (git branch checkout in the panel stalled; the quit path blocked; a minimized window would not restore).
- The whole time the status bar showed `{state: "indexing", phase: "both", progress: 0, total_files: 0}` — a *fake* "Indexing" state with an empty progress bar — because `Manager.initProject` announced the pass **before** the open (the deliberate "announce before open" contract, see [domains/workspace.md](../domains/workspace.md)). No indexing was actually happening yet.

The root cause is the **eager, all-branches open**, not the indexing itself.

## Decision

**Give every branch its own stable chromem DB root and open only the active one.** Instead of pointing chromem at the shared storage root, each branch's collection lives in a dedicated directory that is itself a DB root holding exactly one collection:

- `<project>/vector_index/branches/<collectionName(branch)>/` — the **stable chromem DB root** for one branch. chromem creates its single collection directory (`<hash2hex>`) inside it, so `NewPersistentDB` enumerates exactly one collection. The mapping branch ⇒ directory is `branchRootPath` = `branches/` + `collectionName(branch)` — the same sanitized identity used for the branch's file-hash sidecar and content-less marker — so it is deterministic and needs no chromem-internal hashing. There is no `active/` directory: the roots are stable, so no directory is ever swapped or renamed to load a branch.
- `lexical/`, `embedding_cache/`, `file_hashes_<collectionName>.json`, and `contentless_<collectionName>.done` stay directly under `<project>/vector_index/`. chromem never opens that root, so these are never parsed as collections.

Behavior changes:

1. `Service.SetProject` **no longer opens the persistent DB**. It resolves paths, the embedding cache, and the file-hash sidecar, ensures the storage root exists, and performs a **one-time legacy migration**: any legacy collection directory found directly under `vector_index/` (8 lowercase hex characters, chromem's `hash2hex` shape) is renamed into its per-branch root as `branches/<collectionName>/<hash2hex>/`. The collection name is recovered by reading the directory's chromem metadata file (`00000000.gob`) rather than replicating chromem's hashing; because the recovered value becomes a path segment it is accepted only in the exact shape `collectionName` produces — the `branch_` prefix followed by a non-empty run of `[A-Za-z0-9_-]` — and an unrecoverable or non-conforming name (including a sanitized-but-unprefixed one, which is not a branch identity and would otherwise let foreign metadata squat an arbitrary name under `branches/`) lands under `branches/_orphan/` (kept on disk, never loaded — the affected branch is simply re-indexed). The migration is directory renames only (no per-document work) and idempotent (an existing target is never overwritten, so a re-run leaves already-migrated directories untouched).
2. `Service.SwitchBranch(branch)` owns the branch-scoped open. When the requested branch is not the loaded one it (a) persists the outgoing branch's file-hash sidecar and then **releases the outgoing branch's residents — its chromem DB, collection and bleve lexical index — before opening anything**, so the switch peaks at one branch's working set instead of two; (b) `openBranchDBLocked(branch)` → `newPersistentDB(branches/<collectionName(branch)>)` → exactly one collection decoded, (c) `GetOrCreateCollection`, (d) opens the per-branch lexical index under `lexical/<branch>/`, and (e) loads the file-hash sidecar. A same-branch switch with a live collection stays a no-op. A failed open therefore leaves the state closed (no collection, search fails fast) rather than silently serving the previous branch's collection while the worktree is elsewhere. No directory is renamed on a switch — the outgoing branch's DB simply stays in its own stable root.
3. Park/close/`DeleteProjectData`/No-Project reset are adjusted for a branch-scoped DB that may be unopened before the first `SwitchBranch`.
4. The status contract becomes honest: the pre-open state is a **distinct "opening/preparing" state** (no progress fraction, no "Vector: building / Lexical: building" claim), and the real `indexing`/`reindexing` progress begins only when a pass actually starts.

Consequence for scaling: the open cost becomes `O(documents in the active branch)` and **stops growing with the number of branches**. Returning to a branch that was indexed before opens its directory directly; a branch never indexed opens an empty collection and is built normally.

## Consequences

**Positive:**

- Startup/project open no longer depends on how many branches have ever been indexed — the multi-minute stall and its growth over time are gone.
- The active branch's collection directory is the only one read, so the open touches ~1/12th (and shrinking) of the files, cutting disk I/O and RAM transiently.
- The status bar stops claiming to be "indexing" while it is only opening, removing the misleading empty 0/0 bar.
- Inactive branch directories remain on disk, so returning to them still skips re-embedding (no ONNX re-index) — only the gob decode of that one branch is paid.

**Negative / trade-offs:**

- A git branch switch (or an external `.git/HEAD` change detected by `GitMonitor`) now opens the target branch's own chromem DB (release the current branch's residents, open the target's stable root) instead of `GetOrCreateCollection` on an already-loaded DB. The decode runs on the background monitor goroutine rather than on the project-switch RPC, but it is **not** off the RPC path entirely: `SwitchBranch` holds the Service write lock for its whole duration, so an RPC that reads Service state under that lock — `GetVectorIndexStatus`, via `GetLexical` — blocks for the decode. That window is now one branch instead of all of them (which is the point of this ADR), not zero. `Indexer.HandleBranchSwitch` therefore publishes the honest `{state: "loading", phase: "open"}` before the open, so the UI does not claim a ready index during it (see [domains/workspace.md](../domains/workspace.md)).
- One-time migration on first open after upgrade: one directory rename per legacy collection for the field layout (cheap), executed under the Service lock but not per-document.
- The Service may hold a nil `db`/`collection` between `SetProject` and the first `SwitchBranch`; all accessors and the readiness gating must tolerate that (they already gate search on `WaitReady`).
- The park LRU continues to park the whole Service state (one branch's DB) — unchanged in spirit, and each parked state's on-disk directory is simply its own stable per-branch root under `branches/`.

## Alternatives Considered

- **Prune / TTL-cap the branch collections** — bounds growth but keeps the eager multi-branch open as the model and silently discards indexes the user may still want; rejected as a fix for the stall (kept as a possible future hygiene measure).
- **Single-file snapshot per branch (chromem `Export`/`Import`)** — would make the open one file read + one gob stream (~1 s even for a large branch), but replaces chromem's incremental per-document persistence with a whole-DB export after every pass (crash-consistency and write-amplification changes). This is essentially ADR-049's rejected option **G** (replace the store); deferred, not needed to remove the multi-minute stall.
- **Status-only fix** — make the bar honest but keep the eager open; rejected because the user-visible freeze (git checkout, quit, window restore) is caused by the decode itself, not the label.
- **Parallelizing the decode** — `chromem.NewPersistentDB` has a TODO to parallelize, but it is an external dependency and parallelism would not remove the `O(branches × documents)` work, only its constant factor.
- **Replace the store (sqlite-vec / usearch / mmap)** — ADR-049 option **G**, rejected there for this cycle and still the largest change; the branch-scoped open addresses the actual bottleneck (multiplied by branches) without a persistence rewrite.

## Related

- [ADR-049: Vector-Index Memory-Reduction Program](../decisions/049-vector-index-memory-reduction.md) — the memory program this builds on; its park/migration machinery is reused unchanged.
- [domains/workspace.md](../domains/workspace.md) — Vector Index behavior and lifecycle.
