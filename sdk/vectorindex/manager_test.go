package vectorindex

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/sdk/embedding"
)

func TestManagerSwitchProject(t *testing.T) {
	persistDir := t.TempDir()

	svc, err := NewService(ServiceConfig{
		PersistPath:   persistDir,
		EmbeddingFunc: fakeEmbeddingFunc(),
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

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

	// Index project A.
	if err := mgr.SwitchProject("project-a", wsA, ProjectCallbacks{}); err != nil {
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
	if err := mgr.SwitchProject("project-b", wsB, ProjectCallbacks{}); err != nil {
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
	if err := mgr.SwitchProject("project-a", wsA, ProjectCallbacks{}); err != nil {
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
		PersistPath:   persistDir,
		EmbeddingFunc: fakeEmbeddingFunc(),
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

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

	// Index project A.
	if err := mgr.SwitchProject("project-a", wsA, ProjectCallbacks{}); err != nil {
		t.Fatalf("SwitchProject A: %v", err)
	}
	if err := svc.WaitReady(context.Background()); err != nil {
		t.Fatalf("WaitReady A: %v", err)
	}

	// Trigger a debounced incremental run for project A.
	mgr.NotifyFileChange(wsA)

	// Immediately switch to project B (this should cancel the debounce).
	if err := mgr.SwitchProject("project-b", wsB, ProjectCallbacks{}); err != nil {
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
	if err := mgr.SwitchProject("project-a", wsA, ProjectCallbacks{}); err != nil {
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
		PersistPath:   persistDir,
		EmbeddingFunc: fakeEmbeddingFunc(),
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

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

	if err := mgr.SwitchProject("project-x", ws, ProjectCallbacks{}); err != nil {
		t.Fatalf("SwitchProject: %v", err)
	}
	if err := svc.WaitReady(context.Background()); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}

	projectDir := filepath.Join(persistDir, "project-x")
	if _, err := os.Stat(projectDir); err != nil {
		t.Fatalf("project vector dir should exist: %v", err)
	}

	if err := mgr.DeleteProjectData("project-x"); err != nil {
		t.Fatalf("DeleteProjectData: %v", err)
	}

	if _, err := os.Stat(projectDir); !os.IsNotExist(err) {
		t.Fatal("project vector dir should have been removed")
	}
}
