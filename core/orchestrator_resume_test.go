package core

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/agent/router"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
)

// seedableRecordingCM embeds mockContextManager and additionally implements
// orchestration.StepSeedable so the Conductor seeds the resumed trajectory
// into it. It records the seeded steps for assertions, proving end-to-end that
// Resume → runConductor → RunConductor set ConductorConfig.ResumeSteps (the
// Conductor only calls SeedSteps when ResumeSteps is non-empty).
type seedableRecordingCM struct {
	mockContextManager
	seededMu    sync.Mutex
	seededSteps []agent.Step
}

func (m *seedableRecordingCM) SeedSteps(steps []agent.Step) {
	m.seededMu.Lock()
	defer m.seededMu.Unlock()
	m.seededSteps = steps
}

func (m *seedableRecordingCM) SeededSteps() []agent.Step {
	m.seededMu.Lock()
	defer m.seededMu.Unlock()
	return m.seededSteps
}

// newResumeTestOrchestrator builds an orchestrator wired for resume tests. The
// context factory captures the last-created seedable CM in *cmOut so the test
// can assert on the seeded steps. The spyEmitter captures the Routing event.
func newResumeTestOrchestrator(t *testing.T, mockLLM *mockLLMCaller, emitter *spyEmitter, cmOut **seedableRecordingCM) *Orchestrator {
	t.Helper()
	registry := createTestRegistry()
	cf := func(systemPrompt string, _ llm.ModelMetadata, _ string, _ ...orchestration.PruningOverride) ContextManager {
		cm := &seedableRecordingCM{mockContextManager: mockContextManager{systemPrompt: systemPrompt}}
		if cmOut != nil {
			*cmOut = cm
		}
		return cm
	}
	return NewOrchestrator(OrchestratorConfig{}, OrchestratorDeps{
		Router:         newCoreRouter(mockLLM, 5),
		LLM:            mockLLM,
		ToolExec:       registry,
		ToolRegistry:   registry,
		TokenCounter:   llm.NewSimpleTokenCounter(),
		ContextFactory: cf,
		Emitter:        emitter,
		CircuitBreaker: defaultCircuitBreakerConfig,
	})
}

// resumeStepsFixture builds n prior ReAct steps with a recognizable observation
// marker so tests can assert the trajectory reached the executor.
func resumeStepsFixture(n int) []agent.Step {
	steps := make([]agent.Step, 0, n)
	for i := range n {
		steps = append(steps, agent.Step{
			Thought:     "prior reasoning",
			Action:      llm.ToolCall{ID: "pc" + itoa(i), Name: "read_file", Input: json.RawMessage(`{}`)},
			Observation: "PRIOR-OBS-" + itoa(i),
		})
	}
	return steps
}

func itoa(i int) string {
	const digits = "0123456789"
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{digits[i%10]}, b...)
		i /= 10
	}
	return string(b)
}

// routingCall finds the first recorded "Routing" call in a spyEmitter and
// returns its (mode, domain, complexity) arguments.
func routingCall(s *spyEmitter) (mode, domain, complexity string, ok bool) {
	for _, c := range s.calls {
		if c.method == "Routing" && len(c.args) >= 3 {
			m, _ := c.args[0].(string)
			d, _ := c.args[1].(string)
			cx, _ := c.args[2].(string)
			return m, d, cx, true
		}
	}
	return "", "", "", false
}

// finishingStep returns the step number reported by the Finishing event, or 0
// if no Finishing event was emitted.
func finishingStep(s *spyEmitter) int {
	for _, c := range s.calls {
		if c.method == "Finishing" && len(c.args) >= 1 {
			if n, ok := c.args[0].(int); ok {
				return n
			}
		}
	}
	return 0
}

// TestResume_ContinuesStepCounterFromTrajectory verifies that the resumed
// executor's step counter continues from len(resumeSteps)+1, proving the
// trajectory is threaded through Resume → runConductor → the executor.
func TestResume_ContinuesStepCounterFromTrajectory(t *testing.T) {
	mockLLM := &mockLLMCaller{responses: []*llm.ChatResponse{
		executorFinishResponse("resumed output"),
	}}
	emitter := &spyEmitter{}
	var cm *seedableRecordingCM
	orch := newResumeTestOrchestrator(t, mockLLM, emitter, &cm)

	bb := orchestration.NewMapBlackboard()
	bb.SetOriginalRequest("long running task")

	const prior = 3
	steps := resumeStepsFixture(prior)
	result, err := orch.Resume(context.Background(), bb, nil, "", steps, nil, "")
	if err != nil {
		t.Fatalf("Resume failed: %v", err)
	}
	if result.Output != "resumed output" {
		t.Fatalf("unexpected output: %q", result.Output)
	}

	// The executor should finish at step prior+1, not step 1.
	if got := finishingStep(emitter); got != prior+1 {
		t.Fatalf("step counter: expected finish at step %d, got %d", prior+1, got)
	}
}

// TestResume_SeedsContextManagerWithTrajectory verifies that the resumed
// trajectory is seeded into the ContextManager via StepSeedable, so the full
// prior trajectory appears in the context window. This exercises the complete
// Resume → ConductorConfig.ResumeSteps → Conductor.SeedSteps chain.
func TestResume_SeedsContextManagerWithTrajectory(t *testing.T) {
	mockLLM := &mockLLMCaller{responses: []*llm.ChatResponse{
		executorFinishResponse("done"),
	}}
	emitter := &spyEmitter{}
	var cm *seedableRecordingCM
	orch := newResumeTestOrchestrator(t, mockLLM, emitter, &cm)

	bb := orchestration.NewMapBlackboard()
	bb.SetOriginalRequest("the task")

	steps := resumeStepsFixture(2)
	if _, err := orch.Resume(context.Background(), bb, nil, "", steps, nil, ""); err != nil {
		t.Fatalf("Resume failed: %v", err)
	}

	if cm == nil {
		t.Fatal("context manager was never created")
	}
	seeded := cm.SeededSteps()
	if len(seeded) != len(steps) {
		t.Fatalf("expected %d seeded steps, got %d", len(steps), len(seeded))
	}
	if seeded[0].Observation != steps[0].Observation {
		t.Errorf("first seeded step observation = %q, want %q", seeded[0].Observation, steps[0].Observation)
	}
}

// TestResume_DefaultsDomainWithoutRouting verifies that when no routing decision
// was persisted, Resume defaults to the "general" domain (and a standard
// Conductor complexity) instead of re-routing or failing.
// TestResume_DefaultsDomainWithoutRouting verifies that when no routing decision
// was persisted, Resume defaults to the "general" domain (applied to the
// conductor context internally) instead of re-routing or failing. It must NOT
// emit a Routing event: the task is not re-routed, and the previous
// display-only emit was removed as misleading.
func TestResume_DefaultsDomainWithoutRouting(t *testing.T) {
	mockLLM := &mockLLMCaller{responses: []*llm.ChatResponse{
		executorFinishResponse("ok"),
	}}
	emitter := &spyEmitter{}
	orch := newResumeTestOrchestrator(t, mockLLM, emitter, nil)

	bb := orchestration.NewMapBlackboard()
	bb.SetOriginalRequest("task without routing")

	if _, err := orch.Resume(context.Background(), bb, nil, "", nil, nil, ""); err != nil {
		t.Fatalf("Resume failed: %v", err)
	}

	if _, _, _, ok := routingCall(emitter); ok {
		t.Fatal("resume must not emit a Routing event (the task is not re-routed)")
	}
}

// TestResume_ReusesRoutingDomain verifies that a persisted routing decision is
// reused (applied to the conductor context) rather than re-routing. It must NOT
// emit a Routing event — the display-only resume emit was removed.
func TestResume_ReusesRoutingDomain(t *testing.T) {
	mockLLM := &mockLLMCaller{responses: []*llm.ChatResponse{
		executorFinishResponse("ok"),
	}}
	emitter := &spyEmitter{}
	orch := newResumeTestOrchestrator(t, mockLLM, emitter, nil)

	bb := orchestration.NewMapBlackboard()
	bb.SetOriginalRequest("research task")

	routing := &router.RoutingDecision{Domain: "research", Complexity: 5}
	if _, err := orch.Resume(context.Background(), bb, routing, "", nil, nil, ""); err != nil {
		t.Fatalf("Resume failed: %v", err)
	}

	if _, _, _, ok := routingCall(emitter); ok {
		t.Fatal("resume must not emit a Routing event (routing decision is reused, not re-routed)")
	}
}

// TestResume_WorksWithoutPlanAndRouting verifies the central acceptance
// criterion: resume succeeds when there is no plan and no routing decision.
// Previously ResumeTask rejected such tasks; now the Conductor runs them via a
// standalone checklist.
func TestResume_WorksWithoutPlanAndRouting(t *testing.T) {
	mockLLM := &mockLLMCaller{responses: []*llm.ChatResponse{
		executorFinishResponse("plan-less resume done"),
	}}
	emitter := &spyEmitter{}
	orch := newResumeTestOrchestrator(t, mockLLM, emitter, nil)

	// Blackboard with NO plan and NO routing — just facts and the original
	// request, exactly what RestoreBlackboard produces for a plan-less task.
	bb := orchestration.NewMapBlackboard()
	bb.SetOriginalRequest("plan-less task")
	bb.StoreFact(orchestration.Fact{Content: "a restored fact", Keywords: []string{"restored"}})

	result, err := orch.Resume(context.Background(), bb, nil, "", nil, nil, "")
	if err != nil {
		t.Fatalf("Resume without plan/routing failed: %v", err)
	}
	if !strings.Contains(result.Output, "plan-less resume done") {
		t.Fatalf("unexpected output: %q", result.Output)
	}
}

// TestResume_EmptyTrajectoryFallback verifies that a resume with no persisted
// trajectory (nil resumeSteps) degrades gracefully to a fresh-start executor
// that begins at step 1.
func TestResume_EmptyTrajectoryFallback(t *testing.T) {
	mockLLM := &mockLLMCaller{responses: []*llm.ChatResponse{
		executorFinishResponse("fresh start"),
	}}
	emitter := &spyEmitter{}
	var cm *seedableRecordingCM
	orch := newResumeTestOrchestrator(t, mockLLM, emitter, &cm)

	bb := orchestration.NewMapBlackboard()
	bb.SetOriginalRequest("no trajectory")

	result, err := orch.Resume(context.Background(), bb, nil, "", nil, nil, "")
	if err != nil {
		t.Fatalf("Resume failed: %v", err)
	}
	if result.Output != "fresh start" {
		t.Fatalf("unexpected output: %q", result.Output)
	}

	// Fresh start → first executor step is 1.
	if got := finishingStep(emitter); got != 1 {
		t.Fatalf("expected finish at step 1 (fresh start), got %d", got)
	}
	// No steps seeded.
	if cm != nil && len(cm.SeededSteps()) != 0 {
		t.Fatalf("expected no seeded steps, got %d", len(cm.SeededSteps()))
	}
}
