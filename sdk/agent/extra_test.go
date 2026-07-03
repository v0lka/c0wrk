package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/sdk/llm"
	"github.com/v0lka/c0wrk/sdk/tools"
)

// ============================================================================
// Fruitless Result Detector: OnStepLimit paths for abort
// ============================================================================

func TestExecutor_Run_FruitlessDetector_Abort_StepLimitAllowOnce(t *testing.T) {
	fruitlessConfig := CircuitBreakerConfig{
		RepeatNudgeThreshold:         50,
		RepeatAbortThreshold:         100,
		TruncationAbortThreshold:     100,
		ParseErrorAbortThreshold:     100,
		FruitlessNudgeThreshold:      5,
		FruitlessAbortThreshold:      8,
		FruitlessMaxResultLen:        32,
		SameToolRepeatNudgeThreshold: 50,
		SameToolRepeatAbortThreshold: 100,
		SameToolResultSizeDelta:      64,
	}

	responses := make([]*llm.ChatResponse, 10)
	for i := 0; i < 9; i++ {
		responses[i] = llmResponseWithToolCall(
			fmt.Sprintf("search %d", i+1),
			"search",
			json.RawMessage(fmt.Sprintf(`{"q":"test%d"}`, i+1)),
		)
	}
	responses[9] = llmResponseFinish("done", "completed")

	mockLLM := &mockLLMCaller{responses: responses}
	mockTools := newMockToolExecutor()
	mockTools.results["search"] = tools.ToolResult{Content: "short", IsError: false}

	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 20, nil, false, ToolResultBudget{}, fruitlessConfig)

	callCount := 0
	exec.SetHITLHandler(&testStepLimitAdapter{fn: func(ctx context.Context, currentStep int, maxSteps int, reason string) (StepLimitResponse, error) {
		callCount++
		return StepLimitAllowOnce, nil
	}})

	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "search", Description: "search", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Finished {
		t.Error("expected Finished=true after fruitless abort → AllowOnce → finish")
	}
	if callCount != 1 {
		t.Errorf("expected OnStepLimit to be called once, got %d", callCount)
	}
}

func TestExecutor_Run_FruitlessDetector_Abort_StepLimitAllowAlways(t *testing.T) {
	fruitlessConfig := CircuitBreakerConfig{
		RepeatNudgeThreshold:         50,
		RepeatAbortThreshold:         100,
		TruncationAbortThreshold:     100,
		ParseErrorAbortThreshold:     100,
		FruitlessNudgeThreshold:      5,
		FruitlessAbortThreshold:      8,
		FruitlessMaxResultLen:        32,
		SameToolRepeatNudgeThreshold: 50,
		SameToolRepeatAbortThreshold: 100,
		SameToolResultSizeDelta:      64,
	}

	responses := make([]*llm.ChatResponse, 10)
	for i := 0; i < 9; i++ {
		responses[i] = llmResponseWithToolCall(
			fmt.Sprintf("search %d", i+1),
			"search",
			json.RawMessage(fmt.Sprintf(`{"q":"test%d"}`, i+1)),
		)
	}
	responses[9] = llmResponseFinish("done", "completed")

	mockLLM := &mockLLMCaller{responses: responses}
	mockTools := newMockToolExecutor()
	mockTools.results["search"] = tools.ToolResult{Content: "short", IsError: false}

	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 20, nil, false, ToolResultBudget{}, fruitlessConfig)

	callCount := 0
	exec.SetHITLHandler(&testStepLimitAdapter{fn: func(ctx context.Context, currentStep int, maxSteps int, reason string) (StepLimitResponse, error) {
		callCount++
		return StepLimitAllowAlways, nil
	}})

	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "search", Description: "search", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Finished {
		t.Error("expected Finished=true after fruitless abort → AllowAlways → finish")
	}
	if callCount != 1 {
		t.Errorf("expected OnStepLimit to be called once, got %d", callCount)
	}
}

func TestExecutor_Run_FruitlessDetector_Abort_StepLimitDeny(t *testing.T) {
	fruitlessConfig := CircuitBreakerConfig{
		RepeatNudgeThreshold:         50,
		RepeatAbortThreshold:         100,
		TruncationAbortThreshold:     100,
		ParseErrorAbortThreshold:     100,
		FruitlessNudgeThreshold:      5,
		FruitlessAbortThreshold:      8,
		FruitlessMaxResultLen:        32,
		SameToolRepeatNudgeThreshold: 50,
		SameToolRepeatAbortThreshold: 100,
		SameToolResultSizeDelta:      64,
	}

	responses := make([]*llm.ChatResponse, 15)
	for i := 0; i < 15; i++ {
		responses[i] = llmResponseWithToolCall(
			fmt.Sprintf("search %d", i+1),
			"search",
			json.RawMessage(fmt.Sprintf(`{"q":"test%d"}`, i+1)),
		)
	}

	mockLLM := &mockLLMCaller{responses: responses}
	mockTools := newMockToolExecutor()
	mockTools.results["search"] = tools.ToolResult{Content: "short", IsError: false}

	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 20, nil, false, ToolResultBudget{}, fruitlessConfig)

	exec.SetHITLHandler(&testStepLimitAdapter{fn: func(ctx context.Context, currentStep int, maxSteps int, reason string) (StepLimitResponse, error) {
		return StepLimitDeny, nil
	}})

	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "search", Description: "search", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Finished {
		t.Error("expected Finished=false after fruitless abort → Deny")
	}
	if !strings.Contains(result.Output, "empty or minimal results") {
		t.Errorf("expected fruitless abort message, got %q", result.Output)
	}
}

// ============================================================================
// SameTool Repetition: OnStepLimit paths for abort
// ============================================================================

func TestExecutor_Run_SameToolRepeat_Abort_StepLimitAllowOnce(t *testing.T) {
	sameToolConfig := CircuitBreakerConfig{
		RepeatNudgeThreshold:         50,
		RepeatAbortThreshold:         100,
		TruncationAbortThreshold:     100,
		ParseErrorAbortThreshold:     100,
		FruitlessNudgeThreshold:      50,
		FruitlessAbortThreshold:      100,
		FruitlessMaxResultLen:        32,
		SameToolRepeatNudgeThreshold: 8,
		SameToolRepeatAbortThreshold: 12,
		SameToolResultSizeDelta:      64,
	}

	responses := make([]*llm.ChatResponse, 14)
	for i := 0; i < 13; i++ {
		responses[i] = llmResponseWithToolCall(
			fmt.Sprintf("search %d", i+1),
			"search",
			json.RawMessage(fmt.Sprintf(`{"q":"query%d"}`, i+1)),
		)
	}
	responses[13] = llmResponseFinish("done", "completed")

	mockLLM := &mockLLMCaller{responses: responses}
	mockTools := newMockToolExecutor()
	mockTools.results["search"] = tools.ToolResult{Content: strings.Repeat("x", 50), IsError: false}

	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 20, nil, false, ToolResultBudget{}, sameToolConfig)

	callCount := 0
	exec.SetHITLHandler(&testStepLimitAdapter{fn: func(ctx context.Context, currentStep int, maxSteps int, reason string) (StepLimitResponse, error) {
		callCount++
		return StepLimitAllowOnce, nil
	}})

	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "search", Description: "search", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Finished {
		t.Error("expected Finished=true after same-tool abort → AllowOnce → finish")
	}
	if callCount != 1 {
		t.Errorf("expected OnStepLimit to be called once, got %d", callCount)
	}
}

func TestExecutor_Run_SameToolRepeat_Abort_StepLimitAllowAlways(t *testing.T) {
	sameToolConfig := CircuitBreakerConfig{
		RepeatNudgeThreshold:         50,
		RepeatAbortThreshold:         100,
		TruncationAbortThreshold:     100,
		ParseErrorAbortThreshold:     100,
		FruitlessNudgeThreshold:      50,
		FruitlessAbortThreshold:      100,
		FruitlessMaxResultLen:        32,
		SameToolRepeatNudgeThreshold: 8,
		SameToolRepeatAbortThreshold: 12,
		SameToolResultSizeDelta:      64,
	}

	responses := make([]*llm.ChatResponse, 14)
	for i := 0; i < 13; i++ {
		responses[i] = llmResponseWithToolCall(
			fmt.Sprintf("search %d", i+1),
			"search",
			json.RawMessage(fmt.Sprintf(`{"q":"query%d"}`, i+1)),
		)
	}
	responses[13] = llmResponseFinish("done", "completed")

	mockLLM := &mockLLMCaller{responses: responses}
	mockTools := newMockToolExecutor()
	mockTools.results["search"] = tools.ToolResult{Content: strings.Repeat("x", 50), IsError: false}

	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 20, nil, false, ToolResultBudget{}, sameToolConfig)

	callCount := 0
	exec.SetHITLHandler(&testStepLimitAdapter{fn: func(ctx context.Context, currentStep int, maxSteps int, reason string) (StepLimitResponse, error) {
		callCount++
		return StepLimitAllowAlways, nil
	}})

	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "search", Description: "search", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Finished {
		t.Error("expected Finished=true after same-tool abort → AllowAlways → finish")
	}
	if callCount != 1 {
		t.Errorf("expected OnStepLimit to be called once, got %d", callCount)
	}
}

func TestExecutor_Run_SameToolRepeat_Abort_StepLimitDeny(t *testing.T) {
	sameToolConfig := CircuitBreakerConfig{
		RepeatNudgeThreshold:         50,
		RepeatAbortThreshold:         100,
		TruncationAbortThreshold:     100,
		ParseErrorAbortThreshold:     100,
		FruitlessNudgeThreshold:      50,
		FruitlessAbortThreshold:      100,
		FruitlessMaxResultLen:        32,
		SameToolRepeatNudgeThreshold: 8,
		SameToolRepeatAbortThreshold: 12,
		SameToolResultSizeDelta:      64,
	}

	responses := make([]*llm.ChatResponse, 15)
	for i := 0; i < 15; i++ {
		responses[i] = llmResponseWithToolCall(
			fmt.Sprintf("search %d", i+1),
			"search",
			json.RawMessage(fmt.Sprintf(`{"q":"query%d"}`, i+1)),
		)
	}

	mockLLM := &mockLLMCaller{responses: responses}
	mockTools := newMockToolExecutor()
	mockTools.results["search"] = tools.ToolResult{Content: strings.Repeat("x", 50), IsError: false}

	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 20, nil, false, ToolResultBudget{}, sameToolConfig)

	exec.SetHITLHandler(&testStepLimitAdapter{fn: func(ctx context.Context, currentStep int, maxSteps int, reason string) (StepLimitResponse, error) {
		return StepLimitDeny, nil
	}})

	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "search", Description: "search", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Finished {
		t.Error("expected Finished=false after same-tool abort → Deny")
	}
	if !strings.Contains(result.Output, "similar results") {
		t.Errorf("expected same-tool abort message, got %q", result.Output)
	}
}

// ============================================================================
// Parse Error: OnStepLimit paths for abort + reset test
// ============================================================================

func TestExecutor_Run_ConsecutiveParseErrors_Abort_StepLimitAllowOnce(t *testing.T) {
	responses := make([]*llm.ChatResponse, 4)
	for i := range responses {
		responses[i] = llmResponseWithToolCall(
			fmt.Sprintf("attempt %d", i+1),
			"create_file",
			json.RawMessage(fmt.Sprintf(`{"bad_input":%d}`, i+1)),
		)
	}

	mockLLM := &mockLLMCaller{responses: responses}
	mockTools := newMockToolExecutor()
	mockTools.results["create_file"] = tools.ToolResult{
		Content: "failed to parse input: invalid field type",
		IsError: true,
	}

	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)

	callCount := 0
	exec.SetHITLHandler(&testStepLimitAdapter{fn: func(ctx context.Context, currentStep int, maxSteps int, reason string) (StepLimitResponse, error) {
		callCount++
		return StepLimitAllowOnce, nil
	}})

	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "create_file", Description: "create a file", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// After AllowOnce resets the counter, the 4th parse error bumps count to 1.
	// Then mock LLM returns default end_turn → implicit finish → Finished=true.
	if !result.Finished {
		t.Error("expected Finished=true after AllowOnce → one more parse error → end_turn")
	}
	if callCount != 1 {
		t.Errorf("expected OnStepLimit to be called once, got %d", callCount)
	}
}

func TestExecutor_Run_ConsecutiveParseErrors_Abort_StepLimitAllowAlways(t *testing.T) {
	responses := make([]*llm.ChatResponse, 5)
	for i := 0; i < 4; i++ {
		responses[i] = llmResponseWithToolCall(
			fmt.Sprintf("attempt %d", i+1),
			"create_file",
			json.RawMessage(fmt.Sprintf(`{"bad_input":%d}`, i+1)),
		)
	}
	responses[4] = llmResponseFinish("done", "completed")

	mockLLM := &mockLLMCaller{responses: responses}
	mockTools := newMockToolExecutor()
	mockTools.results["create_file"] = tools.ToolResult{
		Content: "failed to parse input: invalid field type",
		IsError: true,
	}

	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)

	callCount := 0
	exec.SetHITLHandler(&testStepLimitAdapter{fn: func(ctx context.Context, currentStep int, maxSteps int, reason string) (StepLimitResponse, error) {
		callCount++
		return StepLimitAllowAlways, nil
	}})

	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "create_file", Description: "create a file", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Finished {
		t.Error("expected Finished=true after AllowAlways → finish")
	}
	if callCount != 1 {
		t.Errorf("expected OnStepLimit to be called once, got %d", callCount)
	}
}

func TestExecutor_Run_ConsecutiveParseErrors_Abort_StepLimitDeny(t *testing.T) {
	responses := make([]*llm.ChatResponse, 3)
	for i := range responses {
		responses[i] = llmResponseWithToolCall(
			fmt.Sprintf("attempt %d", i+1),
			"create_file",
			json.RawMessage(fmt.Sprintf(`{"bad_input":%d}`, i+1)),
		)
	}

	mockLLM := &mockLLMCaller{responses: responses}
	mockTools := newMockToolExecutor()
	mockTools.results["create_file"] = tools.ToolResult{
		Content: "failed to parse input: invalid field type",
		IsError: true,
	}

	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)

	exec.SetHITLHandler(&testStepLimitAdapter{fn: func(ctx context.Context, currentStep int, maxSteps int, reason string) (StepLimitResponse, error) {
		return StepLimitDeny, nil
	}})

	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "create_file", Description: "create a file", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Finished {
		t.Error("expected Finished=false after parse error abort → Deny")
	}
	if !strings.Contains(result.Output, "failed to parse input") {
		t.Errorf("expected parse error abort message, got %q", result.Output)
	}
}

func TestExecutor_Run_ParseErrorResetsOnSuccess(t *testing.T) {
	cfg := defaultCircuitBreakerConfig
	cfg.ParseErrorAbortThreshold = 3

	responses := []*llm.ChatResponse{
		llmResponseWithToolCall("attempt 1", "tool_a", json.RawMessage(`{"x":"y"}`)),
		llmResponseWithToolCall("success", "tool_b", json.RawMessage(`{"good":"input"}`)),
		llmResponseWithToolCall("attempt 2", "tool_a", json.RawMessage(`{"x":"y"}`)),
		llmResponseFinish("done", "completed"),
	}

	mockLLM := &mockLLMCaller{responses: responses}
	mockTools := newMockToolExecutor()
	mockTools.results["tool_a"] = tools.ToolResult{
		Content: "failed to parse input: invalid field type",
		IsError: true,
	}
	mockTools.results["tool_b"] = tools.ToolResult{
		Content: "successful result data",
		IsError: false,
	}

	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, cfg)

	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "tool_a", Description: "tool a", Source: "core"},
		{Name: "tool_b", Description: "tool b", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Finished {
		t.Error("expected Finished=true (parse counter should reset after success)")
	}
}

// ============================================================================
// Truncation: OnStepLimit paths for abort
// ============================================================================

func TestExecutor_Run_ConsecutiveTruncation_Abort_StepLimitAllowOnce(t *testing.T) {
	responses := make([]*llm.ChatResponse, 4)
	for i := 0; i < 3; i++ {
		responses[i] = &llm.ChatResponse{
			Message: llm.Message{
				Role:    "assistant",
				Content: fmt.Sprintf("attempt %d", i+1),
				ToolCalls: []llm.ToolCall{
					{ID: fmt.Sprintf("call_%d", i), Name: "write_file", Input: json.RawMessage(`{"content":"tr`)},
				},
			},
			StopReason: "max_tokens",
			Usage:      llm.TokenUsage{InputTokens: 100, OutputTokens: 4096},
		}
	}
	responses[3] = llmResponseFinish("done", "completed")

	mockLLM := &mockLLMCaller{responses: responses}
	mockTools := newMockToolExecutor()
	cm := newMockContextManager()

	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	callCount := 0
	exec.SetHITLHandler(&testStepLimitAdapter{fn: func(ctx context.Context, currentStep int, maxSteps int, reason string) (StepLimitResponse, error) {
		callCount++
		return StepLimitAllowOnce, nil
	}})

	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "write_file", Description: "write a file", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Finished {
		t.Error("expected Finished=true after truncation abort → AllowOnce → finish")
	}
	if callCount != 1 {
		t.Errorf("expected OnStepLimit to be called once, got %d", callCount)
	}
}

func TestExecutor_Run_ConsecutiveTruncation_Abort_StepLimitAllowAlways(t *testing.T) {
	responses := make([]*llm.ChatResponse, 5)
	for i := 0; i < 4; i++ {
		responses[i] = &llm.ChatResponse{
			Message: llm.Message{
				Role:    "assistant",
				Content: fmt.Sprintf("attempt %d", i+1),
				ToolCalls: []llm.ToolCall{
					{ID: fmt.Sprintf("call_%d", i), Name: "write_file", Input: json.RawMessage(`{"content":"tr`)},
				},
			},
			StopReason: "max_tokens",
			Usage:      llm.TokenUsage{InputTokens: 100, OutputTokens: 4096},
		}
	}
	responses[4] = llmResponseFinish("done", "completed")

	mockLLM := &mockLLMCaller{responses: responses}
	mockTools := newMockToolExecutor()
	cm := newMockContextManager()

	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	callCount := 0
	exec.SetHITLHandler(&testStepLimitAdapter{fn: func(ctx context.Context, currentStep int, maxSteps int, reason string) (StepLimitResponse, error) {
		callCount++
		return StepLimitAllowAlways, nil
	}})

	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "write_file", Description: "write a file", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Finished {
		t.Error("expected Finished=true after truncation abort → AllowAlways → finish")
	}
	if callCount != 1 {
		t.Errorf("expected OnStepLimit to be called once, got %d", callCount)
	}
}

// ============================================================================
// Repeat Identical Tool: OnStepLimit paths for abort
// ============================================================================

func TestExecutor_Run_CircuitBreaker_Abort_StepLimitAllowOnce(t *testing.T) {
	responses := []*llm.ChatResponse{
		llmResponseWithToolCall("call 1", "search", json.RawMessage(`{"q":"same"}`)),
		llmResponseWithToolCall("call 2", "search", json.RawMessage(`{"q":"same"}`)),
		llmResponseWithToolCall("call 3", "search", json.RawMessage(`{"q":"same"}`)),
		llmResponseWithToolCall("call 4", "search", json.RawMessage(`{"q":"same"}`)),
		llmResponseFinish("done", "finished at last"),
	}

	mockLLM := &mockLLMCaller{responses: responses}
	mockTools := newMockToolExecutor()
	mockTools.results["search"] = tools.ToolResult{Content: "found", IsError: false}

	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)

	callCount := 0
	exec.SetHITLHandler(&testStepLimitAdapter{fn: func(ctx context.Context, currentStep int, maxSteps int, reason string) (StepLimitResponse, error) {
		callCount++
		return StepLimitAllowOnce, nil
	}})

	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "search", Description: "search", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Finished {
		t.Error("expected Finished=true after circuit breaker abort → AllowOnce → finish")
	}
	if callCount != 1 {
		t.Errorf("expected OnStepLimit to be called once, got %d", callCount)
	}
}

func TestExecutor_Run_CircuitBreaker_Abort_StepLimitAllowAlways(t *testing.T) {
	responses := []*llm.ChatResponse{
		llmResponseWithToolCall("call 1", "search", json.RawMessage(`{"q":"same"}`)),
		llmResponseWithToolCall("call 2", "search", json.RawMessage(`{"q":"same"}`)),
		llmResponseWithToolCall("call 3", "search", json.RawMessage(`{"q":"same"}`)),
		llmResponseWithToolCall("call 4", "search", json.RawMessage(`{"q":"same"}`)),
		llmResponseFinish("done", "finished"),
	}

	mockLLM := &mockLLMCaller{responses: responses}
	mockTools := newMockToolExecutor()
	mockTools.results["search"] = tools.ToolResult{Content: "found", IsError: false}

	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)

	callCount := 0
	exec.SetHITLHandler(&testStepLimitAdapter{fn: func(ctx context.Context, currentStep int, maxSteps int, reason string) (StepLimitResponse, error) {
		callCount++
		return StepLimitAllowAlways, nil
	}})

	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "search", Description: "search", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Finished {
		t.Error("expected Finished=true after circuit breaker abort → AllowAlways → finish")
	}
	if callCount != 1 {
		t.Errorf("expected OnStepLimit to be called once, got %d", callCount)
	}
}

func TestExecutor_Run_CircuitBreaker_Abort_StepLimitDeny(t *testing.T) {
	responses := []*llm.ChatResponse{
		llmResponseWithToolCall("call 1", "search", json.RawMessage(`{"q":"same"}`)),
		llmResponseWithToolCall("call 2", "search", json.RawMessage(`{"q":"same"}`)),
		llmResponseWithToolCall("call 3", "search", json.RawMessage(`{"q":"same"}`)),
		llmResponseWithToolCall("call 4", "search", json.RawMessage(`{"q":"same"}`)),
	}

	mockLLM := &mockLLMCaller{responses: responses}
	mockTools := newMockToolExecutor()
	mockTools.results["search"] = tools.ToolResult{Content: "found", IsError: false}

	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)

	exec.SetHITLHandler(&testStepLimitAdapter{fn: func(ctx context.Context, currentStep int, maxSteps int, reason string) (StepLimitResponse, error) {
		return StepLimitDeny, nil
	}})

	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "search", Description: "search", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Finished {
		t.Error("expected Finished=false after circuit breaker abort → Deny")
	}
	if !strings.Contains(result.Output, "Aborted") {
		t.Errorf("expected abort message, got %q", result.Output)
	}
}

func TestExecutor_Run_CircuitBreaker_ErrorAware_AbortWithDeny(t *testing.T) {
	responses := []*llm.ChatResponse{
		llmResponseWithToolCall("call 1", "search", json.RawMessage(`{"q":"same"}`)),
		llmResponseWithToolCall("call 2", "search", json.RawMessage(`{"q":"same"}`)),
		llmResponseWithToolCall("call 3", "search", json.RawMessage(`{"q":"same"}`)),
	}

	mockLLM := &mockLLMCaller{responses: responses}
	mockTools := newMockToolExecutor()
	mockTools.results["search"] = tools.ToolResult{Content: "error: not found", IsError: true}

	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)

	exec.SetHITLHandler(&testStepLimitAdapter{fn: func(ctx context.Context, currentStep int, maxSteps int, reason string) (StepLimitResponse, error) {
		return StepLimitDeny, nil
	}})

	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "search", Description: "search", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Finished {
		t.Error("expected Finished=false after error-aware circuit breaker abort → Deny")
	}
}

// ============================================================================
// handleImplicitFinish: finish_nudge path (suppressAssistantEvents=true)
// ============================================================================

func TestExecutor_Run_ImplicitFinish_FinishNudge(t *testing.T) {
	// suppressAssistantEvents=true enables the finish_nudge path in handleImplicitFinish
	responses := []*llm.ChatResponse{
		{Message: llm.Message{Role: "assistant", Content: "answer"}, StopReason: "end_turn"},
		llmResponseFinish("done", "completed"),
	}
	mockLLM := &mockLLMCaller{responses: responses}
	mockTools := newMockToolExecutor()
	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 5, nil, true, ToolResultBudget{}, defaultCircuitBreakerConfig)
	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "search", Description: "search", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Finished {
		t.Error("expected Finished=true after finish_nudge → finish call")
	}
}

func TestExecutor_Run_ImplicitFinish_NonEndTurn_Nudge(t *testing.T) {
	// Non-end_turn stop reason with no tool calls — enters the nudge path
	responses := []*llm.ChatResponse{
		{Message: llm.Message{Role: "assistant", Content: "no tools"}, StopReason: "stop"},
		llmResponseWithToolCall("now", "search", json.RawMessage(`{"q":"x"}`)),
		llmResponseFinish("done", "finished"),
	}
	mockLLM := &mockLLMCaller{responses: responses}
	mockTools := newMockToolExecutor()
	mockTools.results["search"] = tools.ToolResult{Content: "result", IsError: false}
	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 5, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "search", Description: "search", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Finished {
		t.Error("expected Finished=true after non-end_turn nudge → tool call → finish")
	}
}

func TestExecutor_Run_ImplicitFinish_NonEndTurn_ImplicitFinish(t *testing.T) {
	// Non-end_turn with nudge already attempted — implicit finish path
	responses := []*llm.ChatResponse{
		{Message: llm.Message{Role: "assistant", Content: "no tools"}, StopReason: "stop"},
		{Message: llm.Message{Role: "assistant", Content: "still no tools"}, StopReason: "stop"},
	}
	mockLLM := &mockLLMCaller{responses: responses}
	mockTools := newMockToolExecutor()
	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 5, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "search", Description: "search", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Finished {
		t.Error("expected Finished=true after nudge attempted → implicit finish")
	}
}

func TestExecutor_Run_ImplicitFinish_NonEndTurn_FinishNudge(t *testing.T) {
	// suppressAssistantEvents=true, non-end_turn → finish_nudge path (line 193)
	responses := []*llm.ChatResponse{
		{Message: llm.Message{Role: "assistant", Content: "no tools"}, StopReason: "stop"},
		llmResponseFinish("done", "finished via finish tool"),
	}
	mockLLM := &mockLLMCaller{responses: responses}
	mockTools := newMockToolExecutor()
	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 5, nil, true, ToolResultBudget{}, defaultCircuitBreakerConfig)
	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "search", Description: "search", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Finished {
		t.Error("expected Finished=true after non-end_turn finish_nudge → finish")
	}
}

// ============================================================================
// processToolResult: cache + truncation nudge path
// ============================================================================

func TestExecutor_Run_ProcessToolResult_TruncationCache(t *testing.T) {
	// Trigger per-tool truncation which sets wasTruncated=true,
	// then the tool cache path appends a fragmentation nudge.
	tb := ToolResultBudget{HardCapTokens: 1 << 30}
	ptt := map[string]ToolTruncationConfig{
		"search": {MaxLines: 1, MaxBytes: 50},
	}
	responses := []*llm.ChatResponse{
		llmResponseWithToolCall("search", "search", json.RawMessage(`{"q":"test"}`)),
		llmResponseFinish("done", "done"),
	}
	mockLLM := &mockLLMCaller{responses: responses}
	mockTools := newMockToolExecutor()
	// Return a long result that will be truncated
	mockTools.results["search"] = tools.ToolResult{Content: "line1\nline2\nline3\nline4\nline5", IsError: false}
	cm := newMockContextManager()
	tc := NewToolResultCache(5 * time.Minute)
	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 5, nil, false, tb, defaultCircuitBreakerConfig)
	exec.SetPerToolTruncation(ptt)
	exec.SetToolCache(tc)
	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "search", Description: "search", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Finished {
		t.Error("expected Finished=true")
	}
	// Verify the result output or step observation contains the truncation nudge
}

// ============================================================================
// log() with non-nil logger
// ============================================================================

func TestExecutor_Log_WithLogger(t *testing.T) {
	mockLLM := &mockLLMCaller{}
	mockTools := newMockToolExecutor()
	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 5, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	// SetLogger sets e.logger to non-nil, so log() returns it (not discard)
	logger := slog.New(slog.NewTextHandler(&nopWriter{}, nil))
	exec.SetLogger(logger)
	// log() should return the set logger, not the discard one.
	// We verify that log() doesn't return a discard logger by checking
	// that the returned logger is the same one we set.
	got := exec.log()
	if got != logger {
		t.Error("log() should return the SetLogger value, not discard")
	}
}

type nopWriter struct{}

func (w *nopWriter) Write(p []byte) (int, error) { return len(p), nil }

// ============================================================================
// Fruitless detector: AllowAlways disables threshold (line 893 sets to 0)
// Verify that AllowAlways properly prevents subsequent abort
// ============================================================================

func TestExecutor_Run_FruitlessDetector_AllowAlways_NotReAbort(t *testing.T) {
	cfg := CircuitBreakerConfig{
		RepeatNudgeThreshold:         50,
		RepeatAbortThreshold:         100,
		TruncationAbortThreshold:     100,
		ParseErrorAbortThreshold:     100,
		FruitlessNudgeThreshold:      5,
		FruitlessAbortThreshold:      8,
		FruitlessMaxResultLen:        32,
		SameToolRepeatNudgeThreshold: 50,
		SameToolRepeatAbortThreshold: 100,
		SameToolResultSizeDelta:      64,
	}
	// After AllowAlways disables fruitless abort, subsequent fruitless results
	// should NOT abort. Provide 12 fruitless responses + finish.
	responses := make([]*llm.ChatResponse, 13)
	for i := 0; i < 12; i++ {
		responses[i] = llmResponseWithToolCall(
			fmt.Sprintf("fruitless %d", i+1),
			"search",
			json.RawMessage(fmt.Sprintf(`{"q":"test%d"}`, i+1)),
		)
	}
	responses[12] = llmResponseFinish("done", "completed")
	mockLLM := &mockLLMCaller{responses: responses}
	mockTools := newMockToolExecutor()
	mockTools.results["search"] = tools.ToolResult{Content: "short", IsError: false}
	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 20, nil, false, ToolResultBudget{}, cfg)
	exec.SetHITLHandler(&testStepLimitAdapter{fn: func(ctx context.Context, currentStep int, maxSteps int, reason string) (StepLimitResponse, error) {
		return StepLimitAllowAlways, nil
	}})
	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "search", Description: "search", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Finished {
		t.Error("expected Finished=true after AllowAlways — should not re-abort on subsequent fruitless results")
	}
}

// ============================================================================
// getTruncationHint: cover all tool-specific branches
// ============================================================================

func TestGetTruncationHint_AllTools(t *testing.T) {
	tests := []struct {
		toolName string
		want     string
	}{
		{tools.ToolReadFile, "Re-read the file with start_line/end_line to see specific sections, or use ripgrep to search for specific content."},
		{tools.ToolRipgrep, "Narrow your search pattern or add path filters to reduce results."},
		{tools.ToolGrep, "Narrow your search pattern or add path filters to reduce results."},
		{tools.ToolGlob, "Use a more specific glob pattern to reduce results."},
		{tools.ToolWebFetch, "The page content was truncated. Ask the user to open the URL directly, or try fetching a more specific page."},
		{"unknown_tool", "Break into smaller operations or use targeted queries."},
	}
	for _, tt := range tests {
		t.Run(tt.toolName, func(t *testing.T) {
			got := getTruncationHint(tt.toolName)
			if got != tt.want {
				t.Errorf("getTruncationHint(%q) = %q, want %q", tt.toolName, got, tt.want)
			}
		})
	}
}

// ============================================================================
// applyToolResultBudget: HardCapTokens = 0 path
// ============================================================================

func TestApplyToolResultBudget_NoHardCap(t *testing.T) {
	mockLLM := &mockLLMCaller{}
	mockTools := newMockToolExecutor()
	// HardCapTokens=0 → early return, no truncation
	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 5, nil, false, ToolResultBudget{HardCapTokens: 0, MaxFillFraction: 0.5}, defaultCircuitBreakerConfig)
	cm := newMockContextManager()
	result := exec.applyToolResultBudget("some observation", cm, "search", "")
	if result != "some observation" {
		t.Errorf("expected unchanged observation, got %q", result)
	}
}

// ============================================================================
// CallLLM with nil response (defensive check)
// ============================================================================

func TestCallLLMWithReactiveCompaction_NilResponse(t *testing.T) {
	mockLLM := &mockLLMCaller{
		responses: []*llm.ChatResponse{nil}, // nil response triggers defensive check
	}
	mockTools := newMockToolExecutor()
	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 5, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	state := &runState{effectiveMaxSteps: 5, stepNum: 1}
	resp, _, err := exec.callLLMWithReactiveCompaction(context.Background(), state, cm, nil)
	if err == nil {
		t.Error("expected error for nil LLM response")
	}
	if resp != nil {
		t.Error("expected nil response")
	}
}

// ============================================================================
// handleStepLimitBoundary: error from OnStepLimit
// ============================================================================

func TestHandleStepLimitBoundary_CallbackError(t *testing.T) {
	mockLLM := &mockLLMCaller{}
	mockTools := newMockToolExecutor()
	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 5, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	exec.SetHITLHandler(&testStepLimitAdapter{fn: func(ctx context.Context, currentStep int, maxSteps int, reason string) (StepLimitResponse, error) {
		return StepLimitDeny, errors.New("callback error")
	}})
	// Run with only 1 step to hit the boundary quickly
	responses := []*llm.ChatResponse{
		llmResponseWithToolCall("step1", "search", json.RawMessage(`{"q":"x"}`)),
		llmResponseWithToolCall("step2", "search", json.RawMessage(`{"q":"y"}`)),
		llmResponseWithToolCall("step3", "search", json.RawMessage(`{"q":"z"}`)),
		llmResponseWithToolCall("step4", "search", json.RawMessage(`{"q":"w"}`)),
		llmResponseWithToolCall("step5", "search", json.RawMessage(`{"q":"v"}`)),
		llmResponseFinish("done", "finished"),
	}
	mockLLM.responses = responses
	exec.llm = mockLLM
	result, err := exec.Run(context.Background(), []tools.ToolDescriptor{
		{Name: "search", Description: "search", Source: "core"},
	}, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Finished {
		t.Error("expected Finished=false when callback returns error")
	}
}

// ============================================================================
// Reactive compaction on context-exceeded error
// ============================================================================

func TestExecutor_Run_ReactiveCompaction(t *testing.T) {
	mockLLM := &mockLLMCaller{
		errors: []error{errors.New("context length exceeded")},
		responses: []*llm.ChatResponse{
			llmResponseFinish("recovered", "done after compaction"),
		},
	}
	mockTools := newMockToolExecutor()
	cm := newMockContextManager()
	exec := newExecutorDefaultHITL(mockLLM, mockTools, &mockTokenCounter{}, 5, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	result, err := exec.Run(context.Background(), nil, cm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Finished {
		t.Error("expected Finished=true after reactive compaction + recovery")
	}
}
