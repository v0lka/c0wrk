package vectorindex

// Regression guard for a Windows-only CI flake (2026-10-17): a failed
// indexing pass used to leave the lexical root.bolt handle open after
// Service.Close, and t.TempDir's RemoveAll then failed with "being used by
// another process" — invisible on POSIX, where unlinking an open file
// succeeds. The scenario replays the failed-pass shape (no collection at
// SetProject, both index passes failing, then a live-branch positive
// control) and asserts Close releases every handle: directly via the FD
// table where it is walkable (unix), and via a full RemoveAll of the
// persistence dir, which a Windows open handle makes fail.

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestServiceCloseReleasesFailedPassHandles(t *testing.T) {
	persistDir := t.TempDir()
	svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	if err := svc.SetProject("p", persistDir); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	indexer := NewIndexer(IndexerConfig{
		Service: svc,
		ChunkFn: fakeChunkFunc,
		HashFn:  fakeHashFunc,
	})
	wsDir := createTestWorkspace(t)
	ctx := context.Background()
	// The failed-pass shape: no SwitchBranch before the first passes, so
	// both fail at collection validation (no collection exists).
	if err := indexer.IndexIncremental(ctx, wsDir); err == nil {
		t.Fatal("incremental without a collection must fail")
	}
	if err := indexer.IndexFull(ctx, wsDir); err == nil {
		t.Fatal("full without a collection must fail")
	}
	// Positive control: the live branch opens the lexical store, and one
	// more failed pass leaves it resident — Close must still release it.
	if err := svc.SwitchBranch(ctx, "main"); err != nil {
		t.Fatalf("SwitchBranch main: %v", err)
	}
	if err := indexer.IndexIncremental(ctx, filepath.Join(wsDir, "does-not-exist")); err == nil {
		t.Fatal("incremental over a missing workspace must fail")
	}
	if err := svc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if runtime.GOOS != "windows" {
		if leaks := openHandlesUnder(t, persistDir); len(leaks) > 0 {
			t.Errorf("handles still open under the persistence dir after Close: %v", leaks)
		}
	}
	// On Windows an open handle makes this RemoveAll fail; on unix the FD
	// walk above is the authoritative check. The TempDir is about to be
	// removed by the framework anyway, so removing it here is free.
	if err := os.RemoveAll(persistDir); err != nil {
		t.Errorf("removing the persistence dir after Close = %v, want nil (a handle survived Close)", err)
	}
}

// openHandlesUnder walks the process FD table and returns links pointing
// inside dir. Skips (rather than fails) where /dev/fd is unavailable.
func openHandlesUnder(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Skipf("cannot walk /dev/fd: %v", err)
	}
	var hits []string
	for _, e := range entries {
		link, err := os.Readlink("/dev/fd/" + e.Name())
		if err != nil {
			continue
		}
		if rel, err := filepath.Rel(dir, link); err == nil && rel != ".." && !filepath.IsAbs(rel) {
			hits = append(hits, link)
		}
	}
	return hits
}
