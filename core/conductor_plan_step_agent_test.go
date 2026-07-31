package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agents"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// appendCaptureContextFactory is like captureContextFactory but appends each
// assembled system prompt to a slice (in call order), so a test can compare the
// prompts built for several plan steps in a single wave. It returns a working
// mockContextManager so the produced executor can actually run.
func appendCaptureContextFactory(captured *[]string) ContextManagerFactory {
	return func(systemPrompt string, _ llm.ModelMetadata, _ string, _ ...orchestration.PruningOverride) ContextManager {
		*captured = append(*captured, systemPrompt)
		return &mockContextManager{systemPrompt: systemPrompt}
	}
}

// --- declare_plan → PlanStep wiring ---

// TestPublish_PropagatesAgentToPlanStep verifies that conductorPublisher.Publish
// copies PlanTaskInput.Agent onto the resulting PlanStep, and that a task
// without an agent leaves PlanStep.Agent empty (no regression). This is the
// declare_plan boundary: the `agent` field a plan author sets on a step must
// reach the persisted/executed PlanStep.
func TestPublish_PropagatesAgentToPlanStep(t *testing.T) {
	bb := orchestration.NewMapBlackboard()
	p := &conductorPublisher{
		emitter:  &mockEmitter{},
		bb:       bb,
		plansDir: t.TempDir(),
	}

	tasks := []tools.PlanTaskInput{
		{ID: "review", Summary: "Code review", Description: "Review the PR", Agent: "code-reviewer"},
		{ID: "build", Summary: "Build", Description: "Build the project"},
	}
	if _, err := p.Publish(context.Background(), tasks); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	plan := bb.GetPlan()
	if plan == nil || len(plan.Steps) != 2 {
		t.Fatalf("expected 2 plan steps, got %v", plan)
	}
	if plan.Steps[0].Agent != "code-reviewer" {
		t.Errorf("step %q Agent = %q, want %q", plan.Steps[0].ID, plan.Steps[0].Agent, "code-reviewer")
	}
	if plan.Steps[1].Agent != "" {
		t.Errorf("step without agent must have empty Agent, got %q", plan.Steps[1].Agent)
	}
}

// --- serialization / restoration ---

// TestPlanStep_AgentSurvivesJSONRoundTrip verifies that a declared step's Agent
// field survives the JSON persistence used by the PersistentBlackboard
// (json.Marshal/Unmarshal of *orchestration.Plan) and is therefore present when
// a task is continued from a checkpoint. This covers the persistence/restore
// acceptance criterion for declared plan steps.
func TestPlanStep_AgentSurvivesJSONRoundTrip(t *testing.T) {
	original := &orchestration.Plan{Steps: []orchestration.PlanStep{
		{ID: "review", Summary: "Code review", Description: "Review", Agent: "code-reviewer"},
		{ID: "build", Summary: "Build", Description: "Build"},
	}}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var restored orchestration.Plan
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if restored.Steps[0].Agent != "code-reviewer" {
		t.Errorf("agent step did not survive round-trip: got Agent=%q, want %q", restored.Steps[0].Agent, "code-reviewer")
	}
	if restored.Steps[1].Agent != "" {
		t.Errorf("plain step should have empty Agent after round-trip, got %q", restored.Steps[1].Agent)
	}
}

// --- plan step execution wiring (the core acceptance criterion) ---

// TestDefaultPlanStepWave_AgentStepProducesDifferentPrompt verifies that a
// declared plan step carrying agent:"code-reviewer" executes with that profile's
// system prompt (its body becomes the core directive), while a step without an
// agent uses the standard orchestrator prompt — i.e. the two produce DIFFERENT
// prompts. This exercises the production defaultPlanStepWave wiring
// (PlanStep.Agent → DelegationTask.Agent → buildSubAgentTask profile branch)
// end-to-end, through the same path the Conductor uses at runtime.
func TestDefaultPlanStepWave_AgentStepProducesDifferentPrompt(t *testing.T) {
	const directiveMarker = "YOU ARE A RUTHLESS CODE REVIEWER: INSPECT WITH EXTREME PREJUDICE"

	resolver := func(name string) (*agents.Agent, bool) {
		if name == "code-reviewer" {
			return testAgentProfile(directiveMarker, agents.AgentMetadata{Name: "code-reviewer"}), true
		}
		return nil, false
	}

	ctx := tools.WithAgentResolver(
		WithComplexity(WithDomain(sdktools.WithWorkspacePath(context.Background(), "/ws"), "code"), 5),
		resolver,
	)

	var captured []string
	l := &conductorLauncher{deps: conductorDeps{
		contextFactory: appendCaptureContextFactory(&captured),
		toolRegistry:   newSubagentTestRegistry(subagentToolSet()),
		llm:            &mockLLMCaller{}, // returns end_turn → executor terminates after one iteration
		toolExec:       &mockToolExecutor{},
		emitter:        &mockEmitter{},
	}}

	ready := []orchestration.PlanStep{
		{ID: "agent_step", Summary: "Review", Description: "Review the PR", Agent: "code-reviewer"},
		{ID: "plain_step", Summary: "Build", Description: "Build the project"},
	}
	registry := tools.NewDelegationRegistry()
	_ = l.defaultPlanStepWave(ctx, ready, registry)

	if len(captured) != 2 {
		t.Fatalf("expected 2 captured prompts (one per step), got %d", len(captured))
	}
	agentPrompt, plainPrompt := captured[0], captured[1]

	// The agent step's profile body must be its core directive.
	if !strings.Contains(agentPrompt, directiveMarker) {
		t.Errorf("agent step must run with the profile body as its core directive;\nprompt missing marker %q\n--- prompt ---\n%s", directiveMarker, agentPrompt)
	}
	// The plain step must NOT carry the specialized directive.
	if strings.Contains(plainPrompt, directiveMarker) {
		t.Errorf("plain step must use the standard orchestrator prompt, not the reviewer directive;\nprompt unexpectedly contains %q", directiveMarker)
	}
	// The two prompts must differ — this is the acceptance criterion.
	if agentPrompt == plainPrompt {
		t.Error("agent step and plain step produced identical system prompts; agent profile was not applied to the plan step")
	}
}
