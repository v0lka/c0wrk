package session

import (
	"runtime"
	"sort"
	"strings"
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
// non-retrying os.RemoveAll, and a straggler appends an entry while
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
	n      int            // in-flight goroutines
	closed bool           // no further spawn is accepted
	live   map[string]int // spawn name -> in-flight count ("" spawns are not tracked by name)

	// zero is closed once n drops to zero after (or at) close. It is closed
	// exactly once, under mu.
	zero chan struct{}
}

func newBackgroundTracker() *backgroundTracker {
	return &backgroundTracker{zero: make(chan struct{}), live: make(map[string]int)}
}

// spawn registers fn and runs it on a new goroutine. It reports false without
// running anything once the tracker has been closed, letting a late caller
// skip the work or fall back to a synchronous path.
//
// name attributes the goroutine for shutdown diagnostics: a join timeout logs
// the names still in flight (see aliveNames), turning an anonymous
// "something survived shutdown" into the spawn site that needs fixing. An
// empty name is allowed for trivial fire-and-forget wrappers.
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

// finish records that one tracked goroutine returned. It closes zero only
// once the tracker is closed: a transient drain to zero (every short-lived
// goroutine finished while the tracker is still open) must NOT close zero,
// because spawn does not reopen it — a closed zero would make every later
// closeAndWait return immediately, pretending goroutines that spawned
// afterwards have been joined.
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

// aliveNames snapshots the names of goroutines still in flight, sorted for
// stable log output. It is read AFTER a failed closeAndWait join: whatever it
// returns is exactly what survived the whole join budget.
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

// stragglerDumpMaxBytes bounds the filtered goroutine dump attached to a join
// timeout WARN. A stuck walk or HTTP call produces a few hundred bytes; the
// cap only guards against a pathological process-wide pileup landing whole in
// the session log.
const stragglerDumpMaxBytes = 32 * 1024

// stragglerGoroutineDump renders the stacks of goroutines that still carry a
// frame from this module family (github.com/v0lka/*), which is where a
// shutdown straggler must live: stdlib-only goroutines (GC scavenger, timer
// system) cannot be manager stragglers. The dumper's own goroutine passes the
// same filter (its caller chain is stopBackground → here); it is easy to tell
// apart and harmless to keep — its frames show exactly when and from where
// the dump was taken.
//
// The dump is the diagnostic of last resort: every tracked goroutine is
// cancellation-driven, so a join timeout means a cancellation path is broken
// somewhere, and these stacks name the exact function and line.
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
