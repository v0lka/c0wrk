package vectorindex

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	chromem "github.com/philippgille/chromem-go"
)

// countingEmbedFunc wraps fakeEmbeddingFunc with an invocation counter so
// tests can observe when an index pass reaches the embedder.
func countingEmbedFunc(counter *atomic.Int64) chromem.EmbeddingFunc {
	base := fakeEmbeddingFunc()
	return func(ctx context.Context, text string) ([]float32, error) {
		counter.Add(1)
		return base(ctx, text)
	}
}

// TestNewManager_TuningKnobs_ResolvedAndStored verifies that every
// vector_index tuning knob set on ManagerConfig is resolved and stored on
// the Manager — and that EmbeddingBatchSize is forwarded to the Service for
// the batched-embedding path (the desktop startup sets the same value on
// the sp4rk EmbedderConfig).
func TestNewManager_TuningKnobs_ResolvedAndStored(t *testing.T) {
	mgr, err := NewManager(ManagerConfig{
		EmbeddingFunc:      fakeEmbeddingFunc(),
		EmbeddingBatchSize: 7,
		PrepWorkers:        5,
		Debounce:           250 * time.Millisecond,
		ChunkOverlap:       77,
		SearchWaitTimeout:  4 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { mgr.Shutdown() })

	if mgr.embeddingBatchSize != 7 {
		t.Errorf("Manager.embeddingBatchSize = %d, want 7", mgr.embeddingBatchSize)
	}
	if mgr.service.embeddingBatchSize != 7 {
		t.Errorf("ManagerConfig.EmbeddingBatchSize must be forwarded to the Service, got service batch size %d, want 7", mgr.service.embeddingBatchSize)
	}
	if mgr.prepWorkers != 5 {
		t.Errorf("Manager.prepWorkers = %d, want 5", mgr.prepWorkers)
	}
	if mgr.debounce != 250*time.Millisecond {
		t.Errorf("Manager.debounce = %v, want 250ms", mgr.debounce)
	}
	if got := mgr.effectiveDebounce(); got != 250*time.Millisecond {
		t.Errorf("effectiveDebounce() = %v, want 250ms", got)
	}
	if mgr.chunkOverlap != 77 {
		t.Errorf("Manager.chunkOverlap = %d, want 77", mgr.chunkOverlap)
	}
	if mgr.searchWaitTimeout != 4*time.Second {
		t.Errorf("Manager.searchWaitTimeout = %v, want 4s (stored raw for the search-path wiring)", mgr.searchWaitTimeout)
	}
}

// TestNewManager_TuningKnobs_ZeroValuesKeepDefaults pins the historical
// behaviour: a zero-value ManagerConfig (or a zero-value Manager literal)
// resolves every knob to its pre-config hardcoded default. This is what
// keeps existing configs without a vector_index block identical to today.
func TestNewManager_TuningKnobs_ZeroValuesKeepDefaults(t *testing.T) {
	mgr, err := NewManager(ManagerConfig{EmbeddingFunc: fakeEmbeddingFunc()})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { mgr.Shutdown() })

	if mgr.embeddingBatchSize != DefaultEmbeddingBatchSize {
		t.Errorf("default embeddingBatchSize = %d, want DefaultEmbeddingBatchSize (%d)", mgr.embeddingBatchSize, DefaultEmbeddingBatchSize)
	}
	if mgr.service.embeddingBatchSize != DefaultEmbeddingBatchSize {
		t.Errorf("default service embeddingBatchSize = %d, want DefaultEmbeddingBatchSize (%d)", mgr.service.embeddingBatchSize, DefaultEmbeddingBatchSize)
	}
	if mgr.prepWorkers != DefaultPrepWorkers {
		t.Errorf("default prepWorkers = %d, want DefaultPrepWorkers (%d)", mgr.prepWorkers, DefaultPrepWorkers)
	}
	if mgr.chunkOverlap != DefaultChunkOverlap {
		t.Errorf("default chunkOverlap = %d, want DefaultChunkOverlap (%d)", mgr.chunkOverlap, DefaultChunkOverlap)
	}
	if got := mgr.effectiveDebounce(); got != DefaultDebounce {
		t.Errorf("default effectiveDebounce() = %v, want DefaultDebounce (%v)", got, DefaultDebounce)
	}
	// SearchWaitTimeout intentionally carries NO default at this layer:
	// 0 is the explicit "fail fast" sentinel resolved only at the config
	// layer (unset key → 3000 ms there).
	if mgr.searchWaitTimeout != 0 {
		t.Errorf("default searchWaitTimeout = %v, want 0 (fail-fast sentinel)", mgr.searchWaitTimeout)
	}
}

// TestManagerDebounce_ConfiguredDurationReachesTimer proves the configured
// debounce (vector_index.debounce_ms) reaches the incremental-pass timer in
// scheduleIncrementalLocked: after NotifyFileChange with a 150 ms window, no
// embed call may happen within the first 60 ms (timers never fire early, so
// the negative half is deterministic), and one must land shortly after the
// window elapses. A regression back to the hardcoded 1 s would fail the
// second half of the assertion.
func TestManagerDebounce_ConfiguredDurationReachesTimer(t *testing.T) {
	const debounce = 150 * time.Millisecond
	var embeds atomic.Int64

	mgr, err := NewManager(ManagerConfig{
		EmbeddingFunc: countingEmbedFunc(&embeds),
		Debounce:      debounce,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	// Create the temp dirs BEFORE registering the Shutdown cleanup:
	// t.Cleanup is LIFO, so Shutdown (registered later) runs first and
	// releases the lexical store (.zap/.bolt) handles before TempDir's
	// RemoveAll. Windows refuses to delete files with open handles (EBUSY).
	ws := t.TempDir()
	viRoot := t.TempDir()
	t.Cleanup(func() { mgr.Shutdown() })

	if err := os.WriteFile(filepath.Join(ws, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	if err := mgr.SwitchProject("proj-debounce", ws, filepath.Join(viRoot, "vi"), ProjectCallbacks{}); err != nil {
		t.Fatalf("SwitchProject: %v", err)
	}
	if err := mgr.Service().WaitReady(context.Background()); err != nil {
		t.Fatalf("initial index never became ready: %v", err)
	}
	baseline := embeds.Load()

	// A change that requires an incremental pass once the debounce fires.
	if err := os.WriteFile(filepath.Join(ws, "b.go"), []byte("package b\n"), 0o644); err != nil {
		t.Fatalf("write b.go: %v", err)
	}
	mgr.NotifyFileChange()

	// Before the window elapses nothing may have fired.
	time.Sleep(60 * time.Millisecond)
	if got := embeds.Load(); got != baseline {
		t.Fatalf("incremental pass fired before the configured %v debounce elapsed: embeds %d → %d", debounce, baseline, got)
	}

	// After the window (+ generous execution margin) the pass must have run.
	deadline := time.Now().Add(3 * time.Second)
	for embeds.Load() == baseline && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := embeds.Load(); got == baseline {
		t.Fatal("incremental pass never fired after the configured debounce (a 1 s hardcoded window would also fail this deadline)")
	}
}

// TestManagerChunkOverlap_ReachesChunker proves vector_index.chunk_overlap
// flows from ManagerConfig.ChunkOverlap through initProject's
// IndexerConfig.Overlap into the chunk function invoked during indexing.
// The field was previously dead — never set by the Manager, so the chunker
// always saw NewIndexer's hardcoded default.
func TestManagerChunkOverlap_ReachesChunker(t *testing.T) {
	var gotOverlap atomic.Int32
	gotOverlap.Store(-1)
	chunkFn := func(_ string, _ []byte, _, overlap int) ([]ChunkResult, error) {
		gotOverlap.Store(int32(overlap))
		return []ChunkResult{{Content: "package a\n", Language: "go"}}, nil
	}

	mgr, err := NewManager(ManagerConfig{
		EmbeddingFunc: fakeEmbeddingFunc(),
		ChunkFn:       chunkFn,
		ChunkOverlap:  77,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	// Create the temp dirs BEFORE registering the Shutdown cleanup:
	// t.Cleanup is LIFO, so Shutdown (registered later) runs first and
	// releases the lexical store (.zap/.bolt) handles before TempDir's
	// RemoveAll. Windows refuses to delete files with open handles (EBUSY).
	ws := t.TempDir()
	viRoot := t.TempDir()
	t.Cleanup(func() { mgr.Shutdown() })

	if err := os.WriteFile(filepath.Join(ws, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	if err := mgr.SwitchProject("proj-overlap", ws, filepath.Join(viRoot, "vi"), ProjectCallbacks{}); err != nil {
		t.Fatalf("SwitchProject: %v", err)
	}
	if err := mgr.Service().WaitReady(context.Background()); err != nil {
		t.Fatalf("initial index never became ready: %v", err)
	}

	if got := gotOverlap.Load(); got != 77 {
		t.Fatalf("chunker received overlap %d, want 77 (ManagerConfig.ChunkOverlap → IndexerConfig.Overlap → ChunkFunc)", got)
	}
}

// TestManagerPrepWorkers_ReachesIndexer proves vector_index.prep_workers
// flows from ManagerConfig.PrepWorkers through initProject's
// IndexerConfig.PrepWorkers into the per-project Indexer that runs index
// passes. (The pool's behavior itself — overlap, document-set equality,
// cancellation, monotonic progress — is covered by the indexer tests; this
// pins only the Manager wiring.)
func TestManagerPrepWorkers_ReachesIndexer(t *testing.T) {
	mgr, err := NewManager(ManagerConfig{
		EmbeddingFunc: fakeEmbeddingFunc(),
		PrepWorkers:   3,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	// Create the temp dirs BEFORE registering the Shutdown cleanup:
	// t.Cleanup is LIFO, so Shutdown (registered later) runs first and
	// releases the lexical store (.zap/.bolt) handles before TempDir's
	// RemoveAll. Windows refuses to delete files with open handles (EBUSY).
	ws := t.TempDir()
	viRoot := t.TempDir()
	t.Cleanup(func() { mgr.Shutdown() })

	if err := os.WriteFile(filepath.Join(ws, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	if err := mgr.SwitchProject("proj-prepworkers", ws, filepath.Join(viRoot, "vi"), ProjectCallbacks{}); err != nil {
		t.Fatalf("SwitchProject: %v", err)
	}
	if err := mgr.Service().WaitReady(context.Background()); err != nil {
		t.Fatalf("initial index never became ready: %v", err)
	}

	mgr.mu.RLock()
	indexer := mgr.indexer
	mgr.mu.RUnlock()
	if indexer == nil {
		t.Fatal("expected the project indexer to be published after init")
	}
	if indexer.prepWorkers != 3 {
		t.Errorf("Indexer.prepWorkers = %d, want 3 (ManagerConfig.PrepWorkers → IndexerConfig.PrepWorkers)", indexer.prepWorkers)
	}
}
