package core

// Resume-funnel tests: ONE funnel enumerates every non-terminal unit the task's
// durable ledger holds — all kinds, depths and namespaces, including the units
// a goal-verification pass records under its own namespace — and settles them
// uniformly:
//
//   - paused                → relaunched from its checkpoint
//   - not-started/interrupted → relaunched FRESH (the former
//     "interrupted ⇒ mark failed, never relaunch" branch is gone)
//   - terminal              → replayed, never re-run
//
// The tests drive the funnel at the Orchestrator.Resume level, seeding the
// durable ledger directly (the shape a prior run or the verifier's isolated
// sink leaves behind).

import (
	"context"
	"encoding/json"
	"testing"

	coretools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/c0wrk/core/units"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
)

// unitLedgerBB is a PersistableBlackboard whose durable unit ledger is backed
// by an in-memory units.Store, exposed through the optional UnitStore/
// UnitLedger capabilities NewBlackboardLedger consults. It is the restore-side
// shape of the production PersistentBlackboard: the resume funnel reads exactly
// the units a prior run (or an isolated context) recorded.
type unitLedgerBB struct {
	testPersistableBlackboard
	us     *memUnitStore
	ledger units.Ledger
}

func (b *unitLedgerBB) UnitStore() units.Store { return b.us }

func (b *unitLedgerBB) UnitLedger() units.Ledger {
	if b.ledger == nil {
		b.ledger = units.NewLedger(b.us, b.taskID, "")
	}
	return b.ledger
}

func newUnitLedgerBB(taskID string, store TaskPersistence) *unitLedgerBB {
	return &unitLedgerBB{
		testPersistableBlackboard: testPersistableBlackboard{
			MapBlackboard: orchestration.NewMapBlackboard(),
			taskID:        taskID,
			store:         store,
		},
		us: newMemUnitStore(),
	}
}

// seedLedgerUnit records one durable unit for the task in the given namespace,
// with the given kind/status/spec/checkpoint — exactly the shape a prior run
// (or the goal verifier's isolated sink) leaves behind. A non-empty task.id
// causes the spec to be marshaled into the record's Spec (what a relaunch needs
// to rebuild the delegation without an LLM decision).
func seedLedgerUnit(t *testing.T, b *unitLedgerBB, namespace, id string, kind units.UnitKind, status units.UnitStatus, parentID string, depth int, task coretools.DelegationTask, steps []agent.Step) {
	t.Helper()
	var spec json.RawMessage
	if task.ID != "" || task.Task != "" {
		if task.ID == "" {
			task.ID = id
		}
		raw, err := json.Marshal(coretools.DelegationSpec{
			Task:     task,
			ParentID: parentID,
			Depth:    depth,
			Kind:     coretools.DelegationKindSubagent,
		})
		if err != nil {
			t.Fatalf("marshal spec for %s: %v", id, err)
		}
		spec = raw
	}
	ledger := units.NewLedger(b.us, b.taskID, namespace)
	if err := ledger.Begin(units.UnitRecord{
		ID:       id,
		Kind:     kind,
		ParentID: parentID,
		Depth:    depth,
		Status:   status,
		Spec:     spec,
	}); err != nil {
		t.Fatalf("ledger Begin %s: %v", id, err)
	}
	if len(steps) > 0 {
		if err := ledger.Checkpoint(id, steps); err != nil {
			t.Fatalf("ledger Checkpoint %s: %v", id, err)
		}
	}
}

// newFunnelOrchestrator builds the wave orchestrator plus its emitter and
// context-manager recorder — the standard harness for the resume funnel.
func newFunnelOrchestrator(t *testing.T, caller agent.LLMCaller) (*Orchestrator, *launchRecorder, *cmRecorder, *specRecordingStore) {
	t.Helper()
	emitter := &launchRecorder{}
	rec := &cmRecorder{}
	o := newWaveOrchestrator(t, caller, emitter, rec)
	recStore := &specRecordingStore{}
	o.SetTaskStore(recStore)
	return o, emitter, rec, recStore
}

// TestResumeFunnel_InterruptedUnitRelaunchedFresh is acceptance criterion 1: a
// delegate that never took a checkpoint (abandoned mid-flight — the ledger
// holds it interrupted, with no steps) is RELAUNCHED FRESH on Resume. The former
// behavior — mark it failed and never relaunch — is gone.
func TestResumeFunnel_InterruptedUnitRelaunchedFresh(t *testing.T) {
	caller := &pauseScriptLLM{script: []pauseScriptStep{
		// Wave: the interrupted del_1 is relaunched and finishes.
		{respond: executorFinishResponse("del_1 done")},
		// The resumed conductor's only LLM call: finish.
		{respond: executorFinishResponse("all done")},
	}}
	o, emitter, _, recStore := newFunnelOrchestrator(t, caller)

	bb := newUnitLedgerBB("task-funnel-interrupted", recStore)
	bb.SetOriginalRequest("do the delegated work")
	// A delegation left in flight by a crash/app exit: interrupted, no steps.
	seedLedgerUnit(t, bb, "", "del_1", units.UnitKindSubagent, units.UnitStatusInterrupted, "", 0,
		coretools.DelegationTask{ID: "del_1", Summary: "s", Task: "do work"}, nil)

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
}

// TestResumeFunnel_VerifierPausedUnitRelaunched is acceptance criterion 2: a
// goal-verification delegate that paused — recorded ONLY in the ledger, under
// the verifier namespace and parented under the verifier unit — is relaunched.
// The verifier's container unit is scoped to its parent: it is NOT itself a
// relaunchable unit.
func TestResumeFunnel_VerifierPausedUnitRelaunched(t *testing.T) {
	checkpoint := []agent.Step{{
		Thought:     "prior",
		Action:      llm.ToolCall{ID: "c1", Name: "bash_exec", Input: json.RawMessage(`{"command":"echo hi","timeout":"5s"}`)},
		Observation: "hi",
	}}
	caller := &pauseScriptLLM{script: []pauseScriptStep{
		// Wave: the paused verifier delegate resumes from its checkpoint.
		{respond: executorFinishResponse("verifier del done")},
		// The resumed conductor's only LLM call: finish.
		{respond: executorFinishResponse("all done")},
	}}
	o, emitter, rec, recStore := newFunnelOrchestrator(t, caller)

	bb := newUnitLedgerBB("task-funnel-verifier", recStore)
	bb.SetOriginalRequest("reach the goal")
	// The verifier's isolated pass: a container unit plus a paused child
	// delegation, both namespaced and parent-linked.
	seedLedgerUnit(t, bb, verifierUnitNamespace, verifierUnitID, units.UnitKindGoalVerification, units.UnitStatusRunning, "", 0,
		coretools.DelegationTask{}, nil)
	seedLedgerUnit(t, bb, verifierUnitNamespace, "del_v", units.UnitKindSubagent, units.UnitStatusPaused, verifierUnitID, 0,
		coretools.DelegationTask{ID: "del_v", Summary: "s", Task: "do verification work"}, checkpoint)

	ctx := WithComplexity(WithDomain(context.Background(), "general"), 1)
	res, err := o.Resume(ctx, bb, nil, t.TempDir(), nil, nil, "")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if res.Status != orchestration.ExecutionStatusSuccess {
		t.Fatalf("Resume status = %q, want success", res.Status)
	}
	if n := emitter.launchCount(verifierUnitID); n != 0 {
		t.Errorf("SubAgentLaunch for the verifier container = %d, want 0 (a container is scoped to its parent, not relaunchable)", n)
	}
	if n := emitter.launchCount("del_v"); n != 1 {
		t.Errorf("SubAgentLaunch for the paused verifier delegate del_v = %d, want 1", n)
	}
	if sr, ok := bb.GetStepResult("del_v"); !ok || sr.Error != nil || sr.FullOutput != "verifier del done" {
		t.Fatalf("del_v after resume = %+v (ok=%v), want completed with output", sr, ok)
	}
	// The ledger checkpoint was lifted and seeded into the relaunched subagent.
	seeded := 0
	for _, cm := range rec.snapshot() {
		if s := cm.SeededSteps(); len(s) == 1 && s[0].Action.Name == "bash_exec" {
			seeded++
		}
	}
	if seeded != 1 {
		t.Errorf("context managers seeded with the del_v ledger checkpoint = %d, want exactly 1 (the resumed verifier delegate)", seeded)
	}
}

// TestResumeFunnel_TerminalUnitReplayedNotRerun is acceptance criterion 3: a
// unit that already completed is REPLAYED into the wave registry (so dependency
// resolution sees it) and never re-run. A paused dependent is relaunched and
// succeeds because its completed dependency was replayed, not relaunched.
func TestResumeFunnel_TerminalUnitReplayedNotRerun(t *testing.T) {
	caller := &pauseScriptLLM{script: []pauseScriptStep{
		// Wave: only del_parent runs — del_child is replayed, not relaunched.
		{respond: executorFinishResponse("parent done")},
		// The resumed conductor's only LLM call: finish.
		{respond: executorFinishResponse("all done")},
	}}
	o, emitter, _, recStore := newFunnelOrchestrator(t, caller)

	bb := newUnitLedgerBB("task-funnel-terminal", recStore)
	bb.SetOriginalRequest("delegate the work")
	// del_child completed in the prior run; del_parent paused, depending on it.
	bb.SetStepResult("del_child", "child done", nil, nil)
	seedLedgerUnit(t, bb, "", "del_child", units.UnitKindSubagent, units.UnitStatusCompleted, "", 0,
		coretools.DelegationTask{ID: "del_child", Summary: "child", Task: "child work"}, nil)
	bb.SetStepResult("del_parent", "", agent.ErrPaused, nil)
	seedLedgerUnit(t, bb, "", "del_parent", units.UnitKindSubagent, units.UnitStatusPaused, "", 0,
		coretools.DelegationTask{ID: "del_parent", Summary: "parent", Task: "parent work", DependsOn: []string{"del_child"}}, nil)

	ctx := WithComplexity(WithDomain(context.Background(), "general"), 1)
	res, err := o.Resume(ctx, bb, nil, t.TempDir(), nil, nil, "")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if res.Status != orchestration.ExecutionStatusSuccess {
		t.Fatalf("Resume status = %q, want success", res.Status)
	}
	if n := emitter.launchCount("del_child"); n != 0 {
		t.Errorf("SubAgentLaunch for the completed del_child = %d, want 0 (terminal units are replayed, never re-run)", n)
	}
	if n := emitter.launchCount("del_parent"); n != 1 {
		t.Errorf("SubAgentLaunch for the paused del_parent = %d, want 1", n)
	}
	if sr, ok := bb.GetStepResult("del_parent"); !ok || sr.Error != nil || sr.FullOutput != "parent done" {
		t.Fatalf("del_parent after resume = %+v (ok=%v), want completed with output (its completed dependency was replayed)", sr, ok)
	}
}

// TestResumeFunnel_LegacyInterruptedSpecRelaunched pins the same behavior for a
// task persisted BEFORE the durable ledger existed: the legacy delegation spec
// (with no blackboard step result — the interrupted case) is lifted into the
// funnel and relaunched fresh, never marked failed.
func TestResumeFunnel_LegacyInterruptedSpecRelaunched(t *testing.T) {
	caller := &pauseScriptLLM{script: []pauseScriptStep{
		// Wave: the interrupted legacy del_1 is relaunched fresh.
		{respond: executorFinishResponse("del_1 done")},
		// The resumed conductor's only LLM call: finish.
		{respond: executorFinishResponse("all done")},
	}}
	o, emitter, _, recStore := newFunnelOrchestrator(t, caller)

	// delegSpecBB has no unit store, so the ledger is empty and only the legacy
	// spec store carries the delegation.
	bb := newDelegSpecBB("task-funnel-legacy", recStore)
	bb.SetOriginalRequest("do the delegated work")
	bb.specs = []coretools.DelegationSpec{{
		Task: coretools.DelegationTask{ID: "del_1", Summary: "s", Task: "do work"},
	}}

	ctx := WithComplexity(WithDomain(context.Background(), "general"), 1)
	res, err := o.Resume(ctx, bb, nil, t.TempDir(), nil, nil, "")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if res.Status != orchestration.ExecutionStatusSuccess {
		t.Fatalf("Resume status = %q, want success", res.Status)
	}
	if n := emitter.launchCount("del_1"); n != 1 {
		t.Errorf("SubAgentLaunch for the interrupted legacy del_1 = %d, want 1 (relaunched fresh)", n)
	}
	if sr, ok := bb.GetStepResult("del_1"); !ok || sr.Error != nil || sr.FullOutput != "del_1 done" {
		t.Fatalf("del_1 after resume = %+v (ok=%v), want completed with output", sr, ok)
	}
}
