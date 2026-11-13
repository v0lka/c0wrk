package vectorindex

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// stallWarns returns the recorder's stall-Warn records (the watchdog's single
// diagnostic line per stall window), in order.
func stallWarns(rec *walkLogRecorder) []walkLogRecord {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	var out []walkLogRecord
	for _, r := range rec.records {
		if r.level == slog.LevelWarn && strings.Contains(r.msg, "vector index pass stalled") {
			out = append(out, r)
		}
	}
	return out
}

// TestPassWatchdogFiresOncePerStall pins the exactly-once semantics under the
// virtual clock: a full threshold of silence fires ONE dump; a still-stalled
// pass never repeats it; progress resolves the window so the NEXT silent
// window fires again — with the stage and last file captured at fire time.
func TestPassWatchdogFiresOncePerStall(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := &walkLogRecorder{}
		wd := newPassWatchdog(slog.New(rec), nil)
		wd.start()
		wd.heartbeat(StageReadHashChunk, "alpha.go")

		time.Sleep(passStallTimeout + time.Minute)
		warns := stallWarns(rec)
		if len(warns) != 1 {
			t.Fatalf("stall warns after one silent window = %d, want exactly 1", len(warns))
		}
		w := warns[0]
		if w.attrs["stage"] != string(StageReadHashChunk) {
			t.Errorf("warn stage = %q, want %q", w.attrs["stage"], string(StageReadHashChunk))
		}
		if w.attrs["last_file"] != "alpha.go" {
			t.Errorf("warn last_file = %q, want %q", w.attrs["last_file"], "alpha.go")
		}
		if w.attrs["threshold"] != passStallTimeout.String() {
			t.Errorf("warn threshold = %q, want %q", w.attrs["threshold"], passStallTimeout.String())
		}
		if w.attrs["dump_truncated"] != "false" {
			t.Errorf("warn dump_truncated = %q, want false", w.attrs["dump_truncated"])
		}
		if !strings.Contains(w.attrs["stacks"], "goroutine ") {
			t.Error("warn stacks carries no goroutine dump")
		}

		// Still no progress: the wedged pass must not re-dump.
		time.Sleep(3 * passStallTimeout)
		if got := len(stallWarns(rec)); got != 1 {
			t.Fatalf("stall warns after prolonged stall = %d, want still 1", got)
		}

		// Progress resolves the window; the next silent window fires again.
		wd.heartbeat(StageEmbedding, "beta.go")
		time.Sleep(passStallTimeout + time.Minute)
		warns = stallWarns(rec)
		if len(warns) != 2 {
			t.Fatalf("stall warns after progress + second stall = %d, want 2", len(warns))
		}
		if warns[1].attrs["stage"] != string(StageEmbedding) {
			t.Errorf("second warn stage = %q, want %q", warns[1].attrs["stage"], string(StageEmbedding))
		}
		if warns[1].attrs["last_file"] != "beta.go" {
			t.Errorf("second warn last_file = %q, want %q", warns[1].attrs["last_file"], "beta.go")
		}
	})
}

// TestPassWatchdogSilentWhileProgressing pins the no-dump-on-progress half:
// heartbeats spaced under the threshold keep every window resolved, and only
// a final full silence produces the single dump.
func TestPassWatchdogSilentWhileProgressing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := &walkLogRecorder{}
		wd := newPassWatchdog(slog.New(rec), nil)
		wd.start()
		for i := range 8 {
			time.Sleep(passStallTimeout / 2)
			wd.heartbeat(StageReadHashChunk, fmt.Sprintf("f%d.go", i))
		}
		if got := len(stallWarns(rec)); got != 0 {
			t.Fatalf("stall warns while progressing = %d, want 0", got)
		}
		time.Sleep(passStallTimeout + time.Minute)
		if got := len(stallWarns(rec)); got != 1 {
			t.Fatalf("stall warns after the final silent window = %d, want 1", got)
		}
	})
}

// TestPassWatchdogStopIsIdempotentAndSilences pins the idempotent lifecycle:
// double stop is a no-op, post-stop heartbeats and re-start are ignored, and
// a released watchdog never fires again.
func TestPassWatchdogStopIsIdempotentAndSilences(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := &walkLogRecorder{}
		wd := newPassWatchdog(slog.New(rec), nil)
		wd.start()
		wd.stop()
		wd.stop()
		wd.heartbeat(StageEmbedding, "late.go")
		wd.start()
		time.Sleep(3 * passStallTimeout)
		if got := len(stallWarns(rec)); got != 0 {
			t.Fatalf("stall warns after stop = %d, want 0", got)
		}
	})
}

// TestPassWatchdogIgnoresStaleTimer pins the stale-generation guard without
// any real timing: callbacks captured by the injected timer factory are
// fired manually out of order, and only the CURRENT generation's callback may
// dump — a fire racing a heartbeat that already re-armed stays silent.
func TestPassWatchdogIgnoresStaleTimer(t *testing.T) {
	rec := &walkLogRecorder{}
	// A stopped real timer stands in for every armed deadline; the test
	// invokes the captured callbacks itself, in the order they were armed.
	idle := time.AfterFunc(time.Hour, func() {})
	t.Cleanup(func() { idle.Stop() })
	var mu sync.Mutex
	var cbs []func()
	wd := newPassWatchdog(slog.New(rec), func(_ time.Duration, f func()) *time.Timer {
		mu.Lock()
		cbs = append(cbs, f)
		mu.Unlock()
		return idle
	})
	wd.start()                               // gen 0
	wd.heartbeat(StageReadHashChunk, "a.go") // gen 1
	wd.heartbeat(StageEmbedding, "b.go")     // gen 2

	mu.Lock()
	stale := append([]func(){}, cbs[:2]...)
	current := cbs[2]
	mu.Unlock()

	stale[0]()
	stale[1]()
	if got := len(stallWarns(rec)); got != 0 {
		t.Fatalf("stale-timer warns = %d, want 0", got)
	}
	current()
	if got := len(stallWarns(rec)); got != 1 {
		t.Fatalf("current-generation warns = %d, want 1", got)
	}
	current() // one dump per stall window, even on a double fire
	if got := len(stallWarns(rec)); got != 1 {
		t.Fatalf("warns after double fire = %d, want still 1", got)
	}
	if w := stallWarns(rec)[0]; w.attrs["stage"] != string(StageEmbedding) || w.attrs["last_file"] != "b.go" {
		t.Errorf("warn = stage %q / last_file %q, want %q / %q",
			w.attrs["stage"], w.attrs["last_file"], string(StageEmbedding), "b.go")
	}
}

// TestPassWatchdogNilReceiver pins nil tolerance: Service-side leases carry
// no watchdog and every method must no-op rather than panic.
func TestPassWatchdogNilReceiver(t *testing.T) {
	var wd *passWatchdog
	wd.start()
	wd.heartbeat(StageEmbedding, "x")
	wd.suspend()
	wd.resume()
	wd.stop()
	wd.watchContext(context.Background())
}

// TestPassWatchdogSuspendExcludesQueuedWait pins the queue-wait exclusion
// under the virtual clock: time spent suspended — the pass-lease
// acquisition, an unbounded wait behind a legitimately long predecessor —
// never counts toward a stall, however long it lasts; resume arms a FRESH
// window that still fires when the pass then truly goes silent. Suspend is
// idempotent, and a second suspend after resume excludes again.
func TestPassWatchdogSuspendExcludesQueuedWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := &walkLogRecorder{}
		wd := newPassWatchdog(slog.New(rec), nil)
		wd.start()
		wd.heartbeat(StageWalkValidation, "")

		// The queued wait: far past the threshold, no stall while suspended.
		wd.suspend()
		wd.suspend() // idempotent
		time.Sleep(10 * passStallTimeout)
		if got := len(stallWarns(rec)); got != 0 {
			t.Fatalf("stall warns while suspended (queued behind the lease) = %d, want 0 — a healthy queued pass is not stalled", got)
		}

		// Resume arms a fresh window; true silence past the threshold fires.
		wd.resume()
		time.Sleep(passStallTimeout + time.Minute)
		if got := len(stallWarns(rec)); got != 1 {
			t.Fatalf("stall warns after resume + true silence = %d, want exactly 1", got)
		}

		// A second suspend/resume cycle keeps the same discipline.
		wd.heartbeat(StageReadHashChunk, "late.go")
		wd.suspend()
		time.Sleep(5 * passStallTimeout)
		wd.resume()
		if got := len(stallWarns(rec)); got != 1 {
			t.Fatalf("stall warns across a second suspended wait = %d, want still 1", got)
		}
		wd.stop()
	})
}

// TestPassWatchdogSuspendInvalidatesArmedTimers pins the generation bump
// without any real timing: every callback armed before the suspend —
// including the generation that was current at suspend time — lands on a
// stale generation and stays silent, so a timer whose fire raced the
// suspend (timer.Stop cannot recall an entered callback) cannot dump the
// false stall the suspension exists to prevent. Only the window armed by
// resume may fire.
func TestPassWatchdogSuspendInvalidatesArmedTimers(t *testing.T) {
	rec := &walkLogRecorder{}
	clock, callbacks := manualWatchdogClock()
	wd := newPassWatchdog(slog.New(rec), clock)
	wd.start()                             // gen 0
	wd.heartbeat(StageWalkValidation, "")  // gen 1
	wd.heartbeat(StageCacheLookup, "a.go") // gen 2

	wd.suspend() // gen 3, timer dropped
	for _, stale := range callbacks() {
		stale() // gens 0..2 — all stale against gen 3
	}
	if got := len(stallWarns(rec)); got != 0 {
		t.Fatalf("stall warns from pre-suspend timers = %d, want 0", got)
	}

	wd.resume() // gen 3 window re-armed
	armed := callbacks()
	if len(armed) != 4 {
		t.Fatalf("armed callbacks after resume = %d, want 4 (3 pre-suspend + the resumed window)", len(armed))
	}
	armed[3]()
	if got := len(stallWarns(rec)); got != 1 {
		t.Fatalf("stall warns from the resumed window's timer = %d, want 1", got)
	}
	wd.stop()
}

// TestPassWatchdogWatchContextReleases pins the cancellation release: a
// cancelled pass context stops the watchdog immediately, without waiting for
// the pass goroutine to unwind out of a blocked read.
func TestPassWatchdogWatchContextReleases(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := &walkLogRecorder{}
		wd := newPassWatchdog(slog.New(rec), nil)
		wd.start()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		wd.watchContext(ctx)
		cancel()
		synctest.Wait() // the watcher observes the cancellation and stops
		time.Sleep(2 * passStallTimeout)
		if got := len(stallWarns(rec)); got != 0 {
			t.Fatalf("stall warns after ctx cancellation = %d, want 0", got)
		}
	})
}

// manualWatchdogClock is the injected clock for REAL-pipeline watchdog tests:
// it captures every armed callback in order and hands the watchdog a stopped
// stand-in timer, so the test fires the stall evidence on demand while the
// pass runs in real time (its bleve writer loops cannot live inside a
// testing/synctest bubble).
func manualWatchdogClock() (clock func(time.Duration, func()) *time.Timer, callbacks func() []func()) {
	idle := time.AfterFunc(time.Hour, func() {})
	idle.Stop()
	var mu sync.Mutex
	var cbs []func()
	return func(_ time.Duration, f func()) *time.Timer {
			mu.Lock()
			cbs = append(cbs, f)
			mu.Unlock()
			return idle
		}, func() []func() {
			mu.Lock()
			defer mu.Unlock()
			return append([]func(){}, cbs...)
		}
}

// parkedPassFixture builds a full-pass stall fixture: an in-memory service, a
// workspace, and an indexer whose two prep workers park inside their chunk
// calls on a seam. enterParked waits for BOTH workers to enter — once they
// have, every pass heartbeat has landed and the last-armed callback is the
// current generation.
func parkedPassFixture(t *testing.T, rec *walkLogRecorder) (idx *Indexer, ws string, seam *blockingChunkSeam, callbacks func() []func(), enterParked func(what string)) {
	t.Helper()
	svc := setupTestService(t)
	ws = createTestWorkspace(t)
	seam = newBlockingChunkSeam()
	t.Cleanup(seam.releaseAll)
	entries := make(chan struct{}, 16)
	seam.entries = func() { entries <- struct{}{} }
	idx = NewIndexer(IndexerConfig{Service: svc, HashFn: fakeHashFunc, ChunkFn: seam.chunkFn, Logger: slog.New(rec), PrepWorkers: 2})
	clock, callbacks := manualWatchdogClock()
	idx.stallAfterFunc = clock
	enterParked = func(what string) {
		t.Helper()
		for range 2 {
			select {
			case <-entries:
			case <-time.After(10 * time.Second):
				t.Fatalf("%s: prep workers never reached preparation", what)
			}
		}
	}
	return idx, ws, seam, callbacks, enterParked
}

// TestStallWatchdogFiresOnceDuringParkedPass drives a real IndexFull pass
// parked inside preparation: stale-generation callbacks stay silent, the
// current one dumps exactly once with the preparation stage and a workspace
// file as last_file, a still-stalled pass never repeats, and the pass itself
// completes normally — the watchdog only observes, and its release silences
// it for good.
func TestStallWatchdogFiresOnceDuringParkedPass(t *testing.T) {
	rec := &walkLogRecorder{}
	idx, ws, seam, callbacks, enterParked := parkedPassFixture(t, rec)

	passDone := make(chan error, 1)
	go func() { passDone <- idx.IndexFull(context.Background(), ws) }()
	enterParked("parked pass")

	cbs := callbacks()
	if len(cbs) == 0 {
		t.Fatal("watchdog armed no timer")
	}
	// Stale generations stay silent; the current one dumps exactly once.
	for _, stale := range cbs[:len(cbs)-1] {
		stale()
	}
	if got := len(stallWarns(rec)); got != 0 {
		t.Fatalf("stale-timer warns during parked pass = %d, want 0", got)
	}
	current := cbs[len(cbs)-1]
	current()
	warns := stallWarns(rec)
	if len(warns) != 1 {
		t.Fatalf("stall warns during parked pass = %d, want exactly 1", len(warns))
	}
	if warns[0].attrs["stage"] != string(StageReadHashChunk) {
		t.Errorf("warn stage = %q, want %q", warns[0].attrs["stage"], string(StageReadHashChunk))
	}
	switch base := filepath.Base(warns[0].attrs["last_file"]); base {
	case "main.go", "utils.go", "README.md", "config.yaml":
	default:
		t.Errorf("warn last_file = %q, want one of the workspace files", warns[0].attrs["last_file"])
	}
	if !strings.Contains(warns[0].attrs["stacks"], "goroutine ") {
		t.Error("warn stacks carries no goroutine dump")
	}
	current() // no repetition for a still-stalled pass
	if got := len(stallWarns(rec)); got != 1 {
		t.Fatalf("stall warns after a repeated fire = %d, want still 1", got)
	}

	// The pass completes normally — the watchdog never interrupts it — and
	// its release silences the watchdog for good.
	seam.releaseAll()
	select {
	case err := <-passDone:
		if err != nil {
			t.Fatalf("parked pass after release: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("parked pass never finished after release")
	}
	if got := len(stallWarns(rec)); got != 1 {
		t.Fatalf("stall warns after the pass ended = %d, want still 1", got)
	}
}

// TestStallWatchdogReleasesOnCancellation pins the AC's cancellation release
// against a real pass: the pass sits parked past the dump threshold (its
// manual timer deliberately never fires on its own), cancellation unwinds it,
// and firing even the CURRENT armed callback after cancellation — as after the
// pass's end — produces nothing: a cancelled pass can never dump.
func TestStallWatchdogReleasesOnCancellation(t *testing.T) {
	rec := &walkLogRecorder{}
	idx, ws, seam, callbacks, enterParked := parkedPassFixture(t, rec)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	passDone := make(chan error, 1)
	go func() { passDone <- idx.IndexFull(ctx, ws) }()
	enterParked("cancelled pass")

	cancel()
	seam.releaseAll()
	select {
	case err := <-passDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled pass error = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled pass never returned after release")
	}

	// The watchdog is released with the pass: firing every armed callback —
	// including the generation that was current while parked — stays silent.
	for _, cb := range callbacks() {
		cb()
	}
	if got := len(stallWarns(rec)); got != 0 {
		t.Fatalf("stall warns for a cancelled parked pass = %d, want 0", got)
	}
}

// TestWalkHeartbeaterSlowWalkNeverStalls pins the fix for the boundary-only
// walk stages: a walk whose entries keep arriving — however slowly — feeds
// the watchdog through the throttled heartbeater and never fires a stall
// verdict, spanning MULTIPLE full thresholds of walk time; only when the
// entries stop (one wedged stat on dead storage) does the window fire,
// exactly once, carrying the walk stage and the last observed entry.
func TestWalkHeartbeaterSlowWalkNeverStalls(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := &walkLogRecorder{}
		wd := newPassWatchdog(slog.New(rec), nil)
		wd.start()
		hb := newWalkHeartbeater(wd, StageWalkValidation)

		// A slow walk in miniature: one entry per walkHeartbeatEvery for
		// four full stall thresholds — the NFS/cloud-placeholder traversal
		// population, compressed onto the virtual clock.
		entries := 4 * int(passStallTimeout/walkHeartbeatEvery)
		for i := range entries {
			time.Sleep(walkHeartbeatEvery)
			hb.entry(fmt.Sprintf("ws/tree/entry-%d", i))
		}
		if got := len(stallWarns(rec)); got != 0 {
			t.Fatalf("stall warns during a progressing slow walk = %d, want 0 — slow is not stalled", got)
		}

		// The walk wedges on one stat: no further entries arrive, and the
		// full silent window fires exactly once with the walk stage and the
		// last observed entry as evidence.
		time.Sleep(passStallTimeout + time.Minute)
		warns := stallWarns(rec)
		if len(warns) != 1 {
			t.Fatalf("stall warns after the walk wedged = %d, want exactly 1", len(warns))
		}
		if warns[0].attrs["stage"] != string(StageWalkValidation) {
			t.Errorf("warn stage = %q, want %q", warns[0].attrs["stage"], string(StageWalkValidation))
		}
		wantLast := fmt.Sprintf("ws/tree/entry-%d", entries-1)
		if warns[0].attrs["last_file"] != wantLast {
			t.Errorf("warn last_file = %q, want %q", warns[0].attrs["last_file"], wantLast)
		}
		wd.stop()
	})
}

// TestWalkHeartbeaterThrottlesBursts pins the throttle: a burst of walk
// entries inside one walkHeartbeatEvery window produces AT MOST one
// heartbeat (one freshly armed timer beyond the start window), so walking a
// large tree does not re-arm the stall timer per directory entry.
func TestWalkHeartbeaterThrottlesBursts(t *testing.T) {
	rec := &walkLogRecorder{}
	clock, callbacks := manualWatchdogClock()
	wd := newPassWatchdog(slog.New(rec), clock)
	wd.start()
	hb := newWalkHeartbeater(wd, StageWalkValidation)
	for i := range 1000 {
		hb.entry(fmt.Sprintf("ws/burst-%d", i))
	}
	if got := len(callbacks()); got != 2 {
		t.Fatalf("armed timers after a 1000-entry burst = %d, want 2 (the start window plus one heartbeat)", got)
	}
	wd.stop()
}
