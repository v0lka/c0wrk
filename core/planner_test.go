package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/core/skills"
	coretools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/c0wrk/sdk/agent"
	"github.com/v0lka/c0wrk/sdk/llm"
	"github.com/v0lka/c0wrk/sdk/orchestration"
	tools "github.com/v0lka/c0wrk/sdk/tools"
)

// Tests use shared mock types from testhelpers_test.go:
// - mockLLMCaller: implements LLMCaller (use callFn for custom behavior)

func TestPlan_CreatesValidDAG(t *testing.T) {
	// Mock returns 3-step plan with dependencies: step_2 depends on step_1, step_3 depends on step_1
	mockResponse := `{
		"steps": [
			{"id": "step_1", "summary": "Initialize project", "description": "Initialize project", "depends_on": [], "parallelizable": false, "estimated_tools": ["bash"]},
			{"id": "step_2", "summary": "Create main module", "description": "Create main module", "depends_on": ["step_1"], "parallelizable": true, "estimated_tools": ["file_write"]},
			{"id": "step_3", "summary": "Create tests", "description": "Create tests", "depends_on": ["step_1"], "parallelizable": true, "estimated_tools": ["file_write"]}
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

	availableTools := []tools.ToolDescriptor{
		{Name: "bash", Description: "Execute shell commands"},
		{Name: "file_write", Description: "Write content to a file"},
	}

	plan, err := planner.Plan(context.Background(), "Create a new Go project", availableTools, nil, nil, false)
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

func TestPlan_IncludesToolsInPrompt(t *testing.T) {
	var capturedRequest llm.ChatRequest

	mock := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			capturedRequest = req
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:    "assistant",
					Content: `{"steps": [{"id": "step_1", "description": "Do something", "depends_on": [], "parallelizable": true, "estimated_tools": ["bash"]}]}`,
				},
				StopReason: "end_turn",
			}, nil
		},
	}

	planner := NewPlanner(mock)

	availableTools := []tools.ToolDescriptor{
		{Name: "bash", Description: "Execute shell commands"},
		{Name: "file_read", Description: "Read file contents"},
	}

	_, err := planner.Plan(context.Background(), "Build project", availableTools, nil, nil, false)
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
}

func TestReplan_ReturnsUpdatedPlan(t *testing.T) {
	// Mock returns modified plan with only remaining steps
	mockResponse := `{
		"steps": [
			{"id": "step_2_retry", "description": "Retry creating main module with fix", "depends_on": [], "parallelizable": false, "estimated_tools": ["file_write"]},
			{"id": "step_3", "description": "Create tests", "depends_on": ["step_2_retry"], "parallelizable": false, "estimated_tools": ["file_write"]}
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

	plan, err := planner.Replan(context.Background(), originalPlan, completedSteps, failedStep, reflection, nil, nil)
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
					Content: `{"steps": [{"id": "step_1", "description": "Do something", "depends_on": [], "parallelizable": true, "estimated_tools": ["bash"]}]}`,
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

	_, err := planner.Plan(context.Background(), "Deploy application", nil, reflections, nil, false)
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
	mockResponse := "```json\n{\"steps\": [{\"id\": \"step_1\", \"description\": \"Test\", \"depends_on\": [], \"parallelizable\": true, \"estimated_tools\": []}]}\n```"

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

	plan, err := planner.Plan(context.Background(), "Test task", nil, nil, nil, false)
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
				"profile": {
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

	plan, err := planner.Plan(context.Background(), "Research task", nil, nil, nil, false)
	if err != nil {
		t.Fatalf("Plan() returned error: %v", err)
	}

	if len(plan.Steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(plan.Steps))
	}

	step := plan.Steps[0]
	if step.Profile == nil {
		t.Fatal("expected Profile to be non-nil")
	}

	// parsePlanResponse now normalizes JSON-decoded profiles to *AgentProfile so
	// resolveAgentProfile can type-assert successfully during step configuration.
	profile, ok := step.Profile.(*AgentProfile)
	if !ok {
		t.Fatalf("expected Profile to be *AgentProfile, got %T", step.Profile)
	}

	if profile.Role != "researcher" {
		t.Errorf("expected role 'researcher', got %q", profile.Role)
	}

	if len(profile.AllowedTools) != 3 {
		t.Errorf("expected 3 allowed tools, got %d", len(profile.AllowedTools))
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
				"estimated_tools": ["bash"]
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

	plan, err := planner.Plan(context.Background(), "Simple task", nil, nil, nil, false)
	if err != nil {
		t.Fatalf("Plan() returned error: %v", err)
	}

	if len(plan.Steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(plan.Steps))
	}

	step := plan.Steps[0]
	if step.Profile != nil {
		t.Error("expected Profile to be nil when not provided in JSON")
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
					Content: `{"steps": [{"id": "step_1", "description": "Do something", "depends_on": [], "parallelizable": true, "estimated_tools": ["bash"]}]}`,
				},
				StopReason: "end_turn",
			}, nil
		},
	}

	planner := NewPlanner(mock)

	// With workspace path
	ctx := tools.WithWorkspacePath(context.Background(), "/my/project")
	_, err := planner.Plan(ctx, "Build project", nil, nil, nil, false)
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
	_, err = planner.Plan(context.Background(), "Build project", nil, nil, nil, false)
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

func TestParsePlanResponse_ExtractsSummary(t *testing.T) {
	p := &Planner{}
	input := `{"steps": [{"id": "step_1", "summary": "Setup auth module", "description": "What: Create authentication module\nHow: Use JWT tokens\nWhere: auth/\nAcceptance Criteria:\n- Module compiles", "depends_on": [], "parallelizable": false}]}`
	plan, err := p.parsePlanResponse(input, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(plan.Steps))
	}
	if plan.Steps[0].Summary != "Setup auth module" {
		t.Errorf("expected summary 'Setup auth module', got %q", plan.Steps[0].Summary)
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
			plan, err := planner.parsePlanResponse(tt.input, nil)
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
					Content: `{"steps": [{"id": "step_1", "description": "Retry with fix", "depends_on": [], "parallelizable": false, "estimated_tools": ["bash_exec"]}]}`,
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

	_, err := planner.Replan(context.Background(), originalPlan, completedSteps, failedStep, reflection, sessionReflections, nil)
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

// TestBuildPlanSystemPrompt_WithEnvInfo verifies that buildPlanSystemPrompt includes
// the full environment block when EnvInfo is present in context.
func TestBuildPlanSystemPrompt_WithEnvInfo(t *testing.T) {
	var capturedRequest llm.ChatRequest

	mock := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			capturedRequest = req
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:    "assistant",
					Content: `{"steps": [{"id": "step_1", "description": "Do something", "depends_on": [], "parallelizable": true, "estimated_tools": ["bash"]}]}`,
				},
				StopReason: "end_turn",
			}, nil
		},
	}

	planner := NewPlanner(mock)

	info := &tools.EnvInfo{
		OS:   "macOS 15.4 (Darwin 24.4.0)",
		Arch: "arm64",
	}
	ctx := tools.WithEnvInfo(context.Background(), info)

	_, err := planner.Plan(ctx, "Build project", nil, nil, nil, false)
	if err != nil {
		t.Fatalf("Plan() returned error: %v", err)
	}

	systemPrompt := capturedRequest.Messages[0].Content
	if !strings.Contains(systemPrompt, "## Environment") {
		t.Error("system prompt should contain environment block")
	}
	if !strings.Contains(systemPrompt, "macOS 15.4") {
		t.Error("system prompt should contain OS info")
	}
	if !strings.Contains(systemPrompt, "arm64") {
		t.Error("system prompt should contain architecture info")
	}
}

// TestPlanContinuation verifies that PlanContinuation generates a valid plan
// for follow-up requests after task completion.
// === Informed Planner Tests ===

// plannerMockTool is a simple tool implementation for planner tests.
type plannerMockTool struct {
	tools.BaseTool
	result tools.ToolResult
}

func (m *plannerMockTool) Execute(_ context.Context, _ json.RawMessage) (tools.ToolResult, error) {
	return m.result, nil
}

// newPlannerTestRegistry creates a core ToolRegistry with the given tool definitions.
// Each entry specifies a tool name and optional source (empty = default "core").
func newPlannerTestRegistry(defs []struct{ name, source string }) *coretools.ToolRegistry {
	reg := coretools.NewToolRegistry()
	for _, d := range defs {
		tool := &plannerMockTool{
			BaseTool: tools.BaseTool{
				ToolName:        d.name,
				ToolDescription: d.name + " tool",
				Schema:          json.RawMessage(`{"type":"object"}`),
			},
			result: tools.ToolResult{Content: "mock result from " + d.name},
		}
		if d.source != "" {
			reg.RegisterWithSource(tool, d.source)
		} else {
			reg.Register(tool)
		}
	}
	return reg
}

// plannerContextFactory returns a ContextManagerFactory that creates mockContextManagers.
func plannerContextFactory() ContextManagerFactory {
	return func(systemPrompt string, _ llm.ModelMetadata, _ string, _ ...orchestration.PruningOverride) ContextManager {
		return &mockContextManager{systemPrompt: systemPrompt}
	}
}

// validPlanJSON is a plan JSON string used across informed planner tests.
const validPlanJSON = `{"steps": [{"id": "step_1", "description": "Research codebase", "depends_on": [], "parallelizable": false, "estimated_tools": ["read_file"]}, {"id": "step_2", "description": "Implement changes", "depends_on": ["step_1"], "parallelizable": false, "estimated_tools": ["write_file"]}]}`

// finishWithPlan returns a ChatResponse that calls the finish tool with the given plan JSON.
func finishWithPlan(planJSON string) *llm.ChatResponse {
	answerBytes, _ := json.Marshal(planJSON)
	finishInput := `{"answer":` + string(answerBytes) + `}`
	return &llm.ChatResponse{
		Message: llm.Message{
			Role:    "assistant",
			Content: "Here is the plan",
			ToolCalls: []llm.ToolCall{
				{ID: "call_finish", Name: "finish", Input: json.RawMessage(finishInput)},
			},
		},
		StopReason: "tool_use",
		Usage:      llm.TokenUsage{InputTokens: 200, OutputTokens: 100},
	}
}

func TestPlanWithExploration_InformedPath(t *testing.T) {
	callCount := 0
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callCount++
			if callCount == 1 {
				// Executor step 1: explore with get_architecture
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: "Let me explore the codebase",
						ToolCalls: []llm.ToolCall{
							{ID: "call_1", Name: "get_architecture", Input: json.RawMessage(`{}`)},
						},
					},
					StopReason: "tool_use",
					Usage:      llm.TokenUsage{InputTokens: 100, OutputTokens: 50},
				}, nil
			}
			// Executor step 2: finish with plan
			return finishWithPlan(validPlanJSON), nil
		},
	}

	reg := newPlannerTestRegistry([]struct{ name, source string }{
		{"get_architecture", "mcp:test-server"},
		{"read_file", ""},
		{"list_directory", ""},
		{"glob", ""},
		{"ripgrep", ""},
	})

	planner := NewPlanner(mockLLM)
	planner.SetToolRegistry(reg)
	planner.SetContextFactory(plannerContextFactory())
	planner.SetTokenCounter(llm.NewSimpleTokenCounter())

	ctx := WithDomain(context.Background(), "code")
	ctx = tools.WithWorkspacePath(ctx, "/tmp/test")

	plan, err := planner.Plan(ctx, "Refactor authentication module", nil, nil, nil, false)
	if err != nil {
		t.Fatalf("Plan() returned error: %v", err)
	}

	// Verify exploration was used (multiple LLM calls, not just one)
	if callCount < 2 {
		t.Errorf("expected at least 2 LLM calls (exploration), got %d", callCount)
	}

	// Verify plan was parsed correctly
	if len(plan.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(plan.Steps))
	}
	if plan.Steps[0].ID != "step_1" {
		t.Errorf("expected first step ID 'step_1', got %q", plan.Steps[0].ID)
	}
	if plan.Steps[1].ID != "step_2" {
		t.Errorf("expected second step ID 'step_2', got %q", plan.Steps[1].ID)
	}
	if len(plan.Steps[1].DependsOn) != 1 || plan.Steps[1].DependsOn[0] != "step_1" {
		t.Errorf("expected step_2 to depend on step_1, got %v", plan.Steps[1].DependsOn)
	}
}

func TestPlanWithExploration_FSOnlyPath(t *testing.T) {
	// Only FS tools — should still use exploration
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			return finishWithPlan(validPlanJSON), nil
		},
	}

	reg := newPlannerTestRegistry([]struct{ name, source string }{
		{"read_file", ""},
		{"list_directory", ""},
		{"glob", ""},
		{"ripgrep", ""},
	})

	planner := NewPlanner(mockLLM)
	planner.SetToolRegistry(reg)
	planner.SetContextFactory(plannerContextFactory())
	planner.SetTokenCounter(llm.NewSimpleTokenCounter())

	// Verify getPlannerTools returns only FS tools
	plannerTools := planner.getPlannerTools()
	if len(plannerTools) == 0 {
		t.Fatal("expected FS planner tools, got none")
	}

	ctx := WithDomain(context.Background(), "code")
	ctx = tools.WithWorkspacePath(ctx, "/tmp/test")

	plan, err := planner.Plan(ctx, "Analyze codebase", nil, nil, nil, false)
	if err != nil {
		t.Fatalf("Plan() returned error: %v", err)
	}
	if len(plan.Steps) != 2 {
		t.Errorf("expected 2 steps, got %d", len(plan.Steps))
	}
}

func TestPlanDirect_GeneralDomain(t *testing.T) {
	t.Run("low_complexity_uses_direct", func(t *testing.T) {
		var capturedRequest llm.ChatRequest
		callCount := 0

		mockLLM := &mockLLMCaller{
			callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
				callCount++
				capturedRequest = req
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"steps": [{"id": "step_1", "description": "Do general task", "depends_on": [], "parallelizable": true, "estimated_tools": ["bash"]}]}`,
					},
					StopReason: "end_turn",
				}, nil
			},
		}

		reg := newPlannerTestRegistry([]struct{ name, source string }{
			{"get_architecture", "mcp:test-server"},
			{"read_file", ""},
		})

		planner := NewPlanner(mockLLM)
		planner.SetToolRegistry(reg)
		planner.SetContextFactory(plannerContextFactory())

		// domain="general" with no/low complexity → still uses planDirect
		ctx := WithDomain(context.Background(), "general")

		plan, err := planner.Plan(ctx, "Write a poem", nil, nil, nil, false)
		if err != nil {
			t.Fatalf("Plan() returned error: %v", err)
		}

		// planDirect uses exactly 1 LLM call
		if callCount != 1 {
			t.Errorf("expected 1 LLM call (planDirect), got %d", callCount)
		}

		if len(capturedRequest.Messages) != 2 {
			t.Errorf("expected 2 messages (system + user), got %d", len(capturedRequest.Messages))
		}
		if capturedRequest.Messages[0].Role != "system" {
			t.Errorf("expected first message role 'system', got %q", capturedRequest.Messages[0].Role)
		}
		if capturedRequest.Messages[1].Role != "user" {
			t.Errorf("expected second message role 'user', got %q", capturedRequest.Messages[1].Role)
		}

		if len(plan.Steps) != 1 {
			t.Errorf("expected 1 step, got %d", len(plan.Steps))
		}
	})

	t.Run("high_complexity_uses_exploration", func(t *testing.T) {
		callCount := 0
		mockLLM := &mockLLMCaller{
			callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
				callCount++
				return finishWithPlan(validPlanJSON), nil
			},
		}

		reg := newPlannerTestRegistry([]struct{ name, source string }{
			{"get_architecture", "mcp:test-server"},
			{"read_file", ""},
			{"list_directory", ""},
			{"glob", ""},
			{"ripgrep", ""},
		})

		planner := NewPlanner(mockLLM)
		planner.SetToolRegistry(reg)
		planner.SetContextFactory(plannerContextFactory())
		planner.SetTokenCounter(llm.NewSimpleTokenCounter())

		// domain="general" with complexity >= 4 → uses exploration (not planDirect)
		ctx := WithDomain(context.Background(), "general")
		ctx = WithComplexity(ctx, 5)
		ctx = tools.WithWorkspacePath(ctx, "/tmp/test")

		plan, err := planner.Plan(ctx, "Analyze the entire codebase architecture and produce a comprehensive report", nil, nil, nil, false)
		if err != nil {
			t.Fatalf("Plan() returned error: %v", err)
		}

		// Exploration uses multiple LLM calls (at least the finish call)
		if callCount < 1 {
			t.Errorf("expected at least 1 LLM call (exploration), got %d", callCount)
		}

		if plan == nil {
			t.Fatal("expected non-nil plan")
		}
	})
}

func TestPlanDirect_NoToolsAvailable(t *testing.T) {
	callCount := 0
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callCount++
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:    "assistant",
					Content: `{"steps": [{"id": "step_1", "description": "Fallback task", "depends_on": [], "parallelizable": false, "estimated_tools": []}]}`,
				},
				StopReason: "end_turn",
			}, nil
		},
	}

	// Test with nil registry
	planner := NewPlanner(mockLLM)
	// toolRegistry is nil by default

	ctx := WithDomain(context.Background(), "code")

	plan, err := planner.Plan(ctx, "Do something", nil, nil, nil, false)
	if err != nil {
		t.Fatalf("Plan() returned error: %v", err)
	}

	// Should use planDirect (single call)
	if callCount != 1 {
		t.Errorf("expected 1 LLM call (planDirect fallback), got %d", callCount)
	}
	if len(plan.Steps) != 1 {
		t.Errorf("expected 1 step, got %d", len(plan.Steps))
	}

	// Test with registry that has no relevant tools
	callCount = 0
	reg := newPlannerTestRegistry([]struct{ name, source string }{
		{"unrelated_tool", ""},
	})
	planner2 := NewPlanner(mockLLM)
	planner2.SetToolRegistry(reg)

	plan2, err := planner2.Plan(ctx, "Do something", nil, nil, nil, false)
	if err != nil {
		t.Fatalf("Plan() returned error: %v", err)
	}
	if callCount != 1 {
		t.Errorf("expected 1 LLM call (planDirect, no relevant tools), got %d", callCount)
	}
	if len(plan2.Steps) != 1 {
		t.Errorf("expected 1 step, got %d", len(plan2.Steps))
	}
}

func TestGetPlannerTools(t *testing.T) {
	t.Run("mixed_registry_fs_only", func(t *testing.T) {
		reg := newPlannerTestRegistry([]struct{ name, source string }{
			{"get_architecture", "mcp:test-server"},
			{"query_codebase", "mcp:test-server"},
			{"read_file", ""},
			{"list_directory", ""},
			{"glob", ""},
			{"ripgrep", ""},
			{"write_file", ""}, // should NOT be included
		})

		p := &Planner{toolRegistry: reg}
		result := p.getPlannerTools()

		// Should include only FS tools, not MCP or write tools
		names := map[string]bool{}
		for _, td := range result {
			names[td.Name] = true
		}

		expectedFS := []string{"read_file", "list_directory", "glob", "ripgrep"}
		for _, e := range expectedFS {
			if !names[e] {
				t.Errorf("expected tool %q in result", e)
			}
		}
		if names["write_file"] {
			t.Error("write_file should NOT be included in planner tools")
		}
		if names["get_architecture"] {
			t.Error("MCP tool get_architecture should NOT be included in planner tools")
		}
		if names["query_codebase"] {
			t.Error("MCP tool query_codebase should NOT be included in planner tools")
		}
	})

	t.Run("fs_only", func(t *testing.T) {
		reg := newPlannerTestRegistry([]struct{ name, source string }{
			{"read_file", ""},
			{"glob", ""},
		})

		p := &Planner{toolRegistry: reg}
		result := p.getPlannerTools()

		names := map[string]bool{}
		for _, td := range result {
			names[td.Name] = true
		}

		if !names["read_file"] || !names["glob"] {
			t.Errorf("expected FS tools in result, got %v", names)
		}
	})

	t.Run("nil_registry", func(t *testing.T) {
		p := &Planner{}
		result := p.getPlannerTools()
		if result != nil {
			t.Errorf("expected nil for nil registry, got %v", result)
		}
	})
}

func TestPlanWithExploration_DomainVariants(t *testing.T) {
	for _, domain := range []string{"code", "research", "mixed"} {
		t.Run(domain, func(t *testing.T) {
			callCount := 0
			mockLLM := &mockLLMCaller{
				callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
					callCount++
					// Return finish immediately with plan
					return finishWithPlan(validPlanJSON), nil
				},
			}

			reg := newPlannerTestRegistry([]struct{ name, source string }{
				{"get_architecture", "mcp:test-server"},
				{"read_file", ""},
			})

			planner := NewPlanner(mockLLM)
			planner.SetToolRegistry(reg)
			planner.SetContextFactory(plannerContextFactory())
			planner.SetTokenCounter(llm.NewSimpleTokenCounter())

			ctx := WithDomain(context.Background(), domain)
			ctx = tools.WithWorkspacePath(ctx, "/tmp/test")

			plan, err := planner.Plan(ctx, "Domain-specific task", nil, nil, nil, false)
			if err != nil {
				t.Fatalf("Plan() returned error for domain %q: %v", domain, err)
			}

			// Verify exploration was used (the executor ran, not just a direct LLM call).
			// With exploration, the LLM is called through the executor's ReAct loop,
			// which uses a different pattern than planDirect's single call.
			if plan == nil {
				t.Fatal("expected non-nil plan")
			}
			if len(plan.Steps) != 2 {
				t.Errorf("expected 2 steps, got %d", len(plan.Steps))
			}
		})
	}

	// Verify "general" with low complexity does NOT use exploration
	t.Run("general_low_complexity_uses_direct", func(t *testing.T) {
		callCount := 0
		mockLLM := &mockLLMCaller{
			callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
				callCount++
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"steps": [{"id": "step_1", "description": "General task", "depends_on": []}]}`,
					},
					StopReason: "end_turn",
				}, nil
			},
		}

		reg := newPlannerTestRegistry([]struct{ name, source string }{
			{"get_architecture", "mcp:test-server"},
			{"read_file", ""},
		})

		planner := NewPlanner(mockLLM)
		planner.SetToolRegistry(reg)
		planner.SetContextFactory(plannerContextFactory())

		ctx := WithDomain(context.Background(), "general")
		ctx = WithComplexity(ctx, 2) // below threshold → planDirect

		_, err := planner.Plan(ctx, "General task", nil, nil, nil, false)
		if err != nil {
			t.Fatalf("Plan() returned error: %v", err)
		}

		// planDirect uses exactly 1 LLM call
		if callCount != 1 {
			t.Errorf("expected 1 LLM call (planDirect for 'general' low complexity), got %d", callCount)
		}
	})

	// Verify "general" with high complexity uses exploration
	t.Run("general_high_complexity_uses_exploration", func(t *testing.T) {
		callCount := 0
		mockLLM := &mockLLMCaller{
			callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
				callCount++
				return finishWithPlan(validPlanJSON), nil
			},
		}

		reg := newPlannerTestRegistry([]struct{ name, source string }{
			{"get_architecture", "mcp:test-server"},
			{"read_file", ""},
			{"list_directory", ""},
			{"glob", ""},
			{"ripgrep", ""},
		})

		planner := NewPlanner(mockLLM)
		planner.SetToolRegistry(reg)
		planner.SetContextFactory(plannerContextFactory())
		planner.SetTokenCounter(llm.NewSimpleTokenCounter())

		ctx := WithDomain(context.Background(), "general")
		ctx = WithComplexity(ctx, 5) // >= 4 threshold → exploration
		ctx = tools.WithWorkspacePath(ctx, "/tmp/test")

		plan, err := planner.Plan(ctx, "Analyze the entire codebase", nil, nil, nil, false)
		if err != nil {
			t.Fatalf("Plan() returned error: %v", err)
		}

		if plan == nil {
			t.Fatal("expected non-nil plan")
		}
		if len(plan.Steps) != 2 {
			t.Errorf("expected 2 steps, got %d", len(plan.Steps))
		}
	})
}

func TestPlanWithExploration_ExecutorError_FallsBackToDirect(t *testing.T) {
	callCount := 0
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callCount++
			if callCount == 1 {
				// First call (from exploration executor): return an error
				return nil, errors.New("simulated executor failure")
			}
			// Second call (from planDirect fallback): return valid plan JSON
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:    "assistant",
					Content: validPlanJSON,
				},
				StopReason: "end_turn",
			}, nil
		},
	}

	reg := newPlannerTestRegistry([]struct{ name, source string }{
		{"get_architecture", "mcp:test-server"},
		{"read_file", ""},
		{"list_directory", ""},
		{"glob", ""},
		{"ripgrep", ""},
	})

	planner := NewPlanner(mockLLM)
	planner.SetToolRegistry(reg)
	planner.SetContextFactory(plannerContextFactory())
	planner.SetTokenCounter(llm.NewSimpleTokenCounter())

	ctx := WithDomain(context.Background(), "code")
	ctx = tools.WithWorkspacePath(ctx, "/tmp/test")

	plan, err := planner.Plan(ctx, "Refactor module", nil, nil, nil, false)
	if err != nil {
		t.Fatalf("Plan() should have fallen back to planDirect, got error: %v", err)
	}

	// Verify fallback happened: 1 call failed in executor, 1 call succeeded in planDirect
	if callCount < 2 {
		t.Errorf("expected at least 2 LLM calls (failed exploration + direct fallback), got %d", callCount)
	}

	// Verify plan was parsed correctly from the fallback
	if len(plan.Steps) != 2 {
		t.Fatalf("expected 2 steps from fallback plan, got %d", len(plan.Steps))
	}
	if plan.Steps[0].ID != "step_1" {
		t.Errorf("expected first step ID 'step_1', got %q", plan.Steps[0].ID)
	}
	if plan.Steps[1].ID != "step_2" {
		t.Errorf("expected second step ID 'step_2', got %q", plan.Steps[1].ID)
	}
}

func TestPlanWithExploration_ContextCancellation_Propagates(t *testing.T) {
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			return nil, context.Canceled
		},
	}

	reg := newPlannerTestRegistry([]struct{ name, source string }{
		{"get_architecture", "mcp:test-server"},
		{"read_file", ""},
	})

	planner := NewPlanner(mockLLM)
	planner.SetToolRegistry(reg)
	planner.SetContextFactory(plannerContextFactory())
	planner.SetTokenCounter(llm.NewSimpleTokenCounter())

	ctx, cancel := context.WithCancel(context.Background())
	ctx = WithDomain(ctx, "code")
	ctx = tools.WithWorkspacePath(ctx, "/tmp/test")
	cancel() // cancel immediately

	_, err := planner.Plan(ctx, "Refactor module", nil, nil, nil, false)
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled error, got: %v", err)
	}
}

func TestSummarizeExplorationSteps(t *testing.T) {
	t.Run("empty input", func(t *testing.T) {
		result := summarizeExplorationSteps(nil)
		if result != "" {
			t.Errorf("expected empty string, got %q", result)
		}
	})

	t.Run("steps with thoughts and tools", func(t *testing.T) {
		steps := []agent.Step{
			{
				Thought: "Found main.go in project root",
				Action:  llm.ToolCall{Name: "read_file"},
			},
			{
				Thought: "Project uses Go modules",
				Action:  llm.ToolCall{Name: "list_directory"},
			},
		}
		result := summarizeExplorationSteps(steps)
		if !strings.Contains(result, "Found main.go in project root") {
			t.Errorf("expected first thought in output, got %q", result)
		}
		if !strings.Contains(result, "(via read_file)") {
			t.Errorf("expected (via read_file) suffix, got %q", result)
		}
		if !strings.Contains(result, "Project uses Go modules") {
			t.Errorf("expected second thought in output, got %q", result)
		}
		if !strings.Contains(result, "(via list_directory)") {
			t.Errorf("expected (via list_directory) suffix, got %q", result)
		}
	})

	t.Run("steps without thoughts are skipped", func(t *testing.T) {
		steps := []agent.Step{
			{
				Thought: "Useful thought",
				Action:  llm.ToolCall{Name: "read_file"},
			},
			{
				Thought: "",
				Action:  llm.ToolCall{Name: "list_directory"},
			},
			{
				Thought: "   ", // whitespace only
				Action:  llm.ToolCall{Name: "glob"},
			},
		}
		result := summarizeExplorationSteps(steps)
		if !strings.Contains(result, "Useful thought") {
			t.Errorf("expected non-empty thought in output, got %q", result)
		}
		if strings.Contains(result, "list_directory") {
			t.Errorf("expected step with empty thought to be skipped, got %q", result)
		}
		if strings.Contains(result, "glob") {
			t.Errorf("expected step with whitespace-only thought to be skipped, got %q", result)
		}
	})

	t.Run("truncation at cap", func(t *testing.T) {
		// Generate many steps that exceed 4000 chars
		steps := make([]agent.Step, 0, 200)
		for i := 0; i < 200; i++ {
			steps = append(steps, agent.Step{
				Thought: strings.Repeat("x", 50),
				Action:  llm.ToolCall{Name: "read_file"},
			})
		}
		result := summarizeExplorationSteps(steps)
		if len(result) > 4000 {
			t.Errorf("expected result length <= 4000, got %d", len(result))
		}
		if !strings.HasSuffix(result, "\n") {
			t.Errorf("expected result to end with newline, got %q", result[len(result)-10:])
		}
	})

	t.Run("thought without tool name", func(t *testing.T) {
		steps := []agent.Step{
			{
				Thought: "Synthesized findings",
				Action:  llm.ToolCall{Name: ""},
			},
		}
		result := summarizeExplorationSteps(steps)
		if !strings.Contains(result, "Synthesized findings") {
			t.Errorf("expected thought in output, got %q", result)
		}
		if strings.Contains(result, "(via") {
			t.Errorf("expected no (via ...) suffix for empty tool name, got %q", result)
		}
	})
}

func TestPlanContinuation(t *testing.T) {
	mockResponse := `{
		"steps": [
			{"id": "continuation_1", "description": "Address follow-up request", "depends_on": [], "parallelizable": false, "estimated_tools": ["bash"]}
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

	originalRequest := "Build a CLI tool"
	existingPlan := &Plan{
		Steps: []PlanStep{
			{ID: "step_1", Description: "Create main.go"},
			{ID: "step_2", Description: "Add tests"},
		},
	}
	completedSteps := []CompletedStep{
		{StepID: "step_1", Output: "Created main.go with basic CLI structure"},
		{StepID: "step_2", Output: "Added unit tests for all functions"},
	}
	newMessage := "Now add a version flag"

	plan, err := planner.PlanContinuation(context.Background(), originalRequest, existingPlan, completedSteps, newMessage, nil, nil, false)
	if err != nil {
		t.Fatalf("PlanContinuation() returned error: %v", err)
	}

	// Verify plan structure
	if len(plan.Steps) != 1 {
		t.Errorf("Expected 1 step, got %d", len(plan.Steps))
	}

	if plan.Steps[0].ID != "continuation_1" {
		t.Errorf("Expected step ID 'continuation_1', got %q", plan.Steps[0].ID)
	}

	if plan.Steps[0].Description != "Address follow-up request" {
		t.Errorf("Expected description 'Address follow-up request', got %q", plan.Steps[0].Description)
	}
}

// --- AGENTS.md prompt injection tests ---

// TestPlan_AgentsMD_InjectedInPrompt verifies that when AgentsMD is present in
// context, the planner's system prompt contains the AGENTS.md section.
func TestPlan_AgentsMD_InjectedInPrompt(t *testing.T) {
	var capturedRequest llm.ChatRequest

	mock := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			capturedRequest = req
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:    "assistant",
					Content: `{"steps": [{"id": "step_1", "summary": "Do it", "description": "Do it", "depends_on": [], "parallelizable": true, "estimated_tools": ["bash"]}]}`,
				},
				StopReason: "end_turn",
			}, nil
		},
	}

	planner := NewPlanner(mock)

	agentsContent := "# Project Instructions\nAlways run `make test` before committing."
	ctx := WithAgentsMD(context.Background(), &AgentsMD{Content: agentsContent})

	_, err := planner.Plan(ctx, "Build the feature", nil, nil, nil, false)
	if err != nil {
		t.Fatalf("Plan() returned error: %v", err)
	}

	systemPrompt := capturedRequest.Messages[0].Content
	if !strings.Contains(systemPrompt, "AGENTS.md") {
		t.Error("system prompt should contain 'AGENTS.md' heading")
	}
	if !strings.Contains(systemPrompt, agentsContent) {
		t.Error("system prompt should contain full AGENTS.md content")
	}
	if !strings.Contains(systemPrompt, "strictly follow") {
		t.Error("system prompt should instruct planner to strictly follow AGENTS.md")
	}
	if !strings.Contains(systemPrompt, "ask_user") {
		t.Error("system prompt should mention ask_user for contradictions")
	}
}

// TestPlan_AgentsMD_AbsentWhenNotInContext verifies that when AgentsMD is NOT
// in context, the planner's system prompt does not contain the AGENTS.md section.
func TestPlan_AgentsMD_AbsentWhenNotInContext(t *testing.T) {
	var capturedRequest llm.ChatRequest

	mock := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			capturedRequest = req
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:    "assistant",
					Content: `{"steps": [{"id": "step_1", "summary": "Do it", "description": "Do it", "depends_on": [], "parallelizable": true, "estimated_tools": ["bash"]}]}`,
				},
				StopReason: "end_turn",
			}, nil
		},
	}

	planner := NewPlanner(mock)

	_, err := planner.Plan(context.Background(), "Build the feature", nil, nil, nil, false)
	if err != nil {
		t.Fatalf("Plan() returned error: %v", err)
	}

	systemPrompt := capturedRequest.Messages[0].Content
	if strings.Contains(systemPrompt, "<agents-md>") {
		t.Error("system prompt should NOT contain <agents-md> section when AgentsMD is not in context")
	}
}

// TestPlan_AgentsMD_InjectedInReplan verifies that the replan prompt also
// includes the AGENTS.md section when AgentsMD is present in context.
func TestPlan_AgentsMD_InjectedInReplan(t *testing.T) {
	var capturedRequest llm.ChatRequest

	mock := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			capturedRequest = req
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:    "assistant",
					Content: `{"steps": [{"id": "step_1", "summary": "Retry step", "description": "Retry", "depends_on": [], "parallelizable": true, "estimated_tools": ["bash"]}]}`,
				},
				StopReason: "end_turn",
			}, nil
		},
	}

	planner := NewPlanner(mock)

	agentsContent := "# Project Rules\nDo not modify vendor directory."
	ctx := WithAgentsMD(context.Background(), &AgentsMD{Content: agentsContent})

	originalPlan := &Plan{
		Steps: []PlanStep{
			{ID: "step_1", Summary: "Do something", Description: "Do something", DependsOn: []string{}},
		},
	}
	failedStep := CompletedStep{StepID: "step_1", Output: "failed", Error: errors.New("build error")}

	_, err := planner.Replan(ctx, originalPlan, nil, failedStep, nil, nil, nil)
	if err != nil {
		t.Fatalf("Replan() returned error: %v", err)
	}

	systemPrompt := capturedRequest.Messages[0].Content
	if !strings.Contains(systemPrompt, "<agents-md>") {
		t.Error("replan system prompt should contain <agents-md> section")
	}
	if !strings.Contains(systemPrompt, agentsContent) {
		t.Error("replan system prompt should contain full AGENTS.md content")
	}
}

// TestPlan_AgentsMD_InjectedInContinuation verifies that the continuation
// prompt also includes the AGENTS.md section when present in context.
func TestPlan_AgentsMD_InjectedInContinuation(t *testing.T) {
	var capturedRequest llm.ChatRequest

	mock := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			capturedRequest = req
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:    "assistant",
					Content: `{"steps": [{"id": "continuation_1", "summary": "New step", "description": "New step", "depends_on": ["step_1"], "parallelizable": true, "estimated_tools": ["bash"]}]}`,
				},
				StopReason: "end_turn",
			}, nil
		},
	}

	planner := NewPlanner(mock)

	agentsContent := "# Project Rules\nAlways use idiomatic Go."
	ctx := WithAgentsMD(context.Background(), &AgentsMD{Content: agentsContent})

	existingPlan := &Plan{
		Steps: []PlanStep{
			{ID: "step_1", Summary: "Original step", Description: "Original step", DependsOn: []string{}},
		},
	}
	completedSteps := []CompletedStep{
		{StepID: "step_1", Output: "done"},
	}

	_, err := planner.PlanContinuation(ctx, "Original request", existingPlan, completedSteps, "Follow-up", nil, nil, false)
	if err != nil {
		t.Fatalf("PlanContinuation() returned error: %v", err)
	}

	systemPrompt := capturedRequest.Messages[0].Content
	if !strings.Contains(systemPrompt, "<agents-md>") {
		t.Error("continuation system prompt should contain <agents-md> section")
	}
	if !strings.Contains(systemPrompt, agentsContent) {
		t.Error("continuation system prompt should contain full AGENTS.md content")
	}
}

func TestFormatActiveSkills(t *testing.T) {
	t.Parallel()

	t.Run("no skills in context", func(t *testing.T) {
		result := formatActiveSkills(context.Background(), "test preamble")
		if result != "" {
			t.Error("expected empty string when no skills in context")
		}
	})

	t.Run("with active skills", func(t *testing.T) {
		ctx := WithActiveSkills(context.Background(), &ActiveSkills{
			Skills: []*skills.Skill{
				{
					Metadata: skills.SkillMetadata{
						Name:         "pdf-processing",
						Description:  "Extract PDF text and tables.",
						AllowedTools: "Read Write",
					},
					Body:    "Step 1: Read the PDF.\nStep 2: Extract text.",
					DirPath: "/skills/pdf-processing",
				},
			},
		})

		result := formatActiveSkills(ctx, "test preamble")
		if !strings.Contains(result, "Active Skills") {
			t.Error("expected Active Skills heading")
		}
		if !strings.Contains(result, "pdf-processing") {
			t.Error("expected skill name")
		}
		if !strings.Contains(result, "Extract PDF text") {
			t.Error("expected skill description")
		}
		if !strings.Contains(result, "Allowed tools: Read Write") {
			t.Error("expected allowed-tools field")
		}
		if !strings.Contains(result, "Step 1: Read the PDF") {
			t.Error("expected skill body")
		}
	})

	t.Run("long skill body is emitted in full", func(t *testing.T) {
		longBody := strings.Repeat("x", 3000)
		ctx := WithActiveSkills(context.Background(), &ActiveSkills{
			Skills: []*skills.Skill{
				{
					Metadata: skills.SkillMetadata{Name: "long-skill", Description: "A long skill."},
					Body:     longBody,
					DirPath:  "/skills/long-skill",
				},
			},
		})

		result := formatActiveSkills(ctx, "test preamble")
		if strings.Contains(result, "truncated") {
			t.Error("skill body must not be truncated")
		}
		if !strings.Contains(result, longBody) {
			t.Error("expected full skill body to appear verbatim")
		}
	})

	t.Run("multiple skills", func(t *testing.T) {
		ctx := WithActiveSkills(context.Background(), &ActiveSkills{
			Skills: []*skills.Skill{
				{
					Metadata: skills.SkillMetadata{Name: "skill-a", Description: "First skill."},
					Body:     "Instructions for A.",
					DirPath:  "/skills/skill-a",
				},
				{
					Metadata: skills.SkillMetadata{Name: "skill-b", Description: "Second skill."},
					Body:     "Instructions for B.",
					DirPath:  "/skills/skill-b",
				},
			},
		})

		result := formatActiveSkills(ctx, "test preamble")
		if !strings.Contains(result, "skill-a") {
			t.Error("expected skill-a name")
		}
		if !strings.Contains(result, "skill-b") {
			t.Error("expected skill-b name")
		}
		if !strings.Contains(result, "Instructions for A") {
			t.Error("expected skill-a body")
		}
		if !strings.Contains(result, "Instructions for B") {
			t.Error("expected skill-b body")
		}
	})
}

func TestFormatPlanReflections(t *testing.T) {
	t.Parallel()

	t.Run("nil input", func(t *testing.T) {
		result := formatPlanReflections(nil)
		if result != "" {
			t.Errorf("expected empty string for nil input, got %q", result)
		}
	})

	t.Run("empty slice", func(t *testing.T) {
		result := formatPlanReflections([]Reflection{})
		if result != "" {
			t.Errorf("expected empty string for empty slice, got %q", result)
		}
	})

	t.Run("single reflection", func(t *testing.T) {
		result := formatPlanReflections([]Reflection{
			{
				FailureAnalysis: "test failed",
				RootCause:       "missing import",
				ActionPlan:      "add the import",
			},
		})
		if !strings.Contains(result, "Reflections from past attempts") {
			t.Error("expected header")
		}
		if !strings.Contains(result, "1. Failure: test failed") {
			t.Error("expected failure analysis")
		}
		if !strings.Contains(result, "Root cause: missing import") {
			t.Error("expected root cause")
		}
		if !strings.Contains(result, "Action plan: add the import") {
			t.Error("expected action plan")
		}
	})

	t.Run("multiple reflections", func(t *testing.T) {
		result := formatPlanReflections([]Reflection{
			{FailureAnalysis: "first failure", RootCause: "cause-1", ActionPlan: "plan-1"},
			{FailureAnalysis: "second failure", RootCause: "cause-2", ActionPlan: "plan-2"},
		})
		if !strings.Contains(result, "1. Failure: first failure") {
			t.Error("expected first reflection numbered 1")
		}
		if !strings.Contains(result, "2. Failure: second failure") {
			t.Error("expected second reflection numbered 2")
		}
	})
}

func TestFormatSessionReflections(t *testing.T) {
	t.Parallel()

	t.Run("nil input", func(t *testing.T) {
		result := formatSessionReflections(nil)
		if result != "" {
			t.Errorf("expected empty string for nil input, got %q", result)
		}
	})

	t.Run("empty slice", func(t *testing.T) {
		result := formatSessionReflections([]Reflection{})
		if result != "" {
			t.Errorf("expected empty string for empty slice, got %q", result)
		}
	})

	t.Run("single reflection", func(t *testing.T) {
		result := formatSessionReflections([]Reflection{
			{
				Summary:         "attempt summary",
				RootCause:       "root cause here",
				ActionPlan:      "do this next",
				SuggestedAction: "retry",
			},
		})
		if !strings.Contains(result, "Previous session reflections") {
			t.Error("expected header")
		}
		if !strings.Contains(result, "1. Summary: attempt summary") {
			t.Error("expected summary")
		}
		if !strings.Contains(result, "Root cause: root cause here") {
			t.Error("expected root cause")
		}
		if !strings.Contains(result, "Action plan: do this next") {
			t.Error("expected action plan")
		}
		if !strings.Contains(result, "Suggested: retry") {
			t.Error("expected suggested action")
		}
	})

	t.Run("multiple reflections", func(t *testing.T) {
		result := formatSessionReflections([]Reflection{
			{Summary: "first", RootCause: "c1", ActionPlan: "p1", SuggestedAction: "retry"},
			{Summary: "second", RootCause: "c2", ActionPlan: "p2", SuggestedAction: "replan"},
		})
		if !strings.Contains(result, "1. Summary: first") {
			t.Error("expected first reflection numbered 1")
		}
		if !strings.Contains(result, "2. Summary: second") {
			t.Error("expected second reflection numbered 2")
		}
		if !strings.Contains(result, "Suggested: replan") {
			t.Error("expected suggested action in second reflection")
		}
	})
}

func TestFormatVectorSearchHints(t *testing.T) {
	t.Parallel()

	t.Run("nil context", func(t *testing.T) {
		result := formatVectorSearchHints(context.Background(), "")
		if result != "" {
			t.Errorf("expected empty string for nil context, got %q", result)
		}
	})

	t.Run("with hints no footer", func(t *testing.T) {
		ctx := WithVectorSearchHints(context.Background(), &VectorSearchHints{
			Files: []VectorSearchHint{
				{FilePath: "src/main.go", Summary: "entry point"},
				{FilePath: "src/util.go"},
			},
		})
		result := formatVectorSearchHints(ctx, "")
		if !strings.Contains(result, "Relevant Project Files") {
			t.Error("expected header")
		}
		if !strings.Contains(result, "- src/main.go: entry point") {
			t.Error("expected file with summary")
		}
		if !strings.Contains(result, "- src/util.go") {
			t.Error("expected file without summary")
		}
		if strings.Contains(result, "semantic_search") {
			t.Error("should not contain footer when footer is empty")
		}
	})

	t.Run("with hints and footer", func(t *testing.T) {
		ctx := WithVectorSearchHints(context.Background(), &VectorSearchHints{
			Files: []VectorSearchHint{
				{FilePath: "src/main.go"},
			},
		})
		result := formatVectorSearchHints(ctx, "\nUse semantic_search tool for deeper investigation.")
		if !strings.Contains(result, "semantic_search") {
			t.Error("expected footer")
		}
	})
}

func TestFormatAgentsMD(t *testing.T) {
	t.Parallel()

	t.Run("nil context", func(t *testing.T) {
		result := formatAgentsMD(context.Background())
		if result != "" {
			t.Errorf("expected empty string for nil context, got %q", result)
		}
	})

	t.Run("with content", func(t *testing.T) {
		ctx := WithAgentsMD(context.Background(), &AgentsMD{Content: "Use Go 1.26."})
		result := formatAgentsMD(ctx)
		if !strings.Contains(result, "AGENTS.md") {
			t.Error("expected header")
		}
		if !strings.Contains(result, "<agents-md>") {
			t.Error("expected agents-md tag")
		}
		if !strings.Contains(result, "Use Go 1.26.") {
			t.Error("expected content")
		}
	})

	t.Run("empty content", func(t *testing.T) {
		ctx := WithAgentsMD(context.Background(), &AgentsMD{Content: ""})
		result := formatAgentsMD(ctx)
		if result != "" {
			t.Errorf("expected empty string for empty content, got %q", result)
		}
	})
}

func TestAppendPlannerContextSections(t *testing.T) {
	t.Parallel()

	t.Run("empty context returns base", func(t *testing.T) {
		result := appendPlannerContextSections(context.Background(), "base prompt")
		if result != "base prompt" {
			t.Errorf("expected unchanged base, got %q", result)
		}
	})

	t.Run("all sections present", func(t *testing.T) {
		ctx := context.Background()
		ctx = WithVectorSearchHints(ctx, &VectorSearchHints{
			Files: []VectorSearchHint{{FilePath: "test.go", Summary: "test file"}},
		})
		ctx = WithAgentsMD(ctx, &AgentsMD{Content: "Project rules here."})
		ctx = WithActiveSkills(ctx, &ActiveSkills{
			Skills: []*skills.Skill{
				{
					Metadata: skills.SkillMetadata{Name: "test-skill"},
					Body:     "Skill instructions",
				},
			},
		})

		result := appendPlannerContextSections(ctx, "base")
		if !strings.Contains(result, "base") {
			t.Error("expected base")
		}
		if !strings.Contains(result, "Relevant Project Files") {
			t.Error("expected vector hints section")
		}
		if !strings.Contains(result, "test.go") {
			t.Error("expected file path in hints")
		}
		if strings.Contains(result, "semantic_search") {
			t.Error("planner should not have vector hints footer")
		}
		if !strings.Contains(result, "<agents-md>") {
			t.Error("expected AGENTS.md section")
		}
		if !strings.Contains(result, "Project rules here.") {
			t.Error("expected AGENTS.md content")
		}
		if !strings.Contains(result, "Active Skills") {
			t.Error("expected skills section")
		}
		if !strings.Contains(result, "test-skill") {
			t.Error("expected skill name")
		}
		if !strings.Contains(result, "incorporate their guidance") {
			t.Error("expected planner-specific skill preamble")
		}
	})
}
