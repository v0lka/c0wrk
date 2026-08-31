package core

// Full-cycle cooperative-pause tests at the RunConductor level: a declared
// plan is executing when the pause signal trips mid-plan (inside a plan-step
// subagent). The run ends as a clean paused checkpoint (never-started steps
// stay pending, the partial trajectory is checkpointed on the blackboard).
// The resumed run — seeded continuable via deps.resumedWithPlan, exactly like
// Orchestrator.Resume does — continues the SAME plan without a re-declare and
// drives every step to a terminal state.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coretools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
)

// pauseScriptStep is one entry of the pause-cycle LLM script: the response to
// return, plus optional synchronization. gate blocks the (subagent) caller
// until the test releases it — the window in which the test arms the pause
// signal — and started is closed when the caller reaches this entry, so the
// test knows the run is blocked mid-plan.
type pauseScriptStep struct {
	respond *llm.ChatResponse
	started chan struct{}
	gate    chan struct{}
}

// pauseScriptLLM plays a fixed call sequence across BOTH conductor runs (the
// mock caller is shared by the Conductor loop and every plan-step subagent).
// An out-of-script call is a hard error: the run deviated from the
// pause/resume choreography (e.g. an extra Conductor turn), and the failing
// run's assertions will surface it.
type pauseScriptLLM struct {
	mu     sync.Mutex
	script []pauseScriptStep
	idx    int
}

func (s *pauseScriptLLM) Call(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	s.mu.Lock()
	i := s.idx
	if i >= len(s.script) {
		s.mu.Unlock()
		return nil, fmt.Errorf("pauseScriptLLM: script exhausted at call %d — the run made an unexpected extra LLM call", i+1)
	}
	step := s.script[i]
	s.idx++
	s.mu.Unlock()
	if step.started != nil {
		close(step.started)
	}
	if step.gate != nil {
		<-step.gate
	}
	return step.respond, nil
}

// consumed returns how many script entries have been served.
func (s *pauseScriptLLM) consumed() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.idx
}

// assistantToolCall builds a tool_use assistant response with a single call.
func assistantToolCall(id, name, input string) *llm.ChatResponse {
	return &llm.ChatResponse{
		Message: llm.Message{
			Role:      "assistant",
			Content:   "calling " + name,
			ToolCalls: []llm.ToolCall{{ID: id, Name: name, Input: json.RawMessage(input)}},
		},
		StopReason: "tool_use",
	}
}

// pauseCycleEmitter is a mockEmitter that (a) counts PlanGenerated calls —
// the re-publication detector — and (b) supports WithPlanStepID so
// scopePlanStepEvents wires the real planStepEventTranslator instead of
// NoopEvents (mockEmitter alone does not implement the scoping capability).
type pauseCycleEmitter struct {
	mockEmitter
	planGenerated int
}

func (e *pauseCycleEmitter) PlanGenerated(_ int, _ []orchestration.PlanStepEvent) {
	e.planGenerated++
}

func (e *pauseCycleEmitter) WithPlanStepID(string) Emitter {
	return &pauseCycleEmitter{}
}

// cmRecorder records every ContextManager the orchestrator creates (the
// Conductor's own and each plan-step subagent's), guarded against the
// concurrent goroutines that create them.
type cmRecorder struct {
	mu  sync.Mutex
	cms []*seedableRecordingCM
}

func (r *cmRecorder) add(cm *seedableRecordingCM) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cms = append(r.cms, cm)
}

func (r *cmRecorder) withTaskSubstring(substr string) []*seedableRecordingCM {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*seedableRecordingCM
	for _, cm := range r.cms {
		if strings.Contains(cm.systemPrompt, substr) || strings.Contains(cm.taskDefinition, substr) {
			out = append(out, cm)
		}
	}
	return out
}

// newPauseCycleOrchestrator builds an orchestrator whose registry carries the
// real declare_plan/execute_plan tools (plus the mock bash_exec from
// createTestRegistry) and whose context factory records every created
// seedable CM so the test can assert on checkpoint seeding.
func newPauseCycleOrchestrator(t *testing.T, caller agent.LLMCaller, emitter *pauseCycleEmitter, rec *cmRecorder) *Orchestrator {
	t.Helper()
	registry := createTestRegistry()
	registry.Register(coretools.NewDeclarePlanTool(nil))
	registry.Register(coretools.NewExecutePlanTool())
	cf := func(systemPrompt string, _ llm.ModelMetadata, _ string, _ ...orchestration.PruningOverride) ContextManager {
		cm := &seedableRecordingCM{mockContextManager: mockContextManager{systemPrompt: systemPrompt}}
		rec.add(cm)
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

// TestRunConductor_PauseMidPlan_ResumeCompletesAllStepsTerminal is the
// full-cycle conductor scenario: declare_plan → execute_plan → the first
// step's subagent is paused mid-work by the universal pause signal → the run
// ends as a paused checkpoint (s1 checkpointed with its partial trajectory,
// s2 untouched and pending) → the resumed continuable run continues the SAME
// plan without a re-declare (no second PlanGenerated) and completes every
// step successfully.
func TestRunConductor_PauseMidPlan_ResumeCompletesAllStepsTerminal(t *testing.T) {
	caller := &pauseScriptLLM{script: []pauseScriptStep{
		// Run 1 (declared plan): publish the roadmap, start executing it.
		{respond: assistantToolCall("c1", "declare_plan", `{"tasks":[
			{"id":"s1","summary":"Do the groundwork","description":"s1: do the groundwork"},
			{"id":"s2","summary":"Finish the build","description":"s2: finish the build","depends_on":["s1"]}]}`)},
		{respond: assistantToolCall("c2", "execute_plan", `{}`)},
		// s1's subagent: its first LLM call is gated — the test arms the pause
		// signal while the subagent is blocked here, then lets it return a
		// real tool call so the pause trips at the NEXT step boundary with a
		// non-empty partial trajectory.
		{respond: assistantToolCall("g1", "bash_exec", `{"command":"echo groundwork","timeout":"5s"}`),
			started: make(chan struct{}), gate: make(chan struct{})},
		// Run 2 (continuable resume): execute_plan continues — NO declare_plan.
		{respond: assistantToolCall("c3", "execute_plan", `{}`)},
		{respond: executorFinishResponse("s1 resumed done")},
		{respond: executorFinishResponse("s2 done")},
		{respond: executorFinishResponse("plan finished")},
	}}

	emitter := &pauseCycleEmitter{}
	rec := &cmRecorder{}
	o := newPauseCycleOrchestrator(t, caller, emitter, rec)

	registry := createTestRegistry()
	registry.Register(coretools.NewDeclarePlanTool(nil))
	registry.Register(coretools.NewExecutePlanTool())
	availableTools := registry.ListFiltered(nil)

	bb := orchestration.NewMapBlackboard()
	plansDir := t.TempDir()
	ctx := WithComplexity(WithDomain(context.Background(), "general"), 2)

	// The universal pause signal, armed by the test mid-plan (production
	// wires Orchestrator.PauseSession through newPauseChecker; the override
	// here gives the test direct control of the same seam).
	var armed atomic.Bool
	pauseChecker := func(context.Context) bool { return armed.Load() }

	// --- Run 1: declared plan, paused mid-execution ---
	deps1 := o.buildConductorDeps(nil, nil)
	deps1.pauseChecker = pauseChecker

	type runOutcome struct {
		result *orchestration.ExecutionResult
		err    error
	}
	outCh := make(chan runOutcome, 1)
	go func() {
		result, err := RunConductor(ctx, "execute the roadmap for the widget refactor", bb, availableTools, deps1, plansDir)
		outCh <- runOutcome{result, err}
	}()

	gated := caller.script[2]
	select {
	case <-gated.started:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for the s1 subagent to reach its (gated) LLM call")
	}
	armed.Store(true)
	close(gated.gate)

	var out runOutcome
	select {
	case out = <-outCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for run 1 to end at the pause checkpoint")
	}
	if !errors.Is(out.err, agent.ErrPaused) {
		t.Fatalf("run 1 error = %v, want ErrPaused (the pause must end the run cooperatively)", out.err)
	}
	if out.result == nil || out.result.Status != orchestration.ExecutionStatusPaused {
		t.Fatalf("run 1 status = %+v, want %q", out.result, orchestration.ExecutionStatusPaused)
	}

	// s1 carries the paused checkpoint with its partial trajectory; s2 was
	// never dispatched and has no result — it stays pending for the resume.
	sr1, ok := bb.GetStepResult("s1")
	if !ok || !isPaused(sr1.Error) {
		t.Fatalf("s1 StepResult = %+v (ok=%v), want a paused checkpoint", sr1, ok)
	}
	if len(sr1.Steps) != 1 || sr1.Steps[0].Action.Name != "bash_exec" {
		t.Fatalf("s1 checkpoint trajectory = %+v, want exactly the completed bash_exec step", sr1.Steps)
	}
	if _, ok := bb.GetStepResult("s2"); ok {
		t.Error("s2 must have no StepResult after the mid-plan pause (never-started steps stay pending)")
	}

	// Exactly one plan publication so far, the paused step got a
	// plan_step_paused (not a terminal complete), and the finish fallback did
	// NOT sweep the pending s2 into a terminal state.
	if emitter.planGenerated != 1 {
		t.Fatalf("PlanGenerated calls = %d, want 1", emitter.planGenerated)
	}
	if n := len(emitter.planStepPaused); n != 1 {
		t.Fatalf("PlanStepPaused calls = %d, want 1 (s1 must surface as paused)", n)
	}
	if n := len(emitter.planStepCompletes); n != 0 {
		t.Fatalf("PlanStepComplete calls = %d, want 0 (a pause is not terminal — nothing may be swept)", n)
	}

	// --- Run 2: continuable resume continues the SAME plan, no re-declare ---
	armed.Store(false)
	deps2 := o.buildConductorDeps(nil, nil)
	deps2.pauseChecker = pauseChecker
	deps2.resumedWithPlan = true // what Orchestrator.Resume seeds for this bb

	result2, err2 := RunConductor(ctx, "execute the roadmap for the widget refactor", bb, availableTools, deps2, plansDir)
	if err2 != nil {
		t.Fatalf("run 2 (resume) error = %v, want nil", err2)
	}
	if result2 == nil || result2.Status != orchestration.ExecutionStatusSuccess {
		t.Fatalf("run 2 status = %+v, want success", result2)
	}

	// Every step reached a terminal, error-free state.
	sr1b, ok := bb.GetStepResult("s1")
	if !ok || sr1b.Error != nil || sr1b.FullOutput != "s1 resumed done" {
		t.Fatalf("s1 after resume = %+v (ok=%v), want a successful step with the resumed output", sr1b, ok)
	}
	sr2, ok := bb.GetStepResult("s2")
	if !ok || sr2.Error != nil || sr2.FullOutput != "s2 done" {
		t.Fatalf("s2 after resume = %+v (ok=%v), want a successful step", sr2, ok)
	}

	// No re-publication: still exactly one PlanGenerated, and the resumed run
	// emitted terminal completes for BOTH steps (s1 re-ran from its
	// checkpoint, s2 finally ran).
	if emitter.planGenerated != 1 {
		t.Errorf("PlanGenerated calls after resume = %d, want 1 (the resumed run must continue, not re-declare)", emitter.planGenerated)
	}
	completedIDs := map[string]bool{}
	for _, pc := range emitter.planStepCompletes {
		if !pc.success {
			t.Errorf("PlanStepComplete for %q reported failure (errMsg %q)", pc.stepID, pc.errMsg)
		}
		completedIDs[pc.stepID] = true
	}
	if !completedIDs["s1"] || !completedIDs["s2"] {
		t.Errorf("terminal plan_step_complete seen for %v, want both s1 and s2", completedIDs)
	}

	// The resumed s1 subagent picked up its checkpoint: its context manager
	// was seeded with the partial trajectory from run 1.
	groundworkCMs := rec.withTaskSubstring("do the groundwork")
	if len(groundworkCMs) != 2 {
		t.Fatalf("s1 context managers created = %d, want 2 (one per run)", len(groundworkCMs))
	}
	seeded := groundworkCMs[1].SeededSteps()
	if len(seeded) != 1 || seeded[0].Action.Name != "bash_exec" {
		t.Fatalf("resumed s1 subagent seeded steps = %+v, want the run-1 bash_exec checkpoint step", seeded)
	}

	// The whole choreography consumed the script exactly: no unexpected extra
	// Conductor/subagent turn happened in either run.
	if got := caller.consumed(); got != len(caller.script) {
		t.Errorf("script consumed %d/%d entries — the runs deviated from the choreography", got, len(caller.script))
	}
}
