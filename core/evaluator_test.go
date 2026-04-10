package core

import (
	"context"
	"encoding/json"
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

// ---------------------------------------------------------------------------
// Test tool stubs (avoid importing core/coretools which creates an import cycle)
// ---------------------------------------------------------------------------

// testVerdictTool is a minimal report_verdict tool for testing.
// It reads the blackboard from context and records the verdict.
type testVerdictTool struct {
	*tools.BaseTool
}

func newTestVerdictTool() *testVerdictTool {
	schema := json.RawMessage(`{
	"type":"object",
	"properties":{
		"criterion_id":{"type":"string"},
		"verdict":{"type":"string","enum":["YES","NO"]},
		"explanation":{"type":"string"}
	},
	"required":["criterion_id","verdict","explanation"]
}`)
	return &testVerdictTool{
		BaseTool: &tools.BaseTool{
			ToolName:        "report_verdict",
			ToolDescription: "Record an evaluation verdict for an acceptance criterion",
			Schema:          schema,
			Policy:          tools.PolicyAlwaysAllow,
		},
	}
}

func (t *testVerdictTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	bb := BlackboardFromContext(ctx)
	if bb == nil {
		return tools.ToolResult{Content: "blackboard not available", IsError: true}, nil
	}
	var params struct {
		CriterionID string `json:"criterion_id"`
		Verdict     string `json:"verdict"`
		Explanation string `json:"explanation"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return tools.ParseInputError(err)
	}
	bb.SetEvalVerdict(params.CriterionID, strings.ToUpper(params.Verdict), params.Explanation)
	return tools.ToolResult{Content: fmt.Sprintf("Verdict recorded for %s: %s", params.CriterionID, params.Verdict)}, nil
}

// testEvidenceTool is a minimal read_evidence tool stub for testing.
type testEvidenceTool struct {
	*tools.BaseTool
}

func newTestEvidenceTool() *testEvidenceTool {
	schema := json.RawMessage(`{
	"type":"object",
	"properties":{
		"list":{"type":"boolean"}
	}
}`)
	return &testEvidenceTool{
		BaseTool: &tools.BaseTool{
			ToolName:        "read_evidence",
			ToolDescription: "Read evidence from the blackboard",
			Schema:          schema,
			Policy:          tools.PolicyAlwaysAllow,
		},
	}
}

func (t *testEvidenceTool) Execute(ctx context.Context, _ json.RawMessage) (tools.ToolResult, error) {
	return tools.ToolResult{Content: "no step results available"}, nil
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

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
// It registers the report_verdict and read_evidence tools so the executor
// ReAct loop can dispatch tool calls from the mock LLM.
func newTestEvaluatorReAct(llmCaller LLMCaller) *Evaluator {
	reg := tools.NewToolRegistry()
	reg.Register(newTestVerdictTool())
	reg.Register(newTestEvidenceTool())

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

// ---------------------------------------------------------------------------
// Programmatic tests (kept unchanged)
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// Filter and ReadOnly tests (kept unchanged)
// ---------------------------------------------------------------------------

func TestFilterEvaluatorTools(t *testing.T) {
	allTools := []tools.ToolDescriptor{
		{Name: "file_ops"},
		{Name: "ripgrep"},
		{Name: "glob"},
		{Name: "read_evidence"},
		{Name: "report_verdict"},
		{Name: "bash_exec"},
		{Name: "web_search"},
	}

	filtered := filterEvaluatorTools(allTools)

	if len(filtered) != 5 {
		t.Errorf("expected 5 filtered tools, got %d", len(filtered))
	}

	names := make(map[string]bool)
	for _, td := range filtered {
		names[td.Name] = true
	}

	for expected := range evaluatorToolAllowlist {
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

func TestFilterEvaluatorTools_FileOpsReadOnly(t *testing.T) {
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

	for _, v := range enumVals {
		action, ok := v.(string)
		if !ok {
			continue
		}
		if fileOpsWriteActions[action] {
			t.Errorf("write action %q should not be in filtered schema", action)
		}
	}

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

	input, _ := json.Marshal(map[string]string{"pattern": "test"})
	result, err := ro.Execute(context.Background(), "ripgrep", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "grep results" {
		t.Errorf("expected 'grep results', got %q", result.Content)
	}

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

// ---------------------------------------------------------------------------
// ReAct evaluator tests (llm_judge via per-criterion agent)
// ---------------------------------------------------------------------------

// TestEvaluator_ReActVerdictViaBlackboard tests the happy path: LLM produces
// a report_verdict tool call, verdict ends up in Blackboard, returned as EvalDetail.
func TestEvaluator_ReActVerdictViaBlackboard(t *testing.T) {
	callCount := 0
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callCount++
			if callCount == 1 {
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: "",
						ToolCalls: []llm.ToolCall{
							{
								ID:    "call_1",
								Name:  "report_verdict",
								Input: json.RawMessage(`{"criterion_id":"ac_1","verdict":"YES","explanation":"evidence confirms it"}`),
							},
						},
					},
					StopReason: "tool_use",
				}, nil
			}
			return &llm.ChatResponse{
				Message:    llm.Message{Role: "assistant", Content: "Done."},
				StopReason: "end_turn",
			}, nil
		},
	}

	evaluator := newTestEvaluatorReAct(mockLLM)

	criteria := []AcceptanceCriterion{
		{ID: "ac_1", Description: "Test criterion", CheckType: "llm_judge"},
	}

	bb := NewMapBlackboard()
	evalResult, err := evaluator.Evaluate(context.Background(), "test result", criteria, bb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(evalResult.Passed) != 1 {
		t.Errorf("expected 1 passed, got %d", len(evalResult.Passed))
	}
	if evalResult.Passed[0].Criterion.ID != "ac_1" {
		t.Errorf("expected ac_1, got %s", evalResult.Passed[0].Criterion.ID)
	}
	if !strings.Contains(evalResult.Passed[0].Diagnostic, "evidence confirms it") {
		t.Errorf("expected explanation in diagnostic, got %q", evalResult.Passed[0].Diagnostic)
	}
}

// TestEvaluator_ReActPerCriterionFreshContext tests that each criterion gets
// its own executor run (LLM called separately for each).
func TestEvaluator_ReActPerCriterionFreshContext(t *testing.T) {
	criterionSeen := make(map[string]bool)
	callIdx := 0

	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callIdx++

			for _, msg := range req.Messages {
				if strings.Contains(msg.Content, "ac_1") {
					criterionSeen["ac_1"] = true
				}
				if strings.Contains(msg.Content, "ac_2") {
					criterionSeen["ac_2"] = true
				}
			}

			if callIdx%2 == 1 {
				criterionID := "ac_1"
				if callIdx > 2 {
					criterionID = "ac_2"
				}
				return &llm.ChatResponse{
					Message: llm.Message{
						Role: "assistant",
						ToolCalls: []llm.ToolCall{{
							ID:   fmt.Sprintf("call_%d", callIdx),
							Name: "report_verdict",
							Input: json.RawMessage(fmt.Sprintf(
								`{"criterion_id":%q,"verdict":"YES","explanation":"met"}`, criterionID)),
						}},
					},
					StopReason: "tool_use",
				}, nil
			}
			return &llm.ChatResponse{
				Message:    llm.Message{Role: "assistant", Content: "Done."},
				StopReason: "end_turn",
			}, nil
		},
	}

	evaluator := newTestEvaluatorReAct(mockLLM)

	criteria := []AcceptanceCriterion{
		{ID: "ac_1", Description: "First criterion", CheckType: "llm_judge"},
		{ID: "ac_2", Description: "Second criterion", CheckType: "llm_judge"},
	}

	bb := NewMapBlackboard()
	evalResult, err := evaluator.Evaluate(context.Background(), "test", criteria, bb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(evalResult.Passed) != 2 {
		t.Errorf("expected 2 passed, got %d (failed=%d, unclear=%d)",
			len(evalResult.Passed), len(evalResult.Failed), len(evalResult.Unclear))
	}

	if !criterionSeen["ac_1"] {
		t.Error("expected LLM calls for ac_1")
	}
	if !criterionSeen["ac_2"] {
		t.Error("expected LLM calls for ac_2")
	}
}

// TestEvaluator_ReActAllCriteriaEvaluated verifies all criteria get evaluated
// even when some fail.
func TestEvaluator_ReActAllCriteriaEvaluated(t *testing.T) {
	callIdx := 0
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callIdx++
			if callIdx%2 == 1 {
				criterionID := "ac_1"
				verdict := "NO"
				if callIdx > 2 {
					criterionID = "ac_2"
					verdict = "YES"
				}
				return &llm.ChatResponse{
					Message: llm.Message{
						Role: "assistant",
						ToolCalls: []llm.ToolCall{{
							ID:   fmt.Sprintf("call_%d", callIdx),
							Name: "report_verdict",
							Input: json.RawMessage(fmt.Sprintf(
								`{"criterion_id":%q,"verdict":%q,"explanation":"reason"}`, criterionID, verdict)),
						}},
					},
					StopReason: "tool_use",
				}, nil
			}
			return &llm.ChatResponse{
				Message:    llm.Message{Role: "assistant", Content: "Done."},
				StopReason: "end_turn",
			}, nil
		},
	}

	evaluator := newTestEvaluatorReAct(mockLLM)
	criteria := []AcceptanceCriterion{
		{ID: "ac_1", Description: "Fails", CheckType: "llm_judge"},
		{ID: "ac_2", Description: "Passes", CheckType: "llm_judge"},
	}

	bb := NewMapBlackboard()
	evalResult, err := evaluator.Evaluate(context.Background(), "test", criteria, bb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
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

// TestEvaluator_ReActNoContextFactory verifies that when contextFactory is nil,
// llm_judge criteria are marked UNCLEAR with a "dependencies not configured" diagnostic.
func TestEvaluator_ReActNoContextFactory(t *testing.T) {
	evaluator := newTestEvaluator(nil, &mockLLMCaller{})

	criteria := []AcceptanceCriterion{
		{ID: "ac_1", Description: "Test", CheckType: "llm_judge"},
	}

	bb := NewMapBlackboard()
	evalResult, err := evaluator.Evaluate(context.Background(), "result", criteria, bb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(evalResult.Unclear) != 1 {
		t.Fatalf("expected 1 unclear, got %d", len(evalResult.Unclear))
	}
	if !strings.Contains(evalResult.Unclear[0].Diagnostic, "dependencies not configured") {
		t.Errorf("expected deps not configured diagnostic, got %q", evalResult.Unclear[0].Diagnostic)
	}
}

// TestEvaluator_ReActMixedTypes tests evaluation with a mix of programmatic
// and llm_judge criteria.
func TestEvaluator_ReActMixedTypes(t *testing.T) {
	mockTools := &mockToolExecutor{
		results: map[string]tools.ToolResult{
			"bash_exec": {Content: "ok", IsError: false},
		},
	}

	callIdx := 0
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callIdx++
			if callIdx == 1 {
				return &llm.ChatResponse{
					Message: llm.Message{
						Role: "assistant",
						ToolCalls: []llm.ToolCall{{
							ID:    "call_1",
							Name:  "report_verdict",
							Input: json.RawMessage(`{"criterion_id":"llm_1","verdict":"NO","explanation":"not met"}`),
						}},
					},
					StopReason: "tool_use",
				}, nil
			}
			return &llm.ChatResponse{
				Message:    llm.Message{Role: "assistant", Content: "Done."},
				StopReason: "end_turn",
			}, nil
		},
	}

	reg := tools.NewToolRegistry()
	reg.Register(newTestVerdictTool())
	reg.Register(newTestEvidenceTool())
	counter, _ := llm.NewTokenCounter("approximate")
	factory := func(sp string, mm llm.ModelMetadata, cs string) ContextManager {
		return &mockContextManager{systemPrompt: sp}
	}

	evaluator := NewEvaluator(mockTools, mockLLM, reg, counter, factory, nil, nil, ToolResultBudget{})

	criteria := []AcceptanceCriterion{
		{ID: "prog_1", Description: "Programmatic", CheckType: "programmatic", CheckCmd: "echo ok"},
		{ID: "llm_1", Description: "LLM check", CheckType: "llm_judge"},
	}

	bb := NewMapBlackboard()
	evalResult, err := evaluator.Evaluate(context.Background(), "result", criteria, bb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(evalResult.Passed) != 1 {
		t.Errorf("expected 1 passed (programmatic), got %d", len(evalResult.Passed))
	}
	if len(evalResult.Failed) != 1 {
		t.Errorf("expected 1 failed (llm), got %d", len(evalResult.Failed))
	}
	if evalResult.AllPassed {
		t.Error("expected AllPassed to be false")
	}
}

// TestEvaluator_ReActMissingVerdict tests that when the agent finishes without
// calling report_verdict, the criterion is marked UNCLEAR.
func TestEvaluator_ReActMissingVerdict(t *testing.T) {
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{{
			Message:    llm.Message{Role: "assistant", Content: "I don't know."},
			StopReason: "end_turn",
		}},
	}

	evaluator := newTestEvaluatorReAct(mockLLM)

	criteria := []AcceptanceCriterion{
		{ID: "ac_1", Description: "Test", CheckType: "llm_judge"},
	}

	bb := NewMapBlackboard()
	evalResult, err := evaluator.Evaluate(context.Background(), "result", criteria, bb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(evalResult.Unclear) != 1 {
		t.Fatalf("expected 1 unclear, got %d", len(evalResult.Unclear))
	}
	if !strings.Contains(evalResult.Unclear[0].Diagnostic, "no verdict reported") {
		t.Errorf("expected 'no verdict reported' diagnostic, got %q", evalResult.Unclear[0].Diagnostic)
	}
}
