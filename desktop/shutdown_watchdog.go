package desktop

import (
	"log/slog"
	"sync"
	"time"

	"github.com/v0lka/c0wrk/backend/crashlog"

	"github.com/v0lka/c0wrk/core/shutdown"
)

// shutdownWatchdog enforces one hard deadline over the entire application
// teardown. Every step of Shutdown is bounded individually, but a broken
// cancellation path can park the Wails main thread indefinitely (the observed
// failure: the session log ends mid-teardown and "application shutdown:
// complete" never appears); on expiry the watchdog logs at Error and invokes
// onExpiry (crashlog.ForceExit(0) in production), so the process always
// terminates within a bounded time. Armed at the top of Shutdown, disarmed
// via defer on every return path. See specs/domains/crash-logging.md.
type shutdownWatchdog struct {
	deadline time.Duration
	log      *slog.Logger
	onExpiry func()
	done     chan struct{}
	finished chan struct{}
	stopOnce sync.Once
}

// startShutdownWatchdog arms the watchdog and returns it. A non-positive
// deadline falls back to the core/shutdown default hard deadline; nil logger
// to slog.Default(); nil onExpiry to crashlog.ForceExit(0), whose clean-exit
// hooks (marker removal + exit banner) keep a forced-but-legitimate quit from
// being misreported as a crash on the next launch. Stop the returned watchdog
// via defer once teardown completes.
func startShutdownWatchdog(deadline time.Duration, log *slog.Logger, onExpiry func()) *shutdownWatchdog {
	if deadline <= 0 {
		deadline = shutdown.DefaultHardDeadline()
	}
	if log == nil {
		log = slog.Default()
	}
	if onExpiry == nil {
		onExpiry = func() { crashlog.ForceExit(0) }
	}
	w := &shutdownWatchdog{
		deadline: deadline,
		log:      log,
		onExpiry: onExpiry,
		done:     make(chan struct{}),
		finished: make(chan struct{}),
	}
	go w.run()
	return w
}

// run waits for the deadline or for stop, whichever comes first.
func (w *shutdownWatchdog) run() {
	defer close(w.finished)
	timer := time.NewTimer(w.deadline)
	defer timer.Stop()
	// Drain the stop signal first with a non-blocking receive: a teardown that
	// already completed (its deferred stop() closed done) must win even when the
	// deadline has also elapsed. The blocking select below would otherwise pick
	// uniformly at random between the two ready cases and could force an exit for
	// a healthy, just-finished shutdown.
	select {
	case <-w.done:
		return
	default:
	}
	select {
	case <-w.done:
		return
	case <-timer.C:
		// Re-check done non-blockingly: stop() can close it in the instant
		// between the deadline firing and this arm running — a completed
		// teardown must never force an exit.
		select {
		case <-w.done:
			return
		default:
		}
		w.log.Error("shutdown exceeded hard deadline; forcing exit",
			"deadline_ms", w.deadline.Milliseconds())
		w.onExpiry()
	}
}

// stop disarms the watchdog.
func (w *shutdownWatchdog) stop() {
	w.stopOnce.Do(func() { close(w.done) })
}
