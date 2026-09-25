package core

// Recovery-contract regression suite.
//
// The three reported recovery defects are manifestations of ONE contract, so
// they are pinned here together as the scenario-level regression home for that
// contract (see ADR-048, specs/decisions/048-unified-recovery-ledger.md):
//
//   1. an interrupted delegate (abandoned by a crash/app exit) is relaunched on
//      Resume — never marked failed and dropped — and the durable ledger stays
//      the authoritative record of its status;
//   2. a cooperative pause inside the goal verifier SUSPENDS the request
//      (goal stays active → ExecutionStatusPaused) instead of synthesizing a
//      not_met rejection from the interrupted verification;
//   3. an errored goal turn surfaces as a RESUMABLE failure (the goal stays
//      active, the cause is preserved, the task maps to ExecutionStatusFailed)
//      — never blocked_idle / "partial" — and a later Resume recovers it.
//
// Each test drives the real production entry point (Resume, resumeGoalLoop, or
// runGoalTurns) with the shared harnesses, so it would fail if the contract
// regressed.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/core/goal"
	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/c0wrk/core/units"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/agent/router"
	"github.com/v0lka/sp4rk/orchestration"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// ledgerUnitStatus reads the durable status of one unit from the ledger the
// resume path itself uses (the blackboard's cached ledger), across namespaces.
func ledgerUnitStatus(t *testing.T, bb *unitLedgerBB, id string) (units.UnitStatus, bool) {
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

// TestRecoveryContract_InterruptedDelegateRelaunch pins scenario 1: a delegate
// whose unit is durably `interrupted` (a crash/app exit left it in flight, with
// no resume checkpoint) is RELAUNCHED FRESH by Resume — the former
// "interrupted ⇒ mark failed, never relaunch" behavior is gone — and the ledger
// stays the authoritative record, settling the unit completed.
func TestRecoveryContract_InterruptedDelegateRelaunch(t *testing.T) {
	caller := &pauseScriptLLM{script: []pauseScriptStep{
		// The resume wave: the interrupted del_1 is relaunched and finishes.
		{respond: executorFinishResponse("del_1 done")},
		// The resumed conductor's only LLM call: finish.
		{respond: executorFinishResponse("all done")},
	}}
	o, emitter, _, recStore := newFunnelOrchestrator(t, caller)

	bb := newUnitLedgerBB("task-recovery-interrupted", recStore)
	bb.SetOriginalRequest("do the delegated work")
	// A delegation abandoned mid-flight: the durable ledger holds it
	// `interrupted` with no checkpoint.
	seedLedgerUnit(t, bb, "", "del_1", units.UnitKindSubagent, units.UnitStatusInterrupted, "", 0,
		tools.DelegationTask{ID: "del_1", Summary: "s", Task: "do work"}, nil)

	if st, ok := ledgerUnitStatus(t, bb, "del_1"); !ok || st != units.UnitStatusInterrupted {
		t.Fatalf("ledger before resume: del_1 status = %q (found=%v), want %q", st, ok, units.UnitStatusInterrupted)
	}

	ctx := WithComplexity(WithDomain(context.Background(), "general"), 1)
	res, err := o.Resume(ctx, bb, nil, t.TempDir(), nil, nil, "")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if res.Status != orchestration.ExecutionStatusSuccess {
		t.Fatalf("Resume status = %q, want success", res.Status)
	}
	if n := emitter.launchCount("del_1"); n != 1 {
		t.Errorf("SubAgentLaunch for the interrupted del_1 = %d, want 1 (relaunched fresh, not marked failed)", n)
	}
	if sr, ok := bb.GetStepResult("del_1"); !ok || sr.Error != nil || sr.FullOutput != "del_1 done" {
		t.Fatalf("del_1 after resume = %+v (ok=%v), want completed with output", sr, ok)
	}
	// The durable ledger follows the relaunch: the recovered unit is settled.
	if st, ok := ledgerUnitStatus(t, bb, "del_1"); !ok || st != units.UnitStatusCompleted {
		t.Errorf("ledger after resume: del_1 status = %q (found=%v), want %q", st, ok, units.UnitStatusCompleted)
	}
}

// TestRecoveryContract_VerifierPauseSuspendsInsteadOfRejecting pins scenario 2:
// a cooperative pause tripping inside the goal verifier's isolated pass reports
// the pause sentinel and NO verdict; the loop suspends the request (paused=true,
// goal active) rather than synthesizing a not_met rejection, and the mapped task
// outcome is a PAUSE (session_paused), not a failed/rejected goal.
func TestRecoveryContract_VerifierPauseSuspendsInsteadOfRejecting(t *testing.T) {
	o := newVerificationTestOrchestrator()
	verifierCalls := 0
	o.goalVerifier = func(_ context.Context, _ *goal.GoalState, _ *goal.Verdict, _, _ string, _ orchestration.Blackboard, _ []sdktools.ToolDescriptor, _ conductorDeps) (*tools.VerificationOutcome, error) {
		verifierCalls++
		// The pass was interrupted: no confirm, no reject.
		return nil, agent.ErrPaused
	}

	runner := &mockGoalTurnRunner{
		turnVerds: []*goal.Verdict{metVerdict("done")},
		turnCalls: []int{2},
	}
	gs := &goal.GoalState{Status: goal.StatusActive, Condition: "ship it"}
	bb := orchestration.NewMapBlackboard()

	result, paused := o.runGoalTurns(context.Background(), "msg", bb, nil, "", nil, gs, runner.run)

	if !paused {
		t.Fatal("paused = false, want true (a cooperative verifier pause must suspend the request)")
	}
	if result.Status != goal.StatusActive {
		t.Fatalf("goal status = %q, want %q (never terminalized on a verifier pause)", result.Status, goal.StatusActive)
	}
	if result.LastVerdict != nil && result.LastVerdict.Status == "not_met" {
		t.Fatalf("LastVerdict = %+v, want NO synthesized not_met from a paused verification", result.LastVerdict)
	}
	if result.LastVerification == "rejected" {
		t.Errorf("LastVerification = %q, want it NOT to be a rejection", result.LastVerification)
	}
	if verifierCalls != 1 {
		t.Errorf("verifier passes = %d, want 1", verifierCalls)
	}
	if runner.calls != 1 {
		t.Errorf("agent turns = %d, want 1 (a paused verification runs no extra turn)", runner.calls)
	}
	// The contract maps a verifier pause to a resumable PAUSE task outcome.
	mapped := o.goalLoopResult("", bb, nil, result.Status, gs.Condition, paused, result.LastErrorTyped)
	if mapped.Status != orchestration.ExecutionStatusPaused {
		t.Fatalf("mapped task status = %q, want %q (session_paused, resumable)", mapped.Status, orchestration.ExecutionStatusPaused)
	}
}

// TestRecoveryContract_GoalTurnErrorIsResumableFailure pins scenario 3: turns
// that keep erroring must surface as a RESUMABLE failure — the goal stays
// active, the cause is preserved on GoalState.LastError and in the task output,
// and the task maps to ExecutionStatusFailed (never blocked_idle / "partial") —
// and a later Resume with a healthy runner recovers the goal.
func TestRecoveryContract_GoalTurnErrorIsResumableFailure(t *testing.T) {
	o := newGoalTestOrchestrator()
	o.goalTurnRunner = (&mockGoalTurnRunner{errAllTurns: true}).run

	gs := &goal.GoalState{Condition: "ship it", Status: goal.StatusActive, CreatedAt: time.Now()}
	bb := orchestration.NewMapBlackboard()
	routing := &router.RoutingDecision{Domain: "general", Complexity: 3}

	result, err := o.resumeGoalLoop(context.Background(), "continue", bb, nil, "", routing, gs, nil, "", "")
	if err != nil {
		t.Fatalf("resumeGoalLoop: %v", err)
	}
	if result.Status != orchestration.ExecutionStatusFailed {
		t.Fatalf("result.Status = %q, want %q (a resumable failure — never partial/blocked_idle)", result.Status, orchestration.ExecutionStatusFailed)
	}
	if !strings.Contains(result.Output, "provider unavailable") {
		t.Errorf("result.Output = %q, want it to carry the turn error reason", result.Output)
	}
	if gs.Status != goal.StatusActive {
		t.Errorf("goal status = %q, want %q (non-terminal, so Resume re-enters)", gs.Status, goal.StatusActive)
	}
	if gs.LastError == "" {
		t.Error("gs.LastError is empty, want the recorded turn error")
	}

	// The turn-loop half of the contract: an errored turn is retried a bounded
	// number of times and never misclassified as an idle (blocked_idle) turn.
	runner := &mockGoalTurnRunner{errAllTurns: true}
	gs2 := &goal.GoalState{Status: goal.StatusActive, Condition: "ship it"}
	loopResult, paused := o.runGoalTurns(context.Background(), "msg", orchestration.NewMapBlackboard(), nil, "", nil, gs2, runner.run)
	if loopResult.Status != goal.StatusActive || paused {
		t.Fatalf("runGoalTurns status = %q, paused = %v, want %q / false", loopResult.Status, paused, goal.StatusActive)
	}
	if want := 1 + goalTurnMaxErrorRetries; runner.calls != want {
		t.Errorf("turn attempts = %d, want %d (bounded retry)", runner.calls, want)
	}
	mapped := o.goalLoopResult("", bb, nil, loopResult.Status, "ship it", paused, loopResult.LastErrorTyped)
	if mapped.Status != orchestration.ExecutionStatusFailed {
		t.Errorf("mapped task status = %q, want %q", mapped.Status, orchestration.ExecutionStatusFailed)
	}

	// Resume recovers: with a healthy turn runner the loop re-enters (the goal
	// was left active) and reaches "met".
	o.goalTurnRunner = (&goalSeedRecorder{}).run
	recovered, err := o.resumeGoalLoop(context.Background(), "continue", bb, nil, "", routing, gs, nil, "", "")
	if err != nil {
		t.Fatalf("second resumeGoalLoop: %v", err)
	}
	if recovered.Status != orchestration.ExecutionStatusSuccess {
		t.Errorf("recovered.Status = %q, want %q (Resume must recover the errored goal)", recovered.Status, orchestration.ExecutionStatusSuccess)
	}
}

// TestRecoveryContract_GoalTurnErrorRespectsTurnBudget pins the budget guard on
// the turn-error retry: a retry spends a real turn, so an erroring goal with a
// user-set MaxTurns must NOT run MaxTurns + goalTurnMaxErrorRetries turns. The
// loop halts at the budget with the goal left ACTIVE (still resumable).
func TestRecoveryContract_GoalTurnErrorRespectsTurnBudget(t *testing.T) {
	o := newGoalTestOrchestrator()
	runner := &mockGoalTurnRunner{errAllTurns: true}
	o.goalTurnRunner = runner.run

	gs := &goal.GoalState{
		Status: goal.StatusActive, Condition: "ship it",
		Budget: goal.GoalBudget{MaxTurns: 2},
	}
	loopResult, paused := o.runGoalTurns(context.Background(), "msg", orchestration.NewMapBlackboard(), nil, "", nil, gs, runner.run)
	if loopResult.Status != goal.StatusActive || paused {
		t.Fatalf("runGoalTurns status = %q, paused = %v, want %q / false", loopResult.Status, paused, goal.StatusActive)
	}
	if runner.calls != 2 {
		t.Errorf("turn attempts = %d, want exactly the MaxTurns budget (2) — the retry must not outrun it", runner.calls)
	}
	if gs.LastError == "" {
		t.Error("gs.LastError is empty, want the recorded turn error (the cause stays recoverable)")
	}
}

// TestRecoveryContract_GoalCancelIsNotATurnError pins the cancel classification:
// a user cancel landing after a retried turn error must NOT be reported as
// "stopped after a turn error" (the stale marker must be cleared).
func TestRecoveryContract_GoalCancelIsNotATurnError(t *testing.T) {
	o := newGoalTestOrchestrator()
	runner := &mockGoalTurnRunner{turnErrs: []error{errors.New("provider unavailable")}}
	o.goalTurnRunner = runner.run

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel as soon as the first (erroring) turn has run: the loop retries,
	// re-checks the context at the top of the next turn, and breaks as a cancel.
	runner.onTurn = func(int) { cancel() }

	gs := &goal.GoalState{Status: goal.StatusActive, Condition: "ship it"}
	loopResult, paused := o.runGoalTurns(ctx, "msg", orchestration.NewMapBlackboard(), nil, "", nil, gs, runner.run)
	if paused {
		t.Fatal("paused = true, want false (a cancel is not a pause)")
	}
	if gs.LastError != "" {
		t.Errorf("gs.LastError = %q, want it cleared on a cancel", gs.LastError)
	}
	mapped := o.goalLoopResult("", orchestration.NewMapBlackboard(), nil, loopResult.Status, gs.Condition, paused, gs.LastErrorTyped)
	if mapped.Status == orchestration.ExecutionStatusFailed {
		t.Errorf("mapped task status = %q, want a cancel/partial — never the turn-error failure", mapped.Status)
	}
}
