package vectorindex

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// passStallTimeout is the fixed pass-level stall threshold: an indexing pass
// that emits no progress event — no processed file, no stage transition, no
// embed batch — for this long is treated as wedged. It is a diagnostic
// threshold, not a deadline: the watchdog never interrupts the pass, it only
// records evidence once.
const passStallTimeout = 5 * time.Minute

// stallWatchdogSeq hands every watchdog an opaque identity (StallSnapshot.
// Watch): a consumer that receives transitions from more than one watchdog —
// two overlapping passes on one indexer — can then tell "this watchdog
// resolved the stall it opened" from "a different watchdog's resolve landed
// here" and ignore the latter.
var stallWatchdogSeq atomic.Uint64

// StallSnapshot is one watchdog's point-in-time liveness verdict: whether its
// pass is stalled (a full passStallTimeout window without progress), where
// progress stopped, and for how long. It is evidence for status surfaces and
// tests, never a control signal — the watchdog itself never interrupts a pass.
type StallSnapshot struct {
	// Stalled reports the pass made no progress for at least
	// passStallTimeout. A watchdog reports Stalled=false when its stall
	// window closed (the first heartbeat after a fire) or when it has been
	// released — a finished or cancelled pass is never stalled.
	Stalled bool
	// Stage is the bounded pipeline stage (TelemetryStage) current when
	// progress stopped; empty before the first heartbeat.
	Stage string
	// LastFile is the most recent file path the pass prepared; empty when
	// the stall happened before any file was seen.
	LastFile string
	// SilentFor is how long the pass has been without progress. On a
	// Stalled=false transition it is zero by convention (the stall window
	// just closed).
	SilentFor time.Duration
	// Watch identifies the reporting watchdog. Constant per watchdog;
	// consumers use it to ignore a resolve that belongs to another
	// watchdog's stall window.
	Watch uint64
}

// passWatchdog detects progress stagnation inside one indexing pass. Every
// pass progress point (file prepared, stage transition, embed or commit
// batch) calls heartbeat, which re-arms the timer; a full passStallTimeout of
// silence logs ONE Warn carrying the current bounded stage, the last file
// path (an explicit user decision for this diagnostic log — Telemetry stays
// content-free) and a capped all-goroutine stack dump (goroutineDump, the
// shutdown evidence helper). The Warn is emitted once per stall window:
// progress resolves the window and a fresh one may fire again, but a pass
// that stays wedged never repeats the dump.
//
// Lifecycle is idempotent and pass-scoped: start arms, stop releases, and
// watchContext additionally releases the moment the pass context is
// cancelled, so a cancelled pass unwinding out of an uninterruptible read
// cannot trip the watchdog again. Every method tolerates a nil receiver
// (Service-side pass leases carry no watchdog) and a stopped watchdog.
type passWatchdog struct {
	logger *slog.Logger
	// afterFunc is the instance-local timer factory for tests (the
	// Manager.afterFunc seam precedent); nil uses time.AfterFunc, which
	// testing/synctest already runs on the bubble's virtual clock.
	afterFunc func(time.Duration, func()) *time.Timer
	// onStallChange, when set (before start), receives every stall-window
	// transition: opened by fire, closed by the first heartbeat that
	// observes the fired window. Both invocations happen under mu, so one
	// consumer sees this watchdog's transitions in order. The status
	// consumers it feeds (the Manager's recordStall) only take their own
	// locks — the wd.mu → consumer-lock order is one-way, never the reverse.
	onStallChange func(StallSnapshot)

	mu       sync.Mutex
	stage    string
	lastFile string
	// lastProgress is the wall clock of the most recent progress point
	// (armed by start, advanced by every heartbeat); SilentFor reads off it.
	lastProgress time.Time
	// watch is the opaque identity carried in every StallSnapshot.
	watch uint64
	// gen identifies the currently armed timer: each heartbeat increments it,
	// and a callback whose gen has moved on is stale (fired racing a heartbeat
	// that already rearmed) and must stay silent.
	gen     uint64
	fired   bool
	stopped bool
	timer   *time.Timer
	// suspended marks a suspended stall window (see suspend/resume): the
	// timer is dropped and the generation is bumped so a fire already in
	// flight lands on a stale generation.
	suspended bool
	// done closes once via stopOnce; watchContext selects on it so its
	// goroutine exits when the watchdog is released by either side.
	done     chan struct{}
	stopOnce sync.Once
}

// newPassWatchdog builds a watchdog that logs to logger. Construction does
// not arm the timer — start does. An onStallChange consumer may still be
// attached via setOnStallChange before start.
func newPassWatchdog(logger *slog.Logger, afterFunc func(time.Duration, func()) *time.Timer) *passWatchdog {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &passWatchdog{
		logger:    logger,
		afterFunc: afterFunc,
		watch:     stallWatchdogSeq.Add(1),
		done:      make(chan struct{}),
	}
}

// setOnStallChange attaches the transition consumer. Must run before start
// (the watchdog is configured by the same goroutine that starts it); later
// calls are ignored.
func (w *passWatchdog) setOnStallChange(fn func(StallSnapshot)) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped || w.timer != nil {
		return
	}
	w.onStallChange = fn
}

// start arms the stall timer. Already-armed, suspended, or stopped watchdogs
// are no-ops.
func (w *passWatchdog) start() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped || w.timer != nil || w.suspended {
		return
	}
	w.lastProgress = time.Now()
	w.timer = w.afterTimer()
}

// heartbeat records one progress point: the pass's current bounded stage and,
// when non-empty, the most recent file path. Progress resolves the current
// stall window (firing becomes possible again) and re-arms the timer. Closing
// a fired window invokes onStallChange with Stalled=false — under mu, so the
// open/close pair of one window is delivered in order.
func (w *passWatchdog) heartbeat(stage TelemetryStage, file string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped {
		return
	}
	w.stage = string(stage)
	if file != "" {
		w.lastFile = file
	}
	// Progress observed means the pass is running again: clear a suspended
	// window (the pass goroutine is the suspender and cannot heartbeat while
	// suspended itself, but the invariant is cheap to keep explicit).
	w.suspended = false
	resolved := w.fired
	w.fired = false
	w.lastProgress = time.Now()
	w.gen++
	if w.timer != nil {
		w.timer.Stop()
	}
	w.timer = w.afterTimer()
	if resolved && w.onStallChange != nil {
		w.onStallChange(StallSnapshot{
			Stage: w.stage,
			Watch: w.watch,
		})
	}
}

// stop releases the watchdog: the timer is dropped, later heartbeats are
// ignored, and any pending fire stays silent. Idempotent.
func (w *passWatchdog) stop() {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.stopped = true
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
	w.mu.Unlock()
	w.stopOnce.Do(func() { close(w.done) })
}

// suspend suspends the stall window without ending the watchdog's life:
// the armed timer is dropped and the generation is bumped (a fire callback
// already in flight becomes stale and stays silent), so time spent waiting
// on something the pass cannot progress through never counts toward a
// stall. Waits that need this are the pass-lease queue (one
// full/incremental pass runs at a time, so a healthy pass queued behind a
// legitimately long predecessor would otherwise sit silent past
// passStallTimeout and fire a FALSE stall verdict — a 512 KiB goroutine
// dump plus a wedged sub-status for a pass that never got to run; the
// predecessor's own watchdog owns that wedge's evidence) and the background
// sidecar migration waits (backfill work the pass does not own). Walks and
// validation are NOT among them: they are the pass's own work and heartbeat
// per walked entry instead (see walkHeartbeater), so a single hung stat
// stays detectable. A suspended
// watchdog fires nothing; resume arms a fresh window. No-op on a stopped
// watchdog and idempotent while suspended.
func (w *passWatchdog) suspend() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped || w.suspended {
		return
	}
	w.suspended = true
	w.gen++ // a fire racing this suspend is stale and must stay silent
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
}

// resume re-arms the stall window after suspend: the pass is running again,
// so the window starts over — suspended time is not progress time. No-op
// when not suspended (or stopped): an un-suspended watchdog keeps its
// current window, and a released one stays released.
func (w *passWatchdog) resume() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped || !w.suspended {
		return
	}
	w.suspended = false
	w.lastProgress = time.Now()
	w.timer = w.afterTimer()
}

// walkHeartbeatEvery bounds how often a progressing walk feeds the pass
// stall watchdog: one heartbeat per interval sits an order of magnitude
// inside passStallTimeout, so a walk whose entries keep arriving — however
// slowly (network or cloud-placeholder storage, the population
// backend/storageprobe exists to name) — is never misread as stalled,
// while a single wedged stat (no further entries arriving) still goes
// silent for the full threshold and fires on schedule.
const walkHeartbeatEvery = 30 * time.Second

// walkHeartbeater adapts a workspace walk's per-entry callback into
// pass-watchdog heartbeats, throttled to at most one per walkHeartbeatEvery
// so walking a large tree does not re-arm a timer per directory entry. The
// most recently observed entry rides along as the diagnostic last-file of
// the stall evidence. Not safe for concurrent use — a walk is
// single-goroutine, and the heartbeater's state needs no lock.
//
// Lock-order note: entry may be invoked under the caller's locks —
// ValidateCollection walks while holding the service read lock — so the
// heartbeat takes the watchdog lock beneath s.mu (s.mu → wd.mu). That order
// is safe and one-way: the watchdog's lock is a leaf for service state —
// its onStallChange consumers reach status surfaces that are deliberately
// lock-free about the service (IndexAvailability is an atomic read; the
// backend status snapshot reads the manager, never the service — ADR-104
// §4's never-block contract) — so nothing ever acquires s.mu while holding
// the watchdog lock.
type walkHeartbeater struct {
	wd    *passWatchdog
	stage TelemetryStage
	last  time.Time
}

// newWalkHeartbeater wraps one watchdog stage into a per-entry heartbeat
// feed.
func newWalkHeartbeater(wd *passWatchdog, stage TelemetryStage) *walkHeartbeater {
	return &walkHeartbeater{wd: wd, stage: stage}
}

// entry records one walked directory entry — file or directory, ignored or
// not, erroring or not: the entry's arrival is itself the liveness signal.
// A nil receiver or watchdog makes it a no-op, so callers can pass
// h.entry unconditionally.
func (h *walkHeartbeater) entry(path string) {
	if h == nil || h.wd == nil {
		return
	}
	if now := time.Now(); now.Sub(h.last) >= walkHeartbeatEvery {
		h.last = now
		h.wd.heartbeat(h.stage, path)
	}
}

// watchContext releases the watchdog as soon as ctx is cancelled, without
// waiting for the pass goroutine to unwind out of a blocked read. The
// goroutine exits on either side of the race: ctx cancellation or stop.
func (w *passWatchdog) watchContext(ctx context.Context) {
	if w == nil || ctx == nil {
		return
	}
	go func() {
		select {
		case <-ctx.Done():
			w.stop()
		case <-w.done:
		}
	}()
}

// afterTimer arms one timer for the current generation. The gen is captured
// by the closure so a fire racing a heartbeat lands on a stale generation.
func (w *passWatchdog) afterTimer() *time.Timer {
	gen := w.gen
	after := w.afterFunc
	if after == nil {
		after = time.AfterFunc
	}
	return after(passStallTimeout, func() { w.fire(gen) })
}

// fire logs the single Warn of one stall window: bounded stage, last file
// path, and the capped goroutine dump naming the exact blocking call. Stale
// generations and already-reported windows stay silent. The onStallChange
// open transition is delivered under mu (before the dump) so it is ordered
// against the window's eventual close; the dump itself is taken outside mu —
// runtime.Stack self-blocks all goroutines.
func (w *passWatchdog) fire(gen uint64) {
	w.mu.Lock()
	if w.stopped || w.fired || gen != w.gen {
		w.mu.Unlock()
		return
	}
	w.fired = true
	stage, file := w.stage, w.lastFile
	if w.onStallChange != nil {
		w.onStallChange(StallSnapshot{
			Stalled:   true,
			Stage:     stage,
			LastFile:  file,
			SilentFor: time.Since(w.lastProgress),
			Watch:     w.watch,
		})
	}
	w.mu.Unlock()
	dump, truncated := goroutineDump()
	w.logger.Warn("vector index pass stalled: no progress for the full stall threshold; dumping goroutine stacks as evidence",
		"threshold", passStallTimeout,
		"stage", stage,
		"last_file", file,
		"dump_truncated", truncated,
		"stacks", dump,
	)
}

// snapshot reads the current liveness verdict without transitioning
// anything: a released watchdog is never stalled, whatever its last window
// did. Nil receivers read as a live (not stalled) pass — Service-side leases
// carry no watchdog, and "no watchdog" must not read as "stalled".
func (w *passWatchdog) snapshot() StallSnapshot {
	if w == nil {
		return StallSnapshot{}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return StallSnapshot{
		Stalled:   w.fired && !w.stopped,
		Stage:     w.stage,
		LastFile:  w.lastFile,
		SilentFor: time.Since(w.lastProgress),
		Watch:     w.watch,
	}
}
