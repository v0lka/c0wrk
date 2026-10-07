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
// re-drain loop honours the single shared deadline instead of spinning when
// the registry is never observed empty: once the deadline has passed, the loop
// abandons the remaining workers with a WARN and returns. Without the bound, a
// straggler that keeps re-registering a persistence worker would spin the
// shutdown main thread forever.
func TestStopBackground_DrainLoopBoundedByDeadline(t *testing.T) {
	manager, _, _ := testManager(t)
	// A zero budget makes the deadline already expired when the drain loop
	// runs, so the drain runs once (with a non-positive remainder) and then the
	// new deadline branch fires deterministically.
	manager.stopTimeout = 0

	capture := &messageCapture{}
	manager.SetLogger(slog.New(capture))

	// A blackboard whose persistence worker never reports stopped (zero value:
	// nil persistDone, nil persistCh) keeps the registry non-empty, so the loop
	// cannot terminate by draining to zero — exactly the straggler shape.
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
