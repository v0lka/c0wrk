# ADR-013: Pre-Fusion Score Thresholds for RRF Hybrid Search

## Status

Accepted

## Context

ADR-005 introduced Bleve BM25 + Reciprocal Rank Fusion (RRF) hybrid search. RRF uses only document rank, ignoring score magnitude: a cosine similarity of 0.05 (noise) and 0.45 (relevant) at the same rank contribute identically to the fused score.

With a large per-side fanout (`max(topK*4, 100)` → 200 for `topK=50`) and `k=60`, documents appearing in the tail of **both** retriever lists earn a double RRF contribution (`2/(60+rank)`) that outweighs the single contribution of a genuinely relevant one-sided hit at rank 1 (`1/61`). A noise chunk that is weakly semantic yet contains the query term — e.g. `packages.json` matching "blackboard" — lands in both tails, receives a double boost, and jumps above relevant results that are strong in only one retriever. This is the root cause of users seeing "completely inadequate" hybrid results while vector-only and lexical-only modes return relevant hits.

The existing test corpus (`seedHybridService`, 2 documents) cannot reproduce this: with no tail, there is no tail intersection, so RRF behaves correctly. The bug only manifests on a real index with a noise tail in both lists.

## Decision

1. **Apply pre-fusion score thresholds** to each retriever's ranked list **before** RRF fusion, extending ADR-005's "pre-fusion filtering" principle from path/content filters to score magnitude:
   - **Vector side**: discard hits with cosine similarity below an absolute floor (`VectorScoreFloor`) OR below a relative cutoff (`VectorScoreRatio × top similarity` among post-filter vector hits).
   - **Lexical side**: discard hits with BM25 below a relative cutoff (`LexicalScoreRatio × top BM25` among post-filter lexical hits). A relative threshold is preferred over an absolute one because BM25 magnitudes vary widely across queries.
2. **Make RRF parameters configurable** via `vector_index.hybrid_*` config knobs, replacing the hardcoded `rrfK=60` and `defaultHybridFanout=100` constants:
   - `hybrid_rrf_k` (int, default 60) — RRF constant k.
   - `hybrid_fanout_multiplier` (int, default 4) — per-side pool = `max(topK × multiplier, fanout_min)`.
   - `hybrid_fanout_min` (int, default 100) — minimum per-side fanout.
   - `hybrid_vector_score_floor` (float, default 0.0) — absolute cosine floor; 0 disables.
   - `hybrid_vector_score_ratio` (float, default 0.25) — relative cosine cutoff; 0 disables.
   - `hybrid_lexical_score_ratio` (float, default 0.1) — relative BM25 cutoff; 0 disables.
3. **Two-pass aggregation** in `aggregateVectorHits` / `aggregateLexicalHits`: first pass applies path/must-match filters and computes the top score among survivors; second pass applies the score gate, assigns 1-based ranks over survivors, and adds the RRF contribution. Hits failing the score gate receive no RRF contribution, so noise-tail documents cannot earn a double boost.
4. **Thresholds apply only in the hybrid path** (`hybridSearchRRF`). Vector-only and lexical-only modes return their raw top-K without score gating, preserving their existing behavior and test expectations.
5. **Zero-valued thresholds disable the gate** — a zero `HybridConfig` (used by tests that don't care about score gating) passes every hit. Production wiring sets non-zero defaults via `config.ApplyDefaults`.

## Consequences

**Positive:**

- Noise-tail documents that are weakly semantic yet contain a query term no longer dominate hybrid top-K. One-sided relevant hits survive in the fused results.
- RRF parameters are tunable per corpus without code changes, enabling calibration for different codebase sizes and query patterns.
- The noise-tail scenario is now covered by `TestHybridSearch_NoiseCorpusRRF`, which models a 45-document corpus with relevant tops, one-sided relevant hits, and a 40-document noise tail in both lists.

**Negative:**

- Slightly reduced recall: a document at rank 80 in the vector list and rank 5 in the lexical list may be discarded by the vector score gate before it can benefit from its strong lexical rank. This is an acceptable trade-off because such documents are, by definition, weakly semantic.
- Two new config knobs that users may need to tune for unusual corpora. Defaults (0.25 vector ratio, 0.1 lexical ratio) are calibrated for typical code-search workloads.
- The score gate adds a second pass over each retriever's results, but this is O(fanout) and negligible relative to the embedding/BM25 query cost.

## Alternatives Considered

1. **Weighted fusion instead of pure RRF (ADR-005 option C):** Normalize scores to [0,1] and combine as `w_v × normVec + w_l × normLex + rrf_bonus`. Most robust but requires score normalization and weight calibration. Deferred pending user testing of the threshold approach; can be layered on top if thresholds prove insufficient.
2. **Reduce fanout closer to topK (ADR-005 option A):** Changing `hybridFanout` from `max(topK*4, 100)` to `max(topK*2, topK+30)` narrows the noise window but does not eliminate it (the `r ≤ 61` window still exceeds `topK=50`). Now achievable via the `hybrid_fanout_multiplier` / `hybrid_fanout_min` knobs without code changes, but not enabled by default.
3. **Reduce k (ADR-005 option D):** `k=30` steepens the RRF curve but does not distinguish noise from relevance at the same rank. A half-measure; now tunable via `hybrid_rrf_k` if needed.
4. **AND operator for lexical queries (ADR-005 option E):** `mq.SetOperator(bleve.MatchQueryOperatorAnd)` reduces noise for multi-term queries but does not affect single-term queries. Deferred pending user testing.

## Related Specs

- [005-bleve-rrf-hybrid-search.md](./005-bleve-rrf-hybrid-search.md) — superseded ADR (original RRF design)
- [../domains/workspace.md](../domains/workspace.md) — hybrid search behavior and configuration
