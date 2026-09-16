package core

// Tests for the goal verifier's isolated unit sink (step_3).
//
// The verifier runs as an ISOLATED Conductor pass on a throwaway seeded
// MapBlackboard, so its run has no PersistableBlackboard to persist its units
// through. These tests prove the ledger-bound unit sink replaces that wiring:
//  1. a delegation the verifier spawns is persisted with a verifier parent
//     link (parented under the goal_verification unit, namespaced), and
//  2. its pause checkpoint is durable (a FRESH ledger over the same store reads
//     it back), while
//  3. the verifier's LLM view stays the isolated seeded blackboard — the live
//     task's blackboard never receives the verifier's delegation state.

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/v0lka/c0wrk/core/goal"
	coretools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/c0wrk/core/units"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// ----------------------------------------------------------------------------
// Test doubles
// ----------------------------------------------------------------------------

// memUnitStore is an in-memory units.Store keyed by task + namespace + id. It
// stands in for the durable task_units table: a fresh ledger reading the same
// store sees everything a previous ledger wrote, which is exactly the
// "survives a restart" property the acceptance criteria require.
type memUnitStore struct {
	mu     sync.Mutex
	byTask map[string]map[string]units.UnitRecord
}

func newMemUnitStore() *memUnitStore {
	return &memUnitStore{byTask: make(map[string]map[string]units.UnitRecord)}
}

func (s *memUnitStore) SaveUnit(rec units.UnitRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byTask[rec.TaskID] == nil {
		s.byTask[rec.TaskID] = make(map[string]units.UnitRecord)
	}
	s.byTask[rec.TaskID][rec.Namespace+"\x00"+rec.ID] = rec
	return nil
}

func (s *memUnitStore) LoadUnits(taskID string) ([]units.UnitRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]units.UnitRecord, 0, len(s.byTask[taskID]))
	for _, rec := range s.byTask[taskID] {
		out = append(out, rec)
	}
	return out, nil
}

// unitStoreTaskStore is a TaskPersistence that also exposes a UnitStore — the
// shape the production *session.TaskStoreAdapter has, and what
// units.StoreFrom(deps.taskStore) extracts.
type unitStoreTaskStore struct {
	mockTaskStoreWithReactivate
	us units.Store
}

func (s *unitStoreTaskStore) UnitStore() units.Store { return s.us }

func findUnit(recs []units.UnitRecord, id string) (units.UnitRecord, bool) {
	for _, rec := range recs {
		if rec.ID == id {
			return rec, true
		}
	}
	return units.UnitRecord{}, false
}

// ----------------------------------------------------------------------------
// unitSink: parent link, namespace and durable checkpoint
// ----------------------------------------------------------------------------

// TestUnitSink_PersistsDelegationWithVerifierParentAndDurableCheckpoint proves
// the sink's core contract directly: registering a delegation records a
// subagent unit parented under the verifier unit and scoped to the verifier
// namespace, and a later pause checkpoint is durable (read back by a FRESH
// ledger over the same store).
func TestUnitSink_PersistsDelegationWithVerifierParentAndDurableCheckpoint(t *testing.T) {
	const taskID = "task-ver-sink"
	store := newMemUnitStore()
	sink := newVerifierUnitSink(taskID, store, &goal.GoalState{Condition: "c", VerifyClause: "v"}, nil)
	if sink == nil {
		t.Fatal("newVerifierUnitSink returned nil for a non-empty task id")
	}

	reg := coretools.NewDelegationRegistry()
	sink.wire(reg, sink.parentID)
	if err := reg.RegisterTask(coretools.DelegationTask{ID: "del_1", Summary: "s", Task: "do work"}); err != nil {
		t.Fatalf("RegisterTask: %v", err)
	}

	checkpoint := []agent.Step{
		{Thought: "prior", Action: llm.ToolCall{ID: "c1", Name: "read_file", Input: json.RawMessage(`{}`)}, Observation: "o1"},
		{Thought: "more", Action: llm.ToolCall{ID: "c2", Name: "read_file", Input: json.RawMessage(`{}`)}, Observation: "o2"},
	}
	sink.record("del_1", agent.ErrPaused, checkpoint)

	// Read through a FRESH ledger over the same store — the "restart" path.
	fresh := units.NewLedger(store, taskID, verifierUnitNamespace)
	recs, err := fresh.List()
	if err != nil {
		t.Fatalf("fresh ledger List: %v", err)
	}

	// The verifier parent unit exists, so the delegation's parent link resolves.
	parent, ok := findUnit(recs, verifierUnitID)
	if !ok {
		t.Fatalf("verifier parent unit %q was not persisted; have %d units", verifierUnitID, len(recs))
	}
	if parent.Kind != units.UnitKindGoalVerification {
		t.Errorf("parent kind = %q, want %q", parent.Kind, units.UnitKindGoalVerification)
	}

	del, ok := findUnit(recs, "del_1")
	if !ok {
		t.Fatalf("delegation unit del_1 was not persisted; have %d units", len(recs))
	}
	if del.Kind != units.UnitKindSubagent {
		t.Errorf("delegation kind = %q, want %q", del.Kind, units.UnitKindSubagent)
	}
	if del.ParentID != verifierUnitID {
		t.Errorf("delegation parent = %q, want the verifier unit %q", del.ParentID, verifierUnitID)
	}
	if del.Namespace != verifierUnitNamespace {
		t.Errorf("delegation namespace = %q, want %q", del.Namespace, verifierUnitNamespace)
	}
	if del.TaskID != taskID {
		t.Errorf("delegation task = %q, want %q", del.TaskID, taskID)
	}
	if del.Status != units.UnitStatusPaused {
		t.Errorf("delegation status = %q, want %q (the pause checkpoint must be durable)", del.Status, units.UnitStatusPaused)
	}
	// The checkpoint's ReAct steps round-tripped through the durable store.
	var gotSteps []agent.Step
	if err := json.Unmarshal(del.Steps, &gotSteps); err != nil {
		t.Fatalf("unmarshal persisted checkpoint steps: %v", err)
	}
	if len(gotSteps) != len(checkpoint) || gotSteps[1].Thought != "more" {
		t.Errorf("persisted checkpoint = %+v, want the 2 paused steps", gotSteps)
	}
	// The rebuild spec round-tripped too.
	if len(del.Spec) == 0 {
		t.Error("delegation unit carries no rebuild spec")
	}
}

// TestUnitSink_RecordIgnoresUnregisteredIDs verifies the known-id guard: a
// plan-step or unrelated id can never synthesize a phantom verifier unit.
func TestUnitSink_RecordIgnoresUnregisteredIDs(t *testing.T) {
	const taskID = "task-ver-unknown"
	store := newMemUnitStore()
	sink := newVerifierUnitSink(taskID, store, nil, nil)

	sink.record("never_registered", agent.ErrPaused, []agent.Step{{Thought: "x"}})

	recs, err := units.NewLedger(store, taskID, verifierUnitNamespace).List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if _, ok := findUnit(recs, "never_registered"); ok {
		t.Fatal("record() synthesized a unit for an id the sink never registered")
	}
}

// TestNewVerifierUnitSink_NoTaskIDIsNil verifies the graceful degradation: a
// run with no task id yields no sink, so RunConductor keeps its
// blackboard-derived wiring.
func TestNewVerifierUnitSink_NoTaskIDIsNil(t *testing.T) {
	if sink := newVerifierUnitSink("", newMemUnitStore(), nil, nil); sink != nil {
		t.Fatal("newVerifierUnitSink must return nil without a task id")
	}
}

// ----------------------------------------------------------------------------
// RunConductor wiring: deps.unitSink replaces the blackboard-derived sink
// ----------------------------------------------------------------------------

// TestRunConductor_UnitSinkWinsOverBlackboard proves RunConductor wires the
// registry's spec sink from deps.unitSink when it is present, EVEN when the
// run's blackboard is not persistable (a bare MapBlackboard, as the verifier
// uses). The delegation is recorded as a unit parented under the verifier unit,
// and nothing is written to the task store's legacy delegation table.
func TestRunConductor_UnitSinkWinsOverBlackboard(t *testing.T) {
	const taskID = "task-ver-conductor"
	store := newMemUnitStore()
	sink := newVerifierUnitSink(taskID, store, nil, nil)

	caller := &mockLLMCaller{responses: []*llm.ChatResponse{
		// Conductor turn 1: delegate del_1 (blocking).
		{
			Message: llm.Message{
				Role:    "assistant",
				Content: "delegating",
				ToolCalls: []llm.ToolCall{{ID: "c1", Name: "delegate", Input: json.RawMessage(
					`{"tasks":[{"id":"del_1","summary":"do the thing","task":"do the thing carefully"}]}`)}},
			},
			StopReason: "tool_use",
		},
		// Subagent del_1's only LLM call: finish.
		executorFinishResponse("subagent done"),
		// Conductor turn 2: finish.
		executorFinishResponse("all done"),
	}}

	recStore := &specRecordingStore{}
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

	deps := o.buildConductorDeps(nil, nil)
	deps.unitSink = sink

	// A NON-persistable blackboard: the blackboard-derived wiring would persist
	// nothing, so the only way del_1 lands durably is the deps sink.
	bb := orchestration.NewMapBlackboard()
	availableTools := createTestRegistryWithDelegate(t).ListFiltered(nil)
	ctx := WithComplexity(WithDomain(context.Background(), "general"), 1)

	if _, err := RunConductor(ctx, "please delegate the thing", bb, availableTools, deps, ""); err != nil {
		t.Fatalf("RunConductor: %v", err)
	}

	recs, err := units.NewLedger(store, taskID, verifierUnitNamespace).List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	del, ok := findUnit(recs, "del_1")
	if !ok {
		t.Fatalf("del_1 was not recorded through the deps unit sink; have %d units", len(recs))
	}
	if del.ParentID != verifierUnitID || del.Namespace != verifierUnitNamespace {
		t.Errorf("del_1 = (parent %q, ns %q), want (%q, %q)", del.ParentID, del.Namespace, verifierUnitID, verifierUnitNamespace)
	}
	if del.Status != units.UnitStatusCompleted {
		t.Errorf("del_1 status = %q, want %q (the subagent finished)", del.Status, units.UnitStatusCompleted)
	}
	// The legacy delegation-spec store must NOT have been written: the isolated
	// sink wins, so the verifier's delegations never enter the live task's table.
	if got := len(recStore.recorded()); got != 0 {
		t.Errorf("the task store recorded %d delegation specs, want 0 (the unit sink must win)", got)
	}
}

// ----------------------------------------------------------------------------
// defaultGoalVerifier injection: end-to-end
// ----------------------------------------------------------------------------

// TestDefaultGoalVerifier_PersistsUnitsWithVerifierParent is the end-to-end
// acceptance test: the production verifier (re_derivation mode, so `delegate`
// is available) spawns a delegation. The run persists a goal_verification
// parent unit plus the delegation parented under it — while the verifier's LLM
// view stays the isolated seeded blackboard, so the live task's blackboard
// never receives the verifier's delegation state.
func TestDefaultGoalVerifier_PersistsUnitsWithVerifierParent(t *testing.T) {
	const taskID = "task-ver-e2e"
	memStore := newMemUnitStore()
	taskStore := &unitStoreTaskStore{us: memStore}

	caller := &mockLLMCaller{responses: []*llm.ChatResponse{
		// Verifier turn 1: delegate del_1 (re_derivation mode grants delegate).
		{
			Message: llm.Message{
				Role:    "assistant",
				Content: "re-deriving",
				ToolCalls: []llm.ToolCall{{ID: "c1", Name: "delegate", Input: json.RawMessage(
					`{"tasks":[{"id":"del_1","summary":"re-run the process","task":"re-run the process read-only"}]}`)}},
			},
			StopReason: "tool_use",
		},
		// Subagent del_1's only LLM call: finish.
		executorFinishResponse("clean"),
		// Verifier turn 2: finish (no verdict — irrelevant to this test).
		executorFinishResponse("done"),
	}}

	o := NewOrchestrator(OrchestratorConfig{}, OrchestratorDeps{
		LLM:            caller,
		ToolExec:       createTestRegistryWithDelegate(t),
		ToolRegistry:   createTestRegistryWithDelegate(t),
		TokenCounter:   llm.NewSimpleTokenCounter(),
		ContextFactory: testContextFactory,
		Emitter:        &mockEmitter{},
		CircuitBreaker: defaultCircuitBreakerConfig,
	})
	o.SetTaskStore(taskStore)

	gs := &goal.GoalState{
		Status:           goal.StatusActive,
		Condition:        "the design is sound",
		VerifyClause:     "a fresh re-derivation comes back clean",
		VerificationMode: goal.VerificationModeReDerivation,
	}
	// The goal loop's blackboard is the live task's — it must stay untouched by
	// the verifier's run (isolation).
	goalBB := &testPersistableBlackboard{
		MapBlackboard: orchestration.NewMapBlackboard(),
		taskID:        taskID,
		store:         taskStore,
	}

	available := verifierFixtureDescriptors()
	deps := o.buildConductorDeps(nil, nil)
	if _, err := o.defaultGoalVerifier(context.Background(), gs, &goal.Verdict{Status: "met"}, "verify it", "THE WORK PRODUCT", goalBB, available, deps); err != nil {
		t.Fatalf("defaultGoalVerifier: %v", err)
	}

	recs, err := units.NewLedger(memStore, taskID, verifierUnitNamespace).List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if _, ok := findUnit(recs, verifierUnitID); !ok {
		t.Fatalf("the verifier pass unit was not persisted; have %d units", len(recs))
	}
	del, ok := findUnit(recs, "del_1")
	if !ok {
		t.Fatalf("the verifier's delegation was not persisted; have %d units", len(recs))
	}
	if del.ParentID != verifierUnitID {
		t.Errorf("delegation parent = %q, want the verifier unit %q", del.ParentID, verifierUnitID)
	}
	if del.Namespace != verifierUnitNamespace || del.Kind != units.UnitKindSubagent {
		t.Errorf("delegation unit = (ns %q, kind %q), want (%q, %q)", del.Namespace, del.Kind, verifierUnitNamespace, units.UnitKindSubagent)
	}

	// ISOLATION: the verifier wrote its delegation only to its own throwaway
	// blackboard — the live task's blackboard was never touched.
	if _, ok := goalBB.GetStepResult("del_1"); ok {
		t.Error("the live goal blackboard received the verifier's delegation step result (isolation broken)")
	}
	if goalBB.GetFinalResult() == "THE WORK PRODUCT" {
		t.Error("the live goal blackboard was seeded with the verifier's work product (isolation broken)")
	}
}

// TestDefaultGoalVerifier_NoSinkWithoutTaskID verifies the graceful-degradation
// path: a verifier invoked on a NON-persistable blackboard (no task id) gets no
// sink, so RunConductor keeps its previous wiring and nothing breaks.
func TestDefaultGoalVerifier_NoSinkWithoutTaskID(t *testing.T) {
	caller := &mockLLMCaller{responses: []*llm.ChatResponse{executorFinishResponse("done")}}
	o := NewOrchestrator(OrchestratorConfig{}, OrchestratorDeps{
		LLM:            caller,
		ToolExec:       createTestRegistryWithDelegate(t),
		ToolRegistry:   createTestRegistryWithDelegate(t),
		TokenCounter:   llm.NewSimpleTokenCounter(),
		ContextFactory: testContextFactory,
		Emitter:        &mockEmitter{},
		CircuitBreaker: defaultCircuitBreakerConfig,
	})

	gs := &goal.GoalState{Status: goal.StatusActive, Condition: "x", VerifyClause: "y"}
	deps := o.buildConductorDeps(nil, nil)
	var _ sdktools.ToolDescriptor // keep sdktools imported for the fixture type
	if _, err := o.defaultGoalVerifier(context.Background(), gs, nil, "msg", "", orchestration.NewMapBlackboard(), verifierFixtureDescriptors(), deps); err != nil {
		t.Fatalf("defaultGoalVerifier: %v", err)
	}
}

// ----------------------------------------------------------------------------
// Launcher pause path
// ----------------------------------------------------------------------------

// TestPersistUnitOutcome_PausedDelegationCheckpointIsDurable proves the path a
// PAUSED delegate actually travels: the launcher's persistUnitOutcome keeps the
// outcome on the run's own (isolated) blackboard — so read_step_output still
// works in-run — AND records the resume checkpoint durably in the ledger-bound
// sink, where a FRESH ledger reads it back. This is the "its pause checkpoint is
// durable" criterion at the exact call site every pause branch uses.
func TestPersistUnitOutcome_PausedDelegationCheckpointIsDurable(t *testing.T) {
	const taskID = "task-ver-launcher"
	store := newMemUnitStore()
	sink := newVerifierUnitSink(taskID, store, nil, nil)

	reg := coretools.NewDelegationRegistry()
	sink.wire(reg, sink.parentID)
	if err := reg.RegisterTask(coretools.DelegationTask{ID: "del_1", Summary: "s", Task: "do work"}); err != nil {
		t.Fatalf("RegisterTask: %v", err)
	}

	bb := orchestration.NewMapBlackboard()
	l := &conductorLauncher{bb: bb, deps: conductorDeps{unitSink: sink}}

	checkpoint := []agent.Step{
		{Thought: "partial work", Action: llm.ToolCall{ID: "c1", Name: "read_file", Input: json.RawMessage(`{}`)}, Observation: "o1"},
	}
	l.persistUnitOutcome("del_1", "partial output", agent.ErrPaused, checkpoint)

	// The run's own blackboard still carries the paused step result.
	sr, ok := bb.GetStepResult("del_1")
	if !ok || !isPaused(sr.Error) {
		t.Fatalf("blackboard step result = %+v (ok=%v), want a paused checkpoint", sr, ok)
	}

	// The durable ledger carries the checkpoint + paused status.
	recs, err := units.NewLedger(store, taskID, verifierUnitNamespace).List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	del, ok := findUnit(recs, "del_1")
	if !ok {
		t.Fatalf("del_1 was not recorded in the ledger by persistUnitOutcome; have %d units", len(recs))
	}
	if del.Status != units.UnitStatusPaused {
		t.Errorf("status = %q, want %q", del.Status, units.UnitStatusPaused)
	}
	var got []agent.Step
	if err := json.Unmarshal(del.Steps, &got); err != nil {
		t.Fatalf("unmarshal durable checkpoint: %v", err)
	}
	if len(got) != 1 || got[0].Thought != "partial work" {
		t.Errorf("durable checkpoint = %+v, want the single paused step", got)
	}
}
