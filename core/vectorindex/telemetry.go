package vectorindex

import (
	"sync"
	"time"
)

// TelemetryStage is a bounded label for one vector-index pipeline stage.
type TelemetryStage string

const (
	// StageWalkValidation measures full walks and incremental validation.
	StageWalkValidation TelemetryStage = "walk_validation"
	// StageReadHashChunk measures file preparation work.
	StageReadHashChunk TelemetryStage = "read_hash_chunk"
	// StageCacheLookup measures file-hash sidecar access.
	StageCacheLookup TelemetryStage = "cache_lookup"
	// StageEmbedding measures document embedding work.
	StageEmbedding TelemetryStage = "embedding"
	// StageChromemCommit measures vector-store commits.
	StageChromemCommit TelemetryStage = "chromem_commit"
	// StageBleveUpsert measures lexical batch commits.
	StageBleveUpsert TelemetryStage = "bleve_upsert"
)

// StageMetrics contains aggregate timing and item counts for a bounded stage.
type StageMetrics struct {
	Calls    int64
	Items    int64
	Duration time.Duration
}

// TelemetrySnapshot is an aggregate, content-free view of indexing work:
// numeric aggregates only — never source text, paths, project IDs, branch
// names, or caller-defined labels. See specs/domains/workspace.md (Vector Index).
type TelemetrySnapshot struct {
	Stages          map[TelemetryStage]StageMetrics
	Batches         int64
	BatchItems      int64
	BatchCapacity   int64
	EmbeddingHits   int64
	EmbeddingMisses int64
}

// EmbeddingCacheHitRatio reports content-addressed cache hits among lookups.
func (s TelemetrySnapshot) EmbeddingCacheHitRatio() float64 {
	total := s.EmbeddingHits + s.EmbeddingMisses
	if total == 0 {
		return 0
	}
	return float64(s.EmbeddingHits) / float64(total)
}

// BatchFill returns the fraction of configured ONNX rows occupied by the
// indexer's streaming batches; at most 1 (only a final-pass tail underfills).
func (s TelemetrySnapshot) BatchFill() float64 {
	if s.BatchCapacity == 0 {
		return 0
	}
	return float64(s.BatchItems) / float64(s.BatchCapacity)
}

// Telemetry aggregates low-cardinality indexing metrics (content-free — see
// specs/domains/workspace.md); safe for concurrent prep workers; nil no-op.
type Telemetry struct {
	mu              sync.Mutex
	stages          map[TelemetryStage]StageMetrics
	batches         int64
	batchItems      int64
	batchCapacity   int64
	embeddingHits   int64
	embeddingMisses int64
}

func (t *Telemetry) observeEmbeddingCache(hit bool) {
	if t == nil {
		return
	}
	t.mu.Lock()
	if hit {
		t.embeddingHits++
	} else {
		t.embeddingMisses++
	}
	t.mu.Unlock()
}

func (t *Telemetry) observe(stage TelemetryStage, items int, elapsed time.Duration) {
	if t == nil {
		return
	}
	t.mu.Lock()
	if t.stages == nil {
		t.stages = make(map[TelemetryStage]StageMetrics, 6)
	}
	m := t.stages[stage]
	m.Calls++
	m.Items += int64(items)
	m.Duration += elapsed
	t.stages[stage] = m
	t.mu.Unlock()
}

func (t *Telemetry) observeBatch(items, capacity int) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.batches++
	t.batchItems += int64(items)
	t.batchCapacity += int64(capacity)
	t.mu.Unlock()
}

// Snapshot returns a stable copy of the current aggregate metrics.
func (t *Telemetry) Snapshot() TelemetrySnapshot {
	if t == nil {
		return TelemetrySnapshot{Stages: map[TelemetryStage]StageMetrics{}}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	stages := make(map[TelemetryStage]StageMetrics, len(t.stages))
	for stage, metrics := range t.stages {
		stages[stage] = metrics
	}
	return TelemetrySnapshot{
		Stages: stages, Batches: t.batches,
		BatchItems: t.batchItems, BatchCapacity: t.batchCapacity,
		EmbeddingHits: t.embeddingHits, EmbeddingMisses: t.embeddingMisses,
	}
}

// Reset clears all aggregates while preserving the collector instance.
func (t *Telemetry) Reset() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.stages = nil
	t.batches, t.batchItems, t.batchCapacity = 0, 0, 0
	t.embeddingHits, t.embeddingMisses = 0, 0
	t.mu.Unlock()
}
