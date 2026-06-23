package orchestration

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestStepDumpTracker_OpenAndClose(t *testing.T) {
	dir := t.TempDir()
	tracker := NewStepDumpTracker(dir)

	w1 := tracker.OpenStepDump("step-1")
	w2 := tracker.OpenStepDump("step-2")
	if w1 == nil {
		t.Fatal("expected non-nil writer for step-1")
	}
	if w2 == nil {
		t.Fatal("expected non-nil writer for step-2")
	}

	// Write to verify files are writable
	if _, err := io.WriteString(w1, "dump1\n"); err != nil {
		t.Fatalf("write step-1: %v", err)
	}
	if _, err := io.WriteString(w2, "dump2\n"); err != nil {
		t.Fatalf("write step-2: %v", err)
	}

	if err := tracker.CloseAll(); err != nil {
		t.Fatalf("CloseAll: %v", err)
	}

	// Verify files exist on disk
	data1, err := os.ReadFile(filepath.Join(dir, "step_step-1.jsonl"))
	if err != nil {
		t.Fatalf("read step-1 file: %v", err)
	}
	if !strings.Contains(string(data1), "dump1") {
		t.Errorf("step-1 file missing content: %q", string(data1))
	}
	data2, err := os.ReadFile(filepath.Join(dir, "step_step-2.jsonl"))
	if err != nil {
		t.Fatalf("read step-2 file: %v", err)
	}
	if !strings.Contains(string(data2), "dump2") {
		t.Errorf("step-2 file missing content: %q", string(data2))
	}
}

func TestStepDumpTracker_IdempotentOpen(t *testing.T) {
	dir := t.TempDir()
	tracker := NewStepDumpTracker(dir)

	w1 := tracker.OpenStepDump("step-1")
	w2 := tracker.OpenStepDump("step-1")
	if w1 != w2 {
		t.Error("expected same writer for repeated OpenStepDump with same stepID")
	}
}

func TestStepDumpTracker_CloseAllIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	tracker := NewStepDumpTracker(dir)

	_ = tracker.OpenStepDump("step-1")
	if err := tracker.CloseAll(); err != nil {
		t.Fatalf("first CloseAll: %v", err)
	}
	if err := tracker.CloseAll(); err != nil {
		t.Fatalf("second CloseAll: %v", err)
	}
}

func TestStepDumpTracker_OpenAfterClose(t *testing.T) {
	dir := t.TempDir()
	tracker := NewStepDumpTracker(dir)

	_ = tracker.OpenStepDump("step-1")
	_ = tracker.CloseAll()

	w := tracker.OpenStepDump("step-2")
	if w != nil {
		t.Error("expected nil after CloseAll")
	}
}

func TestStepDumpTracker_ConcurrentOpen(t *testing.T) {
	dir := t.TempDir()
	tracker := NewStepDumpTracker(dir)

	const goroutines = 10
	var wg sync.WaitGroup
	writers := make([]io.Writer, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			writers[idx] = tracker.OpenStepDump("step-shared")
		}(i)
	}
	wg.Wait()

	first := writers[0]
	for i := 1; i < goroutines; i++ {
		if writers[i] != first {
			t.Errorf("goroutine %d got different writer", i)
		}
	}
}

func TestStepDumpTracker_NonexistentDir(t *testing.T) {
	// Use a path that cannot be created (nested under a file)
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "not-a-dir")
	if err := os.WriteFile(filePath, []byte("block"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	badDir := filepath.Join(filePath, "sub", "deep")

	tracker := NewStepDumpTracker(badDir)
	w := tracker.OpenStepDump("step-1")
	if w != nil {
		t.Error("expected nil when directory creation fails")
	}
}
