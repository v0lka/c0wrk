package core

// End-to-end tests for the system-driven auto-resume wave (Orchestrator.Resume
// → resumePausedWork): everything that was paused is settled FORMALLY — before
// the resumed conductor's first LLM call, without any model decision. Paused
// plan steps continue through the plan DAG engine; paused delegates are
// rebuilt from their persisted specs and re-launched with the SAME ids;
// children settle before their re-delegating parents; a pause that re-trips
// mid-wave checkpoints cleanly with zero LLM calls.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/core/goal"
	coretools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// launchRecorder is a mockEmitter that records every SubAgentLaunch step ID.
// WithPlanStepID returns the receiver itself so scoped (per-subagent) event
// streams keep flowing into the same recorder — assertions can see launches
// from both the original run and the resume wave.
type launchRecorder struct {
	mockEmitter
	mu       sync.Mutex
	launches []string
}

func (e *launchRecorder) SubAgentLaunch(id, _ string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.launches = append(e.launches, id)
}

func (e *launchRecorder) WithPlanStepID(string) Emitter { return e }

func (e *launchRecorder) launchCount(id string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, l := range e.launches {
		if l == id {
			n++
		}
	}
	return n
}

// delegSpecBB is a PersistableBlackboard that also implements
// DelegationSpecReader, mirroring the restored backend PersistentBlackboard:
// the specs recorded by the task store during run 1 are exposed for the wave.
type delegSpecBB struct {
	testPersistableBlackboard
	specs []coretools.DelegationSpec
}

func (b *delegSpecBB) DelegationSpecs() []coretools.DelegationSpec { return b.specs }

func newDelegSpecBB(taskID string, store TaskPersistence) *delegSpecBB {
	return &delegSpecBB{testPersistableBlackboard: testPersistableBlackboard{
		MapBlackboard: orchestration.NewMapBlackboard(),
		taskID:        taskID,
		store:         store,
	}}
}

// newWaveOrchestrator builds an orchestrator whose registry carries the real
// delegate + declare_plan + execute_plan tools, recording every created
// seedable ContextManager (conductor + subagents).
func newWaveOrchestrator(t *testing.T, caller agent.LLMCaller, emitter Emitter, rec *cmRecorder) *Orchestrator {
	t.Helper()
	registry := createTestRegistry()
	registry.Register(coretools.NewDeclarePlanTool(nil))
	registry.Register(coretools.NewExecutePlanTool())
	registry.Register(coretools.NewDelegateTool())
	cf := func(systemPrompt string, _ llm.ModelMetadata, _ string, _ ...orchestration.PruningOverride) ContextManager {
		cm := &seedableRecordingCM{mockContextManager: mockContextManager{systemPrompt: systemPrompt}}
		if rec != nil {
			rec.add(cm)
		}
		return cm
	}
	return NewOrchestrator(OrchestratorConfig{}, OrchestratorDeps{
		LLM:            caller,
		ToolExec:       registry,
		ToolRegistry:   registry,
		TokenCounter:   llm.NewSimpleTokenCounter(),
		ContextFactory: cf,
		Emitter:        emitter,
		CircuitBreaker: defaultCircuitBreakerConfig,
	})
}

// TestResumeWave_PlanSettledBeforeFirstLLMCall: run 1 declares a 3-step plan
// and pauses mid-s1. Resume settles ALL steps in the wave (s1 from its
// checkpoint, s2/s3 fresh) and the resumed conductor's ONLY LLM call is its
// finish — the model is never asked to continue the plan.
func TestResumeWave_PlanSettledBeforeFirstLLMCall(t *testing.T) {
	caller := &pauseScriptLLM{script: []pauseScriptStep{
		// Run 1: declare the roadmap, start executing it.
		{respond: assistantToolCall("c1", "declare_plan", `{"tasks":[
			{"id":"s1","summary":"Do the groundwork","description":"s1: do the groundwork","depends_on":[]},
			{"id":"s2","summary":"Finish the build","description":"s2: finish the build","depends_on":["s1"]},
			{"id":"s3","summary":"Wrap up","description":"s3: wrap up","depends_on":["s2"]}]}`)},
		{respond: assistantToolCall("c2", "execute_plan", `{}`)},
		// s1's subagent: gated tool call — the pause is armed while blocked
		// here, then the response returns so the pause trips at the NEXT step
		// boundary with a non-empty partial trajectory.
		{respond: assistantToolCall("g1", "bash_exec", `{"command":"echo groundwork","timeout":"5s"}`),
			started: make(chan struct{}), gate: make(chan struct{})},
		// Wave (inside Resume, before any conductor LLM call): s1 continues
		// from its checkpoint, then s2, then s3.
		{respond: executorFinishResponse("s1 done")},
		{respond: executorFinishResponse("s2 done")},
		{respond: executorFinishResponse("s3 done")},
		// The resumed conductor's ONLY LLM call: finish.
		{respond: executorFinishResponse("all done")},
	}}

	emitter := &launchRecorder{}
	rec := &cmRecorder{}
	o := newWaveOrchestrator(t, caller, emitter, rec)
	recStore := &specRecordingStore{}
	o.SetTaskStore(recStore)

	registry := createTestRegistry()
	registry.Register(coretools.NewDeclarePlanTool(nil))
	registry.Register(coretools.NewExecutePlanTool())
	availableTools := registry.ListFiltered(nil)

	bb := newDelegSpecBB("task-wave-plan", recStore)
	bb.SetOriginalRequest("build the widget")
	plansDir := t.TempDir()
	ctx := WithComplexity(WithDomain(context.Background(), "general"), 2)

	// --- Run 1: pause mid-s1 under the real pause signal ---
	clearSignal := o.installPauseSignal()
	deps1 := o.buildConductorDeps(nil, nil)

	type runOutcome struct {
		result *orchestration.ExecutionResult
		err    error
	}
	outCh := make(chan runOutcome, 1)
	go func() {
		result, err := RunConductor(ctx, "build the widget", bb, availableTools, deps1, plansDir)
		outCh <- runOutcome{result, err}
	}()

	gated := caller.script[2]
	select {
	case <-gated.started:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for the s1 subagent to reach its gated LLM call")
	}
	o.PauseSession()
	close(gated.gate)

	var out runOutcome
	select {
	case out = <-outCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for run 1 to pause")
	}
	if !errors.Is(out.err, agent.ErrPaused) {
		t.Fatalf("run 1 error = %v, want ErrPaused", out.err)
	}
	clearSignal()

	// s1 has the paused checkpoint; s2/s3 were never dispatched.
	sr1, ok := bb.GetStepResult("s1")
	if !ok || !isPaused(sr1.Error) {
		t.Fatalf("s1 checkpoint = %+v (ok=%v), want paused", sr1, ok)
	}

	// --- Resume: the wave settles everything, then ONE conductor call ---
	res, err := o.Resume(ctx, bb, nil, plansDir, nil, nil, "")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if res.Status != orchestration.ExecutionStatusSuccess {
		t.Fatalf("Resume status = %q, want success", res.Status)
	}

	for _, step := range []struct{ id, want string }{{"s1", "s1 done"}, {"s2", "s2 done"}, {"s3", "s3 done"}} {
		sr, ok := bb.GetStepResult(step.id)
		if !ok || sr.Error != nil || sr.FullOutput != step.want {
			t.Fatalf("%s after resume = %+v (ok=%v), want successful %q", step.id, sr, ok, step.want)
		}
	}

	// The choreography consumed the script exactly — the conductor made no
	// execute_plan/declare_plan call in run 2 (only the final finish).
	if got := caller.consumed(); got != len(caller.script) {
		t.Errorf("script consumed %d/%d — run 2 deviated (extra model turns)", got, len(caller.script))
	}

	// The conductor's (last-created) context manager carries the wave summary
	// on its task message, next to the original request.
	cms := rec.snapshot()
	if len(cms) == 0 {
		t.Fatal("no context managers recorded")
	}
	conductorCM := cms[len(cms)-1]
	if !strings.Contains(conductorCM.taskDefinition, "Auto-resumed subagents") {
		t.Errorf("conductor task message lacks the wave summary: %q", conductorCM.taskDefinition)
	}
	if !strings.Contains(conductorCM.taskDefinition, "build the widget") {
		t.Errorf("conductor task message lacks the original request: %q", conductorCM.taskDefinition)
	}
}

// TestResumeWave_DelegateSameIdSeeded: run 1 delegates del_1 (blocking) and
// the pause trips mid-subagent. Resume rebuilds del_1 from its persisted spec,
// re-launches it under the SAME id (same chat block), seeds its context with
// the checkpoint trajectory, and settles the result before the conductor's
// first LLM call.
func TestResumeWave_DelegateSameIdSeeded(t *testing.T) {
	caller := &pauseScriptLLM{script: []pauseScriptStep{
		// Run 1: delegate del_1.
		{respond: assistantToolCall("c1", "delegate", `{"tasks":[{"id":"del_1","summary":"do the thing","task":"do the thing carefully"}]}`)},
		// del_1's subagent: gated tool call; pause armed while blocked.
		{respond: assistantToolCall("g1", "bash_exec", `{"command":"echo part1","timeout":"5s"}`),
			started: make(chan struct{}), gate: make(chan struct{})},
		// Wave: del_1 continues from its checkpoint and finishes.
		{respond: executorFinishResponse("del_1 done")},
		// The resumed conductor's ONLY LLM call: finish.
		{respond: executorFinishResponse("all done")},
	}}

	emitter := &launchRecorder{}
	rec := &cmRecorder{}
	o := newWaveOrchestrator(t, caller, emitter, rec)
	recStore := &specRecordingStore{}
	o.SetTaskStore(recStore)

	registry := createTestRegistry()
	registry.Register(coretools.NewDelegateTool())
	availableTools := registry.ListFiltered(nil)

	bb := newDelegSpecBB("task-wave-del", recStore)
	bb.SetOriginalRequest("delegate the thing")
	plansDir := t.TempDir()
	ctx := WithComplexity(WithDomain(context.Background(), "general"), 1)

	// --- Run 1: pause mid-del_1 ---
	clearSignal := o.installPauseSignal()
	deps1 := o.buildConductorDeps(nil, nil)
	outCh := make(chan error, 1)
	go func() {
		_, err := RunConductor(ctx, "delegate the thing", bb, availableTools, deps1, plansDir)
		outCh <- err
	}()
	gated := caller.script[1]
	select {
	case <-gated.started:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for the del_1 subagent's gated call")
	}
	o.PauseSession()
	close(gated.gate)
	select {
	case err := <-outCh:
		if !errors.Is(err, agent.ErrPaused) {
			t.Fatalf("run 1 error = %v, want ErrPaused", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for run 1 to pause")
	}
	clearSignal()

	// The spec was persisted during run 1 and the checkpoint is on the bb.
	specs := recStore.recorded()
	if len(specs) != 1 || specs[0].Task.ID != "del_1" {
		t.Fatalf("persisted specs = %+v, want exactly del_1", specs)
	}
	sr, ok := bb.GetStepResult("del_1")
	if !ok || !isPaused(sr.Error) {
		t.Fatalf("del_1 checkpoint = %+v (ok=%v), want paused", sr, ok)
	}
	bb.specs = specs

	// --- Resume: wave relaunches del_1 (same id), then ONE conductor call ---
	res, err := o.Resume(ctx, bb, nil, plansDir, nil, nil, "")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if res.Status != orchestration.ExecutionStatusSuccess {
		t.Fatalf("Resume status = %q, want success", res.Status)
	}

	sr2, ok := bb.GetStepResult("del_1")
	if !ok || sr2.Error != nil || sr2.FullOutput != "del_1 done" {
		t.Fatalf("del_1 after resume = %+v (ok=%v), want completed with output", sr2, ok)
	}

	// del_1 launched exactly twice (run 1 + wave) — the SAME id, so its chat
	// block continues rather than a new one appearing.
	if n := emitter.launchCount("del_1"); n != 2 {
		t.Errorf("SubAgentLaunch for del_1 = %d, want 2 (original + resumed)", n)
	}

	// The wave's del_1 context manager was seeded with the checkpoint step.
	cms := rec.snapshot()
	seededCount := 0
	for _, cm := range cms {
		if s := cm.SeededSteps(); len(s) == 1 && s[0].Action.Name == "bash_exec" {
			seededCount++
		}
	}
	if seededCount != 1 {
		t.Errorf("context managers seeded with the del_1 checkpoint = %d, want exactly 1 (the wave relaunch)", seededCount)
	}

	// The conductor's (last) CM task message carries the wave summary.
	if len(cms) == 0 {
		t.Fatal("no context managers recorded")
	}
	if conductorCM := cms[len(cms)-1]; !strings.Contains(conductorCM.taskDefinition, "Auto-resumed subagents") {
		t.Errorf("conductor task message lacks the wave summary: %q", conductorCM.taskDefinition)
	}

	if got := caller.consumed(); got != len(caller.script) {
		t.Errorf("script consumed %d/%d — run 2 deviated", got, len(caller.script))
	}
}

// TestResumeWave_RepauseMidWaveZeroLLMCalls: the pause re-trips inside the
// wave's resumed subagent. Resume returns paused, the checkpoint is rewritten,
// and the conductor makes ZERO LLM calls.
func TestResumeWave_RepauseMidWaveZeroLLMCalls(t *testing.T) {
	caller := &pauseScriptLLM{script: []pauseScriptStep{
		// Run 1: delegate del_1; its subagent is gated and pauses.
		{respond: assistantToolCall("c1", "delegate", `{"tasks":[{"id":"del_1","summary":"s","task":"do work"}]}`)},
		{respond: assistantToolCall("g1", "bash_exec", `{"command":"echo part1","timeout":"5s"}`),
			started: make(chan struct{}), gate: make(chan struct{})},
		// Wave: the resumed del_1 makes another tool call — gated; the pause
		// re-trips here. NO conductor LLM call follows.
		{respond: assistantToolCall("g2", "bash_exec", `{"command":"echo part2","timeout":"5s"}`),
			started: make(chan struct{}), gate: make(chan struct{})},
	}}

	emitter := &launchRecorder{}
	o := newWaveOrchestrator(t, caller, emitter, nil)
	recStore := &specRecordingStore{}
	o.SetTaskStore(recStore)

	registry := createTestRegistry()
	registry.Register(coretools.NewDelegateTool())
	availableTools := registry.ListFiltered(nil)

	bb := newDelegSpecBB("task-wave-repause", recStore)
	bb.SetOriginalRequest("delegate the work")
	plansDir := t.TempDir()
	ctx := WithComplexity(WithDomain(context.Background(), "general"), 1)

	// --- Run 1: pause mid-del_1 ---
	clearSignal := o.installPauseSignal()
	deps1 := o.buildConductorDeps(nil, nil)
	outCh := make(chan error, 1)
	go func() {
		_, err := RunConductor(ctx, "delegate the work", bb, availableTools, deps1, plansDir)
		outCh <- err
	}()
	g1 := caller.script[1]
	select {
	case <-g1.started:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for del_1's gated call (run 1)")
	}
	o.PauseSession()
	close(g1.gate)
	select {
	case err := <-outCh:
		if !errors.Is(err, agent.ErrPaused) {
			t.Fatalf("run 1 error = %v, want ErrPaused", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for run 1 to pause")
	}
	clearSignal()

	bb.specs = recStore.recorded()

	// --- Resume in a goroutine; re-arm the pause while the wave's del_1 is
	// blocked in its gated call. ---
	resCh := make(chan *HandleResult, 1)
	errCh := make(chan error, 1)
	go func() {
		res, err := o.Resume(ctx, bb, nil, plansDir, nil, nil, "")
		resCh <- res
		errCh <- err
	}()
	g2 := caller.script[2]
	select {
	case <-g2.started:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for the wave's resumed del_1 gated call")
	}
	o.PauseSession()
	close(g2.gate)

	select {
	case res := <-resCh:
		if err := <-errCh; err != nil {
			t.Fatalf("Resume error: %v", err)
		}
		if res == nil || res.Status != orchestration.ExecutionStatusPaused {
			t.Fatalf("Resume result = %+v, want paused", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for the re-paused Resume to return")
	}

	// The checkpoint was rewritten: still paused, now with the longer
	// trajectory (seeded part1 + the new part2 step).
	sr, ok := bb.GetStepResult("del_1")
	if !ok || !isPaused(sr.Error) {
		t.Fatalf("del_1 after re-pause = %+v (ok=%v), want a rewritten paused checkpoint", sr, ok)
	}
	if len(sr.Steps) != 2 {
		t.Fatalf("del_1 checkpoint trajectory = %d steps, want 2 (part1 + part2)", len(sr.Steps))
	}

	// ZERO conductor LLM calls: the script was fully consumed by run 1 + the
	// wave's subagent — entry 3 was the last, and no finish was ever served.
	if got := caller.consumed(); got != 3 {
		t.Errorf("script consumed %d entries, want 3 (no conductor LLM call after the re-pause)", got)
	}
}

// TestResumeWave_NestedChildSettlesBeforeParent: a re-delegating parent and
// its child both pause. The wave settles the CHILD first, appends the child's
// outcome to the parent's task text, then resumes the parent.
func TestResumeWave_NestedChildSettlesBeforeParent(t *testing.T) {
	caller := &pauseScriptLLM{script: []pauseScriptStep{
		// Run 1: the conductor delegates the parent with allow_redelegate.
		{respond: assistantToolCall("c1", "delegate", `{"tasks":[{"id":"del_p","summary":"parent","task":"orchestrate the work","allow_redelegate":true}]}`)},
		// The parent subagent delegates a child.
		{respond: assistantToolCall("p1", "delegate", `{"tasks":[{"id":"del_c","summary":"child","task":"do the sub-work"}]}`)},
		// The child's gated tool call; the pause trips inside the child.
		{respond: assistantToolCall("cc1", "bash_exec", `{"command":"echo childpart","timeout":"5s"}`),
			started: make(chan struct{}), gate: make(chan struct{})},
		// Wave depth 1: the child continues and finishes.
		{respond: executorFinishResponse("child done")},
		// Wave depth 0: the parent continues (with the child outcome in its
		// task text) and finishes.
		{respond: executorFinishResponse("parent done")},
		// The resumed conductor's ONLY LLM call: finish.
		{respond: executorFinishResponse("all done")},
	}}

	emitter := &launchRecorder{}
	rec := &cmRecorder{}
	o := newWaveOrchestrator(t, caller, emitter, rec)
	recStore := &specRecordingStore{}
	o.SetTaskStore(recStore)

	registry := createTestRegistry()
	registry.Register(coretools.NewDelegateTool())
	availableTools := registry.ListFiltered(nil)

	bb := newDelegSpecBB("task-wave-nested", recStore)
	bb.SetOriginalRequest("orchestrate everything")
	plansDir := t.TempDir()
	ctx := WithComplexity(WithDomain(context.Background(), "general"), 2)

	// --- Run 1: pause inside the child ---
	clearSignal := o.installPauseSignal()
	deps1 := o.buildConductorDeps(nil, nil)
	outCh := make(chan error, 1)
	go func() {
		_, err := RunConductor(ctx, "orchestrate everything", bb, availableTools, deps1, plansDir)
		outCh <- err
	}()
	g := caller.script[2]
	select {
	case <-g.started:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for the child's gated call")
	}
	o.PauseSession()
	close(g.gate)
	select {
	case err := <-outCh:
		if !errors.Is(err, agent.ErrPaused) {
			t.Fatalf("run 1 error = %v, want ErrPaused", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for run 1 to pause")
	}
	clearSignal()

	// Both checkpoints exist; both specs were persisted (parent depth 0,
	// child depth 1 with ParentID del_p).
	waitForSpecCount(t, recStore, 2)
	bb.specs = recStore.recorded()
	if sr, ok := bb.GetStepResult("del_c"); !ok || !isPaused(sr.Error) {
		t.Fatalf("del_c checkpoint = %+v (ok=%v), want paused", sr, ok)
	}
	if sr, ok := bb.GetStepResult("del_p"); !ok || !isPaused(sr.Error) {
		t.Fatalf("del_p checkpoint = %+v (ok=%v), want paused", sr, ok)
	}

	// --- Resume: child settles first, then the parent, then ONE conductor call ---
	res, err := o.Resume(ctx, bb, nil, plansDir, nil, nil, "")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if res.Status != orchestration.ExecutionStatusSuccess {
		t.Fatalf("Resume status = %q, want success", res.Status)
	}

	for _, id := range []string{"del_c", "del_p"} {
		if sr, ok := bb.GetStepResult(id); !ok || sr.Error != nil {
			t.Fatalf("%s after resume = %+v (ok=%v), want completed", id, sr, ok)
		}
	}

	// The child settled BEFORE the parent: the child's finish (script entry
	// 4) was consumed before the parent's (entry 5). pauseScriptLLM serves
	// strictly in order, and the parent's CM carries the child's outcome.
	var parentCM *seedableRecordingCM
	for _, cm := range rec.snapshot() {
		if strings.Contains(cm.taskDefinition, "Auto-resumed sub-delegations") {
			parentCM = cm
		}
	}
	if parentCM == nil {
		t.Fatal("no resumed parent context manager carries the sub-delegation outcome summary")
	}
	if !strings.Contains(parentCM.taskDefinition, "del_c: completed") {
		t.Errorf("parent task text lacks the child outcome line: %q", parentCM.taskDefinition)
	}

	if got := caller.consumed(); got != len(caller.script) {
		t.Errorf("script consumed %d/%d — run 2 deviated", got, len(caller.script))
	}
}

// waitForSpecCount polls the recording store until at least want specs have
// been persisted (the async child-registry sink may lag the run's return).
func waitForSpecCount(t *testing.T, store *specRecordingStore, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(store.recorded()) >= want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d persisted delegation specs; got %d", want, len(store.recorded()))
}

// goalTurnMessageRecorder is a goal-turn runner stand-in that records the
// message each turn received and immediately declares the goal met, ending the
// loop after one turn without spinning up the Conductor stack.
type goalTurnMessageRecorder struct {
	mu       sync.Mutex
	messages []string
}

func (r *goalTurnMessageRecorder) run(
	ctx context.Context,
	_ int,
	message string,
	_ orchestration.Blackboard,
	_ []sdktools.ToolDescriptor,
	_ string,
	_ []llm.Message,
	_ conductorDeps,
) (int, *orchestration.ExecutionResult, error) {
	r.mu.Lock()
	r.messages = append(r.messages, message)
	r.mu.Unlock()
	if sink := coretools.GoalStatusSinkFrom(ctx); sink != nil {
		sink.Declare(*metVerdict("done"))
	}
	return 0, &orchestration.ExecutionResult{Status: orchestration.ExecutionStatusSuccess, Output: "goal turn done"}, nil
}

// TestResumeWave_GoalLoopPathSettlesDelegateFirst: a paused goal task with a
// paused delegate resumes through the goal branch — the wave settles the
// delegate BEFORE the first goal turn, and the turn's message carries the
// factual wave summary next to the original request.
func TestResumeWave_GoalLoopPathSettlesDelegateFirst(t *testing.T) {
	caller := &pauseScriptLLM{script: []pauseScriptStep{
		// Run 1: delegate del_1; its subagent is gated and pauses.
		{respond: assistantToolCall("c1", "delegate", `{"tasks":[{"id":"del_1","summary":"s","task":"do work"}]}`)},
		{respond: assistantToolCall("g1", "bash_exec", `{"command":"echo part1","timeout":"5s"}`),
			started: make(chan struct{}), gate: make(chan struct{})},
		// Wave (before the goal loop's first turn): del_1 finishes.
		{respond: executorFinishResponse("del_1 done")},
	}}

	emitter := &launchRecorder{}
	o := newWaveOrchestrator(t, caller, emitter, nil)
	recStore := &specRecordingStore{}
	o.SetTaskStore(recStore)
	recorder := &goalTurnMessageRecorder{}
	o.goalTurnRunner = recorder.run
	// Turn off independent goal verification: the turn runner is a stand-in,
	// and the mock script has no responses for a verifier pass.
	o.config.GoalLoop.Verification = "off"

	registry := createTestRegistry()
	registry.Register(coretools.NewDelegateTool())
	availableTools := registry.ListFiltered(nil)

	bb := newDelegSpecBB("task-wave-goal", recStore)
	bb.SetOriginalRequest("reach the goal")
	plansDir := t.TempDir()
	ctx := WithComplexity(WithDomain(context.Background(), "general"), 1)

	// --- Run 1: pause mid-del_1 ---
	clearSignal := o.installPauseSignal()
	deps1 := o.buildConductorDeps(nil, nil)
	outCh := make(chan error, 1)
	go func() {
		_, err := RunConductor(ctx, "reach the goal", bb, availableTools, deps1, plansDir)
		outCh <- err
	}()
	g := caller.script[1]
	select {
	case <-g.started:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for del_1's gated call (run 1)")
	}
	o.PauseSession()
	close(g.gate)
	select {
	case err := <-outCh:
		if !errors.Is(err, agent.ErrPaused) {
			t.Fatalf("run 1 error = %v, want ErrPaused", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for run 1 to pause")
	}
	clearSignal()

	bb.specs = recStore.recorded()
	gs := &goal.GoalState{Status: goal.StatusActive, Condition: "reach the goal"}

	// --- Resume: the wave settles del_1, then the goal loop's first turn ---
	res, err := o.Resume(ctx, bb, nil, plansDir, nil, gs, "")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if res == nil || res.Status != orchestration.ExecutionStatusSuccess {
		t.Fatalf("Resume result = %+v, want success", res)
	}

	// The delegate settled on the blackboard before the goal turn ran.
	if sr, ok := bb.GetStepResult("del_1"); !ok || sr.Error != nil || sr.FullOutput != "del_1 done" {
		t.Fatalf("del_1 after resume = %+v (ok=%v), want completed with output", sr, ok)
	}

	// Exactly one goal turn; its message carries the summary + original request.
	msgs := recorder.messages
	if len(msgs) != 1 {
		t.Fatalf("goal turns run = %d, want 1", len(msgs))
	}
	if !strings.Contains(msgs[0], "Auto-resumed subagents") {
		t.Errorf("goal turn message lacks the wave summary: %q", msgs[0])
	}
	if !strings.Contains(msgs[0], "reach the goal") {
		t.Errorf("goal turn message lacks the original request: %q", msgs[0])
	}
}

// TestResumeWave_AsyncDelegateSettlesInWave: an async delegation that was
// running in the background when the pause tripped checkpoints through its
// goroutine's select path. On Resume the wave settles it (blocking-in-wave)
// before the conductor's first LLM call, which then sees the summary.
func TestResumeWave_AsyncDelegateSettlesInWave(t *testing.T) {
	caller := &pauseScriptLLM{script: []pauseScriptStep{
		// Run 1: delegate del_1 async (the tool returns immediately with
		// "running in background").
		{respond: assistantToolCall("c1", "delegate", `{"tasks":[{"id":"del_1","summary":"s","task":"do background work","mode":"async"}]}`)},
		// Two gated "background work" calls. Run 1 is a race between the
		// parent conductor (which reaches its own next step boundary right
		// after the async delegate returns) and the just-launched async
		// subagent (its first step) — whoever calls first consumes the next
		// script entry, so a SINGLE gated entry could be stolen by the parent.
		// Providing two gated entries and arming the pause only after BOTH are
		// reached makes the choreography deterministic: both callers are
		// blocked mid-call (past their step boundary) when the release fires,
		// so each observes the armed pause at its NEXT boundary and
		// checkpoints through the goroutine's select path.
		{respond: assistantToolCall("g1", "bash_exec", `{"command":"echo bgpart","timeout":"5s"}`),
			started: make(chan struct{}), gate: make(chan struct{})},
		{respond: assistantToolCall("g2", "bash_exec", `{"command":"echo bgpart","timeout":"5s"}`),
			started: make(chan struct{}), gate: make(chan struct{})},
		// Wave: del_1 continues from its checkpoint and finishes.
		{respond: executorFinishResponse("del_1 done")},
		// The resumed conductor's ONLY LLM call: finish.
		{respond: executorFinishResponse("all done")},
	}}

	emitter := &launchRecorder{}
	rec := &cmRecorder{}
	o := newWaveOrchestrator(t, caller, emitter, rec)
	recStore := &specRecordingStore{}
	o.SetTaskStore(recStore)

	registry := createTestRegistry()
	registry.Register(coretools.NewDelegateTool())
	availableTools := registry.ListFiltered(nil)

	bb := newDelegSpecBB("task-wave-async", recStore)
	bb.SetOriginalRequest("delegate in background")
	plansDir := t.TempDir()
	ctx := WithComplexity(WithDomain(context.Background(), "general"), 1)

	// --- Run 1: the conductor delegates async, then pauses at its own next
	// step boundary while the background subagent is blocked in the gate. ---
	clearSignal := o.installPauseSignal()
	deps1 := o.buildConductorDeps(nil, nil)
	outCh := make(chan error, 1)
	go func() {
		_, err := RunConductor(ctx, "delegate in background", bb, availableTools, deps1, plansDir)
		outCh <- err
	}()
	// Wait until BOTH the parent's next step and the async subagent's first
	// step are blocked in their gates, THEN arm the pause and release them.
	// This removes the run-1 race for the next script entry: both callers have
	// already passed their current step boundary, so both will see the armed
	// pause at their next boundary and checkpoint.
	g1, g2 := caller.script[1], caller.script[2]
	for i, g := range []pauseScriptStep{g1, g2} {
		select {
		case <-g.started:
		case <-time.After(5 * time.Second):
			t.Fatalf("timeout waiting for gated call %d (parent step + async subagent step)", i+1)
		}
	}
	o.PauseSession()
	close(g1.gate)
	close(g2.gate)
	select {
	case err := <-outCh:
		if !errors.Is(err, agent.ErrPaused) {
			t.Fatalf("run 1 error = %v, want ErrPaused", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for run 1 to pause")
	}

	// The async goroutine checkpoints after the gate release — poll for it.
	// The pause signal must STAY armed until then (clearing it early would
	// let the background subagent run past the pause instead of
	// checkpointing — mirroring production, where the signal lives for the
	// whole request).
	deadline := time.Now().Add(5 * time.Second)
	for {
		sr, ok := bb.GetStepResult("del_1")
		if ok && isPaused(sr.Error) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timeout waiting for the async del_1 to write its paused checkpoint")
		}
		time.Sleep(2 * time.Millisecond)
	}
	clearSignal()
	bb.specs = recStore.recorded()

	// --- Resume: the wave settles del_1, then ONE conductor call ---
	res, err := o.Resume(ctx, bb, nil, plansDir, nil, nil, "")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if res.Status != orchestration.ExecutionStatusSuccess {
		t.Fatalf("Resume status = %q, want success", res.Status)
	}
	if sr, ok := bb.GetStepResult("del_1"); !ok || sr.Error != nil || sr.FullOutput != "del_1 done" {
		t.Fatalf("del_1 after resume = %+v (ok=%v), want completed with output", sr, ok)
	}
	if got := caller.consumed(); got != len(caller.script) {
		t.Errorf("script consumed %d/%d — run 2 deviated", got, len(caller.script))
	}
}
