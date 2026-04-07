package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

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

// TestBuildSystemPrompt_IncludesAC tests that buildSystemPrompt includes acceptance criteria when provided.
func TestBuildSystemPrompt_IncludesAC(t *testing.T) {
	// Create minimal orchestrator just for this test
	mockLLM := &mockLLMCaller{}
	registry := createTestRegistry()
	counter := llm.NewSimpleTokenCounter()

	orchestrator := NewOrchestrator(
		NewRouter(mockLLM, 5),
		NewACExtractor(mockLLM),
		NewPlanner(mockLLM),
		NewEvaluator(registry, mockLLM),
		mockLLM,
		registry,
		registry,
		counter,
		OrchestratorConfig{MaxSteps: 10},
		testContextFactory,
		nil,
		nil, // logger - nil for tests
		nil, // emitter - nil for tests
		nil, // modelRegistry - nil for tests
		ToolResultBudget{},
		nil, // intentVerifier - nil for tests
	)

	// Test with no criteria
	promptNoAC := orchestrator.buildSystemPrompt(context.Background(), "", nil, false)
	if strings.Contains(promptNoAC, "Acceptance Criteria") {
		t.Error("prompt should NOT contain 'Acceptance Criteria' when no AC provided")
	}
	if !strings.Contains(promptNoAC, "helpful AI assistant") {
		t.Error("prompt should contain basic instructions")
	}

	// Test with criteria
	criteria := []AcceptanceCriterion{
		{ID: "ac_1", Description: "Code compiles successfully"},
		{ID: "ac_2", Description: "All tests pass"},
	}
	promptWithAC := orchestrator.buildSystemPrompt(context.Background(), "", criteria, false)

	if !strings.Contains(promptWithAC, "Acceptance Criteria") {
		t.Error("prompt should contain 'Acceptance Criteria' when AC provided")
	}
	if !strings.Contains(promptWithAC, "ac_1") {
		t.Error("prompt should contain AC ID 'ac_1'")
	}
	if !strings.Contains(promptWithAC, "Code compiles successfully") {
		t.Error("prompt should contain AC description")
	}
	if !strings.Contains(promptWithAC, "ac_2") {
		t.Error("prompt should contain AC ID 'ac_2'")
	}
	if !strings.Contains(promptWithAC, "All tests pass") {
		t.Error("prompt should contain second AC description")
	}
	if !strings.Contains(promptWithAC, "MUST satisfy ALL") {
		t.Error("prompt should contain instruction to satisfy all criteria")
	}
}

// TestOrchestrator_ReactMode tests the plan-execute path with AC extraction, execution, and evaluation.
func TestOrchestrator_ReactMode(t *testing.T) {
	callIdx := 0
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callIdx++
			switch callIdx {
			case 1: // ExtractRaw (Phase 1)
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `[{"id": "rc_1", "description": "Code compiles", "nature": "objective", "weight": "must"}]`,
					},
					StopReason: "end_turn",
				}, nil
			case 2: // Router
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"domain": "code", "complexity": 2, "compaction_strategy": "sliding_window", "suggested_tools": ["bash_exec"], "needs_clarification": false}`,
					},
					StopReason: "end_turn",
				}, nil
			case 3: // AC Extractor (Phase 2)
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `[{"id": "ac_1", "description": "Code compiles", "check_type": "programmatic", "check_cmd": "go build ./..."}]`,
					},
					StopReason: "end_turn",
				}, nil
			case 4: // Planner
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"steps": [{"id": "step_1", "description": "Fix the code", "depends_on": [], "parallelizable": false, "estimated_tools": ["bash_exec"], "relevant_ac": ["ac_1"]}]}`,
					},
					StopReason: "end_turn",
				}, nil
			case 5: // Executor - finish immediately
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: "I will complete the task.",
						ToolCalls: []llm.ToolCall{
							{
								ID:    "call_1",
								Name:  "finish",
								Input: json.RawMessage(`{"answer": "Task completed successfully"}`),
							},
						},
					},
					StopReason: "tool_use",
				}, nil
			case 6: // Evaluator (programmatic check runs via tools.Execute)
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: "YES - the code compiles correctly",
					},
					StopReason: "end_turn",
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
	acExtractor := NewACExtractor(mockLLM)
	planner := NewPlanner(mockLLM)
	evaluator := NewEvaluator(registry, mockLLM)

	orchestrator := NewOrchestrator(
		router,
		acExtractor,
		planner,
		evaluator,
		mockLLM,
		registry,
		registry,
		counter,
		OrchestratorConfig{
			MaxSteps: 10,
		},
		testContextFactory,
		nil, // reflector - nil for Phase 2 tests
		nil, // logger - nil for tests
		nil, // emitter - nil for tests
		nil, // modelRegistry - nil for tests
		ToolResultBudget{},
		nil, // intentVerifier - nil for tests
	)

	result, err := orchestrator.Handle(context.Background(), "Fix the code")
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	if result.RoutingDecision == nil {
		t.Fatal("routing decision is nil")
	}

	if result.RoutingDecision.Domain != "code" {
		t.Errorf("expected domain=code, got %s", result.RoutingDecision.Domain)
	}

	// Should have evaluation result
	if result.EvalResult == nil {
		t.Error("should have eval result")
	} else if !result.EvalResult.AllPassed {
		t.Logf("eval result: passed=%d, failed=%d, unclear=%d",
			len(result.EvalResult.Passed), len(result.EvalResult.Failed), len(result.EvalResult.Unclear))
	}
}

// TestOrchestrator_PlanExecuteMode tests the plan_execute mode path.
func TestOrchestrator_PlanExecuteMode(t *testing.T) {
	callIdx := 0
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callIdx++
			switch callIdx {
			case 1: // ExtractRaw (Phase 1)
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `[{"id": "rc_1", "description": "Tests pass", "nature": "objective", "weight": "must"}]`,
					},
					StopReason: "end_turn",
				}, nil
			case 2: // Router
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"domain": "code", "complexity": 4, "compaction_strategy": "sliding_window", "suggested_tools": ["bash_exec", "file_ops"], "needs_clarification": false}`,
					},
					StopReason: "end_turn",
				}, nil
			case 3: // AC Extractor (Phase 2)
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `[{"id": "ac_1", "description": "Tests pass", "check_type": "programmatic", "check_cmd": "go test ./..."}]`,
					},
					StopReason: "end_turn",
				}, nil
			case 4: // Planner
				return &llm.ChatResponse{
					Message: llm.Message{
						Role: "assistant",
						Content: `{"steps": [
							{"id": "step_1", "description": "Write tests", "depends_on": [], "parallelizable": false, "estimated_tools": ["file_ops"], "relevant_ac": ["ac_1"]},
							{"id": "step_2", "description": "Run tests", "depends_on": ["step_1"], "parallelizable": false, "estimated_tools": ["bash_exec"], "relevant_ac": ["ac_1"]}
						]}`,
					},
					StopReason: "end_turn",
				}, nil
			case 5: // Executor for step_1 - finish
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
			case 6: // Executor for step_2 - finish
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
	acExtractor := NewACExtractor(mockLLM)
	planner := NewPlanner(mockLLM)
	evaluator := NewEvaluator(registry, mockLLM)

	orchestrator := NewOrchestrator(
		router,
		acExtractor,
		planner,
		evaluator,
		mockLLM,
		registry,
		registry,
		counter,
		OrchestratorConfig{
			MaxSteps: 10,
		},
		testContextFactory,
		nil, // reflector - nil for Phase 2 tests
		nil, // logger - nil for tests
		nil, // emitter - nil for tests
		nil, // modelRegistry - nil for tests
		ToolResultBudget{},
		nil, // intentVerifier - nil for tests
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

// TestOrchestrator_NeedsClarificationMode tests the needs_clarification mode.
func TestOrchestrator_NeedsClarificationMode(t *testing.T) {
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			if detectCallType(req) == "route" || detectCallType(req) == "extract_raw" || detectCallType(req) == "enrich" {
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"domain": "general", "complexity": 1, "compaction_strategy": "sliding_window", "suggested_tools": [], "needs_clarification": true}`,
					},
					StopReason: "end_turn",
				}, nil
			}
			return &llm.ChatResponse{
				Message:    llm.Message{Role: "assistant", Content: ""},
				StopReason: "end_turn",
			}, nil
		},
	}

	registry := createTestRegistry()
	counter := llm.NewSimpleTokenCounter()

	router := NewRouter(mockLLM, 5)
	acExtractor := NewACExtractor(mockLLM)
	planner := NewPlanner(mockLLM)
	evaluator := NewEvaluator(registry, mockLLM)

	orchestrator := NewOrchestrator(
		router,
		acExtractor,
		planner,
		evaluator,
		mockLLM,
		registry,
		registry,
		counter,
		OrchestratorConfig{
			MaxSteps: 10,
		},
		testContextFactory,
		nil, // reflector - nil for Phase 2 tests
		nil, // logger - nil for tests
		nil, // emitter - nil for tests
		nil, // modelRegistry - nil for tests
		ToolResultBudget{},
		nil, // intentVerifier - nil for tests
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

	// Should not have plan or eval result
	if result.Plan != nil {
		t.Error("needs_clarification should not have plan")
	}

	if result.EvalResult != nil {
		t.Error("needs_clarification should not have eval result")
	}
}

// TestOrchestrator_HandleResultContainsRoutingDecision verifies HandleResult always has routing info.
func TestOrchestrator_HandleResultContainsRoutingDecision(t *testing.T) {
	modes := []string{"direct", "react", "plan_execute", "needs_clarification"}

	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			var tracker routerCallTracker
			mockLLM := &mockLLMCaller{
				callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
					if detectCallType(req) == "route" || detectCallType(req) == "extract_raw" || detectCallType(req) == "enrich" {
						switch tracker.nextCall(req) {
						case "extract_raw":
							return &llm.ChatResponse{
								Message:    llm.Message{Role: "assistant", Content: `[]`},
								StopReason: "end_turn",
							}, nil
						case "enrich":
							return &llm.ChatResponse{
								Message:    llm.Message{Role: "assistant", Content: `[]`},
								StopReason: "end_turn",
							}, nil
						default: // route
							return &llm.ChatResponse{
								Message: llm.Message{
									Role:    "assistant",
									Content: `{"domain": "general", "complexity": 2, "compaction_strategy": "sliding_window", "suggested_tools": [], "needs_clarification": false}`,
								},
								StopReason: "end_turn",
							}, nil
						}
					}
					// Planner
					if detectCallType(req) == "planner" {
						return &llm.ChatResponse{
							Message: llm.Message{
								Role:    "assistant",
								Content: `{"steps": [{"id": "step_1", "description": "Do task", "depends_on": [], "parallelizable": false, "estimated_tools": [], "relevant_ac": []}]}`,
							},
							StopReason: "end_turn",
						}, nil
					}
					// Executor finish
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

			registry := createTestRegistry()
			counter := llm.NewSimpleTokenCounter()

			orchestrator := NewOrchestrator(
				NewRouter(mockLLM, 5),
				NewACExtractor(mockLLM),
				NewPlanner(mockLLM),
				NewEvaluator(registry, mockLLM),
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
				nil, // intentVerifier - nil for tests
			)

			result, err := orchestrator.Handle(context.Background(), "test")
			if err != nil {
				t.Fatalf("Handle failed: %v", err)
			}

			if result.RoutingDecision == nil {
				t.Fatalf("RoutingDecision should not be nil for mode %s", mode)
			}
		})
	}
}

// TestOrchestrator_RunBackwardsCompatibility tests that Run() is backwards compatible.
func TestOrchestrator_RunBackwardsCompatibility(t *testing.T) {
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			if detectCallType(req) == "route" || detectCallType(req) == "extract_raw" || detectCallType(req) == "enrich" {
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"domain": "general", "complexity": 1, "compaction_strategy": "sliding_window", "suggested_tools": [], "needs_clarification": false}`,
					},
					StopReason: "end_turn",
				}, nil
			}
			if detectCallType(req) == "planner" {
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"steps": [{"id": "step_1", "description": "Respond to greeting", "depends_on": [], "parallelizable": false, "estimated_tools": [], "relevant_ac": []}]}`,
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
		NewACExtractor(mockLLM),
		NewPlanner(mockLLM),
		NewEvaluator(registry, mockLLM),
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
		nil, // intentVerifier - nil for tests
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

// === Phase 3: Retry-loop tests ===

// TestReactMode_RetryOnFailedEval tests that reactor retries when evaluation fails.
func TestReactMode_RetryOnFailedEval(t *testing.T) {
	evalCallCount := 0
	var tracker routerCallTracker
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			if detectCallType(req) == "route" || detectCallType(req) == "extract_raw" || detectCallType(req) == "enrich" {
				switch tracker.nextCall(req) {
				case "extract_raw":
					return &llm.ChatResponse{
						Message: llm.Message{
							Role:    "assistant",
							Content: `[{"id": "rc_1", "description": "Test passes", "nature": "objective", "weight": "must"}]`,
						},
						StopReason: "end_turn",
					}, nil
				case "enrich":
					return &llm.ChatResponse{
						Message: llm.Message{
							Role:    "assistant",
							Content: `[{"id": "ac_1", "description": "Test passes", "check_type": "llm_judge"}]`,
						},
						StopReason: "end_turn",
					}, nil
				default: // route
					return &llm.ChatResponse{
						Message: llm.Message{
							Role:    "assistant",
							Content: `{"domain": "code", "complexity": 2, "compaction_strategy": "sliding_window", "suggested_tools": [], "needs_clarification": false}`,
						},
						StopReason: "end_turn",
					}, nil
				}
			}
			if detectCallType(req) == "planner" {
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"steps": [{"id": "step_1", "description": "Run task", "depends_on": [], "parallelizable": false, "estimated_tools": [], "relevant_ac": ["ac_1"]}]}`,
					},
					StopReason: "end_turn",
				}, nil
			}
			if detectCallType(req) == "executor" {
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: "Executing task",
						ToolCalls: []llm.ToolCall{
							{ID: "c1", Name: "finish", Input: json.RawMessage(`{"answer": "Task result"}`)},
						},
					},
					StopReason: "tool_use",
				}, nil
			}
			if detectCallType(req) == "evaluator_judge" {
				evalCallCount++
				if evalCallCount == 1 {
					// First eval - fail
					return &llm.ChatResponse{
						Message:    llm.Message{Role: "assistant", Content: "NO - test did not pass"},
						StopReason: "end_turn",
					}, nil
				}
				// Second eval - pass
				return &llm.ChatResponse{
					Message:    llm.Message{Role: "assistant", Content: "YES - test passed"},
					StopReason: "end_turn",
				}, nil
			}
			if detectCallType(req) == "reflector" {
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"summary": "Test failed", "failed_criteria": ["ac_1"], "hypotheses": ["Logic error"], "suggested_action": "retry", "reasoning": "Try again", "failure_analysis": "Test assertion failed", "root_cause": "Logic bug", "action_plan": "Fix the bug"}`,
					},
					StopReason: "end_turn",
				}, nil
			}
			return &llm.ChatResponse{
				Message:    llm.Message{Role: "assistant", Content: ""},
				StopReason: "end_turn",
			}, nil
		},
	}

	registry := createTestRegistry()
	counter := llm.NewSimpleTokenCounter()
	reflector := NewReflector(mockLLM)

	orchestrator := NewOrchestrator(
		NewRouter(mockLLM, 5),
		NewACExtractor(mockLLM),
		NewPlanner(mockLLM),
		NewEvaluator(registry, mockLLM),
		mockLLM,
		registry,
		registry,
		counter,
		OrchestratorConfig{MaxSteps: 10, MaxRetries: 3},
		testContextFactory,
		reflector,
		nil, // logger - nil for tests
		nil, // emitter - nil for tests
		nil, // modelRegistry - nil for tests
		ToolResultBudget{},
		nil, // intentVerifier - nil for tests
	)

	result, err := orchestrator.Handle(context.Background(), "Run tests")
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	// Should have succeeded on second attempt
	if result.AttemptCount != 2 {
		t.Errorf("expected 2 attempts, got %d", result.AttemptCount)
	}

	// Should have one reflection from the first failed attempt
	if len(result.Reflections) != 1 {
		t.Errorf("expected 1 reflection, got %d", len(result.Reflections))
	}

	// Eval should pass on final result
	if result.EvalResult == nil || !result.EvalResult.AllPassed {
		t.Error("expected final eval to pass")
	}
}

// TestReactMode_MaxRetriesExhausted tests that reactor stops after max retries.
// NOTE: With the new auto-escalation feature, when max retries are exhausted and planner is available,
// it auto-escalates to plan_execute. This test verifies behavior when planner is nil.
func TestReactMode_MaxRetriesExhausted(t *testing.T) {
	evalCallCount := 0
	var tracker routerCallTracker
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			if detectCallType(req) == "route" || detectCallType(req) == "extract_raw" || detectCallType(req) == "enrich" {
				switch tracker.nextCall(req) {
				case "extract_raw":
					return &llm.ChatResponse{
						Message: llm.Message{
							Role:    "assistant",
							Content: `[{"id": "rc_1", "description": "Test passes", "nature": "objective", "weight": "must"}]`,
						},
						StopReason: "end_turn",
					}, nil
				case "enrich":
					return &llm.ChatResponse{
						Message: llm.Message{
							Role:    "assistant",
							Content: `[{"id": "ac_1", "description": "Test passes", "check_type": "llm_judge"}]`,
						},
						StopReason: "end_turn",
					}, nil
				default: // route
					return &llm.ChatResponse{
						Message: llm.Message{
							Role:    "assistant",
							Content: `{"domain": "code", "complexity": 2, "compaction_strategy": "sliding_window", "suggested_tools": [], "needs_clarification": false}`,
						},
						StopReason: "end_turn",
					}, nil
				}
			}
			if detectCallType(req) == "planner" {
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"steps": [{"id": "step_1", "description": "Run tests", "depends_on": [], "parallelizable": false, "estimated_tools": [], "relevant_ac": ["ac_1"]}]}`,
					},
					StopReason: "end_turn",
				}, nil
			}
			if detectCallType(req) == "executor" {
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: "Executing task",
						ToolCalls: []llm.ToolCall{
							{ID: "c1", Name: "finish", Input: json.RawMessage(`{"answer": "Task result"}`)},
						},
					},
					StopReason: "tool_use",
				}, nil
			}
			if detectCallType(req) == "evaluator_judge" {
				evalCallCount++
				// Always fail
				return &llm.ChatResponse{
					Message:    llm.Message{Role: "assistant", Content: "NO - test did not pass"},
					StopReason: "end_turn",
				}, nil
			}
			if detectCallType(req) == "reflector" {
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"summary": "Test failed", "failed_criteria": ["ac_1"], "hypotheses": ["Logic error"], "suggested_action": "retry", "reasoning": "Try again", "failure_analysis": "Test assertion failed", "root_cause": "Logic bug", "action_plan": "Fix the bug"}`,
					},
					StopReason: "end_turn",
				}, nil
			}
			return &llm.ChatResponse{
				Message:    llm.Message{Role: "assistant", Content: ""},
				StopReason: "end_turn",
			}, nil
		},
	}

	registry := createTestRegistry()
	counter := llm.NewSimpleTokenCounter()
	reflector := NewReflector(mockLLM)
	maxRetries := 2

	// Create orchestrator with planner for plan_execute path
	orchestrator := NewOrchestrator(
		NewRouter(mockLLM, 5),
		NewACExtractor(mockLLM),
		NewPlanner(mockLLM),
		NewEvaluator(registry, mockLLM),
		mockLLM,
		registry,
		registry,
		counter,
		OrchestratorConfig{MaxSteps: 10, MaxRetries: maxRetries},
		testContextFactory,
		reflector,
		nil, // logger - nil for tests
		nil, // emitter - nil for tests
		nil, // modelRegistry - nil for tests
		ToolResultBudget{},
		nil, // intentVerifier - nil for tests
	)

	result, err := orchestrator.Handle(context.Background(), "Run tests")
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	// Should have exhausted all retries (maxRetries + 1 attempts)
	expectedAttempts := maxRetries + 1
	if result.AttemptCount != expectedAttempts {
		t.Errorf("expected %d attempts, got %d", expectedAttempts, result.AttemptCount)
	}

	// Should have reflections from failed attempts (maxRetries reflections)
	if len(result.Reflections) != maxRetries {
		t.Errorf("expected %d reflections, got %d", maxRetries, len(result.Reflections))
	}

	// Final eval should NOT pass
	if result.EvalResult == nil {
		t.Fatal("expected eval result")
	}
	if result.EvalResult.AllPassed {
		t.Error("expected final eval to fail")
	}

	// Output should mention failure
	if !contains(result.Output, "some criteria not met") {
		t.Errorf("expected failure message in output, got: %s", result.Output)
	}
}

// TestReactMode_ReflectorCalled tests that reflector is called on evaluation failure.
func TestReactMode_ReflectorCalled(t *testing.T) {
	reflectorCalled := false
	var tracker routerCallTracker
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			if detectCallType(req) == "route" || detectCallType(req) == "extract_raw" || detectCallType(req) == "enrich" {
				switch tracker.nextCall(req) {
				case "extract_raw":
					return &llm.ChatResponse{
						Message: llm.Message{
							Role:    "assistant",
							Content: `[{"id": "rc_1", "description": "Test passes", "nature": "objective", "weight": "must"}]`,
						},
						StopReason: "end_turn",
					}, nil
				case "enrich":
					return &llm.ChatResponse{
						Message: llm.Message{
							Role:    "assistant",
							Content: `[{"id": "ac_1", "description": "Test passes", "check_type": "llm_judge"}]`,
						},
						StopReason: "end_turn",
					}, nil
				default: // route
					return &llm.ChatResponse{
						Message: llm.Message{
							Role:    "assistant",
							Content: `{"domain": "code", "complexity": 2, "compaction_strategy": "sliding_window", "suggested_tools": [], "needs_clarification": false}`,
						},
						StopReason: "end_turn",
					}, nil
				}
			}
			if detectCallType(req) == "planner" {
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"steps": [{"id": "step_1", "description": "Run task", "depends_on": [], "parallelizable": false, "estimated_tools": [], "relevant_ac": ["ac_1"]}]}`,
					},
					StopReason: "end_turn",
				}, nil
			}
			if detectCallType(req) == "executor" {
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: "Executing task",
						ToolCalls: []llm.ToolCall{
							{ID: "c1", Name: "finish", Input: json.RawMessage(`{"answer": "Task result"}`)},
						},
					},
					StopReason: "tool_use",
				}, nil
			}
			if detectCallType(req) == "evaluator_judge" {
				// Always fail
				return &llm.ChatResponse{
					Message:    llm.Message{Role: "assistant", Content: "NO - test did not pass"},
					StopReason: "end_turn",
				}, nil
			}
			if detectCallType(req) == "reflector" {
				reflectorCalled = true
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"summary": "Test failed", "failed_criteria": ["ac_1"], "hypotheses": ["Logic error"], "suggested_action": "abort", "reasoning": "Cannot fix", "failure_analysis": "Test assertion failed", "root_cause": "Unknown", "action_plan": "None"}`,
					},
					StopReason: "end_turn",
				}, nil
			}
			return &llm.ChatResponse{
				Message:    llm.Message{Role: "assistant", Content: ""},
				StopReason: "end_turn",
			}, nil
		},
	}

	registry := createTestRegistry()
	counter := llm.NewSimpleTokenCounter()
	reflector := NewReflector(mockLLM)

	orchestrator := NewOrchestrator(
		NewRouter(mockLLM, 5),
		NewACExtractor(mockLLM),
		NewPlanner(mockLLM),
		NewEvaluator(registry, mockLLM),
		mockLLM,
		registry,
		registry,
		counter,
		OrchestratorConfig{MaxSteps: 10, MaxRetries: 3},
		testContextFactory,
		reflector,
		nil, // logger - nil for tests
		nil, // emitter - nil for tests
		nil, // modelRegistry - nil for tests
		ToolResultBudget{},
		nil, // intentVerifier - nil for tests
	)

	_, err := orchestrator.Handle(context.Background(), "Run tests")
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	if !reflectorCalled {
		t.Error("expected reflector to be called on evaluation failure")
	}
}

// TestPlanExecute_ReplanOnFailure tests replanning when evaluation fails.
func TestPlanExecute_ReplanOnFailure(t *testing.T) {
	replanCalled := false
	evalCallCount := 0
	var tracker routerCallTracker
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			if detectCallType(req) == "route" || detectCallType(req) == "extract_raw" || detectCallType(req) == "enrich" {
				switch tracker.nextCall(req) {
				case "extract_raw":
					return &llm.ChatResponse{
						Message: llm.Message{
							Role:    "assistant",
							Content: `[{"id": "rc_1", "description": "Test passes", "nature": "objective", "weight": "must"}]`,
						},
						StopReason: "end_turn",
					}, nil
				case "enrich":
					return &llm.ChatResponse{
						Message: llm.Message{
							Role:    "assistant",
							Content: `[{"id": "ac_1", "description": "Test passes", "check_type": "llm_judge"}]`,
						},
						StopReason: "end_turn",
					}, nil
				default: // route
					return &llm.ChatResponse{
						Message: llm.Message{
							Role:    "assistant",
							Content: `{"domain": "code", "complexity": 4, "compaction_strategy": "sliding_window", "suggested_tools": [], "needs_clarification": false}`,
						},
						StopReason: "end_turn",
					}, nil
				}
			}
			if detectCallType(req) == "planner" {
				// Check if this is a replan call (contains "Revise")
				for _, msg := range req.Messages {
					if strings.Contains(msg.Content, "Revise") || strings.Contains(msg.Content, "partially completed") {
						replanCalled = true
						// Return plan with a NEW step ID so buildCarryForward doesn't skip it
						return &llm.ChatResponse{
							Message: llm.Message{
								Role:    "assistant",
								Content: `{"steps": [{"id": "step_1_v2", "description": "Do task (revised)", "depends_on": [], "parallelizable": false, "estimated_tools": [], "relevant_ac": ["ac_1"]}]}`,
							},
							StopReason: "end_turn",
						}, nil
					}
				}
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"steps": [{"id": "step_1", "description": "Do task", "depends_on": [], "parallelizable": false, "estimated_tools": [], "relevant_ac": ["ac_1"]}]}`,
					},
					StopReason: "end_turn",
				}, nil
			}
			if detectCallType(req) == "executor" {
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: "Task done",
						ToolCalls: []llm.ToolCall{
							{ID: "c1", Name: "finish", Input: json.RawMessage(`{"answer": "Done"}`)},
						},
					},
					StopReason: "tool_use",
				}, nil
			}
			if detectCallType(req) == "evaluator_judge" {
				evalCallCount++
				if evalCallCount == 1 {
					return &llm.ChatResponse{
						Message:    llm.Message{Role: "assistant", Content: "NO - needs more work"},
						StopReason: "end_turn",
					}, nil
				}
				return &llm.ChatResponse{
					Message:    llm.Message{Role: "assistant", Content: "YES - looks good"},
					StopReason: "end_turn",
				}, nil
			}
			if detectCallType(req) == "reflector" {
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"summary": "Plan incomplete", "failed_criteria": ["ac_1"], "hypotheses": ["Missing step"], "suggested_action": "replan", "reasoning": "Need to add step", "failure_analysis": "Plan was incomplete", "root_cause": "Missing step", "action_plan": "Add step"}`,
					},
					StopReason: "end_turn",
				}, nil
			}
			return &llm.ChatResponse{
				Message:    llm.Message{Role: "assistant", Content: ""},
				StopReason: "end_turn",
			}, nil
		},
	}

	registry := createTestRegistry()
	counter := llm.NewSimpleTokenCounter()
	reflector := NewReflector(mockLLM)

	orchestrator := NewOrchestrator(
		NewRouter(mockLLM, 5),
		NewACExtractor(mockLLM),
		NewPlanner(mockLLM),
		NewEvaluator(registry, mockLLM),
		mockLLM,
		registry,
		registry,
		counter,
		OrchestratorConfig{MaxSteps: 10, MaxRetries: 3},
		testContextFactory,
		reflector,
		nil, // logger - nil for tests
		nil, // emitter - nil for tests
		nil, // modelRegistry - nil for tests
		ToolResultBudget{},
		nil, // intentVerifier - nil for tests
	)

	result, err := orchestrator.Handle(context.Background(), "Build and test feature")
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	if !replanCalled {
		t.Error("expected Planner.Replan to be called on failure")
	}

	// Should have succeeded after replan
	if result.EvalResult == nil || !result.EvalResult.AllPassed {
		t.Error("expected final eval to pass after replan")
	}
}

// helper function for contains check
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || s != "" && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestPlanExecute_FailedStepBlocksDependents tests that when a step fails,
// its dependent steps are not executed.
func TestPlanExecute_FailedStepBlocksDependents(t *testing.T) {
	step2Executed := false
	var tracker routerCallTracker
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			if detectCallType(req) == "route" || detectCallType(req) == "extract_raw" || detectCallType(req) == "enrich" {
				switch tracker.nextCall(req) {
				case "extract_raw":
					return &llm.ChatResponse{
						Message:    llm.Message{Role: "assistant", Content: `[]`},
						StopReason: "end_turn",
					}, nil
				case "enrich":
					return &llm.ChatResponse{
						Message:    llm.Message{Role: "assistant", Content: `[]`},
						StopReason: "end_turn",
					}, nil
				default: // route
					return &llm.ChatResponse{
						Message: llm.Message{
							Role:    "assistant",
							Content: `{"domain": "code", "complexity": 4, "compaction_strategy": "sliding_window", "suggested_tools": [], "needs_clarification": false}`,
						},
						StopReason: "end_turn",
					}, nil
				}
			}
			if detectCallType(req) == "planner" {
				return &llm.ChatResponse{
					Message: llm.Message{
						Role: "assistant",
						Content: `{"steps": [
							{"id": "step_1", "description": "First step (will fail)", "depends_on": [], "parallelizable": false, "estimated_tools": [], "relevant_ac": []},
							{"id": "step_2", "description": "Second step (depends on first)", "depends_on": ["step_1"], "parallelizable": false, "estimated_tools": [], "relevant_ac": []}
						]}`,
					},
					StopReason: "end_turn",
				}, nil
			}
			if detectCallType(req) == "executor" {
				// Check which step is being executed by examining the task content
				// Look for "→ Step N:" which indicates the CURRENT step being executed
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
			}
			return &llm.ChatResponse{
				Message:    llm.Message{Role: "assistant", Content: ""},
				StopReason: "end_turn",
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
		NewACExtractor(mockLLM),
		NewPlanner(mockLLM),
		NewEvaluator(reg, mockLLM),
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
		nil, // intentVerifier - nil for tests
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

// TestHandleReact_CallsSetTaskWithUserMessage tests that handleReact injects the user's task
// into the context manager via SetTask().
func TestHandleReact_CallsSetTaskWithUserMessage(t *testing.T) {
	// Track SetTask calls
	var setTaskCalls []struct {
		Task     string
		Criteria []AcceptanceCriterion
	}

	// Create a custom context factory that tracks SetTask calls
	trackedContextFactory := func(systemPrompt string, modelMeta llm.ModelMetadata, compactionStrategy string) ContextManager {
		return &trackingContextManager{
			mockContextManager: mockContextManager{
				systemPrompt: systemPrompt,
			},
			onSetTask: func(task string, criteria []AcceptanceCriterion) {
				setTaskCalls = append(setTaskCalls, struct {
					Task     string
					Criteria []AcceptanceCriterion
				}{Task: task, Criteria: criteria})
			},
		}
	}

	var tracker routerCallTracker
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			if detectCallType(req) == "route" || detectCallType(req) == "extract_raw" || detectCallType(req) == "enrich" {
				switch tracker.nextCall(req) {
				case "extract_raw":
					return &llm.ChatResponse{
						Message: llm.Message{
							Role:    "assistant",
							Content: `[{"id": "rc_1", "description": "Task must be completed", "nature": "objective", "weight": "must"}]`,
						},
						StopReason: "end_turn",
					}, nil
				case "enrich":
					return &llm.ChatResponse{
						Message: llm.Message{
							Role:    "assistant",
							Content: `[{"id": "ac_1", "description": "Task must be completed", "check_type": "llm_judge"}]`,
						},
						StopReason: "end_turn",
					}, nil
				default: // route
					return &llm.ChatResponse{
						Message: llm.Message{
							Role:    "assistant",
							Content: `{"domain": "general", "complexity": 2, "compaction_strategy": "sliding_window", "suggested_tools": [], "needs_clarification": false}`,
						},
						StopReason: "end_turn",
					}, nil
				}
			}
			if detectCallType(req) == "planner" {
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"steps": [{"id": "step_1", "description": "Complete task", "depends_on": [], "parallelizable": false, "estimated_tools": [], "relevant_ac": ["ac_1"]}]}`,
					},
					StopReason: "end_turn",
				}, nil
			}
			if detectCallType(req) == "executor" {
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: "Task done",
						ToolCalls: []llm.ToolCall{
							{ID: "c1", Name: "finish", Input: json.RawMessage(`{"answer": "The answer"}`)},
						},
					},
					StopReason: "tool_use",
				}, nil
			}
			if detectCallType(req) == "evaluator_judge" {
				return &llm.ChatResponse{
					Message:    llm.Message{Role: "assistant", Content: "YES - task complete"},
					StopReason: "end_turn",
				}, nil
			}
			return &llm.ChatResponse{
				Message:    llm.Message{Role: "assistant", Content: ""},
				StopReason: "end_turn",
			}, nil
		},
	}

	registry := createTestRegistry()
	counter := llm.NewSimpleTokenCounter()

	orchestrator := NewOrchestrator(
		NewRouter(mockLLM, 5),
		NewACExtractor(mockLLM),
		NewPlanner(mockLLM),
		NewEvaluator(registry, mockLLM),
		mockLLM,
		registry,
		registry,
		counter,
		OrchestratorConfig{MaxSteps: 10},
		trackedContextFactory,
		nil, // No reflector
		nil, // logger - nil for tests
		nil, // emitter - nil for tests
		nil, // modelRegistry - nil for tests
		ToolResultBudget{},
		nil, // intentVerifier - nil for tests
	)

	userMessage := "Please complete this important task"
	_, err := orchestrator.Handle(context.Background(), userMessage)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	// Verify SetTask was called at least once
	if len(setTaskCalls) == 0 {
		t.Fatal("expected SetTask to be called, but it was not")
	}

	// Verify the task was set with content that includes the user's message
	// (In plan_execute mode, SetTask receives the step task definition which embeds the user message)
	found := false
	for _, call := range setTaskCalls {
		if strings.Contains(call.Task, userMessage) {
			found = true
			// Verify criteria was also passed
			if len(call.Criteria) == 0 {
				t.Error("expected criteria to be passed to SetTask")
			}
			if len(call.Criteria) > 0 && call.Criteria[0].ID != "ac_1" {
				t.Errorf("expected criteria ID 'ac_1', got '%s'", call.Criteria[0].ID)
			}
			break
		}
	}
	if !found {
		t.Errorf("expected SetTask to be called with task containing user message '%s', got calls: %+v", userMessage, setTaskCalls)
	}
}

// trackingContextManager wraps mockContextManager to track SetTask calls.
type trackingContextManager struct {
	mockContextManager
	onSetTask func(task string, criteria []AcceptanceCriterion)
}

func (t *trackingContextManager) SetTask(task string, criteria []AcceptanceCriterion) {
	t.mockContextManager.SetTask(task, criteria)
	if t.onSetTask != nil {
		t.onSetTask(task, criteria)
	}
}

// TestBuildSystemPrompt_IncludesToolUsageDirective tests that the system prompt
// includes directive language about using tools to discover information.
func TestBuildSystemPrompt_IncludesToolUsageDirective(t *testing.T) {
	mockLLM := &mockLLMCaller{}
	registry := createTestRegistry()
	counter := llm.NewSimpleTokenCounter()

	orchestrator := NewOrchestrator(
		NewRouter(mockLLM, 5),
		NewACExtractor(mockLLM),
		NewPlanner(mockLLM),
		NewEvaluator(registry, mockLLM),
		mockLLM,
		registry,
		registry,
		counter,
		OrchestratorConfig{MaxSteps: 10},
		testContextFactory,
		nil,
		nil, // logger - nil for tests
		nil, // emitter - nil for tests
		nil, // modelRegistry - nil for tests
		ToolResultBudget{},
		nil, // intentVerifier - nil for tests
	)

	prompt := orchestrator.buildSystemPrompt(context.Background(), "", nil, false)

	// Check for directive language about using tools
	if !strings.Contains(prompt, "USE your tools to discover") {
		t.Error("prompt should contain 'USE your tools to discover'")
	}

	// Check for mention of bash and available tools
	if !strings.Contains(prompt, "bash") {
		t.Error("prompt should mention bash tool")
	}

	// Check for directive about not guessing
	if !strings.Contains(prompt, "Do NOT guess") {
		t.Error("prompt should contain 'Do NOT guess'")
	}

	// Check for directive about using tools for environment/identity questions
	if !strings.Contains(prompt, "MUST use tools to discover") {
		t.Error("prompt should contain 'MUST use tools to discover'")
	}
}

// TestPlanExecute_StepFailureTriggersReflection tests that step execution failure
// triggers reflection and replan flow.
func TestPlanExecute_StepFailureTriggersReflection(t *testing.T) {
	reflectorCalled := false
	replanCalled := false
	attemptCount := 0

	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			if detectCallType(req) == "route" || detectCallType(req) == "extract_raw" || detectCallType(req) == "enrich" {
				hasAC := false
				for _, msg := range req.Messages {
					if strings.Contains(msg.Content, "Domain:") {
						hasAC = true
						break
					}
				}
				if hasAC {
					return &llm.ChatResponse{
						Message: llm.Message{
							Role:    "assistant",
							Content: `[{"id": "ac_1", "description": "Task completes", "check_type": "llm_judge"}]`,
						},
						StopReason: "end_turn",
					}, nil
				}
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"domain": "code", "complexity": 4, "compaction_strategy": "sliding_window", "suggested_tools": [], "needs_clarification": false}`,
					},
					StopReason: "end_turn",
				}, nil
			}
			if detectCallType(req) == "planner" {
				// Check if this is a replan call
				for _, msg := range req.Messages {
					if strings.Contains(msg.Content, "Revise") || strings.Contains(msg.Content, "partially completed") {
						replanCalled = true
						// Return plan with a NEW step ID so buildCarryForward doesn't skip it
						return &llm.ChatResponse{
							Message: llm.Message{
								Role:    "assistant",
								Content: `{"steps": [{"id": "step_1_v2", "description": "Do task (revised)", "depends_on": [], "parallelizable": false, "estimated_tools": [], "relevant_ac": ["ac_1"]}]}`,
							},
							StopReason: "end_turn",
						}, nil
					}
				}
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"steps": [{"id": "step_1", "description": "Do task", "depends_on": [], "parallelizable": false, "estimated_tools": [], "relevant_ac": ["ac_1"]}]}`,
					},
					StopReason: "end_turn",
				}, nil
			}
			if detectCallType(req) == "executor" {
				attemptCount++
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: "Task done",
						ToolCalls: []llm.ToolCall{
							{ID: "c1", Name: "finish", Input: json.RawMessage(`{"answer": "Done"}`)},
						},
					},
					StopReason: "tool_use",
				}, nil
			}
			if detectCallType(req) == "evaluator_judge" {
				// Fail first eval, pass second
				if attemptCount <= 1 {
					return &llm.ChatResponse{
						Message:    llm.Message{Role: "assistant", Content: "NO - task not complete"},
						StopReason: "end_turn",
					}, nil
				}
				return &llm.ChatResponse{
					Message:    llm.Message{Role: "assistant", Content: "YES - task complete"},
					StopReason: "end_turn",
				}, nil
			}
			if detectCallType(req) == "reflector" {
				reflectorCalled = true
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"summary": "Step failed", "failed_criteria": ["ac_1"], "hypotheses": ["Need different approach"], "suggested_action": "replan", "reasoning": "Replan needed", "failure_analysis": "Step error", "root_cause": "Unknown", "action_plan": "Replan"}`,
					},
					StopReason: "end_turn",
				}, nil
			}
			return &llm.ChatResponse{
				Message:    llm.Message{Role: "assistant", Content: ""},
				StopReason: "end_turn",
			}, nil
		},
	}

	registry := createTestRegistry()
	counter := llm.NewSimpleTokenCounter()
	reflector := NewReflector(mockLLM)

	orchestrator := NewOrchestrator(
		NewRouter(mockLLM, 5),
		NewACExtractor(mockLLM),
		NewPlanner(mockLLM),
		NewEvaluator(registry, mockLLM),
		mockLLM,
		registry,
		registry,
		counter,
		OrchestratorConfig{MaxSteps: 10, MaxRetries: 3},
		testContextFactory,
		reflector,
		nil, // logger - nil for tests
		nil, // emitter - nil for tests
		nil, // modelRegistry - nil for tests
		ToolResultBudget{},
		nil, // intentVerifier - nil for tests
	)

	result, err := orchestrator.Handle(context.Background(), "Do task with potential failure")
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	if !reflectorCalled {
		t.Error("expected reflector to be called when evaluation fails")
	}

	if !replanCalled {
		t.Error("expected replan to be called when reflector suggests replan")
	}

	// Should have succeeded after replan
	if result.EvalResult == nil || !result.EvalResult.AllPassed {
		t.Error("expected final eval to pass after replan")
	}

	// Should have at least 2 attempts
	if result.AttemptCount < 2 {
		t.Errorf("expected at least 2 attempts, got %d", result.AttemptCount)
	}
}

// TestPlanExecute_StepLifecycleEvents verifies that PlanStepStart and PlanStepComplete
// events are emitted for each plan step during plan_execute mode.
func TestPlanExecute_StepLifecycleEvents(t *testing.T) {
	mockEm := &mockEmitter{}

	var tracker routerCallTracker
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			if detectCallType(req) == "route" || detectCallType(req) == "extract_raw" || detectCallType(req) == "enrich" {
				switch tracker.nextCall(req) {
				case "extract_raw":
					return &llm.ChatResponse{
						Message:    llm.Message{Role: "assistant", Content: `[]`},
						StopReason: "end_turn",
					}, nil
				case "enrich":
					return &llm.ChatResponse{
						Message:    llm.Message{Role: "assistant", Content: `[]`},
						StopReason: "end_turn",
					}, nil
				default: // route
					return &llm.ChatResponse{
						Message: llm.Message{
							Role:    "assistant",
							Content: `{"domain": "code", "complexity": 4, "compaction_strategy": "sliding_window", "suggested_tools": [], "needs_clarification": false}`,
						},
						StopReason: "end_turn",
					}, nil
				}
			}
			if detectCallType(req) == "planner" {
				return &llm.ChatResponse{
					Message: llm.Message{
						Role: "assistant",
						Content: `{"steps": [
							{"id": "step_1", "description": "First step", "depends_on": [], "parallelizable": false, "estimated_tools": [], "relevant_ac": []},
						{"id": "step_2", "description": "Second step", "depends_on": ["step_1"], "parallelizable": false, "estimated_tools": [], "relevant_ac": []}
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
		NewACExtractor(mockLLM),
		NewPlanner(mockLLM),
		NewEvaluator(registry, mockLLM),
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
		nil, // intentVerifier - nil for tests
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

// === Step-Level Retry Tests ===

// TestPlanExecute_StepLevelRetry tests that only failed steps are re-executed on retry.
// Steps are chained with dependencies to avoid parallel execution race conditions.
func TestPlanExecute_StepLevelRetry(t *testing.T) {
	var step1Attempt1Output string
	var step2Attempt1Output string
	var step2Attempt2Output string
	evalCallCount := 0
	var tracker routerCallTracker
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			if detectCallType(req) == "route" || detectCallType(req) == "extract_raw" || detectCallType(req) == "enrich" {
				switch tracker.nextCall(req) {
				case "extract_raw":
					return &llm.ChatResponse{
						Message: llm.Message{
							Role:    "assistant",
							Content: `[{"id": "rc_1", "description": "Step 1 completes", "nature": "objective", "weight": "must"}, {"id": "rc_2", "description": "Step 2 completes", "nature": "objective", "weight": "must"}]`,
						},
						StopReason: "end_turn",
					}, nil
				case "enrich":
					return &llm.ChatResponse{
						Message: llm.Message{
							Role:    "assistant",
							Content: `[{"id": "ac_1", "description": "Step 1 completes", "check_type": "llm_judge"}, {"id": "ac_2", "description": "Step 2 completes", "check_type": "llm_judge"}]`,
						},
						StopReason: "end_turn",
					}, nil
				default: // route
					return &llm.ChatResponse{
						Message: llm.Message{
							Role:    "assistant",
							Content: `{"domain": "code", "complexity": 4, "compaction_strategy": "sliding_window", "suggested_tools": [], "needs_clarification": false}`,
						},
						StopReason: "end_turn",
					}, nil
				}
			}
			if detectCallType(req) == "planner" {
				return &llm.ChatResponse{
					Message: llm.Message{
						Role: "assistant",
						Content: `{"steps": [
							{"id": "step_1", "description": "First step", "depends_on": [], "parallelizable": false, "estimated_tools": [], "relevant_ac": ["ac_1"]},
							{"id": "step_2", "description": "Second step", "depends_on": ["step_1"], "parallelizable": false, "estimated_tools": [], "relevant_ac": ["ac_2"]}
						]}`,
					},
					StopReason: "end_turn",
				}, nil
			}
			if detectCallType(req) == "executor" {
				// Track which step is being executed by examining task content
				// Look for "→ Step N:" which indicates the CURRENT step being executed
				stepID := ""
				for _, msg := range req.Messages {
					if strings.Contains(msg.Content, "→ Step 1:") {
						stepID = "step_1"
						break
					}
					if strings.Contains(msg.Content, "→ Step 2:") {
						stepID = "step_2"
						break
					}
				}

				// Return different outputs based on attempt
				if stepID == "step_1" {
					step1Attempt1Output = "Step 1 completed"
					return &llm.ChatResponse{
						Message: llm.Message{
							Role:    "assistant",
							Content: "Step 1 output from attempt 1",
							ToolCalls: []llm.ToolCall{
								{ID: "c1", Name: "finish", Input: json.RawMessage(`{"answer": "Step 1 completed"}`)},
							},
						},
						StopReason: "tool_use",
					}, nil
				}
				// step_2 - check if this is attempt 1 or retry
				if step2Attempt1Output == "" {
					// First attempt - will fail eval
					step2Attempt1Output = "Step 2 attempt 1"
					return &llm.ChatResponse{
						Message: llm.Message{
							Role:    "assistant",
							Content: "Step 2 output from attempt 1 (will fail)",
							ToolCalls: []llm.ToolCall{
								{ID: "c2", Name: "finish", Input: json.RawMessage(`{"answer": "Step 2 attempt 1"}`)},
							},
						},
						StopReason: "tool_use",
					}, nil
				}
				// Second attempt - will pass eval
				step2Attempt2Output = "Step 2 attempt 2"
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: "Step 2 output from attempt 2 (will pass)",
						ToolCalls: []llm.ToolCall{
							{ID: "c3", Name: "finish", Input: json.RawMessage(`{"answer": "Step 2 attempt 2"}`)},
						},
					},
					StopReason: "tool_use",
				}, nil
			}
			if detectCallType(req) == "evaluator_judge" {
				// The evaluator is called once per criterion, so we need to track which criterion is being evaluated
				// by examining the request content
				evalCallCount++
				isAC2 := false
				for _, msg := range req.Messages {
					if strings.Contains(msg.Content, "Step 2 completes") {
						isAC2 = true
						break
					}
				}
				if isAC2 {
					// Evaluating ac_2 (step 2)
					if evalCallCount <= 2 {
						// First eval - fails
						return &llm.ChatResponse{
							Message:    llm.Message{Role: "assistant", Content: "NO - step 2 failed"},
							StopReason: "end_turn",
						}, nil
					}
					// Second eval - passes
					return &llm.ChatResponse{
						Message:    llm.Message{Role: "assistant", Content: "YES - step 2 passed"},
						StopReason: "end_turn",
					}, nil
				}
				// Evaluating ac_1 (step 1) - always passes
				return &llm.ChatResponse{
					Message:    llm.Message{Role: "assistant", Content: "YES - step 1 passed"},
					StopReason: "end_turn",
				}, nil
			}
			if detectCallType(req) == "reflector" {
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"summary": "Step 2 failed", "failed_criteria": ["ac_2"], "hypotheses": ["Logic error"], "suggested_action": "retry", "reasoning": "Try again", "failure_analysis": "Step 2 failed", "root_cause": "Bug", "action_plan": "Fix"}`,
					},
					StopReason: "end_turn",
				}, nil
			}
			return &llm.ChatResponse{
				Message:    llm.Message{Role: "assistant", Content: ""},
				StopReason: "end_turn",
			}, nil
		},
	}

	registry := createTestRegistry()
	counter := llm.NewSimpleTokenCounter()
	reflector := NewReflector(mockLLM)

	orchestrator := NewOrchestrator(
		NewRouter(mockLLM, 5),
		NewACExtractor(mockLLM),
		NewPlanner(mockLLM),
		NewEvaluator(registry, mockLLM),
		mockLLM,
		registry,
		registry,
		counter,
		OrchestratorConfig{MaxSteps: 10, MaxRetries: 3},
		testContextFactory,
		reflector,
		nil, // logger - nil for tests
		nil, // emitter - nil for tests
		nil, // modelRegistry - nil for tests
		ToolResultBudget{},
		nil, // intentVerifier - nil for tests
	)

	result, err := orchestrator.Handle(context.Background(), "Run two steps")
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	// Should have succeeded on second attempt
	if result.AttemptCount != 2 {
		t.Errorf("expected 2 attempts, got %d", result.AttemptCount)
	}

	// Eval should pass on final result
	if result.EvalResult == nil || !result.EvalResult.AllPassed {
		t.Error("expected final eval to pass")
	}

	// Note: aggregateOutput only returns terminal step outputs (steps that no other step depends on)
	// In this test: step_1 -> step_2, so only step_2 output appears in final result
	// Verify step_2 (the terminal step) has the new output from attempt 2
	if !strings.Contains(result.Output, step2Attempt2Output) {
		t.Errorf("expected step_2 output from attempt 2, got output: %s", result.Output)
	}

	// Verify step_2 was re-executed (step_2 has both attempt 1 and attempt 2 outputs recorded)
	if step2Attempt1Output == "" {
		t.Error("expected step_2 to have been executed in attempt 1")
	}
	if step2Attempt2Output == "" {
		t.Error("expected step_2 to have been re-executed in attempt 2")
	}

	// Verify step_1 was executed (its output was captured)
	if step1Attempt1Output == "" {
		t.Error("expected step_1 to have been executed")
	}
}

// TestPlanExecute_StepLevelRetry_WithDependents tests that transitive dependents are NOT re-executed.
// Steps are chained: step_1 -> step_2 -> step_3 to ensure sequential execution.
// Only step_2 (directly mapped to the failed criterion) is retried; step_3 keeps its original output.
func TestPlanExecute_StepLevelRetry_WithDependents(t *testing.T) {
	var step1Output string
	var step2Attempt1Output string
	var step2Attempt2Output string
	var step3Attempt1Output string
	var step3Attempt2Output string
	evalCallCount := 0
	var tracker routerCallTracker
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			if detectCallType(req) == "route" || detectCallType(req) == "extract_raw" || detectCallType(req) == "enrich" {
				switch tracker.nextCall(req) {
				case "extract_raw":
					return &llm.ChatResponse{
						Message: llm.Message{
							Role:    "assistant",
							Content: `[{"id": "rc_1", "description": "Step 1 completes", "nature": "objective", "weight": "must"}, {"id": "rc_2", "description": "Step 2 completes", "nature": "objective", "weight": "must"}, {"id": "rc_3", "description": "Step 3 completes", "nature": "objective", "weight": "must"}]`,
						},
						StopReason: "end_turn",
					}, nil
				case "enrich":
					return &llm.ChatResponse{
						Message: llm.Message{
							Role:    "assistant",
							Content: `[{"id": "ac_1", "description": "Step 1 completes", "check_type": "llm_judge"}, {"id": "ac_2", "description": "Step 2 completes", "check_type": "llm_judge"}, {"id": "ac_3", "description": "Step 3 completes", "check_type": "llm_judge"}]`,
						},
						StopReason: "end_turn",
					}, nil
				default: // route
					return &llm.ChatResponse{
						Message: llm.Message{
							Role:    "assistant",
							Content: `{"domain": "code", "complexity": 4, "compaction_strategy": "sliding_window", "suggested_tools": [], "needs_clarification": false}`,
						},
						StopReason: "end_turn",
					}, nil
				}
			}
			if detectCallType(req) == "planner" {
				return &llm.ChatResponse{
					Message: llm.Message{
						Role: "assistant",
						Content: `{"steps": [
							{"id": "step_1", "description": "First step", "depends_on": [], "parallelizable": false, "estimated_tools": [], "relevant_ac": ["ac_1"]},
							{"id": "step_2", "description": "Second step", "depends_on": ["step_1"], "parallelizable": false, "estimated_tools": [], "relevant_ac": ["ac_2"]},
							{"id": "step_3", "description": "Third step depends on step_2", "depends_on": ["step_2"], "parallelizable": false, "estimated_tools": [], "relevant_ac": ["ac_3"]}
						]}`,
					},
					StopReason: "end_turn",
				}, nil
			}
			if detectCallType(req) == "executor" {
				// Track which step is being executed
				// Look for "→ Step N:" which indicates the CURRENT step being executed
				stepID := ""
				for _, msg := range req.Messages {
					if strings.Contains(msg.Content, "→ Step 1:") {
						stepID = "step_1"
						break
					}
					if strings.Contains(msg.Content, "→ Step 2:") {
						stepID = "step_2"
						break
					}
					if strings.Contains(msg.Content, "→ Step 3:") {
						stepID = "step_3"
						break
					}
				}

				if stepID == "step_1" {
					step1Output = "Step 1 done"
					return &llm.ChatResponse{
						Message: llm.Message{
							Role:    "assistant",
							Content: "Step 1 output (preserved)",
							ToolCalls: []llm.ToolCall{
								{ID: "c1", Name: "finish", Input: json.RawMessage(`{"answer": "Step 1 done"}`)},
							},
						},
						StopReason: "tool_use",
					}, nil
				}
				if stepID == "step_2" {
					if step2Attempt1Output == "" {
						// First attempt
						step2Attempt1Output = "Step 2 attempt 1"
						return &llm.ChatResponse{
							Message: llm.Message{
								Role:    "assistant",
								Content: "Step 2 output attempt 1 (will fail)",
								ToolCalls: []llm.ToolCall{
									{ID: "c2", Name: "finish", Input: json.RawMessage(`{"answer": "Step 2 attempt 1"}`)},
								},
							},
							StopReason: "tool_use",
						}, nil
					}
					// Retry attempt
					step2Attempt2Output = "Step 2 attempt 2"
					return &llm.ChatResponse{
						Message: llm.Message{
							Role:    "assistant",
							Content: "Step 2 output attempt 2 (will pass)",
							ToolCalls: []llm.ToolCall{
								{ID: "c3", Name: "finish", Input: json.RawMessage(`{"answer": "Step 2 attempt 2"}`)},
							},
						},
						StopReason: "tool_use",
					}, nil
				}
				// step_3 - depends on step_2
				if stepID == "step_3" {
					if step3Attempt1Output == "" {
						// First attempt
						step3Attempt1Output = "Step 3 attempt 1"
						return &llm.ChatResponse{
							Message: llm.Message{
								Role:    "assistant",
								Content: "Step 3 output attempt 1",
								ToolCalls: []llm.ToolCall{
									{ID: "c4", Name: "finish", Input: json.RawMessage(`{"answer": "Step 3 attempt 1"}`)},
								},
							},
							StopReason: "tool_use",
						}, nil
					}
					// Retry attempt
					step3Attempt2Output = "Step 3 attempt 2"
					return &llm.ChatResponse{
						Message: llm.Message{
							Role:    "assistant",
							Content: "Step 3 output attempt 2",
							ToolCalls: []llm.ToolCall{
								{ID: "c5", Name: "finish", Input: json.RawMessage(`{"answer": "Step 3 attempt 2"}`)},
							},
						},
						StopReason: "tool_use",
					}, nil
				}
				// Default: unmatched executor call (e.g. evaluator reconsider misidentified as executor)
				return &llm.ChatResponse{
					Message:    llm.Message{Role: "assistant", Content: "NO - not satisfied"},
					StopReason: "end_turn",
				}, nil
			}
			if detectCallType(req) == "evaluator_judge" {
				// The evaluator is called once per criterion, so we need to track which criterion is being evaluated
				evalCallCount++
				isAC2 := false
				for _, msg := range req.Messages {
					if strings.Contains(msg.Content, "Step 2 completes") {
						isAC2 = true
						break
					}
				}
				if isAC2 {
					// Evaluating ac_2 (step 2)
					if evalCallCount <= 3 {
						// First eval - fails (called after attempt 1 along with ac_1 and ac_3)
						return &llm.ChatResponse{
							Message:    llm.Message{Role: "assistant", Content: "NO - step 2 failed"},
							StopReason: "end_turn",
						}, nil
					}
					// Second eval - passes
					return &llm.ChatResponse{
						Message:    llm.Message{Role: "assistant", Content: "YES - step 2 passed"},
						StopReason: "end_turn",
					}, nil
				}
				// Evaluating ac_1 or ac_3 - always passes
				return &llm.ChatResponse{
					Message:    llm.Message{Role: "assistant", Content: "YES - passed"},
					StopReason: "end_turn",
				}, nil
			}
			if detectCallType(req) == "reflector" {
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"summary": "Step 2 failed", "failed_criteria": ["ac_2"], "hypotheses": ["Logic error"], "suggested_action": "retry", "reasoning": "Try again", "failure_analysis": "Step 2 failed", "root_cause": "Bug", "action_plan": "Fix"}`,
					},
					StopReason: "end_turn",
				}, nil
			}
			return &llm.ChatResponse{
				Message:    llm.Message{Role: "assistant", Content: ""},
				StopReason: "end_turn",
			}, nil
		},
	}

	registry := createTestRegistry()
	counter := llm.NewSimpleTokenCounter()
	reflector := NewReflector(mockLLM)

	orchestrator := NewOrchestrator(
		NewRouter(mockLLM, 5),
		NewACExtractor(mockLLM),
		NewPlanner(mockLLM),
		NewEvaluator(registry, mockLLM),
		mockLLM,
		registry,
		registry,
		counter,
		OrchestratorConfig{MaxSteps: 10, MaxRetries: 3},
		testContextFactory,
		reflector,
		nil, // logger - nil for tests
		nil, // emitter - nil for tests
		nil, // modelRegistry - nil for tests
		ToolResultBudget{},
		nil, // intentVerifier - nil for tests
	)

	result, err := orchestrator.Handle(context.Background(), "Run steps with dependency")
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	// Should have succeeded on second attempt
	if result.AttemptCount != 2 {
		t.Errorf("expected 2 attempts, got %d", result.AttemptCount)
	}

	// Eval should pass on final result
	if result.EvalResult == nil || !result.EvalResult.AllPassed {
		t.Error("expected final eval to pass")
	}

	// Note: aggregateOutput only returns terminal step outputs (steps that no other step depends on)
	// In this test: step_1 -> step_2 -> step_3, so only step_3 output appears in final result
	// Since transitive expansion is removed, step_3 is NOT re-executed and keeps its attempt 1 output
	if !strings.Contains(result.Output, step3Attempt1Output) {
		t.Errorf("expected step_3 output from attempt 1 (not re-executed), got output: %s", result.Output)
	}

	// Verify step_1 was executed (not re-executed since it passed)
	if step1Output == "" {
		t.Error("expected step_1 to have been executed")
	}

	// Verify step_2 was re-executed (directly mapped to failed criterion ac_2)
	if step2Attempt1Output == "" {
		t.Error("expected step_2 to have been executed in attempt 1")
	}
	if step2Attempt2Output == "" {
		t.Error("expected step_2 to have been re-executed in attempt 2")
	}
	// step_3 should NOT have been re-executed (no transitive expansion)
	if step3Attempt1Output == "" {
		t.Error("expected step_3 to have been executed in attempt 1")
	}
	if step3Attempt2Output != "" {
		t.Errorf("expected step_3 to NOT be re-executed (no transitive expansion), but got attempt 2 output: %s", step3Attempt2Output)
	}
}

// TestPlanExecute_StepLevelRetry_FallbackToFull tests fallback to full retry when no RelevantAC mapping.
// Steps are chained with dependencies to ensure sequential execution.
func TestPlanExecute_StepLevelRetry_FallbackToFull(t *testing.T) {
	var step1Attempt1Output string
	var step1Attempt2Output string
	var step2Attempt1Output string
	var step2Attempt2Output string
	evalCallCount := 0
	var tracker routerCallTracker
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			if detectCallType(req) == "route" || detectCallType(req) == "extract_raw" || detectCallType(req) == "enrich" {
				switch tracker.nextCall(req) {
				case "extract_raw":
					return &llm.ChatResponse{
						Message: llm.Message{
							Role:    "assistant",
							Content: `[{"id": "rc_1", "description": "Task completes", "nature": "objective", "weight": "must"}]`,
						},
						StopReason: "end_turn",
					}, nil
				case "enrich":
					return &llm.ChatResponse{
						Message: llm.Message{
							Role:    "assistant",
							Content: `[{"id": "ac_1", "description": "Task completes", "check_type": "llm_judge"}]`,
						},
						StopReason: "end_turn",
					}, nil
				default: // route
					return &llm.ChatResponse{
						Message: llm.Message{
							Role:    "assistant",
							Content: `{"domain": "code", "complexity": 4, "compaction_strategy": "sliding_window", "suggested_tools": [], "needs_clarification": false}`,
						},
						StopReason: "end_turn",
					}, nil
				}
			}
			if detectCallType(req) == "planner" {
				return &llm.ChatResponse{
					Message: llm.Message{
						Role: "assistant",
						Content: `{"steps": [
							{"id": "step_1", "description": "First step", "depends_on": [], "parallelizable": false, "estimated_tools": [], "relevant_ac": []},
							{"id": "step_2", "description": "Second step", "depends_on": ["step_1"], "parallelizable": false, "estimated_tools": [], "relevant_ac": []}
						]}`,
					},
					StopReason: "end_turn",
				}, nil
			}
			if detectCallType(req) == "executor" {
				// Look for "→ Step N:" which indicates the CURRENT step being executed
				stepNum := ""
				for _, msg := range req.Messages {
					if strings.Contains(msg.Content, "→ Step 1:") {
						stepNum = "1"
						break
					}
					if strings.Contains(msg.Content, "→ Step 2:") {
						stepNum = "2"
						break
					}
				}

				if stepNum == "1" {
					if step1Attempt1Output == "" {
						// First attempt
						step1Attempt1Output = "Step 1 done"
						return &llm.ChatResponse{
							Message: llm.Message{
								Role:    "assistant",
								Content: "Step 1 output attempt 1",
								ToolCalls: []llm.ToolCall{
									{ID: "c1", Name: "finish", Input: json.RawMessage(`{"answer": "Step 1 done"}`)},
								},
							},
							StopReason: "tool_use",
						}, nil
					}
					// Second attempt - full retry (no RelevantAC mapping)
					step1Attempt2Output = "Step 1 retry"
					return &llm.ChatResponse{
						Message: llm.Message{
							Role:    "assistant",
							Content: "Step 1 output attempt 2",
							ToolCalls: []llm.ToolCall{
								{ID: "c1b", Name: "finish", Input: json.RawMessage(`{"answer": "Step 1 retry"}`)},
							},
						},
						StopReason: "tool_use",
					}, nil
				}
				// step 2
				if step2Attempt1Output == "" {
					// First attempt
					step2Attempt1Output = "Step 2 done"
					return &llm.ChatResponse{
						Message: llm.Message{
							Role:    "assistant",
							Content: "Step 2 output attempt 1",
							ToolCalls: []llm.ToolCall{
								{ID: "c2", Name: "finish", Input: json.RawMessage(`{"answer": "Step 2 done"}`)},
							},
						},
						StopReason: "tool_use",
					}, nil
				}
				// Second attempt - full retry
				step2Attempt2Output = "Step 2 retry"
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: "Step 2 output attempt 2",
						ToolCalls: []llm.ToolCall{
							{ID: "c2b", Name: "finish", Input: json.RawMessage(`{"answer": "Step 2 retry"}`)},
						},
					},
					StopReason: "tool_use",
				}, nil
			}
			if detectCallType(req) == "evaluator_judge" {
				evalCallCount++
				if evalCallCount == 1 {
					// First eval - fails
					return &llm.ChatResponse{
						Message:    llm.Message{Role: "assistant", Content: "NO - task failed"},
						StopReason: "end_turn",
					}, nil
				}
				// Second eval - passes
				return &llm.ChatResponse{
					Message:    llm.Message{Role: "assistant", Content: "YES - task passed"},
					StopReason: "end_turn",
				}, nil
			}
			if detectCallType(req) == "reflector" {
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"summary": "Task failed", "failed_criteria": ["ac_1"], "hypotheses": ["Error"], "suggested_action": "retry", "reasoning": "Try again", "failure_analysis": "Failed", "root_cause": "Bug", "action_plan": "Fix"}`,
					},
					StopReason: "end_turn",
				}, nil
			}
			return &llm.ChatResponse{
				Message:    llm.Message{Role: "assistant", Content: ""},
				StopReason: "end_turn",
			}, nil
		},
	}

	registry := createTestRegistry()
	counter := llm.NewSimpleTokenCounter()
	reflector := NewReflector(mockLLM)

	orchestrator := NewOrchestrator(
		NewRouter(mockLLM, 5),
		NewACExtractor(mockLLM),
		NewPlanner(mockLLM),
		NewEvaluator(registry, mockLLM),
		mockLLM,
		registry,
		registry,
		counter,
		OrchestratorConfig{MaxSteps: 10, MaxRetries: 3},
		testContextFactory,
		reflector,
		nil, // logger - nil for tests
		nil, // emitter - nil for tests
		nil, // modelRegistry - nil for tests
		ToolResultBudget{},
		nil, // intentVerifier - nil for tests
	)

	result, err := orchestrator.Handle(context.Background(), "Run steps without AC mapping")
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	// Should have succeeded on second attempt
	if result.AttemptCount != 2 {
		t.Errorf("expected 2 attempts, got %d", result.AttemptCount)
	}

	// Eval should pass on final result
	if result.EvalResult == nil || !result.EvalResult.AllPassed {
		t.Error("expected final eval to pass")
	}

	// Verify both steps were re-executed (both steps have attempt 2 outputs)
	// This confirms fallback to full retry since no RelevantAC mapping exists
	if step1Attempt2Output == "" {
		t.Error("expected step_1 to have been re-executed in attempt 2 (full retry)")
	}
	if step2Attempt2Output == "" {
		t.Error("expected step_2 to have been re-executed in attempt 2 (full retry)")
	}

	// Note: aggregateOutput only returns terminal step outputs (steps that no other step depends on)
	// In this test: step_1 -> step_2, so only step_2 output appears in final result
	// Verify the terminal step (step_2) has the attempt 2 output
	if !strings.Contains(result.Output, step2Attempt2Output) {
		t.Errorf("expected step_2 to be re-executed in attempt 2, got output: %s", result.Output)
	}
}

// TestComputeRetrySteps tests the computeRetrySteps function directly.
func TestComputeRetrySteps(t *testing.T) {
	tests := []struct {
		name              string
		steps             []PlanStep
		failedCriteriaIDs []string
		wantRetrySet      map[string]bool // nil means expect nil return (fallback)
	}{
		{
			name: "simple mapping - failed ac_1 maps to step_1 only",
			steps: []PlanStep{
				{ID: "step_1", Description: "Step 1", DependsOn: []string{}, RelevantAC: []string{"ac_1"}},
				{ID: "step_2", Description: "Step 2", DependsOn: []string{}, RelevantAC: []string{"ac_2"}},
			},
			failedCriteriaIDs: []string{"ac_1"},
			wantRetrySet:      map[string]bool{"step_1": true},
		},
		{
			name: "transitive dependents - step_2 depends on step_1",
			steps: []PlanStep{
				{ID: "step_1", Description: "Step 1", DependsOn: []string{}, RelevantAC: []string{"ac_1"}},
				{ID: "step_2", Description: "Step 2", DependsOn: []string{"step_1"}, RelevantAC: []string{"ac_2"}},
			},
			failedCriteriaIDs: []string{"ac_1"},
			wantRetrySet:      map[string]bool{"step_1": true}, // no transitive expansion
		},
		{
			name: "no mapping - returns nil for fallback",
			steps: []PlanStep{
				{ID: "step_1", Description: "Step 1", DependsOn: []string{}, RelevantAC: []string{}},
				{ID: "step_2", Description: "Step 2", DependsOn: []string{}, RelevantAC: []string{}},
			},
			failedCriteriaIDs: []string{"ac_1"},
			wantRetrySet:      nil,
		},
		{
			name: "all steps failing",
			steps: []PlanStep{
				{ID: "step_1", Description: "Step 1", DependsOn: []string{}, RelevantAC: []string{"ac_1"}},
				{ID: "step_2", Description: "Step 2", DependsOn: []string{}, RelevantAC: []string{"ac_2"}},
			},
			failedCriteriaIDs: []string{"ac_1", "ac_2"},
			wantRetrySet:      map[string]bool{"step_1": true, "step_2": true},
		},
		{
			name: "diamond dependency - step_3 and step_4 both depend on step_2",
			steps: []PlanStep{
				{ID: "step_1", Description: "Step 1", DependsOn: []string{}, RelevantAC: []string{"ac_1"}},
				{ID: "step_2", Description: "Step 2", DependsOn: []string{"step_1"}, RelevantAC: []string{"ac_2"}},
				{ID: "step_3", Description: "Step 3", DependsOn: []string{"step_2"}, RelevantAC: []string{"ac_3"}},
				{ID: "step_4", Description: "Step 4", DependsOn: []string{"step_2"}, RelevantAC: []string{"ac_4"}},
			},
			failedCriteriaIDs: []string{"ac_2"}, // step_2 fails
			wantRetrySet:      map[string]bool{"step_2": true}, // no transitive expansion to step_3/step_4
		},
		{
			name: "deep transitive chain",
			steps: []PlanStep{
				{ID: "step_1", Description: "Step 1", DependsOn: []string{}, RelevantAC: []string{"ac_1"}},
				{ID: "step_2", Description: "Step 2", DependsOn: []string{"step_1"}, RelevantAC: []string{"ac_2"}},
				{ID: "step_3", Description: "Step 3", DependsOn: []string{"step_2"}, RelevantAC: []string{"ac_3"}},
				{ID: "step_4", Description: "Step 4", DependsOn: []string{"step_3"}, RelevantAC: []string{"ac_4"}},
			},
			failedCriteriaIDs: []string{"ac_1"},
			wantRetrySet:      map[string]bool{"step_1": true}, // no transitive expansion
		},
		{
			name: "multiple failed criteria map to same step",
			steps: []PlanStep{
				{ID: "step_1", Description: "Step 1", DependsOn: []string{}, RelevantAC: []string{"ac_1", "ac_2"}},
				{ID: "step_2", Description: "Step 2", DependsOn: []string{}, RelevantAC: []string{"ac_3"}},
			},
			failedCriteriaIDs: []string{"ac_1", "ac_2"},
			wantRetrySet:      map[string]bool{"step_1": true},
		},
		{
			name: "empty failed criteria",
			steps: []PlanStep{
				{ID: "step_1", Description: "Step 1", DependsOn: []string{}, RelevantAC: []string{"ac_1"}},
			},
			failedCriteriaIDs: []string{},
			wantRetrySet:      nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := &Plan{Steps: tt.steps}
			got := computeRetrySteps(plan, tt.failedCriteriaIDs)

			if tt.wantRetrySet == nil {
				if got != nil {
					t.Errorf("computeRetrySteps() = %v, want nil", got)
				}
				return
			}

			if got == nil {
				t.Errorf("computeRetrySteps() = nil, want %v", tt.wantRetrySet)
				return
			}

			if len(got) != len(tt.wantRetrySet) {
				t.Errorf("computeRetrySteps() returned %d steps, want %d", len(got), len(tt.wantRetrySet))
			}

			for stepID := range tt.wantRetrySet {
				if !got[stepID] {
					t.Errorf("computeRetrySteps() missing step %s in retry set", stepID)
				}
			}

			for stepID := range got {
				if !tt.wantRetrySet[stepID] {
					t.Errorf("computeRetrySteps() unexpected step %s in retry set", stepID)
				}
			}
		})
	}
}

// TestBuildStepTask tests the buildStepTask function to verify scoping instructions and task formatting.
func TestBuildStepTask(t *testing.T) {
	tests := []struct {
		name            string
		step            PlanStep
		stepIndex       int
		plan            Plan
		allAC           []AcceptanceCriterion
		completedSteps  map[string]CompletedStep
		wantContains    []string // strings that should be in the task
		wantNotContains []string // strings that should NOT be in the task
		wantCriteriaLen int      // expected number of criteria
	}{
		{
			name:      "step 2 of 5 with dependencies",
			stepIndex: 1,
			step: PlanStep{
				ID:          "step_2",
				Description: "Gather historical sales data",
				DependsOn:   []string{"step_1"},
				RelevantAC:  []string{"ac_2"},
			},
			plan: Plan{
				Steps: []PlanStep{
					{ID: "step_1", Description: "Set up environment"},
					{ID: "step_2", Description: "Gather historical sales data"},
					{ID: "step_3", Description: "Analyze trends"},
					{ID: "step_4", Description: "Create forecast"},
					{ID: "step_5", Description: "Present results"},
				},
			},
			allAC: []AcceptanceCriterion{
				{ID: "ac_1", Description: "Environment is set up"},
				{ID: "ac_2", Description: "Historical data is gathered"},
				{ID: "ac_3", Description: "Trends are analyzed"},
			},
			completedSteps: map[string]CompletedStep{
				"step_1": {StepID: "step_1", Output: "Environment configured"},
			},
			wantContains: []string{
				"[Step 2 of 5]",
				"Your task: Gather historical sales data",
				"IMPORTANT: You are executing ONE step in a multi-step plan",
				"Complete ONLY this step's objective",
				"Do NOT produce final deliverables",
				"Plan overview:",
				"✓ Step 1: Set up environment",
				"→ Step 2: Gather historical sales data (THIS STEP)",
				"· Step 3: Analyze trends",
				"· Step 4: Create forecast",
				"· Step 5: Present results",
				"Your specific objective: Gather historical sales data",
				"Produce output that is scoped to this step only",
				"Context from previous steps:",
				"[step_1]: Environment configured",
			},
			wantNotContains: []string{
				"✓ Step 2:",
				"→ Step 1:",
			},
			wantCriteriaLen: 1, // ac_2 should be included
		},
		{
			name:      "first step with no dependencies",
			stepIndex: 0,
			step: PlanStep{
				ID:          "step_1",
				Description: "Set up environment",
				DependsOn:   []string{},
				RelevantAC:  []string{"ac_1"},
			},
			plan: Plan{
				Steps: []PlanStep{
					{ID: "step_1", Description: "Set up environment"},
					{ID: "step_2", Description: "Gather data"},
				},
			},
			allAC: []AcceptanceCriterion{
				{ID: "ac_1", Description: "Environment is set up"},
			},
			completedSteps: map[string]CompletedStep{},
			wantContains: []string{
				"[Step 1 of 2]",
				"→ Step 1: Set up environment (THIS STEP)",
				"· Step 2: Gather data",
				"Your specific objective: Set up environment",
			},
			wantNotContains: []string{
				"Context from previous steps:",
			},
			wantCriteriaLen: 1,
		},
		{
			name:      "last step with multiple relevant AC",
			stepIndex: 2,
			step: PlanStep{
				ID:          "step_3",
				Description: "Present results",
				DependsOn:   []string{"step_1", "step_2"},
				RelevantAC:  []string{"ac_3", "ac_4"},
			},
			plan: Plan{
				Steps: []PlanStep{
					{ID: "step_1", Description: "Gather data"},
					{ID: "step_2", Description: "Analyze data"},
					{ID: "step_3", Description: "Present results"},
				},
			},
			allAC: []AcceptanceCriterion{
				{ID: "ac_1", Description: "Data is gathered"},
				{ID: "ac_2", Description: "Data is analyzed"},
				{ID: "ac_3", Description: "Results are presented"},
				{ID: "ac_4", Description: "Presentation is formatted"},
			},
			completedSteps: map[string]CompletedStep{
				"step_1": {StepID: "step_1", Output: "Data gathered: 100 records"},
				"step_2": {StepID: "step_2", Output: "Analysis complete"},
			},
			wantContains: []string{
				"[Step 3 of 3]",
				"✓ Step 1: Gather data",
				"✓ Step 2: Analyze data",
				"→ Step 3: Present results (THIS STEP)",
				"[step_1]: Data gathered: 100 records",
				"[step_2]: Analysis complete",
			},
			wantCriteriaLen: 2, // ac_3 and ac_4
		},
		{
			name:      "step with unknown AC ID",
			stepIndex: 0,
			step: PlanStep{
				ID:          "step_1",
				Description: "Do something",
				DependsOn:   []string{},
				RelevantAC:  []string{"ac_unknown"},
			},
			plan: Plan{
				Steps: []PlanStep{
					{ID: "step_1", Description: "Do something"},
				},
			},
			allAC: []AcceptanceCriterion{
				{ID: "ac_1", Description: "Something is done"},
			},
			completedSteps:  map[string]CompletedStep{},
			wantContains:    []string{"[Step 1 of 1]"},
			wantCriteriaLen: 0, // unknown AC ID should result in no criteria
		},
		{
			name:      "step with no RelevantAC",
			stepIndex: 0,
			step: PlanStep{
				ID:          "step_1",
				Description: "Do something",
				DependsOn:   []string{},
				RelevantAC:  []string{},
			},
			plan: Plan{
				Steps: []PlanStep{
					{ID: "step_1", Description: "Do something"},
				},
			},
			allAC: []AcceptanceCriterion{
				{ID: "ac_1", Description: "Something is done"},
			},
			completedSteps:  map[string]CompletedStep{},
			wantContains:    []string{"[Step 1 of 1]"},
			wantCriteriaLen: 0, // no RelevantAC means no criteria
		},
		{
			name:      "step with empty allAC",
			stepIndex: 0,
			step: PlanStep{
				ID:          "step_1",
				Description: "Do something",
				DependsOn:   []string{},
				RelevantAC:  []string{"ac_1"},
			},
			plan: Plan{
				Steps: []PlanStep{
					{ID: "step_1", Description: "Do something"},
				},
			},
			allAC:           []AcceptanceCriterion{}, // empty AC list
			completedSteps:  map[string]CompletedStep{},
			wantContains:    []string{"[Step 1 of 1]"},
			wantCriteriaLen: 0, // empty allAC means no criteria can be resolved
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := &Orchestrator{} // buildStepTask doesn't need other fields
			availableTools := []tools.ToolDescriptor{}

			taskDef := o.buildStepTask(tt.step, tt.stepIndex, tt.plan, tt.allAC, tt.completedSteps, availableTools, nil, "", "")

			// Check that wanted strings are present
			for _, want := range tt.wantContains {
				if !strings.Contains(taskDef.Task, want) {
					t.Errorf("buildStepTask() task missing expected content:\nwant: %q\nin task:\n%s", want, taskDef.Task)
				}
			}

			// Check that unwanted strings are NOT present
			for _, notWant := range tt.wantNotContains {
				if strings.Contains(taskDef.Task, notWant) {
					t.Errorf("buildStepTask() task contains unexpected content:\nnot want: %q\nin task:\n%s", notWant, taskDef.Task)
				}
			}

			// Check criteria length
			if len(taskDef.Criteria) != tt.wantCriteriaLen {
				t.Errorf("buildStepTask() returned %d criteria, want %d", len(taskDef.Criteria), tt.wantCriteriaLen)
			}

			// Verify that criteria IDs match expected
			for _, ac := range taskDef.Criteria {
				found := false
				for _, wantACID := range tt.step.RelevantAC {
					if ac.ID == wantACID {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("buildStepTask() returned unexpected criterion ID: %s", ac.ID)
				}
			}
		})
	}
}

// TestBuildSystemPrompt_StepExecution tests the buildSystemPrompt function for step execution mode.
func TestBuildSystemPrompt_StepExecution(t *testing.T) {
	o := &Orchestrator{}
	criteria := []AcceptanceCriterion{
		{ID: "ac_1", Description: "Criterion 1"},
	}

	// Test with isStepExecution = true
	prompt := o.buildSystemPrompt(context.Background(), "", criteria, true)

	wantContains := []string{
		"STEP EXECUTION SCOPE:",
		"You are executing a single step in a multi-step plan",
		"Your responsibility is ONLY to complete this specific step",
		"Do NOT attempt to:",
		"Solve the entire problem",
		"Produce final deliverables",
		"Perform analysis or work that belongs to subsequent steps",
		"Focus narrowly on your step's objective",
		"Acceptance Criteria (you MUST satisfy ALL of these",
		"ac_1: Criterion 1",
	}

	for _, want := range wantContains {
		if !strings.Contains(prompt, want) {
			t.Errorf("buildSystemPrompt(isStepExecution=true) missing expected content: %q\nin prompt:\n%s", want, prompt)
		}
	}

	// Test with isStepExecution = false
	promptNoStep := o.buildSystemPrompt(context.Background(), "", criteria, false)

	notWant := "STEP EXECUTION SCOPE:"
	if strings.Contains(promptNoStep, notWant) {
		t.Errorf("buildSystemPrompt(isStepExecution=false) should NOT contain: %q\nin prompt:\n%s", notWant, promptNoStep)
	}

	// Should still have acceptance criteria
	if !strings.Contains(promptNoStep, "Acceptance Criteria") {
		t.Errorf("buildSystemPrompt(isStepExecution=false) should still contain Acceptance Criteria")
	}
}

// TestBuildCriteriaToStepsMap tests the buildCriteriaToStepsMap function directly.
func TestBuildCriteriaToStepsMap(t *testing.T) {
	tests := []struct {
		name    string
		steps   []PlanStep
		wantMap map[string][]string
	}{
		{
			name: "multiple steps map to same AC",
			steps: []PlanStep{
				{ID: "step_1", Description: "Step 1", RelevantAC: []string{"ac_1"}},
				{ID: "step_2", Description: "Step 2", RelevantAC: []string{"ac_1"}},
				{ID: "step_3", Description: "Step 3", RelevantAC: []string{"ac_2"}},
			},
			wantMap: map[string][]string{
				"ac_1": {"step_1", "step_2"},
				"ac_2": {"step_3"},
			},
		},
		{
			name: "step maps to multiple ACs",
			steps: []PlanStep{
				{ID: "step_1", Description: "Step 1", RelevantAC: []string{"ac_1", "ac_2", "ac_3"}},
				{ID: "step_2", Description: "Step 2", RelevantAC: []string{"ac_2"}},
			},
			wantMap: map[string][]string{
				"ac_1": {"step_1"},
				"ac_2": {"step_1", "step_2"},
				"ac_3": {"step_1"},
			},
		},
		{
			name: "empty RelevantAC",
			steps: []PlanStep{
				{ID: "step_1", Description: "Step 1", RelevantAC: []string{}},
				{ID: "step_2", Description: "Step 2", RelevantAC: nil},
				{ID: "step_3", Description: "Step 3", RelevantAC: []string{"ac_1"}},
			},
			wantMap: map[string][]string{
				"ac_1": {"step_3"},
			},
		},
		{
			name: "all empty RelevantAC",
			steps: []PlanStep{
				{ID: "step_1", Description: "Step 1", RelevantAC: []string{}},
				{ID: "step_2", Description: "Step 2", RelevantAC: []string{}},
			},
			wantMap: map[string][]string{},
		},
		{
			name: "complex mapping",
			steps: []PlanStep{
				{ID: "step_1", Description: "Step 1", RelevantAC: []string{"ac_1", "ac_2"}},
				{ID: "step_2", Description: "Step 2", RelevantAC: []string{"ac_2", "ac_3"}},
				{ID: "step_3", Description: "Step 3", RelevantAC: []string{"ac_3", "ac_4"}},
			},
			wantMap: map[string][]string{
				"ac_1": {"step_1"},
				"ac_2": {"step_1", "step_2"},
				"ac_3": {"step_2", "step_3"},
				"ac_4": {"step_3"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := &Plan{Steps: tt.steps}
			got := buildCriteriaToStepsMap(plan)

			if len(got) != len(tt.wantMap) {
				t.Errorf("buildCriteriaToStepsMap() returned %d entries, want %d", len(got), len(tt.wantMap))
			}

			for acID, wantSteps := range tt.wantMap {
				gotSteps, ok := got[acID]
				if !ok {
					t.Errorf("buildCriteriaToStepsMap() missing AC %s", acID)
					continue
				}
				if len(gotSteps) != len(wantSteps) {
					t.Errorf("buildCriteriaToStepsMap()[%s] = %v, want %v", acID, gotSteps, wantSteps)
					continue
				}
				for i, stepID := range wantSteps {
					if gotSteps[i] != stepID {
						t.Errorf("buildCriteriaToStepsMap()[%s][%d] = %s, want %s", acID, i, gotSteps[i], stepID)
					}
				}
			}

			for acID := range got {
				if _, ok := tt.wantMap[acID]; !ok {
					t.Errorf("buildCriteriaToStepsMap() unexpected AC %s", acID)
				}
			}
		})
	}
}

func TestOrchestrator_ResolveProfile_Default(t *testing.T) {
	// Create orchestrator with default config
	cfg := OrchestratorConfig{
		MaxSteps: 30,
	}
	orch := &Orchestrator{config: cfg}

	// Step without AgentProfile should return default profile
	step := PlanStep{
		ID:          "step_1",
		Description: "Do something",
	}

	profile := orch.resolveProfile(step)

	if profile.Role != "executor" {
		t.Errorf("expected role 'executor', got %q", profile.Role)
	}
	if profile.MaxSteps != 30 {
		t.Errorf("expected MaxSteps 30, got %d", profile.MaxSteps)
	}
}

func TestOrchestrator_ResolveProfile_Custom(t *testing.T) {
	// Create orchestrator with default config
	cfg := OrchestratorConfig{
		MaxSteps: 30,
	}
	orch := &Orchestrator{config: cfg}

	// Step with custom AgentProfile
	step := PlanStep{
		ID:          "step_1",
		Description: "Research topic",
		AgentProfile: &AgentProfile{
			Role:         "researcher",
			AllowedTools: []string{"web_search", "web_fetch"},
			MaxSteps:     15,
		},
	}

	profile := orch.resolveProfile(step)

	if profile.Role != "researcher" {
		t.Errorf("expected role 'researcher', got %q", profile.Role)
	}
	if profile.MaxSteps != 15 {
		t.Errorf("expected MaxSteps 15, got %d", profile.MaxSteps)
	}
	if len(profile.AllowedTools) != 2 {
		t.Errorf("expected 2 allowed tools, got %d", len(profile.AllowedTools))
	}
}

func TestOrchestrator_FilterToolsByProfile_AllTools(t *testing.T) {
	orch := &Orchestrator{}

	allTools := []tools.ToolDescriptor{
		{Name: "bash_exec", Description: "Execute bash"},
		{Name: "file_ops", Description: "File operations"},
		{Name: "web_search", Description: "Search the web"},
	}

	// Empty AllowedTools should return all tools
	profile := AgentProfile{Role: "executor"}
	filtered := orch.filterToolsByProfile(allTools, profile)

	if len(filtered) != 3 {
		t.Errorf("expected 3 tools, got %d", len(filtered))
	}
}

func TestOrchestrator_FilterToolsByProfile_Subset(t *testing.T) {
	orch := &Orchestrator{}

	allTools := []tools.ToolDescriptor{
		{Name: "bash_exec", Description: "Execute bash"},
		{Name: "file_ops", Description: "File operations"},
		{Name: "web_search", Description: "Search the web"},
		{Name: "web_fetch", Description: "Fetch URL"},
	}

	// AllowedTools should filter to subset
	profile := AgentProfile{
		Role:         "researcher",
		AllowedTools: []string{"web_search", "web_fetch"},
	}
	filtered := orch.filterToolsByProfile(allTools, profile)

	if len(filtered) != 2 {
		t.Errorf("expected 2 tools, got %d", len(filtered))
	}

	// Verify the correct tools are included
	foundWebSearch := false
	foundWebFetch := false
	for _, t := range filtered {
		if t.Name == "web_search" {
			foundWebSearch = true
		}
		if t.Name == "web_fetch" {
			foundWebFetch = true
		}
	}
	if !foundWebSearch || !foundWebFetch {
		t.Error("expected web_search and web_fetch to be in filtered tools")
	}
}

func TestOrchestrator_FilterToolsByProfile_NoMatch(t *testing.T) {
	orch := &Orchestrator{}

	allTools := []tools.ToolDescriptor{
		{Name: "bash_exec", Description: "Execute bash"},
		{Name: "file_ops", Description: "File operations"},
	}

	// AllowedTools with no matches should return empty slice
	profile := AgentProfile{
		Role:         "researcher",
		AllowedTools: []string{"web_search", "web_fetch"},
	}
	filtered := orch.filterToolsByProfile(allTools, profile)

	if len(filtered) != 0 {
		t.Errorf("expected 0 tools, got %d", len(filtered))
	}
}

// TestBuildSystemPrompt_WorkspaceContext verifies that WORKSPACE-CONTEXT placeholder
// is properly substituted when workspace path is present or absent in context.
func TestBuildSystemPrompt_WorkspaceContext(t *testing.T) {
	o := &Orchestrator{}

	// Without workspace path in context — placeholder should be replaced with empty string
	promptNoWS := o.buildSystemPrompt(context.Background(), "task", nil, false)
	if strings.Contains(promptNoWS, "WORKSPACE-CONTEXT") {
		t.Error("prompt should not contain raw WORKSPACE-CONTEXT placeholder when no workspace path is set")
	}
	if strings.Contains(promptNoWS, "Your session workspace is:") {
		t.Error("prompt should not contain workspace section when no workspace path is set")
	}

	// With workspace path in context — placeholder should be replaced with workspace info
	ctx := tools.WithWorkspacePath(context.Background(), "/test/workspace")
	promptWithWS := o.buildSystemPrompt(ctx, "task", nil, false)
	if strings.Contains(promptWithWS, "WORKSPACE-CONTEXT") {
		t.Error("prompt should not contain raw WORKSPACE-CONTEXT placeholder when workspace path is set")
	}
	if !strings.Contains(promptWithWS, "/test/workspace") {
		t.Error("prompt should contain the workspace path '/test/workspace'")
	}
	if !strings.Contains(promptWithWS, "Your session workspace is:") {
		t.Error("prompt should contain 'Your session workspace is:' header")
	}
}

func TestIsRecoverableAPIError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"nil error", nil, false},
		{"generic error", errors.New("something went wrong"), false},
		{"400 status code", errors.New("status code: 400 bad request"), true},
		{"missing field content", errors.New("missing field `content`"), true},
		{"failed to deserialize", errors.New("Failed to deserialize response"), true},
		{"retryable LLM error", llm.NewLLMError("test", 429, true, errors.New("rate limited")), true},
		{"non-retryable LLM error", llm.NewLLMError("test", 401, false, errors.New("unauthorized")), false},
		{"404 error", errors.New("status code: 404"), false},
		{"500 generic", errors.New("internal server error"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRecoverableAPIError(tt.err)
			if got != tt.expected {
				t.Errorf("isRecoverableAPIError() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestFinishTool_DefaultPolicy(t *testing.T) {
	ft := NewFinishTool()
	if ft.DefaultPolicy() != tools.PolicyAlwaysAllow {
		t.Errorf("expected PolicyAlwaysAllow, got %v", ft.DefaultPolicy())
	}
}

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

	ft := NewFinishTool()
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

func TestIsContextExceededError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"nil error", nil, false},
		{"generic error", errors.New("something else"), false},
		{"context length exceeded", errors.New("context length exceeded for this model"), true},
		{"maximum context length", errors.New("maximum context length is 128000"), true},
		{"context_length_exceeded", errors.New("error: context_length_exceeded"), true},
		{"too many tokens", errors.New("too many tokens in request"), true},
		{"request too large", errors.New("request too large"), true},
		{"input is too long", errors.New("input is too long"), true},
		{"prompt is too long", errors.New("prompt is too long for the model"), true},
		{"case insensitive", errors.New("CONTEXT LENGTH EXCEEDED"), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isContextExceededError(tt.err)
			if got != tt.expected {
				t.Errorf("isContextExceededError() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestBuildPlanExecutionSteps(t *testing.T) {
	plan := &Plan{
		Steps: []PlanStep{
			{ID: "step_1", Description: "Fetch data from API"},
			{ID: "step_2", Description: "Filter results"},
			{ID: "step_3", Description: "Generate report"},
		},
	}

	completedSteps := []CompletedStep{
		{StepID: "step_1", Output: "Fetched 100 records"},
		{StepID: "step_2", Output: "Filtered to 25 records"},
		{StepID: "step_3", Output: "", Error: errors.New("timeout generating report")},
	}

	steps := buildPlanExecutionSteps(completedSteps, plan)

	if len(steps) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(steps))
	}

	// Step 1: successful
	if !strings.Contains(steps[0].Thought, "step_1") {
		t.Errorf("step 0 thought should reference step_1, got: %s", steps[0].Thought)
	}
	if !strings.Contains(steps[0].Thought, "Fetch data from API") {
		t.Errorf("step 0 thought should contain description, got: %s", steps[0].Thought)
	}
	if steps[0].Observation != "Fetched 100 records" {
		t.Errorf("step 0 observation = %q, want %q", steps[0].Observation, "Fetched 100 records")
	}

	// Step 2: successful
	if steps[1].Observation != "Filtered to 25 records" {
		t.Errorf("step 1 observation = %q, want %q", steps[1].Observation, "Filtered to 25 records")
	}

	// Step 3: failed
	if !strings.Contains(steps[2].Observation, "STEP FAILED") {
		t.Errorf("step 2 observation should contain STEP FAILED, got: %s", steps[2].Observation)
	}
	if !strings.Contains(steps[2].Observation, "timeout generating report") {
		t.Errorf("step 2 observation should contain error message, got: %s", steps[2].Observation)
	}
}

func TestBuildPlanExecutionSteps_Empty(t *testing.T) {
	plan := &Plan{Steps: []PlanStep{}}
	steps := buildPlanExecutionSteps(nil, plan)
	if len(steps) != 0 {
		t.Fatalf("expected 0 steps for nil input, got %d", len(steps))
	}
}

func TestBuildPlanExecutionSteps_ActualSteps(t *testing.T) {
	plan := &Plan{
		Steps: []PlanStep{
			{ID: "step_1", Description: "Fetch data"},
			{ID: "step_2", Description: "Analyze data"},
		},
	}

	// step_1 has actual executor steps; step_2 has none (fallback to synthetic)
	completedSteps := []CompletedStep{
		{
			StepID: "step_1",
			Output: "Fetched",
			Steps: []Step{
				{Thought: "Need to call API", Observation: "Got 100 records"},
				{Thought: "Parse response", Observation: "Parsed OK"},
			},
		},
		{
			StepID: "step_2",
			Output: "Analysis complete",
		},
	}

	steps := buildPlanExecutionSteps(completedSteps, plan)

	// step_1 contributes 2 actual steps, step_2 contributes 1 synthetic step
	if len(steps) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(steps))
	}

	// Actual steps from step_1
	if steps[0].Thought != "Need to call API" {
		t.Errorf("step 0 thought = %q, want %q", steps[0].Thought, "Need to call API")
	}
	if steps[1].Thought != "Parse response" {
		t.Errorf("step 1 thought = %q, want %q", steps[1].Thought, "Parse response")
	}

	// Synthetic step from step_2 (fallback)
	if !strings.Contains(steps[2].Thought, "step_2") {
		t.Errorf("step 2 thought should reference step_2, got: %s", steps[2].Thought)
	}
	if steps[2].Observation != "Analysis complete" {
		t.Errorf("step 2 observation = %q, want %q", steps[2].Observation, "Analysis complete")
	}
}

func TestBuildCarryForward(t *testing.T) {
	tests := []struct {
		name      string
		completed []CompletedStep
		newPlan   *Plan
		wantNil   bool
		wantIDs   []string // expected carried-forward step IDs
	}{
		{
			name: "carries forward matching steps",
			completed: []CompletedStep{
				{StepID: "step_1", Output: "done1"},
				{StepID: "step_2", Output: "done2"},
				{StepID: "step_3", Output: "done3"},
			},
			newPlan: &Plan{Steps: []PlanStep{
				{ID: "step_1"},
				{ID: "step_3"},
				{ID: "step_4"}, // new step, not in completed
			}},
			wantIDs: []string{"step_1", "step_3"},
		},
		{
			name: "skips errored steps",
			completed: []CompletedStep{
				{StepID: "step_1", Output: "done1"},
				{StepID: "step_2", Output: "failed", Error: errors.New("step error")},
			},
			newPlan: &Plan{Steps: []PlanStep{
				{ID: "step_1"},
				{ID: "step_2"},
			}},
			wantIDs: []string{"step_1"}, // step_2 had error, not carried
		},
		{
			name: "returns nil when no overlap",
			completed: []CompletedStep{
				{StepID: "step_1", Output: "done1"},
			},
			newPlan: &Plan{Steps: []PlanStep{
				{ID: "step_x"},
				{ID: "step_y"},
			}},
			wantNil: true,
		},
		{
			name:      "returns nil for empty completed",
			completed: []CompletedStep{},
			newPlan: &Plan{Steps: []PlanStep{
				{ID: "step_1"},
			}},
			wantNil: true,
		},
		{
			name: "dependent_of_non_carried_step_excluded",
			completed: []CompletedStep{
				{StepID: "step_1", Output: "out1"},
				{StepID: "step_2", Output: "out2"},
				{StepID: "step_3", Output: "out3"},
			},
			newPlan: &Plan{
				Steps: []PlanStep{
					{ID: "step_1_v2", Description: "replanned step 1"},
					{ID: "step_2", Description: "depends on replanned", DependsOn: []string{"step_1_v2"}},
					{ID: "step_3", Description: "depends on step_2", DependsOn: []string{"step_2"}},
				},
			},
			wantNil: true, // step_1_v2 is new (not carried), step_2 depends on it → excluded, step_3 depends on step_2 → excluded
		},
		{
			name: "partial_dag_carry_forward",
			completed: []CompletedStep{
				{StepID: "step_1", Output: "out1"},
				{StepID: "step_2", Output: "out2"},
				{StepID: "step_3", Output: "out3"},
			},
			newPlan: &Plan{
				Steps: []PlanStep{
					{ID: "step_1", Description: "unchanged"},
					{ID: "step_2_v2", Description: "replanned step 2", DependsOn: []string{"step_1"}},
					{ID: "step_3", Description: "depends on replanned", DependsOn: []string{"step_2_v2"}},
				},
			},
			wantIDs: []string{"step_1"}, // step_1 carried; step_2_v2 is new; step_3 depends on step_2_v2 → excluded
		},
		{
			name: "independent_branches_partially_carried",
			completed: []CompletedStep{
				{StepID: "step_1", Output: "out1"},
				{StepID: "step_2", Output: "out2"},
				{StepID: "step_3", Output: "out3"},
			},
			newPlan: &Plan{
				Steps: []PlanStep{
					{ID: "step_1", Description: "unchanged branch A"},
					{ID: "step_2", Description: "unchanged branch A", DependsOn: []string{"step_1"}},
					{ID: "step_3_v2", Description: "replanned branch B"},
				},
			},
			wantIDs: []string{"step_1", "step_2"}, // branch A fully carried; step_3_v2 is new
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildCarryForward(tt.completed, tt.newPlan)

			if tt.wantNil {
				if got != nil {
					t.Errorf("buildCarryForward() = %v, want nil", got)
				}
				return
			}

			if got == nil {
				t.Fatalf("buildCarryForward() = nil, want map with %v", tt.wantIDs)
			}

			if len(got) != len(tt.wantIDs) {
				t.Errorf("buildCarryForward() returned %d steps, want %d", len(got), len(tt.wantIDs))
			}

			for _, id := range tt.wantIDs {
				if _, ok := got[id]; !ok {
					t.Errorf("buildCarryForward() missing step %s", id)
				}
			}
		})
	}
}

func TestValidateACMapping(t *testing.T) {
	plan := &Plan{
		Steps: []PlanStep{
			{ID: "step_1", RelevantAC: []string{"ac_1", "ac_2"}},
			{ID: "step_2", RelevantAC: []string{"ac_3"}},
		},
	}

	ac := []AcceptanceCriterion{
		{ID: "ac_1", Description: "criterion 1"},
		{ID: "ac_2", Description: "criterion 2"},
		{ID: "ac_3", Description: "criterion 3"},
		{ID: "ac_4", Description: "criterion 4"},
		{ID: "ac_5", Description: "criterion 5"},
	}

	unmapped := validateACMapping(plan, ac)

	if len(unmapped) != 2 {
		t.Fatalf("expected 2 unmapped, got %d: %v", len(unmapped), unmapped)
	}

	// Check that ac_4 and ac_5 are in the unmapped list
	unmappedSet := make(map[string]bool)
	for _, id := range unmapped {
		unmappedSet[id] = true
	}
	if !unmappedSet["ac_4"] || !unmappedSet["ac_5"] {
		t.Errorf("expected ac_4 and ac_5 to be unmapped, got: %v", unmapped)
	}
}

func TestValidateACMapping_AllMapped(t *testing.T) {
	plan := &Plan{
		Steps: []PlanStep{
			{ID: "step_1", RelevantAC: []string{"ac_1"}},
		},
	}
	ac := []AcceptanceCriterion{
		{ID: "ac_1", Description: "criterion 1"},
	}

	unmapped := validateACMapping(plan, ac)
	if len(unmapped) != 0 {
		t.Fatalf("expected no unmapped, got: %v", unmapped)
	}
}

