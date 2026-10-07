package desktop

import (
	"log/slog"
	"os"
	"sync"
	"time"
)

// defaultShutdownHardDeadline bounds the whole Shutdown teardown when
// shutdown.hardDeadline is unset (0). It is deliberately generous: normal
// teardown completes in well under a second, so the deadline only ever fires
// on a genuinely wedged cancellation path, never on a merely slow one.
const defaultShutdownHardDeadline = 20 * time.Second

// shutdownWatchdog enforces one hard deadline over the entire application
// teardown. Every step of Shutdown is already bounded individually (task
// goroutine joins, manager background-goroutine joins, blackboard persistence
// workers), but a broken cancellation path can still park the Wails main
// thread indefinitely — the observed failure where the session log ends at
// "shutdown: blackboard persistence workers stopped" and
// "application shutdown: complete" never appears, forcing the user to kill the
// process. The watchdog is the last-resort guarantee: when the deadline
// expires it logs at Error and invokes onExpiry (os.Exit in production), so the
// process always terminates within a bounded time.
//
// The watchdog is armed at the top of Shutdown and disarmed (stop) on every
// return path via defer; on expiry it exits the process, so the defer is
// irrelevant in that case.
type shutdownWatchdog struct {
	deadline time.Duration
	log      *slog.Logger
	onExpiry func()
	done     chan struct{}
	finished chan struct{}
	stopOnce sync.Once
}

// startShutdownWatchdog arms the watchdog and returns it. A non-positive
// deadline falls back to defaultShutdownHardDeadline; a nil logger falls back
// to slog.Default(); a nil onExpiry falls back to os.Exit(0) (the production
// behavior). The returned watchdog must be stopped (defer) once teardown
// completes.
func startShutdownWatchdog(deadline time.Duration, log *slog.Logger, onExpiry func()) *shutdownWatchdog {
	if deadline <= 0 {
		deadline = defaultShutdownHardDeadline
	}
	if log == nil {
		log = slog.Default()
	}
	if onExpiry == nil {
		onExpiry = func() { os.Exit(0) }
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
	select {
	case <-w.done:
		return
	case <-timer.C:
		w.log.Error("shutdown exceeded hard deadline; forcing exit",
			"deadline_ms", w.deadline.Milliseconds())
		w.onExpiry()
	}
}

// stop disarms the watchdog. It is idempotent and safe to call from a defer.
func (w *shutdownWatchdog) stop() {
	w.stopOnce.Do(func() { close(w.done) })
}
