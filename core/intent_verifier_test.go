package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/user/agent/sdk/llm"
	"github.com/user/agent/sdk/tools"
)

// ---------------------------------------------------------------------------
// parseIntentVerdict tests
// ---------------------------------------------------------------------------

func TestParseIntentVerdict_YesWithFeedback(t *testing.T) {
	output := "YES\n**What was requested:** something\n**What was delivered:** something"
	passed, feedback := parseIntentVerdict(output)
	if !passed {
		t.Error("expected passed=true for YES response")
	}
	if !strings.Contains(feedback, "What was requested") {
		t.Errorf("expected feedback to contain structured content, got %q", feedback)
	}
}

func TestParseIntentVerdict_NoWithFeedback(t *testing.T) {
	output := "NO\n**Gaps:** missing feature X"
	passed, feedback := parseIntentVerdict(output)
	if passed {
		t.Error("expected passed=false for NO response")
	}
	if !strings.Contains(feedback, "missing feature X") {
		t.Errorf("expected feedback to contain gap info, got %q", feedback)
	}
}

func TestParseIntentVerdict_CaseInsensitive(t *testing.T) {
	tests := []struct {
		input    string
		wantPass bool
	}{
		{"yes, everything looks good", true},
		{"Yes\nAll good", true},
		{"YES\nPerfect", true},
		{"no, something is wrong", false},
		{"No\nMissing items", false},
		{"NO\nFailed", false},
	}
	for _, tt := range tests {
		passed, _ := parseIntentVerdict(tt.input)
		if passed != tt.wantPass {
			t.Errorf("parseIntentVerdict(%q) = %v, want %v", tt.input, passed, tt.wantPass)
		}
	}
}

func TestParseIntentVerdict_EmptyOutput(t *testing.T) {
	passed, feedback := parseIntentVerdict("")
	if passed {
		t.Error("expected passed=false for empty output")
	}
	if feedback != "" {
		t.Errorf("expected empty feedback for empty output, got %q", feedback)
	}
}

func TestParseIntentVerdict_AmbiguousOutput(t *testing.T) {
	// Output that doesn't start with YES or NO should default to false
	passed, _ := parseIntentVerdict("Maybe this works")
	if passed {
		t.Error("expected passed=false for ambiguous output")
	}
}

func TestParseIntentVerdict_YesSingleLine(t *testing.T) {
	passed, feedback := parseIntentVerdict("YES, all good")
	if !passed {
		t.Error("expected passed=true")
	}
	if feedback == "" {
		t.Error("expected non-empty feedback for single-line YES with trailing text")
	}
}

func TestParseIntentVerdict_NoSingleLine(t *testing.T) {
	passed, feedback := parseIntentVerdict("NO, missing tests")
	if passed {
		t.Error("expected passed=false")
	}
	if feedback == "" {
		t.Error("expected non-empty feedback for single-line NO with trailing text")
	}
}

// ---------------------------------------------------------------------------
// mockVerifierTool implements tools.Tool for testing with ToolRegistry
// ---------------------------------------------------------------------------

type mockVerifierTool struct {
	tools.BaseTool
}

func (m *mockVerifierTool) Execute(_ context.Context, _ json.RawMessage) (tools.ToolResult, error) {
	return tools.ToolResult{Content: "mock result"}, nil
}

func newMockVerifierTool(name string) *mockVerifierTool {
	return &mockVerifierTool{
		BaseTool: tools.BaseTool{
			ToolName:        name,
			ToolDescription: name + " tool",
			Schema:          json.RawMessage(`{}`),
		},
	}
}

// ---------------------------------------------------------------------------
// filterReadOnlyTools tests
// ---------------------------------------------------------------------------

func TestFilterReadOnlyTools(t *testing.T) {
	allTools := []tools.ToolDescriptor{
		{Name: "file_ops", Description: "file operations"},
		{Name: "bash_exec", Description: "execute bash"},
		{Name: "ripgrep", Description: "search files"},
		{Name: "glob", Description: "find files"},
		{Name: "web_fetch", Description: "fetch web content"},
		{Name: "ask_user", Description: "ask user"},
	}

	filtered := filterReadOnlyTools(allTools)

	// Should only keep file_ops, ripgrep, glob
	if len(filtered) != 3 {
		t.Fatalf("expected 3 filtered tools, got %d", len(filtered))
	}

	allowed := map[string]bool{
		"file_ops": false,
		"ripgrep":  false,
		"glob":     false,
	}
	for _, td := range filtered {
		if _, ok := allowed[td.Name]; !ok {
			t.Errorf("unexpected tool in filtered list: %s", td.Name)
		}
		allowed[td.Name] = true
	}
	for name, found := range allowed {
		if !found {
			t.Errorf("expected tool %s in filtered list but not found", name)
		}
	}
}

func TestFilterReadOnlyTools_NoMatchingTools(t *testing.T) {
	allTools := []tools.ToolDescriptor{
		{Name: "write_file"},
		{Name: "bash_exec"},
	}
	filtered := filterReadOnlyTools(allTools)
	if len(filtered) != 0 {
		t.Errorf("expected 0 filtered tools, got %d", len(filtered))
	}
}

// ---------------------------------------------------------------------------
// IntentVerifier.Verify integration tests (with mocks)
// ---------------------------------------------------------------------------

func TestIntentVerifierVerify_Passed(t *testing.T) {
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			{
				Message: llm.Message{
					Role:    "assistant",
					Content: "YES\n**What was requested:** implement feature X\n**What was delivered:** feature X implemented\n**Gaps:** None",
				},
				StopReason: "end_turn",
			},
		},
	}

	registry := tools.NewToolRegistry()
	tokenCounter := llm.NewSimpleTokenCounter()

	contextFactory := func(systemPrompt string, modelMeta llm.ModelMetadata, compactionStrategy string) ContextManager {
		return &mockContextManager{
			systemPrompt: systemPrompt,
		}
	}

	verifier := NewIntentVerifier(mockLLM, registry, tokenCounter, contextFactory, nil, nil, ToolResultBudget{})

	result, err := verifier.Verify(context.Background(), "implement feature X", "done", "modified main.go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Passed {
		t.Error("expected Passed=true")
	}
	if !strings.Contains(result.Feedback, "What was requested") {
		t.Errorf("expected structured feedback, got %q", result.Feedback)
	}
}

func TestIntentVerifierVerify_Failed(t *testing.T) {
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			{
				Message: llm.Message{
					Role:    "assistant",
					Content: "NO\n**What was requested:** implement feature X\n**What was delivered:** only partial\n**Gaps:** missing tests\n**Recommendation:** add unit tests",
				},
				StopReason: "end_turn",
			},
		},
	}

	registry := tools.NewToolRegistry()
	tokenCounter := llm.NewSimpleTokenCounter()

	contextFactory := func(systemPrompt string, modelMeta llm.ModelMetadata, compactionStrategy string) ContextManager {
		return &mockContextManager{
			systemPrompt: systemPrompt,
		}
	}

	verifier := NewIntentVerifier(mockLLM, registry, tokenCounter, contextFactory, nil, nil, ToolResultBudget{})

	result, err := verifier.Verify(context.Background(), "implement feature X", "partial", "modified main.go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Passed {
		t.Error("expected Passed=false")
	}
	if !strings.Contains(result.Feedback, "missing tests") {
		t.Errorf("expected feedback about missing tests, got %q", result.Feedback)
	}
}

func TestIntentVerifierToolFiltering(t *testing.T) {
	// Register both read-only and dangerous tools
	registry := tools.NewToolRegistry()
	registry.Register(newMockVerifierTool("file_ops"))
	registry.Register(newMockVerifierTool("ripgrep"))
	registry.Register(newMockVerifierTool("glob"))
	registry.Register(newMockVerifierTool("bash_exec"))
	registry.Register(newMockVerifierTool("write_file"))

	allTools := registry.List()
	filtered := filterReadOnlyTools(allTools)

	if len(filtered) != 3 {
		t.Fatalf("expected 3 read-only tools, got %d", len(filtered))
	}

	for _, td := range filtered {
		if !readOnlyToolAllowlist[td.Name] {
			t.Errorf("tool %q should not be in filtered list", td.Name)
		}
	}
}

func TestIntentVerifierEmptyDiff(t *testing.T) {
	var capturedTask string

	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{
			{
				Message: llm.Message{
					Role:    "assistant",
					Content: "YES\n**What was requested:** answer a question\n**What was delivered:** answered\n**Gaps:** None",
				},
				StopReason: "end_turn",
			},
		},
	}

	registry := tools.NewToolRegistry()
	tokenCounter := llm.NewSimpleTokenCounter()

	contextFactory := func(systemPrompt string, modelMeta llm.ModelMetadata, compactionStrategy string) ContextManager {
		cm := &mockContextManager{
			systemPrompt: systemPrompt,
		}
		// Override SetTask to capture the task description
		origSetTask := cm.SetTask
		_ = origSetTask
		return &taskCapturingCM{
			mockContextManager: cm,
			captureTask:        &capturedTask,
		}
	}

	verifier := NewIntentVerifier(mockLLM, registry, tokenCounter, contextFactory, nil, nil, ToolResultBudget{})

	result, err := verifier.Verify(context.Background(), "what is Go?", "Go is a programming language", "no changes")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Passed {
		t.Error("expected Passed=true")
	}

	// Verify that the change summary was included in the task
	if !strings.Contains(capturedTask, "no changes") {
		t.Errorf("expected task to contain change summary, got %q", capturedTask)
	}
}

// taskCapturingCM wraps mockContextManager to capture SetTask calls.
type taskCapturingCM struct {
	*mockContextManager
	captureTask *string
}

func (c *taskCapturingCM) SetTask(task string, criteria []AcceptanceCriterion) {
	*c.captureTask = task
	c.mockContextManager.SetTask(task, criteria)
}

func TestIntentVerifierVerify_WithToolUse(t *testing.T) {
	// Test that the verifier uses tools during verification
	callCount := 0
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callCount++
			if callCount == 1 {
				// First call: agent uses a tool
				return &llm.ChatResponse{
					Message: llm.Message{
						Role: "assistant",
						ToolCalls: []llm.ToolCall{
							{
								Name:  "read_file",
								Input: json.RawMessage(`{"path":"main.go"}`),
							},
						},
					},
				}, nil
			}
			// Second call: agent gives final verdict
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:    "assistant",
					Content: "YES\n**What was requested:** fix bug\n**What was delivered:** bug fixed\n**Gaps:** None",
				},
				StopReason: "end_turn",
			}, nil
		},
	}

	registry := tools.NewToolRegistry()
	registry.Register(newMockVerifierTool("read_file"))

	tokenCounter := llm.NewSimpleTokenCounter()

	contextFactory := func(systemPrompt string, modelMeta llm.ModelMetadata, compactionStrategy string) ContextManager {
		return &mockContextManager{
			systemPrompt: systemPrompt,
		}
	}

	verifier := NewIntentVerifier(mockLLM, registry, tokenCounter, contextFactory, nil, nil, ToolResultBudget{})

	result, err := verifier.Verify(context.Background(), "fix bug in main.go", "fixed", "modified main.go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Passed {
		t.Error("expected Passed=true")
	}
	if callCount < 2 {
		t.Errorf("expected at least 2 LLM calls (tool use + verdict), got %d", callCount)
	}
	if len(result.Steps) < 2 {
		t.Errorf("expected at least 2 steps, got %d", len(result.Steps))
	}
}
