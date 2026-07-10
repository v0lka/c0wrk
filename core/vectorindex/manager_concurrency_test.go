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
