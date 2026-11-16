package core

// Delegation-launcher auto-retry tests (specs/domains/orchestration/
// delegation.md): a blocking delegation whose execution error classifies
// transient (ADR-065/ADR-128 class) is re-launched ONCE after a fixed backoff
// BEFORE the outcome settles the unit failed — the durable ledger never
// observes failed→running (ADR-048). Non-transient errors settle failed
// immediately; a retried leg that pauses settles as a pause; a retry that
// fails again settles failed exactly once.
//
// Everything runs in a synctest bubble: the fixed 30s backoff elapses on the
// virtual clock (see the timing conventions in docs/development/
// test-timing-migration.md), and the scripted LLM caller drives the executor
// deterministically — a fake-launcher test, no real provider involved.

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"

	"github.com/v0lka/c0wrk/core/llmbudget"
	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/c0wrk/core/units"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
)

// ---------------------------------------------------------------------------
// Classification: the ADR-065/ADR-128 transient provider class
// ---------------------------------------------------------------------------

// TestIsTransientProviderError_ClassTaxonomy pins the class membership once,
// at the seam the delegation launcher and the task-level auto-resend share:
// rate_limit/overloaded (status or ErrKind) and the dns/connect/reset/timeout
// connection classes qualify; tls/eof/other, non-class statuses (500 even
// when the engine calls it retryable), budget deaths and non-provider errors
// never do.
func TestIsTransientProviderError_ClassTaxonomy(t *testing.T) {
	budgetDeath := fmt.Errorf("adaptive request budget exhausted: %s armed for %q (%s): %w",
		"45s", "local-model", "own budget expiry, not a provider abort; the next budget escalates", context.DeadlineExceeded)

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain executor error", errors.New("executor boom"), false},
		{"429 status", llm.NewError("p", 429, true, errors.New("rate limited")), true},
		{"529 status", llm.NewError("p", 529, true, errors.New("overloaded")), true},
		{"errkind rate_limit at status 0", &llm.Error{Provider: "p", ErrKind: llm.ErrKindRateLimit, Err: errors.New("quota")}, true},
		{"errkind overloaded at status 0", &llm.Error{Provider: "p", ErrKind: llm.ErrKindOverloaded, Err: errors.New("busy")}, true},
		{"connection refused", llm.NewError("p", 0, false, fmt.Errorf("dial tcp: %w", syscall.ECONNREFUSED)), true},
		{"connection reset", llm.NewError("p", 0, false, fmt.Errorf("read: %w", syscall.ECONNRESET)), true},
		{"dns failure", llm.NewError("p", 0, false, &net.DNSError{Err: "no such host", Name: "api.example.com", IsNotFound: true}), true},
		{"provider-side timeout", llm.NewError("p", 0, false, fmt.Errorf("do request: %w", context.DeadlineExceeded)), true},
		{"tls misclassification", llm.NewError("p", 0, false, fmt.Errorf("handshake: %w", &tls.CertificateVerificationError{})), false},
		{"eof ambiguity", llm.NewError("p", 0, true, io.EOF), false},
		{"unclassifiable", llm.NewError("p", 0, false, errors.New("something odd")), false},
		{"500 is not in the class", llm.NewError("p", 500, true, errors.New("internal")), false},
		{"budget death never qualifies", llm.NewError("p", 0, false, budgetDeath), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsTransientProviderError(tt.err); got != tt.want {
				t.Fatalf("IsTransientProviderError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestIsTransientProviderError_BudgetDeathWrappedDeep pins the carve-out in
// both directions: the own-budget-expiry signature dominates every wrapping
// shape, while a provider-side timeout with no budget wrapper qualifies.
func TestIsTransientProviderError_BudgetDeathWrappedDeep(t *testing.T) {
	budgetDeath := llm.NewError("p", 0, false,
		fmt.Errorf("llmbudget: request failed: adaptive request budget exhausted: 45s armed for %q (%s): %w",
			"m", "own budget expiry, not a provider abort; the next budget escalates", context.DeadlineExceeded))
	if IsTransientProviderError(fmt.Errorf("subagent failed: %w", budgetDeath)) {
		t.Fatal("a wrapped budget death must never classify transient (ADR-128 carve-out)")
	}
	providerTimeout := llm.NewError("p", 0, false, fmt.Errorf("do request: %w", context.DeadlineExceeded))
	if !IsTransientProviderError(fmt.Errorf("subagent failed: %w", providerTimeout)) {
		t.Fatal("a wrapped provider-side timeout must classify transient")
	}
	if !llmbudget.IsOwnBudgetExpiry(budgetDeath) || llmbudget.IsOwnBudgetExpiry(providerTimeout) {
		t.Fatal("IsOwnBudgetExpiry must separate the two timeout senses")
	}
}

// ---------------------------------------------------------------------------
// Harness: scripted LLM caller, launch recorder, settlement-recording ledger
// ---------------------------------------------------------------------------

// transientRateLimit is the canonical transient first-attempt failure.
func transientRateLimit() error {
	return llm.NewError("probe", 429, true, errors.New("rate limited"))
}

// settlementRecordingStore records the durable status carried by every SaveUnit for
// the watched id — the settlement sequence a test asserts against. The
// ADR-048 invariant is a property of the SEQUENCE (running→completed, never
// failed→running), not just the final state.
type settlementRecordingStore struct {
	*memUnitStore
	watched  string
	mu       sync.Mutex
	statuses []units.UnitStatus
}

func (s *settlementRecordingStore) SaveUnit(rec units.UnitRecord) error {
	if rec.ID == s.watched {
		s.mu.Lock()
		s.statuses = append(s.statuses, rec.Status)
		s.mu.Unlock()
	}
	return s.memUnitStore.SaveUnit(rec)
}

func (s *settlementRecordingStore) recordedStatuses() []units.UnitStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]units.UnitStatus(nil), s.statuses...)
}

// retryLedgerBB mirrors unitLedgerBB with a settlement-recording unit store.
type retryLedgerBB struct {
	testPersistableBlackboard
	us     *settlementRecordingStore
	ledger units.Ledger
}

func (b *retryLedgerBB) UnitStore() units.Store { return b.us }

func (b *retryLedgerBB) UnitLedger() units.Ledger {
	if b.ledger == nil {
		b.ledger = units.NewLedger(b.us, b.taskID, "")
	}
	return b.ledger
}

func newRetryLedgerBB(taskID, watched string) *retryLedgerBB {
	return &retryLedgerBB{
		testPersistableBlackboard: testPersistableBlackboard{
			MapBlackboard: orchestration.NewMapBlackboard(),
			taskID:        taskID,
		},
		us: &settlementRecordingStore{memUnitStore: newMemUnitStore(), watched: watched},
	}
}

// seedRetryUnit Begins the delegation's durable unit so the launcher's
// Settle calls find a record (production Begins it at registration; direct
// launcher construction must seed it the same way).
func seedRetryUnit(t *testing.T, bb *retryLedgerBB, id string) {
	t.Helper()
	if err := bb.UnitLedger().Begin(units.UnitRecord{ID: id, Kind: units.UnitKindSubagent, Status: units.UnitStatusPending}); err != nil {
		t.Fatalf("ledger Begin %s: %v", id, err)
	}
}

// retryLedgerStatus reads the durable status of one unit.
func retryLedgerStatus(t *testing.T, bb *retryLedgerBB, id string) (units.UnitStatus, bool) {
	t.Helper()
	recs, err := bb.UnitLedger().List()
	if err != nil {
		t.Fatalf("ledger List: %v", err)
	}
	for _, rec := range recs {
		if rec.ID == id {
			return rec.Status, true
		}
	}
	return "", false
}

// newRetryTestLauncher builds a conductorLauncher over the given blackboard,
// caller and emitter — the limit-test harness plus launch recording.
func newRetryTestLauncher(bb orchestration.Blackboard, caller agent.LLMCaller, emitter Emitter) *conductorLauncher {
	return &conductorLauncher{
		bb: bb,
		deps: conductorDeps{
			contextFactory: func(systemPrompt string, _ llm.ModelMetadata, _ string, _ ...orchestration.PruningOverride) ContextManager {
				return &mockContextManager{systemPrompt: systemPrompt}
			},
			toolRegistry: newSubagentTestRegistry(subagentToolSet()),
			llm:          caller,
			toolExec:     &mockToolExecutor{},
			emitter:      emitter,
		},
	}
}

// ---------------------------------------------------------------------------
// runRegularBlocking: transient → one relaunch; permanent → no relaunch
// ---------------------------------------------------------------------------

// TestRunRegularBlocking_TransientFailureRelaunchesOnce is the core AC: a
// simulated transient failure (429) is followed by exactly one re-launch that
// completes; the settled outcome carries the auto-retry note, the blackboard
// step result is a clean completion, and the ledger's settlement sequence is
// running→completed — the first (failed) attempt never reaches it.
func TestRunRegularBlocking_TransientFailureRelaunchesOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		caller := &mockLLMCaller{callFn: func(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
			if calls.Add(1) == 1 {
				return nil, transientRateLimit()
			}
			return executorFinishResponse("recovered"), nil
		}}
		bb := newRetryLedgerBB("task-retry-ok", "d0")
		seedRetryUnit(t, bb, "d0")
		rec := &launchRecorder{}
		l := newRetryTestLauncher(bb, caller, rec)

		registry := tools.NewDelegationRegistry()
		if err := registry.Register("d0", "s", nil, "blocking"); err != nil {
			t.Fatalf("register: %v", err)
		}
		results := l.runRegularBlocking(limitTestCtx(), []tools.DelegationTask{{ID: "d0", Summary: "s", Task: "t"}}, registry)

		if len(results) != 1 {
			t.Fatalf("got %d results, want 1", len(results))
		}
		r := results[0]
		if r.Status != tools.DelegationStatusCompleted || r.Error != nil {
			t.Fatalf("result = %v/%v, want completed with no error (one relaunch must heal a transient failure)", r.Status, r.Error)
		}
		if !strings.HasPrefix(r.Output, "> [auto-retry]") {
			t.Errorf("result output %q does not note the auto-retried leg", r.Output)
		}
		if !strings.Contains(r.Output, "recovered") {
			t.Errorf("result output %q lost the retry leg's own output", r.Output)
		}
		if n := calls.Load(); n != 2 {
			t.Errorf("LLM calls = %d, want exactly 2 (failed leg + one re-launch)", n)
		}
		if n := rec.launchCount("d0"); n != 2 {
			t.Errorf("SubAgentLaunch count = %d, want exactly 2 (one re-launch)", n)
		}
		sr, ok := bb.GetStepResult("d0")
		if !ok || sr.Error != nil {
			t.Fatalf("blackboard step result error = %v (ok=%v), want a clean completion", sr.Error, ok)
		}
		if !strings.HasPrefix(sr.FullOutput, "> [auto-retry]") {
			t.Errorf("persisted output %q does not note the auto-retried leg", sr.FullOutput)
		}
		if st, ok := retryLedgerStatus(t, bb, "d0"); !ok || st != units.UnitStatusCompleted {
			t.Errorf("ledger status = %q (found=%v), want completed", st, ok)
		}
		assertSettlementSequence(t, bb, "d0", []units.UnitStatus{units.UnitStatusPending, units.UnitStatusRunning, units.UnitStatusCompleted})
	})
}

// TestRunRegularBlocking_PermanentFailureSettlesFailedImmediately: every
// non-transient failure — a plain executor error, or a status the engine
// calls retryable but the class excludes (500) — settles failed on the first
// attempt with NO re-launch.
func TestRunRegularBlocking_PermanentFailureSettlesFailedImmediately(t *testing.T) {
	for name, firstErr := range map[string]error{
		"plain error":         errors.New("executor boom"),
		"non-class 500":       llm.NewError("p", 500, true, errors.New("internal")),
		"tls misconfig":       llm.NewError("p", 0, false, fmt.Errorf("handshake: %w", &tls.CertificateVerificationError{})),
		"eof stays unclaimed": llm.NewError("p", 0, true, io.EOF),
	} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var calls atomic.Int32
				caller := &mockLLMCaller{callFn: func(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
					calls.Add(1)
					return nil, firstErr
				}}
				bb := newRetryLedgerBB("task-retry-permanent", "d0")
				seedRetryUnit(t, bb, "d0")
				rec := &launchRecorder{}
				l := newRetryTestLauncher(bb, caller, rec)

				registry := tools.NewDelegationRegistry()
				if err := registry.Register("d0", "s", nil, "blocking"); err != nil {
					t.Fatalf("register: %v", err)
				}
				results := l.runRegularBlocking(limitTestCtx(), []tools.DelegationTask{{ID: "d0", Summary: "s", Task: "t"}}, registry)

				if len(results) != 1 {
					t.Fatalf("got %d results, want 1", len(results))
				}
				r := results[0]
				if r.Status != tools.DelegationStatusFailed || r.Error == nil {
					t.Fatalf("result = %v/%v, want failed with the cause", r.Status, r.Error)
				}
				if strings.Contains(r.Output, "[auto-retry]") {
					t.Errorf("result output %q must not note a retry that never ran", r.Output)
				}
				if n := calls.Load(); n != 1 {
					t.Errorf("LLM calls = %d, want exactly 1 (a permanent failure never relaunches)", n)
				}
				if n := rec.launchCount("d0"); n != 1 {
					t.Errorf("SubAgentLaunch count = %d, want exactly 1", n)
				}
				assertSettlementSequence(t, bb, "d0", []units.UnitStatus{units.UnitStatusPending, units.UnitStatusRunning, units.UnitStatusFailed})
			})
		})
	}
}

// TestRunRegularBlocking_TransientRetryExhaustionSettlesFailedOnce: the retry
// is a hard cap of ONE — a re-launched leg that fails transiently again
// settles failed (once), with the retry noted in the failure markdown.
func TestRunRegularBlocking_TransientRetryExhaustionSettlesFailedOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		caller := &mockLLMCaller{callFn: func(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
			calls.Add(1)
			return nil, transientRateLimit()
		}}
		bb := newRetryLedgerBB("task-retry-exhausted", "d0")
		seedRetryUnit(t, bb, "d0")
		rec := &launchRecorder{}
		l := newRetryTestLauncher(bb, caller, rec)

		registry := tools.NewDelegationRegistry()
		if err := registry.Register("d0", "s", nil, "blocking"); err != nil {
			t.Fatalf("register: %v", err)
		}
		results := l.runRegularBlocking(limitTestCtx(), []tools.DelegationTask{{ID: "d0", Summary: "s", Task: "t"}}, registry)

		r := results[0]
		if r.Status != tools.DelegationStatusFailed || r.Error == nil {
			t.Fatalf("result = %v/%v, want failed after the exhausted retry", r.Status, r.Error)
		}
		if !strings.Contains(r.Error.Error(), "re-launched once") {
			t.Errorf("failed-result markdown %q does not note the auto-retried leg", r.Error.Error())
		}
		if n := calls.Load(); n != 2 {
			t.Errorf("LLM calls = %d, want exactly 2 (one retry cap, never a third attempt)", n)
		}
		if n := rec.launchCount("d0"); n != 2 {
			t.Errorf("SubAgentLaunch count = %d, want exactly 2", n)
		}
		assertSettlementSequence(t, bb, "d0", []units.UnitStatus{units.UnitStatusPending, units.UnitStatusRunning, units.UnitStatusFailed})
	})
}

// TestRunRegularBlocking_TransientRetryLegPauseSettlesPaused pins the new
// pause branch: a re-launched leg that cooperatively pauses settles as a
// pause checkpoint (never wrapped into a failure).
func TestRunRegularBlocking_TransientRetryLegPauseSettlesPaused(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		caller := &mockLLMCaller{callFn: func(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
			if calls.Add(1) == 1 {
				return nil, transientRateLimit()
			}
			return nil, agent.ErrPaused
		}}
		bb := newRetryLedgerBB("task-retry-pause", "d0")
		seedRetryUnit(t, bb, "d0")
		rec := &launchRecorder{}
		l := newRetryTestLauncher(bb, caller, rec)

		registry := tools.NewDelegationRegistry()
		if err := registry.Register("d0", "s", nil, "blocking"); err != nil {
			t.Fatalf("register: %v", err)
		}
		results := l.runRegularBlocking(limitTestCtx(), []tools.DelegationTask{{ID: "d0", Summary: "s", Task: "t"}}, registry)

		r := results[0]
		if r.Status != tools.DelegationStatusPaused {
			t.Fatalf("result status = %v, want paused (a retried leg's pause is a checkpoint, not a failure)", r.Status)
		}
		if !isPaused(r.Error) {
			t.Errorf("result error = %v, want the unwrapped pause sentinel", r.Error)
		}
		if n := rec.launchCount("d0"); n != 2 {
			t.Errorf("SubAgentLaunch count = %d, want exactly 2", n)
		}
		if st, ok := retryLedgerStatus(t, bb, "d0"); !ok || st != units.UnitStatusPaused {
			t.Errorf("ledger status = %q (found=%v), want paused", st, ok)
		}
	})
}

// TestRunRegularBlocking_CancelDuringRetryWindowDoesNotRelaunch: a
// cancellation landing while the launcher waits out the fixed backoff aborts
// the retry — no second launch fires into the dying wave — and the settled
// error chains context.Canceled, so the durable ledger records the unit
// `interrupted` (an abandonment the resume funnel relaunches fresh), never
// `failed` (delegation.md's invariant; without the wrap the transient
// provider error alone would settle failed and the abandoned work would be
// replayed as a stale failure instead of relaunched).
func TestRunRegularBlocking_CancelDuringRetryWindowDoesNotRelaunch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(limitTestCtx())
		defer cancel()
		var calls atomic.Int32
		caller := &mockLLMCaller{callFn: func(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
			if calls.Add(1) == 1 {
				cancel() // the user cancels while the failed leg settles
				return nil, transientRateLimit()
			}
			return executorFinishResponse("should never run"), nil
		}}
		bb := newRetryLedgerBB("task-retry-cancel", "d0")
		seedRetryUnit(t, bb, "d0")
		rec := &launchRecorder{}
		l := newRetryTestLauncher(bb, caller, rec)

		registry := tools.NewDelegationRegistry()
		if err := registry.Register("d0", "s", nil, "blocking"); err != nil {
			t.Fatalf("register: %v", err)
		}
		results := l.runRegularBlocking(ctx, []tools.DelegationTask{{ID: "d0", Summary: "s", Task: "t"}}, registry)

		if results[0].Status != tools.DelegationStatusFailed {
			t.Fatalf("result status = %v, want failed (a canceled wave never relaunches)", results[0].Status)
		}
		if !errors.Is(results[0].Error, context.Canceled) {
			t.Errorf("result error %v must chain context.Canceled — the abandonment marker routing the settle to `interrupted`", results[0].Error)
		}
		if n := rec.launchCount("d0"); n != 1 {
			t.Errorf("SubAgentLaunch count = %d, want exactly 1 (no launch into a canceled context)", n)
		}
		if st, ok := retryLedgerStatus(t, bb, "d0"); !ok || st != units.UnitStatusInterrupted {
			t.Errorf("ledger status = %q (found=%v), want interrupted — a cancellation landing in the backoff window is an abandonment, never a failed settle", st, ok)
		}
	})
}

// TestWaitDelegationRetryBackoff_CancelReturnsFalse pins the backoff seam's
// cancellation contract directly: an already-canceled context answers false
// immediately (the timer never wins).
func TestWaitDelegationRetryBackoff_CancelReturnsFalse(t *testing.T) {
	l := &conductorLauncher{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if l.waitDelegationRetryBackoff(ctx) {
		t.Fatal("backoff on a canceled context = true, want false")
	}
}

// ---------------------------------------------------------------------------
// runRedelegBlocking: the same single relaunch on the redelegation path
// ---------------------------------------------------------------------------

// TestRunRedelegBlocking_TransientFailureRelaunchesOnce proves the redeleg
// path shares the seam: one transient failure, one re-launch (fresh child
// registry), completion — and the ledger sequence stays running→completed.
func TestRunRedelegBlocking_TransientFailureRelaunchesOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		caller := &mockLLMCaller{callFn: func(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
			if calls.Add(1) == 1 {
				return nil, transientRateLimit()
			}
			return executorFinishResponse("recovered"), nil
		}}
		bb := newRetryLedgerBB("task-retry-redeleg", "d0")
		seedRetryUnit(t, bb, "d0")
		rec := &launchRecorder{}
		l := newRetryTestLauncher(bb, caller, rec)
		l.deps.maxRedelegDepth = 1

		registry := tools.NewDelegationRegistry()
		if err := registry.Register("d0", "s", nil, "blocking"); err != nil {
			t.Fatalf("register: %v", err)
		}
		result := l.runRedelegBlocking(limitTestCtx(), tools.DelegationTask{ID: "d0", Summary: "s", Task: "t", AllowRedelegate: true}, registry)

		if result.Status != tools.DelegationStatusCompleted || result.Error != nil {
			t.Fatalf("result = %v/%v, want completed with no error", result.Status, result.Error)
		}
		if !strings.HasPrefix(result.Output, "> [auto-retry]") {
			t.Errorf("result output %q does not note the auto-retried leg", result.Output)
		}
		if n := rec.launchCount("d0"); n != 2 {
			t.Errorf("SubAgentLaunch count = %d, want exactly 2 (one re-launch)", n)
		}
		if st, ok := retryLedgerStatus(t, bb, "d0"); !ok || st != units.UnitStatusCompleted {
			t.Errorf("ledger status = %q (found=%v), want completed", st, ok)
		}
		assertSettlementSequence(t, bb, "d0", []units.UnitStatus{units.UnitStatusPending, units.UnitStatusRunning, units.UnitStatusCompleted})
	})
}

// TestRunRedelegBlocking_RetryAbandonsFirstAttemptChildren pins the
// abandonment seam: the first attempt's async children never outlive it. A
// transient failure after an async child delegation cancels the abandoned
// generation (running children cancelled + waited for, pending ones folded
// directly) so the durable ledger records them `interrupted` BEFORE the
// retry leg's fresh generation can register anything — no parallel
// duplicate work, no non-terminal first-generation records racing the retry
// (delegation.md).
func TestRunRedelegBlocking_RetryAbandonsFirstAttemptChildren(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		var planted *tools.DelegationRegistry
		bb := newRetryLedgerBB("task-retry-abandon", "d0")
		seedRetryUnit(t, bb, "d0")
		rec := &launchRecorder{}
		l := newRetryTestLauncher(bb, nil, rec)
		// The task store wires the child spec sink (ledger Begin at register)
		// exactly as production wiring does.
		l.deps.taskStore = &specRecordingStore{}
		l.deps.maxRedelegDepth = 1
		caller := &mockLLMCaller{callFn: func(ctx context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
			switch calls.Add(1) {
			case 1:
				// First attempt, first turn: plant the abandoned generation —
				// what a real allow_redelegate subagent's async delegate call
				// produces — into THIS leg's child registry, then die
				// transiently.
				childReg := tools.DelegationRegistryFrom(ctx)
				if childReg == nil {
					t.Error("the redeleg leg's task context carries no child registry")
					return nil, transientRateLimit()
				}
				planted = childReg
				for _, id := range []string{"child_1", "child_2"} {
					if err := childReg.RegisterTask(tools.DelegationTask{ID: id, Summary: "planted", Task: "plant " + id, Mode: "async"}); err != nil {
						t.Errorf("register %s: %v", id, err)
					}
				}
				cctx, cancel := context.WithCancel(ctx)
				childReg.Start("child_1", cancel) // running async child — a settling goroutine exists
				go func() {
					<-cctx.Done()
					// launchAsync's settlement shape: the registry write, then
					// the durable settle.
					childReg.Complete("child_1", "", cctx.Err(), nil)
					l.persistUnitOutcome("child_1", "", cctx.Err(), nil)
				}()
				// child_2 stays pending (registered, never started — the
				// RegisterTask→Start window): no goroutine will ever settle it.
				return nil, transientRateLimit()
			case 2:
				// The retry leg's FIRST turn: the abandoned generation must
				// already be folded — cancelled in the registry and
				// `interrupted` in the durable ledger — strictly before this
				// fresh generation could register anything.
				if planted != nil {
					for _, id := range []string{"child_1", "child_2"} {
						if d := planted.Get(id); d == nil || d.Status != tools.DelegationStatusCancelled {
							t.Errorf("%s registry status = %v, want cancelled before the retry leg's first turn", id, d)
						}
						if st, ok := retryLedgerStatus(t, bb, id); !ok || st != units.UnitStatusInterrupted {
							t.Errorf("%s ledger status = %q (found=%v), want interrupted before the retry leg's first turn", id, st, ok)
						}
					}
				}
				return executorFinishResponse("recovered"), nil
			default:
				return executorFinishResponse("unreached"), nil
			}
		}}
		l.deps.llm = caller

		registry := tools.NewDelegationRegistry()
		if err := registry.Register("d0", "s", nil, "blocking"); err != nil {
			t.Fatalf("register: %v", err)
		}
		result := l.runRedelegBlocking(limitTestCtx(), tools.DelegationTask{ID: "d0", Summary: "s", Task: "t", AllowRedelegate: true}, registry)

		if result.Status != tools.DelegationStatusCompleted || result.Error != nil {
			t.Fatalf("result = %v/%v, want completed with no error (one re-launch heals the transient failure)", result.Status, result.Error)
		}
		if n := calls.Load(); n != 2 {
			t.Errorf("LLM calls = %d, want exactly 2 (failed leg + one re-launch)", n)
		}
		if n := rec.launchCount("d0"); n != 2 {
			t.Errorf("SubAgentLaunch count = %d, want exactly 2 (one re-launch)", n)
		}
		// The abandoned generation stays folded after the run.
		for _, id := range []string{"child_1", "child_2"} {
			if st, ok := retryLedgerStatus(t, bb, id); !ok || st != units.UnitStatusInterrupted {
				t.Errorf("%s final ledger status = %q (found=%v), want interrupted", id, st, ok)
			}
		}
		assertSettlementSequence(t, bb, "d0", []units.UnitStatus{units.UnitStatusPending, units.UnitStatusRunning, units.UnitStatusCompleted})
	})
}

// ---------------------------------------------------------------------------
// shared assertion
// ---------------------------------------------------------------------------

// assertSettlementSequence asserts the durable settlement sequence of one
// unit, collapsed to status TRANSITIONS (a Checkpoint save carries the
// record's pre-settle status, so raw SaveUnit records repeat the running
// status). The ADR-048 invariant proof: a retried leg's first failure never
// reaches the ledger — no failed→running transition exists.
func assertSettlementSequence(t *testing.T, bb *retryLedgerBB, id string, want []units.UnitStatus) {
	t.Helper()
	raw := bb.us.recordedStatuses()
	got := make([]units.UnitStatus, 0, len(raw))
	for i, st := range raw {
		if i == 0 || st != raw[i-1] {
			got = append(got, st)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("settlement sequence = %v (raw %v), want %v", got, raw, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("settlement sequence = %v (raw %v), want %v (position %d differs: the ledger observed %s where %s was required)",
				got, raw, want, i, got[i], want[i])
		}
	}
}
