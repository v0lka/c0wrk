package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/user/agent/sdk/llm"
	tools "github.com/user/agent/sdk/tools"
)

// concurrentSafeLLMCaller is a thread-safe LLMCaller for parallel evaluator tests.
// Unlike mockLLMCaller, it does not record calls to avoid data races.
type concurrentSafeLLMCaller struct {
	callFn func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error)
	mu     sync.Mutex
	count  int
}

func (c *concurrentSafeLLMCaller) Call(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	c.mu.Lock()
	c.count++
	c.mu.Unlock()
	return c.callFn(ctx, req)
}

// Tests use shared mock types from testhelpers_test.go:
// - mockLLMCaller: implements LLMCaller
// - mockToolExecutor: implements ToolExecutor
// - mockContextManager: implements ContextManager

// newTestEvaluator creates an evaluator with minimal dependencies for testing.
// For programmatic-only tests, llmCaller/toolRegistry/contextFactory can be nil.
func newTestEvaluator(toolExec ToolExecutor, llmCaller LLMCaller, opts ...func(*Evaluator)) *Evaluator {
	e := NewEvaluator(toolExec, llmCaller, nil, nil, nil, nil, nil, ToolResultBudget{})
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// newTestEvaluatorReAct creates an evaluator wired for llm_judge ReAct tests.
func newTestEvaluatorReAct(llmCaller LLMCaller) *Evaluator {
	reg := tools.NewToolRegistry()
	counter := llm.NewTokenCounter("approximate")
	factory := func(systemPrompt string, modelMeta llm.ModelMetadata, compactionStrategy string) ContextManager {
		return &mockContextManager{systemPrompt: systemPrompt}
	}
	return NewEvaluator(
		nil,     // ToolExecutor (not needed for llm_judge)
		llmCaller,
		reg,
		counter,
		factory,
		nil, // logger
		nil, // emitter
		ToolResultBudget{},
	)
}

func TestEvaluator_ProgrammaticPasses(t *testing.T) {
	mockTools := &mockToolExecutor{
		results: map[string]tools.ToolResult{
			"bash_exec": {
				Content: "all tests passed",
				IsError: false,
			},
		},
	}

	evaluator := newTestEvaluator(mockTools, nil)

	criteria := []AcceptanceCriterion{
		{
			ID:          "ac_1",
			Description: "Tests must pass",
			CheckType:   "programmatic",
			CheckCmd:    "go test ./...",
		},
	}

	evalResult, err := evaluator.Evaluate(context.Background(), "", criteria, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(evalResult.Passed) != 1 {
		t.Errorf("expected 1 passed, got %d", len(evalResult.Passed))
	}
	if len(evalResult.Failed) != 0 {
		t.Errorf("expected 0 failed, got %d", len(evalResult.Failed))
	}
	if evalResult.Passed[0].Criterion.ID != "ac_1" {
		t.Errorf("expected criterion ID ac_1, got %s", evalResult.Passed[0].Criterion.ID)
	}
}

func TestEvaluator_ProgrammaticFails(t *testing.T) {
	mockTools := &mockToolExecutor{
		results: map[string]tools.ToolResult{
			"bash_exec": {
				Content: "FAIL: TestSomething",
				IsError: true,
			},
		},
	}

	evaluator := newTestEvaluator(mockTools, nil)

	criteria := []AcceptanceCriterion{
		{
			ID:          "ac_1",
			Description: "Tests must pass",
			CheckType:   "programmatic",
			CheckCmd:    "go test ./...",
		},
	}

	evalResult, err := evaluator.Evaluate(context.Background(), "", criteria, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(evalResult.Failed) != 1 {
		t.Errorf("expected 1 failed, got %d", len(evalResult.Failed))
	}
	if len(evalResult.Passed) != 0 {
		t.Errorf("expected 0 passed, got %d", len(evalResult.Passed))
	}
	if evalResult.Failed[0].Criterion.ID != "ac_1" {
		t.Errorf("expected criterion ID ac_1, got %s", evalResult.Failed[0].Criterion.ID)
	}
	if evalResult.AllPassed {
		t.Error("expected AllPassed to be false")
	}
}

func TestEvaluator_LLMJudgePasses(t *testing.T) {
	// Mock LLM returns "YES" as a direct response (no tool calls).
	// The Executor treats this as an implicit finish with "YES..." as output.
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{{
			Message: llm.Message{
				Role:    "assistant",
				Content: "YES, the criterion is met because the code implements proper error handling.",
			},
			StopReason: "end_turn",
		}},
	}

	evaluator := newTestEvaluatorReAct(mockLLM)

	criteria := []AcceptanceCriterion{
		{
			ID:          "ac_2",
			Description: "Code must have proper error handling",
			CheckType:   "llm_judge",
		},
	}

	bb := NewMapBlackboard()

	evalResult, err := evaluator.Evaluate(context.Background(), "func doSomething() error { return nil }", criteria, bb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(evalResult.Passed) != 1 {
		t.Errorf("expected 1 passed, got %d", len(evalResult.Passed))
	}
	if len(evalResult.Failed) != 0 {
		t.Errorf("expected 0 failed, got %d", len(evalResult.Failed))
	}
	if evalResult.Passed[0].Criterion.ID != "ac_2" {
		t.Errorf("expected criterion ID ac_2, got %s", evalResult.Passed[0].Criterion.ID)
	}
}

func TestEvaluator_LLMJudgeFails(t *testing.T) {
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{{
			Message: llm.Message{
				Role:    "assistant",
				Content: "NO, the criterion is not met because there is no error handling.",
			},
			StopReason: "end_turn",
		}},
	}

	evaluator := newTestEvaluatorReAct(mockLLM)

	criteria := []AcceptanceCriterion{
		{
			ID:          "ac_2",
			Description: "Code must have proper error handling",
			CheckType:   "llm_judge",
		},
	}

	bb := NewMapBlackboard()

	evalResult, err := evaluator.Evaluate(context.Background(), "func doSomething() { panic(\"oops\") }", criteria, bb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(evalResult.Failed) != 1 {
		t.Errorf("expected 1 failed, got %d", len(evalResult.Failed))
	}
	if len(evalResult.Passed) != 0 {
		t.Errorf("expected 0 passed, got %d", len(evalResult.Passed))
	}
	if evalResult.Failed[0].Criterion.ID != "ac_2" {
		t.Errorf("expected criterion ID ac_2, got %s", evalResult.Failed[0].Criterion.ID)
	}
	if evalResult.AllPassed {
		t.Error("expected AllPassed to be false")
	}
}

func TestEvaluator_LLMJudgeUnclear(t *testing.T) {
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{{
			Message: llm.Message{
				Role:    "assistant",
				Content: "I cannot determine if this criterion is met without more context.",
			},
			StopReason: "end_turn",
		}},
	}

	evaluator := newTestEvaluatorReAct(mockLLM)

	criteria := []AcceptanceCriterion{
		{
			ID:          "ac_1",
			Description: "Code must be performant",
			CheckType:   "llm_judge",
		},
	}

	bb := NewMapBlackboard()

	evalResult, err := evaluator.Evaluate(context.Background(), "some code", criteria, bb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(evalResult.Unclear) != 1 {
		t.Errorf("expected 1 unclear, got %d", len(evalResult.Unclear))
	}
	if len(evalResult.Passed) != 0 {
		t.Errorf("expected 0 passed, got %d", len(evalResult.Passed))
	}
	if len(evalResult.Failed) != 0 {
		t.Errorf("expected 0 failed, got %d", len(evalResult.Failed))
	}
	if !evalResult.AllPassed {
		t.Error("expected AllPassed to be true when there are UNCLEAR results but zero FAILED results")
	}
}

func TestEvaluator_MixedCriteria(t *testing.T) {
	mockTools := &mockToolExecutor{
		results: map[string]tools.ToolResult{
			"bash_exec": {
				Content: "all tests passed",
				IsError: false, // passes
			},
		},
	}

	// LLM returns NO for the llm_judge criterion
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{{
			Message: llm.Message{
				Role:    "assistant",
				Content: "NO, the documentation is incomplete.",
			},
			StopReason: "end_turn",
		}},
	}

	reg := tools.NewToolRegistry()
	counter := llm.NewTokenCounter("approximate")
	factory := func(systemPrompt string, modelMeta llm.ModelMetadata, compactionStrategy string) ContextManager {
		return &mockContextManager{systemPrompt: systemPrompt}
	}
	evaluator := NewEvaluator(mockTools, mockLLM, reg, counter, factory, nil, nil, ToolResultBudget{})

	criteria := []AcceptanceCriterion{
		{
			ID:          "ac_1",
			Description: "Tests must pass",
			CheckType:   "programmatic",
			CheckCmd:    "go test ./...",
		},
		{
			ID:          "ac_2",
			Description: "Code must be well documented",
			CheckType:   "llm_judge",
		},
	}

	bb := NewMapBlackboard()

	evalResult, err := evaluator.Evaluate(context.Background(), "func foo() {}", criteria, bb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(evalResult.Passed) != 1 {
		t.Errorf("expected 1 passed, got %d", len(evalResult.Passed))
	}
	if len(evalResult.Failed) != 1 {
		t.Errorf("expected 1 failed, got %d", len(evalResult.Failed))
	}
	if evalResult.AllPassed {
		t.Error("expected AllPassed to be false when one criterion fails")
	}

	// Verify which criterion passed and which failed
	if evalResult.Passed[0].Criterion.ID != "ac_1" {
		t.Errorf("expected ac_1 to pass, got %s", evalResult.Passed[0].Criterion.ID)
	}
	if evalResult.Failed[0].Criterion.ID != "ac_2" {
		t.Errorf("expected ac_2 to fail, got %s", evalResult.Failed[0].Criterion.ID)
	}
}

func TestEvaluator_AllPassed(t *testing.T) {
	mockTools := &mockToolExecutor{
		results: map[string]tools.ToolResult{
			"bash_exec": {
				Content: "all tests passed",
				IsError: false,
			},
		},
	}

	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{{
			Message: llm.Message{
				Role:    "assistant",
				Content: "YES, the code quality is excellent.",
			},
			StopReason: "end_turn",
		}},
	}

	reg := tools.NewToolRegistry()
	counter := llm.NewTokenCounter("approximate")
	factory := func(systemPrompt string, modelMeta llm.ModelMetadata, compactionStrategy string) ContextManager {
		return &mockContextManager{systemPrompt: systemPrompt}
	}
	evaluator := NewEvaluator(mockTools, mockLLM, reg, counter, factory, nil, nil, ToolResultBudget{})

	criteria := []AcceptanceCriterion{
		{
			ID:          "ac_1",
			Description: "Tests must pass",
			CheckType:   "programmatic",
			CheckCmd:    "go test ./...",
		},
		{
			ID:          "ac_2",
			Description: "Code quality is good",
			CheckType:   "llm_judge",
		},
	}

	bb := NewMapBlackboard()

	evalResult, err := evaluator.Evaluate(context.Background(), "func wellWritten() error { return nil }", criteria, bb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(evalResult.Passed) != 2 {
		t.Errorf("expected 2 passed, got %d", len(evalResult.Passed))
	}
	if len(evalResult.Failed) != 0 {
		t.Errorf("expected 0 failed, got %d", len(evalResult.Failed))
	}
	if len(evalResult.Unclear) != 0 {
		t.Errorf("expected 0 unclear, got %d", len(evalResult.Unclear))
	}
	if !evalResult.AllPassed {
		t.Error("expected AllPassed to be true when all criteria pass")
	}
}

// TestEvaluateIntentVerificationSkipped verifies that an intent_verification
// criterion is placed into Unclear with a SKIPPED diagnostic and AllPassed remains true.
func TestEvaluateIntentVerificationSkipped(t *testing.T) {
	evaluator := newTestEvaluator(nil, nil)

	criteria := []AcceptanceCriterion{
		{
			ID:          "ac_intent",
			Description: "User intent must be verified",
			CheckType:   "intent_verification",
		},
	}

	evalResult, err := evaluator.Evaluate(context.Background(), "", criteria, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(evalResult.Unclear) != 1 {
		t.Fatalf("expected 1 unclear, got %d", len(evalResult.Unclear))
	}
	if len(evalResult.Passed) != 0 {
		t.Errorf("expected 0 passed, got %d", len(evalResult.Passed))
	}
	if len(evalResult.Failed) != 0 {
		t.Errorf("expected 0 failed, got %d", len(evalResult.Failed))
	}

	detail := evalResult.Unclear[0]
	if detail.Criterion.ID != "ac_intent" {
		t.Errorf("expected criterion ID ac_intent, got %s", detail.Criterion.ID)
	}
	if !strings.HasPrefix(detail.Diagnostic, "SKIPPED:") {
		t.Errorf("expected diagnostic to start with 'SKIPPED:', got %q", detail.Diagnostic)
	}
	if !evalResult.AllPassed {
		t.Error("expected AllPassed to be true since intent_verification is Unclear, not Failed")
	}
}

// TestEvaluator_BlackboardPassedToContext verifies that the blackboard is
// attached to the context passed to the ReAct executor.
func TestEvaluator_BlackboardPassedToContext(t *testing.T) {
	var capturedCtx context.Context

	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			capturedCtx = ctx
			return &llm.ChatResponse{
				Message:    llm.Message{Role: "assistant", Content: "YES, criterion met."},
				StopReason: "end_turn",
			}, nil
		},
	}

	evaluator := newTestEvaluatorReAct(mockLLM)

	criteria := []AcceptanceCriterion{{
		ID:          "ac_bb",
		Description: "Test blackboard context",
		CheckType:   "llm_judge",
	}}

	bb := NewMapBlackboard()
	bb.SetStepResult("step_1", "some output", nil, nil)

	_, err := evaluator.Evaluate(context.Background(), "result", criteria, bb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify blackboard is accessible from the context
	if capturedCtx == nil {
		t.Fatal("expected context to be captured during LLM call")
	}
	recoveredBB := BlackboardFromContext(capturedCtx)
	if recoveredBB == nil {
		t.Fatal("expected blackboard to be present in context")
	}
	if _, ok := recoveredBB.GetStepResult("step_1"); !ok {
		t.Error("expected step_1 result to be accessible from blackboard in context")
	}
}

// TestEvaluator_NoReconsideration verifies that the evaluator makes only ONE
// LLM interaction per criterion (no reconsideration phase).
func TestEvaluator_NoReconsideration(t *testing.T) {
	callCount := 0
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callCount++
			return &llm.ChatResponse{
				Message:    llm.Message{Role: "assistant", Content: "NO, criterion not met."},
				StopReason: "end_turn",
			}, nil
		},
	}

	evaluator := newTestEvaluatorReAct(mockLLM)

	criteria := []AcceptanceCriterion{{
		ID:          "ac_no_recon",
		Description: "Tests must pass",
		CheckType:   "llm_judge",
	}}

	bb := NewMapBlackboard()
	bb.SetStepResult("step_1", "some evidence", nil, nil)

	evalResult, err := evaluator.Evaluate(context.Background(), "test output", criteria, bb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The executor may call LLM multiple times due to nudge mechanism,
	// but there should be no separate reconsideration phase.
	// With the nudge, expect at most 2 calls (initial + nudge retry).
	if callCount > 2 {
		t.Errorf("expected at most 2 LLM calls (agent loop, no reconsideration), got %d", callCount)
	}

	// Criterion should be failed (no reconsideration to flip it)
	if len(evalResult.Failed) != 1 {
		t.Errorf("expected 1 failed, got %d", len(evalResult.Failed))
	}
	if evalResult.AllPassed {
		t.Error("expected AllPassed to be false")
	}

	// Verify no reconsidered flag
	for _, d := range evalResult.Failed {
		if d.Reconsidered {
			t.Error("expected Reconsidered to be false — reconsideration is removed")
		}
	}
}

func TestParseEvalVerdict(t *testing.T) {
	tests := []struct {
		name           string
		output         string
		expectPrefix   string
	}{
		{"YES response", "YES, criterion met.", "PASSED:"},
		{"yes lowercase", "yes everything is fine", "PASSED:"},
		{"NO response", "NO, criterion not met.", "FAILED:"},
		{"no lowercase", "no the test fails", "FAILED:"},
		{"Unclear response", "I cannot determine", "UNCLEAR:"},
		{"Empty response", "", "UNCLEAR:"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseEvalVerdict(tt.output)
			if !strings.HasPrefix(got, tt.expectPrefix) {
				t.Errorf("parseEvalVerdict(%q) = %q, want prefix %q", tt.output, got, tt.expectPrefix)
			}
		})
	}
}

func TestFilterEvaluatorTools(t *testing.T) {
	allTools := []tools.ToolDescriptor{
		{Name: "file_ops"},
		{Name: "ripgrep"},
		{Name: "glob"},
		{Name: "read_evidence"},
		{Name: "bash_exec"},
		{Name: "web_search"},
	}

	filtered := filterEvaluatorTools(allTools)

	if len(filtered) != 4 {
		t.Errorf("expected 4 filtered tools, got %d", len(filtered))
	}

	names := make(map[string]bool)
	for _, td := range filtered {
		names[td.Name] = true
	}

	for expected := range evaluatorToolWhitelist {
		if !names[expected] {
			t.Errorf("expected tool %q to be in filtered set", expected)
		}
	}

	if names["bash_exec"] {
		t.Error("bash_exec should not be in filtered set")
	}
	if names["web_search"] {
		t.Error("web_search should not be in filtered set")
	}
}

// TestEvaluator_ResultTruncation verifies that long results are truncated
// in the task description sent to the evaluator agent.
func TestEvaluator_ResultTruncation(t *testing.T) {
	var capturedReq llm.ChatRequest
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			capturedReq = req
			return &llm.ChatResponse{
				Message:    llm.Message{Role: "assistant", Content: "YES, all good."},
				StopReason: "end_turn",
			}, nil
		},
	}

	evaluator := newTestEvaluatorReAct(mockLLM)

	criteria := []AcceptanceCriterion{{
		ID:          "ac_trunc",
		Description: "Test truncation",
		CheckType:   "llm_judge",
	}}

	// Create a result longer than maxResultSummaryChars (2000)
	longResult := strings.Repeat("a", 3000)
	bb := NewMapBlackboard()

	_, err := evaluator.Evaluate(context.Background(), longResult, criteria, bb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check that the task message in the context contains truncated result
	// The task is set via cm.SetTask, which the mock records.
	// We can verify indirectly through the LLM request messages
	if len(capturedReq.Messages) == 0 {
		t.Fatal("expected at least one message in LLM request")
	}

	// Find the user message containing the task
	found := false
	for _, msg := range capturedReq.Messages {
		if strings.Contains(msg.Content, "Test truncation") {
			found = true
			// Should contain "..." indicating truncation
			if !strings.Contains(msg.Content, "...") {
				t.Error("expected truncated result to contain '...'")
			}
			// Should NOT contain the full 1000-char string
			if strings.Contains(msg.Content, longResult) {
				t.Error("expected result to be truncated, but full result was found")
			}
			break
		}
	}
	if !found {
		t.Error("expected to find criterion description in LLM request messages")
	}
}

func TestFilterEvaluatorTools_FileOpsReadOnly(t *testing.T) {
	// Build a file_ops descriptor with a schema containing both read and write actions
	schema := `{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"description": "The file operation to perform",
				"enum": ["read_file", "list_directory", "search_files", "search_content", "write_file", "edit_file", "create_directory", "delete_directory", "delete_file"]
			}
		}
	}`

	allTools := []tools.ToolDescriptor{
		{Name: "file_ops", Description: "File operations", InputSchema: json.RawMessage(schema)},
		{Name: "ripgrep"},
	}

	filtered := filterEvaluatorTools(allTools)

	// Find file_ops in filtered
	var fileOps *tools.ToolDescriptor
	for i := range filtered {
		if filtered[i].Name == "file_ops" {
			fileOps = &filtered[i]
			break
		}
	}
	if fileOps == nil {
		t.Fatal("file_ops not found in filtered tools")
	}

	// Parse the filtered schema and check enum
	var parsedSchema map[string]any
	if err := json.Unmarshal(fileOps.InputSchema, &parsedSchema); err != nil {
		t.Fatalf("failed to parse filtered schema: %v", err)
	}

	props, ok := parsedSchema["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties in schema")
	}
	actionProp, ok := props["action"].(map[string]any)
	if !ok {
		t.Fatal("expected action property in schema")
	}
	enumVals, ok := actionProp["enum"].([]any)
	if !ok {
		t.Fatal("expected enum in action property")
	}

	allowedActions := map[string]bool{"read_file": true, "list_directory": true, "search_files": true, "search_content": true}
	for _, v := range enumVals {
		action, ok := v.(string)
		if !ok {
			t.Errorf("expected string in enum, got %T", v)
			continue
		}
		if !allowedActions[action] {
			t.Errorf("unexpected action %q in filtered file_ops schema", action)
		}
		delete(allowedActions, action)
	}
	for missing := range allowedActions {
		t.Errorf("expected read-only action %q not found in filtered schema", missing)
	}

	// Verify write actions are absent
	for _, v := range enumVals {
		action, ok := v.(string)
		if !ok {
			continue
		}
		if fileOpsWriteActions[action] {
			t.Errorf("write action %q should not be in filtered schema", action)
		}
	}

	// Verify description was updated
	if !strings.Contains(fileOps.Description, "read-only") {
		t.Errorf("expected file_ops description to mention 'read-only', got %q", fileOps.Description)
	}
}

func TestReadOnlyToolExecutor_BlocksWrites(t *testing.T) {
	inner := &mockToolExecutor{
		results: map[string]tools.ToolResult{
			"file_ops": {Content: "should not reach"},
		},
	}
	ro := &readOnlyToolExecutor{inner: inner}

	writeActions := []string{"write_file", "edit_file", "create_directory", "delete_directory", "delete_file"}
	for _, action := range writeActions {
		input, _ := json.Marshal(map[string]string{"action": action, "path": "/tmp/test"})
		result, err := ro.Execute(context.Background(), "file_ops", input)
		if err != nil {
			t.Errorf("action %s: unexpected error: %v", action, err)
		}
		if !result.IsError {
			t.Errorf("action %s: expected IsError=true, got false", action)
		}
		if !strings.Contains(result.Content, "not allowed") {
			t.Errorf("action %s: expected 'not allowed' in content, got %q", action, result.Content)
		}
	}

	// Verify inner was never called
	if len(inner.calls) != 0 {
		t.Errorf("expected 0 calls to inner executor for write actions, got %d", len(inner.calls))
	}
}

func TestReadOnlyToolExecutor_AllowsReads(t *testing.T) {
	inner := &mockToolExecutor{
		results: map[string]tools.ToolResult{
			"file_ops": {Content: "file contents here"},
		},
	}
	ro := &readOnlyToolExecutor{inner: inner}

	readActions := []string{"read_file", "list_directory", "search_files", "search_content"}
	for _, action := range readActions {
		input, _ := json.Marshal(map[string]string{"action": action, "path": "/tmp/test"})
		result, err := ro.Execute(context.Background(), "file_ops", input)
		if err != nil {
			t.Errorf("action %s: unexpected error: %v", action, err)
		}
		if result.IsError {
			t.Errorf("action %s: expected IsError=false, got true", action)
		}
	}

	if len(inner.calls) != len(readActions) {
		t.Errorf("expected %d calls to inner executor for read actions, got %d", len(readActions), len(inner.calls))
	}
}

func TestReadOnlyToolExecutor_OtherToolsPassThrough(t *testing.T) {
	inner := &mockToolExecutor{
		results: map[string]tools.ToolResult{
			"ripgrep": {Content: "grep results"},
			"glob":    {Content: "glob results"},
		},
	}
	ro := &readOnlyToolExecutor{inner: inner}

	// ripgrep should pass through
	input, _ := json.Marshal(map[string]string{"pattern": "test"})
	result, err := ro.Execute(context.Background(), "ripgrep", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "grep results" {
		t.Errorf("expected 'grep results', got %q", result.Content)
	}

	// glob should pass through
	result, err = ro.Execute(context.Background(), "glob", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "glob results" {
		t.Errorf("expected 'glob results', got %q", result.Content)
	}

	if len(inner.calls) != 2 {
		t.Errorf("expected 2 calls to inner executor, got %d", len(inner.calls))
	}
}

// TestEvaluator_ProgrammaticInputMarshaling verifies bash_exec receives correct JSON input.
// newConcurrentTestEvaluatorReAct creates an evaluator wired for parallel llm_judge tests
// using a thread-safe LLM caller.
func newConcurrentTestEvaluatorReAct(llmCaller LLMCaller) *Evaluator {
	reg := tools.NewToolRegistry()
	counter := llm.NewTokenCounter("approximate")
	factory := func(systemPrompt string, modelMeta llm.ModelMetadata, compactionStrategy string) ContextManager {
		return &mockContextManager{systemPrompt: systemPrompt}
	}
	return NewEvaluator(
		nil,
		llmCaller,
		reg,
		counter,
		factory,
		nil,
		nil,
		ToolResultBudget{},
	)
}

// TestEvaluator_ParallelLLMJudge verifies that multiple llm_judge criteria
// run concurrently by checking that total wall-clock time is less than
// sequential execution would take.
func TestEvaluator_ParallelLLMJudge(t *testing.T) {
	const (
		numCriteria = 5
		delayPerCall = 50 * time.Millisecond
	)

	var inflight int64
	var maxInflight int64

	mockLLM := &concurrentSafeLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			cur := atomic.AddInt64(&inflight, 1)
			defer atomic.AddInt64(&inflight, -1)

			// Track max concurrent calls
			for {
				old := atomic.LoadInt64(&maxInflight)
				if cur <= old || atomic.CompareAndSwapInt64(&maxInflight, old, cur) {
					break
				}
			}

			time.Sleep(delayPerCall)
			return &llm.ChatResponse{
				Message:    llm.Message{Role: "assistant", Content: "YES, criterion met."},
				StopReason: "end_turn",
			}, nil
		},
	}

	evaluator := newConcurrentTestEvaluatorReAct(mockLLM)

	criteria := make([]AcceptanceCriterion, numCriteria)
	for i := range criteria {
		criteria[i] = AcceptanceCriterion{
			ID:          fmt.Sprintf("ac_%d", i),
			Description: fmt.Sprintf("Criterion %d", i),
			CheckType:   "llm_judge",
		}
	}

	bb := NewMapBlackboard()
	start := time.Now()
	evalResult, err := evaluator.Evaluate(context.Background(), "some result", criteria, bb)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(evalResult.Passed) != numCriteria {
		t.Errorf("expected %d passed, got %d", numCriteria, len(evalResult.Passed))
	}

	// If sequential, it would take >= numCriteria * delayPerCall = 250ms.
	// Parallel should complete much faster.
	sequentialMin := time.Duration(numCriteria) * delayPerCall
	if elapsed >= sequentialMin {
		t.Errorf("expected parallel execution to be faster than %v, took %v", sequentialMin, elapsed)
	}

	// Verify concurrency actually happened
	if atomic.LoadInt64(&maxInflight) < 2 {
		t.Errorf("expected at least 2 concurrent calls, max was %d", atomic.LoadInt64(&maxInflight))
	}
}

// TestEvaluator_ParallelPreservesDeterministicOrder verifies that results
// appear in original criteria order regardless of goroutine completion order.
func TestEvaluator_ParallelPreservesDeterministicOrder(t *testing.T) {
	// Criteria complete in reverse order via staggered delays.
	const numCriteria = 4

	mockLLM := &concurrentSafeLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			// Determine which criterion this is from the user message content
			var delay time.Duration
			for _, msg := range req.Messages {
				switch {
				case strings.Contains(msg.Content, "Criterion 0"):
					delay = 80 * time.Millisecond
				case strings.Contains(msg.Content, "Criterion 1"):
					delay = 60 * time.Millisecond
				case strings.Contains(msg.Content, "Criterion 2"):
					delay = 40 * time.Millisecond
				case strings.Contains(msg.Content, "Criterion 3"):
					delay = 20 * time.Millisecond
				}
			}
			time.Sleep(delay)
			return &llm.ChatResponse{
				Message:    llm.Message{Role: "assistant", Content: "YES, met."},
				StopReason: "end_turn",
			}, nil
		},
	}

	evaluator := newConcurrentTestEvaluatorReAct(mockLLM)

	criteria := make([]AcceptanceCriterion, numCriteria)
	for i := range criteria {
		criteria[i] = AcceptanceCriterion{
			ID:          fmt.Sprintf("ac_%d", i),
			Description: fmt.Sprintf("Criterion %d", i),
			CheckType:   "llm_judge",
		}
	}

	bb := NewMapBlackboard()
	evalResult, err := evaluator.Evaluate(context.Background(), "result", criteria, bb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(evalResult.Passed) != numCriteria {
		t.Fatalf("expected %d passed, got %d", numCriteria, len(evalResult.Passed))
	}

	// Verify order matches original criteria order, NOT completion order
	for i, detail := range evalResult.Passed {
		expectedID := fmt.Sprintf("ac_%d", i)
		if detail.Criterion.ID != expectedID {
			t.Errorf("passed[%d]: expected criterion ID %s, got %s", i, expectedID, detail.Criterion.ID)
		}
	}
}

// TestEvaluator_ParallelWithMixedTypes verifies that programmatic criteria
// run before parallel llm_judge criteria, and all results are correct.
func TestEvaluator_ParallelWithMixedTypes(t *testing.T) {
	mockTools := &mockToolExecutor{
		results: map[string]tools.ToolResult{
			"bash_exec": {Content: "ok", IsError: false},
		},
	}

	mockLLM := &concurrentSafeLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			// Return YES for llm_judge_1, NO for llm_judge_2
			for _, msg := range req.Messages {
				if strings.Contains(msg.Content, "LLM check 1") {
					return &llm.ChatResponse{
						Message:    llm.Message{Role: "assistant", Content: "YES, good."},
						StopReason: "end_turn",
					}, nil
				}
				if strings.Contains(msg.Content, "LLM check 2") {
					return &llm.ChatResponse{
						Message:    llm.Message{Role: "assistant", Content: "NO, bad."},
						StopReason: "end_turn",
					}, nil
				}
			}
			return &llm.ChatResponse{
				Message:    llm.Message{Role: "assistant", Content: "YES, default."},
				StopReason: "end_turn",
			}, nil
		},
	}

	reg := tools.NewToolRegistry()
	counter := llm.NewTokenCounter("approximate")
	factory := func(systemPrompt string, modelMeta llm.ModelMetadata, compactionStrategy string) ContextManager {
		return &mockContextManager{systemPrompt: systemPrompt}
	}
	evaluator := NewEvaluator(mockTools, mockLLM, reg, counter, factory, nil, nil, ToolResultBudget{})

	criteria := []AcceptanceCriterion{
		{ID: "prog_1", Description: "Programmatic", CheckType: "programmatic", CheckCmd: "echo ok"},
		{ID: "llm_1", Description: "LLM check 1", CheckType: "llm_judge"},
		{ID: "intent_1", Description: "Intent", CheckType: "intent_verification"},
		{ID: "llm_2", Description: "LLM check 2", CheckType: "llm_judge"},
	}

	bb := NewMapBlackboard()
	evalResult, err := evaluator.Evaluate(context.Background(), "result", criteria, bb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// prog_1 passes, llm_1 passes => 2 passed
	if len(evalResult.Passed) != 2 {
		t.Errorf("expected 2 passed, got %d", len(evalResult.Passed))
	}
	// llm_2 fails => 1 failed
	if len(evalResult.Failed) != 1 {
		t.Errorf("expected 1 failed, got %d", len(evalResult.Failed))
	}
	// intent_1 skipped => 1 unclear
	if len(evalResult.Unclear) != 1 {
		t.Errorf("expected 1 unclear, got %d", len(evalResult.Unclear))
	}

	if evalResult.AllPassed {
		t.Error("expected AllPassed to be false")
	}

	// Verify the failed one is llm_2
	if evalResult.Failed[0].Criterion.ID != "llm_2" {
		t.Errorf("expected failed criterion llm_2, got %s", evalResult.Failed[0].Criterion.ID)
	}
}

// TestEvaluator_ParallelErrorPropagation verifies that an error from one
// llm_judge criterion propagates correctly, returning the first error
// in criteria order.
func TestEvaluator_ParallelErrorPropagation(t *testing.T) {
	mockLLM := &concurrentSafeLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			for _, msg := range req.Messages {
				if strings.Contains(msg.Content, "Failing criterion") {
					return nil, errors.New("LLM call failed for failing criterion")
				}
			}
			return &llm.ChatResponse{
				Message:    llm.Message{Role: "assistant", Content: "YES, met."},
				StopReason: "end_turn",
			}, nil
		},
	}

	evaluator := newConcurrentTestEvaluatorReAct(mockLLM)

	criteria := []AcceptanceCriterion{
		{ID: "ac_ok", Description: "Good criterion", CheckType: "llm_judge"},
		{ID: "ac_fail", Description: "Failing criterion", CheckType: "llm_judge"},
		{ID: "ac_ok2", Description: "Another good criterion", CheckType: "llm_judge"},
	}

	bb := NewMapBlackboard()
	_, err := evaluator.Evaluate(context.Background(), "result", criteria, bb)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "failing criterion") {
		t.Errorf("expected error about failing criterion, got: %v", err)
	}
}

func TestEvaluator_ProgrammaticInputMarshaling(t *testing.T) {
	mockTools := &mockToolExecutor{
		results: map[string]tools.ToolResult{
			"bash_exec": {Content: "ok", IsError: false},
		},
	}

	evaluator := newTestEvaluator(mockTools, nil)

	criteria := []AcceptanceCriterion{{
		ID:        "ac_cmd",
		CheckType: "programmatic",
		CheckCmd:  "go test ./...",
	}}

	_, err := evaluator.Evaluate(context.Background(), "", criteria, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mockTools.calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(mockTools.calls))
	}
	if mockTools.calls[0] != "bash_exec" {
		t.Errorf("expected bash_exec, got %s", mockTools.calls[0])
	}

	var input map[string]string
	if err := json.Unmarshal(mockTools.inputs[0], &input); err != nil {
		t.Fatalf("failed to unmarshal tool input: %v", err)
	}
	if input["command"] != "go test ./..." {
		t.Errorf("expected command 'go test ./...', got %q", input["command"])
	}
}
