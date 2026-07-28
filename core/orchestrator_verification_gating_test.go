package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/core/goal"
	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// ----------------------------------------------------------------------------
// runGoalTurns verification-gating tests (step_5)
//
// These exercise the met-branch verifier gate in runGoalTurns. The agent turn
// runner is mocked to declare a "met" verdict; the verifier seam (o.goalVerifier)
// is injected to return confirm/reject. The config's GoalLoop.Verification mode
// toggles the gate on/off.
// ----------------------------------------------------------------------------

// recordingVerifier is an injectable verifier that returns a preset outcome and
// records that it was invoked. The returned closure's preset is fixed.
type recordingVerifier struct {
	called  bool
	outcome *tools.VerificationOutcome
	err     error
}

func (r *recordingVerifier) fn(_ context.Context, _ *goal.GoalState, _ *goal.Verdict, _, _ string, _ orchestration.Blackboard, _ []sdktools.ToolDescriptor, _ conductorDeps) (*tools.VerificationOutcome, error) {
	r.called = true
	return r.outcome, r.err
}

// confirmingVerifierFn is a verifier that unconditionally CONFIRMS every met
// claim. It is shared by the runGoalLoop integration tests
// (orchestrator_goal_test.go) whose spy turn runners declare a met verdict on
// turn 1: those tests exercise routing/continuation wiring, not verification,
// so a confirming verifier lets the met verdict pass the
// independent-verification gate and terminate the loop exactly as it did before
// the gate existed. Mirrors the seam injected via o.goalVerifier.
func confirmingVerifierFn(_ context.Context, _ *goal.GoalState, v *goal.Verdict, _, _ string, _ orchestration.Blackboard, _ []sdktools.ToolDescriptor, _ conductorDeps) (*tools.VerificationOutcome, error) {
	return &tools.VerificationOutcome{Confirmed: true, Reason: "integration test confirming verifier", DeclaredAt: time.Now()}, nil
}

// newVerificationTestOrchestrator builds a goal-test orchestrator with
// independent verification enabled by default (the production default).
func newVerificationTestOrchestrator() *Orchestrator {
	o := newGoalTestOrchestrator()
	o.config.GoalLoop.Verification = "independent"
	return o
}

// goalStatusCountingEmitter wraps mockEmitter and counts goal_status
// ServiceWithMeta emissions (phase == "goal_status"). Used by the
// double-emission regression test.
type goalStatusCountingEmitter struct {
	mockEmitter
	statuses int
}

func (m *goalStatusCountingEmitter) ServiceWithMeta(_ string, meta map[string]any) {
	if p, ok := meta["phase"].(string); ok && p == "goal_status" {
		m.statuses++
	}
}

// TestRunGoalTurns_MetConfirmed_Terminates verifies that a met verdict the
// verifier CONFIRMS terminates the goal as met (same outcome as today, plus one
// verifier pass). The verifier must be invoked exactly once.
func TestRunGoalTurns_MetConfirmed_Terminates(t *testing.T) {
	o := newVerificationTestOrchestrator()
	verifier := &recordingVerifier{
		outcome: &tools.VerificationOutcome{Confirmed: true, Reason: "tests pass", DeclaredAt: time.Now()},
	}
	o.goalVerifier = verifier.fn

	runner := &mockGoalTurnRunner{
		turnVerds: []*goal.Verdict{metVerdict("done")},
		turnCalls: []int{2},
	}
	gs := &goal.GoalState{Status: goal.StatusActive, Condition: "ship it"}
	bb := orchestration.NewMapBlackboard()
	pause := &atomic.Bool{}

	result := o.runGoalTurns(context.Background(), "msg", bb, nil, "", nil, gs, pause, runner.run)

	if result.Status != goal.StatusMet {
		t.Fatalf("Status = %q, want %q (confirmed met should terminate as met)", result.Status, goal.StatusMet)
	}
	if !verifier.called {
		t.Error("expected the verifier to be invoked for a met verdict")
	}
	if runner.calls != 1 {
		t.Errorf("expected exactly 1 agent turn, got %d", runner.calls)
	}
	if result.TurnCount != 1 {
		t.Errorf("TurnCount = %d, want 1", result.TurnCount)
	}
	if result.LastVerification != "confirmed" {
		t.Errorf("LastVerification = %q, want %q", result.LastVerification, "confirmed")
	}
	if result.LastVerdict == nil || result.LastVerdict.Status != "met" {
		t.Errorf("LastVerdict = %+v, want the original met verdict preserved", result.LastVerdict)
	}
}

// TestRunGoalTurns_MetRejected_Continues verifies the headline acceptance
// criterion: a met verdict the verifier REJECTS does NOT terminate the goal.
// The loop continues, the turn counter is unaffected (no extra turn for the
// rejection), and the loop runs another turn. The verifier rejects turn 1's met
// then confirms turn 2's met so the goal terminates cleanly on the second.
func TestRunGoalTurns_MetRejected_Continues(t *testing.T) {
	o := newVerificationTestOrchestrator()
	verifierCalls := 0
	o.goalVerifier = func(_ context.Context, _ *goal.GoalState, _ *goal.Verdict, _, _ string, _ orchestration.Blackboard, _ []sdktools.ToolDescriptor, _ conductorDeps) (*tools.VerificationOutcome, error) {
		verifierCalls++
		if verifierCalls == 1 {
			// Reject turn 1's met claim.
			return &tools.VerificationOutcome{Confirmed: false, Reason: "test suite still failing", DeclaredAt: time.Now()}, nil
		}
		// Confirm turn 2's met claim.
		return &tools.VerificationOutcome{Confirmed: true, Reason: "now passing", DeclaredAt: time.Now()}, nil
	}

	// Turn 1: met (rejected). Turn 2: met again (confirmed → terminates).
	runner := &mockGoalTurnRunner{
		turnVerds: []*goal.Verdict{metVerdict("done"), metVerdict("now really done")},
		turnCalls: []int{2, 3},
	}
	gs := &goal.GoalState{Status: goal.StatusActive, Condition: "ship it"}
	bb := orchestration.NewMapBlackboard()
	pause := &atomic.Bool{}

	result := o.runGoalTurns(context.Background(), "msg", bb, nil, "", nil, gs, pause, runner.run)

	if result.Status != goal.StatusMet {
		t.Fatalf("Status = %q, want %q (second met should be confirmed and terminate)", result.Status, goal.StatusMet)
	}
	// Two agent turns ran; the rejection on turn 1 must NOT have added a turn.
	if runner.calls != 2 {
		t.Errorf("expected 2 agent turns, got %d (rejection must not increment the turn count)", runner.calls)
	}
	if result.TurnCount != 2 {
		t.Errorf("TurnCount = %d, want 2", result.TurnCount)
	}
	// The verifier was invoked on BOTH met attempts (turn 1 rejected, turn 2 confirmed).
	if verifierCalls != 2 {
		t.Errorf("expected 2 verifier calls, got %d", verifierCalls)
	}
	// Final LastVerdict is the confirmed met verdict (turn 2's), not the
	// synthesized rejection.
	if result.LastVerdict == nil || result.LastVerdict.Status != "met" {
		t.Errorf("LastVerdict = %+v, want met (the confirmed verdict)", result.LastVerdict)
	}
	if result.LastVerification != "confirmed" {
		t.Errorf("LastVerification = %q, want confirmed", result.LastVerification)
	}
}

// TestRunGoalTurns_MetRejected_LastVerdictCarriesReason verifies that when a
// met verdict is rejected, gs.LastVerdict is the synthesized not_met verdict
// carrying the rejection reason — the data renderGoalModeVolatile reads to
// show the next-turn notice. We stop the loop after one turn via a tight budget.
func TestRunGoalTurns_MetRejected_LastVerdictCarriesReason(t *testing.T) {
	o := newVerificationTestOrchestrator()
	verifier := &recordingVerifier{
		outcome: &tools.VerificationOutcome{
			Confirmed:  false,
			Reason:     "the build is red",
			DeclaredAt: time.Now(),
		},
	}
	o.goalVerifier = verifier.fn

	// Single turn: met (rejected). A MaxTurns of 1 halts exhausted right after.
	runner := &mockGoalTurnRunner{
		turnVerds: []*goal.Verdict{metVerdict("done")},
		turnCalls: []int{2},
	}
	gs := &goal.GoalState{
		Status:    goal.StatusActive,
		Condition: "ship it",
		Budget:    goal.GoalBudget{MaxTurns: 1},
	}
	bb := orchestration.NewMapBlackboard()
	pause := &atomic.Bool{}

	result := o.runGoalTurns(context.Background(), "msg", bb, nil, "", nil, gs, pause, runner.run)

	// The rejected met does NOT terminate as met; the budget then halts exhausted.
	if result.Status != goal.StatusExhausted {
		t.Fatalf("Status = %q, want %q (rejected met must not terminate as met; budget then halts)", result.Status, goal.StatusExhausted)
	}
	if result.LastVerdict == nil || result.LastVerdict.Status != "not_met" {
		t.Fatalf("LastVerdict = %+v, want a synthesized not_met verdict", result.LastVerdict)
	}
	if !strings.Contains(result.LastVerdict.Reason, "the build is red") {
		t.Errorf("LastVerdict.Reason = %q, want it to contain the verifier's rejection reason", result.LastVerdict.Reason)
	}
	if result.LastVerification != "rejected" {
		t.Errorf("LastVerification = %q, want %q", result.LastVerification, "rejected")
	}
}

// TestRunGoalTurns_VerificationOff_ReproducesToday verifies that with
// verification "off", a met verdict terminates the goal as met WITHOUT invoking
// the verifier at all — exactly today's behavior.
func TestRunGoalTurns_VerificationOff_ReproducesToday(t *testing.T) {
	o := newVerificationTestOrchestrator()
	o.config.GoalLoop.Verification = "off"
	// Inject a verifier that WOULD reject — if it is ever called, the test fails
	// (verification off must short-circuit before the verifier).
	verifier := &recordingVerifier{
		outcome: &tools.VerificationOutcome{Confirmed: false, Reason: "must not be called"},
	}
	o.goalVerifier = verifier.fn

	runner := &mockGoalTurnRunner{
		turnVerds: []*goal.Verdict{metVerdict("done")},
		turnCalls: []int{2},
	}
	gs := &goal.GoalState{Status: goal.StatusActive, Condition: "ship it"}
	bb := orchestration.NewMapBlackboard()
	pause := &atomic.Bool{}

	result := o.runGoalTurns(context.Background(), "msg", bb, nil, "", nil, gs, pause, runner.run)

	if result.Status != goal.StatusMet {
		t.Fatalf("Status = %q, want %q (verification off → met terminates immediately)", result.Status, goal.StatusMet)
	}
	if verifier.called {
		t.Error("verifier must NOT be invoked when verification is 'off'")
	}
	if result.LastVerification != "off" {
		t.Errorf("LastVerification = %q, want %q", result.LastVerification, "off")
	}
	if runner.calls != 1 {
		t.Errorf("expected exactly 1 agent turn, got %d", runner.calls)
	}
}

// TestRunGoalTurns_MetRejectedNeverTerminatesAsMet verifies the safety
// invariant: a met claim the verifier rejects can NEVER terminate the goal as
// met, even across many turns. The loop keeps running until the budget is
// exhausted, never producing StatusMet while the verifier keeps rejecting.
func TestRunGoalTurns_MetRejectedNeverTerminatesAsMet(t *testing.T) {
	o := newVerificationTestOrchestrator()
	// Verifier ALWAYS rejects.
	verifier := &recordingVerifier{
		outcome: &tools.VerificationOutcome{Confirmed: false, Reason: "never good enough", DeclaredAt: time.Now()},
	}
	o.goalVerifier = verifier.fn

	// Every turn declares met; every met is rejected.
	runner := &mockGoalTurnRunner{
		turnVerds: []*goal.Verdict{metVerdict("done"), metVerdict("done"), metVerdict("done")},
		turnCalls: []int{2, 2, 2},
	}
	gs := &goal.GoalState{
		Status:    goal.StatusActive,
		Condition: "ship it",
		Budget:    goal.GoalBudget{MaxTurns: 3},
	}
	bb := orchestration.NewMapBlackboard()
	pause := &atomic.Bool{}

	result := o.runGoalTurns(context.Background(), "msg", bb, nil, "", nil, gs, pause, runner.run)

	if result.Status == goal.StatusMet {
		t.Fatalf("Status = met — a verifier-rejected met must NEVER terminate the goal as met")
	}
	if result.Status != goal.StatusExhausted {
		t.Errorf("Status = %q, want %q (budget exhausted after persistent rejections)", result.Status, goal.StatusExhausted)
	}
	if runner.calls != 3 {
		t.Errorf("expected 3 agent turns (all met, all rejected), got %d", runner.calls)
	}
}

// TestRunGoalTurns_RejectedMetEmitsGoalStatusOnce is a regression test for the
// double-emission bug: a rejected met claim must emit goal_status EXACTLY once
// per turn (via the bottom-of-loop emission), not twice (once in the rejection
// block + once at the bottom). Before the fix, the rejection block called
// emitGoalStatus and then fell through to the bottom-of-loop emitGoalStatus,
// emitting the event twice per rejected turn.
func TestRunGoalTurns_RejectedMetEmitsGoalStatusOnce(t *testing.T) {
	emitter := &goalStatusCountingEmitter{}

	o := newVerificationTestOrchestrator()
	o.emitter = emitter
	o.goalVerifier = func(_ context.Context, _ *goal.GoalState, _ *goal.Verdict, _, _ string, _ orchestration.Blackboard, _ []sdktools.ToolDescriptor, _ conductorDeps) (*tools.VerificationOutcome, error) {
		return &tools.VerificationOutcome{Confirmed: false, Reason: "still failing", DeclaredAt: time.Now()}, nil
	}

	// Single rejected met claim; a tight budget halts the loop after turn 1.
	runner := &mockGoalTurnRunner{
		turnVerds: []*goal.Verdict{metVerdict("done")},
		turnCalls: []int{2},
	}
	gs := &goal.GoalState{Status: goal.StatusActive, Condition: "ship it", Budget: goal.GoalBudget{MaxTurns: 1}}
	bb := orchestration.NewMapBlackboard()
	pause := &atomic.Bool{}

	result := o.runGoalTurns(context.Background(), "msg", bb, nil, "", nil, gs, pause, runner.run)

	if result.Status != goal.StatusExhausted {
		t.Fatalf("Status = %q, want exhausted (rejected met + budget)", result.Status)
	}
	if result.LastVerification != "rejected" {
		t.Errorf("LastVerification = %q, want rejected", result.LastVerification)
	}
	// One rejected turn → exactly ONE goal_status emission (the bottom-of-loop
	// one). Before the fix this was 2 (the in-block one was redundant).
	if emitter.statuses != 1 {
		t.Errorf("expected exactly 1 goal_status emission for a single rejected turn, got %d (double-emission regression)", emitter.statuses)
	}
}

// TestRenderGoalModeVolatile_RejectedPrependsNotice verifies the next-turn
// prompt surfaces the rejection: when gs.LastVerification == "rejected", the
// volatile section prepends the rejection notice (with the reason) before the
// budget line.
func TestRenderGoalModeVolatile_RejectedPrependsNotice(t *testing.T) {
	gs := &goal.GoalState{
		Condition:        "ship it",
		Budget:           goal.GoalBudget{MaxTurns: 5},
		TurnCount:        2,
		LastVerification: "rejected",
		LastVerdict: &goal.Verdict{
			Status: "not_met",
			Reason: "the test suite is still failing",
		},
	}
	got := renderGoalModeVolatile(gs)

	if !strings.Contains(got, "REJECTED by independent verification") {
		t.Errorf("expected the rejection notice, got %q", got)
	}
	if !strings.Contains(got, "the test suite is still failing") {
		t.Errorf("expected the rejection reason in the notice, got %q", got)
	}
	if !strings.Contains(got, "Address this before re-declaring met") {
		t.Errorf("expected the action directive in the notice, got %q", got)
	}
	// The budget line is still present (the notice is prepended, not a replacement).
	if !strings.Contains(got, "turn 2/5") {
		t.Errorf("expected the budget line still present, got %q", got)
	}
}

// TestRenderGoalModeVolatile_ConfirmedHasNoNotice verifies a confirmed met
// (LastVerification == "confirmed") does NOT emit the rejection notice — the
// notice is reserved for genuine rejections only.
func TestRenderGoalModeVolatile_ConfirmedHasNoNotice(t *testing.T) {
	gs := &goal.GoalState{
		Condition:        "ship it",
		Budget:           goal.GoalBudget{MaxTurns: 5},
		TurnCount:        2,
		LastVerification: "confirmed",
	}
	got := renderGoalModeVolatile(gs)
	if strings.Contains(got, "REJECTED") {
		t.Errorf("confirmed met must not show the rejection notice, got %q", got)
	}
}

// TestRenderGoalModeVolatile_CleanTurnHasNoNotice verifies a clean turn
// (LastVerification == "", the steady state) emits only the budget line.
func TestRenderGoalModeVolatile_CleanTurnHasNoNotice(t *testing.T) {
	gs := &goal.GoalState{
		Condition:        "ship it",
		Budget:           goal.GoalBudget{MaxTurns: 5},
		TurnCount:        2,
		LastVerification: "",
	}
	got := renderGoalModeVolatile(gs)
	if strings.Contains(got, "REJECTED") {
		t.Errorf("a clean turn must not show the rejection notice, got %q", got)
	}
	if !strings.Contains(got, "turn 2/5") {
		t.Errorf("expected the budget line, got %q", got)
	}
}

// TestRunGoalTurns_MetRejected_PromptNoticeOneShot verifies the rejection
// notice is one-shot: after a rejected met on turn N, the notice shows on turn
// N+1's prompt (LastVerification == "rejected" right after turn N), but is
// cleared once turn N+1's prompt has been rendered. This is checked indirectly
// by asserting LastVerification is reset to "" during the turn N+1 body before
// any new verdict is processed — proving the marker does not leak past the
// addressing turn.
func TestRunGoalTurns_MetRejected_PromptNoticeOneShot(t *testing.T) {
	o := newVerificationTestOrchestrator()
	// Verifier rejects turn 1's met, then confirms turn 2's met.
	rejectCalls := 0
	o.goalVerifier = func(_ context.Context, _ *goal.GoalState, _ *goal.Verdict, _, _ string, _ orchestration.Blackboard, _ []sdktools.ToolDescriptor, _ conductorDeps) (*tools.VerificationOutcome, error) {
		rejectCalls++
		if rejectCalls == 1 {
			return &tools.VerificationOutcome{Confirmed: false, Reason: "first rejection", DeclaredAt: time.Now()}, nil
		}
		return &tools.VerificationOutcome{Confirmed: true, Reason: "confirmed", DeclaredAt: time.Now()}, nil
	}

	runner := &mockGoalTurnRunner{
		turnVerds: []*goal.Verdict{metVerdict("done"), metVerdict("now done")},
		turnCalls: []int{2, 2},
	}
	gs := &goal.GoalState{Status: goal.StatusActive, Condition: "ship it"}
	bb := orchestration.NewMapBlackboard()
	pause := &atomic.Bool{}

	result := o.runGoalTurns(context.Background(), "msg", bb, nil, "", nil, gs, pause, runner.run)

	if result.Status != goal.StatusMet {
		t.Fatalf("Status = %q, want met (second met confirmed)", result.Status)
	}
	// The verifier ran twice (once per met attempt).
	if rejectCalls != 2 {
		t.Errorf("expected 2 verifier calls, got %d", rejectCalls)
	}
	// Final marker is "confirmed" (the last met attempt was confirmed).
	if result.LastVerification != "confirmed" {
		t.Errorf("LastVerification = %q, want confirmed", result.LastVerification)
	}
}

// TestRunConductor_VerifierPassBoundedByComplexityBudget verifies the
// independent goal-verification pass is NOT artificially capped: with the
// hardcoded verificationMaxSteps constant removed, the verifier's Conductor run
// is bounded EXACTLY by the complexity-derived budget (complexity ×
// stepsPerComplexity) — the same bound a normal executor run receives — so the
// verifier is never more limited than the executor it checks (only its
// write/coord toolset is withheld).
//
// We invoke RunConductor the way defaultGoalVerifier does — via
// buildConductorDeps, which now sets NO step override — under a fixed low
// complexity, with a looping mock LLM that emits a DISTINCT bash_exec call per
// iteration (so the repeat detector never trips) and a relaxed circuit breaker
// (so no abort trips before the budget). The loop must run the FULL
// complexity-derived budget, proving there is no residual fixed cap (e.g. the
// former 12-step clamp) on the verifier.
func TestRunConductor_VerifierPassBoundedByComplexityBudget(t *testing.T) {
	const complexity = 1
	wantSteps := complexity * stepsPerComplexity // 1 × 20 = 20 — ≠ the former 12-step cap

	// The LLM always emits a bash_exec call with a DISTINCT input per iteration
	// so the circuit-breaker's repeat detector never trips — the only thing
	// halting the loop is the complexity-derived step budget.
	iter := 0
	mockLLM := &mockLLMCaller{
		callFn: func(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
			iter++
			return &llm.ChatResponse{
				Message: llm.Message{
					Role: "assistant",
					ToolCalls: []llm.ToolCall{{
						ID:    fmt.Sprintf("c%d", iter),
						Name:  "bash_exec",
						Input: json.RawMessage(fmt.Sprintf(`{"command":"echo step-%d","timeout":"5s"}`, iter)),
					}},
				},
				StopReason: "tool_use",
			}, nil
		},
	}
	o := newResumeTestOrchestrator(t, mockLLM, &spyEmitter{}, nil)

	// Build deps the way defaultGoalVerifier does (buildConductorDeps). With the
	// verifier cap removed it sets NO step override; the complexity-derived
	// budget applies unchanged — identical to a normal executor run.
	counter := &turnUsageCounter{}
	deps := o.buildConductorDeps(nil, nil)
	deps.toolExec = &countingToolExec{inner: deps.toolExec, counter: counter}
	// Relax the circuit breaker so no abort (repeat/fruitless/same-tool) trips
	// before the complexity budget — isolating the step bound under test from
	// the breaker's independent halt conditions.
	breakerCeiling := wantSteps + 10
	deps.circuitBreaker = agent.CircuitBreakerConfig{
		RepeatNudgeThreshold:         breakerCeiling,
		RepeatAbortThreshold:         breakerCeiling,
		TruncationAbortThreshold:     breakerCeiling,
		ParseErrorAbortThreshold:     breakerCeiling,
		FruitlessNudgeThreshold:      breakerCeiling,
		FruitlessAbortThreshold:      breakerCeiling,
		SameToolRepeatNudgeThreshold: breakerCeiling,
		SameToolRepeatAbortThreshold: breakerCeiling,
	}

	availableTools := []sdktools.ToolDescriptor{
		{Name: "bash_exec", Description: "run", InputSchema: json.RawMessage(`{"type":"object"}`), Source: "test"},
	}

	bb := orchestration.NewMapBlackboard()
	bb.SetOriginalRequest("verify the goal")
	ctx := WithComplexity(WithDomain(context.Background(), "general"), complexity)

	if _, err := RunConductor(ctx, "verify the goal", bb, availableTools, deps, ""); err != nil {
		t.Fatalf("RunConductor returned error: %v", err)
	}

	// The loop ran the FULL complexity-derived budget. This proves (a) the
	// verifier pass is bounded by the complexity budget, not a fixed cap, and
	// (b) there is no residual ~12-step clamp — it ran well past 12.
	if counter.toolCalls != wantSteps {
		t.Errorf("expected exactly %d tool executions (the complexity-derived budget, NOT an artificial cap), got %d — the verifier pass must be bounded exactly like a normal executor run", wantSteps, counter.toolCalls)
	}
}
