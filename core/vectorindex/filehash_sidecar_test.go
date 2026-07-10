package vectorindex

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	chromem "github.com/philippgille/chromem-go"
)

// TestGetCollectionFileHashes_NoEmbeddingAfterPopulation verifies that after
// AddDocuments has populated the file-hash sidecar, reading the hashes does NOT
// invoke the embedding function — fixing the wasted "running inference" on every
// no-op ValidateCollection pass (the root cause of the nightly reindex churn).
func TestGetCollectionFileHashes_NoEmbeddingAfterPopulation(t *testing.T) {
	var embedCalls atomic.Int32
	embed := func(_ context.Context, _ string) ([]float32, error) {
		embedCalls.Add(1)
		return []float32{0.1, 0.2}, nil
	}

	svc, err := NewService(ServiceConfig{EmbeddingFunc: embed})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	dir := t.TempDir()
	if err := svc.SetProject("proj", dir); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch: %v", err)
	}

	// Add documents (this embeds + populates the sidecar).
	docs := []chromem.Document{
		{ID: "d1", Content: "hello", Metadata: map[string]string{"file_path": filepath.Join(dir, "a.go"), "content_hash": "hashA"}},
		{ID: "d2", Content: "world", Metadata: map[string]string{"file_path": filepath.Join(dir, "b.go"), "content_hash": "hashB"}},
	}
	svc.AcquireWriteLock()
	if err := svc.AddDocuments(context.Background(), docs, nil); err != nil {
		svc.ReleaseWriteLock()
		t.Fatalf("AddDocuments: %v", err)
	}
	svc.ReleaseWriteLock()

	embedCalls.Store(0)

	// Reading hashes must NOT embed now that the sidecar is populated.
	svc.mu.RLock()
	hashes, err := svc.getCollectionFileHashes()
	svc.mu.RUnlock()
	if err != nil {
		t.Fatalf("getCollectionFileHashes: %v", err)
	}
	if got := embedCalls.Load(); got != 0 {
		t.Errorf("expected 0 embedding calls after sidecar population, got %d", got)
	}
	if hashes[filepath.Join(dir, "a.go")] != "hashA" || hashes[filepath.Join(dir, "b.go")] != "hashB" {
		t.Errorf("unexpected hashes: %v", hashes)
	}
}

// TestSwitchBranch_FileHashMigrationIsAsync verifies that SwitchBranch does NOT
// perform the sidecar backfill (which embeds the query vector) synchronously
// when a non-empty collection exists but no sidecar does (the upgrade / new-
// machine scenario). Instead the backfill is deferred to a background goroutine
// and settles before ValidateCollection runs via WaitFileHashMigration.
func TestSwitchBranch_FileHashMigrationIsAsync(t *testing.T) {
	dir := t.TempDir()

	var (
		embedCalls atomic.Int32
		gateOpen   atomic.Bool
		block      = make(chan struct{})
		release    sync.Once
	)
	embed := func(_ context.Context, _ string) ([]float32, error) {
		embedCalls.Add(1)
		if !gateOpen.Load() {
			<-block // hold the migration's query embedding until released
		}
		return []float32{0.1, 0.2}, nil
	}
	unblock := func() {
		gateOpen.Store(true)
		release.Do(func() { close(block) })
	}

	// Build a non-empty, persisted collection via service A (gate open so A can
	// embed freely).
	gateOpen.Store(true)
	a, err := NewService(ServiceConfig{EmbeddingFunc: embed})
	if err != nil {
		t.Fatalf("NewService A: %v", err)
	}
	if err := a.SetProject("proj", dir); err != nil {
		t.Fatalf("SetProject A: %v", err)
	}
	if err := a.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch A: %v", err)
	}
	docs := []chromem.Document{
		{ID: "d1", Content: "alpha", Metadata: map[string]string{"file_path": filepath.Join(dir, "a.go"), "content_hash": "hA"}},
		{ID: "d2", Content: "beta", Metadata: map[string]string{"file_path": filepath.Join(dir, "b.go"), "content_hash": "hB"}},
	}
	a.AcquireWriteLock()
	if err := a.AddDocuments(context.Background(), docs, nil); err != nil {
		a.ReleaseWriteLock()
		t.Fatalf("AddDocuments A: %v", err)
	}
	a.ReleaseWriteLock()
	if err := a.Close(); err != nil {
		t.Fatalf("Close A: %v", err)
	}

	// Simulate the upgrade scenario: collection present, sidecar absent.
	sidecar := filepath.Join(dir, "file_hashes_"+collectionName("main")+".json")
	if err := os.Remove(sidecar); err != nil {
		t.Fatalf("remove sidecar: %v", err)
	}

	// Reopen with the gate CLOSED. If SwitchBranch ran the backfill
	// synchronously it would block here on the embedding gate (test would hang
	// until the go-test timeout); the fix defers it to a goroutine.
	gateOpen.Store(false)
	b, err := NewService(ServiceConfig{EmbeddingFunc: embed})
	if err != nil {
		t.Fatalf("NewService B: %v", err)
	}
	t.Cleanup(func() { unblock(); _ = b.Close() })

	if err := b.SetProject("proj", dir); err != nil {
		t.Fatalf("SetProject B: %v", err)
	}
	if err := b.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch B: %v", err)
	}
	// SwitchBranch returned without completing the backfill: it is pending in
	// the background (blocked on the gate), proving the migration is async.
	if !b.fileHashMigrationPending.Load() {
		t.Fatal("expected file-hash migration to be pending (deferred to background)")
	}

	// Release the gate and wait for the background backfill to settle.
	unblock()
	if err := b.WaitFileHashMigration(context.Background()); err != nil {
		t.Fatalf("WaitFileHashMigration: %v", err)
	}
	if b.fileHashMigrationPending.Load() {
		t.Fatal("expected file-hash migration to be settled after waiting")
	}

	// The migrated map must reflect the collection built by A.
	b.mu.RLock()
	hashes, err := b.getCollectionFileHashes()
	b.mu.RUnlock()
	if err != nil {
		t.Fatalf("getCollectionFileHashes: %v", err)
	}
	if hashes[filepath.Join(dir, "a.go")] != "hA" || hashes[filepath.Join(dir, "b.go")] != "hB" {
		t.Errorf("unexpected migrated hashes: %v", hashes)
	}
}
