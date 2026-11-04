package session

import (
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// Tracks manager-owned background goroutines so Shutdown can join them;
// without it they outlive the teardown and touch the filesystem. Not a
// sync.WaitGroup (Add/Wait concurrency, leaky waiter) — counter + zero channel.
type backgroundTracker struct {
	mu     sync.Mutex
	n      int            // in-flight goroutines
	closed bool           // no further spawn is accepted
	live   map[string]int // spawn name -> in-flight count ("" spawns are not tracked by name)

	// zero is closed exactly once, under mu, when n drops to zero after
	// (or at) close.
	zero chan struct{}
}

func newBackgroundTracker() *backgroundTracker {
	return &backgroundTracker{zero: make(chan struct{}), live: make(map[string]int)}
}

// Runs fn on a new goroutine; false without running anything once closed.
// name feeds the join-timeout diagnostics (see aliveNames); empty allowed.
func (t *backgroundTracker) spawn(name string, fn func()) bool {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return false
	}
	t.n++
	if name != "" {
		t.live[name]++
	}
	t.mu.Unlock()

	go func() {
		defer t.finish(name)
		fn()
	}()
	return true
}

// Records one goroutine returned. zero closes only after close: a transient
// drain to zero while still open must not close it (spawn does not reopen —
// later closeAndWait calls would pretend post-drain spawns were joined).
func (t *backgroundTracker) finish(name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.n--
	if name != "" {
		if t.live[name] <= 1 {
			delete(t.live, name)
		} else {
			t.live[name]--
		}
	}
	if t.n == 0 && t.closed {
		t.closeZeroLocked()
	}
}

// Snapshots the in-flight names, sorted; read AFTER a failed closeAndWait —
// whatever it returns survived the whole join budget.
func (t *backgroundTracker) aliveNames() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	names := make([]string, 0, len(t.live))
	for name := range t.live {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Closes t.zero if still open. Callers must hold mu.
func (t *backgroundTracker) closeZeroLocked() {
	select {
	case <-t.zero:
	default:
		close(t.zero)
	}
}

// Stops accepting new goroutines and waits until all in-flight ones finish
// or the timeout elapses (true = all joined within budget). Idempotent: a
// second call returns as soon as no goroutine is in flight.
func (t *backgroundTracker) closeAndWait(timeout time.Duration) bool {
	t.mu.Lock()
	t.closed = true
	if t.n == 0 {
		t.closeZeroLocked()
	}
	t.mu.Unlock()

	select {
	case <-t.zero:
		return true
	case <-time.After(timeout):
		return false
	}
}

// Bounds the filtered dump attached to a join-timeout WARN; a stuck walk or
// HTTP call produces a few hundred bytes, the cap guards only a pathological
// pileup landing whole in the session log.
const stragglerDumpMaxBytes = 32 * 1024

// Renders the stacks of goroutines still carrying a github.com/v0lka/*
// frame — where a shutdown straggler must live (the dumper's own passes
// harmlessly). A join timeout means a broken cancellation path; these
// stacks name it.
func stragglerGoroutineDump() string {
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	var kept strings.Builder
	for _, block := range strings.Split(string(buf[:n]), "\n\n") {
		if !strings.Contains(block, "v0lka/") {
			continue
		}
		if kept.Len() > 0 {
			kept.WriteString("\n\n")
		}
		kept.WriteString(block)
		if kept.Len() >= stragglerDumpMaxBytes {
			return kept.String()[:stragglerDumpMaxBytes] + "\n…(truncated)"
		}
	}
	return kept.String()
}
