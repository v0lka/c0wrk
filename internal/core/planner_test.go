package core

import (
	"context"
	"strings"
	"testing"

	"github.com/user/agent/internal/llm"
	"github.com/user/agent/internal/tools"
)

// Tests use shared mock types from testhelpers_test.go:
// - mockLLMCaller: implements LLMCaller (use callFn for custom behavior)

func TestPlan_CreatesValidDAG(t *testing.T) {
	// Mock returns 3-step plan with dependencies: step_2 depends on step_1, step_3 depends on step_1
	mockResponse := `{
		"steps": [
			{"id": "step_1", "description": "Initialize project", "depends_on": [], "parallelizable": false, "estimated_tools": ["bash"], "relevant_ac": ["ac_1"]},
			{"id": "step_2", "description": "Create main module", "depends_on": ["step_1"], "parallelizable": true, "estimated_tools": ["file_write"], "relevant_ac": ["ac_2"]},
			{"id": "step_3", "description": "Create tests", "depends_on": ["step_1"], "parallelizable": true, "estimated_tools": ["file_write"], "relevant_ac": ["ac_3"]}
		]
	}`

	mock := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:    "assistant",
					Content: mockResponse,
				},
				StopReason: "end_turn",
			}, nil
		},
	}

	planner := NewPlanner(mock)

	criteria := []AcceptanceCriterion{
		{ID: "ac_1", Description: "Project initialized"},
		{ID: "ac_2", Description: "Main module created"},
		{ID: "ac_3", Description: "Tests created"},
	}

	availableTools := []tools.ToolDescriptor{
		{Name: "bash", Description: "Execute shell commands"},
		{Name: "file_write", Description: "Write content to a file"},
	}

	plan, err := planner.Plan(context.Background(), "Create a new Go project", criteria, availableTools, nil)
	if err != nil {
		t.Fatalf("Plan() returned error: %v", err)
	}

	// Verify structure
	if len(plan.Steps) != 3 {
		t.Errorf("Expected 3 steps, got %d", len(plan.Steps))
	}

	// Verify step_1
	if plan.Steps[0].ID != "step_1" {
		t.Errorf("Expected first step ID 'step_1', got '%s'", plan.Steps[0].ID)
	}
	if len(plan.Steps[0].DependsOn) != 0 {
		t.Errorf("Expected step_1 to have no dependencies, got %v", plan.Steps[0].DependsOn)
	}

	// Verify step_2 depends on step_1
	if plan.Steps[1].ID != "step_2" {
		t.Errorf("Expected second step ID 'step_2', got '%s'", plan.Steps[1].ID)
	}
	if len(plan.Steps[1].DependsOn) != 1 || plan.Steps[1].DependsOn[0] != "step_1" {
		t.Errorf("Expected step_2 to depend on step_1, got %v", plan.Steps[1].DependsOn)
	}

	// Verify step_3 depends on step_1
	if plan.Steps[2].ID != "step_3" {
		t.Errorf("Expected third step ID 'step_3', got '%s'", plan.Steps[2].ID)
	}
	if len(plan.Steps[2].DependsOn) != 1 || plan.Steps[2].DependsOn[0] != "step_1" {
		t.Errorf("Expected step_3 to depend on step_1, got %v", plan.Steps[2].DependsOn)
	}

	// Verify parallelizable flags (step_2 and step_3 can run in parallel)
	if !plan.Steps[1].Parallelizable || !plan.Steps[2].Parallelizable {
		t.Error("Expected step_2 and step_3 to be parallelizable")
	}
}

func TestPlan_IncludesToolsAndCriteria(t *testing.T) {
	var capturedRequest llm.ChatRequest

	mock := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			capturedRequest = req
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:    "assistant",
					Content: `{"steps": [{"id": "step_1", "description": "Do something", "depends_on": [], "parallelizable": true, "estimated_tools": ["bash"], "relevant_ac": ["ac_1"]}]}`,
				},
				StopReason: "end_turn",
			}, nil
		},
	}

	planner := NewPlanner(mock)

	criteria := []AcceptanceCriterion{
		{ID: "ac_1", Description: "Tests must pass"},
		{ID: "ac_2", Description: "Code must compile"},
	}

	availableTools := []tools.ToolDescriptor{
		{Name: "bash", Description: "Execute shell commands"},
		{Name: "file_read", Description: "Read file contents"},
	}

	_, err := planner.Plan(context.Background(), "Build project", criteria, availableTools, nil)
	if err != nil {
		t.Fatalf("Plan() returned error: %v", err)
	}

	// Verify system prompt contains tool names
	systemPrompt := capturedRequest.Messages[0].Content
	if !strings.Contains(systemPrompt, "bash") {
		t.Error("System prompt should contain tool name 'bash'")
	}
	if !strings.Contains(systemPrompt, "file_read") {
		t.Error("System prompt should contain tool name 'file_read'")
	}
	if !strings.Contains(systemPrompt, "Execute shell commands") {
		t.Error("System prompt should contain tool description")
	}

	// Verify system prompt contains AC descriptions
	if !strings.Contains(systemPrompt, "ac_1") {
		t.Error("System prompt should contain AC ID 'ac_1'")
	}
	if !strings.Contains(systemPrompt, "Tests must pass") {
		t.Error("System prompt should contain AC description 'Tests must pass'")
	}
	if !strings.Contains(systemPrompt, "Code must compile") {
		t.Error("System prompt should contain AC description 'Code must compile'")
	}
}

func TestReplan_ReturnsUpdatedPlan(t *testing.T) {
	// Mock returns modified plan with only remaining steps
	mockResponse := `{
		"steps": [
			{"id": "step_2_retry", "description": "Retry creating main module with fix", "depends_on": [], "parallelizable": false, "estimated_tools": ["file_write"], "relevant_ac": ["ac_2"]},
			{"id": "step_3", "description": "Create tests", "depends_on": ["step_2_retry"], "parallelizable": false, "estimated_tools": ["file_write"], "relevant_ac": ["ac_3"]}
		]
	}`

	mock := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:    "assistant",
					Content: mockResponse,
				},
				StopReason: "end_turn",
			}, nil
		},
	}

	planner := NewPlanner(mock)

	originalPlan := &Plan{
		Steps: []PlanStep{
			{ID: "step_1", Description: "Initialize project", DependsOn: []string{}},
			{ID: "step_2", Description: "Create main module", DependsOn: []string{"step_1"}},
			{ID: "step_3", Description: "Create tests", DependsOn: []string{"step_2"}},
		},
	}

	completedSteps := []CompletedStep{
		{StepID: "step_1", Output: "Project initialized successfully"},
	}

	failedStep := CompletedStep{
		StepID: "step_2",
		Output: "Failed to create module",
		Error:  nil,
	}

	reflection := &Reflection{
		FailureAnalysis: "Module creation failed due to missing import",
		RootCause:       "Import path was incorrect",
		ActionPlan:      "Fix the import path and retry",
	}

	criteria := []AcceptanceCriterion{
		{ID: "ac_2", Description: "Main module created"},
		{ID: "ac_3", Description: "Tests created"},
	}

	plan, err := planner.Replan(context.Background(), originalPlan, completedSteps, failedStep, reflection, criteria, nil)
	if err != nil {
		t.Fatalf("Replan() returned error: %v", err)
	}

	// Verify updated plan structure
	if len(plan.Steps) != 2 {
		t.Errorf("Expected 2 steps in updated plan, got %d", len(plan.Steps))
	}

	// Verify first step is the retry
	if plan.Steps[0].ID != "step_2_retry" {
		t.Errorf("Expected first step ID 'step_2_retry', got '%s'", plan.Steps[0].ID)
	}

	// Verify second step depends on the retry
	if len(plan.Steps[1].DependsOn) != 1 || plan.Steps[1].DependsOn[0] != "step_2_retry" {
		t.Errorf("Expected step_3 to depend on step_2_retry, got %v", plan.Steps[1].DependsOn)
	}
}

func TestPlan_WithReflections(t *testing.T) {
	var capturedRequest llm.ChatRequest

	mock := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			capturedRequest = req
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:    "assistant",
					Content: `{"steps": [{"id": "step_1", "description": "Do something", "depends_on": [], "parallelizable": true, "estimated_tools": ["bash"], "relevant_ac": ["ac_1"]}]}`,
				},
				StopReason: "end_turn",
			}, nil
		},
	}

	planner := NewPlanner(mock)

	reflections := []Reflection{
		{
			FailureAnalysis: "Previous attempt failed due to timeout",
			RootCause:       "Network latency was too high",
			ActionPlan:      "Increase timeout value",
		},
		{
			FailureAnalysis: "Build failed",
			RootCause:       "Missing dependency",
			ActionPlan:      "Add missing dependency first",
		},
	}

	_, err := planner.Plan(context.Background(), "Deploy application", nil, nil, reflections)
	if err != nil {
		t.Fatalf("Plan() returned error: %v", err)
	}

	// Verify reflections are included in the prompt
	systemPrompt := capturedRequest.Messages[0].Content
	if !strings.Contains(systemPrompt, "Previous attempt failed due to timeout") {
		t.Error("System prompt should contain first reflection's failure analysis")
	}
	if !strings.Contains(systemPrompt, "Network latency was too high") {
		t.Error("System prompt should contain first reflection's root cause")
	}
	if !strings.Contains(systemPrompt, "Increase timeout value") {
		t.Error("System prompt should contain first reflection's action plan")
	}
	if !strings.Contains(systemPrompt, "Build failed") {
		t.Error("System prompt should contain second reflection's failure analysis")
	}
	if !strings.Contains(systemPrompt, "Missing dependency") {
		t.Error("System prompt should contain second reflection's root cause")
	}
	if !strings.Contains(systemPrompt, "Reflections from past attempts") {
		t.Error("System prompt should contain reflections header")
	}
}

func TestPlan_ParsesMarkdownCodeBlock(t *testing.T) {
	mockResponse := "```json\n{\"steps\": [{\"id\": \"step_1\", \"description\": \"Test\", \"depends_on\": [], \"parallelizable\": true, \"estimated_tools\": [], \"relevant_ac\": []}]}\n```"

	mock := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:    "assistant",
					Content: mockResponse,
				},
				StopReason: "end_turn",
			}, nil
		},
	}

	planner := NewPlanner(mock)

	plan, err := planner.Plan(context.Background(), "Test task", nil, nil, nil)
	if err != nil {
		t.Fatalf("Plan() should parse markdown code block, got error: %v", err)
	}

	if len(plan.Steps) != 1 {
		t.Errorf("Expected 1 step, got %d", len(plan.Steps))
	}
	if plan.Steps[0].ID != "step_1" {
		t.Errorf("Expected step ID 'step_1', got '%s'", plan.Steps[0].ID)
	}
}

func TestReplan_IncludesOriginalPlanAndFailureDetails(t *testing.T) {
	var capturedRequest llm.ChatRequest

	mock := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			capturedRequest = req
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:    "assistant",
					Content: `{"steps": []}`,
				},
				StopReason: "end_turn",
			}, nil
		},
	}

	planner := NewPlanner(mock)

	originalPlan := &Plan{
		Steps: []PlanStep{
			{ID: "step_1", Description: "First step"},
		},
	}

	completedSteps := []CompletedStep{
		{StepID: "step_1", Output: "Step 1 completed"},
	}

	failedStep := CompletedStep{
		StepID: "step_2",
		Output: "Failure output",
	}

	reflection := &Reflection{
		FailureAnalysis: "Test failure analysis",
		RootCause:       "Test root cause",
		ActionPlan:      "Test action plan",
	}

	_, _ = planner.Replan(context.Background(), originalPlan, completedSteps, failedStep, reflection, nil, nil)

	systemPrompt := capturedRequest.Messages[0].Content

	// Verify original plan is included
	if !strings.Contains(systemPrompt, "step_1") {
		t.Error("System prompt should contain original plan step ID")
	}
	if !strings.Contains(systemPrompt, "First step") {
		t.Error("System prompt should contain original plan step description")
	}

	// Verify completed steps are included
	if !strings.Contains(systemPrompt, "Step 1 completed") {
		t.Error("System prompt should contain completed step output")
	}

	// Verify failed step details are included
	if !strings.Contains(systemPrompt, "step_2") {
		t.Error("System prompt should contain failed step ID")
	}
	if !strings.Contains(systemPrompt, "Failure output") {
		t.Error("System prompt should contain failed step output")
	}

	// Verify reflection is included
	if !strings.Contains(systemPrompt, "Test failure analysis") {
		t.Error("System prompt should contain reflection failure analysis")
	}
	if !strings.Contains(systemPrompt, "Test root cause") {
		t.Error("System prompt should contain reflection root cause")
	}
	if !strings.Contains(systemPrompt, "Test action plan") {
		t.Error("System prompt should contain reflection action plan")
	}
}

func TestParsePlanResponse_WithAgentProfile(t *testing.T) {
	mockResponse := `{
		"steps": [
			{
				"id": "step_1",
				"description": "Research the codebase",
				"depends_on": [],
				"parallelizable": true,
				"estimated_tools": ["web_search", "file_read"],
				"relevant_ac": ["ac_1"],
				"agent_profile": {
					"role": "researcher",
					"allowed_tools": ["web_search", "web_fetch", "bash_exec"]
				}
			}
		]
	}`

	mock := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:    "assistant",
					Content: mockResponse,
				},
				StopReason: "end_turn",
			}, nil
		},
	}

	planner := NewPlanner(mock)

	plan, err := planner.Plan(context.Background(), "Research task", nil, nil, nil)
	if err != nil {
		t.Fatalf("Plan() returned error: %v", err)
	}

	if len(plan.Steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(plan.Steps))
	}

	step := plan.Steps[0]
	if step.AgentProfile == nil {
		t.Fatal("expected AgentProfile to be non-nil")
	}

	if step.AgentProfile.Role != "researcher" {
		t.Errorf("expected role 'researcher', got %q", step.AgentProfile.Role)
	}

	if len(step.AgentProfile.AllowedTools) != 3 {
		t.Errorf("expected 3 allowed tools, got %d", len(step.AgentProfile.AllowedTools))
	}
}

func TestParsePlanResponse_WithoutAgentProfile(t *testing.T) {
	mockResponse := `{
		"steps": [
			{
				"id": "step_1",
				"description": "Do something",
				"depends_on": [],
				"parallelizable": true,
				"estimated_tools": ["bash"],
				"relevant_ac": ["ac_1"]
			}
		]
	}`

	mock := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:    "assistant",
					Content: mockResponse,
				},
				StopReason: "end_turn",
			}, nil
		},
	}

	planner := NewPlanner(mock)

	plan, err := planner.Plan(context.Background(), "Simple task", nil, nil, nil)
	if err != nil {
		t.Fatalf("Plan() returned error: %v", err)
	}

	if len(plan.Steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(plan.Steps))
	}

	step := plan.Steps[0]
	if step.AgentProfile != nil {
		t.Error("expected AgentProfile to be nil when not provided in JSON")
	}
}

// TestPlan_WorkspacePathSubstitution verifies that the WORKSPACE-PATH placeholder
// is properly substituted when workspace path is present or absent in context.
func TestPlan_WorkspacePathSubstitution(t *testing.T) {
	var capturedRequest llm.ChatRequest

	mock := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			capturedRequest = req
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:    "assistant",
					Content: `{"steps": [{"id": "step_1", "description": "Do something", "depends_on": [], "parallelizable": true, "estimated_tools": ["bash"], "relevant_ac": ["ac_1"]}]}`,
				},
				StopReason: "end_turn",
			}, nil
		},
	}

	planner := NewPlanner(mock)

	// With workspace path
	ctx := tools.WithWorkspacePath(context.Background(), "/my/project")
	_, err := planner.Plan(ctx, "Build project", nil, nil, nil)
	if err != nil {
		t.Fatalf("Plan() returned error: %v", err)
	}

	systemPrompt := capturedRequest.Messages[0].Content
	if strings.Contains(systemPrompt, "WORKSPACE-PATH") {
		t.Error("system prompt should not contain raw WORKSPACE-PATH placeholder")
	}
	if !strings.Contains(systemPrompt, "/my/project") {
		t.Error("system prompt should contain the workspace path '/my/project'")
	}
	if !strings.Contains(systemPrompt, "Session workspace:") {
		t.Error("system prompt should contain 'Session workspace:' header")
	}

	// Without workspace path
	_, err = planner.Plan(context.Background(), "Build project", nil, nil, nil)
	if err != nil {
		t.Fatalf("Plan() returned error: %v", err)
	}

	systemPromptNoWS := capturedRequest.Messages[0].Content
	if strings.Contains(systemPromptNoWS, "WORKSPACE-PATH") {
		t.Error("system prompt should not contain raw WORKSPACE-PATH placeholder when no workspace path")
	}
	if strings.Contains(systemPromptNoWS, "Session workspace:") {
		t.Error("system prompt should not contain 'Session workspace:' when no workspace path is set")
	}
}

// TestReplan_WorkspacePathSubstitution verifies that the WORKSPACE-PATH placeholder
// is properly substituted in replan system prompts.
func TestReplan_WorkspacePathSubstitution(t *testing.T) {
	var capturedRequest llm.ChatRequest

	mock := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			capturedRequest = req
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:    "assistant",
					Content: `{"steps": []}`,
				},
				StopReason: "end_turn",
			}, nil
		},
	}

	planner := NewPlanner(mock)
	originalPlan := &Plan{Steps: []PlanStep{{ID: "step_1", Description: "First step"}}}
	failedStep := CompletedStep{StepID: "step_1", Output: "Failed"}

	ctx := tools.WithWorkspacePath(context.Background(), "/replan/workspace")
	_, _ = planner.Replan(ctx, originalPlan, nil, failedStep, nil, nil, nil)

	systemPrompt := capturedRequest.Messages[0].Content
	if strings.Contains(systemPrompt, "WORKSPACE-PATH") {
		t.Error("replan system prompt should not contain raw WORKSPACE-PATH placeholder")
	}
	if !strings.Contains(systemPrompt, "/replan/workspace") {
		t.Error("replan system prompt should contain the workspace path '/replan/workspace'")
	}
}

func TestParsePlanResponse_EdgeCases(t *testing.T) {
	planner := &Planner{}

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{
			name:  "valid JSON with steps",
			input: `{"steps":[{"id":"step_1","description":"do thing"}]}`,
		},
		{
			name:  "JSON in markdown code block",
			input: "```json\n{\"steps\":[{\"id\":\"step_1\",\"description\":\"do thing\"}]}\n```",
		},
		{
			name:  "JSON in plain code block",
			input: "```\n{\"steps\":[{\"id\":\"step_1\",\"description\":\"do thing\"}]}\n```",
		},
		{
			name:    "no JSON in response",
			input:   "here is some text with no json",
			wantErr: true,
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
		{
			name:    "invalid JSON",
			input:   `{invalid json}`,
			wantErr: true,
		},
		{
			name:  "JSON with surrounding text",
			input: `Here is the plan: {"steps":[{"id":"step_1","description":"test"}]} end`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := planner.parsePlanResponse(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if plan == nil {
				t.Fatal("expected non-nil plan")
			}
		})
	}
}

func TestReplanWithSessionReflections(t *testing.T) {
	// Capture what prompt the LLM receives
	var capturedMessages []llm.Message
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			capturedMessages = req.Messages
			return &llm.ChatResponse{
				Message: llm.Message{
					Content: `{"steps": [{"id": "step_1", "description": "Retry with fix", "depends_on": [], "parallelizable": false, "estimated_tools": ["bash_exec"], "relevant_ac": ["ac_1"]}]}`,
				},
			}, nil
		},
	}

	planner := NewPlanner(mockLLM)

	originalPlan := &Plan{
		Steps: []PlanStep{
			{ID: "step_1", Description: "Do something"},
		},
	}
	completedSteps := []CompletedStep{
		{StepID: "step_1", Output: "partial result"},
	}
	failedStep := CompletedStep{StepID: "step_1", Output: "partial result"}
	reflection := &Reflection{
		Summary:   "Step failed due to timeout",
		RootCause: "API rate limiting",
	}
	criteria := []AcceptanceCriterion{
		{ID: "ac_1", Description: "Must complete"},
	}
	sessionReflections := []Reflection{
		{
			Summary:         "First attempt failed",
			RootCause:       "Wrong API endpoint",
			ActionPlan:      "Use correct endpoint",
			SuggestedAction: "retry",
		},
		{
			Summary:         "Second attempt also failed",
			RootCause:       "API rate limited",
			ActionPlan:      "Add retry logic",
			SuggestedAction: "replan",
		},
	}

	_, err := planner.Replan(context.Background(), originalPlan, completedSteps, failedStep, reflection, criteria, sessionReflections)
	if err != nil {
		t.Fatalf("Replan failed: %v", err)
	}

	// Verify the system prompt contains session reflections
	if len(capturedMessages) == 0 {
		t.Fatal("no messages captured")
	}
	systemPrompt := capturedMessages[0].Content
	if !strings.Contains(systemPrompt, "Previous session reflections") {
		t.Error("system prompt should contain 'Previous session reflections'")
	}
	if !strings.Contains(systemPrompt, "Wrong API endpoint") {
		t.Error("system prompt should contain first reflection's root cause")
	}
	if !strings.Contains(systemPrompt, "API rate limited") {
		t.Error("system prompt should contain second reflection's root cause")
	}
}
