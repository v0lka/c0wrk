package vectorindex

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/v0lka/sp4rk/embedding"
)

// wedgedHarness reproduces the stuck-indexing-goroutine shape of
// TestShutdownReturnsWhenIndexingGoroutineIsStuck with the two seams this
// diagnosis needs: a recording logger and an injectable EmbedderWedged
// probe. The embedder blocks forever (non-ctx-aware, simulating an ONNX
// inference wedged inside a C call) so Shutdown's drain grace expires; the
// harness hands the test the captured logs and the probe's call record.
// Cleanup discipline mirrors the sibling test: release the stuck embedder
// BEFORE the service close, then join the indexing goroutine, so the on-disk
// handles close even on Windows (TempDir RemoveAll would fail with EBUSY).
type wedgedHarness struct {
	mgr        *Manager
	svc        *Service
	logs       *bytes.Buffer
	probeMu    sync.Mutex
	probeCalls int
}

func newWedgedHarness(t *testing.T, probe func() bool) *wedgedHarness {
	t.Helper()

	releaseEmbed := make(chan struct{})
	embedEntered := make(chan struct{})
	var enterOnce sync.Once

	var logs bytes.Buffer
	h := &wedgedHarness{
		logs: &logs,
	}

	// Non-ctx-aware embedder: blocks on releaseEmbed, simulating a hung ONNX
	// inference that ignores ctx.Done(). Signals entered exactly once so the
	// test can deterministically wait for "the goroutine is stuck" without
	// polling or sleeping.
	embed := func(_ context.Context, _ string) ([]float32, error) {
		enterOnce.Do(func() { close(embedEntered) })
		<-releaseEmbed
		return nil, errors.New("embedder released after shutdown skip")
	}

	svc, err := NewService(ServiceConfig{EmbeddingFunc: embed})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	h.svc = svc

	// probeRecord counts every consultation and forwards to the test's
	// probe; a nil probe wires NOTHING, which is the nil-probe shape
	// TestShutdownNilProbeKeepsGenericWarning pins.
	probeRecord := func() bool {
		h.probeMu.Lock()
		h.probeCalls++
		h.probeMu.Unlock()
		return probe()
	}
	var wiredProbe func() bool
	if probe != nil {
		wiredProbe = probeRecord
	}

	h.mgr = &Manager{
		service:        svc,
		logger:         slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		chunkFn:        defaultChunkFn,
		hashFn:         embedding.ComputeFileHash,
		shutdownGrace:  250 * time.Millisecond, // short for the test; prod default is 10 s
		embedderWedged: wiredProbe,
	}
	// The TempDirs are created BEFORE the cleanup registration (LIFO): the
	// service close must run BEFORE the TempDirs' RemoveAll, or Windows
	// fails the removal on the still-open lexical gate handle (POSIX hides
	// the ordering with unlink-under-open; the sibling stuck-shutdown test
	// documents the same discipline).
	persistDir := t.TempDir()
	ws := t.TempDir()
	t.Cleanup(func() {
		// Release first, then join (the goroutine holds the service write
		// lock until its deferred release), then close the service.
		close(releaseEmbed)
		h.mgr.indexingWG.Wait()
		_ = svc.Close()
	})

	if err := os.WriteFile(filepath.Join(ws, "a.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	viPath := filepath.Join(persistDir, "wedged-project")
	if err := h.mgr.SwitchProject("wedged-project", ws, viPath, ProjectCallbacks{}); err != nil {
		t.Fatalf("SwitchProject: %v", err)
	}

	// Deterministic barrier: the indexing goroutine has reached the embedder
	// (and holds the service write lock) before Shutdown runs.
	select {
	case <-embedEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("indexing goroutine never reached the embedder before Shutdown")
	}
	return h
}

// TestShutdownDiagnosesWedgedEmbedder pins the authoritative-diagnosis half
// of the drain contract: when the drain grace expires AND the ONNX-thread
// probe (sp4rk Embedder.Wedged, backed by the runner worker's per-job
// heartbeat) says the thread is wedged, Shutdown logs the explicit wedge
// diagnosis beside the generic grace warning. The probe owns its wedge
// bound — the healthy-inference envelope, never the drain grace — so this
// test injects the verdict directly and asserts only that it is consulted;
// the grace itself stays the backstop, and Shutdown must still return
// within it.
func TestShutdownDiagnosesWedgedEmbedder(t *testing.T) {
	h := newWedgedHarness(t, func() bool { return true })

	start := time.Now()
	h.mgr.Shutdown()
	elapsed := time.Since(start)

	// The grace is 250 ms; the generous cap bounds a wedged-drain regression,
	// it never asserts elapsed-time behavior.
	if elapsed > 5*time.Second {
		t.Fatalf("Shutdown took %v; the drain grace must stay the backstop", elapsed)
	}

	logs := h.logs.String()
	if !strings.Contains(logs, "did not exit within grace period") {
		t.Error("the generic grace-expiry warning vanished behind the diagnosis")
	}
	if got := strings.Count(logs, "the ONNX inference thread is wedged"); got != 1 {
		t.Errorf("the authoritative wedged diagnosis appeared %d times, want exactly 1, logs:\n%s", got, logs)
	}
	h.probeMu.Lock()
	defer h.probeMu.Unlock()
	if h.probeCalls == 0 {
		t.Error("the wedged probe was never consulted on drain expiry")
	}
}

// TestShutdownUnwedgedKeepsGenericWarning is the complement: when the
// authoritative probe says the inference thread is NOT wedged (the stuck
// goroutine is wedged elsewhere — gob-decode, a filesystem read), the drain
// expiry logs the generic warning ALONE. A diagnosis must never be
// fabricated when the authoritative source says no.
func TestShutdownUnwedgedKeepsGenericWarning(t *testing.T) {
	h := newWedgedHarness(t, func() bool { return false })

	start := time.Now()
	h.mgr.Shutdown()
	elapsed := time.Since(start)
	if elapsed > 5*time.Second {
		t.Fatalf("Shutdown took %v; the drain grace must stay the backstop", elapsed)
	}

	logs := h.logs.String()
	if !strings.Contains(logs, "did not exit within grace period") {
		t.Error("the generic grace-expiry warning is missing")
	}
	if strings.Contains(logs, "the ONNX inference thread is wedged") {
		t.Errorf("a wedged diagnosis was fabricated while the authoritative probe said no:\n%s", logs)
	}
}

// TestShutdownNilProbeKeepsGenericWarning pins the nil-probe shape: a
// manager built without EmbedderWedged (CPU-provider embedder, test
// literals) drains exactly as before — generic warning, no diagnosis, no
// nil dereference.
func TestShutdownNilProbeKeepsGenericWarning(t *testing.T) {
	h := newWedgedHarness(t, nil)

	start := time.Now()
	h.mgr.Shutdown()
	elapsed := time.Since(start)
	if elapsed > 5*time.Second {
		t.Fatalf("Shutdown took %v; the drain grace must stay the backstop", elapsed)
	}

	logs := h.logs.String()
	if !strings.Contains(logs, "did not exit within grace period") {
		t.Error("the generic grace-expiry warning is missing")
	}
	if strings.Contains(logs, "the ONNX inference thread is wedged") {
		t.Errorf("a nil probe must not produce a diagnosis:\n%s", logs)
	}
}
