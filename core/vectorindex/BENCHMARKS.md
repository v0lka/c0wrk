# Vector Index Benchmarks

## Purpose

This benchmark suite establishes a reproducible baseline for the complete and
incremental indexing pipeline and for real ONNX batch inference. It is intended
for before/after comparisons, not cross-machine performance claims.

The benchmark data is synthetic and deterministic. Metrics contain aggregate
counts and durations only: no source text, absolute paths, project IDs, branch
names, or caller-defined labels are recorded.

## Run

From the c0wrk repository root:

```sh
scripts/benchmark-vector-index.sh
```

Additional cache-focused benchmark (no ONNX assets required):

```sh
go test ./core/vectorindex -run '^$' -bench BenchmarkEmbeddingCacheWarm -benchmem
```

Embedding/persistence pipelining gate profile (requires local ONNX assets; skips
otherwise):

```sh
EMBEDDING_TEST_MODEL_PATH=... EMBEDDING_TEST_TOKENIZER_PATH=... \
EMBEDDING_TEST_LIBRARY_PATH=... go test ./core/vectorindex -run '^$' \
  -bench '^BenchmarkRealONNXIndexingPersistence$' -benchtime=1x -count=5 -benchmem
```

Optional controls:

```sh
BENCHTIME=1x COUNT=3 SP4RK_ROOT=../sp4rk scripts/benchmark-vector-index.sh
```

The ADR-104 store-protocol section runs in every invocation (deterministic
embedder, no ONNX assets needed); tune it with `STORE_PROTOCOL_SCENARIOS`,
`STORE_PROTOCOL_BRANCHES`, `STORE_PROTOCOL_ROOTS`, and `PROTOCOL_CORPUS`
(`small` default | `large`), each case in its own `go test` process.

The multi-session grid section honors `GRID_WORKERS`, `GRID_THREADS`,
`GRID_BATCHES`, `GRID_SEQUENCES` (space-separated lists) and
`GRID_MEMORY_CAP_MIB` (admission cap; memory-rejected cases print `SKIP`).
Every grid case runs in its own process, so peak RSS stays per-case.

The script uses these asset paths by default:

- `.cache/models/jina-v2-small.onnx`
- `.cache/models/jina-v2-small-tokenizer.json`
- `.cache/libonnxruntime.dylib`

Override them with `EMBEDDING_TEST_MODEL_PATH`,
`EMBEDDING_TEST_TOKENIZER_PATH`, and `EMBEDDING_TEST_LIBRARY_PATH`. When any
asset is unavailable, the deterministic host-pipeline benchmark still runs and
the ONNX matrix prints `SKIP` and exits successfully. The Go embedding
benchmarks themselves also call `Skip` when the variables are unset or their
files do not exist.

Each ONNX matrix case runs in a separate `go test` process. This is required
because `getrusage(RUSAGE_SELF).Maxrss` is a process-lifetime high-water mark;
running all capacities in one process would make later RSS values include
memory retained from earlier cases.

## Corpus and scenarios

Both suites generate text from fixed templates; neither reads repository source
files.

- `short`: 24 small Go files for the host pipeline; 256 texts averaging about
  20 tokens for ONNX.
- `mixed`: 24 deterministic Go/Markdown files with short and long sections;
  256 alternating short/long texts for ONNX (about 218 tokens on average in the
  baseline below).
- Batch capacities: 8, 16, 32, and 64, each in fixed-512 and length-bucket modes.
- Dynamic-shape ONNX inference is measured separately for short/mixed corpora and is not a production mode.
- `cold_full`: new chromem and Bleve stores, followed by a full pass.
- `warm_full`: repeat full pass against an initialized store.
- `warm_incremental_noop`: sidecar/stat validation with no changed files.
- `warm_incremental_one_file`: rewrite and re-index one file.

The host-pipeline benchmark uses a deterministic in-process embedder so it runs
on every developer/CI machine without ONNX assets. The ONNX benchmark uses the
real sp4rk embedder and reports tokenizer and inference costs.

## Metrics

Standard `-benchmem` output provides `ns/op`, `B/op`, and `allocs/op`.
Additional metrics are:

- `docs/sec`, `ms/pass` or `ms/op`: throughput and end-to-end latency.
- `ms/walk_validation`: workspace walk or incremental validation wall time.
- `ms/read_hash_chunk`: summed prep-worker work (not wall time when workers
  overlap).
- `ms/cache_lookup`: file-hash sidecar snapshot lookup.
- `ms/embedding`: host time in batch embedding.
- `ms/chromem_commit`: vector-store commit time. On the legacy nil-batch-
  embedder path this also includes chromem-owned per-document embedding.
- `ms/bleve_upsert`: lexical batch fill and commit time.
- `persistence-ratio`: `ms/chromem_commit + ms/bleve_upsert` divided by the
  end-to-end pass wall time, measured with the production ONNX embedder by
  `BenchmarkRealONNXIndexingPersistence`. This is the go/no-go input for the
  embedding/persistence pipelining gate (see below).
- `index-batch-fill`: documents entering the indexer's streaming inference
  accumulator divided by configured ONNX batch capacity. It is at most 1;
  every non-final batch is full and only the final pass tail may reduce it.
- `batch-fill`: real ONNX rows divided by fixed session capacity.
- `inferences/op`, `sessions`: exact inference and session counts.
- `hit_ratio`, `embed_inputs/op`: content-addressed embedding-cache hit share
  over unique normalized chunks and the number of unique texts reaching the
  embedder. A fully warm pass reports 1.0 and 0 respectively.
- `tokens/min`, `tokens/avg`, `tokens/max`: non-padding token lengths derived
  from attention masks.
- `peak-RSS-MiB`, `peak-RSS-delta-MiB`: process high-water RSS and increase
  after benchmark setup. RSS is available on Darwin/Linux; unsupported
  platforms report zero.

`vectorindex.Telemetry` and `embedding.Telemetry` expose snapshots for focused
benchmarks. Their stage keys are fixed enums, and snapshots have numeric fields
only, which bounds cardinality and prevents paths/content from entering the
telemetry surface.

## Baseline (2026-09-09)

Environment: Apple M4 Max, Darwin/arm64, Go 1.27.1, ONNX Runtime 1.28.1,
`jina-embeddings-v2-small-en`, `-benchtime=1x`, one isolated process per ONNX
case. Single-iteration numbers are a checked-in reference showing complete
metric coverage; use `COUNT=5` or greater and benchstat for optimization claims.

### Real ONNX embedding

| Corpus | Batch | docs/s | ms/op | B/op | allocs/op | Peak RSS MiB | RSS delta MiB | Fill | Inferences | Sessions | Tokens min/avg/max |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| short | 8 | 79.12 | 3236 | 39,304,168 | 500,901 | 1048 | 328.2 | 1.00 | 32 | 2 | 19 / 19.72 / 20 |
| short | 16 | 76.86 | 3331 | 39,213,728 | 500,852 | 2040 | 946.1 | 1.00 | 16 | 2 | 19 / 19.72 / 20 |
| short | 32 | 76.03 | 3367 | 39,289,344 | 500,859 | 3111 | 1311 | 1.00 | 8 | 2 | 19 / 19.72 / 20 |
| short | 64 | 77.37 | 3309 | 39,172,904 | 500,830 | 5156 | 1952 | 1.00 | 4 | 2 | 19 / 19.72 / 20 |
| mixed | 8 | 72.78 | 3517 | 470,839,440 | 5,453,367 | 1124 | 401.2 | 1.00 | 32 | 2 | 19 / 217.7 / 416 |
| mixed | 16 | 74.96 | 3415 | 470,579,760 | 5,453,276 | 1905 | 807.9 | 1.00 | 16 | 2 | 19 / 217.7 / 416 |
| mixed | 32 | 74.25 | 3448 | 470,510,168 | 5,453,219 | 3122 | 1323 | 1.00 | 8 | 2 | 19 / 217.7 / 416 |
| mixed | 64 | 74.24 | 3448 | 470,146,264 | 5,453,126 | 5807 | 2632 | 1.00 | 4 | 2 | 19 / 217.7 / 416 |

The fixed-capacity output tensor makes RSS rise with batch size while this
machine's throughput stays broadly flat; this confirms batch 32 remains a
reasonable latency/memory compromise rather than a universal optimum.

### Length-bucket evaluation (batch 32)

Measured in isolated processes on the same environment and corpus. These are
single-iteration directional figures; use `COUNT=5` or greater for statistical
claims.

| Corpus | Mode | Wall time | docs/s | Peak RSS MiB | RSS delta MiB | Sessions |
|---|---|---:|---:|---:|---:|---:|
| short | fixed-512 | 3208 ms | 79.80 | 2795 | 30.31 | 2 |
| short | buckets | 346 ms | 739.7 | 515 | 24.30 | 1 |
| short | dynamic | 148 ms | 1734 | 759 | 192.5 | 1 |
| mixed | fixed-512 | 3308 ms | 77.38 | 3135 | 355.0 | 2 |
| mixed | buckets | 1961 ms | 130.6 | 3404 | 391.2 | 2 |
| mixed | dynamic | 2740 ms | 93.42 | 16787 | 7639 | 1 |

Length buckets substantially improve short-corpus wall time and RSS. Mixed
corpus is faster but retains both short and long bucket sessions, increasing
RSS; therefore fixed-512 remains the default and buckets are opt-in. The
separately evaluated dynamic path is not the base solution because its
whole-batch output allocation produces unacceptable mixed-corpus RSS.

### Parallel ONNX sessions grid (2026-09-09): NO-GO

`BenchmarkEmbedderMultiSessionGrid` (sp4rk, benchmark-only harness) evaluates
`workers × intra-op threads × batch × sequence bucket` in an isolated process
per case. Every worker owns one session plus its session options and closes
them exactly once; a bounded dispatcher feeds two queues with strict query
preference; memory admission (`model + owned tensors + 384 MiB conservative
session overhead` per worker) rejects cases before creating any session.
Metrics: `docs/sec`, `cpu-util-percent` (user+system CPU / wall time across
all cores), `query-p95-ms` (worker-side submit→complete), `peak-RSS-MiB`.
`threads=0` is the production configuration. Corpus: 256 deterministic mixed
documents, one query per pass, `-benchtime=1x`, single iteration:

| Workers | Threads | Batch | Seq | docs/s | CPU util | Query p95 ms | Peak RSS MiB |
|---:|---:|---:|---:|---:|---:|---:|---:|
| 1 | 0 | 32 | 512 | 69.0 | 49.5% | 419 | 2659 |
| 2 | 0 | 32 | 512 | 93.0 | 86.4% | 540 | 5789 |
| 1 | 2 | 32 | 512 | 47.6 | 12.5% | 592 | 2981 |
| 2 | 2 | 32 | 512 | 65.9 | 23.6% | 763 | 5500 |
| 1 | 0 | 32 | 64 | 657.9 | 49.8% | 42.9 | 492 |
| 2 | 0 | 32 | 64 | 965.8 | 94.2% | 51.8 | 902 |
| 1 | 4 | 32 | 512 | 54.0 | 24.9% | 526 | 2980 |
| 2 | 4 | 32 | 512 | 88.4 | 48.0% | 577 | 5789 |

The production-shape comparison was repeated with `-count=3` (medians):
workers=1 69.3 docs/s / 411 ms p95 / ~3.3 GiB / 49% CPU versus workers=2
93.5 docs/s / 570 ms p95 / ~6.4 GiB / 87% CPU.

**Decision: no-go.** workers=2 raises throughput ~35% but doubles peak RSS,
degrades query p95 ~39% (ORT thread-pool contention defeats queue priority),
and nearly doubles CPU utilization — unacceptable for a desktop app sharing
cores with the agent runtime and UI. Splitting intra-op threads to make room
for a second worker is slower than production. The only winning regime
(sequence 64) is already dominated by opt-in length buckets. `workers=1`
remains the production path; no c0wrk wiring change was made. The harness
stays available (`scripts/benchmark-vector-index.sh`, `GRID_*` env knobs) for
re-evaluation on hardware with more core/memory headroom.

### Host full/incremental pipeline (batch 32)

This layer uses the deterministic embedder, so its timings isolate walk,
chunking, chromem, and Bleve rather than real model speed.

| Corpus | Scenario | docs/s | ms/pass | B/op | allocs/op | Index fill | Inferences |
|---|---|---:|---:|---:|---:|---:|---:|
| short | cold full | 1258 | 19.09 | 2,247,864 | 16,055 | 0.96 | 2 |
| short | warm full | 889.3 | 26.99 | 1,770,960 | 14,205 | 0.96 | 2 |
| short | incremental noop | 60,013 | 0.400 | 135,760 | 1,433 | 0 | 0 |
| short | incremental one file | 34.98 | 28.59 | 1,033,704 | 3,660 | 0.06 | 1 |
| mixed | cold full | 201.9 | 118.9 | 11,637,528 | 74,366 | 0.96 | 6 |
| mixed | warm full | 142.1 | 168.9 | 11,693,800 | 77,403 | 0.96 | 6 |
| mixed | incremental noop | 47,670 | 0.504 | 140,536 | 1,517 | 0 | 0 |
| mixed | incremental one file | 23.99 | 41.69 | 1,223,536 | 5,931 | 0.14 | 1 |

The benchmark emits the same four scenarios for every batch capacity (8, 16,
32, 64); the table is intentionally condensed because the deterministic
embedder's inference time is negligible. The full raw matrix is produced by the
script and should be retained with performance-review artifacts when comparing
changes.

### Content-addressed embedding cache warm pass

Deterministic 128-document corpus with 32 unique normalized boilerplate chunks,
`-benchtime=5x`. The host embedder uses a conservative synthetic 10 ms batch
latency (still far below the real fixed-512 ONNX latency above), so the result
measures cache filesystem overhead against avoided inference without requiring
model assets.

| State | ns/op | docs/s | embed inputs/op | hit ratio |
|---|---:|---:|---:|---:|
| cold | 11,056,025 | 11,577 | 32 | 0 |
| warm | 1,111,075 | 115,204 | 0 | 1.000 |

The warm pass is approximately 10x faster on this host and sends no text to the
embedder. Absolute numbers are not cross-machine claims; the acceptance signals
are `hit_ratio=1`, `embed_inputs/op=0`, and lower warm latency.

## Embedding/persistence pipelining gate (2026-09-09): NO-GO

Question: should embedding inference be pipelined with chromem/Bleve
persistence (a bounded ordered commit queue with a single owner of the mutable
branch/collection state and a per-file completion ledger)?

Gate: pipeline only if persistence is at least 10% of end-to-end wall time in
chromem/Bleve, measured with the production ONNX embedder (`jina-v2-small`,
batch 32). The deterministic host benchmark cannot answer this question — its
near-zero inference cost inflates the persistence share to 82–96% of wall time
— so the profile below is the decision input.

Measured by `BenchmarkRealONNXIndexingPersistence` (cold full pass, fresh
chromem + Bleve stores per iteration, `-benchtime=1x -count=5`, Apple M4 Max,
same environment as the baseline above):

| Corpus | persistence-ratio range | Median | Worst sample |
|---|---:|---:|---:|
| short | 1.27% – 1.91% | ~1.5% | 1.91% |
| mixed | 0.98% – 1.60% | ~1.5% | 9.69% (single cold-flush outlier) |

The single 9.69% mixed sample is a one-off chromem cold flush (206.8 ms vs the
13–16 ms typical for the same corpus), not a systematic share; every other
sample sits at 1.0–1.9%.

Decision: **no-go.** The gate is not met — typical persistence share is ~1.5%
of wall time, an order of magnitude below the 10% threshold, and the
theoretical ceiling of a perfect pipeline (removing 100% of serialized
persistence time) is below the repository's 5% noise threshold for optimization
claims. The runtime keeps its current shape: one `indexPrepared` consumer under
the service write lock, ordered bounded commit windows, and the
`documentAccumulator` per-file completion ledger that publishes a file-hash
sidecar entry only after every chunk has committed or been deliberately
dropped. No bounded commit channels were introduced; concurrency complexity
must be re-justified by a fresh profile (e.g. after a much faster embedder or a
materially slower storage backend) that again clears the 10% gate.

Acceptance cross-checks run with this decision (current tree):
`go test -race ./core/vectorindex/... -count=2` (includes the lexical and
manager concurrency suites, rapid-double-switch, bounded shutdown, cancellation
and sidecar-completion tests) — green.

## Retrieval-quality gate (chunking defaults)

`retrieval_quality_test.go` is a deterministic acceptance harness for
`max_chunk_size` / `chunk_overlap` default changes. It chunks a fixed
synthetic corpus (12 multi-section Go/Markdown files with sections sized
600–4400 chars, plus generated and minified noise files) with a candidate
configuration, embeds chunks with a deterministic token-hash embedder, and
measures MRR and Recall@3 over 72 boundary-sensitive queries, plus total
chunks, unique chunks (the post-dedup inference workload), and implied
inference batches. When the bundled tokenizer asset is present
(`EMBEDDING_TEST_TOKENIZER_PATH` or `.cache/models/jina-v2-small-tokenizer.json`),
it additionally audits the share of chunks whose real token length reaches
the model's 512-token ceiling — the truncation loss the synthetic embedder
cannot see.

Acceptance rule: a candidate defaults change may land only if MRR and
Recall@3 do not degrade (tolerance 0.005), the unique-chunk count does not
increase, and the real-tokenizer truncation share does not increase.
Otherwise the historical defaults stay.

Evaluation of 2026-09-09 (deterministic corpus, real jina-v2-small
tokenizer): baseline `(1500, 200)` — MRR 0.1917, R@3 0.2222, 227 unique
chunks, 8 implied batches, truncation share 0.3284.

| Candidate | MRR | R@3 | Unique | Batches | Truncation | Verdict |
|---|---:|---:|---:|---:|---:|---|
| (1500, 200) baseline | 0.1917 | 0.2222 | 227 | 8 | 0.3284 | — |
| (1500, 150) | 0.1694 | 0.2222 | 225 | 8 | 0.3284 | rejected: MRR drop |
| (1500, 100) | 0.1694 | 0.2222 | 218 | 7 | 0.3333 | rejected: MRR drop, truncation up |
| (1600, 200) | 0.1694 | 0.2222 | 218 | 7 | 0.3333 | rejected: MRR drop, truncation up |
| (1800, 200) | 0.2082 | 0.3333 | 207 | 7 | 0.3729 | rejected: truncation share up |
| (1200, 200) | 0.1468 | 0.1944 | 268 | 9 | 0.0000 | rejected: MRR/R@3 drop, more chunks |
| (2000, 200) | 0.1694 | 0.2222 | 196 | 7 | 0.3333 | rejected: MRR drop |

No candidate satisfied all three conditions, so the historical defaults
(1500 / 200) are retained. The chunk/inference reduction in this step comes
from the content filter instead: on the same corpus the filter removes the
generated and minified noise files (233 → 227 unique chunks here; on real
repositories the cut is proportionally far larger — e.g. the BPE
`tokenizer.json` case that motivated `max_chunks_per_file` produces zero
chunks instead of ~30k). Notable data point for future work: even the
current 1500-char default truncates ~33% of chunks on token-dense synthetic
code — shrinking chunk size removes truncation entirely (1200 → 0.0000) but
failed the retrieval and chunk-count conditions on this corpus.

## Multi-branch / multi-root store protocol (ADR-104)

This section is the **reproducible measurement contract** for the
content-addressed store ([ADR-104](../../specs/decisions/104-content-addressed-vector-store.md)).
It is the acceptance instrument for every roadmap step that touches store
identity, ownership, residency, or migration. The protocol is fixed here
BEFORE implementation; the harness (a new scenario family in this suite plus
its script entry) lands with the implementing steps and must satisfy this
contract, not redefine it. Owner approval of the latency budgets below is a
gate before implementation begins.

### Environment recording

Every run records the same environment block as the baseline above: machine
model, OS/arch, Go version, ONNX Runtime version, embedding model, and — new
— the store topology knobs and the store `format` version. Cross-machine
comparisons are out of scope; before/after on one machine is the claim shape.

### Corpus

Deterministic synthetic repository generator (an extension of the existing
template generator, seeded, no repository source files read):

- `SMALL`: 500 files (~2 MB text); `LARGE`: 5000 files (~25 MB text). Mixed
  Go/Markdown, short and long sections, generated and minified noise included
  (the content filter must see real work).
- **Duplicate-content fraction:** 10% of files exist VERBATIM at a second path
  (copy with a different name). The store must keep both occurrences
  searchable (asserted by ground truth) while storing one set of chunk
  records (asserted by the ledger).
- **Ground-truth query set:** ≥ 20 queries with known-correct answers derived
  from the generator's manifest (which file, which section). Queries are
  boundary-sensitive like the retrieval-quality set and include two queries
  whose correct answers are duplicated-content paths (BOTH must be findable).

### Topology grid

Branches per root: `1, 4, 12`. Roots: `1, 2, 4` (the checkout plus managed
worktrees of the same repository). Branch fan-out shape: all branches forked
from ONE indexed commit (the sharing-best case) for the fork-reuse scenario,
and from DISTINCT commits (real diversity) for the storage-cost scenario.
Every case runs in its own process (peak-RSS discipline, as above).

### Scenarios

- **cold full index**: fresh store, full pass at commit A (includes corpus
  walk + inference; ONNX init is timed separately, see below).
- **cold ONNX init (measured separately)**: embedder session creation + first
  inference, timed as its own stage and REPORTED SEPARATELY from every store
  scenario. No scenario's wall time may include an un-attributed ONNX start;
  warm-attach numbers are meaningless otherwise.
- **warm attach**: process restart against an existing fully-indexed store,
  opening the branch's view — store-open time only (no indexing work). The
  ADR-064 successor metric: must be O(one table), not O(branch documents).
- **branch fork reuse**: create a new branch from indexed commit A; time to
  first correct search on it. Must complete with ZERO corpus bytes read
  (assert `chunks-written == 0`, `files-walked == 0`) — a pointer write.
- **branch switch**: A → B → A round trip on a multi-branch store.
- **incremental**: edit ≤ 10 files (dirty overlay path); full pass time.
- **concurrent roots**: R roots issuing ground-truth searches while one root
  runs a cold full index — search p95 under load and indexing throughput
  regression vs the unloaded cold index.
- **migration (once)**: v1 layout (per-branch chromem roots) → v2 store for
  the same corpus; wall time and transient disk overhead (rollback window).

### Primary metric: time-to-first-correct-search (TTFS)

From the operation's start (process start / project switch / branch switch /
fork) to the moment a ground-truth query batch returns EVERY expected file at
rank ≤ 3. Readiness events or "ready" states do NOT stop the clock —
correctness does (the honest-status principle of ADR-064, generalized from
"honest about preparing" to "honest about correct"). TTFS is reported per
scenario as median and p95 over the repetitions.

### Latency budgets (seconds) — the gate numbers

Budgets are targets on the reference environment class (the baseline machine
above, `LARGE` corpus unless noted); a roadmap step that regresses a budget
beyond noise (>5%, per the optimization-claim threshold) does not land
without an explicit owner decision:

| Scenario | Budget | Notes |
|---|---:|---|
| warm attach, 12 branches | ≤ 1.5 s | to first correct search ≤ 2.0 s |
| branch fork from indexed commit | ≤ 1.5 s | zero corpus bytes read (hard assert) |
| cold full index, LARGE (excl. ONNX init) | ≤ 60 s | ONNX cold init reported separately, ≤ 3 s |
| cold full index, SMALL (excl. ONNX init) | ≤ 8 s | |
| incremental pass (≤ 10 files) | ≤ 5 s | |
| branch switch (warm) | ≤ 1.5 s | |
| search p95, warm, top-10 hybrid | ≤ 0.15 s | per query, unloaded |
| search p95 under concurrent indexing (4 roots) | ≤ 0.5 s | indexing throughput regression ≤ 30% |
| migration v1→v2, LARGE | ≤ 300 s | background; UI never blocks |

Budget amendment (2026-11-03, owner decision): on the first LARGE-corpus
full-grid landing the sub-second TTFS class measured at the edge of
inter-invocation variance on the reference machine — across two independent
full-grid invocations the medians spanned warm attach (12 branches) 0.60–1.02 s,
fork 1.03–1.27 s, switch 1.07–1.34 s — while every structural assertion held
on every repetition (one-table decode regardless of branch count, zero-write
fork, zero re-embeds). The owner amended the three 1.0 s budgets to
≤ 1.5 s (LARGE) rather than gate the protocol on machine noise; optimization
of the table-decode path remains a benchstat-gated follow-up. The SMALL
reference stays far inside every budget.

### Repetitions and reporting

Minimum `COUNT=5` per case, median + p95 reported; comparisons use
`benchstat` with the repository's 5% noise threshold. Single-iteration runs
may be checked in as reference coverage (as the 2026-09-09 baseline is) but
are never evidence for or against a budget. Assertions (zero-corpus-read on
fork, both duplicate-path occurrences findable, ledger dedup: unique chunk
bytes counted once across views) are evaluated on EVERY repetition and any
failure fails the case — they are correctness gates, not statistics.

### Baseline (2026-11-03, SMALL corpus): first full publication

Environment: Apple M4 Max, Darwin/arm64, Go 1.27.3, deterministic host
embedder (16-dim bag-of-runes) + the REAL chunker — no ONNX assets
required; `format:2` store, one isolated `go test` process per scenario,
`-benchtime=1x` (single-iteration reference coverage; `COUNT=5`+ remains
the rule for optimization claims). Corpus: SMALL = 500 generated files
(2 MB text, mixed Go/Markdown + minified noise) + 50 verbatim duplicate
paths = 550 manifest files, 2 730 chunk records, 24 ground-truth queries
(2 duplicate-path). Topology: warm_attach at 4 branches (the script's grid
covers 1/4/12); concurrent_roots at 2 roots.

The v1 arm ("before") is the same tree with the corpus registry unwired —
the legacy per-branch store model. The v2 arm is the ADR-104 store.

| Scenario (SMALL) | TTFS / metric | embed texts | chunks written | storage |
|---|---:|---:|---:|---:|
| cold full, v1 (before) | 1564 ms | 2598 | 2730 | 1.90 MiB/branch |
| cold full, v2 (after) | 1863 ms | 2598 | 2730 | 2.26 MiB shared |
| warm attach (4 branches' history) | 260 ms | 0 | 0 | — |
| branch fork from indexed commit | 320 ms | 0 | 0 | +0 bytes |
| branch switch A→B / B→A, v1 | 266 / 284 ms | 0 | 0 | per-branch copy |
| branch switch A→B / B→A, v2 | 300 / 298 ms | 0 | 0 | shared |
| incremental (10-file edit) | 93 ms | 62 | 65 | overlay delta |
| migration v1→v2 (rename promotion) | 411 ms | 0 | 0 | +0.36 MiB transient |
| eviction re-derive (re-attach) | 279 ms | 0 | 0 | — |
| concurrent: cold build under 1 searcher | 2122 ms | 2611 | — | search p50 1.76 ms, p95 4.41 ms, 0 errors, −12% throughput |

Structural counters (asserted on every repetition, not sampled):
`corpus-decodes`=1 for a fresh-process warm attach (one table, regardless
of branch history); fork asserts `chunks-written == 0` AND `embed-texts == 0`
(a pointer write); migration and eviction assert zero re-embeddings; the
duplicate-path ground-truth queries assert BOTH occurrences findable on
every pass; the dedup ledger holds one corpus on disk for all branches of
one commit while v1 pays the full per-branch copy.

Reading: **cold parity with a bounded publish overhead** — v2's cold pass
pays +19% wall (corpus publication + manifest + attach) and +0.36 MiB
transient disk once; in exchange every warm operation is structurally
cheaper: a fork is a pointer write (v1 re-walks, re-chunks and re-commits
the whole corpus per branch — 2598 embeds' worth of work plus a full disk
copy per branch), warm attach decodes exactly one table with zero
stat-walk work, and multi-branch storage stops multiplying (2.26 MiB
shared vs 1.90 MiB × branches). Budget check on this reference
environment: warm attach 0.26 s; fork 0.32 s with the hard zero-write
assert green; cold SMALL 1.86 s; incremental 93 ms; branch switch ~0.3 s;
search p95 under load 4.41 ms with −12% indexing throughput. The
LARGE-corpus full grid (branches 1/4/12 × roots 1/2/4, `COUNT=5`) is
recorded in the next section.

### Full grid (2026-11-03, LARGE corpus): complete topology publication

Environment: Apple M4 Max, Darwin/arm64, Go 1.27.3, deterministic host
embedder (16-dim bag-of-runes) + the REAL chunker — no ONNX assets
required; `format:2` store, one isolated `go test` process per scenario,
`-benchtime=1x`, `COUNT=5` (median [p95] below). Corpus: LARGE = 5000
generated files (~25 MB text) + 500 verbatim duplicate paths = 5500
manifest files, 27 252 chunk records, 24 ground-truth queries (2
duplicate-path). Topology: the full grid — warm attach at 1/4/12 branches;
concurrent roots at 2/4 (`concurrent_roots` is defined as "searchers on
R−1 warm roots while one root cold-builds" and requires R ≥ 2; the script
skips a roots=1 cell with a printed note — that cell degenerates to the
unloaded cold_full case measured separately).

| Scenario (LARGE) | TTFS / metric — median [p95] | embed texts | chunks written | storage |
|---|---:|---:|---:|---:|
| cold full, v1 (before) | 13.3 [13.7] s | 25 873 | 27 252 | 16.6 MiB/branch |
| cold full, v2 (after) | 24.2 [25.7] s | 25 873 | 27 252 | 21.6 MiB shared |
| warm attach, 1 branch | attach 832 [859] ms; TTFS 928 [951] ms | 0 | 0 | 21.7 MiB |
| warm attach, 4 branches | attach 433 [837] ms; TTFS 471 [929] ms | 0 | 0 | 79.9 MiB |
| warm attach, 12 branches | attach 599 [833] ms; TTFS 692 [923] ms | 0 | 0 | 234.8 MiB |
| branch fork from indexed commit | 1.03 [1.17] s | 0 | 0 | +0 bytes |
| branch switch A→B / B→A | 1.07 [1.34] / 1.23 [1.95] s | 0 | 0 | shared |
| incremental (10-file edit) | 165 [210] ms | 56 | 58 | overlay delta |
| migration v1→v2 (rename promotion) | 1.59 [1.63] s | 0 | 0 | +1.38 MiB transient |
| eviction re-derive (re-attach) | 497 [574] ms | 0 | 0 | 40.9 MiB |
| concurrent: 2 roots (1 searcher) | cold-under-load 14.2 [14.6] s | 25 885 | — | search p50 3.4 / p95 5.9 [6.3] ms, 3 965 samples, 0 errors |
| concurrent: 4 roots (3 searchers) | cold-under-load 16.7 [17.6] s | 25 887 | — | search p50 5.2 / p95 11.3 [12.5] ms, 8 497 samples, 0 errors |

Structural counters (asserted on every repetition, not sampled): warm
attach `corpus-decodes`=1 at 1, 4 AND 12 branches (one table, regardless
of branch history); fork asserts `chunks-written == 0` AND
`embed-texts == 0` on every repetition (a pointer write); migration and
eviction assert zero re-embeddings; the duplicate-path ground-truth
queries assert BOTH occurrences findable on every pass; concurrent
search-errors=0 across ~8 500 sampled searches at 4 roots.

Budget check (owner-amended 1.5 s class above): warm attach 12 branches
attach 0.599 s ≤ 1.5 s and TTFS 0.923 s ≤ 2.0 s; fork 1.03 s ≤ 1.5 s with
the hard zero-write assert green; branch switch 1.07/1.23 s ≤ 1.5 s; cold
LARGE 24.2 s ≤ 60 s; incremental 165 ms ≤ 5 s; migration 1.59 s ≤ 300 s;
search p95 under 4-root indexing 11.3 ms ≤ 0.5 s with NO indexing
throughput regression (299 files/s under load vs 206 files/s unloaded —
the under-load arm rides the content-addressed embedding cache). Every
budget green.

Inter-run variance (recorded for honesty): a same-day first full-grid
invocation — aborted at a since-fixed script defect that fed the harness
an invalid `roots=1` concurrent cell — measured the ten shared sections
green with medians spanning warm attach (12 branches) 1.021 s vs 0.599 s
here, fork 1.269 s vs 1.030 s, switch 1.082/1.343 s vs 1.072/1.231 s,
cold v2 44.4 s vs 24.2 s. That spread across independent invocations —
not within-run noise — is what the owner's 1.5 s amendment of the
sub-second class accounts for; within-run repetition spread stayed small
(e.g. switch p95 1.34 s at medians 1.07 s).

Reading: **at 10× the SMALL corpus every warm operation stays at the
one-table-decode floor** — attach cost is flat across 1/4/12 branches
(433–832 ms medians, dominated by the single table decode plus the
24-query correctness batch), a fork remains a pointer write (0 chunks, 0
embeds, +0 bytes), and the store's dedup ledger holds unique chunk bytes
counted once across views on every repetition (asserted, not sampled).
The warm-attach topology pins each branch at a DISTINCT commit — the
storage-cost shape — and the measured store grows 21.7 → 79.9 → 234.8 MiB
across 1/4/12 such branches; the sharing-best shape is measured by the
fork row instead (+0 bytes per additional branch at one indexed commit).
The cold pass pays the corpus publication openly: +82% wall vs the v1 arm
at LARGE (24.2 s vs 13.3 s — 27 252 chunk records + the 5500-row
occurrence table + the lexical leg) with identical inference counts
(25 873 embeds both arms), a one-time cost every warm operation then
amortizes.

Run:

```sh
STORE_PROTOCOL_SCENARIOS='warm_attach' STORE_PROTOCOL_BRANCHES='1 4 12' \
  scripts/benchmark-vector-index.sh
```
