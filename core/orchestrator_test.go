package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/user/agent/core/skills"
	"github.com/user/agent/sdk/agent"
	"github.com/user/agent/sdk/llm"
	"github.com/user/agent/sdk/orchestration"
	tools "github.com/user/agent/sdk/tools"
	"github.com/user/agent/sdk/tools/builtins"
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
	// Register a mock write_file tool
	reg.Register(&mockTool{
		name:        "write_file",
		description: "Write files",
		result:      tools.ToolResult{Content: "File written", IsError: false},
	})
	return reg
}

// Helper to create a context factory for tests
func testContextFactory(systemPrompt string, modelMeta llm.ModelMetadata, compactionStrategy string, _ ...orchestration.PruningOverride) ContextManager {
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

	orchestrator := NewOrchestrator(OrchestratorConfig{MaxSteps: 10}, OrchestratorDeps{
		Router:         router,
		Planner:        planner,
		LLM:            mockLLM,
		ToolExec:       registry,
		ToolRegistry:   registry,
		TokenCounter:   counter,
		ContextFactory: testContextFactory,
		CircuitBreaker: defaultCircuitBreakerConfig,
	})

	result, err := orchestrator.HandleMessage(context.Background(), "do something", "", HandleOptions{ExecutionMode: "advanced"})
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
						Content: `{"domain": "code", "complexity": 4, "compaction_strategy": "sliding_window", "suggested_tools": ["bash_exec", "write_file"], "needs_clarification": false}`,
					},
					StopReason: "end_turn",
				}, nil
			case 2: // Planner
				return &llm.ChatResponse{
					Message: llm.Message{
						Role: "assistant",
						Content: `{"steps": [
							{"id": "step_1", "description": "Write tests", "depends_on": [], "parallelizable": false, "estimated_tools": ["write_file"]},
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

	orchestrator := NewOrchestrator(OrchestratorConfig{MaxSteps: 10}, OrchestratorDeps{
		Router:         router,
		Planner:        planner,
		LLM:            mockLLM,
		ToolExec:       registry,
		ToolRegistry:   registry,
		TokenCounter:   counter,
		ContextFactory: testContextFactory,
		CircuitBreaker: defaultCircuitBreakerConfig,
	})

	result, err := orchestrator.HandleMessage(context.Background(), "Implement and test a new feature", "", HandleOptions{ExecutionMode: "advanced"})
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

	orchestrator := NewOrchestrator(OrchestratorConfig{MaxSteps: 10}, OrchestratorDeps{
		Router:         NewRouter(mockLLM, 5),
		Planner:        NewPlanner(mockLLM),
		LLM:            mockLLM,
		ToolExec:       registry,
		ToolRegistry:   registry,
		TokenCounter:   counter,
		ContextFactory: testContextFactory,
		CircuitBreaker: defaultCircuitBreakerConfig,
	})

	result, err := orchestrator.HandleMessage(context.Background(), "test", "", HandleOptions{ExecutionMode: "advanced"})
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	if result.RoutingDecision == nil {
		t.Fatal("RoutingDecision should not be nil")
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

	orchestrator := NewOrchestrator(OrchestratorConfig{MaxSteps: 10, MaxRetries: 0}, OrchestratorDeps{
		Router:         NewRouter(mockLLM, 5),
		Planner:        NewPlanner(mockLLM),
		LLM:            mockLLM,
		ToolExec:       reg,
		ToolRegistry:   reg,
		TokenCounter:   counter,
		ContextFactory: testContextFactory,
		CircuitBreaker: defaultCircuitBreakerConfig,
	})

	_, err := orchestrator.HandleMessage(context.Background(), "Run two steps", "", HandleOptions{ExecutionMode: "advanced"})
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

	orchestrator := NewOrchestrator(OrchestratorConfig{MaxSteps: 10}, OrchestratorDeps{
		Router:         NewRouter(mockLLM, 5),
		Planner:        NewPlanner(mockLLM),
		LLM:            mockLLM,
		ToolExec:       registry,
		ToolRegistry:   registry,
		TokenCounter:   counter,
		ContextFactory: testContextFactory,
		CircuitBreaker: defaultCircuitBreakerConfig,
		Emitter:        mockEm,
	})

	result, err := orchestrator.HandleMessage(context.Background(), "Execute a multi-step task", "", HandleOptions{ExecutionMode: "advanced"})
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

	orchestrator := NewOrchestrator(OrchestratorConfig{MaxSteps: 10}, OrchestratorDeps{
		Router:         NewRouter(mockLLM, 5),
		Planner:        NewPlanner(mockLLM),
		LLM:            mockLLM,
		ToolExec:       registry,
		ToolRegistry:   registry,
		TokenCounter:   counter,
		ContextFactory: testContextFactory,
		CircuitBreaker: defaultCircuitBreakerConfig,
	})
	result, err := orchestrator.HandleMessage(context.Background(), "Run the tests", "", HandleOptions{ExecutionMode: "advanced"})
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
			configurator := coreStepConfigurator(cfg, nil, nil, nil, nil, nil) // nil deps: default family; no per-step skill/tool narrowing

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

// TestCoreStepConfigurator_RoleSuffixes verifies that coreStepConfigurator
// selects the appropriate role suffixes based on agent profile role.
func TestCoreStepConfigurator_RoleSuffixes(t *testing.T) {
	registry := llm.NewModelRegistry(nil)

	tests := []struct {
		name           string
		registry       *llm.ModelRegistry
		role           string
		wantContains   string // substring expected in suffix
		notWantContain string // substring NOT expected in suffix
	}{
		{
			name:         "researcher gets role suffix",
			registry:     registry,
			role:         "researcher",
			wantContains: "Role: Researcher",
		},
		{
			name:         "coder gets role suffix",
			registry:     registry,
			role:         "coder",
			wantContains: "Role: Coder",
		},
		{
			name:         "tester gets role suffix",
			registry:     registry,
			role:         "tester",
			wantContains: "Role: Tester",
		},
		{
			name:         "nil registry still works",
			registry:     nil,
			role:         "tester",
			wantContains: "Role: Tester",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := OrchestratorConfig{MaxSteps: 30}
			configurator := coreStepConfigurator(cfg, tt.registry, nil, nil, nil, nil)

			profile := AgentProfile{Role: tt.role}
			step := orchestration.PlanStep{
				ID:          "test_step",
				Description: "Test step",
				Profile:     &profile,
			}

			defaults := orchestration.StepDefaults{
				MaxSteps: 30,
				AllTools: nil,
			}

			stepCfg := configurator(step, defaults)

			if stepCfg.SystemPromptSuffix == "" {
				t.Error("expected non-empty SystemPromptSuffix")
				return
			}

			if !strings.Contains(stepCfg.SystemPromptSuffix, tt.wantContains) {
				t.Errorf("expected suffix to contain %q, got %q", tt.wantContains, stepCfg.SystemPromptSuffix)
			}

			if tt.notWantContain != "" && strings.Contains(stepCfg.SystemPromptSuffix, tt.notWantContain) {
				t.Errorf("expected suffix NOT to contain %q, but it did: %q", tt.notWantContain, stepCfg.SystemPromptSuffix)
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
func (m *mockTaskStore) PersistFacts(taskID string, facts []Fact) error { return nil }
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

// TestHandleMessage_Continuation tests the continuation flow with mocks.
// Continuations always use the P&E path: PlanContinuation + Resume.
func TestHandleMessage_Continuation(t *testing.T) {
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
						Content: `{"steps": [{"id": "continuation_1", "description": "Continue the work", "depends_on": ["step_2"], "parallelizable": false, "estimated_tools": []}]}`,
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

	orchestrator := NewOrchestrator(OrchestratorConfig{MaxSteps: 10}, OrchestratorDeps{
		Router:         router,
		Planner:        planner,
		LLM:            mockLLM,
		ToolExec:       registry,
		ToolRegistry:   registry,
		TokenCounter:   counter,
		ContextFactory: testContextFactory,
		CircuitBreaker: defaultCircuitBreakerConfig,
	})
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
	orchestrator.SetBlackboardRestoreFunc(testBlackboardRestoreFunc())

	result, err := orchestrator.HandleMessage(context.Background(), "Continue the work", "session-456", HandleOptions{TaskID: "task-123", ExecutionMode: "advanced"})
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

	orchestrator := NewOrchestrator(OrchestratorConfig{MaxSteps: 10}, OrchestratorDeps{
		Router:         router,
		Planner:        planner,
		LLM:            mockLLM,
		ToolExec:       registry,
		ToolRegistry:   registry,
		TokenCounter:   counter,
		ContextFactory: testContextFactory,
		CircuitBreaker: defaultCircuitBreakerConfig,
	})
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
	orchestrator.SetBlackboardRestoreFunc(testBlackboardRestoreFunc())

	result, err := orchestrator.HandleMessage(context.Background(), "unclear request", "session-456", HandleOptions{TaskID: "task-123", ExecutionMode: "advanced"})
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

	orchestrator := NewOrchestrator(OrchestratorConfig{MaxSteps: 10}, OrchestratorDeps{
		Router:         NewRouter(&mockLLMCaller{}, 5),
		Planner:        NewPlanner(&mockLLMCaller{}),
		LLM:            &mockLLMCaller{},
		ToolExec:       registry,
		ToolRegistry:   registry,
		TokenCounter:   counter,
		ContextFactory: testContextFactory,
		CircuitBreaker: defaultCircuitBreakerConfig,
	})
	// Note: taskStore is nil by default

	_, err := orchestrator.HandleMessage(context.Background(), "message", "session-456", HandleOptions{TaskID: "task-123", ExecutionMode: "advanced"})
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

	orchestrator := NewOrchestrator(OrchestratorConfig{MaxSteps: 10}, OrchestratorDeps{
		Router:         router,
		Planner:        planner,
		LLM:            mockLLM,
		ToolExec:       registry,
		ToolRegistry:   registry,
		TokenCounter:   counter,
		ContextFactory: testContextFactory,
		CircuitBreaker: defaultCircuitBreakerConfig,
	})
	// Set up mock task persistence that returns nil (task not found)
	mockStore := &mockTaskStore{
		taskState: nil, // task not found
	}
	orchestrator.SetTaskStore(mockStore)
	orchestrator.SetBlackboardRestoreFunc(testBlackboardRestoreFunc())

	_, err := orchestrator.HandleMessage(context.Background(), "message", "session-456", HandleOptions{TaskID: "non-existent-task", ExecutionMode: "advanced"})
	if err == nil {
		t.Fatal("expected error when task is not found")
	}

	if !strings.Contains(err.Error(), "task not found") {
		t.Errorf("expected 'task not found' error, got: %v", err)
	}
}

// TestHandleMessage_PlanExecuteFirstMessage tests the Plan&Execute mode first message flow.
// TaskID="" with a complex plan should create clean BB, generate plan, run full P&E via Resume.
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

	orchestrator := NewOrchestrator(OrchestratorConfig{MaxSteps: 10}, OrchestratorDeps{
		Router:         router,
		Planner:        planner,
		LLM:            mockLLM,
		ToolExec:       registry,
		ToolRegistry:   registry,
		TokenCounter:   counter,
		ContextFactory: testContextFactory,
		CircuitBreaker: defaultCircuitBreakerConfig,
	})
	result, err := orchestrator.HandleMessage(context.Background(), "Build a CLI tool", "session-test", HandleOptions{ExecutionMode: "advanced"})
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
// TaskID set should restore BB, call PlanContinuation, execute new steps.
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

	orchestrator := NewOrchestrator(OrchestratorConfig{MaxSteps: 10}, OrchestratorDeps{
		Router:         router,
		Planner:        planner,
		LLM:            mockLLM,
		ToolExec:       registry,
		ToolRegistry:   registry,
		TokenCounter:   counter,
		ContextFactory: testContextFactory,
		CircuitBreaker: defaultCircuitBreakerConfig,
	})
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
	orchestrator.SetBlackboardRestoreFunc(testBlackboardRestoreFunc())

	result, err := orchestrator.HandleMessage(context.Background(), "Continue the work", "session-456", HandleOptions{TaskID: "task-123", ExecutionMode: "advanced"})
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
			case 2: // PlanContinuation
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"steps": [{"id": "cont_1", "description": "cont", "depends_on": [], "parallelizable": true}]}`,
					},
					StopReason: "end_turn",
				}, nil
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

	orchestrator := NewOrchestrator(OrchestratorConfig{MaxSteps: 10}, OrchestratorDeps{
		Router:         router,
		Planner:        planner,
		LLM:            mockLLM,
		ToolExec:       registry,
		ToolRegistry:   registry,
		TokenCounter:   counter,
		ContextFactory: testContextFactory,
		CircuitBreaker: defaultCircuitBreakerConfig,
	})
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
	orchestrator.SetBlackboardRestoreFunc(testBlackboardRestoreFunc())

	_, err := orchestrator.HandleMessage(context.Background(), "Continue", "session-456", HandleOptions{TaskID: "task-123", ExecutionMode: "advanced"})
	if err != nil {
		t.Fatalf("HandleMessage failed: %v", err)
	}

	if !reactivateCalled {
		t.Error("expected ReactivateTask to be called for continuation")
	}
}

// mockTaskStoreWithReactivate is a mock TaskPersistence that tracks ReactivateTask calls.
type mockTaskStoreWithReactivate struct {
	taskState    *TaskState
	loadErr      error
	reactivateFn func(taskID string) error
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
func (m *mockTaskStoreWithReactivate) PersistReflection(taskID string, r Reflection) error {
	return nil
}
func (m *mockTaskStoreWithReactivate) PersistCompletion(taskID, finalOutput string, attemptCount int) error {
	return nil
}
func (m *mockTaskStoreWithReactivate) PersistFailure(taskID string) error { return nil }
func (m *mockTaskStoreWithReactivate) PersistFacts(taskID string, facts []Fact) error { return nil }
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

// TestHandleMessage_Clarification tests that clarification early return works.
func TestHandleMessage_Clarification(t *testing.T) {
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

	orchestrator := NewOrchestrator(OrchestratorConfig{MaxSteps: 10}, OrchestratorDeps{
		Router:         router,
		Planner:        planner,
		LLM:            mockLLM,
		ToolExec:       registry,
		ToolRegistry:   registry,
		TokenCounter:   counter,
		ContextFactory: testContextFactory,
		CircuitBreaker: defaultCircuitBreakerConfig,
	})
	result, err := orchestrator.HandleMessage(context.Background(), "unclear request", "session-test", HandleOptions{ExecutionMode: "advanced"})
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
}

// TestHandleMessage_ClarificationSuppressedWithUserSkills verifies that when the
// router returns needs_clarification=true but the user explicitly invoked a skill
// (UserSkills is non-empty), the clarification short-circuit is bypassed and
// execution proceeds to planning. It also verifies that the routing message
// contains the skill's description so the router can classify accurately.
func TestHandleMessage_ClarificationSuppressedWithUserSkills(t *testing.T) {
	// Create a temp skill directory so the SkillManager can resolve the skill.
	skillDir := t.TempDir()
	vsDir := filepath.Join(skillDir, "vibespec-check")
	if err := os.MkdirAll(vsDir, 0o755); err != nil {
		t.Fatalf("failed to create skill dir: %v", err)
	}
	skillMD := "---\nname: vibespec-check\ndescription: Analyze codebase for specification compliance\n---\nRun the vibespec check.\n"
	if err := os.WriteFile(filepath.Join(vsDir, "SKILL.md"), []byte(skillMD), 0o644); err != nil {
		t.Fatalf("failed to write SKILL.md: %v", err)
	}
	sm := skills.NewSkillManager([]string{skillDir}, nil)
	if err := sm.Scan(); err != nil {
		t.Fatalf("SkillManager.Scan failed: %v", err)
	}

	var routerReceivedMessage string
	callIdx := 0
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callIdx++
			switch callIdx {
			case 1: // Router — capture the user message it receives
				for _, m := range req.Messages {
					if m.Role == "user" {
						routerReceivedMessage = m.Content
					}
				}
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"domain": "code", "complexity": 3, "needs_clarification": true, "matched_skills": []}`,
					},
					StopReason: "end_turn",
				}, nil
			case 2: // Planner
				return &llm.ChatResponse{
					Message: llm.Message{
						Role: "assistant",
						Content: `{"steps": [
							{"id": "step_1", "description": "Run vibespec check", "depends_on": [], "parallelizable": false, "estimated_tools": ["bash_exec"]}
						]}`,
					},
					StopReason: "end_turn",
				}, nil
			case 3: // Executor — finish
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: "Check complete",
						ToolCalls: []llm.ToolCall{
							{
								ID:    "call_1",
								Name:  "finish",
								Input: json.RawMessage(`{"answer": "Vibespec check done"}`),
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

	orchestrator := NewOrchestrator(OrchestratorConfig{MaxSteps: 10}, OrchestratorDeps{
		Router:         router,
		Planner:        planner,
		LLM:            mockLLM,
		ToolExec:       registry,
		ToolRegistry:   registry,
		TokenCounter:   counter,
		ContextFactory: testContextFactory,
		CircuitBreaker: defaultCircuitBreakerConfig,
		SkillManager:   sm,
	})

	// Simulate "/vibespec-check entire codebase" → message="entire codebase", UserSkills=["vibespec-check"]
	result, err := orchestrator.HandleMessage(context.Background(), "entire codebase", "session-test", HandleOptions{
		ExecutionMode: "advanced",
		UserSkills:    []string{"vibespec-check"},
	})
	if err != nil {
		t.Fatalf("HandleMessage failed: %v", err)
	}

	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// Must NOT return the clarification message
	clarificationMsg := "I need more information to help you. Could you please clarify your request?"
	if result.Output == clarificationMsg {
		t.Error("expected clarification to be suppressed when UserSkills is non-empty, but got clarification message")
	}

	// Routing decision should have NeedsClarification overridden to false
	if result.RoutingDecision != nil && result.RoutingDecision.NeedsClarification {
		t.Error("expected NeedsClarification to be false after suppression")
	}

	// Verify the router received the skill-augmented message (not bare "entire codebase")
	if !strings.Contains(routerReceivedMessage, "vibespec-check") {
		t.Errorf("router message should contain skill name, got: %s", routerReceivedMessage)
	}
	if !strings.Contains(routerReceivedMessage, "Analyze codebase") {
		t.Errorf("router message should contain skill description, got: %s", routerReceivedMessage)
	}
	if !strings.Contains(routerReceivedMessage, "entire codebase") {
		t.Errorf("router message should contain the original arguments, got: %s", routerReceivedMessage)
	}

	// Verify execution proceeded past routing (planner was called)
	if callIdx < 2 {
		t.Errorf("expected at least 2 LLM calls (router + planner), got %d", callIdx)
	}
}

// TestBuildSkillAugmentedRoutingMessage verifies the message augmentation logic.
func TestBuildSkillAugmentedRoutingMessage(t *testing.T) {
	// Create a SkillManager with a test skill
	skillDir := t.TempDir()
	checkDir := filepath.Join(skillDir, "code-check")
	if err := os.MkdirAll(checkDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	md := "---\nname: code-check\ndescription: Run static analysis on source code\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(checkDir, "SKILL.md"), []byte(md), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	sm := skills.NewSkillManager([]string{skillDir}, nil)
	if err := sm.Scan(); err != nil {
		t.Fatalf("scan: %v", err)
	}

	o := &Orchestrator{skillManager: sm}

	t.Run("single skill with args", func(t *testing.T) {
		got := o.buildSkillAugmentedRoutingMessage("src/main.go", []string{"code-check"})
		if !strings.Contains(got, "/code-check") {
			t.Errorf("expected /code-check in message, got: %s", got)
		}
		if !strings.Contains(got, "Run static analysis") {
			t.Errorf("expected skill description in message, got: %s", got)
		}
		if !strings.Contains(got, "src/main.go") {
			t.Errorf("expected original args in message, got: %s", got)
		}
	})

	t.Run("skill without args", func(t *testing.T) {
		got := o.buildSkillAugmentedRoutingMessage("", []string{"code-check"})
		if !strings.Contains(got, "/code-check") {
			t.Errorf("expected /code-check in message, got: %s", got)
		}
		if strings.HasSuffix(got, " ") {
			t.Errorf("message should not have trailing space, got: %q", got)
		}
	})

	t.Run("unknown skill falls back to bare name", func(t *testing.T) {
		got := o.buildSkillAugmentedRoutingMessage("args", []string{"unknown-skill"})
		if !strings.Contains(got, "/unknown-skill") {
			t.Errorf("expected /unknown-skill in message, got: %s", got)
		}
		if strings.Contains(got, "skill:") {
			t.Errorf("unknown skill should not have description, got: %s", got)
		}
	})
}

// TestBuildSystemPrompt_PlanMode verifies that buildSystemPrompt includes the
// Plan Context section when PlanModeKey is set in the context.
func TestBuildSystemPrompt_PlanMode(t *testing.T) {
	ctx := context.WithValue(context.Background(), PlanModeKey, true)
	ctx = tools.WithWorkspacePath(ctx, "/test/workspace")

	modelMeta := llm.ModelMetadata{Family: "openai_flagship"}
	result := buildSystemPrompt(ctx, "test message", modelMeta)

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

	modelMeta := llm.ModelMetadata{Family: "openai_flagship"}
	result := buildSystemPrompt(ctx, "test message", modelMeta)

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

// TestBuildSystemPrompt_PlanWithActiveSkills verifies that skill instructions
// appear in the system prompt when PlanModeKey IS set (Plan&Execute mode)
// and active skills are injected via context.
func TestBuildSystemPrompt_PlanWithActiveSkills(t *testing.T) {
	ctx := context.WithValue(context.Background(), PlanModeKey, true)
	ctx = tools.WithWorkspacePath(ctx, "/test/workspace")
	ctx = WithActiveSkills(ctx, &ActiveSkills{
		Skills: []*skills.Skill{
			{
				Metadata: skills.SkillMetadata{
					Name:        "pdf-processing",
					Description: "Extract PDF text and tables.",
				},
				Body:    "Step 1: Read the PDF file using read_file.",
				DirPath: "/skills/pdf-processing",
			},
		},
	})

	modelMeta := llm.ModelMetadata{Family: "openai_flagship"}
	result := buildSystemPrompt(ctx, "process this PDF", modelMeta)

	if !strings.Contains(result, "Active Skills") {
		t.Error("plan mode prompt should contain Active Skills section when skills are active")
	}
	if !strings.Contains(result, "pdf-processing") {
		t.Error("plan mode prompt should contain the skill name")
	}
	if !strings.Contains(result, "Step 1: Read the PDF file") {
		t.Error("plan mode prompt should contain the skill body")
	}
	// Plan Context should also be present (plan mode)
	if !strings.Contains(result, "Plan Context") {
		t.Error("plan mode prompt should contain Plan Context section")
	}
}

// TestBuildSystemPrompt_ReactWithActiveSkills verifies that skill instructions
// appear in the system prompt when PlanModeKey is NOT set (ReAct mode).
// This is the key verification: skills must work in BOTH execution modes.
func TestBuildSystemPrompt_ReactWithActiveSkills(t *testing.T) {
	ctx := context.Background()
	ctx = tools.WithWorkspacePath(ctx, "/test/workspace")
	ctx = WithActiveSkills(ctx, &ActiveSkills{
		Skills: []*skills.Skill{
			{
				Metadata: skills.SkillMetadata{
					Name:         "data-analysis",
					Description:  "Analyze datasets and generate visualizations.",
					AllowedTools: "Read Write Bash(jq:*)",
				},
				Body:    "1. Read the dataset using read_file.\n2. Process with jq.",
				DirPath: "/skills/data-analysis",
			},
		},
	})

	modelMeta := llm.ModelMetadata{Family: "openai_flagship"}
	result := buildSystemPrompt(ctx, "analyze this dataset", modelMeta)

	if !strings.Contains(result, "Active Skills") {
		t.Error("react mode prompt should contain Active Skills section when skills are active")
	}
	if !strings.Contains(result, "data-analysis") {
		t.Error("react mode prompt should contain the skill name")
	}
	if !strings.Contains(result, "Read the dataset") {
		t.Error("react mode prompt should contain the skill body")
	}
	if !strings.Contains(result, "Allowed tools: Read Write Bash(jq:*)") {
		t.Error("react mode prompt should contain the skill allowed-tools")
	}
	// Plan Context should NOT be present (ReAct mode)
	if strings.Contains(result, "Plan Context") {
		t.Error("react mode prompt should NOT contain Plan Context section")
	}
	// ReAct mode should include the Completion section
	if !strings.Contains(result, "single-step mode") {
		t.Error("react mode prompt should contain single-step mode completion instruction")
	}
}

// TestBuildSystemPrompt_NoActiveSkills verifies that when no active skills
// are in the context, neither mode includes the Active Skills section.
func TestBuildSystemPrompt_NoActiveSkills(t *testing.T) {
	for _, planMode := range []bool{true, false} {
		name := "ReAct"
		ctx := context.Background()
		if planMode {
			name = "Plan"
			ctx = context.WithValue(ctx, PlanModeKey, true)
		}
		ctx = tools.WithWorkspacePath(ctx, "/test/workspace")

		t.Run(name, func(t *testing.T) {
			modelMeta := llm.ModelMetadata{Family: "openai_flagship"}
			result := buildSystemPrompt(ctx, "test message", modelMeta)

			if strings.Contains(result, "Active Skills") {
				t.Errorf("%s mode prompt should NOT contain Active Skills section when no skills are active", name)
			}
		})
	}
}

// TestOrchestrator_VectorSearchHints_NilFunc verifies that when vectorSearchFunc is nil,
// HandleMessage works normally without injecting hints (no panic).
func TestOrchestrator_VectorSearchHints_NilFunc(t *testing.T) {
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			// Verify no vector search hints in system prompt
			for _, msg := range req.Messages {
				if msg.Role == "system" && strings.Contains(msg.Content, "Relevant Project Files") {
					t.Error("system prompt should NOT contain vector search hints when func is nil")
				}
			}
			// Router returns needs_clarification to short-circuit
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

	orchestrator := NewOrchestrator(OrchestratorConfig{MaxSteps: 10}, OrchestratorDeps{
		Router:         NewRouter(mockLLM, 5),
		Planner:        NewPlanner(mockLLM),
		LLM:            mockLLM,
		ToolExec:       registry,
		ToolRegistry:   registry,
		TokenCounter:   counter,
		ContextFactory: testContextFactory,
		CircuitBreaker: defaultCircuitBreakerConfig,
	})

	result, err := orchestrator.HandleMessage(context.Background(), "test query", "", HandleOptions{ExecutionMode: "advanced"})
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
}

// TestOrchestrator_VectorSearchHints_WithResults verifies that when vectorSearchFunc
// returns results, hints are injected into the context and available downstream.
func TestOrchestrator_VectorSearchHints_WithResults(t *testing.T) {
	// Create a vector search function that returns test results
	searchFunc := func(ctx context.Context, query string, topK int, fileFilter string) ([]builtins.VectorSearchResult, error) {
		return []builtins.VectorSearchResult{
			{
				FilePath: "src/main.go",
				FileName: "main.go",
				Content:  "package main\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}",
				Score:    0.95,
			},
			{
				FilePath: "src/handler.go",
				FileName: "handler.go",
				Content:  "func handleRequest(w http.ResponseWriter, r *http.Request) {\n\tw.WriteHeader(200)\n}",
				Score:    0.85,
			},
		}, nil
	}

	// Test the hint injection directly via injectVectorSearchHints
	o := &Orchestrator{
		vectorSearchFunc: searchFunc,
	}

	ctx := context.Background()
	ctx = o.injectVectorSearchHints(ctx, "how does the main function work")

	hints := VectorSearchHintsFromContext(ctx)
	if hints == nil {
		t.Fatal("expected vector search hints in context")
	}
	if len(hints.Files) != 2 {
		t.Fatalf("expected 2 hints, got %d", len(hints.Files))
	}
	if hints.Files[0].FilePath != "src/main.go" {
		t.Errorf("expected first hint path 'src/main.go', got %q", hints.Files[0].FilePath)
	}
	if hints.Files[1].FilePath != "src/handler.go" {
		t.Errorf("expected second hint path 'src/handler.go', got %q", hints.Files[1].FilePath)
	}
}

// TestOrchestrator_VectorSearchHints_ContentTruncation verifies that hint summaries
// are truncated to 100 characters.
func TestOrchestrator_VectorSearchHints_ContentTruncation(t *testing.T) {
	longContent := strings.Repeat("a", 200)
	searchFunc := func(ctx context.Context, query string, topK int, fileFilter string) ([]builtins.VectorSearchResult, error) {
		return []builtins.VectorSearchResult{
			{
				FilePath: "long.go",
				FileName: "long.go",
				Content:  longContent,
				Score:    0.9,
			},
		}, nil
	}

	o := &Orchestrator{
		vectorSearchFunc: searchFunc,
	}

	ctx := o.injectVectorSearchHints(context.Background(), "query")
	hints := VectorSearchHintsFromContext(ctx)
	if hints == nil {
		t.Fatal("expected hints")
	}
	if len(hints.Files[0].Summary) != 100 {
		t.Errorf("expected summary length 100, got %d", len(hints.Files[0].Summary))
	}
}

// TestOrchestrator_VectorSearchHints_ErrorSkipped verifies that when the vector search
// function returns an error, hints are silently skipped.
func TestOrchestrator_VectorSearchHints_ErrorSkipped(t *testing.T) {
	searchFunc := func(ctx context.Context, query string, topK int, fileFilter string) ([]builtins.VectorSearchResult, error) {
		return nil, context.DeadlineExceeded
	}

	o := &Orchestrator{
		vectorSearchFunc: searchFunc,
	}

	ctx := o.injectVectorSearchHints(context.Background(), "query")
	hints := VectorSearchHintsFromContext(ctx)
	if hints != nil {
		t.Error("expected nil hints when search returns error")
	}
}

// --- AGENTS.md integration tests ---

// TestOrchestrator_AgentsMD_InjectedWhenPresent verifies that when AGENTS.md
// exists in the workspace root, its content is injected into the context
// and it appears as the first VectorSearchHint.
func TestOrchestrator_AgentsMD_InjectedWhenPresent(t *testing.T) {
	tmpDir := t.TempDir()
	agentsContent := "# Project Instructions\nAlways run tests before committing."
	if err := os.WriteFile(filepath.Join(tmpDir, "AGENTS.md"), []byte(agentsContent), 0o644); err != nil {
		t.Fatalf("failed to write AGENTS.md: %v", err)
	}

	o := &Orchestrator{}
	ctx := tools.WithWorkspacePath(context.Background(), tmpDir)
	ctx = o.injectVectorSearchHints(ctx, "test query")

	// Verify AgentsMD is in context
	amd := AgentsMDFromContext(ctx)
	if amd == nil {
		t.Fatal("expected AgentsMD in context")
	}
	if amd.Content != agentsContent {
		t.Errorf("expected AgentsMD content %q, got %q", agentsContent, amd.Content)
	}

	// Verify AGENTS.md appears as the first hint
	hints := VectorSearchHintsFromContext(ctx)
	if hints == nil {
		t.Fatal("expected VectorSearchHints in context")
	}
	if len(hints.Files) != 1 {
		t.Fatalf("expected 1 hint (AGENTS.md), got %d", len(hints.Files))
	}
	if hints.Files[0].FilePath != "AGENTS.md" {
		t.Errorf("expected hint file path 'AGENTS.md', got %q", hints.Files[0].FilePath)
	}
}

// TestOrchestrator_AgentsMD_AbsentWhenNoWorkspace verifies that no AgentsMD
// is injected when there is no workspace path in the context.
func TestOrchestrator_AgentsMD_AbsentWhenNoWorkspace(t *testing.T) {
	o := &Orchestrator{}
	ctx := o.injectVectorSearchHints(context.Background(), "test query")

	amd := AgentsMDFromContext(ctx)
	if amd != nil {
		t.Error("expected nil AgentsMD when no workspace path")
	}
}

// TestOrchestrator_AgentsMD_AbsentWhenFileMissing verifies that no AgentsMD
// is injected when the workspace exists but has no AGENTS.md file.
func TestOrchestrator_AgentsMD_AbsentWhenFileMissing(t *testing.T) {
	tmpDir := t.TempDir()

	o := &Orchestrator{}
	ctx := tools.WithWorkspacePath(context.Background(), tmpDir)
	ctx = o.injectVectorSearchHints(ctx, "test query")

	amd := AgentsMDFromContext(ctx)
	if amd != nil {
		t.Error("expected nil AgentsMD when AGENTS.md file is missing")
	}
	hints := VectorSearchHintsFromContext(ctx)
	if hints != nil {
		t.Error("expected nil VectorSearchHints when no vector results and no AGENTS.md")
	}
}

// TestOrchestrator_AgentsMD_WithVectorSearch verifies that when both vector
// search results and AGENTS.md exist, AGENTS.md is prepended as the first hint.
func TestOrchestrator_AgentsMD_WithVectorSearch(t *testing.T) {
	tmpDir := t.TempDir()
	agentsContent := "# Project Instructions\nRun make test before committing."
	if err := os.WriteFile(filepath.Join(tmpDir, "AGENTS.md"), []byte(agentsContent), 0o644); err != nil {
		t.Fatalf("failed to write AGENTS.md: %v", err)
	}

	searchFunc := func(ctx context.Context, query string, topK int, fileFilter string) ([]builtins.VectorSearchResult, error) {
		return []builtins.VectorSearchResult{
			{
				FilePath: "src/main.go",
				FileName: "main.go",
				Content:  "package main",
				Score:    0.9,
			},
		}, nil
	}

	o := &Orchestrator{
		vectorSearchFunc: searchFunc,
	}

	ctx := tools.WithWorkspacePath(context.Background(), tmpDir)
	ctx = o.injectVectorSearchHints(ctx, "test query")

	// Verify AgentsMD is in context
	amd := AgentsMDFromContext(ctx)
	if amd == nil {
		t.Fatal("expected AgentsMD in context")
	}

	// Verify AGENTS.md is the first hint, followed by vector results
	hints := VectorSearchHintsFromContext(ctx)
	if hints == nil {
		t.Fatal("expected VectorSearchHints in context")
	}
	if len(hints.Files) != 2 {
		t.Fatalf("expected 2 hints (AGENTS.md + vector result), got %d", len(hints.Files))
	}
	if hints.Files[0].FilePath != "AGENTS.md" {
		t.Errorf("expected first hint to be 'AGENTS.md', got %q", hints.Files[0].FilePath)
	}
	if hints.Files[1].FilePath != "src/main.go" {
		t.Errorf("expected second hint to be 'src/main.go', got %q", hints.Files[1].FilePath)
	}
}

// TestOrchestrator_AgentsMD_NilVectorSearchFunc verifies that AGENTS.md
// is still injected even when vectorSearchFunc is nil.
func TestOrchestrator_AgentsMD_NilVectorSearchFunc(t *testing.T) {
	tmpDir := t.TempDir()
	agentsContent := "# Instructions\nUse go test."
	if err := os.WriteFile(filepath.Join(tmpDir, "AGENTS.md"), []byte(agentsContent), 0o644); err != nil {
		t.Fatalf("failed to write AGENTS.md: %v", err)
	}

	o := &Orchestrator{
		vectorSearchFunc: nil, // no vector search
	}

	ctx := tools.WithWorkspacePath(context.Background(), tmpDir)
	ctx = o.injectVectorSearchHints(ctx, "test query")

	// AgentsMD should still be injected
	amd := AgentsMDFromContext(ctx)
	if amd == nil {
		t.Fatal("expected AgentsMD in context even with nil vectorSearchFunc")
	}

	// VectorSearchHints should contain only AGENTS.md
	hints := VectorSearchHintsFromContext(ctx)
	if hints == nil {
		t.Fatal("expected VectorSearchHints with AGENTS.md hint")
	}
	if len(hints.Files) != 1 || hints.Files[0].FilePath != "AGENTS.md" {
		t.Errorf("expected single AGENTS.md hint, got %v", hints.Files)
	}
}

// ---------------------------------------------------------------------------
// Per-step skills & tools — Normal-mode invariant and narrowing tests (Task 6)
// ---------------------------------------------------------------------------

// newSkillFixture builds an in-memory *skills.Skill for tests. The skill is not
// loaded from disk; we only need Metadata.Name and Body to be non-empty so that
// formatActiveSkills renders it into the system prompt.
func newSkillFixture(name, body string) *skills.Skill {
	return &skills.Skill{
		Metadata: skills.SkillMetadata{Name: name, Description: name + " description"},
		Body:     body,
	}
}

// mockAllTools returns a stable tool descriptor set that covers the critical
// tools plus a few workload-specific ones, used to exercise the Option 3a
// always-include-critical-tools behavior.
func mockAllTools() []tools.ToolDescriptor {
	return []tools.ToolDescriptor{
		{Name: "finish"},
		{Name: "store_fact"},
		{Name: "search_facts"},
		{Name: "ask_user"},
		{Name: "set_step_status"},
		{Name: "read_step_output"},
		{Name: "read_file"},
		{Name: "write_file"},
		{Name: "bash_exec"},
		{Name: "ripgrep"},
	}
}

// TestCoreStepConfigurator_NormalMode_KeepsFullToolPool verifies the hard
// invariant: when resolveAgentProfile returns the default executor profile
// (empty AllowedTools) — as it does for CreateSyntheticPlan — StepConfig leaves
// AllowedTools nil so the SDK falls through to defaults.AllTools (= full pool).
func TestCoreStepConfigurator_NormalMode_KeepsFullToolPool(t *testing.T) {
	cfg := OrchestratorConfig{MaxSteps: 10}
	configurator := coreStepConfigurator(cfg, nil, nil, nil, nil, nil)

	// Simulate CreateSyntheticPlan: AgentProfile emitted as value type, so the
	// *AgentProfile type assertion in resolveAgentProfile fails and the default
	// executor profile (empty AllowedTools, empty Skills) is used.
	syntheticPlan := (&Planner{}).CreateSyntheticPlan("run task", "general")
	step := orchestration.PlanStep{
		ID:          syntheticPlan.Steps[0].ID,
		Description: syntheticPlan.Steps[0].Description,
		Profile:     syntheticPlan.Steps[0].Profile,
	}

	defaults := orchestration.StepDefaults{MaxSteps: 10, AllTools: mockAllTools()}
	stepCfg := configurator(step, defaults)

	if stepCfg.AllowedTools != nil {
		t.Fatalf("normal mode must not filter tools (SDK fall-through expected), got AllowedTools=%v", stepCfg.AllowedTools)
	}
}

// TestCoreStepConfigurator_NormalMode_KeepsRouterMatchedSkills verifies that
// normal-mode synthetic plans emit no step-local SystemPrompt so the SDK falls
// back to cfg.SystemPrompt(ctx, ...) which renders the full task-scope
// ActiveSkills pool.
func TestCoreStepConfigurator_NormalMode_KeepsRouterMatchedSkills(t *testing.T) {
	cfg := OrchestratorConfig{MaxSteps: 10}

	// Wire a task context with two router-matched skills. These are task-wide.
	taskPool := &ActiveSkills{Skills: []*skills.Skill{
		newSkillFixture("alpha", "alpha body"),
		newSkillFixture("beta", "beta body"),
	}}
	taskCtx := WithActiveSkills(context.Background(), taskPool)
	taskCtxProvider := func() context.Context { return taskCtx }

	builder := func(ctx context.Context, userMessage string, _ llm.ModelMetadata) string {
		return formatActiveSkills(ctx, "preamble")
	}

	configurator := coreStepConfigurator(cfg, nil, nil, builder, taskCtxProvider, nil)

	syntheticPlan := (&Planner{}).CreateSyntheticPlan("run task", "general")
	step := orchestration.PlanStep{
		ID:          syntheticPlan.Steps[0].ID,
		Description: syntheticPlan.Steps[0].Description,
		Profile:     syntheticPlan.Steps[0].Profile,
	}

	stepCfg := configurator(step, orchestration.StepDefaults{MaxSteps: 10, AllTools: mockAllTools()})

	// Empty profile.Skills must not trigger step-local prompt synthesis; the
	// SDK will then call cfg.SystemPrompt(taskCtx, ...) itself at run time.
	if stepCfg.SystemPrompt != "" {
		t.Fatalf("expected empty StepConfig.SystemPrompt for normal-mode step (SDK fall-through), got %q", stepCfg.SystemPrompt)
	}
}

// TestCoreStepConfigurator_StepSkillNarrowing verifies Task 4 behavior: when
// profile.Skills is non-empty, the configurator synthesizes a step-local
// SystemPrompt that contains only the named skills.
func TestCoreStepConfigurator_StepSkillNarrowing(t *testing.T) {
	cfg := OrchestratorConfig{MaxSteps: 10}

	taskPool := &ActiveSkills{Skills: []*skills.Skill{
		newSkillFixture("alpha", "alpha body"),
		newSkillFixture("beta", "beta body"),
		newSkillFixture("gamma", "gamma body"),
	}}
	taskCtx := WithActiveSkills(context.Background(), taskPool)

	builder := func(ctx context.Context, _ string, _ llm.ModelMetadata) string {
		return formatActiveSkills(ctx, "preamble")
	}

	configurator := coreStepConfigurator(
		cfg,
		nil,
		nil,
		builder,
		func() context.Context { return taskCtx },
		nil,
	)

	step := orchestration.PlanStep{
		ID:          "step_1",
		Description: "narrow to alpha only",
		Profile:     &AgentProfile{Role: "executor", Skills: []string{"alpha"}},
	}

	stepCfg := configurator(step, orchestration.StepDefaults{MaxSteps: 10, AllTools: mockAllTools()})

	if stepCfg.SystemPrompt == "" {
		t.Fatal("expected non-empty step-local SystemPrompt when profile.Skills is set")
	}
	if !strings.Contains(stepCfg.SystemPrompt, "Skill: alpha") {
		t.Errorf("expected prompt to include Skill: alpha, got %q", stepCfg.SystemPrompt)
	}
	if strings.Contains(stepCfg.SystemPrompt, "Skill: beta") {
		t.Errorf("expected prompt NOT to include Skill: beta (narrowed out), got %q", stepCfg.SystemPrompt)
	}
	if strings.Contains(stepCfg.SystemPrompt, "Skill: gamma") {
		t.Errorf("expected prompt NOT to include Skill: gamma (narrowed out), got %q", stepCfg.SystemPrompt)
	}
}

// TestCoreStepConfigurator_StepToolNarrowing_UnionsCriticalTools verifies Task 3:
// when AllowedTools is non-empty, the configurator unions it with
// criticalAlwaysAllowedTools so the executor can always finish and persist facts.
func TestCoreStepConfigurator_StepToolNarrowing_UnionsCriticalTools(t *testing.T) {
	cfg := OrchestratorConfig{MaxSteps: 10}
	configurator := coreStepConfigurator(cfg, nil, nil, nil, nil, nil)

	// Planner emits a narrowed AllowedTools without the critical tools.
	step := orchestration.PlanStep{
		ID:          "step_1",
		Description: "narrow tools to read_file only",
		Profile:     &AgentProfile{Role: "researcher", AllowedTools: []string{"read_file"}},
	}

	stepCfg := configurator(step, orchestration.StepDefaults{MaxSteps: 10, AllTools: mockAllTools()})

	got := map[string]bool{}
	for _, td := range stepCfg.AllowedTools {
		got[td.Name] = true
	}

	for _, name := range []string{"read_file", "finish", "store_fact", "search_facts", "ask_user", "set_step_status", "read_step_output"} {
		if !got[name] {
			t.Errorf("expected tool %q in AllowedTools, got %v", name, got)
		}
	}
	if got["write_file"] || got["bash_exec"] {
		t.Errorf("expected write_file/bash_exec NOT to be in AllowedTools, got %v", got)
	}
}

// TestCoreStepConfigurator_UnknownSkillDropped verifies that narrowActiveSkills
// drops skill names that are not present in either the task-scope pool or the
// SkillManager, and returns no prompt when the intersection is empty.
func TestCoreStepConfigurator_UnknownSkillDropped(t *testing.T) {
	cfg := OrchestratorConfig{MaxSteps: 10}

	taskPool := &ActiveSkills{Skills: []*skills.Skill{
		newSkillFixture("alpha", "alpha body"),
	}}
	taskCtx := WithActiveSkills(context.Background(), taskPool)

	builderCalls := 0
	builder := func(ctx context.Context, _ string, _ llm.ModelMetadata) string {
		builderCalls++
		return formatActiveSkills(ctx, "preamble")
	}

	configurator := coreStepConfigurator(
		cfg,
		nil,
		nil,
		builder,
		func() context.Context { return taskCtx },
		nil, // no SkillManager, so unknown names cannot be resolved
	)

	t.Run("partial_intersection_renders_kept_only", func(t *testing.T) {
		builderCalls = 0
		step := orchestration.PlanStep{
			ID:          "step_1",
			Description: "alpha plus unknown",
			Profile:     &AgentProfile{Role: "executor", Skills: []string{"alpha", "does_not_exist"}},
		}
		stepCfg := configurator(step, orchestration.StepDefaults{MaxSteps: 10, AllTools: mockAllTools()})

		if builderCalls != 1 {
			t.Fatalf("expected sysPromptBuilder to be called exactly once, got %d", builderCalls)
		}
		if !strings.Contains(stepCfg.SystemPrompt, "Skill: alpha") {
			t.Errorf("expected Skill: alpha in prompt, got %q", stepCfg.SystemPrompt)
		}
		if strings.Contains(stepCfg.SystemPrompt, "does_not_exist") {
			t.Errorf("unknown skill leaked into prompt: %q", stepCfg.SystemPrompt)
		}
	})

	t.Run("empty_intersection_falls_through", func(t *testing.T) {
		builderCalls = 0
		step := orchestration.PlanStep{
			ID:          "step_2",
			Description: "all unknown",
			Profile:     &AgentProfile{Role: "executor", Skills: []string{"does_not_exist"}},
		}
		stepCfg := configurator(step, orchestration.StepDefaults{MaxSteps: 10, AllTools: mockAllTools()})

		if builderCalls != 0 {
			t.Errorf("expected sysPromptBuilder NOT to be called when intersection is empty, got %d calls", builderCalls)
		}
		if stepCfg.SystemPrompt != "" {
			t.Errorf("expected empty SystemPrompt when intersection is empty (SDK fall-through), got %q", stepCfg.SystemPrompt)
		}
	})
}
