# ADR-005: Bleve + RRF Hybrid Search

## Status

Superseded by [013](./013-rrf-pre-fusion-score-thresholds.md)

> **Drift note (2026-08-25, vibespec-check):** The shared document-ID notation is `sha256(path)` truncated to the first 8 BYTES rendered as 16 hex chars (core/vectorindex/collection.go), not 8 hex characters as written in Decision §3.

## Context

The existing vector index uses chromem-go (cosine similarity on ONNX embeddings) for semantic code search. Semantic search excels at concept/intent queries but struggles with exact identifier lookups (e.g., "MatcherFactory", "handleUserAuth"). Users frequently need both modes — a natural-language "how does auth work?" query and an exact-match "find all usages of parseHTMLDoc" query — ideally without toggling modes manually.

Forces at play:

- Chromem-go cosine similarity is excellent for meaning but degrades on identifiers and short unique strings.
- BM25/keyword search is excellent for identifiers but cannot understand paraphrases.
- Adding a separate search backend introduces dual-write complexity and potential index drift.
- The project already avoids CGO (`modernc.org/sqlite`); a CGO-free BM25 library is required.
- The codebase uses Go 1.26 and a single module (`github.com/v0lka/c0wrk`).

## Decision

1. **Add `github.com/blevesearch/bleve/v2`** (scorch backend, pure Go) as the lexical retriever.
2. **Register a custom `c0wrk_code` analyzer** that chains: unicode tokenizer → camelCase splitter (`c0wrk_camel_case`) → `to_lower` → `stop_en`. This ensures `MatcherFactory` matches queries for "matcher" and "factory".
3. **Share document IDs** between chromem and bleve (`sha256(path)[:8hex]:{chunkIndex}`). Lexical index stores `Content` and keyword fields (`FilePath`, `Language`) but does NOT store term vectors. Lexical hits are enriched via `chromem.Collection.GetByID` so the result struct is identical.
4. **Fuse ranked lists with Reciprocal Rank Fusion** (`score = Σ 1/(k+rank)` with `k=60`). Per-side fanout is `max(topK*4, 100)` to feed RRF with enough candidates.
5. **Per-side filters (glob + MustMatch) are applied BEFORE fusion** so rank spaces are comparable across retrievers.
6. **Dual-write**: chromem commits first (source of truth); lexical upsert/delete is best-effort. Drift is repaired by `Indexer.RebuildLexical` invoked automatically by the manager when `chromem.Count() > 0 && lexical.Count() == 0`.
7. **Auto-fallback**: `ModeHybrid` degrades to `ModeVector` when lexical is empty or unavailable (transient upgrade path for existing users).
8. **Config knob**: `vector_index.hybrid: *bool` (pointer-bool, defaults `true`).

## Consequences

**Positive:**

- Users get high-quality results for both concept queries and identifier searches out of the box.
- Existing embedding infrastructure (ONNX, chromem, chunker) unchanged.
- Auto-fallback means zero migration friction for existing users; lexical backfill happens on first project open.
- The `+token` sugar in the agent tool gives LLMs a simple mechanism to pin mandatory identifiers.

**Negative:**

- Disk footprint increases (bleve index alongside chromem data).
- Index time increases by ~20-40% (dual write + bleve flush).
- Complexity of reconciliation logic (RebuildLexical) — failure paths must be tested.
- Bleve upgrades require attention to index format compatibility.

## Alternatives Considered

1. **Tantivy (via FFI/CGO):** High performance but introduces CGO dependency, violating the project's CGO-free policy.
2. **Custom inverted index:** Lower dependency but reimplementing tokenization, scoring, and persistence is non-trivial and maintenance-heavy.
3. **Pre-filter + re-rank:** Use embedding-only retrieval and re-rank with BM25. Rejected because identifier-only queries return no candidates from the embedding pass at all.
4. **Elasticsearch/Meilisearch external process:** Heavyweight for a desktop app; deployment/startup complexity unacceptable.
