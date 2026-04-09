package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/user/agent/sdk/llm"
	tools "github.com/user/agent/sdk/tools"
)

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
	counter, _ := llm.NewTokenCounter("approximate")
	factory := func(systemPrompt string, modelMeta llm.ModelMetadata, compactionStrategy string) ContextManager {
		return &mockContextManager{systemPrompt: systemPrompt}
	}
	return NewEvaluator(
		nil, // ToolExecutor (not needed for llm_judge)
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
	// Mock LLM returns batch JSON response.
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{{
			Message: llm.Message{
				Role:    "assistant",
				Content: `[{"criterion_id":"ac_2","verdict":"YES","explanation":"the code implements proper error handling."}]`,
			},
			StopReason: "end_turn",
		}},
	}

	evaluator := newTestEvaluator(nil, mockLLM)

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
				Content: `[{"criterion_id":"ac_2","verdict":"NO","explanation":"there is no error handling."}]`,
			},
			StopReason: "end_turn",
		}},
	}

	evaluator := newTestEvaluator(nil, mockLLM)

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
	// Batch response with unknown verdict maps to UNCLEAR.
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{{
			Message: llm.Message{
				Role:    "assistant",
				Content: `[{"criterion_id":"ac_1","verdict":"MAYBE","explanation":"cannot determine without more context"}]`,
			},
			StopReason: "end_turn",
		}},
	}

	evaluator := newTestEvaluator(nil, mockLLM)

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

	// LLM returns NO for the llm_judge criterion via batch response
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{{
			Message: llm.Message{
				Role:    "assistant",
				Content: `[{"criterion_id":"ac_2","verdict":"NO","explanation":"the documentation is incomplete."}]`,
			},
			StopReason: "end_turn",
		}},
	}

	evaluator := NewEvaluator(mockTools, mockLLM, nil, nil, nil, nil, nil, ToolResultBudget{})

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
				Content: `[{"criterion_id":"ac_2","verdict":"YES","explanation":"the code quality is excellent."}]`,
			},
			StopReason: "end_turn",
		}},
	}

	evaluator := NewEvaluator(mockTools, mockLLM, nil, nil, nil, nil, nil, ToolResultBudget{})

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

// TestEvaluator_BatchEvidencePrefetch verifies that evidence from blackboard
// is included in the batch LLM call.
func TestEvaluator_BatchEvidencePrefetch(t *testing.T) {
	var capturedReq llm.ChatRequest

	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			capturedReq = req
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:    "assistant",
					Content: `[{"criterion_id":"ac_bb","verdict":"YES","explanation":"evidence confirms it."}]`,
				},
				StopReason: "end_turn",
			}, nil
		},
	}

	evaluator := newTestEvaluator(nil, mockLLM)

	criteria := []AcceptanceCriterion{{
		ID:          "ac_bb",
		Description: "Test blackboard evidence",
		CheckType:   "llm_judge",
	}}

	bb := NewMapBlackboard()
	bb.SetStepResult("step_1", "some output from step 1", nil, nil)

	_, err := evaluator.Evaluate(context.Background(), "result", criteria, bb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify evidence is included in the user message
	if len(capturedReq.Messages) < 2 {
		t.Fatal("expected at least 2 messages in LLM request")
	}
	userMsg := capturedReq.Messages[1].Content
	if !strings.Contains(userMsg, "step_1") {
		t.Error("expected step_1 ID in evidence summary")
	}
	if !strings.Contains(userMsg, "some output from step 1") {
		t.Error("expected step_1 output in evidence summary")
	}
}

// TestEvaluator_BatchSingleLLMCall verifies that the batch evaluator makes
// exactly one LLM call for all llm_judge criteria.
func TestEvaluator_BatchSingleLLMCall(t *testing.T) {
	callCount := 0
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callCount++
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:    "assistant",
					Content: `[{"criterion_id":"ac_1","verdict":"NO","explanation":"not met"},{"criterion_id":"ac_2","verdict":"YES","explanation":"met"}]`,
				},
				StopReason: "end_turn",
			}, nil
		},
	}

	evaluator := newTestEvaluator(nil, mockLLM)

	criteria := []AcceptanceCriterion{
		{ID: "ac_1", Description: "Tests must pass", CheckType: "llm_judge"},
		{ID: "ac_2", Description: "Code quality", CheckType: "llm_judge"},
	}

	bb := NewMapBlackboard()
	bb.SetStepResult("step_1", "some evidence", nil, nil)

	evalResult, err := evaluator.Evaluate(context.Background(), "test output", criteria, bb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Batch evaluator should make exactly 1 LLM call for all criteria.
	if callCount != 1 {
		t.Errorf("expected exactly 1 LLM call for batch evaluation, got %d", callCount)
	}

	if len(evalResult.Failed) != 1 {
		t.Errorf("expected 1 failed, got %d", len(evalResult.Failed))
	}
	if len(evalResult.Passed) != 1 {
		t.Errorf("expected 1 passed, got %d", len(evalResult.Passed))
	}
	if evalResult.AllPassed {
		t.Error("expected AllPassed to be false")
	}
}

func TestParseEvalVerdict(t *testing.T) {
	tests := []struct {
		name         string
		output       string
		expectPrefix string
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

// TestEvaluator_BatchResultTruncation verifies that long results are truncated
// in the batch evaluator prompt.
func TestEvaluator_ResultTruncation(t *testing.T) {
	var capturedReq llm.ChatRequest
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			capturedReq = req
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:    "assistant",
					Content: `[{"criterion_id":"ac_trunc","verdict":"YES","explanation":"all good."}]`,
				},
				StopReason: "end_turn",
			}, nil
		},
	}

	evaluator := newTestEvaluator(nil, mockLLM)

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

	if len(capturedReq.Messages) < 2 {
		t.Fatal("expected at least 2 messages in LLM request")
	}

	// Find the user message containing the task
	userMsg := capturedReq.Messages[1].Content
	if !strings.Contains(userMsg, "Test truncation") {
		t.Error("expected to find criterion description in LLM request")
	}
	// Should contain "..." indicating truncation
	if !strings.Contains(userMsg, "...") {
		t.Error("expected truncated result to contain '...'")
	}
	// Should NOT contain the full 3000-char string
	if strings.Contains(userMsg, longResult) {
		t.Error("expected result to be truncated, but full result was found")
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

// TestEvaluator_BatchMultipleCriteria verifies that multiple llm_judge criteria
// are evaluated in a single batch call and results preserve criteria order.
func TestEvaluator_BatchMultipleCriteria(t *testing.T) {
	const numCriteria = 5

	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			// Build a response for all criteria
			var results []string
			for i := 0; i < numCriteria; i++ {
				results = append(results, fmt.Sprintf(`{"criterion_id":"ac_%d","verdict":"YES","explanation":"criterion %d met"}`, i, i))
			}
			return &llm.ChatResponse{
				Message:    llm.Message{Role: "assistant", Content: "[" + strings.Join(results, ",") + "]"},
				StopReason: "end_turn",
			}, nil
		},
	}

	evaluator := newTestEvaluator(nil, mockLLM)

	criteria := make([]AcceptanceCriterion, numCriteria)
	for i := range criteria {
		criteria[i] = AcceptanceCriterion{
			ID:          fmt.Sprintf("ac_%d", i),
			Description: fmt.Sprintf("Criterion %d", i),
			CheckType:   "llm_judge",
		}
	}

	bb := NewMapBlackboard()
	evalResult, err := evaluator.Evaluate(context.Background(), "some result", criteria, bb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(evalResult.Passed) != numCriteria {
		t.Errorf("expected %d passed, got %d", numCriteria, len(evalResult.Passed))
	}

	// Verify order matches original criteria order
	for i, detail := range evalResult.Passed {
		expectedID := fmt.Sprintf("ac_%d", i)
		if detail.Criterion.ID != expectedID {
			t.Errorf("passed[%d]: expected criterion ID %s, got %s", i, expectedID, detail.Criterion.ID)
		}
	}
}

// TestEvaluator_BatchWithMixedTypes verifies that programmatic criteria
// run before batch llm_judge evaluation, and all results are correct.
func TestEvaluator_BatchWithMixedTypes(t *testing.T) {
	mockTools := &mockToolExecutor{
		results: map[string]tools.ToolResult{
			"bash_exec": {Content: "ok", IsError: false},
		},
	}

	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:    "assistant",
					Content: `[{"criterion_id":"llm_1","verdict":"YES","explanation":"good."},{"criterion_id":"llm_2","verdict":"NO","explanation":"bad."}]`,
				},
				StopReason: "end_turn",
			}, nil
		},
	}

	evaluator := NewEvaluator(mockTools, mockLLM, nil, nil, nil, nil, nil, ToolResultBudget{})

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

// TestEvaluator_BatchErrorPropagation verifies that an LLM error from the
// batch call propagates correctly.
func TestEvaluator_BatchErrorPropagation(t *testing.T) {
	mockLLM := &mockLLMCaller{
		err: errors.New("batch LLM call failed"),
	}

	evaluator := newTestEvaluator(nil, mockLLM)

	criteria := []AcceptanceCriterion{
		{ID: "ac_ok", Description: "Good criterion", CheckType: "llm_judge"},
		{ID: "ac_ok2", Description: "Another good criterion", CheckType: "llm_judge"},
	}

	bb := NewMapBlackboard()
	_, err := evaluator.Evaluate(context.Background(), "result", criteria, bb)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "batch LLM call failed") {
		t.Errorf("expected error about batch LLM call, got: %v", err)
	}
}

// TestEvaluator_BatchMalformedJSON verifies that malformed JSON in the batch
// response marks all criteria as UNCLEAR.
func TestEvaluator_BatchMalformedJSON(t *testing.T) {
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{{
			Message:    llm.Message{Role: "assistant", Content: "This is not JSON at all"},
			StopReason: "end_turn",
		}},
	}

	evaluator := newTestEvaluator(nil, mockLLM)

	criteria := []AcceptanceCriterion{
		{ID: "ac_1", Description: "First", CheckType: "llm_judge"},
		{ID: "ac_2", Description: "Second", CheckType: "llm_judge"},
	}

	bb := NewMapBlackboard()
	evalResult, err := evaluator.Evaluate(context.Background(), "result", criteria, bb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(evalResult.Unclear) != 2 {
		t.Errorf("expected 2 unclear, got %d", len(evalResult.Unclear))
	}
	for _, d := range evalResult.Unclear {
		if !strings.Contains(d.Diagnostic, "failed to parse batch response") {
			t.Errorf("expected parse error diagnostic, got %q", d.Diagnostic)
		}
	}
}

// TestEvaluator_BatchMissingCriterion verifies that if the LLM response
// omits a criterion, it's marked UNCLEAR.
func TestEvaluator_BatchMissingCriterion(t *testing.T) {
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{{
			Message: llm.Message{
				Role:    "assistant",
				Content: `[{"criterion_id":"ac_1","verdict":"YES","explanation":"met"}]`,
			},
			StopReason: "end_turn",
		}},
	}

	evaluator := newTestEvaluator(nil, mockLLM)

	criteria := []AcceptanceCriterion{
		{ID: "ac_1", Description: "First", CheckType: "llm_judge"},
		{ID: "ac_2", Description: "Second (missing from response)", CheckType: "llm_judge"},
	}

	bb := NewMapBlackboard()
	evalResult, err := evaluator.Evaluate(context.Background(), "result", criteria, bb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(evalResult.Passed) != 1 {
		t.Errorf("expected 1 passed, got %d", len(evalResult.Passed))
	}
	if len(evalResult.Unclear) != 1 {
		t.Errorf("expected 1 unclear, got %d", len(evalResult.Unclear))
	}
	if evalResult.Unclear[0].Criterion.ID != "ac_2" {
		t.Errorf("expected ac_2 to be unclear, got %s", evalResult.Unclear[0].Criterion.ID)
	}
	if !strings.Contains(evalResult.Unclear[0].Diagnostic, "missing from batch response") {
		t.Errorf("expected missing criterion diagnostic, got %q", evalResult.Unclear[0].Diagnostic)
	}
}

// TestEvaluator_BatchCodeFenceStripping verifies that JSON wrapped in
// markdown code fences is parsed correctly.
func TestEvaluator_BatchCodeFenceStripping(t *testing.T) {
	response := "```json\n" +
		`[{"criterion_id":"ac_1","verdict":"YES","explanation":"met"}]` +
		"\n```"

	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{{
			Message:    llm.Message{Role: "assistant", Content: response},
			StopReason: "end_turn",
		}},
	}

	evaluator := newTestEvaluator(nil, mockLLM)

	criteria := []AcceptanceCriterion{
		{ID: "ac_1", Description: "First", CheckType: "llm_judge"},
	}

	bb := NewMapBlackboard()
	evalResult, err := evaluator.Evaluate(context.Background(), "result", criteria, bb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(evalResult.Passed) != 1 {
		t.Errorf("expected 1 passed, got %d", len(evalResult.Passed))
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
