package session

import (
	"sync"
	"time"
)

// backgroundTracker tracks manager-owned background goroutines so that
// Shutdown can join them.
//
// The Manager spawns a few long-lived goroutines (the asynchronous ignore
// resolver build, deferred session-temp-dir removals, best-effort title
// generation). None of them is owned by a session's done channel, so without
// this tracker they can outlive Shutdown and touch the filesystem — reopening
// a file, recreating a directory, or emitting a log line — after the process
// has torn every session down. That is exactly the temporal race that makes a
// test's TempDir cleanup flake with ENOTEMPTY ("directory not empty") on a
// CPU-starved runner: testing removes the per-test temp parent with a single
// non-retrying os.RemoveAll, and a straggler goroutine appends an entry while
// that walk is in progress.
//
// The tracker is deliberately not a sync.WaitGroup: WaitGroup forbids Add
// concurrent with Wait, which would force a lock around every spawn anyway,
// and its Wait would need an extra goroutine that leaks when the budget
// expires. A counter plus a single "reached zero" channel gives the same
// guarantee with no goroutine to leak.
//
// Zero value is not usable; construct with newBackgroundTracker.
type backgroundTracker struct {
	mu     sync.Mutex
	n      int  // in-flight goroutines
	closed bool // no further spawn is accepted

	// zero is closed once n drops to zero after (or at) close. It is closed
	// exactly once, under mu.
	zero chan struct{}
}

func newBackgroundTracker() *backgroundTracker {
	return &backgroundTracker{zero: make(chan struct{})}
}

// spawn registers fn and runs it on a new goroutine. It reports false without
// running anything once the tracker has been closed, letting a late caller
// skip the work or fall back to a synchronous path.
func (t *backgroundTracker) spawn(fn func()) bool {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return false
	}
	t.n++
	t.mu.Unlock()

	go func() {
		defer t.finish()
		fn()
	}()
	return true
}

// finish records that one tracked goroutine returned. It closes zero only
// once the tracker is closed: a transient drain to zero (every short-lived
// goroutine finished while the tracker is still open) must NOT close zero,
// because spawn does not reopen it — a closed zero would make every later
// closeAndWait return immediately, pretending goroutines that spawned
// afterwards have been joined.
func (t *backgroundTracker) finish() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.n--
	if t.n == 0 && t.closed {
		t.closeZeroLocked()
	}
}

// closeZeroLocked closes t.zero if it is still open. Callers must hold mu.
func (t *backgroundTracker) closeZeroLocked() {
	select {
	case <-t.zero:
	default:
		close(t.zero)
	}
}

// closeAndWait stops accepting new goroutines and waits until every in-flight
// one has finished, or until timeout elapses. It returns true when all of them
// finished within the budget.
//
// It is idempotent: a second call observes the already-closed tracker and
// returns immediately (as soon as no goroutine is in flight).
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
