# ADR-049: Vector-Index Memory-Reduction Program

## Status

Accepted

## Context

Field reports showed the desktop process growing to multi-gigabyte RSS during and after vector indexing; on an 8 GB machine (worse with an integrated GPU carving out memory) a full project re-index could reach OOM. The 2026-09 memory investigation decomposed the footprint into independent multipliers and ordered the fixes by cost. The letters below are the exploration's option labels, reused verbatim:

- **A. GC overlay ×2** — the process set no `GOMEMLIMIT`/`debug.SetMemoryLimit` anywhere, so with the default `GOGC=100` the heap may grow to ~2× the live size and is returned to the OS only lazily. On a large live heap (chromem keeps every document in RAM) the re-index spike alone roughly doubles RSS.
- **B. Full-enumeration transient spikes** — chromem-go has no `ListAll`, so `RebuildLexical`, `collectDocumentIDs`, and the file-hash sidecar migration materialized the entire collection through `Query(ctx, " ", count, …)` (a `[]Result` slice + similarity heap + pointer slices) — hundreds of MB transient on large collections, on top of everything else.
- **C. Park LRU bounded by count, not bytes** — `park_capacity: 3` kept the FULL chromem + bleve state of up to three recently-closed projects resident plus the current one: three monorepos → 4 × ~1 GB+ copies with no byte ceiling at all.
- **D. Per-chunk metadata duplication** — 6 of 9 metadata fields are identical across every chunk of a file (`content_hash`, `file_size`, …): a per-chunk `map[string]string` where a per-file sidecar row would do (~0.5–0.8 KB/chunk).
- **E. Vector quantization** (fp16/int8 in storage) — up to −1 KB/chunk, cosine quality nearly unaffected, but requires a chromem-go fork or a contract change.
- **F. Chunk content resident in RAM** — each document's `Content` (up to `max_chunk_size` characters) is derivable from the source file via `(file_path, start_line..end_line)`; storing it costs ~30% of index memory for search-time convenience only.
- **G. Replace the store** (sqlite-vec / usearch / custom mmap storage) — solves the problem by class, but rewrites the persistence layer and migrates every existing index.

The accepted program is **A + B + C + F**: the cheap-to-medium wins that close the OOM symptoms and cut the resident constant. D's dedup effect is delivered as a by-product of F's implementation (commit-time metadata narrowing + per-file sidecar rows) without forking chromem. E and G stay rejected for this cycle (see Alternatives). Steps 1–6 of the program shipped one item each; this ADR records the whole.

## Decision

1. **A — GC soft memory limit + explicit scavenges** (`desktop/memlimit.go`, `desktop/memlimit_ram_*.go`, knob `runtime.memory_soft_limit_mb`).
   - Tri-state knob: `0`/omitted = AUTO, `> 0` = explicit MiB applied verbatim, `-1` = OFF; anything below `-1` fails config load (a typo, not a sentinel).
   - AUTO = `clamp(50% of physical RAM, 2 GiB..8 GiB)`; unknown RAM resolves to the 2 GiB floor.
   - Resolution precedence: OFF → explicit config value (wins even over a `GOMEMLIMIT` env var, override logged) → `GOMEMLIMIT` env (preempts AUTO: the Go runtime already applied it at process start, and the app must not silently override an operator-level setting) → AUTO. Every branch logs its decision (`source=auto|config|env|off`, total RAM).
   - Applied once at startup — after config load and logger re-init (Phase 2), strictly before background indexing starts — through `debug.SetMemoryLimit`: a GC target, NOT a hard cap; under live-set pressure the heap may exceed it, the GC just runs more often.
   - `debug.FreeOSMemory()` runs after every full indexing pass (both `IndexFull` call sites, in background goroutines, never under a manager/service lock), after any park-budget/capacity eviction, and after the content-less migration completes. Incremental passes deliberately do not scavenge (no spike to return).

2. **B — enumeration without materialization** (sidecar v5 `idxset` field).
   - File-hash sidecar entry format v5: `content_hash | size | mtime_unix_nano | chunker_fingerprint | chunk_set` (`|`-separated; 3- and 4-field legacy entries keep parsing).
   - `chunk_set` (idxset) codec: a contiguous run `0..N-1` encodes as the bare count `"N"` (empty set → `"0"`); a gapped set — chunks dropped by the poisoned-text fallback — encodes as `"L:0,1,3"` in ascending order. Parsing is strict: malformed values, and sets above `1<<20` entries, downgrade the entry to legacy so a corrupt sidecar cannot become an allocation bomb.
   - With a known set, chunk document IDs are pure arithmetic on the deterministic `DocumentID(path, i)` — `collectDocumentIDs`, `RebuildLexical` v2, the migration probe, and `ValidateCollection` stats never issue the materializing `Query(" ", N)`. An entry without a usable set falls back all-or-nothing to the legacy space-query; a migrated-but-unchanged file keeps a legacy (set-less) entry until its next index pass rewrites it, which for an unchanged file may not come, so such files can retain the Query fallback indefinitely.
   - Enumeration queries (`Browse`, migration) use a first-axis unit vector via `QueryEmbedding` when the embedding dimension is known, so enumeration costs no ONNX inference.
   - Back-compat: an older binary's 3-or-4-field parser rejects any 5-field entry as a legacy bare hash — slower (one redundant read+hash pass rewrites the entry) but correct.

3. **F — content-less storage contract + best-effort reconstruction.**
   - Commit-time strip: on the batch path a document commits with `Content=""` whenever its embedding is present, and metadata is narrowed unconditionally to the four chunk-position fields (`file_path`, `start_line`, `end_line`, `language`). Per-file bookkeeping (hash, size, mtime) moves into the sidecar row (`fileHashInfo`) — this is where D's dedup lands, without touching chromem.
   - Read side: a per-call `contentResolver` reconstructs text from the source file (bounded read ≤ `max_file_size`, `lines[start-1:end]`, path→lines cache, dead-path failure cache). Reconstruction is **best-effort by contract**: a missing or unreadable file yields the placeholder `[content unavailable: source file missing or unreadable: <path>]` — never an error; hydration fills only the final top-K results (legacy-stored content, where present, stays authoritative); `must_match` filtering runs on reconstructed text so deleted files drop out of filtered results.
   - `RebuildLexical` v2 walks the sidecar instead of the collection: a known fingerprint + chunk set → bounded read + hash check → re-chunk with the same parameters → the same deterministic IDs, upserted to bleve in bounded windows — zero ONNX calls. When the sidecar is empty or its entries are ineligible (legacy fp-less rows), it falls back to the old collection-query path (transitional net: pre-upgrade projects never end up with an empty lexical index).

4. **One-time background migration with a marker.**
   - A collection is marked fully migrated by `contentless_<collectionName>.done`, written next to the branch's file-hash sidecar.
   - The migration runs in the background (never blocks project open/switch), waits for the file-hash backfill to settle first, then probes legacy documents WITHOUT holding the service lock: known idxset → ID arithmetic; otherwise a gap-tolerant scan. Documents re-commit in ≤200-document windows with the same IDs and vectors and stripped content — chromem atomically replaces each per-document gob in place, so shrunken gobs land where the fat ones were.
   - The marker is written only on full success, followed by `go freeOSMemory()`. Any cancellation (project switch, park/eviction, close, branch switch, rebuild) leaves every document individually consistent — either fully legacy or fully stripped, never half — and does NOT write the marker: the next open replays the migration to completion. An empty collection gets the marker synchronously; `IndexIncremental` waits for the migration so migration re-commits never interleave with incremental deletes/re-indexes.

5. **C — park budget in bytes** (knob `vector_index.park_budget_mb`).
   - A parked state's estimated footprint = `documents × (embedding_dims × 4 B + 1 KiB)` — coarse, but proportional to the true vector + lexical footprint, so a few huge indexes stop monopolizing the LRU at the expense of many small ones.
   - Right after a state is parked, the OLDEST parked states are evicted — sidecar flushed, then `go freeOSMemory()` outside the lock — while the summed estimates exceed the budget. The effective bound is `min(park_capacity, park_budget_mb)`; a single state larger than the whole budget evicts everything including itself.
   - Tri-state semantics: unset → default 1024 MiB; negative → byte budget disabled (capacity alone); explicit `0` → rejected at config load as ambiguous; `park_capacity: 0` still disables parking entirely regardless of the budget.

6. **Observability — live RSS indicator.**
   - The `GetProcessMemory` RPC (gopsutil-backed, per-OS, pure Go) feeds a StatusBar indicator — `RSS 1177 MB`, tabular-nums, exact-bytes tooltip, 5 s polling — so the program's effect is visible in-app without external tooling. Failures hide the indicator silently: it is diagnostics, not a feature.
   - The startup log always records the resolved soft-limit decision and its source.

## Consequences

**Positive:**

- The ×2 GC overlay is gone by default (AUTO), and memory is handed back to the OS after full passes, evictions, and the migration instead of lingering until an idle GC.
- Index memory drops by the content share (~30%) plus D's metadata dedup; per-document gob files shrink in place during the migration.
- Full-enumeration transient spikes are gone from every hot path; `Browse` loses its per-call ONNX inference; `RebuildLexical` needs zero ONNX.
- Parking is bounded by estimated bytes as well as count — RAM-rich machines keep the latency win, constrained machines get a ceiling.
- The whole program is observable: startup logs the limit decision, the status bar shows live RSS.

**Negative / trade-offs:**

- Search results now depend on source files: a deleted, moved, or unreadable file yields a placeholder instead of stale cached text, and `must_match` excludes it — the index and the disk can disagree transiently after external edits until the next incremental pass.
- Legacy collections pay one extra probe pass (the migration), amortized once per collection and replay-safe.
- The park budget uses an estimate, not measured RSS: proportional but coarse (the 1 KiB/document allowance deliberately overestimates stripped documents).
- The soft limit is a GC target, not a cap — under live-set pressure the heap may still exceed it; the limit trades GC CPU for RSS.
- Top-K hydration and `must_match` filtering each add a bounded file read per result; the resolver caches per call, not across calls.

## Alternatives Considered

- **E — fp16/int8 vector quantization** — deferred: ~−1 KB/chunk for cosine-tolerant quality, but it needs a chromem-go fork or contract change; revisit only if footprint complaints persist after A+B+C+F.
- **G — replace the store (sqlite-vec / usearch / custom mmap)** — rejected for this cycle: a class-level rewrite of the persistence layer plus migration of existing indexes is justified only for million-chunk monorepos.
- **D as a standalone change (fork chromem per-file metadata)** — unnecessary once F landed: commit-time metadata narrowing plus the sidecar's `fileHashInfo` rows deliver the same dedup without touching chromem.
- **Hard RSS cap (rlimits / cgroups)** — rejected: a desktop app killing itself mid-index is strictly worse than a soft GC target that trades CPU for RSS.
- **Lowering GOGC instead of `SetMemoryLimit`** — rejected: GOGC is relative to the live heap (no absolute envelope), and on a large live set it thrashes the GC without bounding RSS.
