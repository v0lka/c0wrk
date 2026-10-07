package desktop

import (
	"io"
	"log/slog"
	"testing"
	"time"
)

// discardLogger returns a logger that drops everything, so watchdog tests do
// not pollute the test output with the expected expiry Error record.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestShutdownWatchdog_FiresOnExpiry verifies the hard-deadline guarantee: when
// teardown does not complete within the deadline, the watchdog logs at Error
// and invokes the forced-exit action — the mechanism that makes the app always
// close instead of beach-balling until it is killed.
func TestShutdownWatchdog_FiresOnExpiry(t *testing.T) {
	expired := make(chan struct{})
	w := startShutdownWatchdog(25*time.Millisecond, discardLogger(), func() { close(expired) })
	defer w.stop()

	select {
	case <-expired:
	case <-time.After(5 * time.Second):
		t.Fatal("watchdog did not fire onExpiry within the budget")
	}
}

// TestShutdownWatchdog_StopDisarms verifies the normal path: once teardown
// completes and stop() is called, the watchdog goroutine exits and never fires.
func TestShutdownWatchdog_StopDisarms(t *testing.T) {
	fired := make(chan struct{}, 1)
	w := startShutdownWatchdog(time.Hour, discardLogger(), func() { fired <- struct{}{} })
	w.stop()

	select {
	case <-w.finished:
	case <-time.After(5 * time.Second):
		t.Fatal("watchdog goroutine did not exit after stop")
	}

	select {
	case <-fired:
		t.Fatal("watchdog fired onExpiry after stop disarmed it")
	default:
	}
}

// TestShutdownWatchdog_DeadlineFallback verifies a non-positive (unset)
// deadline falls back to the built-in default and a nil exit action is accepted
// (production default: os.Exit).
func TestShutdownWatchdog_DeadlineFallback(t *testing.T) {
	w := startShutdownWatchdog(0, nil, func() {})
	defer w.stop()
	if w.deadline != defaultShutdownHardDeadline {
		t.Fatalf("deadline = %v, want the default %v", w.deadline, defaultShutdownHardDeadline)
	}
}

// TestApp_ShutdownDeadline verifies the App resolves the configured shutdown
// deadline and falls back to the default when Startup never set it.
func TestApp_ShutdownDeadline(t *testing.T) {
	a := &App{}
	if got := a.shutdownDeadline(); got != defaultShutdownHardDeadline {
		t.Fatalf("unset deadline = %v, want %v", got, defaultShutdownHardDeadline)
	}
	a.shutdownHardDeadline = 5 * time.Second
	if got := a.shutdownDeadline(); got != 5*time.Second {
		t.Fatalf("configured deadline = %v, want 5s", got)
	}
}
