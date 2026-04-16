package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	coretools "github.com/user/agent/core/tools"
	"github.com/user/agent/sdk/agent"
	"github.com/user/agent/sdk/llm"
	tools "github.com/user/agent/sdk/tools"
)

// Tests use shared mock types from testhelpers_test.go:
// - mockLLMCaller: implements LLMCaller (use callFn for custom behavior)

func TestPlan_CreatesValidDAG(t *testing.T) {
	// Mock returns 3-step plan with dependencies: step_2 depends on step_1, step_3 depends on step_1
	mockResponse := `{
		"steps": [
			{"id": "step_1", "description": "Initialize project", "depends_on": [], "parallelizable": false, "estimated_tools": ["bash"]},
			{"id": "step_2", "description": "Create main module", "depends_on": ["step_1"], "parallelizable": true, "estimated_tools": ["file_write"]},
			{"id": "step_3", "description": "Create tests", "depends_on": ["step_1"], "parallelizable": true, "estimated_tools": ["file_write"]}
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

	plan, err := planner.Plan(context.Background(), "Create a new Go project", availableTools, nil)
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

	_, err := planner.Plan(context.Background(), "Build project", availableTools, nil)
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

	plan, err := planner.Replan(context.Background(), originalPlan, completedSteps, failedStep, reflection, nil)
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

	_, err := planner.Plan(context.Background(), "Deploy application", nil, reflections)
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

	plan, err := planner.Plan(context.Background(), "Test task", nil, nil)
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

	_, _ = planner.Replan(context.Background(), originalPlan, completedSteps, failedStep, reflection, nil)

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

	plan, err := planner.Plan(context.Background(), "Research task", nil, nil)
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

	// Profile unmarshals as map[string]any from JSON since it's typed as `any`.
	profileMap, ok := step.Profile.(map[string]any)
	if !ok {
		t.Fatalf("expected Profile to be map[string]any, got %T", step.Profile)
	}

	if profileMap["role"] != "researcher" {
		t.Errorf("expected role 'researcher', got %q", profileMap["role"])
	}

	allowedTools, ok := profileMap["allowed_tools"].([]any)
	if !ok {
		t.Fatalf("expected allowed_tools to be []any, got %T", profileMap["allowed_tools"])
	}
	if len(allowedTools) != 3 {
		t.Errorf("expected 3 allowed tools, got %d", len(allowedTools))
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

	plan, err := planner.Plan(context.Background(), "Simple task", nil, nil)
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
	_, err := planner.Plan(ctx, "Build project", nil, nil)
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
	_, err = planner.Plan(context.Background(), "Build project", nil, nil)
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
	_, _ = planner.Replan(ctx, originalPlan, nil, failedStep, nil, nil)

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

	_, err := planner.Replan(context.Background(), originalPlan, completedSteps, failedStep, reflection, sessionReflections)
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

	_, err := planner.Plan(ctx, "Build project", nil, nil)
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
	return func(systemPrompt string, _ llm.ModelMetadata, _ string) ContextManager {
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
		{"get_architecture", "codebase-memory-server"},
		{"read_file", ""},
		{"list_directory", ""},
		{"glob", ""},
		{"ripgrep", ""},
		{"search_files", ""},
		{"batch", ""},
	})

	planner := NewPlanner(mockLLM)
	planner.SetToolRegistry(reg)
	planner.SetContextFactory(plannerContextFactory())
	planner.SetTokenCounter(llm.NewSimpleTokenCounter())

	ctx := WithDomain(context.Background(), "code")
	ctx = tools.WithWorkspacePath(ctx, "/tmp/test")

	plan, err := planner.Plan(ctx, "Refactor authentication module", nil, nil)
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
	// Only FS tools, no codebase-memory tools — should still use exploration
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
		{"search_files", ""},
		{"batch", ""},
	})

	planner := NewPlanner(mockLLM)
	planner.SetToolRegistry(reg)
	planner.SetContextFactory(plannerContextFactory())
	planner.SetTokenCounter(llm.NewSimpleTokenCounter())

	// Verify getPlannerTools returns only FS tools
	plannerTools := planner.getPlannerTools()
	for _, pt := range plannerTools {
		if strings.HasPrefix(pt.Source, "codebase-memory") {
			t.Errorf("unexpected codebase-memory tool: %s", pt.Name)
		}
	}
	if len(plannerTools) == 0 {
		t.Fatal("expected FS planner tools, got none")
	}

	// Verify batch is included
	hasBatch := false
	for _, pt := range plannerTools {
		if pt.Name == "batch" {
			hasBatch = true
		}
	}
	if !hasBatch {
		t.Error("expected batch tool in planner tools")
	}

	ctx := WithDomain(context.Background(), "code")
	ctx = tools.WithWorkspacePath(ctx, "/tmp/test")

	plan, err := planner.Plan(ctx, "Analyze codebase", nil, nil)
	if err != nil {
		t.Fatalf("Plan() returned error: %v", err)
	}
	if len(plan.Steps) != 2 {
		t.Errorf("expected 2 steps, got %d", len(plan.Steps))
	}
}

func TestPlanDirect_GeneralDomain(t *testing.T) {
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

	// Register codebase-memory tools — should still use planDirect for "general" domain
	reg := newPlannerTestRegistry([]struct{ name, source string }{
		{"get_architecture", "codebase-memory-server"},
		{"read_file", ""},
	})

	planner := NewPlanner(mockLLM)
	planner.SetToolRegistry(reg)
	planner.SetContextFactory(plannerContextFactory())

	ctx := WithDomain(context.Background(), "general")

	plan, err := planner.Plan(ctx, "Write a poem", nil, nil)
	if err != nil {
		t.Fatalf("Plan() returned error: %v", err)
	}

	// planDirect uses exactly 1 LLM call
	if callCount != 1 {
		t.Errorf("expected 1 LLM call (planDirect), got %d", callCount)
	}

	// Verify it's a direct system+user message pattern (not executor ReAct)
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

	plan, err := planner.Plan(ctx, "Do something", nil, nil)
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

	plan2, err := planner2.Plan(ctx, "Do something", nil, nil)
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
	t.Run("codebase_memory_and_fs", func(t *testing.T) {
		reg := newPlannerTestRegistry([]struct{ name, source string }{
			{"get_architecture", "codebase-memory-server"},
			{"query_codebase", "codebase-memory-server"},
			{"read_file", ""},
			{"list_directory", ""},
			{"glob", ""},
			{"ripgrep", ""},
			{"search_files", ""},
			{"batch", ""},
			{"write_file", ""}, // should NOT be included
		})

		p := &Planner{toolRegistry: reg}
		result := p.getPlannerTools()

		// Should include both codebase-memory and FS tools, but not write_file
		names := map[string]bool{}
		for _, td := range result {
			names[td.Name] = true
		}

		expected := []string{"get_architecture", "query_codebase", "read_file", "list_directory", "glob", "ripgrep", "search_files", "batch"}
		for _, e := range expected {
			if !names[e] {
				t.Errorf("expected tool %q in result", e)
			}
		}
		if names["write_file"] {
			t.Error("write_file should NOT be included in planner tools")
		}
	})

	t.Run("fs_only", func(t *testing.T) {
		reg := newPlannerTestRegistry([]struct{ name, source string }{
			{"read_file", ""},
			{"glob", ""},
			{"batch", ""},
		})

		p := &Planner{toolRegistry: reg}
		result := p.getPlannerTools()

		names := map[string]bool{}
		for _, td := range result {
			names[td.Name] = true
		}

		if !names["read_file"] || !names["glob"] || !names["batch"] {
			t.Errorf("expected FS tools in result, got %v", names)
		}

		for _, td := range result {
			if strings.HasPrefix(td.Source, "codebase-memory") {
				t.Errorf("unexpected codebase-memory tool: %s", td.Name)
			}
		}
	})

	t.Run("nil_registry", func(t *testing.T) {
		p := &Planner{}
		result := p.getPlannerTools()
		if result != nil {
			t.Errorf("expected nil for nil registry, got %v", result)
		}
	})

	t.Run("batch_always_included", func(t *testing.T) {
		reg := newPlannerTestRegistry([]struct{ name, source string }{
			{"read_file", ""},
			{"batch", ""},
		})

		p := &Planner{toolRegistry: reg}
		result := p.getPlannerTools()

		hasBatch := false
		for _, td := range result {
			if td.Name == "batch" {
				hasBatch = true
			}
		}
		if !hasBatch {
			t.Error("expected batch to be included when FS tools are present")
		}
	})
}

func TestHasCodebaseMemoryTools(t *testing.T) {
	t.Run("with_codebase_memory", func(t *testing.T) {
		reg := newPlannerTestRegistry([]struct{ name, source string }{
			{"get_architecture", "codebase-memory-server"},
			{"read_file", ""},
		})

		p := &Planner{toolRegistry: reg}
		if !p.hasCodebaseMemoryTools() {
			t.Error("expected hasCodebaseMemoryTools() to return true")
		}
	})

	t.Run("without_codebase_memory", func(t *testing.T) {
		reg := newPlannerTestRegistry([]struct{ name, source string }{
			{"read_file", ""},
			{"glob", ""},
		})

		p := &Planner{toolRegistry: reg}
		if p.hasCodebaseMemoryTools() {
			t.Error("expected hasCodebaseMemoryTools() to return false")
		}
	})

	t.Run("nil_registry", func(t *testing.T) {
		p := &Planner{}
		if p.hasCodebaseMemoryTools() {
			t.Error("expected hasCodebaseMemoryTools() to return false for nil registry")
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
				{"get_architecture", "codebase-memory-server"},
				{"read_file", ""},
				{"batch", ""},
			})

			planner := NewPlanner(mockLLM)
			planner.SetToolRegistry(reg)
			planner.SetContextFactory(plannerContextFactory())
			planner.SetTokenCounter(llm.NewSimpleTokenCounter())

			ctx := WithDomain(context.Background(), domain)
			ctx = tools.WithWorkspacePath(ctx, "/tmp/test")

			plan, err := planner.Plan(ctx, "Domain-specific task", nil, nil)
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

	// Verify "general" does NOT use exploration
	t.Run("general_uses_direct", func(t *testing.T) {
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
			{"get_architecture", "codebase-memory-server"},
			{"read_file", ""},
		})

		planner := NewPlanner(mockLLM)
		planner.SetToolRegistry(reg)
		planner.SetContextFactory(plannerContextFactory())

		ctx := WithDomain(context.Background(), "general")

		_, err := planner.Plan(ctx, "General task", nil, nil)
		if err != nil {
			t.Fatalf("Plan() returned error: %v", err)
		}

		// planDirect uses exactly 1 LLM call
		if callCount != 1 {
			t.Errorf("expected 1 LLM call (planDirect for 'general'), got %d", callCount)
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
		{"get_architecture", "codebase-memory-server"},
		{"read_file", ""},
		{"list_directory", ""},
		{"glob", ""},
		{"ripgrep", ""},
		{"search_files", ""},
		{"batch", ""},
	})

	planner := NewPlanner(mockLLM)
	planner.SetToolRegistry(reg)
	planner.SetContextFactory(plannerContextFactory())
	planner.SetTokenCounter(llm.NewSimpleTokenCounter())

	ctx := WithDomain(context.Background(), "code")
	ctx = tools.WithWorkspacePath(ctx, "/tmp/test")

	plan, err := planner.Plan(ctx, "Refactor module", nil, nil)
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
		{"get_architecture", "codebase-memory-server"},
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

	_, err := planner.Plan(ctx, "Refactor module", nil, nil)
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

	plan, err := planner.PlanContinuation(context.Background(), originalRequest, existingPlan, completedSteps, newMessage, nil)
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
