package core

// Tests for the launcher as the SINGLE writer of unit lifecycle through the
// units.Ledger: spec at register, running at start, paused+steps at checkpoint,
// completed/failed at settle — for plan steps, blocking delegates and (direct)
// delegate transitions. The blackboard keeps the in-memory step-result view.

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	coretools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/c0wrk/core/units"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
)

// recordingUnitStore is an in-memory units.Store that records every write so a
// test can inspect the durable unit lifecycle the launcher produced.
type recordingUnitStore struct {
	mu   sync.Mutex
	recs map[string]units.UnitRecord
}

func newRecordingUnitStore() *recordingUnitStore {
	return &recordingUnitStore{recs: make(map[string]units.UnitRecord)}
}

func (s *recordingUnitStore) SaveUnit(rec units.UnitRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if prev, ok := s.recs[rec.ID]; ok && rec.CreatedAt.IsZero() {
		rec.CreatedAt = prev.CreatedAt
	}
	s.recs[rec.ID] = rec
	return nil
}

func (s *recordingUnitStore) LoadUnits(string) ([]units.UnitRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]units.UnitRecord, 0, len(s.recs))
	for _, r := range s.recs {
		out = append(out, r)
	}
	return out, nil
}

func (s *recordingUnitStore) get(id string) (units.UnitRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.recs[id]
	return r, ok
}

// unitRecordingBB is a PersistableBlackboard that exposes a durable unit store,
// so NewBlackboardLedger wires a real (recording) ledger for the launcher.
type unitRecordingBB struct {
	testPersistableBlackboard
	rec *recordingUnitStore
}

func (b *unitRecordingBB) UnitStore() units.Store { return b.rec }

var _ PersistableBlackboard = (*unitRecordingBB)(nil)
var _ UnitStoreProvider = (*unitRecordingBB)(nil)

func newUnitRecordingBB(taskID string, store TaskPersistence) *unitRecordingBB {
	return &unitRecordingBB{
		testPersistableBlackboard: testPersistableBlackboard{
			MapBlackboard: orchestration.NewMapBlackboard(),
			taskID:        taskID,
			store:         store,
		},
		rec: newRecordingUnitStore(),
	}
}

// TestUnitLedger_DelegateLifecycleEndToEnd drives a full RunConductor with a
// blocking delegate and asserts the launcher recorded the delegation's unit
// lifecycle in the ledger: registered (spec), then settled completed.
func TestUnitLedger_DelegateLifecycleEndToEnd(t *testing.T) {
	caller := &mockLLMCaller{responses: []*llm.ChatResponse{
		{
			Message: llm.Message{
				Role:    "assistant",
				Content: "delegating",
				ToolCalls: []llm.ToolCall{{ID: "c1", Name: "delegate", Input: json.RawMessage(
					`{"tasks":[{"id":"del_1","summary":"do the thing","task":"do the thing carefully"}]}`)}},
			},
			StopReason: "tool_use",
		},
		executorFinishResponse("subagent done"),
		executorFinishResponse("all done"),
	}}

	recStore := &specRecordingStore{}
	bb := newUnitRecordingBB("task-unit-del", recStore)

	o := NewOrchestrator(OrchestratorConfig{}, OrchestratorDeps{
		LLM:            caller,
		ToolExec:       createTestRegistryWithDelegate(t),
		ToolRegistry:   createTestRegistryWithDelegate(t),
		TokenCounter:   llm.NewSimpleTokenCounter(),
		ContextFactory: testContextFactory,
		Emitter:        &mockEmitter{},
		CircuitBreaker: defaultCircuitBreakerConfig,
	})
	o.SetTaskStore(recStore)

	availableTools := createTestRegistryWithDelegate(t).ListFiltered(nil)
	ctx := WithComplexity(WithDomain(context.Background(), "general"), 1)
	if _, err := o.runConductor(ctx, "please delegate the thing", bb, availableTools, t.TempDir(), nil, nil, nil, "", "", false); err != nil {
		t.Fatalf("runConductor: %v", err)
	}

	rec, ok := bb.rec.get("del_1")
	if !ok {
		t.Fatal("ledger: delegate unit del_1 was never registered")
	}
	if rec.Kind != units.UnitKindSubagent {
		t.Errorf("unit kind = %q, want %q", rec.Kind, units.UnitKindSubagent)
	}
	if rec.Status != units.UnitStatusCompleted {
		t.Errorf("unit status = %q, want %q (running at start, completed at settle)", rec.Status, units.UnitStatusCompleted)
	}
	if len(rec.Spec) == 0 {
		t.Error("unit spec must be recorded at register time")
	}
	var spec coretools.DelegationSpec
	if err := json.Unmarshal(rec.Spec, &spec); err != nil {
		t.Fatalf("unit spec is not a DelegationSpec: %v", err)
	}
	if spec.Task.ID != "del_1" || spec.Task.Task != "do the thing carefully" {
		t.Errorf("unit spec task = %+v, want the registered delegation task", spec.Task)
	}
	if rec.ParentID != "" || rec.Depth != 0 {
		t.Errorf("root-registry unit stamps = (parent %q, depth %d), want (\"\", 0)", rec.ParentID, rec.Depth)
	}

	// The blackboard keeps the in-memory step-result view.
	if sr, ok := bb.GetStepResult("del_1"); !ok || sr.Error != nil {
		t.Fatalf("blackboard StepResult for del_1 missing/failed despite ledger settle: %+v (ok=%v)", sr, ok)
	}
}

// ledgerWaveStub is a runPlanStepWave stub that marks each dispatched step
// running in the registry (mirroring the production wave) and returns the
// canned outcome for it.
type ledgerWaveStub struct {
	outcomes map[string]planStepOutcome
}

func (w *ledgerWaveStub) dispatch(_ context.Context, ready []orchestration.PlanStep, registry *coretools.DelegationRegistry) []planStepOutcome {
	out := make([]planStepOutcome, 0, len(ready))
	for _, s := range ready {
		registry.Start(s.ID, nil)
		oc, ok := w.outcomes[s.ID]
		if !ok {
			oc = planStepOutcome{stepID: s.ID, output: s.Summary + " out"}
		}
		oc.stepID = s.ID
		out = append(out, oc)
	}
	return out
}

func newDeclaredPlanLauncher(bb *unitRecordingBB, wave *ledgerWaveStub) *conductorLauncher {
	ps := newPlanRunState(false)
	ps.markDeclared() // declared in THIS run → Execute proceeds
	return &conductorLauncher{
		deps:            conductorDeps{emitter: &mockEmitter{}},
		bb:              bb,
		planState:       ps,
		runPlanStepWave: wave.dispatch,
	}
}

// TestUnitLedger_PlanStepLifecycle asserts plan steps register and settle their
// unit records: a success settles completed, a failure settles failed, and a
// step blocked by an upstream failure is settled failed without launching.
func TestUnitLedger_PlanStepLifecycle(t *testing.T) {
	bb := newUnitRecordingBB("task-unit-plan", nil)
	bb.SetPlan(&orchestration.Plan{Steps: []orchestration.PlanStep{
		{ID: "s1", Summary: "one", Description: "d1"},
		{ID: "s2", Summary: "two", Description: "d2", DependsOn: []string{"s1"}},
		{ID: "s3", Summary: "three", Description: "d3", DependsOn: []string{"s2"}},
	}})

	wave := &ledgerWaveStub{outcomes: map[string]planStepOutcome{
		"s2": {stepID: "s2", err: errors.New("boom")},
	}}
	l := newDeclaredPlanLauncher(bb, wave)

	if _, err := l.Execute(context.Background(), nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	want := map[string]units.UnitStatus{
		"s1": units.UnitStatusCompleted,
		"s2": units.UnitStatusFailed,
		"s3": units.UnitStatusFailed, // never launched — upstream failure
	}
	for id, status := range want {
		rec, ok := bb.rec.get(id)
		if !ok {
			t.Fatalf("plan step %s was never registered in the ledger", id)
		}
		if rec.Kind != units.UnitKindPlanStep {
			t.Errorf("unit %s kind = %q, want %q", id, rec.Kind, units.UnitKindPlanStep)
		}
		if rec.Status != status {
			t.Errorf("unit %s status = %q, want %q", id, rec.Status, status)
		}
		if len(rec.Spec) == 0 {
			t.Errorf("unit %s must carry its register-time spec", id)
		}
	}

	// The blackboard keeps the in-memory step-result view for launched steps.
	if sr, ok := bb.GetStepResult("s1"); !ok || sr.Error != nil {
		t.Fatalf("blackboard StepResult for s1 missing/failed: %+v (ok=%v)", sr, ok)
	}
}

// TestUnitLedger_PlanStepPaused asserts a cooperative pause records the unit as
// paused WITH its resume checkpoint (the partial trajectory) in the ledger.
func TestUnitLedger_PlanStepPaused(t *testing.T) {
	bb := newUnitRecordingBB("task-unit-pause", nil)
	bb.SetPlan(&orchestration.Plan{Steps: []orchestration.PlanStep{
		{ID: "p1", Summary: "pause me", Description: "d1"},
	}})

	checkpoint := []agent.Step{{Thought: "prior work"}}
	wave := &ledgerWaveStub{outcomes: map[string]planStepOutcome{
		"p1": {stepID: "p1", output: "partial", steps: checkpoint, err: agent.ErrPaused},
	}}
	l := newDeclaredPlanLauncher(bb, wave)

	if _, err := l.Execute(context.Background(), nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	rec, ok := bb.rec.get("p1")
	if !ok {
		t.Fatal("paused plan step p1 was never registered in the ledger")
	}
	if rec.Status != units.UnitStatusPaused {
		t.Fatalf("unit status = %q, want %q", rec.Status, units.UnitStatusPaused)
	}
	var steps []agent.Step
	if err := json.Unmarshal(rec.Steps, &steps); err != nil {
		t.Fatalf("unit checkpoint is not []agent.Step: %v", err)
	}
	if len(steps) != 1 || steps[0].Thought != "prior work" {
		t.Fatalf("unit checkpoint = %+v, want the paused partial trajectory", steps)
	}
}

// TestUnitLedger_RegisterRunningPauseSettle exercises the direct lifecycle
// transitions the launcher owns for a delegate-style unit: register (spec) →
// running at start → paused+steps at checkpoint → completed at settle.
func TestUnitLedger_RegisterRunningPauseSettle(t *testing.T) {
	bb := newUnitRecordingBB("task-unit-direct", nil)
	l := &conductorLauncher{bb: bb}

	reg := coretools.NewDelegationRegistry()
	wireDelegationSpecSink(reg, l.unitLedger(), "", "task-unit-direct", nil, nil)
	if err := reg.RegisterTaskKind(coretools.DelegationKindSubagent, coretools.DelegationTask{
		ID: "d1", Summary: "s", Task: "t",
	}); err != nil {
		t.Fatalf("RegisterTaskKind: %v", err)
	}

	if rec, _ := bb.rec.get("d1"); rec.Status != units.UnitStatusPending {
		t.Fatalf("after register status = %q, want pending", rec.Status)
	}

	l.markUnitRunning("d1")
	if rec, _ := bb.rec.get("d1"); rec.Status != units.UnitStatusRunning {
		t.Fatalf("after start status = %q, want running", rec.Status)
	}

	checkpoint := []agent.Step{{Thought: "prior"}}
	l.persistUnitOutcome("d1", "partial", agent.ErrPaused, checkpoint)
	rec, _ := bb.rec.get("d1")
	if rec.Status != units.UnitStatusPaused {
		t.Fatalf("after pause status = %q, want paused", rec.Status)
	}
	var steps []agent.Step
	if err := json.Unmarshal(rec.Steps, &steps); err != nil || len(steps) != 1 {
		t.Fatalf("paused unit checkpoint = %s (err %v), want one step", rec.Steps, err)
	}

	// A second unit, settled without a pause, ends completed.
	if err := reg.RegisterTaskKind(coretools.DelegationKindPlanStep, coretools.DelegationTask{
		ID: "d2", Summary: "s", Task: "t",
	}); err != nil {
		t.Fatalf("RegisterTaskKind(d2): %v", err)
	}
	l.markUnitRunning("d2")
	l.persistUnitOutcome("d2", "done", nil, nil)
	rec2, _ := bb.rec.get("d2")
	if rec2.Status != units.UnitStatusCompleted {
		t.Fatalf("after settle status = %q, want completed", rec2.Status)
	}
	if rec2.Kind != units.UnitKindPlanStep {
		t.Errorf("unit d2 kind = %q, want %q (kind rides the registration spec)", rec2.Kind, units.UnitKindPlanStep)
	}
}
