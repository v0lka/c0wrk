package session

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// messageCapture is a minimal slog.Handler that records every message and
// drops the output, so a test can assert which records a flow produced without
// pinning the volatile attributes (counts, elapsed ms).
type messageCapture struct {
	mu   sync.Mutex
	msgs []string
}

func (c *messageCapture) Enabled(context.Context, slog.Level) bool { return true }

func (c *messageCapture) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	c.msgs = append(c.msgs, r.Message)
	c.mu.Unlock()
	return nil
}

func (c *messageCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *messageCapture) WithGroup(string) slog.Handler      { return c }

func (c *messageCapture) contains(substr string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, m := range c.msgs {
		if strings.Contains(m, substr) {
			return true
		}
	}
	return false
}

func (c *messageCapture) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.msgs...)
}

// TestStopBackground_DrainLoopBoundedByDeadline verifies that the blackboard
// drain loop is BOUNDED: it returns instead of spinning when the shared
// deadline has already elapsed. The single registered blackboard is drained on
// the first (and only) pass, and the pre-expired (negative) budget makes the
// deadline branch fire on that pass, so stopBackground returns with the WARN.
//
// What this test does NOT exercise: the multi-pass shape where a straggler
// re-registers a blackboard after the first snapshot (the re-drain the branch
// also bounds). Nothing re-registers here, so the loop would equally terminate
// on the empty-registry check; the deadline branch is what actually ends this
// run because the shared deadline is already spent. The straggler path is
// bounded by the same `time.Now().After(deadline)` check this test exercises.
func TestStopBackground_DrainLoopBoundedByDeadline(t *testing.T) {
	manager, _, _ := testManager(t)
	// A negative budget puts the shared deadline strictly in the past, so the
	// single pass drains immediately (a non-positive remainder makes
	// pb.Shutdown return at once) and the deadline branch then fires
	// deterministically. A plain zero budget is NOT enough for that: the
	// branch compares with a strict After, and on a coarse monotonic clock
	// (Windows ticks at ~0.5 ms) the whole drain can land inside the
	// deadline's own tick — the branch then never fires and the loop exits
	// through the empty-registry check without the WARN this test asserts
	// (seen on a CI windows runner).
	manager.stopTimeout = -time.Millisecond

	capture := &messageCapture{}
	manager.SetLogger(slog.New(capture))

	// A blackboard whose persistence worker never reports stopped (zero value:
	// nil persistDone, nil persistCh) is drained on the single pass. Nothing
	// re-registers it, so it is the pre-expired shared deadline — not an empty
	// registry — that ends the loop here.
	manager.trackBlackboard(&PersistentBlackboard{})

	done := make(chan struct{})
	go func() {
		manager.stopBackground()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stopBackground did not return despite the elapsed deadline (drain loop not bounded)")
	}

	if !capture.contains("blackboard drain deadline exceeded") {
		t.Errorf("expected the drain-deadline WARN; captured messages: %v", capture.snapshot())
	}
}
