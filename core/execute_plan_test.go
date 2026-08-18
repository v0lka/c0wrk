package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/orchestration"
)

// --- planStepEventTranslator tests ---

// scopableMockEmitter is a mockEmitter that also implements PlanStepScopable
// (WithPlanStepID), so it can be used with scopePlanStepEvents.
type scopableMockEmitter struct {
	mockEmitter
	scopedCopies []*scopableMockEmitter
}

func (m *scopableMockEmitter) WithPlanStepID(id string) Emitter {
	cp := &scopableMockEmitter{}
	m.scopedCopies = append(m.scopedCopies, cp)
	return cp
}

func TestPlanStepEventTranslator_SubAgentLaunch_EmitsPlanStepStartOnRoot(t *testing.T) {
	root := &scopableMockEmitter{}
	// Simulate scopePlanStepEvents: create scoped copy, wrap in translator
	scoped, ok := root.WithPlanStepID("step_1").(*scopableMockEmitter)
	if !ok {
		t.Fatal("expected *scopableMockEmitter from WithPlanStepID")
	}
	translator := &planStepEventTranslator{
		Emitter: scoped,
		root:    root,
		summary: "My Step Summary",
	}

	translator.SubAgentLaunch("step_1", "full task description")

	if len(root.planStepStarts) != 1 {
		t.Fatalf("expected 1 PlanStepStart on root, got %d", len(root.planStepStarts))
	}
	ps := root.planStepStarts[0]
	if ps.stepID != "step_1" {
		t.Errorf("expected step_id 'step_1', got %q", ps.stepID)
	}
	if ps.summary != "My Step Summary" {
		t.Errorf("expected summary 'My Step Summary', got %q", ps.summary)
	}
	// PlanStepStart must NOT be emitted on the scoped copy
	if len(scoped.planStepStarts) != 0 {
		t.Errorf("expected 0 PlanStepStart on scoped copy, got %d", len(scoped.planStepStarts))
	}
}

func TestPlanStepEventTranslator_SubAgentComplete_EmitsPlanStepCompleteOnRoot(t *testing.T) {
	root := &scopableMockEmitter{}
	scoped, ok := root.WithPlanStepID("step_2").(*scopableMockEmitter)
	if !ok {
		t.Fatal("expected *scopableMockEmitter from WithPlanStepID")
	}
	translator := &planStepEventTranslator{
		Emitter: scoped,
		root:    root,
		summary: "Step 2",
	}

	dur := 5 * time.Second
	translator.SubAgentComplete("step_2", true, dur)

	if len(root.planStepCompletes) != 1 {
		t.Fatalf("expected 1 PlanStepComplete on root, got %d", len(root.planStepCompletes))
	}
	pc := root.planStepCompletes[0]
	if pc.stepID != "step_2" {
		t.Errorf("expected step_id 'step_2', got %q", pc.stepID)
	}
	if !pc.success {
		t.Errorf("expected success=true")
	}
	// Scoped copy should not receive PlanStepComplete
	if len(scoped.planStepCompletes) != 0 {
		t.Errorf("expected 0 PlanStepComplete on scoped copy, got %d", len(scoped.planStepCompletes))
	}
}

func TestPlanStepEventTranslator_ChildEventsDelegateToScoped(t *testing.T) {
	root := &scopableMockEmitter{}
	scoped, ok := root.WithPlanStepID("step_3").(*scopableMockEmitter)
	if !ok {
		t.Fatal("expected *scopableMockEmitter from WithPlanStepID")
	}
	translator := &planStepEventTranslator{
		Emitter: scoped,
		root:    root,
		summary: "Step 3",
	}

	// Child events (not SubAgentLaunch/Complete) should delegate to the
	// embedded scoped emitter via the Emitter interface.
	translator.StepTodoUpdate("step_3", []agent.TodoItem{{Text: "item", Checked: false}})
	translator.AssistantChunk("hello")

	if len(scoped.stepTodoUpdates) != 1 {
		t.Errorf("expected 1 StepTodoUpdate on scoped copy, got %d", len(scoped.stepTodoUpdates))
	}
	if len(scoped.assistantChunks) != 1 {
		t.Errorf("expected 1 AssistantChunk on scoped copy, got %d", len(scoped.assistantChunks))
	}
	// Root must not receive these child events
	if len(root.stepTodoUpdates) != 0 || len(root.assistantChunks) != 0 {
		t.Errorf("child events must not reach root emitter")
	}
}

// --- inlineStepLifecycle.markCompleted tests ---

func TestInlineStepLifecycle_MarkCompleted_PreventsCompleteAllDoubleComplete(t *testing.T) {
	emitter := &mockEmitter{}
	bb := orchestration.NewMapBlackboard()
	bb.SetPlan(&orchestration.Plan{
		Steps: []orchestration.PlanStep{
			{ID: "step_1", Summary: "Do", Description: "Do it"},
			{ID: "step_2", Summary: "Done", Description: "Done it"},
		},
	})
	lc := newInlineStepLifecycle(emitter, bb)

	// Simulate execute_plan completing step_1 via the translator
	lc.markCompleted("step_1")

	// Now run the finish fallback — step_1 must NOT be double-completed
	lc.completeAll(true, "")

	for _, pc := range emitter.planStepCompletes {
		if pc.stepID == "step_1" {
			t.Fatalf("step_1 was double-completed by completeAll after markCompleted")
		}
	}
}

func TestInlineStepLifecycle_MarkCompleted_NoEmission(t *testing.T) {
	emitter := &mockEmitter{}
	bb := orchestration.NewMapBlackboard()
	lc := newInlineStepLifecycle(emitter, bb)

	lc.markCompleted("step_5")

	if len(emitter.planStepStarts) != 0 || len(emitter.planStepCompletes) != 0 {
		t.Errorf("markCompleted must not emit any events")
	}
}

// --- delegate guard (PlanChecker) tests ---

// TestConductorLauncher_HasDeclaredPlan exercises the FALLBACK path used when
// the launcher is constructed directly (no planRunState wired). This mirrors
// how many tests build a bare conductorLauncher. Production wiring goes through
// RunConductor, which shares a planRunState between the launcher and the
// publisher — that path is covered by the TestConductorLauncher_PlanRunState
// tests below.
func TestConductorLauncher_HasDeclaredPlan(t *testing.T) {
	bb := orchestration.NewMapBlackboard()
	l := &conductorLauncher{bb: bb}

	if l.HasDeclaredPlan() {
		t.Error("expected false with no plan on blackboard")
	}

	bb.SetPlan(&orchestration.Plan{
		Steps: []orchestration.PlanStep{{ID: "s1"}},
	})
	if !l.HasDeclaredPlan() {
		t.Error("expected true after plan is set")
	}
}

// TestConductorLauncher_PlanRunState_NotDeclaredOnRestore verifies the core
// fix: when a planRunState is wired (production path), HasDeclaredPlan is false
// even if the blackboard carries a restored plan from a previous (completed)
// task. A continuation must stay free to act plan-less, declare a new plan, or
// delegate.
func TestConductorLauncher_PlanRunState_NotDeclaredOnRestore(t *testing.T) {
	bb := orchestration.NewMapBlackboard()
	// Simulate a restored plan from a previous task.
	bb.SetPlan(&orchestration.Plan{Steps: []orchestration.PlanStep{{ID: "s1"}}})

	planState := &planRunState{}
	l := &conductorLauncher{bb: bb, planState: planState}

	if l.HasDeclaredPlan() {
		t.Fatal("expected HasDeclaredPlan=false: a restored plan must NOT count as declared in the current run")
	}

	// And the raw blackboard plan is indeed present — proving the guard is
	// what changed, not the underlying state.
	if bb.GetPlan() == nil {
		t.Fatal("blackboard plan unexpectedly nil")
	}
}

// TestConductorLauncher_PlanRunState_DeclaredAfterPublish verifies that after
// conductorPublisher.Publish runs (the only path that sets the plan in the
// current run), both the launcher's HasDeclaredPlan and the planState reflect
// the declaration.
func TestConductorLauncher_PlanRunState_DeclaredAfterPublish(t *testing.T) {
	bb := orchestration.NewMapBlackboard()
	planState := &planRunState{}

	tmp := t.TempDir()
	emitter := &mockEmitter{}
	publisher := &conductorPublisher{emitter: emitter, bb: bb, plansDir: tmp, planState: planState}
	launcher := &conductorLauncher{bb: bb, planState: planState}

	if _, err := publisher.Publish(context.Background(), []tools.PlanTaskInput{
		{ID: "s1", Summary: "Do", Description: "Do it"},
	}); err != nil {
		t.Fatalf("Publish failed: %v", err)
	}

	if !launcher.HasDeclaredPlan() {
		t.Error("expected HasDeclaredPlan=true after Publish")
	}
	if !planState.isDeclared() {
		t.Error("expected planState.isDeclared()=true after Publish")
	}
}

// TestInlineStepLifecycle_CompleteAll_NoSynthesisForRestoredPlan verifies the
// finish-fallback fix: on a plan-less continuation, the blackboard carries a
// restored plan from a previous (completed) task, but planRunState is fresh
// (not declared). completeAll must NOT synthesize terminal events for the
// restored steps — the fresh lifecycle has empty started/completed sets, so
// without the planDeclaredInRun gate every restored step would be treated as
// "never started" and re-emit PlanStepComplete (PlanStepStart is deduped by
// the emitter, but PlanStepComplete is not).
func TestInlineStepLifecycle_CompleteAll_NoSynthesisForRestoredPlan(t *testing.T) {
	emitter := &mockEmitter{}
	bb := orchestration.NewMapBlackboard()
	// Restored plan from a previous (completed) task.
	bb.SetPlan(&orchestration.Plan{Steps: []orchestration.PlanStep{
		{ID: "s1", Summary: "old", Description: "old desc"},
		{ID: "s2", Summary: "old2", Description: "old desc 2"},
	}})

	planState := &planRunState{} // fresh — NOT declared in this run
	lc := newInlineStepLifecycle(emitter, bb)
	lc.planState = planState

	lc.completeAll(true, "")

	if len(emitter.planStepStarts) != 0 {
		t.Errorf("expected 0 PlanStepStart for restored plan, got %d", len(emitter.planStepStarts))
	}
	if len(emitter.planStepCompletes) != 0 {
		t.Errorf("expected 0 PlanStepComplete for restored plan, got %d", len(emitter.planStepCompletes))
	}
}

// TestInlineStepLifecycle_CompleteAll_SynthesizesWhenDeclaredInRun is the
// positive counterpart: when a plan WAS declared in this run, completeAll
// still synthesizes terminal events for never-started plan steps (the gate
// does not suppress legitimate cleanup). Also confirms started steps complete.
func TestInlineStepLifecycle_CompleteAll_SynthesizesWhenDeclaredInRun(t *testing.T) {
	emitter := &mockEmitter{}
	bb := orchestration.NewMapBlackboard()
	bb.SetPlan(&orchestration.Plan{Steps: []orchestration.PlanStep{
		{ID: "s1", Summary: "A", Description: "Desc A"},
		{ID: "s2", Summary: "B", Description: "Desc B"},
	}})

	planState := &planRunState{}
	planState.markDeclared() // a plan was declared in this run
	lc := newInlineStepLifecycle(emitter, bb)
	lc.planState = planState

	// Start s1 via a checklist update so it lands in the started set; s2 is
	// never touched (must be synthesized).
	lc.onChecklistUpdate("s1", []agent.TodoItem{{Text: "A", Checked: false}})
	lc.completeAll(true, "")

	// s1: complete from startedPending; s2: synthesized start+complete.
	if len(emitter.planStepCompletes) != 2 {
		t.Fatalf("expected 2 PlanStepComplete (1 started + 1 synthesized), got %d", len(emitter.planStepCompletes))
	}
	// s2 needs a synthesized start; s1 was already started via checklist.
	synthesizedStarts := 0
	for _, s := range emitter.planStepStarts {
		if s.stepID == "s2" {
			synthesizedStarts++
		}
	}
	if synthesizedStarts != 1 {
		t.Fatalf("expected 1 synthesized PlanStepStart for s2, got %d", synthesizedStarts)
	}
}

// --- Execute (conductorLauncher.Execute) error test ---

func TestExecute_NoPlan_ReturnsError(t *testing.T) {
	bb := orchestration.NewMapBlackboard()
	l := &conductorLauncher{bb: bb}

	_, err := l.Execute(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error when no plan declared")
	}
	if !strings.Contains(err.Error(), "no plan declared") {
		t.Errorf("expected 'no plan declared' error, got: %v", err)
	}
}

// TestExecute_RestoredPlan_Refused verifies the restored-plan guard: when
// planRunState is wired (production path) but NOT declared, Execute refuses to
// run a plan restored from a previous (completed) task. Re-running a completed
// task's steps would duplicate side effects, so the Conductor must declare a
// new plan or use delegate instead.
func TestExecute_RestoredPlan_Refused(t *testing.T) {
	bb := orchestration.NewMapBlackboard()
	bb.SetPlan(&orchestration.Plan{Steps: []orchestration.PlanStep{{ID: "s1", Summary: "old"}}})

	planState := &planRunState{} // fresh — NOT declared in this run
	rec := &waveRecorder{}
	l := &conductorLauncher{
		bb:              bb,
		planState:       planState,
		runPlanStepWave: rec.dispatch,
	}

	_, err := l.Execute(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for restored (not declared) plan")
	}
	if !strings.Contains(err.Error(), "restored from a previous") {
		t.Errorf("expected 'restored from a previous' error, got: %v", err)
	}
	// The wave dispatcher must NOT have been called — the guard rejects before
	// any step runs.
	if rec.calls != 0 {
		t.Errorf("expected 0 dispatch calls (guard rejects before execution), got %d", rec.calls)
	}
}

// TestExecute_DeclaredInRun_Proceeds is the positive counterpart: when
// planRunState IS declared, Execute runs the plan normally (the guard does not
// suppress legitimate execution). Uses the waveRecorder stub to avoid real
// subagent executors.
func TestExecute_DeclaredInRun_Proceeds(t *testing.T) {
	bb := orchestration.NewMapBlackboard()
	bb.SetPlan(&orchestration.Plan{Steps: []orchestration.PlanStep{{ID: "s1", Summary: "A"}}})

	planState := &planRunState{}
	planState.markDeclared() // a plan was declared in this run
	rec := &waveRecorder{}
	l := &conductorLauncher{
		deps:            conductorDeps{emitter: &mockEmitter{}},
		bb:              bb,
		planState:       planState,
		runPlanStepWave: rec.dispatch,
	}

	results, err := l.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("expected Execute to proceed for a declared plan, got error: %v", err)
	}
	if rec.calls != 1 {
		t.Errorf("expected 1 dispatch call, got %d", rec.calls)
	}
	if len(results) != 1 || results[0].StepID != "s1" || results[0].Status != "completed" {
		t.Errorf("unexpected results: %+v", results)
	}
}

// --- scopePlanStepEvents tests ---

func TestScopePlanStepEvents_ReturnsTranslator(t *testing.T) {
	emitter := &scopableMockEmitter{}
	l := &conductorLauncher{deps: conductorDeps{emitter: emitter}, bb: orchestration.NewMapBlackboard()}

	result := l.scopePlanStepEvents("step_a", "A")
	translator, ok := result.(*planStepEventTranslator)
	if !ok {
		t.Fatalf("expected *planStepEventTranslator, got %T", result)
	}

	// SubAgentLaunch → PlanStepStart on root, not subagent_launch
	translator.SubAgentLaunch("step_a", "Do A")
	if len(emitter.planStepStarts) != 1 {
		t.Errorf("expected 1 PlanStepStart on root, got %d", len(emitter.planStepStarts))
	}
	if emitter.planStepStarts[0].summary != "A" {
		t.Errorf("expected summary 'A', got %q", emitter.planStepStarts[0].summary)
	}
}

func TestScopePlanStepEvents_NoopWhenNotScopable(t *testing.T) {
	// A plain mockEmitter doesn't implement PlanStepScopable.
	emitter := &mockEmitter{}
	l := &conductorLauncher{deps: conductorDeps{emitter: emitter}, bb: orchestration.NewMapBlackboard()}

	result := l.scopePlanStepEvents("step_x", "X")
	if _, ok := result.(*agent.NoopEvents); !ok {
		t.Errorf("expected *NoopEvents when emitter is not scorable, got %T", result)
	}
}

// ---------------------------------------------------------------------------
// Execute (conductorLauncher.Execute) DAG scheduler tests
//
// These exercise the wave-detection / dependency-resolution / cascade /
// cycle / cancellation logic in Execute WITHOUT real LLM executors, by
// injecting a stub runPlanStepWave (waveRecorder) that returns canned
// outcomes per step. The event-translation adapter (planStepEventTranslator)
// is covered by the tests above; here the stub stands in for "steps that ran".
// ---------------------------------------------------------------------------

// waveRecorder is a stub runPlanStepWave: it records each dispatched wave and
// resolves every ready step's outcome from outcomes (absent = success). It
// never touches the registry — Execute does all localReg bookkeeping.
type waveRecorder struct {
	waves    [][]string                             // step IDs per dispatched wave, in call order
	outcomes map[string]error                       // stepID -> failure error (nil value = success)
	calls    int                                    // number of dispatch invocations
	out      func(stepID, output string, err error) // optional per-step outcome hook
}

func (r *waveRecorder) dispatch(_ context.Context, ready []orchestration.PlanStep, _ *tools.DelegationRegistry) []planStepOutcome {
	r.calls++
	ids := make([]string, len(ready))
	out := make([]planStepOutcome, 0, len(ready))
	for i, step := range ready {
		ids[i] = step.ID
		oc := planStepOutcome{stepID: step.ID, output: step.Summary + " output"}
		if err, ok := r.outcomes[step.ID]; ok {
			oc.err = err
		}
		if r.out != nil {
			r.out(step.ID, oc.output, oc.err)
		}
		out = append(out, oc)
	}
	r.waves = append(r.waves, ids)
	return out
}

// planWith builds a blackboard carrying the given plan steps (in declaration order).
func planWith(steps ...orchestration.PlanStep) orchestration.Blackboard {
	bb := orchestration.NewMapBlackboard()
	bb.SetPlan(&orchestration.Plan{Steps: steps})
	return bb
}

func step(id, summary string, deps ...string) orchestration.PlanStep {
	return orchestration.PlanStep{ID: id, Summary: summary, Description: "desc " + id, DependsOn: deps}
}

func resultIDs(results []tools.PlanStepResult) []string {
	ids := make([]string, len(results))
	for i, r := range results {
		ids[i] = r.StepID
	}
	return ids
}

func failedIDs(results []tools.PlanStepResult) []string {
	var ids []string
	for _, r := range results {
		if r.Status == "failed" {
			ids = append(ids, r.StepID)
		}
	}
	return ids
}

// TestExecute_LinearDAG_RunsInSequence verifies a dependency chain executes
// one step per wave (no step is ready until its predecessor completes) and
// returns results in plan-declaration order.
func TestExecute_LinearDAG_RunsInSequence(t *testing.T) {
	rec := &waveRecorder{}
	l := &conductorLauncher{
		deps:            conductorDeps{emitter: &mockEmitter{}},
		bb:              planWith(step("step_1", "A"), step("step_2", "B", "step_1"), step("step_3", "C", "step_2")),
		runPlanStepWave: rec.dispatch,
	}

	results, err := l.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	// Three sequential waves, one step each.
	if len(rec.waves) != 3 {
		t.Fatalf("expected 3 sequential waves, got %d: %v", len(rec.waves), rec.waves)
	}
	for i, want := range []string{"step_1", "step_2", "step_3"} {
		if len(rec.waves[i]) != 1 || rec.waves[i][0] != want {
			t.Errorf("wave %d = %v, want [%q]", i, rec.waves[i], want)
		}
	}

	// All completed, in declaration order.
	for _, r := range results {
		if r.Status != "completed" {
			t.Errorf("step %q status = %q, want completed", r.StepID, r.Status)
		}
	}
	if got := resultIDs(results); !equalStrings(got, []string{"step_1", "step_2", "step_3"}) {
		t.Errorf("result order = %v, want [step_1 step_2 step_3]", got)
	}
}

// TestExecute_DiamondDAG_ParallelIndependentSteps verifies that independent
// siblings (both depending on the same root, with no mutual dependency) run in
// a single parallel wave, and that results are returned in declaration order
// (not the randomised map-iteration order within the wave).
func TestExecute_DiamondDAG_ParallelIndependentSteps(t *testing.T) {
	// Declaration order intentionally lists step_b before step_a to prove the
	// result sort follows the declaration index, not execution/append order.
	rec := &waveRecorder{}
	l := &conductorLauncher{
		deps: conductorDeps{emitter: &mockEmitter{}},
		bb: planWith(
			step("root", "Root"),
			step("step_b", "B", "root"),
			step("step_a", "A", "root"),
			step("final", "Final", "step_a", "step_b"),
		),
		runPlanStepWave: rec.dispatch,
	}

	results, err := l.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	// 3 waves: [root], [step_b step_a] (parallel), [final].
	if len(rec.waves) != 3 {
		t.Fatalf("expected 3 waves, got %d: %v", len(rec.waves), rec.waves)
	}
	if len(rec.waves[1]) != 2 {
		t.Fatalf("expected the sibling wave to contain both independent steps, got %v", rec.waves[1])
	}
	siblingWave := stringSet(rec.waves[1])
	if _, ok := siblingWave["step_a"]; !ok {
		t.Errorf("expected step_a in the parallel wave, got %v", rec.waves[1])
	}
	if _, ok := siblingWave["step_b"]; !ok {
		t.Errorf("expected step_b in the parallel wave, got %v", rec.waves[1])
	}

	// Results must follow declaration order despite random map iteration.
	want := []string{"root", "step_b", "step_a", "final"}
	if got := resultIDs(results); !equalStrings(got, want) {
		t.Errorf("result order = %v, want declaration order %v", got, want)
	}
}

// TestExecute_UpstreamFailureCascadesAndEmitsTerminalEvents verifies #1a: when
// step_1 fails, its dependents (step_2, step_3) become unsatisfiable and MUST
// receive a synthesized PlanStepStart+PlanStepComplete terminal pair (so they
// are not left stuck "pending" in the plan panel), AND be markCompleted'd so
// the finish fallback does not double-complete them.
func TestExecute_UpstreamFailureCascadesAndEmitsTerminalEvents(t *testing.T) {
	emitter := &mockEmitter{}
	bb := planWith(
		step("step_1", "A"),
		step("step_2", "B", "step_1"),
		step("step_3", "C", "step_2"),
	)
	lc := newInlineStepLifecycle(emitter, bb)
	l := &conductorLauncher{
		deps:            conductorDeps{emitter: emitter, lifecycle: lc},
		bb:              bb,
		runPlanStepWave: (&waveRecorder{outcomes: map[string]error{"step_1": errors.New("boom")}}).dispatch,
	}

	results, err := l.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	// All three failed: step_1 (ran + failed), step_2/step_3 (unsatisfiable).
	if got := failedIDs(results); len(got) != 3 {
		t.Fatalf("expected all 3 steps failed, got %v (results=%v)", got, resultIDs(results))
	}

	// The cascaded steps (step_2, step_3) must each have a terminal pair.
	started := make(map[string]bool)
	completed := make(map[string]bool)
	for _, s := range emitter.planStepStarts {
		started[s.stepID] = true
	}
	for _, c := range emitter.planStepCompletes {
		completed[c.stepID] = true
	}
	for _, id := range []string{"step_2", "step_3"} {
		if !started[id] {
			t.Errorf("cascaded step %q: expected synthesized PlanStepStart, got starts=%v", id, emitter.planStepStarts)
		}
		if !completed[id] {
			t.Errorf("cascaded step %q: expected synthesized PlanStepComplete(success=false), got completes=%v", id, emitter.planStepCompletes)
		}
	}

	// The cascaded steps must carry the failure error message.
	for _, c := range emitter.planStepCompletes {
		if c.stepID == "step_2" || c.stepID == "step_3" {
			if c.success {
				t.Errorf("step %q complete: expected success=false", c.stepID)
			}
			if !strings.Contains(c.errMsg, "dependencies could not be satisfied") {
				t.Errorf("step %q errMsg = %q, want it to mention unsatisfied dependencies", c.stepID, c.errMsg)
			}
		}
	}

	// Finish fallback must NOT double-complete any step (all markCompleted'd).
	before := len(emitter.planStepCompletes)
	lc.completeAll(false, "")
	if after := len(emitter.planStepCompletes); after != before {
		t.Errorf("completeAll emitted %d extra PlanStepComplete (double-completion); before=%d after=%d", after-before, before, after)
	}
}

// TestExecute_DependencyCycle_FailsAllAsUnsatisfiable verifies that a
// dependency cycle (a→b→a) is detected: no step ever becomes ready, so the
// loop falls through to the unsatisfiable branch. The dispatcher must never be
// called (nothing to run).
func TestExecute_DependencyCycle_FailsAllAsUnsatisfiable(t *testing.T) {
	emitter := &mockEmitter{}
	rec := &waveRecorder{}
	l := &conductorLauncher{
		deps:            conductorDeps{emitter: emitter},
		bb:              planWith(step("step_a", "A", "step_b"), step("step_b", "B", "step_a")),
		runPlanStepWave: rec.dispatch,
	}

	results, err := l.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if rec.calls != 0 {
		t.Errorf("expected the dispatcher to never be called for a cycle, got %d calls", rec.calls)
	}
	if got := failedIDs(results); len(got) != 2 {
		t.Fatalf("expected both cycle steps failed, got %v", got)
	}
	for _, r := range results {
		if r.Error == nil || !strings.Contains(r.Error.Error(), "dependencies could not be satisfied") {
			t.Errorf("step %q error = %v, want unsatisfied-dependency error", r.StepID, r.Error)
		}
	}
	// Both must get a terminal event pair (never started).
	if len(emitter.planStepStarts) != 2 || len(emitter.planStepCompletes) != 2 {
		t.Errorf("expected 2 starts + 2 completes for cycle steps, got %d starts / %d completes",
			len(emitter.planStepStarts), len(emitter.planStepCompletes))
	}
}

// TestExecute_CancelledContext_MarksAllFailed verifies that when the context is
// already cancelled, every pending step is reported failed with ctx.Err() and
// the dispatcher is never called.
func TestExecute_CancelledContext_MarksAllFailed(t *testing.T) {
	rec := &waveRecorder{}
	l := &conductorLauncher{
		deps:            conductorDeps{emitter: &mockEmitter{}},
		bb:              planWith(step("step_1", "A"), step("step_2", "B")),
		runPlanStepWave: rec.dispatch,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled before Execute starts

	results, err := l.Execute(ctx, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Execute error = %v, want context.Canceled", err)
	}
	if rec.calls != 0 {
		t.Errorf("expected dispatcher never called with a cancelled context, got %d calls", rec.calls)
	}
	for _, r := range results {
		if r.Status != "failed" {
			t.Errorf("step %q status = %q, want failed", r.StepID, r.Status)
		}
		if !errors.Is(r.Error, context.Canceled) {
			t.Errorf("step %q error = %v, want context.Canceled", r.StepID, r.Error)
		}
	}
}

// TestExecute_Resume_SkipsSuccessfulAndRerunsFailed verifies the resume
// semantics: a second execute_plan call skips steps that already completed
// successfully on the blackboard and re-runs only failed/never-started steps
// (instead of rejecting the call or re-running everything).
func TestExecute_Resume_SkipsSuccessfulAndRerunsFailed(t *testing.T) {
	rec := &waveRecorder{outcomes: map[string]error{"step_2": errors.New("boom")}}
	l := &conductorLauncher{
		deps: conductorDeps{emitter: &mockEmitter{}},
		bb: planWith(
			step("step_1", "A"),
			step("step_2", "B", "step_1"),
			step("step_3", "C", "step_2"),
		),
		runPlanStepWave: rec.dispatch,
	}

	first, err := l.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("first Execute returned error: %v", err)
	}
	// step_1 completed, step_2 failed, step_3 never-started (unsatisfiable).
	if got := failedIDs(first); !equalStrings(got, []string{"step_2", "step_3"}) {
		t.Fatalf("first-run failed = %v, want [step_2 step_3]", got)
	}

	// Fix step_2 and reset the recorder so the resume can be asserted in isolation.
	delete(rec.outcomes, "step_2")
	rec.waves = nil
	rec.calls = 0

	second, err := l.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("resume Execute returned error: %v", err)
	}
	if rec.calls != 2 {
		t.Fatalf("expected 2 dispatches on resume (step_2 then step_3), got %d: %v", rec.calls, rec.waves)
	}
	if len(rec.waves) != 2 ||
		len(rec.waves[0]) != 1 || rec.waves[0][0] != "step_2" ||
		len(rec.waves[1]) != 1 || rec.waves[1][0] != "step_3" {
		t.Errorf("resume waves = %v, want [[step_2] [step_3]]", rec.waves)
	}
	if got := resultIDs(second); !equalStrings(got, []string{"step_1", "step_2", "step_3"}) {
		t.Errorf("resume result order = %v, want [step_1 step_2 step_3]", got)
	}
	for _, r := range second {
		if r.Status != "completed" {
			t.Errorf("step %q status = %q, want completed", r.StepID, r.Status)
		}
	}
	// step_1 was NOT re-dispatched; its output comes from the first run's
	// stored result.
	var step1Output string
	for _, r := range second {
		if r.StepID == "step_1" {
			step1Output = r.Output
		}
	}
	if step1Output != "A output" {
		t.Errorf("step_1 output = %q, want previous stored output %q", step1Output, "A output")
	}
}

// TestExecute_ForceRerun_SpecificStepAndDependents verifies explicit step
// targets: forcing a step re-runs it plus every transitive dependent, while
// other already-successful steps are still skipped.
func TestExecute_ForceRerun_SpecificStepAndDependents(t *testing.T) {
	rec := &waveRecorder{}
	l := &conductorLauncher{
		deps: conductorDeps{emitter: &mockEmitter{}},
		bb: planWith(
			step("step_1", "A"),
			step("step_2", "B", "step_1"),
			step("step_3", "C", "step_2"),
		),
		runPlanStepWave: rec.dispatch,
	}

	if _, err := l.Execute(context.Background(), nil); err != nil {
		t.Fatalf("first Execute returned error: %v", err)
	}

	rec.waves = nil
	rec.calls = 0

	results, err := l.Execute(context.Background(), []string{"step_2"})
	if err != nil {
		t.Fatalf("forced Execute returned error: %v", err)
	}
	if rec.calls != 2 {
		t.Fatalf("expected 2 dispatches (step_2 then step_3), got %d: %v", rec.calls, rec.waves)
	}
	if len(rec.waves) != 2 ||
		len(rec.waves[0]) != 1 || rec.waves[0][0] != "step_2" ||
		len(rec.waves[1]) != 1 || rec.waves[1][0] != "step_3" {
		t.Errorf("forced waves = %v, want [[step_2] [step_3]]", rec.waves)
	}
	if got := resultIDs(results); !equalStrings(got, []string{"step_1", "step_2", "step_3"}) {
		t.Errorf("result order = %v, want [step_1 step_2 step_3]", got)
	}
	// step_1 must be skipped (reported from its previous result, not re-run).
	var step1Output string
	for _, r := range results {
		if r.StepID == "step_1" {
			step1Output = r.Output
		}
	}
	if step1Output != "A output" {
		t.Errorf("step_1 output = %q, want previous stored output %q", step1Output, "A output")
	}
}

// TestExecute_ForceRerun_UnknownStepRejected verifies explicit step targets are
// validated against the declared plan before any step is dispatched.
func TestExecute_ForceRerun_UnknownStepRejected(t *testing.T) {
	rec := &waveRecorder{}
	l := &conductorLauncher{
		deps:            conductorDeps{emitter: &mockEmitter{}},
		bb:              planWith(step("step_1", "A")),
		runPlanStepWave: rec.dispatch,
	}

	_, err := l.Execute(context.Background(), []string{"nope"})
	if err == nil {
		t.Fatal("expected error for unknown step, got nil")
	}
	if !strings.Contains(err.Error(), "unknown step") {
		t.Errorf("error = %q, want it to mention 'unknown step'", err.Error())
	}
	if rec.calls != 0 {
		t.Errorf("expected 0 dispatch calls for an unknown step, got %d", rec.calls)
	}
}

// TestExecute_Resume_SkippedStepLifecycleRecordsSuccess verifies the resume
// skip path records an already-successful step in the current run's lifecycle:
// it emits a synthesized success terminal pair (so a re-declared plan panel
// shows it completed rather than pending) and marks it completed so the finish
// fallback does not re-synthesize it with a wrong success flag.
func TestExecute_Resume_SkippedStepLifecycleRecordsSuccess(t *testing.T) {
	emitter := &mockEmitter{}
	bb := orchestration.NewMapBlackboard()
	bb.SetPlan(&orchestration.Plan{Steps: []orchestration.PlanStep{
		{ID: "step_1", Summary: "A", Description: "desc step_1"},
		{ID: "step_2", Summary: "B", Description: "desc step_2", DependsOn: []string{"step_1"}},
	}})
	// step_1 already succeeded on the blackboard (e.g. from a previous run).
	bb.SetStepResult("step_1", "A output", nil, nil)

	lc := newInlineStepLifecycle(emitter, bb)
	rec := &waveRecorder{}
	l := &conductorLauncher{
		deps:            conductorDeps{emitter: emitter, lifecycle: lc},
		bb:              bb,
		runPlanStepWave: rec.dispatch,
	}

	results, err := l.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	// step_1 is skipped (not dispatched); step_2 runs in the only wave.
	if rec.calls != 1 || len(rec.waves) != 1 || !equalStrings(rec.waves[0], []string{"step_2"}) {
		t.Fatalf("expected single wave [step_2], got calls=%d waves=%v", rec.calls, rec.waves)
	}
	if got := resultIDs(results); !equalStrings(got, []string{"step_1", "step_2"}) {
		t.Errorf("result order = %v, want [step_1 step_2]", got)
	}

	// The skipped step must emit exactly one synthesized success terminal pair.
	step1Completes := 0
	step1Success := false
	for _, c := range emitter.planStepCompletes {
		if c.stepID == "step_1" {
			step1Completes++
			step1Success = c.success
		}
	}
	if step1Completes != 1 {
		t.Fatalf("expected exactly 1 PlanStepComplete for skipped step_1, got %d: %v", step1Completes, emitter.planStepCompletes)
	}
	if !step1Success {
		t.Errorf("skipped step_1 terminal pair must be success=true, got false")
	}

	// The finish fallback must NOT synthesize a second (failure) terminal for
	// the skipped step — it is already recorded as completed.
	before := len(emitter.planStepCompletes)
	lc.completeAll(false, "boom")
	if after := len(emitter.planStepCompletes); after != before {
		t.Errorf("completeAll double-completed the skipped step: before=%d after=%d (%v)", before, after, emitter.planStepCompletes)
	}
}

// --- small slice/set helpers ---

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func stringSet(items []string) map[string]struct{} {
	out := make(map[string]struct{}, len(items))
	for _, s := range items {
		out[s] = struct{}{}
	}
	return out
}
