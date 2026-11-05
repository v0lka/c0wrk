package desktop

import (
	"log/slog"
	"sync"
	"time"

	"github.com/v0lka/c0wrk/backend/crashlog"
)

// Component ceilings the default deadline must cover CUMULATIVELY — shutdown
// runs sequentially, so the worst case is the SUM of the bounded steps, not
// the largest one:
//
//   - embeddedLLMStopCeiling: stopEmbeddedLLM is bounded at ~46 s when a quit
//     races an in-flight cold load holding the supervisor gate — a 30 s wait
//     for the single-instance gate plus the stop's own 15/16 s force budget
//     (core/embeddedllm) — after which the child is terminated.
//   - sessionManagerStopBudget: a.app.Shutdown joins the manager's background
//     goroutines and drains both blackboards under ONE shared budget
//     (backend/session/manager.go).
//   - teardownSlack: every remaining step is individually bounded and fast
//     (batcher stop, pending-action drains, judgeWG.Wait, FrontendAPI
//     cleanup, db/sessionLogger close); the slack covers their sum on slow
//     storage.
const (
	embeddedLLMStopCeiling   = 30*time.Second + 16*time.Second
	sessionManagerStopBudget = 10 * time.Second
	teardownSlack            = 10 * time.Second
)

// defaultShutdownHardDeadline bounds the whole Shutdown teardown when
// shutdown.hardDeadline is unset (0): the cumulative worst case of the
// sequential teardown steps (above), with the historical 60 s as a floor —
// normal teardown completes in well under a second, so the deadline only
// fires on a genuinely wedged cancellation path. Sizing it below the
// cumulative worst case would force-exit a legitimate quit mid-stop, leaving
// the detached inference server running — exactly what the deadline prevents.
const cumulativeTeardownCeiling = embeddedLLMStopCeiling + sessionManagerStopBudget + teardownSlack

var defaultShutdownHardDeadline = max(cumulativeTeardownCeiling, 60*time.Second)

// EffectiveShutdownDeadline maps the configured shutdown.hardDeadline
// (seconds; 0 = unset) onto the effective teardown deadline — the single
// source shared by the shutdown watchdog and the GPU-relaunch handover
// budget, so the handover can never be sized against a deadline the
// predecessor is not actually bounded by. See specs/domains/webview-gpu-policy.md.
func EffectiveShutdownDeadline(configuredSeconds int) time.Duration {
	if configuredSeconds > 0 {
		return time.Duration(configuredSeconds) * time.Second
	}
	return defaultShutdownHardDeadline
}

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
// deadline falls back to defaultShutdownHardDeadline; nil logger to
// slog.Default(); nil onExpiry to crashlog.ForceExit(0), whose clean-exit
// hooks (marker removal + exit banner) keep a forced-but-legitimate quit from
// being misreported as a crash on the next launch. Stop the returned watchdog
// via defer once teardown completes.
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
