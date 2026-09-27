package backend

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/core/embeddedllm"
)

// This file covers the OPERATOR CANCELLATION of the background install:
// CancelEmbeddedLLMInstall, the cancellable context the run executes under,
// and the quiet outcome a REQUESTED cancellation produces.
//
// The contract being pinned:
//
//   - the cancel RPC is IDEMPOTENT: with no install in flight it is a success
//     no-op, and so is a second click while the first one is still unwinding.
//   - it never CLAIMS the operation gate — it is a reader of the run's
//     bookkeeping. The run itself releases the gate through its own defer, so
//     the gate stays held between the click and the run's unwind, and every
//     other embedded RPC keeps refusing across that window.
//   - a REQUESTED cancellation is QUIET: no runtime_error toast, no recorded
//     failure (a retry must not inherit a stale error line), a state event and
//     an Info log. The click is the report; core keeps the partial bytes as
//     the resume point, which is why cancelling is not removing.
//   - the quiet outcome requires BOTH signals — the operator's flag AND the
//     cancellation cause. A run that fails for an unrelated reason after the
//     click stays a reported failure, and so does a shutdown of the
//     application context.

// embeddedCancelHardware is a probe result that passes both synchronous
// install gates (the same shape the concurrent-run test uses).
func embeddedCancelHardware() embeddedllm.Hardware {
	return embeddedllm.Hardware{
		Platform: "darwin-arm64", Arch: "arm64", RAMGiB: 32,
		Backend: embeddedllm.BackendMetal,
	}
}

// installCancelBookkeeping snapshots the run's cancel publication for an
// assertion. Direct-field access is fine here: the tests run single-goroutine
// with respect to the bookkeeping at the points they check (the background run
// is parked or finished).
func installCancelBookkeeping(f *FrontendAPI) (cancel context.CancelFunc, requested bool) {
	f.embedded.mu.Lock()
	defer f.embedded.mu.Unlock()
	return f.embedded.installCancel, f.embedded.installCancelRequested
}

// TestCancelEmbeddedLLMInstallWithoutAnInstallIsANoop pins the idempotence:
// on an idle subsystem the cancel is a success no-op (the outcome the operator
// wants already holds) that claims nothing and emits nothing. A late double
// click must not surface as an error either.
func TestCancelEmbeddedLLMInstallWithoutAnInstallIsANoop(t *testing.T) {
	f, rec, _ := newEmbeddedTestAPI(t)

	for i := 1; i <= 2; i++ {
		if err := f.CancelEmbeddedLLMInstall(); err != nil {
			t.Fatalf("cancel click %d on an idle subsystem = %v, want a no-op success", i, err)
		}
	}
	if got := f.embeddedBusyOperation(); got != embeddedOpIdle {
		t.Errorf("the gate is held by %q after a no-op cancel, want idle", got)
	}
	if cancel, requested := installCancelBookkeeping(f); cancel != nil || requested {
		t.Errorf("bookkeeping after a no-op cancel = %v/%v, want cleared", cancel, requested)
	}
	if n := rec.count(EventRuntimeError); n != 0 {
		t.Errorf("a no-op cancel raised %d runtime_error toast(s)", n)
	}
	if n := rec.count(EventEmbeddedLLMState); n != 0 {
		t.Errorf("a no-op cancel emitted %d state event(s)", n)
	}
}

// TestCancelEmbeddedLLMInstallIsQuietAndReleasesTheGate drives the full RPC
// flow. The installFn double parks on its OWN release channel once the run's
// context is cancelled, so the moment between the cancel click and the run's
// unwind is observable — that is the window in which the gate must still be
// held by the run, because the cancel never claims or releases it.
func TestCancelEmbeddedLLMInstallIsQuietAndReleasesTheGate(t *testing.T) {
	f, rec, _ := newEmbeddedTestAPI(t)
	f.embedded.probeFn = func(context.Context, *slog.Logger) (embeddedllm.Hardware, error) {
		return embeddedCancelHardware(), nil
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	f.embedded.installFn = func(runCtx context.Context, _ *embeddedllm.Installer,
		_ embeddedllm.InstallOptions) (*embeddedllm.InstallReport, error) {
		close(entered)
		// The double plays the real downloader's part: park until the run's
		// context is cancelled, then end the run with the very cancellation
		// cause core wraps for the caller to branch on.
		<-runCtx.Done()
		<-release
		return nil, runCtx.Err()
	}

	if err := f.InstallEmbeddedLLM(); err != nil {
		t.Fatalf("InstallEmbeddedLLM: %v", err)
	}
	<-entered
	if !f.GetEmbeddedLLMStatus().Installing {
		t.Fatal("Installing = false while the background run is in flight")
	}
	if cancel, _ := installCancelBookkeeping(f); cancel == nil {
		t.Fatal("the run's cancel was not published while the run is in flight")
	}

	if err := f.CancelEmbeddedLLMInstall(); err != nil {
		t.Fatalf("CancelEmbeddedLLMInstall: %v", err)
	}
	// The cancellation is delivered but the run is still parked on its own
	// release channel: the GATE must still be held by the run. The cancel is a
	// reader, not an operation — it must neither claim the gate nor release it
	// on the run's behalf, or a second install could race the unwinding run
	// for the staging tree.
	if got := f.embeddedBusyOperation(); got != embeddedOpInstall {
		t.Fatalf("the gate is held by %q between the cancel click and the run's unwind, want %q",
			got, embeddedOpInstall)
	}
	// A second click while the first one is still unwinding stays an
	// idempotent success — not a refusal, not an error.
	if err := f.CancelEmbeddedLLMInstall(); err != nil {
		t.Fatalf("a second cancel click while the run unwinds: %v", err)
	}

	close(release)
	waitForIdleGate(t, f, "the cancelled install to release the gate")

	// The QUIET outcome: no toast and no recorded failure — a retry must not
	// inherit a stale error line from a cancellation nobody needs to be told
	// about, because the click is the report.
	if toasts := rec.runtimeErrors(); len(toasts) != 0 {
		t.Errorf("a cancelled install raised %d runtime_error toast(s): %+v", len(toasts), toasts)
	}
	if status := f.GetEmbeddedLLMStatus(); status.Installing || status.Error != "" || status.InstallError != "" {
		t.Errorf("status after the cancellation = installing %v, error %q, install_error %q; want the run over and no recorded failure",
			status.Installing, status.Error, status.InstallError)
	}
	// The bookkeeping is withdrawn (and the context released) with the run.
	if cancel, requested := installCancelBookkeeping(f); cancel != nil || requested {
		t.Errorf("bookkeeping after the cancelled run = %v/%v, want cleared", cancel, requested)
	}
	// The outcome still reaches the UI — as a state event with NO error field.
	rec.waitForCount(t, EventEmbeddedLLMState, 1)
	states := rec.of(EventEmbeddedLLMState)
	last := statePayload(t, states[len(states)-1])
	if last.Installed || last.Error != "" || last.InstallError != "" {
		t.Errorf("final state event = %+v, want not installed and no error", last)
	}

	// The quiet outcome leaves the slot usable: a retry is accepted and runs.
	releaseRetry := make(chan struct{})
	runs := make(chan struct{}, 1)
	f.embedded.installFn = func(context.Context, *embeddedllm.Installer,
		embeddedllm.InstallOptions) (*embeddedllm.InstallReport, error) {
		runs <- struct{}{}
		<-releaseRetry
		return nil, errors.New("second attempt")
	}
	if err := f.InstallEmbeddedLLM(); err != nil {
		t.Fatalf("a retry after a cancelled install was refused: %v", err)
	}
	<-runs
	close(releaseRetry)
	waitForIdleGate(t, f, "the retry to release the gate")
}

// TestCancelEmbeddedLLMInstallDoesNotSilenceAGenuineFailure pins the AND of
// the quiet-outcome predicate: the requested flag ALONE must not quiet a run
// that failed for an unrelated reason. The operator cancelled, but the
// download broke anyway, and a silent outcome would hide a fault the retry is
// about to hit again.
func TestCancelEmbeddedLLMInstallDoesNotSilenceAGenuineFailure(t *testing.T) {
	f, rec, _ := newEmbeddedTestAPI(t)
	f.embedded.probeFn = func(context.Context, *slog.Logger) (embeddedllm.Hardware, error) {
		return embeddedCancelHardware(), nil
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	failure := errors.New("sha256 mismatch: the pinned runtime archive changed upstream")
	f.embedded.installFn = func(context.Context, *embeddedllm.Installer,
		embeddedllm.InstallOptions) (*embeddedllm.InstallReport, error) {
		close(entered)
		<-release // ignores the context on purpose: the fault arrives first
		return nil, failure
	}

	if err := f.InstallEmbeddedLLM(); err != nil {
		t.Fatalf("InstallEmbeddedLLM: %v", err)
	}
	<-entered
	if err := f.CancelEmbeddedLLMInstall(); err != nil {
		t.Fatalf("CancelEmbeddedLLMInstall: %v", err)
	}
	close(release)
	waitForIdleGate(t, f, "the failed run to release the gate")

	rec.waitForCount(t, EventRuntimeError, 1)
	toasts := rec.runtimeErrors()
	if len(toasts) != 1 {
		t.Fatalf("runtime_error emitted %d time(s), want 1: a cancelled-but-failed run is a reported failure", len(toasts))
	}
	if toasts[0]["error_code"] != embeddedErrCodeInstall {
		t.Errorf("error_code = %q, want %q", toasts[0]["error_code"], embeddedErrCodeInstall)
	}
	if !strings.Contains(toasts[0]["message"], failure.Error()) {
		t.Errorf("the toast message %q does not carry the cause", toasts[0]["message"])
	}
	status := waitForStatus(t, f, "the failure to be recorded", func(s EmbeddedLLMStatus) bool {
		return !s.Installing && s.InstallError != ""
	})
	if !strings.Contains(status.InstallError, "sha256 mismatch") {
		t.Errorf("status.InstallError = %q, want the failure cause", status.InstallError)
	}
}
