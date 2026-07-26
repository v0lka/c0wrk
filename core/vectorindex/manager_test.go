package vectorindex

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/sp4rk/embedding"
)

func TestManagerSwitchProject(t *testing.T) {
	persistDir := t.TempDir()

	svc, err := NewService(ServiceConfig{
		EmbeddingFunc: fakeEmbeddingFunc(),
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() {
		if err := svc.Close(); err != nil {
			t.Logf("Service.Close in cleanup: %v", err)
		}
	})

	mgr := &Manager{
		service: svc,
		logger:  slog.New(slog.DiscardHandler),
		chunkFn: defaultChunkFn,
		hashFn:  embedding.ComputeFileHash,
	}

	// Create two workspaces with distinct files.
	wsA := t.TempDir()
	if err := os.WriteFile(filepath.Join(wsA, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}

	wsB := t.TempDir()
	if err := os.WriteFile(filepath.Join(wsB, "b.go"), []byte("package b\n"), 0o644); err != nil {
		t.Fatalf("write b.go: %v", err)
	}

	viPathA := filepath.Join(persistDir, "project-a")
	viPathB := filepath.Join(persistDir, "project-b")

	// Index project A.
	if err := mgr.SwitchProject("project-a", wsA, viPathA, ProjectCallbacks{}); err != nil {
		t.Fatalf("SwitchProject A: %v", err)
	}
	if err := svc.WaitReady(context.Background()); err != nil {
		t.Fatalf("WaitReady A: %v", err)
	}
	colA := svc.GetCollection()
	if colA == nil || colA.Count() == 0 {
		t.Fatal("expected project A collection to have documents")
	}

	// Switch to project B.
	if err := mgr.SwitchProject("project-b", wsB, viPathB, ProjectCallbacks{}); err != nil {
		t.Fatalf("SwitchProject B: %v", err)
	}
	if err := svc.WaitReady(context.Background()); err != nil {
		t.Fatalf("WaitReady B: %v", err)
	}
	colB := svc.GetCollection()
	if colB == nil || colB.Count() == 0 {
		t.Fatal("expected project B collection to have documents")
	}

	// Switch back to project A and verify its data is still intact.
	if err := mgr.SwitchProject("project-a", wsA, viPathA, ProjectCallbacks{}); err != nil {
		t.Fatalf("SwitchProject A again: %v", err)
	}
	if err := svc.WaitReady(context.Background()); err != nil {
		t.Fatalf("WaitReady A again: %v", err)
	}
	colA2 := svc.GetCollection()
	if colA2 == nil || colA2.Count() == 0 {
		t.Fatal("expected project A collection to survive after switching to B")
	}
}

// TestManagerSwitchProject_CancelsDebounce ensures that a pending
// debounced incremental run from a previous project is cancelled
// before the new project starts indexing.
func TestManagerSwitchProject_CancelsDebounce(t *testing.T) {
	persistDir := t.TempDir()

	svc, err := NewService(ServiceConfig{
		EmbeddingFunc: fakeEmbeddingFunc(),
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() {
		if err := svc.Close(); err != nil {
			t.Logf("Service.Close in cleanup: %v", err)
		}
	})

	mgr := &Manager{
		service: svc,
		logger:  slog.New(slog.DiscardHandler),
		chunkFn: defaultChunkFn,
		hashFn:  embedding.ComputeFileHash,
	}

	// Workspace A with one file.
	wsA := t.TempDir()
	if err := os.WriteFile(filepath.Join(wsA, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}

	// Workspace B with a different file.
	wsB := t.TempDir()
	if err := os.WriteFile(filepath.Join(wsB, "b.go"), []byte("package b\n"), 0o644); err != nil {
		t.Fatalf("write b.go: %v", err)
	}

	viPathA := filepath.Join(persistDir, "project-a")
	viPathB := filepath.Join(persistDir, "project-b")

	// Index project A.
	if err := mgr.SwitchProject("project-a", wsA, viPathA, ProjectCallbacks{}); err != nil {
		t.Fatalf("SwitchProject A: %v", err)
	}
	if err := svc.WaitReady(context.Background()); err != nil {
		t.Fatalf("WaitReady A: %v", err)
	}

	// Trigger a debounced incremental run for project A.
	mgr.NotifyFileChange()

	// Immediately switch to project B (this should cancel the debounce).
	if err := mgr.SwitchProject("project-b", wsB, viPathB, ProjectCallbacks{}); err != nil {
		t.Fatalf("SwitchProject B: %v", err)
	}
	if err := svc.WaitReady(context.Background()); err != nil {
		t.Fatalf("WaitReady B: %v", err)
	}

	// Give any stray debounce a chance to fire.
	time.Sleep(1500 * time.Millisecond)

	// Verify project B's collection only contains its own file.
	files, err := svc.GetCollectionFiles()
	if err != nil {
		t.Fatalf("GetCollectionFiles: %v", err)
	}
	for fp := range files {
		if filepath.Base(fp) != "b.go" {
			t.Fatalf("project B collection contains unexpected file: %s", fp)
		}
	}

	// Switch back to A and verify it was not corrupted.
	if err := mgr.SwitchProject("project-a", wsA, viPathA, ProjectCallbacks{}); err != nil {
		t.Fatalf("SwitchProject A again: %v", err)
	}
	if err := svc.WaitReady(context.Background()); err != nil {
		t.Fatalf("WaitReady A again: %v", err)
	}
	filesA, err := svc.GetCollectionFiles()
	if err != nil {
		t.Fatalf("GetCollectionFiles A: %v", err)
	}
	if len(filesA) == 0 {
		t.Fatal("project A collection should still have its file")
	}
}

func TestManagerDeleteProjectData(t *testing.T) {
	persistDir := t.TempDir()

	svc, err := NewService(ServiceConfig{
		EmbeddingFunc: fakeEmbeddingFunc(),
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() {
		if err := svc.Close(); err != nil {
			t.Logf("Service.Close in cleanup: %v", err)
		}
	})

	mgr := &Manager{
		service: svc,
		logger:  slog.New(slog.DiscardHandler),
		chunkFn: defaultChunkFn,
		hashFn:  embedding.ComputeFileHash,
	}

	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "x.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatalf("write x.go: %v", err)
	}

	projectDir := filepath.Join(persistDir, "project-x")

	if err := mgr.SwitchProject("project-x", ws, projectDir, ProjectCallbacks{}); err != nil {
		t.Fatalf("SwitchProject: %v", err)
	}
	if err := svc.WaitReady(context.Background()); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}

	if _, err := os.Stat(projectDir); err != nil {
		t.Fatalf("project vector dir should exist: %v", err)
	}

	if err := mgr.DeleteProjectData(projectDir); err != nil {
		t.Fatalf("DeleteProjectData: %v", err)
	}

	if _, err := os.Stat(projectDir); !os.IsNotExist(err) {
		t.Fatal("project vector dir should have been removed")
	}
}

// TestManagerSwitchProject_NoProjectDisabled verifies that switching to the
// No Project pseudo-project fully disables the vector index: no persistent
// directory is created, no indexing goroutine runs, the service collection is
// cleared (so stale CODE-project results cannot leak), and no indexer remains
// configured.
func TestManagerSwitchProject_NoProjectDisabled(t *testing.T) {
	persistDir := t.TempDir()

	svc, err := NewService(ServiceConfig{
		EmbeddingFunc: fakeEmbeddingFunc(),
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() {
		if err := svc.Close(); err != nil {
			t.Logf("Service.Close in cleanup: %v", err)
		}
	})

	mgr := &Manager{
		service: svc,
		logger:  slog.New(slog.DiscardHandler),
		chunkFn: defaultChunkFn,
		hashFn:  embedding.ComputeFileHash,
	}

	// Index a real CODE project first so the service holds a populated
	// collection; this lets us verify the No Project switch clears it.
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	codeDir := filepath.Join(persistDir, "project-code")
	if err := mgr.SwitchProject("project-code", ws, codeDir, ProjectCallbacks{}); err != nil {
		t.Fatalf("SwitchProject code: %v", err)
	}
	if err := svc.WaitReady(context.Background()); err != nil {
		t.Fatalf("WaitReady code: %v", err)
	}
	if col := svc.GetCollection(); col == nil || col.Count() == 0 {
		t.Fatal("expected CODE project collection to have documents")
	}

	// Switch to No Project: the guard must short-circuit without building.
	noProjectDir := filepath.Join(persistDir, core.NoProjectID)
	if err := mgr.SwitchProject(core.NoProjectID, ws, noProjectDir, ProjectCallbacks{}); err != nil {
		t.Fatalf("SwitchProject NoProject: %v", err)
	}

	// No persistent vector directory is created for No Project.
	if _, err := os.Stat(noProjectDir); !os.IsNotExist(err) {
		t.Fatalf("No Project vector dir should not be created, got err=%v", err)
	}

	// The service collection is cleared: no stale CODE results, no build.
	if col := svc.GetCollection(); col != nil {
		t.Fatalf("expected nil collection for No Project, got count=%d", col.Count())
	}

	// No indexer is configured for No Project.
	mgr.mu.RLock()
	idx := mgr.indexer
	mgr.mu.RUnlock()
	if idx != nil {
		t.Fatal("expected no indexer configured for No Project")
	}

	// The service is not marked ready: no indexing goroutine ran for No
	// Project (SetProject resets readiness to false).
	if svc.IsReady() {
		t.Fatal("expected service to be not-ready for No Project (no indexing ran)")
	}
}

// TestManagerSwitchProject_AsyncInitFailureIsSoft verifies that when the
// asynchronous project initialization fails (here: an uncreatable vector index
// directory), SwitchProject still returns nil — because init runs off the RPC
// path — and the failure is handled softly: readiness is flipped to true so
// WaitReady doesn't hang, and the collection stays nil so search returns a
// clean "no collection" error instead of blocking the whole project switch.
//
// This is the observable behavior change vs. the prior synchronous
// SwitchProject, which ran SetProject inline and returned its error directly.
// Under async init that error cannot propagate synchronously, so init failures
// become logged soft failures (vector indexing is an optional subsystem).
func TestManagerSwitchProject_AsyncInitFailureIsSoft(t *testing.T) {
	svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	mgr := &Manager{
		service: svc,
		logger:  slog.New(slog.DiscardHandler),
		chunkFn: defaultChunkFn,
		hashFn:  embedding.ComputeFileHash,
	}

	// An uncreatable vector index path: a subpath beneath a regular file, so
	// os.MkdirAll inside SetProject fails.
	blocker := filepath.Join(t.TempDir(), "not_a_dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	badVectorPath := filepath.Join(blocker, "vector_index")

	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}

	// SwitchProject must return nil: init (including SetProject) runs in a
	// background goroutine, so its failure cannot propagate synchronously.
	if err := mgr.SwitchProject("project-bad", ws, badVectorPath, ProjectCallbacks{}); err != nil {
		t.Fatalf("SwitchProject returned error (init should be async+soft): %v", err)
	}

	// The failed init flips readiness to true so search RPCs fail fast
	// rather than hanging on WaitReady.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := svc.WaitReady(ctx); err != nil {
		t.Fatalf("WaitReady after soft init failure: %v", err)
	}

	if col := svc.GetCollection(); col != nil {
		t.Fatalf("expected nil collection after soft init failure, got count=%d", col.Count())
	}
}

// TestManagerSwitchProject_InitFailureThenRecover verifies that a soft init
// failure does not wedge the manager: the failed init's goroutine exits
// cleanly (so the next SwitchProject's cancel+wait teardown returns at once),
// and a subsequent valid SwitchProject initializes normally with a populated
// collection.
func TestManagerSwitchProject_InitFailureThenRecover(t *testing.T) {
	persistDir := t.TempDir()

	svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	mgr := &Manager{
		service: svc,
		logger:  slog.New(slog.DiscardHandler),
		chunkFn: defaultChunkFn,
		hashFn:  embedding.ComputeFileHash,
	}

	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}

	// 1) A failing init (uncreatable path).
	blocker := filepath.Join(t.TempDir(), "not_a_dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	badVectorPath := filepath.Join(blocker, "vector_index")
	if err := mgr.SwitchProject("project-bad", ws, badVectorPath, ProjectCallbacks{}); err != nil {
		t.Fatalf("SwitchProject bad: %v", err)
	}
	ctxWait, cancelWait := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelWait()
	if err := svc.WaitReady(ctxWait); err != nil {
		t.Fatalf("WaitReady after soft failure: %v", err)
	}

	// 2) A valid SwitchProject recovers and indexes normally.
	goodVectorPath := filepath.Join(persistDir, "project-good")
	if err := mgr.SwitchProject("project-good", ws, goodVectorPath, ProjectCallbacks{}); err != nil {
		t.Fatalf("SwitchProject good: %v", err)
	}
	ctxReady, cancelReady := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelReady()
	if err := svc.WaitReady(ctxReady); err != nil {
		t.Fatalf("WaitReady good: %v", err)
	}
	col := svc.GetCollection()
	if col == nil || col.Count() == 0 {
		t.Fatal("expected recovered project collection to have documents")
	}
}
