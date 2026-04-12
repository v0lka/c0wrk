package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/user/agent/sdk/agent"
	"github.com/user/agent/sdk/llm"
	"github.com/user/agent/sdk/orchestration"
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

// TestCoreStepConfigurator_RoleSuffixInjection verifies that coreStepConfigurator
// injects role-specific suffixes correctly based on the AgentProfile.
func TestCoreStepConfigurator_RoleSuffixInjection(t *testing.T) {
	tests := []struct {
		name           string
		profile        AgentProfile
		wantSuffix     bool
		suffixContains string // substring to check in suffix
	}{
		{
			name:           "researcher role gets researcher suffix",
			profile:        AgentProfile{Role: "researcher"},
			wantSuffix:     true,
			suffixContains: "Role: Researcher",
		},
		{
			name:           "coder role gets coder suffix",
			profile:        AgentProfile{Role: "coder"},
			wantSuffix:     true,
			suffixContains: "Role: Coder",
		},
		{
			name:           "tester role gets tester suffix",
			profile:        AgentProfile{Role: "tester"},
			wantSuffix:     true,
			suffixContains: "Role: Tester",
		},
		{
			name:       "executor role gets no suffix",
			profile:    AgentProfile{Role: "executor"},
			wantSuffix: false,
		},
		{
			name:       "unknown role gets no suffix",
			profile:    AgentProfile{Role: "unknown"},
			wantSuffix: false,
		},
		{
			name:       "empty role gets no suffix",
			profile:    AgentProfile{Role: ""},
			wantSuffix: false,
		},
		{
			name:       "researcher with explicit SystemPrompt gets no suffix",
			profile:    AgentProfile{Role: "researcher", SystemPrompt: "custom prompt"},
			wantSuffix: false,
		},
		{
			name:       "coder with explicit SystemPrompt gets no suffix",
			profile:    AgentProfile{Role: "coder", SystemPrompt: "custom prompt"},
			wantSuffix: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := OrchestratorConfig{MaxSteps: 30}
			configurator := coreStepConfigurator(cfg)

			step := orchestration.PlanStep{
				ID:          "test_step",
				Description: "Test step",
				Profile:     &tt.profile,
			}

			defaults := orchestration.StepDefaults{
				MaxSteps: 30,
				AllTools: nil,
			}

			stepCfg := configurator(step, defaults)

			if tt.wantSuffix {
				if stepCfg.SystemPromptSuffix == "" {
					t.Error("expected non-empty SystemPromptSuffix")
				}
				if !strings.Contains(stepCfg.SystemPromptSuffix, tt.suffixContains) {
					t.Errorf("expected suffix to contain %q, got %q", tt.suffixContains, stepCfg.SystemPromptSuffix)
				}
			} else if stepCfg.SystemPromptSuffix != "" {
				t.Errorf("expected empty SystemPromptSuffix, got %q", stepCfg.SystemPromptSuffix)
			}

			// Verify SystemPrompt is passed through correctly
			if stepCfg.SystemPrompt != tt.profile.SystemPrompt {
				t.Errorf("SystemPrompt = %q, want %q", stepCfg.SystemPrompt, tt.profile.SystemPrompt)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ContinueTask Tests
// ---------------------------------------------------------------------------

// mockTaskStore is a mock implementation of TaskPersistence for testing ContinueTask.
type mockTaskStore struct {
	taskState *TaskState
	loadErr   error
}

func (m *mockTaskStore) PersistNewTask(taskID, sessionID, originalRequest string) error {
	return nil
}

func (m *mockTaskStore) PersistPlan(taskID string, plan *Plan) error { return nil }
func (m *mockTaskStore) PersistRouting(taskID string, routing *RoutingDecision) error {
	return nil
}
func (m *mockTaskStore) PersistStepResult(taskID, stepID, summary, fullOutput, errorText string, steps []Step) error {
	return nil
}
func (m *mockTaskStore) PersistReflection(taskID string, r Reflection) error { return nil }
func (m *mockTaskStore) PersistCompletion(taskID, finalOutput string, attemptCount int) error {
	return nil
}
func (m *mockTaskStore) PersistFailure(taskID string) error { return nil }
func (m *mockTaskStore) PersistStepFileChanges(taskID, stepID string, changes []FileChange) error {
	return nil
}
func (m *mockTaskStore) LoadTaskState(taskID string) (*TaskState, error) {
	if m.loadErr != nil {
		return nil, m.loadErr
	}
	return m.taskState, nil
}
func (m *mockTaskStore) GetUnfinishedTaskID(sessionID string) (string, error) {
	return "", nil
}
func (m *mockTaskStore) ReactivateTask(taskID string) error { return nil }

// TestHandleMessage_ReActContinuation tests the ReAct continuation flow with mocks.
func TestHandleMessage_ReActContinuation(t *testing.T) {
	callIdx := 0
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callIdx++
			switch callIdx {
			case 1: // Router - returns code domain
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"domain": "code", "complexity": 3, "compaction_strategy": "sliding_window", "suggested_tools": [], "needs_clarification": false}`,
					},
					StopReason: "end_turn",
				}, nil
			default: // Executor for continuation step - finish
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: "Continuation step completed",
						ToolCalls: []llm.ToolCall{{
							ID:    "call_1",
							Name:  "finish",
							Input: json.RawMessage(`{"answer": "Done"}`),
						}},
					},
					StopReason: "tool_use",
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
		OrchestratorConfig{MaxSteps: 10},
		testContextFactory,
		nil, // reflector
		nil, // logger
		nil, // emitter
		nil, // modelRegistry
		ToolResultBudget{},
		nil, // bbFactory
	)

	// Set up mock task persistence with a stored task
	mockStore := &mockTaskStore{
		taskState: &TaskState{
			TaskID:          "task-123",
			SessionID:       "session-456",
			OriginalRequest: "original task",
			Status:          "completed",
			Plan: &Plan{
				Steps: []orchestration.PlanStep{
					{ID: "step_1", Description: "First step"},
					{ID: "step_2", Description: "Second step", DependsOn: []string{"step_1"}},
				},
			},
			StepResults: map[string]orchestration.StepResult{
				"step_1": {StepID: "step_1", FullOutput: "output 1"},
				"step_2": {StepID: "step_2", FullOutput: "output 2"},
			},
		},
	}
	orchestrator.SetTaskStore(mockStore)

	result, err := orchestrator.HandleMessage(context.Background(), "Continue the work", "session-456", HandleOptions{PlanFirst: false, TaskID: "task-123"})
	if err != nil {
		t.Fatalf("HandleMessage failed: %v", err)
	}

	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// Verify routing decision is present
	if result.RoutingDecision == nil {
		t.Error("expected RoutingDecision in result")
	}

	// Verify output is present
	if result.Output == "" {
		t.Error("expected non-empty output")
	}

	// Verify plan was extended with continuation step
	if result.Plan == nil {
		t.Fatal("expected plan in result")
	}

	if len(result.Plan.Steps) != 3 {
		t.Errorf("expected 3 steps (2 original + 1 continuation), got %d", len(result.Plan.Steps))
	}

	// Verify the continuation step was added
	lastStep := result.Plan.Steps[len(result.Plan.Steps)-1]
	if lastStep.ID != "continuation_1" {
		t.Errorf("expected continuation step ID 'continuation_1', got %q", lastStep.ID)
	}

	// Verify continuation step depends on terminal steps
	// In the plan: step_1 -> step_2, so step_2 is terminal
	if len(lastStep.DependsOn) != 1 || lastStep.DependsOn[0] != "step_2" {
		t.Errorf("expected continuation to depend on ['step_2'], got %v", lastStep.DependsOn)
	}
}

// TestHandleMessage_ReActContinuation_ClarificationBypass tests that when router returns NeedsClarification,
// HandleMessage returns early with the clarification request.
func TestHandleMessage_ReActContinuation_ClarificationBypass(t *testing.T) {
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
		OrchestratorConfig{MaxSteps: 10},
		testContextFactory,
		nil, // reflector
		nil, // logger
		nil, // emitter
		nil, // modelRegistry
		ToolResultBudget{},
		nil, // bbFactory
	)

	// Set up mock task persistence with a stored task
	mockStore := &mockTaskStore{
		taskState: &TaskState{
			TaskID:          "task-123",
			SessionID:       "session-456",
			OriginalRequest: "original task",
			Status:          "completed",
			Plan: &Plan{
				Steps: []orchestration.PlanStep{
					{ID: "step_1", Description: "First step"},
				},
			},
		},
	}
	orchestrator.SetTaskStore(mockStore)

	result, err := orchestrator.HandleMessage(context.Background(), "unclear request", "session-456", HandleOptions{PlanFirst: false, TaskID: "task-123"})
	if err != nil {
		t.Fatalf("HandleMessage failed: %v", err)
	}

	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// Should return clarification message
	expectedMsg := "I need more information to help you. Could you please clarify your request?"
	if result.Output != expectedMsg {
		t.Errorf("expected clarification message, got: %s", result.Output)
	}

	// Should have routing decision with NeedsClarification=true
	if result.RoutingDecision == nil {
		t.Fatal("expected RoutingDecision in result")
	}
	if !result.RoutingDecision.NeedsClarification {
		t.Error("expected NeedsClarification to be true")
	}

	// Should still have blackboard restored
	if result.Blackboard == nil {
		t.Error("expected blackboard in result even for clarification")
	}
}

// TestHandleMessage_Continuation_NoTaskStore tests that HandleMessage continuation returns error when task store is not configured.
func TestHandleMessage_Continuation_NoTaskStore(t *testing.T) {
	registry := createTestRegistry()
	counter := llm.NewSimpleTokenCounter()

	orchestrator := NewOrchestrator(
		NewRouter(&mockLLMCaller{}, 5),
		NewPlanner(&mockLLMCaller{}),
		&mockLLMCaller{},
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
	// Note: taskStore is nil by default

	_, err := orchestrator.HandleMessage(context.Background(), "message", "session-456", HandleOptions{PlanFirst: false, TaskID: "task-123"})
	if err == nil {
		t.Fatal("expected error when task store is not configured")
	}

	if !strings.Contains(err.Error(), "task persistence not configured") {
		t.Errorf("expected 'task persistence not configured' error, got: %v", err)
	}
}

// TestHandleMessage_Continuation_TaskNotFound tests that HandleMessage continuation returns error when task is not found.
func TestHandleMessage_Continuation_TaskNotFound(t *testing.T) {
	registry := createTestRegistry()
	counter := llm.NewSimpleTokenCounter()

	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			// Router returns normal response
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:    "assistant",
					Content: `{"domain": "code", "complexity": 3, "compaction_strategy": "sliding_window", "suggested_tools": [], "needs_clarification": false}`,
				},
				StopReason: "end_turn",
			}, nil
		},
	}

	router := NewRouter(mockLLM, 5)
	planner := NewPlanner(mockLLM)

	orchestrator := NewOrchestrator(
		router,
		planner,
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

	// Set up mock task persistence that returns nil (task not found)
	mockStore := &mockTaskStore{
		taskState: nil, // task not found
	}
	orchestrator.SetTaskStore(mockStore)

	_, err := orchestrator.HandleMessage(context.Background(), "message", "session-456", HandleOptions{PlanFirst: false, TaskID: "non-existent-task"})
	if err == nil {
		t.Fatal("expected error when task is not found")
	}

	if !strings.Contains(err.Error(), "task not found") {
		t.Errorf("expected 'task not found' error, got: %v", err)
	}
}

// TestHandleMessage_ReActFirstMessage tests the ReAct mode first message flow.
// PlanFirst=false, TaskID="" should create a clean BB, build synthetic 1-step plan,
// execute single step via ExecuteAdHocStep.
func TestHandleMessage_ReActFirstMessage(t *testing.T) {
	callIdx := 0
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callIdx++
			switch callIdx {
			case 1: // Router - returns code domain
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"domain": "code", "complexity": 3, "compaction_strategy": "sliding_window", "suggested_tools": [], "needs_clarification": false}`,
					},
					StopReason: "end_turn",
				}, nil
			default: // Executor for single step - finish
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: "Step completed",
						ToolCalls: []llm.ToolCall{{
							ID:    "call_1",
							Name:  "finish",
							Input: json.RawMessage(`{"answer": "Done"}`),
						}},
					},
					StopReason: "tool_use",
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
		OrchestratorConfig{MaxSteps: 10},
		testContextFactory,
		nil, // reflector
		nil, // logger
		nil, // emitter
		nil, // modelRegistry
		ToolResultBudget{},
		nil, // bbFactory
	)

	result, err := orchestrator.HandleMessage(context.Background(), "Write a hello world program", "session-test", HandleOptions{PlanFirst: false, TaskID: ""})
	if err != nil {
		t.Fatalf("HandleMessage failed: %v", err)
	}

	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// Verify routing decision is present
	if result.RoutingDecision == nil {
		t.Error("expected RoutingDecision in result")
	}

	// Verify output is present
	if result.Output == "" {
		t.Error("expected non-empty output")
	}

	// Verify plan was created with single step
	if result.Plan == nil {
		t.Fatal("expected plan in result")
	}

	if len(result.Plan.Steps) != 1 {
		t.Errorf("expected 1 step in synthetic plan, got %d", len(result.Plan.Steps))
	}

	// Verify the step ID is "step_1" for first message
	if result.Plan.Steps[0].ID != "step_1" {
		t.Errorf("expected step ID 'step_1', got %q", result.Plan.Steps[0].ID)
	}

	// Verify blackboard is present
	if result.Blackboard == nil {
		t.Error("expected blackboard in result")
	}

	// Verify attempt count
	if result.AttemptCount != 1 {
		t.Errorf("expected attempt count 1, got %d", result.AttemptCount)
	}
}

// TestHandleMessage_PlanExecuteFirstMessage tests the Plan&Execute mode first message flow.
// PlanFirst=true, TaskID="" should create clean BB, run full P&E via ExecuteWithBlackboard.
func TestHandleMessage_PlanExecuteFirstMessage(t *testing.T) {
	callIdx := 0
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callIdx++
			switch callIdx {
			case 1: // Router - returns code domain
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"domain": "code", "complexity": 3, "compaction_strategy": "sliding_window", "suggested_tools": [], "needs_clarification": false}`,
					},
					StopReason: "end_turn",
				}, nil
			case 2: // Planner - creates multi-step plan
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"steps": [{"id": "step_1", "description": "Plan step 1", "depends_on": [], "parallelizable": true, "estimated_tools": []}]}`,
					},
					StopReason: "end_turn",
				}, nil
			default: // Executor - finish
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: "Plan executed",
						ToolCalls: []llm.ToolCall{{
							ID:    "call_1",
							Name:  "finish",
							Input: json.RawMessage(`{"answer": "Done"}`),
						}},
					},
					StopReason: "tool_use",
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
		OrchestratorConfig{MaxSteps: 10},
		testContextFactory,
		nil, // reflector
		nil, // logger
		nil, // emitter
		nil, // modelRegistry
		ToolResultBudget{},
		nil, // bbFactory
	)

	result, err := orchestrator.HandleMessage(context.Background(), "Build a CLI tool", "session-test", HandleOptions{PlanFirst: true, TaskID: ""})
	if err != nil {
		t.Fatalf("HandleMessage failed: %v", err)
	}

	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// Verify routing decision is present
	if result.RoutingDecision == nil {
		t.Error("expected RoutingDecision in result")
	}

	// Verify output is present
	if result.Output == "" {
		t.Error("expected non-empty output")
	}

	// Verify plan was created by planner (multi-step)
	if result.Plan == nil {
		t.Fatal("expected plan in result")
	}

	// Verify blackboard is present
	if result.Blackboard == nil {
		t.Error("expected blackboard in result")
	}

	// Verify attempt count
	if result.AttemptCount < 1 {
		t.Errorf("expected attempt count >= 1, got %d", result.AttemptCount)
	}
}

// TestHandleMessage_PlanExecuteContinuation tests the Plan&Execute continuation flow.
// PlanFirst=true, TaskID set should restore BB, call PlanContinuation, execute new steps.
func TestHandleMessage_PlanExecuteContinuation(t *testing.T) {
	callIdx := 0
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callIdx++
			switch callIdx {
			case 1: // Router - returns code domain
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"domain": "code", "complexity": 3, "compaction_strategy": "sliding_window", "suggested_tools": [], "needs_clarification": false}`,
					},
					StopReason: "end_turn",
				}, nil
			case 2: // PlanContinuation - creates continuation step
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"steps": [{"id": "continuation_1", "description": "Continuation step", "depends_on": ["step_1"], "parallelizable": true, "estimated_tools": []}]}`,
					},
					StopReason: "end_turn",
				}, nil
			default: // Executor - finish
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: "Continuation executed",
						ToolCalls: []llm.ToolCall{{
							ID:    "call_1",
							Name:  "finish",
							Input: json.RawMessage(`{"answer": "Done"}`),
						}},
					},
					StopReason: "tool_use",
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
		OrchestratorConfig{MaxSteps: 10},
		testContextFactory,
		nil, // reflector
		nil, // logger
		nil, // emitter
		nil, // modelRegistry
		ToolResultBudget{},
		nil, // bbFactory
	)

	// Set up mock task persistence with a stored task
	mockStore := &mockTaskStore{
		taskState: &TaskState{
			TaskID:          "task-123",
			SessionID:       "session-456",
			OriginalRequest: "original task",
			Status:          "completed",
			Plan: &Plan{
				Steps: []orchestration.PlanStep{
					{ID: "step_1", Description: "First step"},
				},
			},
			StepResults: map[string]orchestration.StepResult{
				"step_1": {StepID: "step_1", FullOutput: "output 1"},
			},
		},
	}
	orchestrator.SetTaskStore(mockStore)

	result, err := orchestrator.HandleMessage(context.Background(), "Continue the work", "session-456", HandleOptions{PlanFirst: true, TaskID: "task-123"})
	if err != nil {
		t.Fatalf("HandleMessage failed: %v", err)
	}

	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// Verify routing decision is present
	if result.RoutingDecision == nil {
		t.Error("expected RoutingDecision in result")
	}

	// Verify output is present
	if result.Output == "" {
		t.Error("expected non-empty output")
	}

	// Verify plan was merged (original + continuation)
	if result.Plan == nil {
		t.Fatal("expected plan in result")
	}

	// Should have original step + continuation step
	if len(result.Plan.Steps) < 2 {
		t.Errorf("expected at least 2 steps (original + continuation), got %d", len(result.Plan.Steps))
	}

	// Verify blackboard is present
	if result.Blackboard == nil {
		t.Error("expected blackboard in result")
	}
}

// TestHandleMessage_ReactivatesTask verifies that ReactivateTask is called on any continuation.
func TestHandleMessage_ReactivatesTask(t *testing.T) {
	tests := []struct {
		name      string
		planFirst bool
	}{
		{"ReAct continuation", false},
		{"Plan&Execute continuation", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			callIdx := 0
			reactivateCalled := false

			mockLLM := &mockLLMCaller{
				callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
					callIdx++
					switch callIdx {
					case 1: // Router
						return &llm.ChatResponse{
							Message: llm.Message{
								Role:    "assistant",
								Content: `{"domain": "code", "complexity": 3, "compaction_strategy": "sliding_window", "suggested_tools": [], "needs_clarification": false}`,
							},
							StopReason: "end_turn",
						}, nil
					case 2:
						if tt.planFirst {
							// PlanContinuation
							return &llm.ChatResponse{
								Message: llm.Message{
									Role:    "assistant",
									Content: `{"steps": [{"id": "cont_1", "description": "cont", "depends_on": [], "parallelizable": true}]}`,
								},
								StopReason: "end_turn",
							}, nil
						}
						fallthrough
					default: // Executor
						return &llm.ChatResponse{
							Message: llm.Message{
								Role:    "assistant",
								Content: "Done",
								ToolCalls: []llm.ToolCall{{
									ID:    "call_1",
									Name:  "finish",
									Input: json.RawMessage(`{"answer": "done"}`),
								}},
							},
							StopReason: "tool_use",
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
				OrchestratorConfig{MaxSteps: 10},
				testContextFactory,
				nil, // reflector
				nil, // logger
				nil, // emitter
				nil, // modelRegistry
				ToolResultBudget{},
				nil, // bbFactory
			)

			// Create mock store that tracks ReactivateTask calls
			mockStore := &mockTaskStoreWithReactivate{
				taskState: &TaskState{
					TaskID:          "task-123",
					SessionID:       "session-456",
					OriginalRequest: "original task",
					Status:          "completed",
					Plan: &Plan{
						Steps: []orchestration.PlanStep{
							{ID: "step_1", Description: "First step"},
						},
					},
					StepResults: map[string]orchestration.StepResult{
						"step_1": {StepID: "step_1", FullOutput: "output 1"},
					},
				},
				reactivateFn: func(taskID string) error {
					reactivateCalled = true
					return nil
				},
			}
			orchestrator.SetTaskStore(mockStore)

			_, err := orchestrator.HandleMessage(context.Background(), "Continue", "session-456", HandleOptions{PlanFirst: tt.planFirst, TaskID: "task-123"})
			if err != nil {
				t.Fatalf("HandleMessage failed: %v", err)
			}

			if !reactivateCalled {
				t.Error("expected ReactivateTask to be called for continuation")
			}
		})
	}
}

// mockTaskStoreWithReactivate is a mock TaskPersistence that tracks ReactivateTask calls.
type mockTaskStoreWithReactivate struct {
	taskState     *TaskState
	loadErr       error
	reactivateFn  func(taskID string) error
}

func (m *mockTaskStoreWithReactivate) PersistNewTask(taskID, sessionID, originalRequest string) error {
	return nil
}
func (m *mockTaskStoreWithReactivate) PersistPlan(taskID string, plan *Plan) error { return nil }
func (m *mockTaskStoreWithReactivate) PersistRouting(taskID string, routing *RoutingDecision) error {
	return nil
}
func (m *mockTaskStoreWithReactivate) PersistStepResult(taskID, stepID, summary, fullOutput, errorText string, steps []Step) error {
	return nil
}
func (m *mockTaskStoreWithReactivate) PersistReflection(taskID string, r Reflection) error { return nil }
func (m *mockTaskStoreWithReactivate) PersistCompletion(taskID, finalOutput string, attemptCount int) error {
	return nil
}
func (m *mockTaskStoreWithReactivate) PersistFailure(taskID string) error { return nil }
func (m *mockTaskStoreWithReactivate) PersistStepFileChanges(taskID, stepID string, changes []FileChange) error {
	return nil
}
func (m *mockTaskStoreWithReactivate) LoadTaskState(taskID string) (*TaskState, error) {
	if m.loadErr != nil {
		return nil, m.loadErr
	}
	return m.taskState, nil
}
func (m *mockTaskStoreWithReactivate) GetUnfinishedTaskID(sessionID string) (string, error) {
	return "", nil
}
func (m *mockTaskStoreWithReactivate) ReactivateTask(taskID string) error {
	if m.reactivateFn != nil {
		return m.reactivateFn(taskID)
	}
	return nil
}

// TestHandleMessage_Clarification tests that clarification early return works for both modes.
func TestHandleMessage_Clarification(t *testing.T) {
	tests := []struct {
		name      string
		planFirst bool
	}{
		{"ReAct mode", false},
		{"Plan&Execute mode", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
				OrchestratorConfig{MaxSteps: 10},
				testContextFactory,
				nil, // reflector
				nil, // logger
				nil, // emitter
				nil, // modelRegistry
				ToolResultBudget{},
				nil, // bbFactory
			)

			result, err := orchestrator.HandleMessage(context.Background(), "unclear request", "session-test", HandleOptions{PlanFirst: tt.planFirst, TaskID: ""})
			if err != nil {
				t.Fatalf("HandleMessage failed: %v", err)
			}

			if result == nil {
				t.Fatal("expected non-nil result")
			}

			// Should return clarification message
			expectedMsg := "I need more information to help you. Could you please clarify your request?"
			if result.Output != expectedMsg {
				t.Errorf("expected clarification message, got: %s", result.Output)
			}

			// Should have routing decision with NeedsClarification=true
			if result.RoutingDecision == nil {
				t.Fatal("expected RoutingDecision in result")
			}
			if !result.RoutingDecision.NeedsClarification {
				t.Error("expected NeedsClarification to be true")
			}
		})
	}
}

// TestBuildSystemPrompt_PlanMode verifies that buildSystemPrompt includes the
// Plan Context section when PlanModeKey is set in the context.
func TestBuildSystemPrompt_PlanMode(t *testing.T) {
	ctx := context.WithValue(context.Background(), PlanModeKey, true)
	ctx = tools.WithWorkspacePath(ctx, "/test/workspace")

	result := buildSystemPrompt(ctx, "test message")

	if !strings.Contains(result, "Plan Context") {
		t.Error("plan mode prompt should contain Plan Context section")
	}
	if !strings.Contains(result, "read_step_output") {
		t.Error("plan mode prompt should contain read_step_output")
	}
	if !strings.Contains(result, "list_step_outputs") {
		t.Error("plan mode prompt should contain list_step_outputs")
	}
}

// TestBuildSystemPrompt_ReactMode verifies that buildSystemPrompt does NOT include
// the Plan Context section when PlanModeKey is not set (ReAct mode).
func TestBuildSystemPrompt_ReactMode(t *testing.T) {
	ctx := context.Background()
	ctx = tools.WithWorkspacePath(ctx, "/test/workspace")

	result := buildSystemPrompt(ctx, "test message")

	if strings.Contains(result, "Plan Context") {
		t.Error("react mode prompt should NOT contain Plan Context section")
	}
	if strings.Contains(result, "read_step_output") {
		t.Error("react mode prompt should NOT contain read_step_output")
	}
	if strings.Contains(result, "list_step_outputs") {
		t.Error("react mode prompt should NOT contain list_step_outputs")
	}
}
