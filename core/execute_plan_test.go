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

// --- Execute (conductorLauncher.Execute) error test ---

func TestExecute_NoPlan_ReturnsError(t *testing.T) {
	bb := orchestration.NewMapBlackboard()
	l := &conductorLauncher{bb: bb}

	_, err := l.Execute(context.Background())
	if err == nil {
		t.Fatal("expected error when no plan declared")
	}
	if !strings.Contains(err.Error(), "no plan declared") {
		t.Errorf("expected 'no plan declared' error, got: %v", err)
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
	waves    [][]string               // step IDs per dispatched wave, in call order
	outcomes map[string]error         // stepID -> failure error (nil value = success)
	calls    int                      // number of dispatch invocations
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
		deps:           conductorDeps{emitter: &mockEmitter{}},
		bb:             planWith(step("step_1", "A"), step("step_2", "B", "step_1"), step("step_3", "C", "step_2")),
		runPlanStepWave: rec.dispatch,
	}

	results, err := l.Execute(context.Background())
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

	results, err := l.Execute(context.Background())
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

	results, err := l.Execute(context.Background())
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
		deps: conductorDeps{emitter: emitter},
		bb:   planWith(step("step_a", "A", "step_b"), step("step_b", "B", "step_a")),
		runPlanStepWave: rec.dispatch,
	}

	results, err := l.Execute(context.Background())
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
		deps: conductorDeps{emitter: &mockEmitter{}},
		bb:   planWith(step("step_1", "A"), step("step_2", "B")),
		runPlanStepWave: rec.dispatch,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled before Execute starts

	results, err := l.Execute(ctx)
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

// TestExecute_Idempotent_SecondCallRejected verifies #3b: execute_plan runs at
// most once per launcher — a second call is rejected with a clear error so the
// Conductor reflects and publishes a new plan instead of re-running every step.
func TestExecute_Idempotent_SecondCallRejected(t *testing.T) {
	rec := &waveRecorder{}
	l := &conductorLauncher{
		deps: conductorDeps{emitter: &mockEmitter{}},
		bb:   planWith(step("step_1", "A")),
		runPlanStepWave: rec.dispatch,
	}

	if _, err := l.Execute(context.Background()); err != nil {
		t.Fatalf("first Execute returned error: %v", err)
	}
	if rec.calls != 1 {
		t.Fatalf("expected 1 dispatch on first call, got %d", rec.calls)
	}

	_, err := l.Execute(context.Background())
	if err == nil {
		t.Fatal("expected second Execute to be rejected, got nil error")
	}
	if !strings.Contains(err.Error(), "at most once") {
		t.Errorf("second-call error = %q, want it to mention 'at most once'", err.Error())
	}
	// No additional dispatch from the rejected call.
	if rec.calls != 1 {
		t.Errorf("expected dispatch count to stay at 1 after rejected second call, got %d", rec.calls)
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
