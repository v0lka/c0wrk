package vectorindex

// Tests for the incremental-failure backoff and circuit breaker
// (specs/decisions/122-vector-incremental-failure-backoff.md): a failed
// debounce-armed pass backs subsequent arms off exponentially (5s doubled
// per consecutive failure, capped at 10 minutes) and, at the breaker
// threshold, suspends file-change arming with an honest suspended status.
//
// Timer behavior runs on virtual time (testing/synctest) — the project's
// timing-test rules forbid sleeps. The real-filesystem integration (tripping
// the breaker through actual failed passes, then resetting via Reindex)
// stays outside synctest and joins with explicit barriers: the afterFunc
// seam hands the fired callbacks to a channel the test launches and joins,
// so no arm ever waits out a real backoff delay — the delay is asserted as
// data, not awaited as time.

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// errEmbedderDown is the stand-in embedder/runtime fault every breaker test
// fails passes with; the suspend status must name it verbatim.
var errEmbedderDown = errors.New("embedder down (test)")

// TestIncrementalArmDelay_BackoffSchedule pins the arm-delay formula: the
// configured debounce window while the streak is zero, then the exponential
// backoff (5s doubled per consecutive failure) capped at 10 minutes.
func TestIncrementalArmDelay_BackoffSchedule(t *testing.T) {
	mgr := &Manager{}
	cases := []struct {
		streak int
		want   time.Duration
	}{
		{0, DefaultDebounce},
		{1, incrementalFailureBackoffStart},
		{2, 2 * incrementalFailureBackoffStart},
		{3, 4 * incrementalFailureBackoffStart},
		{4, 8 * incrementalFailureBackoffStart},
		{5, 16 * incrementalFailureBackoffStart},
		{6, 32 * incrementalFailureBackoffStart},
		{7, 64 * incrementalFailureBackoffStart},
		// The cap: 5s * 2^7 = 640s already exceeds 10 minutes.
		{8, incrementalFailureBackoffMax},
		{14, incrementalFailureBackoffMax},
		{100, incrementalFailureBackoffMax},
	}
	for _, tc := range cases {
		mgr.debounceMu.Lock()
		mgr.incFailStreak = tc.streak
		got := mgr.incrementalArmDelayLocked()
		mgr.debounceMu.Unlock()
		if got != tc.want {
			t.Errorf("streak %d: arm delay = %v, want %v", tc.streak, got, tc.want)
		}
	}

	// A configured (non-default) debounce window is the unbacked arm delay.
	cfgd := &Manager{debounce: 250 * time.Millisecond}
	cfgd.debounceMu.Lock()
	got := cfgd.incrementalArmDelayLocked()
	cfgd.debounceMu.Unlock()
	if got != 250*time.Millisecond {
		t.Errorf("healthy arm delay = %v, want the configured 250ms debounce", got)
	}
}

// TestManagerDebounce_BackoffArmsAfterFailedPasses drives the arm schedule
// on virtual time: after each consecutive failure the NEXT arm carries the
// doubled delay (never the debounce window), fires exactly at the backoff
// boundary, and a success restores the plain debounce arm. scheduleIncrementalLocked
// is the low-level seam here on purpose — the NotifyFileChange suspension
// gate has its own test below.
func TestManagerDebounce_BackoffArmsAfterFailedPasses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const debounce = time.Second
		mgr := &Manager{debounce: debounce, logger: slog.New(slog.DiscardHandler)}
		var mu sync.Mutex
		var delays []time.Duration
		var fires atomic.Int32
		mgr.afterFunc = func(d time.Duration, f func()) *time.Timer {
			mu.Lock()
			delays = append(delays, d)
			mu.Unlock()
			return time.AfterFunc(d, func() { fires.Add(1); f() })
		}
		arm := func() {
			mgr.debounceMu.Lock()
			mgr.scheduleIncrementalLocked()
			mgr.debounceMu.Unlock()
		}
		lastDelay := func() time.Duration {
			mu.Lock()
			defer mu.Unlock()
			return delays[len(delays)-1]
		}

		// The healthy arm is the plain debounce window.
		arm()
		if got := lastDelay(); got != debounce {
			t.Fatalf("healthy arm delay = %v, want the %v debounce", got, debounce)
		}

		// First failure: the next arm backs off to 5s and fires exactly at
		// the boundary — not before it, and not at the debounce window.
		mgr.recordIncrementalPassFailure(context.Background(), errEmbedderDown)
		mgr.stopDebounce()
		arm()
		if got := lastDelay(); got != 5*time.Second {
			t.Fatalf("arm delay after 1 failure = %v, want 5s", got)
		}
		<-time.NewTimer(5*time.Second - time.Nanosecond).C
		synctest.Wait()
		if got := fires.Load(); got != 0 {
			t.Fatalf("backoff arm fired before 5s: %d fires", got)
		}
		<-time.NewTimer(time.Nanosecond).C
		synctest.Wait()
		if got := fires.Load(); got != 1 {
			t.Fatalf("fires at the 5s boundary = %d, want 1", got)
		}

		// Each further consecutive failure doubles the next arm's delay.
		for i, want := range []time.Duration{
			10 * time.Second, 20 * time.Second, 40 * time.Second, 80 * time.Second,
		} {
			mgr.recordIncrementalPassFailure(context.Background(), errEmbedderDown)
			mgr.stopDebounce()
			arm()
			if got := lastDelay(); got != want {
				t.Fatalf("arm delay after %d failures = %v, want %v", i+2, got, want)
			}
		}

		// A success resets the schedule: the next arm is the debounce window
		// again (the "success of any pass" reset path).
		mgr.recordIncrementalPassSuccess()
		mgr.stopDebounce()
		arm()
		if got := lastDelay(); got != debounce {
			t.Fatalf("arm delay after a successful pass = %v, want the %v debounce", got, debounce)
		}
		mgr.stopDebounce()
	})
}

// TestManagerBreaker_SuspendsArmingWithHonestStatus trips the breaker: at
// the threshold the status reports unavailable with the separate suspended
// kind and a reason naming the failure count and the last cause, and
// file-change arming stops — both the NotifyFileChange arm and a late timer
// fire (armed before the trip) must refuse to continue the pipeline.
func TestManagerBreaker_SuspendsArmingWithHonestStatus(t *testing.T) {
	mgr := &Manager{
		logger:        slog.New(slog.DiscardHandler),
		indexer:       &Indexer{},
		workspacePath: "/ws-project",
	}
	pendingTimer := func() bool {
		mgr.debounceMu.Lock()
		defer mgr.debounceMu.Unlock()
		return mgr.debounceTimer != nil
	}

	// Below the threshold arming continues (backed off, but arming).
	for range incrementalFailureBreakerThreshold - 1 {
		mgr.recordIncrementalPassFailure(context.Background(), errEmbedderDown)
	}
	mgr.NotifyFileChange()
	if !pendingTimer() {
		t.Fatal("arming must continue below the breaker threshold")
	}
	mgr.stopDebounce() // the pre-trip arm must not leak into the trip assertions

	mgr.recordIncrementalPassFailure(context.Background(), errEmbedderDown) // threshold → trip

	st := mgr.GetIndexStatus()
	if st.State != IndexStateUnavailable {
		t.Errorf("suspended state = %q, want %q", st.State, IndexStateUnavailable)
	}
	if st.ErrorKind != IndexErrorKindSuspended {
		t.Errorf("suspended error kind = %q, want %q", st.ErrorKind, IndexErrorKindSuspended)
	}
	wantCount := strconv.Itoa(incrementalFailureBreakerThreshold) + " consecutive failed passes"
	if !strings.Contains(st.LastError, wantCount) {
		t.Errorf("suspend reason %q does not name the failure count (%q)", st.LastError, wantCount)
	}
	if !strings.Contains(st.LastError, errEmbedderDown.Error()) {
		t.Errorf("suspend reason %q does not name the last cause (%q)", st.LastError, errEmbedderDown.Error())
	}

	// File-change arming is suspended: the notification no longer arms.
	mgr.NotifyFileChange()
	if pendingTimer() {
		t.Fatal("file-change arming must be suspended after the breaker tripped")
	}

	// A late timer fire (armed before the trip) runs no pass and re-arms
	// nothing: the pipeline stays suspended at the source.
	mgr.runIncrementalGuarded()
	if pendingTimer() {
		t.Fatal("the suspended late fire must not re-arm the debounce")
	}
}

// TestManagerBreaker_ResetPathsResumeArming covers the breaker's reset
// paths at the manager seam: a successful pass and the explicit reset
// (behind Reindex and SwitchProject) both clear the streak, restore the
// plain debounce arm, and drop the suspend status pair.
func TestManagerBreaker_ResetPathsResumeArming(t *testing.T) {
	newTripped := func() *Manager {
		mgr := &Manager{
			logger:        slog.New(slog.DiscardHandler),
			indexer:       &Indexer{},
			workspacePath: "/ws-project",
		}
		for range incrementalFailureBreakerThreshold {
			mgr.recordIncrementalPassFailure(context.Background(), errEmbedderDown)
		}
		return mgr
	}

	// Reset path 1: a successful pass.
	mgr := newTripped()
	if st := mgr.GetIndexStatus(); st.ErrorKind != IndexErrorKindSuspended {
		t.Fatalf("pre-reset kind = %q, want %q", st.ErrorKind, IndexErrorKindSuspended)
	}
	mgr.recordIncrementalPassSuccess()
	if st := mgr.GetIndexStatus(); st.ErrorKind != "" || st.LastError != "" {
		t.Errorf("success left the suspend pair behind: kind=%q reason=%q", st.ErrorKind, st.LastError)
	}
	mgr.debounceMu.Lock()
	delay := mgr.incrementalArmDelayLocked()
	mgr.debounceMu.Unlock()
	if delay != DefaultDebounce {
		t.Errorf("post-success arm delay = %v, want the default debounce", delay)
	}
	mgr.NotifyFileChange()
	mgr.debounceMu.Lock()
	pending := mgr.debounceTimer != nil
	mgr.debounceMu.Unlock()
	if !pending {
		t.Fatal("file-change arming must resume after a successful pass resets the breaker")
	}
	mgr.stopDebounce()

	// Reset path 2: the explicit reset behind Reindex / SwitchProject.
	mgr2 := newTripped()
	mgr2.resetIncrementalFailureState()
	if st := mgr2.GetIndexStatus(); st.ErrorKind != "" || st.LastError != "" {
		t.Errorf("explicit reset left the suspend pair behind: kind=%q reason=%q", st.ErrorKind, st.LastError)
	}
	mgr2.NotifyFileChange()
	mgr2.debounceMu.Lock()
	pending = mgr2.debounceTimer != nil
	mgr2.debounceMu.Unlock()
	if !pending {
		t.Fatal("file-change arming must resume after the explicit breaker reset")
	}
	mgr2.stopDebounce()
}

// TestManagerBreaker_CancelledPassIsNotAVerdict proves a cancelled pass
// counts toward neither the backoff nor the breaker: with a cancelled index
// context the fired pass fails with the context error, the streak stays
// zero, and the next arm carries the plain debounce window.
func TestManagerBreaker_CancelledPassIsNotAVerdict(t *testing.T) {
	persistDir, ws := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	embed := func(ctx context.Context, _ string) ([]float32, error) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return []float32{0.01, 0.02}, nil
	}
	mgr, err := NewManager(ManagerConfig{EmbeddingFunc: embed, Debounce: 25 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	armed := make(chan func(), 1)
	var mu sync.Mutex
	var delays []time.Duration
	mgr.afterFunc = func(d time.Duration, f func()) *time.Timer {
		mu.Lock()
		delays = append(delays, d)
		mu.Unlock()
		armed <- f
		return nil
	}

	if err := mgr.SwitchProject("project-cancel", ws, filepath.Join(persistDir, "vi"), ProjectCallbacks{}); err != nil {
		t.Fatalf("SwitchProject: %v", err)
	}
	waitForBackgroundPasses(t, mgr, mgr.Service())

	// Install a cancelled index context (in-package seam; the manager is
	// idle): the next fired pass must abort into the cancelled branch. A
	// fresh file forces the pass to reach the embedder (whose ctx-cancellation
	// error is what fails it).
	if err := os.WriteFile(filepath.Join(ws, "b.go"), []byte("package b\n"), 0o644); err != nil {
		t.Fatalf("write b.go: %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	mgr.mu.Lock()
	mgr.indexCtx = cancelled
	mgr.mu.Unlock()

	mgr.NotifyFileChange()
	f := <-armed
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	<-done

	mgr.debounceMu.Lock()
	streak := mgr.incFailStreak
	mgr.debounceMu.Unlock()
	if streak != 0 {
		t.Errorf("failure streak after a cancelled pass = %d, want 0 (a cancellation is not a verdict)", streak)
	}
	// The next arm is the plain debounce window again — no backoff accrued.
	mgr.NotifyFileChange()
	f2 := <-armed
	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		f2()
	}()
	<-done2
	mu.Lock()
	lastDelay := delays[len(delays)-1]
	mu.Unlock()
	if lastDelay != 25*time.Millisecond {
		t.Errorf("arm delay after a cancelled pass = %v, want the 25ms debounce", lastDelay)
	}
	mgr.stopDebounce()
}

// TestManagerBreaker_TripSuspendAndReindexReset is the end-to-end path on a
// real service and workspace: five consecutive embedder failures trip the
// breaker — each next arm is backed off 5s→10s→20s→40s (the 5th failure
// trips the breaker before an 80s arm would fire; the delay is asserted as
// data, and the afterFunc seam fires callbacks directly, so no real delay
// is ever awaited) — the suspend status goes honest, file-change arming
// stops, a late fire runs no pass, and a manual Reindex resets the breaker
// so the next healthy pass resumes indexing and re-arms file changes.
func TestManagerBreaker_TripSuspendAndReindexReset(t *testing.T) {
	persistDir, ws := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	var failEmbeds atomic.Bool
	var embedCalls atomic.Int32
	embed := func(_ context.Context, _ string) ([]float32, error) {
		embedCalls.Add(1)
		if failEmbeds.Load() {
			return nil, errEmbedderDown
		}
		return []float32{0.01, 0.02}, nil
	}
	mgr, err := NewManager(ManagerConfig{EmbeddingFunc: embed, Debounce: 20 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	armed := make(chan func(), 1)
	var mu sync.Mutex
	var delays []time.Duration
	mgr.afterFunc = func(d time.Duration, f func()) *time.Timer {
		mu.Lock()
		delays = append(delays, d)
		mu.Unlock()
		armed <- f
		return nil
	}

	if err := mgr.SwitchProject("project-breaker", ws, filepath.Join(persistDir, "vi"), ProjectCallbacks{}); err != nil {
		t.Fatalf("SwitchProject: %v", err)
	}
	waitForBackgroundPasses(t, mgr, mgr.Service())

	// Five consecutive failing passes, each fired through its own armed
	// callback and joined: the arm delays walk the backoff schedule. Every
	// iteration writes a fresh file so the pass genuinely needs the
	// (failing) embedder — an unchanged workspace would no-op to success
	// without embedding anything.
	failEmbeds.Store(true)
	for i := range incrementalFailureBreakerThreshold {
		if err := os.WriteFile(filepath.Join(ws, "f"+strconv.Itoa(i)+".go"), []byte("package main\n"), 0o644); err != nil {
			t.Fatalf("write change file %d: %v", i, err)
		}
		mgr.NotifyFileChange()
		f := <-armed
		done := make(chan struct{})
		go func() {
			defer close(done)
			f()
		}()
		<-done
		mu.Lock()
		got := delays[len(delays)-1]
		mu.Unlock()
		want := []time.Duration{
			20 * time.Millisecond,
			5 * time.Second,
			10 * time.Second,
			20 * time.Second,
			40 * time.Second,
		}[i]
		if got != want {
			t.Fatalf("arm delay for failing pass %d = %v, want %v", i+1, got, want)
		}
	}

	// The breaker tripped: honest suspend status.
	st := mgr.GetIndexStatus()
	if st.State != IndexStateUnavailable {
		t.Errorf("post-trip state = %q, want %q", st.State, IndexStateUnavailable)
	}
	if st.ErrorKind != IndexErrorKindSuspended {
		t.Errorf("post-trip error kind = %q, want %q", st.ErrorKind, IndexErrorKindSuspended)
	}
	if !strings.Contains(st.LastError, "5 consecutive failed passes") ||
		!strings.Contains(st.LastError, errEmbedderDown.Error()) {
		t.Errorf("post-trip reason %q must name the count and the last cause", st.LastError)
	}

	// File changes no longer arm, and a late fire runs no pass (the embedder
	// call count freezes). Arming is observed as an afterFunc invocation —
	// the seam records every arm in the delays slice, so a refused
	// notification is a deterministic "no new arm".
	mgr.NotifyFileChange()
	mu.Lock()
	armedCount := len(delays)
	mu.Unlock()
	mgr.NotifyFileChange()
	mu.Lock()
	refused := len(delays) == armedCount
	mu.Unlock()
	if !refused {
		t.Fatal("file-change arming ran while the breaker was tripped")
	}
	calls := embedCalls.Load()
	mgr.runIncrementalGuarded()
	if embedCalls.Load() != calls {
		t.Errorf("the suspended late fire ran a pass: embed calls %d → %d", calls, embedCalls.Load())
	}

	// The manual-Reindex reset path: the escape hatch resets the breaker and
	// the healthy pass resumes indexing and re-arms file changes.
	failEmbeds.Store(false)
	if err := mgr.Reindex(context.Background()); err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	waitForBackgroundPasses(t, mgr, mgr.Service())
	st = mgr.GetIndexStatus()
	if st.State != IndexStateReady {
		t.Errorf("post-reset state = %q, want %q (the healthy pass reported ready)", st.State, IndexStateReady)
	}
	if st.ErrorKind == IndexErrorKindSuspended || st.LastError != "" {
		t.Errorf("post-reset status kept the suspend pair: kind=%q reason=%q", st.ErrorKind, st.LastError)
	}
	// The post-reset arm carries the plain debounce window again; the armed
	// callback lands in the seam channel for a final healthy pass.
	mgr.NotifyFileChange()
	mu.Lock()
	resumed := len(delays) == armedCount+1
	resetDelay := delays[len(delays)-1]
	mu.Unlock()
	if !resumed {
		t.Fatal("file-change arming must resume after the manual Reindex reset")
	}
	if resetDelay != 20*time.Millisecond {
		t.Errorf("post-reset arm delay = %v, want the plain 20ms debounce", resetDelay)
	}
	f := <-armed
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	<-done
	mgr.stopDebounce()
}

// TestManagerBreaker_SuspendPairSurvivesTerminalReadyReport pins the trip's
// status-persistence rule (ADR-122): a pass that FAILS with a live collection
// reports the terminal "ready" through wrapProgress (indexer.go's
// ready-vs-unavailable contract), and while the breaker is tripped that ready
// report must NOT erase the suspended verdict — the reachable producer is a
// failing branch-switch pass after the trip (a failing Reindex repair
// re-records its own verdict, and debounce arms are refused while
// suspended). Without the wrapProgress gate the status would sit at a bare
// "ready" while file-change indexing silently stayed suspended — the
// "silently stuck ready" surface ADR-122 exists to prevent. A successful
// pass still clears the pair (the breaker's own reset path).
func TestManagerBreaker_SuspendPairSurvivesTerminalReadyReport(t *testing.T) {
	mgr := &Manager{
		logger:        slog.New(slog.DiscardHandler),
		indexer:       &Indexer{},
		workspacePath: "/ws-project",
	}
	for range incrementalFailureBreakerThreshold {
		mgr.recordIncrementalPassFailure(context.Background(), errEmbedderDown)
	}
	if st := mgr.GetIndexStatus(); st.ErrorKind != IndexErrorKindSuspended {
		t.Fatalf("pre-report kind = %q, want %q", st.ErrorKind, IndexErrorKindSuspended)
	}

	// The failing pass's terminal ready report rides wrapProgress (the
	// manager wraps every pass callback with it). The state updates — the
	// live collection keeps search answering — but the suspended pair stays.
	mgr.wrapProgress(nil)(PhaseBoth, IndexStateReady, 0, 0, "")
	st := mgr.GetIndexStatus()
	if st.State != IndexStateReady {
		t.Errorf("post-report state = %q, want %q (search keeps answering)", st.State, IndexStateReady)
	}
	if st.ErrorKind != IndexErrorKindSuspended || st.LastError == "" {
		t.Errorf("the terminal ready report erased the suspend pair: kind=%q reason=%q", st.ErrorKind, st.LastError)
	}

	// The breaker's own reset path still clears it: a successful pass.
	mgr.recordIncrementalPassSuccess()
	if st := mgr.GetIndexStatus(); st.ErrorKind != "" || st.LastError != "" {
		t.Errorf("the reset left the pair behind: kind=%q reason=%q", st.ErrorKind, st.LastError)
	}

	// The unsuspended ready path clears the pair as before (the recovery
	// report of a NON-breaker failure).
	mgr.statusMu.Lock()
	mgr.lastError = "transient os fault"
	mgr.lastErrorKind = IndexErrorKindTransient
	mgr.statusMu.Unlock()
	mgr.wrapProgress(nil)(PhaseBoth, IndexStateReady, 0, 0, "")
	if st := mgr.GetIndexStatus(); st.ErrorKind != "" || st.LastError != "" {
		t.Errorf("an unsuspended ready report kept the failure pair: kind=%q reason=%q", st.ErrorKind, st.LastError)
	}
}

// statusPush is one OnProgress invocation the recorder below observed.
type statusPush struct {
	phase        IndexPhase
	state        IndexState
	files, total int
	file         string
}

// TestManagerBreaker_TripPushesStatusThroughCallbacks pins the trip's live
// notification: the verdict is pushed through the project's OnProgress
// callback (the focused root's vector_index:status stream) exactly once,
// with the last-known progress values; below the threshold nothing is
// pushed; a superseded pass (its index context replaced by a newer
// lifecycle that owns the status) stays silent — the repair path's
// superseded-pass silence; and a manager without callbacks stays nil-safe
// while still recording its stored verdict.
func TestManagerBreaker_TripPushesStatusThroughCallbacks(t *testing.T) {
	var mu sync.Mutex
	record := func(pushes *[]statusPush) ProgressCallback {
		return func(phase IndexPhase, state IndexState, files, total int, file string) {
			mu.Lock()
			*pushes = append(*pushes, statusPush{phase, state, files, total, file})
			mu.Unlock()
		}
	}
	newSetup := func(indexCtx context.Context) *Manager {
		mgr := &Manager{
			logger:        slog.New(slog.DiscardHandler),
			indexer:       &Indexer{},
			workspacePath: "/ws-project",
			indexCtx:      indexCtx,
		}
		// Last-known progress values the push must carry.
		mgr.statusMu.Lock()
		mgr.filesIndexed, mgr.totalFiles, mgr.currentFile = 7, 12, "changed.go"
		mgr.statusMu.Unlock()
		return mgr
	}

	// 1. The trip pushes the verdict through the callbacks, exactly once.
	passCtx := context.Background()
	mgr := newSetup(passCtx)
	var pushes []statusPush
	mgr.projectCallbacks = ProjectCallbacks{OnProgress: record(&pushes)}
	for range incrementalFailureBreakerThreshold - 1 {
		mgr.recordIncrementalPassFailure(passCtx, errEmbedderDown)
	}
	mu.Lock()
	below := len(pushes)
	mu.Unlock()
	if below != 0 {
		t.Fatalf("pushes below the threshold = %d, want 0", below)
	}
	mgr.recordIncrementalPassFailure(passCtx, errEmbedderDown) // threshold → trip
	mu.Lock()
	count := len(pushes)
	var last statusPush
	if count > 0 {
		last = pushes[count-1]
	}
	mu.Unlock()
	if count != 1 {
		t.Fatalf("pushes after the trip = %d, want exactly 1", count)
	}
	if last.state != IndexStateUnavailable {
		t.Errorf("pushed state = %q, want %q", last.state, IndexStateUnavailable)
	}
	if last.phase != "" {
		t.Errorf("pushed phase = %q, want the empty zero phase (a trip carries no phase)", last.phase)
	}
	if last.files != 7 || last.total != 12 || last.file != "changed.go" {
		t.Errorf("pushed progress = %d/%d/%q, want the last-known 7/12/changed.go", last.files, last.total, last.file)
	}

	// 2. A superseded pass stays silent: its index context was replaced by a
	//    newer lifecycle (which already reset the breaker and owns the
	//    status from its own reports).
	mgr2 := newSetup(passCtx)
	var pushes2 []statusPush
	mgr2.projectCallbacks = ProjectCallbacks{OnProgress: record(&pushes2)}
	newer, newerCancel := context.WithCancel(context.Background())
	defer newerCancel()
	mgr2.mu.Lock()
	mgr2.indexCtx = newer
	mgr2.mu.Unlock()
	for range incrementalFailureBreakerThreshold {
		mgr2.recordIncrementalPassFailure(passCtx, errEmbedderDown)
	}
	mu.Lock()
	n2 := len(pushes2)
	mu.Unlock()
	if n2 != 0 {
		t.Fatalf("a superseded pass pushed %d events, want 0", n2)
	}
	// The stored verdict still landed (the push decision never gates the
	// status write itself).
	if st := mgr2.GetIndexStatus(); st.ErrorKind != IndexErrorKindSuspended {
		t.Errorf("superseded trip stored kind = %q, want %q", st.ErrorKind, IndexErrorKindSuspended)
	}

	// 3. Nil callbacks stay nil-safe: the trip records its verdict without a
	//    push target (the pull path serves it).
	mgr3 := newSetup(passCtx)
	for range incrementalFailureBreakerThreshold {
		mgr3.recordIncrementalPassFailure(passCtx, errEmbedderDown)
	}
	if st := mgr3.GetIndexStatus(); st.ErrorKind != IndexErrorKindSuspended {
		t.Errorf("nil-callback trip stored kind = %q, want %q", st.ErrorKind, IndexErrorKindSuspended)
	}
}
