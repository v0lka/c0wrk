package vectorindex

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/v0lka/sp4rk/embedding"
)

// TestManagerNotifyFileChangeSerializesOverlappingPasses verifies that two
// near-simultaneous NotifyFileChange calls never embed concurrently (observed
// peak embed concurrency must be ≤ 1).
//
// Note on scope: embedding serialization is primarily guaranteed by the
// service write lock (IndexIncremental → AddDocuments holds s.mu), so this test
// passes even without the m.indexing guard. The guard's distinct job —
// coalescing the trailing pass so a change arriving mid-pass yields one final
// run rather than a redundant serial no-op pass — is an efficiency property
// that this peak-concurrency assertion does not measure.
func TestManagerNotifyFileChangeSerializesOverlappingPasses(t *testing.T) {
	persistDir := t.TempDir()

	var (
		callCount   atomic.Int32 // total embed invocations
		inFlight    atomic.Int32 // currently-executing embed invocations
		gateEnabled atomic.Bool  // when true, embed calls block on `block`
		maxMu       sync.Mutex
		maxConc     int32 // observed peak concurrency of embed calls
	)
	block := make(chan struct{})

	trackMax := func(cur int32) {
		maxMu.Lock()
		if cur > maxConc {
			maxConc = cur
		}
		maxMu.Unlock()
	}

	embed := func(_ context.Context, _ string) ([]float32, error) {
		callCount.Add(1)
		cur := inFlight.Add(1)
		trackMax(cur)
		// Only block when the test has enabled the gate (after IndexFull).
		if gateEnabled.Load() {
			<-block // hold until the test releases
		}
		inFlight.Add(-1)
		return []float32{0.01, 0.02}, nil
	}

	svc, err := NewService(ServiceConfig{EmbeddingFunc: embed})
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
	if err := os.WriteFile(filepath.Join(ws, "main.go"), []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	viPath := filepath.Join(persistDir, "project-x")

	if err := mgr.SwitchProject("project-x", ws, viPath, ProjectCallbacks{}); err != nil {
		t.Fatalf("SwitchProject: %v", err)
	}
	if err := svc.WaitReady(context.Background()); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	// Freeze the gate threshold: subsequent (incremental) embed calls block.
	threshold := callCount.Load()
	gateEnabled.Store(true)

	// Add a new file so the incremental pass performs embedding work.
	if err := os.WriteFile(filepath.Join(ws, "added.go"), []byte("package main\nfunc added() {}\n"), 0o644); err != nil {
		t.Fatalf("write added.go: %v", err)
	}

	// Trigger pass A (1s debounce → IndexIncremental → embeds → blocks).
	mgr.NotifyFileChange()
	// Wait for pass A to reach the embedder and block (callCount grows).
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if callCount.Load() > threshold {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if callCount.Load() <= threshold {
		t.Fatal("pass A never reached the embedder")
	}

	// While pass A is blocked/in-flight, reset the peak counter and trigger B.
	maxMu.Lock()
	maxConc = 0
	maxMu.Unlock()
	mgr.NotifyFileChange()
	// Let pass B's debounce (1s) elapse. With the guard, B re-arms instead of
	// running concurrently; without the guard, B would embed now (peak=2).
	time.Sleep(1500 * time.Millisecond)

	// Release pass A; the trailing pass B then runs sequentially.
	close(block)

	// Wait for everything to settle (indexing flag cleared).
	deadline = time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if !mgr.indexing.Load() {
			// Give the re-armed trailing run a moment, then re-check stability.
			time.Sleep(1500 * time.Millisecond)
			if !mgr.indexing.Load() {
				break
			}
		}
		time.Sleep(25 * time.Millisecond)
	}

	maxMu.Lock()
	peak := maxConc
	maxMu.Unlock()
	if peak > 1 {
		t.Errorf("expected at most 1 concurrent embed call (serialized passes), got peak %d", peak)
	}
}

// TestManagerSwitchProject_RapidDoubleSwitchCancelsInFlight exercises the
// single-flight contract (initCancel + initWG + indexCancel) when a second
// SwitchProject fires while the first project's background indexing is still
// in flight and blocked on the embedder — which holds the service write lock
// mid-AddDocuments.
//
// The second switch must: cancel the first's index context (unblocking the
// embedder via chromem's ctx propagation and so releasing the write lock),
// wait out the first's init goroutine, then initialize cleanly so the second
// project's collection wins — no deadlock, no leaked indexing goroutine, and
// readiness restored.
//
// Determinism: the embedder is ctx-aware, so SwitchProject B's indexCancel
// unblocks A's blocked embedder (it returns ctx.Err()) rather than relying on
// wall-clock timing. We block on embedCalls > 0 to guarantee A is genuinely
// in flight before issuing the double-switch.
func TestManagerSwitchProject_RapidDoubleSwitchCancelsInFlight(t *testing.T) {
	persistDir := t.TempDir()

	var embedCalls atomic.Int32
	block := make(chan struct{})
	var gateOnce sync.Once
	release := func() { gateOnce.Do(func() { close(block) }) }

	// ctx-aware embedder: blocks on `block` until released OR the index
	// context is cancelled. The ctx path is what lets SwitchProject's
	// indexCancel deterministically unblock an in-flight pass.
	embed := func(ctx context.Context, _ string) ([]float32, error) {
		embedCalls.Add(1)
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return []float32{0.01, 0.02}, nil
	}

	svc, err := NewService(ServiceConfig{EmbeddingFunc: embed})
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
	t.Cleanup(func() {
		release() // unblock any goroutine still waiting on the gate
		mgr.Shutdown()
	})

	// Two workspaces with distinct files so the winning project is unambiguous.
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

	// 1) SwitchProject A: its indexing goroutine reaches the embedder and
	//    blocks (gate closed), holding the service write lock mid-AddDocuments.
	if err := mgr.SwitchProject("project-a", wsA, viPathA, ProjectCallbacks{}); err != nil {
		t.Fatalf("SwitchProject A: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if embedCalls.Load() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if embedCalls.Load() == 0 {
		t.Fatal("project A indexing never reached the embedder before the double-switch")
	}

	// 2) Rapid double-switch to B while A's indexing is in flight. This must
	//    cancel A's index context (unblocking the embedder + releasing the
	//    write lock), wait out A's init goroutine, and not deadlock.
	if err := mgr.SwitchProject("project-b", wsB, viPathB, ProjectCallbacks{}); err != nil {
		t.Fatalf("SwitchProject B: %v", err)
	}

	// 3) Open the gate so B's indexing can proceed (A's embedder already
	//    unblocked via ctx cancellation in step 2).
	release()

	// 4) B must win: readiness restored and B's collection populated with
	//    b.go (not A's a.go). WaitReady succeeding also proves no deadlock —
	//    it requires B's SetProject to have acquired the write lock A held.
	readyCtx, readyCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer readyCancel()
	if err := svc.WaitReady(readyCtx); err != nil {
		t.Fatalf("WaitReady B after rapid double-switch: %v", err)
	}
	col := svc.GetCollection()
	if col == nil || col.Count() == 0 {
		t.Fatal("expected project B collection to have documents after double-switch")
	}
	files, err := svc.GetCollectionFiles()
	if err != nil {
		t.Fatalf("GetCollectionFiles: %v", err)
	}
	for fp := range files {
		if filepath.Base(fp) != "b.go" {
			t.Errorf("project B collection contains unexpected file from A: %s", fp)
		}
	}

	// 5) No leaked indexing pass: the indexing flag settles to false (A's
	//    goroutine exited via ctx cancellation; B's completed normally).
	settleDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(settleDeadline) {
		if !mgr.indexing.Load() {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if mgr.indexing.Load() {
		t.Error("expected indexing flag to settle to false after double-switch")
	}
}
