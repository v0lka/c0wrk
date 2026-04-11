package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/user/agent/sdk/agent"
	"github.com/user/agent/sdk/llm"
	tools "github.com/user/agent/sdk/tools"
)

// mockTool implements tools.Tool for testing.
type mockTool struct {
	name        string
	description string
	result      tools.ToolResult
}

func (m *mockTool) Name() string                    { return m.name }
func (m *mockTool) Description() string             { return m.description }
func (m *mockTool) InputSchema() json.RawMessage    { return json.RawMessage(`{"type":"object"}`) }
func (m *mockTool) DefaultPolicy() tools.ToolPolicy { return tools.PolicyAlwaysAllow }
func (m *mockTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	return m.result, nil
}

// createTestRegistry creates a ToolRegistry with mock tools for testing.
func createTestRegistry() *tools.ToolRegistry {
	reg := tools.NewToolRegistry()
	// Register a mock bash_exec tool
	reg.Register(&mockTool{
		name:        "bash_exec",
		description: "Execute bash commands",
		result:      tools.ToolResult{Content: "PASSED:Build succeeded", IsError: false},
	})
	// Register a mock file_ops tool
	reg.Register(&mockTool{
		name:        "file_ops",
		description: "File operations",
		result:      tools.ToolResult{Content: "File written", IsError: false},
	})
	return reg
}

// Helper to create a context factory for tests
func testContextFactory(systemPrompt string, modelMeta llm.ModelMetadata, compactionStrategy string) ContextManager {
	return &mockContextManager{
		systemPrompt:   systemPrompt,
		taskDefinition: "",
	}
}

// TestOrchestrator_NeedsClarificationMode tests the needs_clarification mode.
func TestOrchestrator_NeedsClarificationMode(t *testing.T) {
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			// Router returns needs_clarification
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:    "assistant",
					Content: `{"domain": "general", "complexity": 1, "compaction_strategy": "sliding_window", "suggested_tools": [], "needs_clarification": true}`,
				},
				StopReason: "end_turn",
			}, nil
		},
	}

	registry := createTestRegistry()
	counter := llm.NewSimpleTokenCounter()

	router := NewRouter(mockLLM, 5)
	planner := NewPlanner(mockLLM)

	orchestrator := NewOrchestrator(
		router,
		planner,
		mockLLM,
		registry,
		registry,
		counter,
		OrchestratorConfig{
			MaxSteps: 10,
		},
		testContextFactory,
		nil, // reflector - nil for tests
		nil, // logger - nil for tests
		nil, // emitter - nil for tests
		nil, // modelRegistry - nil for tests
		ToolResultBudget{},
		nil, // bbFactory - nil for tests
	)

	result, err := orchestrator.Handle(context.Background(), "do something")
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	if result.RoutingDecision == nil {
		t.Fatal("routing decision is nil")
	}

	// Should return clarification request
	if result.Output == "" {
		t.Error("needs_clarification should return a clarification message")
	}

	expectedMsg := "I need more information to help you. Could you please clarify your request?"
	if result.Output != expectedMsg {
		t.Errorf("expected clarification message, got: %s", result.Output)
	}

	// Should not have plan
	if result.Plan != nil {
		t.Error("needs_clarification should not have plan")
	}
}

// TestOrchestrator_PlanExecuteMode tests the plan_execute mode path.
func TestOrchestrator_PlanExecuteMode(t *testing.T) {
	callIdx := 0
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callIdx++
			switch callIdx {
			case 1: // Router
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"domain": "code", "complexity": 4, "compaction_strategy": "sliding_window", "suggested_tools": ["bash_exec", "file_ops"], "needs_clarification": false}`,
					},
					StopReason: "end_turn",
				}, nil
			case 2: // Planner
				return &llm.ChatResponse{
					Message: llm.Message{
						Role: "assistant",
						Content: `{"steps": [
							{"id": "step_1", "description": "Write tests", "depends_on": [], "parallelizable": false, "estimated_tools": ["file_ops"]},
							{"id": "step_2", "description": "Run tests", "depends_on": ["step_1"], "parallelizable": false, "estimated_tools": ["bash_exec"]}
						]}`,
					},
					StopReason: "end_turn",
				}, nil
			case 3: // Executor for step_1 - finish
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: "Created test file",
						ToolCalls: []llm.ToolCall{
							{
								ID:    "call_1",
								Name:  "finish",
								Input: json.RawMessage(`{"answer": "Tests written"}`),
							},
						},
					},
					StopReason: "tool_use",
				}, nil
			case 4: // Executor for step_2 - finish
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: "Running tests",
						ToolCalls: []llm.ToolCall{
							{
								ID:    "call_2",
								Name:  "finish",
								Input: json.RawMessage(`{"answer": "All tests passed"}`),
							},
						},
					},
					StopReason: "tool_use",
				}, nil
			default:
				return &llm.ChatResponse{
					Message:    llm.Message{Role: "assistant", Content: ""},
					StopReason: "end_turn",
				}, nil
			}
		},
	}

	registry := createTestRegistry()
	counter := llm.NewSimpleTokenCounter()

	router := NewRouter(mockLLM, 5)
	planner := NewPlanner(mockLLM)

	orchestrator := NewOrchestrator(
		router,
		planner,
		mockLLM,
		registry,
		registry,
		counter,
		OrchestratorConfig{
			MaxSteps: 10,
		},
		testContextFactory,
		nil, // reflector - nil for tests
		nil, // logger - nil for tests
		nil, // emitter - nil for tests
		nil, // modelRegistry - nil for tests
		ToolResultBudget{},
		nil, // bbFactory - nil for tests
	)

	result, err := orchestrator.Handle(context.Background(), "Implement and test a new feature")
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	if result.RoutingDecision == nil {
		t.Fatal("routing decision is nil")
	}

	// Plan execute mode should have a plan
	if result.Plan == nil {
		t.Fatal("plan_execute mode should have plan")
	}

	if len(result.Plan.Steps) != 2 {
		t.Errorf("expected 2 plan steps, got %d", len(result.Plan.Steps))
	}

	// Verify step dependencies
	if len(result.Plan.Steps) >= 2 {
		step1 := result.Plan.Steps[0]
		step2 := result.Plan.Steps[1]

		if step1.ID != "step_1" {
			t.Errorf("expected first step id=step_1, got %s", step1.ID)
		}

		if step2.ID != "step_2" {
			t.Errorf("expected second step id=step_2, got %s", step2.ID)
		}

		if len(step2.DependsOn) == 0 || step2.DependsOn[0] != "step_1" {
			t.Errorf("step_2 should depend on step_1")
		}
	}
}

// TestOrchestrator_HandleResultContainsRoutingDecision verifies HandleResult always has routing info.
func TestOrchestrator_HandleResultContainsRoutingDecision(t *testing.T) {
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			// Router
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:    "assistant",
					Content: `{"domain": "general", "complexity": 2, "compaction_strategy": "sliding_window", "suggested_tools": [], "needs_clarification": false}`,
				},
				StopReason: "end_turn",
			}, nil
		},
	}

	registry := createTestRegistry()
	counter := llm.NewSimpleTokenCounter()

	orchestrator := NewOrchestrator(
		NewRouter(mockLLM, 5),
		NewPlanner(mockLLM),
		mockLLM,
		registry,
		registry,
		counter,
		OrchestratorConfig{MaxSteps: 10},
		testContextFactory,
		nil, // reflector
		nil, // logger - nil for tests
		nil, // emitter - nil for tests
		nil, // modelRegistry - nil for tests
		ToolResultBudget{},
		nil, // bbFactory - nil for tests
	)

	result, err := orchestrator.Handle(context.Background(), "test")
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	if result.RoutingDecision == nil {
		t.Fatal("RoutingDecision should not be nil")
	}
}

// TestOrchestrator_RunBackwardsCompatibility tests that Run() is backwards compatible.
func TestOrchestrator_RunBackwardsCompatibility(t *testing.T) {
	callIdx := 0
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callIdx++
			if callIdx == 1 {
				// Router
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"domain": "general", "complexity": 1, "compaction_strategy": "sliding_window", "suggested_tools": [], "needs_clarification": false}`,
					},
					StopReason: "end_turn",
				}, nil
			}
			if callIdx == 2 {
				// Planner
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"steps": [{"id": "step_1", "description": "Respond to greeting", "depends_on": [], "parallelizable": false, "estimated_tools": []}]}`,
					},
					StopReason: "end_turn",
				}, nil
			}
			// Executor - finish with greeting
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:    "assistant",
					Content: "Hello!",
					ToolCalls: []llm.ToolCall{
						{ID: "c1", Name: "finish", Input: json.RawMessage(`{"answer": "Hello!"}`)},
					},
				},
				StopReason: "tool_use",
			}, nil
		},
	}

	registry := createTestRegistry()
	counter := llm.NewSimpleTokenCounter()

	orchestrator := NewOrchestrator(
		NewRouter(mockLLM, 5),
		NewPlanner(mockLLM),
		mockLLM,
		registry,
		registry,
		counter,
		OrchestratorConfig{MaxSteps: 10},
		testContextFactory,
		nil, // reflector
		nil, // logger - nil for tests
		nil, // emitter - nil for tests
		nil, // modelRegistry - nil for tests
		ToolResultBudget{},
		nil, // bbFactory - nil for tests
	)

	// Run should return HandleResult (same as Handle)
	result, err := orchestrator.Run(context.Background(), "Hi")
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if result == nil {
		t.Fatal("Run should return non-nil HandleResult")
	}

	if result.Output != "Hello!" {
		t.Errorf("unexpected output: %s", result.Output)
	}
}

// TestPlanExecute_FailedStepBlocksDependents tests that when a step fails,
// its dependent steps are not executed.
func TestPlanExecute_FailedStepBlocksDependents(t *testing.T) {
	step2Executed := false
	callIdx := 0
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callIdx++
			if callIdx == 1 {
				// Router
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"domain": "code", "complexity": 4, "compaction_strategy": "sliding_window", "suggested_tools": [], "needs_clarification": false}`,
					},
					StopReason: "end_turn",
				}, nil
			}
			if callIdx == 2 {
				// Planner
				return &llm.ChatResponse{
					Message: llm.Message{
						Role: "assistant",
						Content: `{"steps": [
							{"id": "step_1", "description": "First step (will fail)", "depends_on": [], "parallelizable": false, "estimated_tools": []},
							{"id": "step_2", "description": "Second step (depends on first)", "depends_on": ["step_1"], "parallelizable": false, "estimated_tools": []}
						]}`,
					},
					StopReason: "end_turn",
				}, nil
			}
			// Check which step is being executed
			for _, msg := range req.Messages {
				if strings.Contains(msg.Content, "→ Step 2:") {
					step2Executed = true
				}
				if strings.Contains(msg.Content, "→ Step 1:") {
					// Simulate failure by returning a finish with error indication
					return &llm.ChatResponse{
						Message: llm.Message{
							Role:    "assistant",
							Content: "Error executing step",
							ToolCalls: []llm.ToolCall{
								{ID: "c1", Name: "bash_exec", Input: json.RawMessage(`{"command": "exit 1"}`)},
							},
						},
						StopReason: "tool_use",
					}, nil
				}
			}
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:    "assistant",
					Content: "Done",
					ToolCalls: []llm.ToolCall{
						{ID: "c1", Name: "finish", Input: json.RawMessage(`{"answer": "Done"}`)},
					},
				},
				StopReason: "tool_use",
			}, nil
		},
	}

	// Create a registry with a failing bash_exec tool
	reg := tools.NewToolRegistry()
	reg.Register(&mockTool{
		name:        "bash_exec",
		description: "Execute bash commands",
		result:      tools.ToolResult{Content: "Command failed", IsError: true},
	})

	counter := llm.NewSimpleTokenCounter()

	orchestrator := NewOrchestrator(
		NewRouter(mockLLM, 5),
		NewPlanner(mockLLM),
		mockLLM,
		reg,
		reg,
		counter,
		OrchestratorConfig{MaxSteps: 10, MaxRetries: 0}, // No retries for this test
		testContextFactory,
		nil, // No reflector
		nil, // logger - nil for tests
		nil, // emitter - nil for tests
		nil, // modelRegistry - nil for tests
		ToolResultBudget{},
		nil, // bbFactory - nil for tests
	)

	_, err := orchestrator.Handle(context.Background(), "Run two steps")
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	// Step 2 should NOT have been executed because step 1 failed
	if step2Executed {
		t.Error("step_2 should NOT have been executed when step_1 failed")
	}
}

// TestPlanExecute_StepLifecycleEvents verifies that PlanStepStart and PlanStepComplete
// events are emitted for each plan step during plan_execute mode.
func TestPlanExecute_StepLifecycleEvents(t *testing.T) {
	mockEm := &mockEmitter{}

	callIdx := 0
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callIdx++
			if callIdx == 1 {
				// Router
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"domain": "code", "complexity": 4, "compaction_strategy": "sliding_window", "suggested_tools": [], "needs_clarification": false}`,
					},
					StopReason: "end_turn",
				}, nil
			}
			if callIdx == 2 {
				// Planner
				return &llm.ChatResponse{
					Message: llm.Message{
						Role: "assistant",
						Content: `{"steps": [
							{"id": "step_1", "description": "First step", "depends_on": [], "parallelizable": false, "estimated_tools": []},
						{"id": "step_2", "description": "Second step", "depends_on": ["step_1"], "parallelizable": false, "estimated_tools": []}
						]}`,
					},
					StopReason: "end_turn",
				}, nil
			}
			// Executor - finish for each step
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:    "assistant",
					Content: "Step done",
					ToolCalls: []llm.ToolCall{
						{ID: "call_1", Name: "finish", Input: json.RawMessage(`{"answer": "Step completed"}`)},
					},
				},
				StopReason: "tool_use",
			}, nil
		},
	}

	registry := createTestRegistry()
	counter := llm.NewSimpleTokenCounter()

	orchestrator := NewOrchestrator(
		NewRouter(mockLLM, 5),
		NewPlanner(mockLLM),
		mockLLM,
		registry,
		registry,
		counter,
		OrchestratorConfig{
			MaxSteps: 10,
		},
		testContextFactory,
		nil,    // reflector - nil for this test
		nil,    // logger - nil for tests
		mockEm, // emitter - use mock to track events
		nil,    // modelRegistry - nil for tests
		ToolResultBudget{},
		nil, // bbFactory - nil for tests
	)

	result, err := orchestrator.Handle(context.Background(), "Execute a multi-step task")
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	// Verify plan_execute mode was used (routing decision should exist)
	if result.RoutingDecision == nil {
		t.Fatal("expected non-nil RoutingDecision")
	}

	// Verify plan has 2 steps
	if result.Plan == nil || len(result.Plan.Steps) != 2 {
		t.Fatalf("expected plan with 2 steps, got %v", result.Plan)
	}

	// Verify PlanStepStart was called for each step
	if len(mockEm.planStepStarts) != 2 {
		t.Errorf("expected 2 PlanStepStart calls, got %d", len(mockEm.planStepStarts))
	}

	// Verify PlanStepComplete was called for each step
	if len(mockEm.planStepCompletes) != 2 {
		t.Errorf("expected 2 PlanStepComplete calls, got %d", len(mockEm.planStepCompletes))
	}

	// Verify the order and content of step events
	if len(mockEm.planStepStarts) >= 2 {
		if mockEm.planStepStarts[0].stepID != "step_1" {
			t.Errorf("expected first PlanStepStart for step_1, got %s", mockEm.planStepStarts[0].stepID)
		}
		if mockEm.planStepStarts[0].description != "First step" {
			t.Errorf("expected first PlanStepStart description 'First step', got %s", mockEm.planStepStarts[0].description)
		}
		if mockEm.planStepStarts[1].stepID != "step_2" {
			t.Errorf("expected second PlanStepStart for step_2, got %s", mockEm.planStepStarts[1].stepID)
		}
	}

	// Verify both steps completed successfully
	if len(mockEm.planStepCompletes) >= 2 {
		if !mockEm.planStepCompletes[0].success {
			t.Error("expected step_1 to complete successfully")
		}
		if !mockEm.planStepCompletes[1].success {
			t.Error("expected step_2 to complete successfully")
		}
	}
}

// TestFinishTool_DefaultPolicy tests the finish tool default policy.
func TestFinishTool_DefaultPolicy(t *testing.T) {
	ft := agent.NewFinishTool()
	if ft.DefaultPolicy() != tools.PolicyAlwaysAllow {
		t.Errorf("expected PolicyAlwaysAllow, got %v", ft.DefaultPolicy())
	}
}

// TestFinishTool_Execute tests the finish tool execution.
func TestFinishTool_Execute(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantContent string
		wantError   bool
	}{
		{"valid input", `{"answer":"done!"}`, "done!", false},
		{"empty answer", `{"answer":""}`, "", false},
		{"invalid json", `{invalid}`, "", true},
		{"missing answer field", `{"other":"val"}`, "", false},
	}

	ft := agent.NewFinishTool()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ft.Execute(context.Background(), json.RawMessage(tt.input))
			if err != nil {
				t.Fatalf("unexpected Go error: %v", err)
			}
			if result.IsError != tt.wantError {
				t.Errorf("IsError = %v, want %v (content: %s)", result.IsError, tt.wantError, result.Content)
			}
			if !tt.wantError && result.Content != tt.wantContent {
				t.Errorf("Content = %q, want %q", result.Content, tt.wantContent)
			}
		})
	}
}

// TestHandle_BlackboardPopulated verifies that Handle() populates the blackboard
// with original request, plan, step results, and final output.
func TestHandle_BlackboardPopulated(t *testing.T) {
	callIdx := 0
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callIdx++
			switch callIdx {
			case 1: // Router
				return &llm.ChatResponse{
					Message:    llm.Message{Role: "assistant", Content: `{"domain":"code","complexity":3,"compaction_strategy":"sliding_window","suggested_tools":["bash_exec"],"needs_clarification":false}`},
					StopReason: "end_turn",
				}, nil
			case 2: // Planner
				return &llm.ChatResponse{
					Message:    llm.Message{Role: "assistant", Content: `{"steps":[{"id":"step_1","description":"Run tests","depends_on":[],"parallelizable":false,"estimated_tools":["bash_exec"]}]}`},
					StopReason: "end_turn",
				}, nil
			case 3: // Executor — finish
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: "done",
						ToolCalls: []llm.ToolCall{{
							ID:    "call_1",
							Name:  "finish",
							Input: json.RawMessage(`{"answer":"All tests pass"}`),
						}},
					},
					StopReason: "tool_use",
				}, nil
			default:
				return &llm.ChatResponse{
					Message:    llm.Message{Role: "assistant", Content: ""},
					StopReason: "end_turn",
				}, nil
			}
		},
	}

	registry := createTestRegistry()
	counter := llm.NewSimpleTokenCounter()

	orchestrator := NewOrchestrator(
		NewRouter(mockLLM, 5),
		NewPlanner(mockLLM),
		mockLLM,
		registry,
		registry,
		counter,
		OrchestratorConfig{MaxSteps: 10},
		testContextFactory,
		nil, // reflector
		nil, // logger
		nil, // emitter
		nil, // modelRegistry
		ToolResultBudget{},
		nil, // bbFactory
	)

	result, err := orchestrator.Handle(context.Background(), "Run the tests")
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	bb := result.Blackboard
	if bb == nil {
		t.Fatal("blackboard is nil on HandleResult")
	}

	// Original request
	if got := bb.GetOriginalRequest(); got != "Run the tests" {
		t.Errorf("original request = %q, want %q", got, "Run the tests")
	}

	// Plan
	plan := bb.GetPlan()
	if plan == nil {
		t.Fatal("blackboard plan is nil")
	}
	if len(plan.Steps) != 1 {
		t.Fatalf("plan steps = %d, want 1", len(plan.Steps))
	}
	if plan.Steps[0].ID != "step_1" {
		t.Errorf("plan step ID = %q, want %q", plan.Steps[0].ID, "step_1")
	}

	// Step result
	sr, ok := bb.GetStepResult("step_1")
	if !ok {
		t.Fatal("blackboard missing step_1 result")
	}
	if sr.FullOutput == "" {
		t.Error("step_1 full output should not be empty")
	}

	// Final result
	if got := bb.GetFinalResult(); got == "" {
		t.Error("blackboard final result should not be empty")
	}
}
