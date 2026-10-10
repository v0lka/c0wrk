package desktop

import (
	"log/slog"
	"sync"
	"time"

	"github.com/v0lka/c0wrk/backend/crashlog"
)

// Component ceilings the default deadline must cover CUMULATIVELY. Shutdown
// runs its steps sequentially, so the worst case is the SUM of the bounded
// steps, not the largest one:
//
//   - embeddedLLMStopCeiling: stopEmbeddedLLM is bounded at ~46 s when a quit
//     races an in-flight cold load holding the supervisor gate — a 30 s
//     bounded wait for the single-instance gate (backend embeddedStopTimeout)
//     plus the stop's own derived force budget (15 s once the gate is held,
//     16 s on the no-gate force path; core/embeddedllm) — after which the
//     child is terminated.
//   - sessionManagerStopBudget: a.app.Shutdown joins the manager's background
//     goroutines and drains both blackboards under ONE shared stopTimeout
//     budget (10 s; backend/session/manager.go).
//   - teardownSlack: every remaining step is individually bounded and fast
//     (batcher stop, pending-action drains, judgeWG.Wait, FrontendAPI
//     lifecycle cleanup, db/sessionLogger close); the slack covers their sum
//     on slow storage.
const (
	embeddedLLMStopCeiling   = 30*time.Second + 16*time.Second
	sessionManagerStopBudget = 10 * time.Second
	teardownSlack            = 10 * time.Second
)

// defaultShutdownHardDeadline bounds the whole Shutdown teardown when
// shutdown.hardDeadline is unset (0). It is derived from the cumulative
// worst case of the sequential teardown steps (see the component ceilings
// above), with the historical 60 s kept as a floor: normal teardown
// completes in well under a second, so the deadline only ever fires on a
// genuinely wedged cancellation path — never on a merely slow one. Sizing it
// below the cumulative worst case would force-exit a LEGITIMATE quit (a cold
// embedded-model load racing a slow manager teardown) mid-stop, leaving the
// detached llama-server running — exactly the outcome the deadline exists to
// prevent.
const cumulativeTeardownCeiling = embeddedLLMStopCeiling + sessionManagerStopBudget + teardownSlack

var defaultShutdownHardDeadline = max(cumulativeTeardownCeiling, 60*time.Second)

// shutdownWatchdog enforces one hard deadline over the entire application
// teardown. Every step of Shutdown is already bounded individually (task
// goroutine joins, manager background-goroutine joins, blackboard persistence
// workers), but a broken cancellation path can still park the Wails main
// thread indefinitely — the observed failure where the session log ends at
// "shutdown: blackboard persistence workers stopped" and
// "application shutdown: complete" never appears, forcing the user to kill the
// process. The watchdog is the last-resort guarantee: when the deadline
// expires it logs at Error and invokes onExpiry (crashlog.ForceExit(0) in
// production — marker removal + exit banner + os.Exit), so the
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
// to slog.Default(); a nil onExpiry falls back to crashlog.ForceExit(0) — the
// forced exit runs the crash-log clean-exit hooks (marker removal + exit
// banner) before exiting, so a forced-but-legitimate quit is not misreported as
// a crash on the next launch. The returned watchdog must be stopped (defer)
// once teardown completes.
func startShutdownWatchdog(deadline time.Duration, log *slog.Logger, onExpiry func()) *shutdownWatchdog {
	if deadline <= 0 {
		deadline = defaultShutdownHardDeadline
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
		// Also re-check done non-blockingly: stop() can close it in the instant
		// between the deadline firing and this arm running. If teardown
		// completed, the deadline is moot — return without exiting.
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

// stop disarms the watchdog. It is idempotent and safe to call from a defer.
func (w *shutdownWatchdog) stop() {
	w.stopOnce.Do(func() { close(w.done) })
}
