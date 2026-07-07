package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/tools"
)

// --- Setter method tests ---

func TestSetLogger(t *testing.T) {
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	if exec.logger != nil {
		t.Error("logger should be nil before SetLogger")
	}
	exec.SetLogger(slog.New(slog.DiscardHandler))
	if exec.logger == nil {
		t.Error("logger should not be nil after SetLogger")
	}
}

func TestSetReasoningEffort(t *testing.T) {
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	if exec.reasoningEffort != "" {
		t.Error("reasoningEffort should be empty initially")
	}
	exec.SetReasoningEffort("high")
	if exec.reasoningEffort != "high" {
		t.Errorf("reasoningEffort = %q, want %q", exec.reasoningEffort, "high")
	}
}

func TestSetToolCache(t *testing.T) {
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	if exec.toolCache != nil {
		t.Error("toolCache should be nil initially")
	}
	cache := NewToolResultCache(0)
	exec.SetToolCache(cache)
	if exec.toolCache == nil {
		t.Error("toolCache should not be nil after SetToolCache")
	}
}

func TestAddNonCacheableTools(t *testing.T) {
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)

	// NewExecutor initialises the set from defaultNonCacheableTools.
	if exec.nonCacheableTools == nil {
		t.Fatal("nonCacheableTools should be initialised from defaults in NewExecutor")
	}
	for _, name := range []string{"finish", "store_fact", tools.ToolBatch} {
		if _, ok := exec.nonCacheableTools[name]; !ok {
			t.Errorf("default nonCacheableTools missing %q", name)
		}
	}

	// AddNonCacheableTools extends the set without removing defaults.
	exec.AddNonCacheableTools("delegate", "reflect")
	for _, name := range []string{"delegate", "reflect", "finish"} {
		if _, ok := exec.nonCacheableTools[name]; !ok {
			t.Errorf("nonCacheableTools missing %q after AddNonCacheableTools", name)
		}
	}

	// Adding an already-present tool is a no-op (no panic, still present).
	exec.AddNonCacheableTools("finish")
	if _, ok := exec.nonCacheableTools["finish"]; !ok {
		t.Error("finish should still be present after re-adding")
	}
}

func TestAddNonCacheableTools_DoesNotMutateDefault(t *testing.T) {
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	exec.AddNonCacheableTools("my_custom_tool")

	// The package-level default must not be polluted.
	if _, ok := defaultNonCacheableTools["my_custom_tool"]; ok {
		t.Error("AddNonCacheableTools must not mutate the package-level defaultNonCacheableTools")
	}
	// The executor's own set should contain the custom tool.
	if _, ok := exec.nonCacheableTools["my_custom_tool"]; !ok {
		t.Error("executor's nonCacheableTools should contain the custom tool")
	}
}

func TestSetPreWarningPercent(t *testing.T) {
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	if exec.preWarningPercent != 0 {
		t.Error("preWarningPercent should be 0 initially")
	}
	exec.SetPreWarningPercent(75)
	if exec.preWarningPercent != 75 {
		t.Errorf("preWarningPercent = %d, want 75", exec.preWarningPercent)
	}
}

func TestSetPerToolTruncation(t *testing.T) {
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	if exec.perToolTruncation != nil {
		t.Error("perToolTruncation should be nil initially")
	}
	cfg := map[string]ToolTruncationConfig{
		"read_file": {MaxLines: 100},
	}
	exec.SetPerToolTruncation(cfg)
	if exec.perToolTruncation == nil {
		t.Error("perToolTruncation should not be nil after SetPerToolTruncation")
	}
	if exec.perToolTruncation["read_file"].MaxLines != 100 {
		t.Errorf("MaxLines = %d, want 100", exec.perToolTruncation["read_file"].MaxLines)
	}
}

// --- checkFruitlessResult edge cases ---

func TestCheckFruitlessResult_ThresholdZero_Disabled(t *testing.T) {
	cfg := CircuitBreakerConfig{
		RepeatNudgeThreshold:         3,
		RepeatAbortThreshold:         4,
		TruncationAbortThreshold:     3,
		ParseErrorAbortThreshold:     3,
		FruitlessNudgeThreshold:      0,
		FruitlessAbortThreshold:      0,
		FruitlessMaxResultLen:        32,
		SameToolRepeatNudgeThreshold: 50,
		SameToolRepeatAbortThreshold: 60,
		SameToolResultSizeDelta:      64,
	}
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, cfg)
	exec.consecutiveFruitlessCount = 100
	act, result, err := exec.checkFruitlessResult(
		context.Background(),
		llm.ToolCall{Name: "search"},
		0, "", false,
		&runState{effectiveMaxSteps: 10},
		newMockContextManager(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Error("expected nil result when fruitless thresholds are disabled")
	}
	if act != actionNone {
		t.Errorf("expected actionNone, got %v", act)
	}
}

func TestCheckFruitlessResult_ErrorDoesNotCount(t *testing.T) {
	cfg := CircuitBreakerConfig{
		RepeatNudgeThreshold:         3,
		RepeatAbortThreshold:         4,
		TruncationAbortThreshold:     3,
		ParseErrorAbortThreshold:     3,
		FruitlessNudgeThreshold:      3,
		FruitlessAbortThreshold:      6,
		FruitlessMaxResultLen:        32,
		SameToolRepeatNudgeThreshold: 50,
		SameToolRepeatAbortThreshold: 60,
		SameToolResultSizeDelta:      64,
	}
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, cfg)
	for i := 0; i < 5; i++ {
		_, _, _ = exec.checkFruitlessResult(
			context.Background(),
			llm.ToolCall{Name: "search"},
			0, "error", true,
			&runState{effectiveMaxSteps: 10},
			newMockContextManager(),
		)
	}
	if exec.consecutiveFruitlessCount != 0 {
		t.Errorf("fruitless count should be 0 for error results, got %d", exec.consecutiveFruitlessCount)
	}
}

func TestCheckFruitlessResult_LargeResultResets(t *testing.T) {
	cfg := CircuitBreakerConfig{
		RepeatNudgeThreshold:         3,
		RepeatAbortThreshold:         4,
		TruncationAbortThreshold:     3,
		ParseErrorAbortThreshold:     3,
		FruitlessNudgeThreshold:      3,
		FruitlessAbortThreshold:      6,
		FruitlessMaxResultLen:        32,
		SameToolRepeatNudgeThreshold: 50,
		SameToolRepeatAbortThreshold: 60,
		SameToolResultSizeDelta:      64,
	}
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, cfg)
	_, _, _ = exec.checkFruitlessResult(
		context.Background(),
		llm.ToolCall{Name: "search"},
		0, "small", false,
		&runState{effectiveMaxSteps: 10},
		newMockContextManager(),
	)
	if exec.consecutiveFruitlessCount != 1 {
		t.Errorf("fruitless count should be 1, got %d", exec.consecutiveFruitlessCount)
	}
	_, _, _ = exec.checkFruitlessResult(
		context.Background(),
		llm.ToolCall{Name: "search"},
		0, strings.Repeat("x", 100), false,
		&runState{effectiveMaxSteps: 10},
		newMockContextManager(),
	)
	if exec.consecutiveFruitlessCount != 0 {
		t.Errorf("fruitless count should be 0 after large result, got %d", exec.consecutiveFruitlessCount)
	}
}

// --- checkFruitlessResult: exempt tools (mutating tools, meta-tools) ---

func TestCheckFruitlessResult_ExemptToolsNotCounted(t *testing.T) {
	cfg := CircuitBreakerConfig{
		RepeatNudgeThreshold:         3,
		RepeatAbortThreshold:         5,
		TruncationAbortThreshold:     3,
		ParseErrorAbortThreshold:     3,
		FruitlessNudgeThreshold:      3,
		FruitlessAbortThreshold:      4,
		FruitlessMaxResultLen:        48,
		SameToolRepeatNudgeThreshold: 50,
		SameToolRepeatAbortThreshold: 60,
		SameToolResultSizeDelta:      64,
	}
	// Each exempt tool produces a short successful result (e.g. edit_file
	// returns "successfully edited file" = 24 bytes). None should increment
	// the fruitless counter, even when called many times in a row.
	exemptTools := []string{
		"edit_file", "write_file", "create_directory",
		"delete_file", "delete_directory",
		"update_checklist", "set_step_status", "store_fact",
	}
	for _, toolName := range exemptTools {
		t.Run(toolName, func(t *testing.T) {
			exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, cfg)
			for i := 0; i < 10; i++ {
				act, result, err := exec.checkFruitlessResult(
					context.Background(),
					llm.ToolCall{Name: toolName},
					0, "ok", false,
					&runState{effectiveMaxSteps: 10},
					newMockContextManager(),
				)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result != nil {
					t.Errorf("expected nil result for exempt tool %s at iteration %d, got abort", toolName, i)
				}
				if act != actionNone {
					t.Errorf("expected actionNone for exempt tool %s at iteration %d, got %v", toolName, i, act)
				}
			}
			if exec.consecutiveFruitlessCount != 0 {
				t.Errorf("fruitless count should be 0 for exempt tool %s after 10 short results, got %d", toolName, exec.consecutiveFruitlessCount)
			}
		})
	}
}

// --- checkSameToolRepetition edge cases ---

func TestCheckSameToolRepetition_StoreFactResets(t *testing.T) {
	cfg := CircuitBreakerConfig{
		RepeatNudgeThreshold:         3,
		RepeatAbortThreshold:         4,
		TruncationAbortThreshold:     3,
		ParseErrorAbortThreshold:     3,
		FruitlessNudgeThreshold:      50,
		FruitlessAbortThreshold:      60,
		FruitlessMaxResultLen:        32,
		SameToolRepeatNudgeThreshold: 3,
		SameToolRepeatAbortThreshold: 6,
		SameToolResultSizeDelta:      64,
	}
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, cfg)
	for i := 0; i < 4; i++ {
		_, _, _ = exec.checkSameToolRepetition(
			context.Background(),
			llm.ToolCall{Name: "search"},
			0, strings.Repeat("x", 50),
			tools.ToolResult{Content: strings.Repeat("x", 50)},
			&runState{effectiveMaxSteps: 10},
			newMockContextManager(),
		)
	}
	_, _, _ = exec.checkSameToolRepetition(
		context.Background(),
		llm.ToolCall{Name: "store_fact"},
		0, "stored",
		tools.ToolResult{Content: "stored"},
		&runState{effectiveMaxSteps: 10},
		newMockContextManager(),
	)
	if exec.sameToolConsecutiveCount != 0 {
		t.Errorf("sameToolConsecutiveCount should be 0 after store_fact, got %d", exec.sameToolConsecutiveCount)
	}
}

func TestCheckSameToolRepetition_ExemptToolsNotCounted(t *testing.T) {
	cfg := CircuitBreakerConfig{
		RepeatNudgeThreshold:         3,
		RepeatAbortThreshold:         5,
		TruncationAbortThreshold:     3,
		ParseErrorAbortThreshold:     3,
		FruitlessNudgeThreshold:      50,
		FruitlessAbortThreshold:      60,
		FruitlessMaxResultLen:        48,
		SameToolRepeatNudgeThreshold: 3,
		SameToolRepeatAbortThreshold: 4,
		SameToolResultSizeDelta:      128,
	}
	// Mutating tools and meta-tools legitimately produce bursts of short,
	// similarly-sized successful results (e.g. batch edit_file calls each
	// returning "successfully edited file" = 24 bytes). They must not trigger
	// the same-tool-repeat detector.
	exemptTools := []string{
		"edit_file", "write_file", "create_directory",
		"delete_file", "delete_directory",
		"update_checklist", "set_step_status", "store_fact",
	}
	for _, toolName := range exemptTools {
		t.Run(toolName, func(t *testing.T) {
			exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, cfg)
			for i := 0; i < 10; i++ {
				act, result, err := exec.checkSameToolRepetition(
					context.Background(),
					llm.ToolCall{Name: toolName},
					0, "ok",
					tools.ToolResult{Content: "ok"},
					&runState{effectiveMaxSteps: 10},
					newMockContextManager(),
				)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result != nil {
					t.Errorf("expected nil result for exempt tool %s at iteration %d, got abort", toolName, i)
				}
				if act != actionNone {
					t.Errorf("expected actionNone for exempt tool %s at iteration %d, got %v", toolName, i, act)
				}
			}
			if exec.sameToolConsecutiveCount != 0 {
				t.Errorf("sameToolConsecutiveCount should be 0 for exempt tool %s after 10 calls, got %d", toolName, exec.sameToolConsecutiveCount)
			}
		})
	}
}

func TestCheckSameToolRepetition_ThresholdZero_Disabled(t *testing.T) {
	cfg := CircuitBreakerConfig{
		RepeatNudgeThreshold:         3,
		RepeatAbortThreshold:         4,
		TruncationAbortThreshold:     3,
		ParseErrorAbortThreshold:     3,
		FruitlessNudgeThreshold:      50,
		FruitlessAbortThreshold:      60,
		FruitlessMaxResultLen:        32,
		SameToolRepeatNudgeThreshold: 0,
		SameToolRepeatAbortThreshold: 0,
		SameToolResultSizeDelta:      64,
	}
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, cfg)
	exec.sameToolConsecutiveCount = 100
	exec.sameToolLastName = "search"
	exec.sameToolLastResultLen = 50
	act, result, err := exec.checkSameToolRepetition(
		context.Background(),
		llm.ToolCall{Name: "search"},
		0, strings.Repeat("x", 50),
		tools.ToolResult{Content: strings.Repeat("x", 50)},
		&runState{effectiveMaxSteps: 10},
		newMockContextManager(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Error("expected nil result when thresholds are disabled")
	}
	if act != actionNone {
		t.Errorf("expected actionNone, got %v", act)
	}
}

// --- checkParseErrors edge cases ---

func TestCheckParseErrors_NudgeBeforeAbort(t *testing.T) {
	cfg := CircuitBreakerConfig{
		RepeatNudgeThreshold:         3,
		RepeatAbortThreshold:         4,
		TruncationAbortThreshold:     3,
		ParseErrorAbortThreshold:     3,
		FruitlessNudgeThreshold:      50,
		FruitlessAbortThreshold:      60,
		FruitlessMaxResultLen:        32,
		SameToolRepeatNudgeThreshold: 50,
		SameToolRepeatAbortThreshold: 60,
		SameToolResultSizeDelta:      64,
	}
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, cfg)
	obs, act, result, err := exec.checkParseErrors(
		context.Background(),
		llm.ToolCall{Name: "create_file"},
		0, "failed to parse input: bad json",
		tools.ToolResult{Content: "failed to parse input: bad json", IsError: true},
		&runState{effectiveMaxSteps: 10},
		newMockContextManager(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Error("expected nil result on first parse error")
	}
	if act != actionNone {
		t.Errorf("expected actionNone on first parse error, got %v", act)
	}
	if !strings.Contains(obs, "failed to parse input") {
		t.Error("observation should contain original error")
	}
	if !strings.Contains(obs, "malformed") {
		t.Error("observation should contain nudge about malformed arguments")
	}
}

func TestCheckParseErrors_ResetOnSuccess(t *testing.T) {
	cfg := CircuitBreakerConfig{
		RepeatNudgeThreshold:         3,
		RepeatAbortThreshold:         4,
		TruncationAbortThreshold:     3,
		ParseErrorAbortThreshold:     3,
		FruitlessNudgeThreshold:      50,
		FruitlessAbortThreshold:      60,
		FruitlessMaxResultLen:        32,
		SameToolRepeatNudgeThreshold: 50,
		SameToolRepeatAbortThreshold: 60,
		SameToolResultSizeDelta:      64,
	}
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, cfg)
	for i := 0; i < 2; i++ {
		_, _, _, _ = exec.checkParseErrors(
			context.Background(),
			llm.ToolCall{Name: "create_file"},
			0, "failed to parse input",
			tools.ToolResult{Content: "failed to parse input", IsError: true},
			&runState{effectiveMaxSteps: 10},
			newMockContextManager(),
		)
	}
	if exec.consecutiveParseErrorCount != 2 {
		t.Errorf("parse error count should be 2, got %d", exec.consecutiveParseErrorCount)
	}
	_, _, _, _ = exec.checkParseErrors(
		context.Background(),
		llm.ToolCall{Name: "search"},
		0, "found results",
		tools.ToolResult{Content: "found results", IsError: false},
		&runState{effectiveMaxSteps: 10},
		newMockContextManager(),
	)
	if exec.consecutiveParseErrorCount != 0 {
		t.Errorf("parse error count should be 0 after success, got %d", exec.consecutiveParseErrorCount)
	}
}

func TestCheckParseErrors_DifferentTool(t *testing.T) {
	cfg := CircuitBreakerConfig{
		RepeatNudgeThreshold:         3,
		RepeatAbortThreshold:         4,
		TruncationAbortThreshold:     3,
		ParseErrorAbortThreshold:     3,
		FruitlessNudgeThreshold:      50,
		FruitlessAbortThreshold:      60,
		FruitlessMaxResultLen:        32,
		SameToolRepeatNudgeThreshold: 50,
		SameToolRepeatAbortThreshold: 60,
		SameToolResultSizeDelta:      64,
	}
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, cfg)
	_, _, _, _ = exec.checkParseErrors(
		context.Background(),
		llm.ToolCall{Name: "create_file"},
		0, "failed to parse input",
		tools.ToolResult{Content: "failed to parse input", IsError: true},
		&runState{effectiveMaxSteps: 10},
		newMockContextManager(),
	)
	_, _, _, _ = exec.checkParseErrors(
		context.Background(),
		llm.ToolCall{Name: "write_file"},
		0, "failed to parse input",
		tools.ToolResult{Content: "failed to parse input", IsError: true},
		&runState{effectiveMaxSteps: 10},
		newMockContextManager(),
	)
	if exec.consecutiveParseErrorCount != 1 {
		t.Errorf("parse error count should be 1 for different tool, got %d", exec.consecutiveParseErrorCount)
	}
	if exec.consecutiveParseErrorTool != "write_file" {
		t.Errorf("parse error tool should be write_file, got %q", exec.consecutiveParseErrorTool)
	}
}

func TestCheckParseErrors_NonParseError(t *testing.T) {
	cfg := defaultCircuitBreakerConfig
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, cfg)
	obs, act, result, err := exec.checkParseErrors(
		context.Background(),
		llm.ToolCall{Name: "tool"},
		0, "something went wrong",
		tools.ToolResult{Content: "something went wrong", IsError: true},
		&runState{effectiveMaxSteps: 10},
		newMockContextManager(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Error("non-parse error should not trigger abort")
	}
	if act != actionNone {
		t.Errorf("non-parse error should return actionNone, got %v", act)
	}
	if obs != "something went wrong" {
		t.Errorf("observation should be unchanged, got %q", obs)
	}
}

// --- context With/From tests ---

type testWriterStruct struct{}

func (tw *testWriterStruct) Write(p []byte) (n int, err error) { return len(p), nil }

type testFactStoreStruct struct{}

func (tfs *testFactStoreStruct) StoreFact(keywords []string, content, author string) {}
func (tfs *testFactStoreStruct) SearchFacts(keywords []string) []FactEntry           { return nil }

var _ FactStore = (*testFactStoreStruct)(nil)

func TestWithStepID_StepIDFromContext(t *testing.T) {
	ctx := WithStepID(context.Background(), "step_42")
	if got := StepIDFromContext(ctx); got != "step_42" {
		t.Errorf("StepIDFromContext = %q, want %q", got, "step_42")
	}
}

func TestStepIDFromContext_Empty(t *testing.T) {
	if got := StepIDFromContext(context.Background()); got != "" {
		t.Errorf("StepIDFromContext with no step ID = %q, want empty", got)
	}
}

func TestWithDumpWriter_DumpWriterFromContext(t *testing.T) {
	w := &testWriterStruct{}
	ctx := WithDumpWriter(context.Background(), w)
	if got := DumpWriterFromContext(ctx); got != w {
		t.Error("DumpWriterFromContext should return the set writer")
	}
}

func TestDumpWriterFromContext_Nil(t *testing.T) {
	if got := DumpWriterFromContext(context.Background()); got != nil {
		t.Error("DumpWriterFromContext should return nil when not set")
	}
}

func TestWithFactStore_FactStoreFromContext(t *testing.T) {
	fs := &testFactStoreStruct{}
	ctx := WithFactStore(context.Background(), fs)
	if got := FactStoreFromContext(ctx); got != fs {
		t.Error("FactStoreFromContext should return the set fact store")
	}
}

func TestFactStoreFromContext_Nil(t *testing.T) {
	if got := FactStoreFromContext(context.Background()); got != nil {
		t.Error("FactStoreFromContext should return nil when not set")
	}
}

// --- handleTruncationStopReason edge cases ---

func TestHandleTruncationStopReason_Abort_DefaultHandler(t *testing.T) {
	cfg := CircuitBreakerConfig{
		RepeatNudgeThreshold:         3,
		RepeatAbortThreshold:         4,
		TruncationAbortThreshold:     2,
		ParseErrorAbortThreshold:     3,
		FruitlessNudgeThreshold:      50,
		FruitlessAbortThreshold:      60,
		FruitlessMaxResultLen:        32,
		SameToolRepeatNudgeThreshold: 50,
		SameToolRepeatAbortThreshold: 60,
		SameToolResultSizeDelta:      64,
	}
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, cfg)
	exec.emitter = &NoopEvents{}
	exec.consecutiveTruncationCount = 2 // exactly at threshold

	resp := &llm.ChatResponse{
		Message: llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{
				{ID: "call_1", Name: "search", Input: []byte(`{"query":"test"}`)},
			},
		},
		Usage: llm.TokenUsage{InputTokens: 100, OutputTokens: 50},
	}
	state := &runState{effectiveMaxSteps: 10}
	cw := newMockContextManager()

	result, act := exec.handleTruncationStopReason(context.Background(), resp, "thinking", state, cw)
	if result == nil {
		t.Fatal("expected non-nil result for truncation abort")
	}
	if result.Finished {
		t.Error("expected Finished=false for truncation abort")
	}
	if act != actionNone {
		t.Errorf("expected actionNone, got %v", act)
	}
}

func TestHandleTruncationStopReason_Abort_StepLimitDeny(t *testing.T) {
	cfg := CircuitBreakerConfig{
		RepeatNudgeThreshold:         3,
		RepeatAbortThreshold:         4,
		TruncationAbortThreshold:     2,
		ParseErrorAbortThreshold:     3,
		FruitlessNudgeThreshold:      50,
		FruitlessAbortThreshold:      60,
		FruitlessMaxResultLen:        32,
		SameToolRepeatNudgeThreshold: 50,
		SameToolRepeatAbortThreshold: 60,
		SameToolResultSizeDelta:      64,
	}
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, cfg)
	exec.emitter = &NoopEvents{}
	exec.consecutiveTruncationCount = 2
	exec.SetHITLHandler(&testStepLimitAdapter{fn: func(ctx context.Context, stepNum, effectiveMaxSteps int, reason string) (StepLimitResponse, error) {
		return StepLimitDeny, nil
	}})

	resp := &llm.ChatResponse{
		Message: llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{
				{ID: "call_1", Name: "search", Input: []byte(`{"query":"test"}`)},
			},
		},
		Usage: llm.TokenUsage{InputTokens: 100, OutputTokens: 50},
	}
	state := &runState{effectiveMaxSteps: 10}
	cw := newMockContextManager()

	result, act := exec.handleTruncationStopReason(context.Background(), resp, "thinking", state, cw)
	if result == nil {
		t.Fatal("expected non-nil result for truncation abort with StepLimitDeny")
	}
	if result.Finished {
		t.Error("expected Finished=false")
	}
	if act != actionNone {
		t.Errorf("expected actionNone, got %v", act)
	}
}

func TestHandleTruncationStopReason_Abort_OnStepLimitError(t *testing.T) {
	cfg := CircuitBreakerConfig{
		RepeatNudgeThreshold:         3,
		RepeatAbortThreshold:         4,
		TruncationAbortThreshold:     2,
		ParseErrorAbortThreshold:     3,
		FruitlessNudgeThreshold:      50,
		FruitlessAbortThreshold:      60,
		FruitlessMaxResultLen:        32,
		SameToolRepeatNudgeThreshold: 50,
		SameToolRepeatAbortThreshold: 60,
		SameToolResultSizeDelta:      64,
	}
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, cfg)
	exec.emitter = &NoopEvents{}
	exec.consecutiveTruncationCount = 2
	exec.SetHITLHandler(&testStepLimitAdapter{fn: func(ctx context.Context, stepNum, effectiveMaxSteps int, reason string) (StepLimitResponse, error) {
		return StepLimitDeny, errors.New("callback error")
	}})

	resp := &llm.ChatResponse{
		Message: llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{
				{ID: "call_1", Name: "search", Input: []byte(`{"query":"test"}`)},
			},
		},
		Usage: llm.TokenUsage{InputTokens: 100, OutputTokens: 50},
	}
	state := &runState{effectiveMaxSteps: 10}
	cw := newMockContextManager()

	result, act := exec.handleTruncationStopReason(context.Background(), resp, "thinking", state, cw)
	if result == nil {
		t.Fatal("expected non-nil result when OnStepLimit errors")
	}
	if act != actionNone {
		t.Errorf("expected actionNone, got %v", act)
	}
}

// --- checkParseErrors abort threshold ---

func TestCheckParseErrors_AbortThreshold(t *testing.T) {
	cfg := CircuitBreakerConfig{
		RepeatNudgeThreshold:         3,
		RepeatAbortThreshold:         4,
		TruncationAbortThreshold:     3,
		ParseErrorAbortThreshold:     3,
		FruitlessNudgeThreshold:      50,
		FruitlessAbortThreshold:      60,
		FruitlessMaxResultLen:        32,
		SameToolRepeatNudgeThreshold: 50,
		SameToolRepeatAbortThreshold: 60,
		SameToolResultSizeDelta:      64,
	}
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, cfg)
	exec.emitter = &NoopEvents{}
	exec.consecutiveParseErrorCount = 2
	exec.consecutiveParseErrorTool = "create_file"

	_, act, result, err := exec.checkParseErrors(
		context.Background(),
		llm.ToolCall{Name: "create_file"},
		0, "failed to parse input: invalid json",
		tools.ToolResult{Content: "failed to parse input: invalid json", IsError: true},
		&runState{effectiveMaxSteps: 10},
		newMockContextManager(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result on 3rd parse error (abort)")
	}
	if act != actionNone {
		t.Errorf("expected actionNone for abort, got %v", act)
	}
	if !strings.Contains(result.Output, "failed to parse input") {
		t.Errorf("output should mention parse failure, got %q", result.Output)
	}
}

func TestCheckParseErrors_Abort_StepLimitAllowOnce(t *testing.T) {
	cfg := CircuitBreakerConfig{
		RepeatNudgeThreshold:         3,
		RepeatAbortThreshold:         4,
		TruncationAbortThreshold:     3,
		ParseErrorAbortThreshold:     3,
		FruitlessNudgeThreshold:      50,
		FruitlessAbortThreshold:      60,
		FruitlessMaxResultLen:        32,
		SameToolRepeatNudgeThreshold: 50,
		SameToolRepeatAbortThreshold: 60,
		SameToolResultSizeDelta:      64,
	}
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, cfg)
	exec.emitter = &NoopEvents{}
	exec.consecutiveParseErrorCount = 2
	exec.consecutiveParseErrorTool = "create_file"
	exec.SetHITLHandler(&testStepLimitAdapter{fn: func(ctx context.Context, stepNum, effectiveMaxSteps int, reason string) (StepLimitResponse, error) {
		return StepLimitAllowOnce, nil
	}})

	state := &runState{effectiveMaxSteps: 10}
	cw := newMockContextManager()

	obs, act, result, _ := exec.checkParseErrors(
		context.Background(),
		llm.ToolCall{Name: "create_file"},
		0, "failed to parse input: bad json",
		tools.ToolResult{Content: "failed to parse input: bad json", IsError: true},
		state,
		cw,
	)
	if result != nil {
		t.Error("expected nil result when StepLimitAllowOnce")
	}
	if act != actionBreak {
		t.Errorf("expected actionBreak, got %v", act)
	}
	if !state.circuitBreakerTriggered {
		t.Error("circuitBreakerTriggered should be true")
	}
	_ = obs
}

// --- applyPerToolTruncation ---

func TestApplyPerToolTruncation_NilConfig(t *testing.T) {
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	content, truncated := exec.applyPerToolTruncation("some content", "read_file")
	if truncated {
		t.Error("should not be truncated with nil config")
	}
	if content != "some content" {
		t.Errorf("content = %q, want %q", content, "some content")
	}
}

func TestApplyPerToolTruncation_ToolNotInConfig(t *testing.T) {
	cfg := map[string]ToolTruncationConfig{"search": {MaxLines: 10}}
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	exec.SetPerToolTruncation(cfg)
	content, truncated := exec.applyPerToolTruncation("some content", "read_file")
	if truncated {
		t.Error("should not be truncated when tool not in config")
	}
	if content != "some content" {
		t.Errorf("content = %q, want unchanged", content)
	}
}

func TestApplyPerToolTruncation_LineTruncation(t *testing.T) {
	cfg := map[string]ToolTruncationConfig{"read_file": {MaxLines: 3}}
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	exec.SetPerToolTruncation(cfg)
	content := "line1\nline2\nline3\nline4\nline5"
	result, truncated := exec.applyPerToolTruncation(content, "read_file")
	if !truncated {
		t.Error("expected truncated=true for content exceeding MaxLines")
	}
	lines := strings.Split(result, "\n")
	if len(lines) != 3 {
		t.Errorf("expected 3 lines after truncation, got %d", len(lines))
	}
}

func TestApplyPerToolTruncation_ByteTruncation(t *testing.T) {
	cfg := map[string]ToolTruncationConfig{"read_file": {MaxBytes: 5}}
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	exec.SetPerToolTruncation(cfg)
	content := "hello world"
	result, truncated := exec.applyPerToolTruncation(content, "read_file")
	if !truncated {
		t.Error("expected truncated=true for content exceeding MaxBytes")
	}
	if len(result) != 5 {
		t.Errorf("expected 5 bytes after truncation, got %d", len(result))
	}
	if result != "hello" {
		t.Errorf("expected 'hello', got %q", result)
	}
}

func TestApplyPerToolTruncation_UTF8Safe(t *testing.T) {
	cfg := map[string]ToolTruncationConfig{"read_file": {MaxBytes: 4}}
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	exec.SetPerToolTruncation(cfg)
	// "héllo" = 6 bytes: h(1) + é(2) + l(1) + l(1) + o(1). MaxBytes=4 cuts at byte 4 (middle of 'é')
	content := "héllo"
	result, truncated := exec.applyPerToolTruncation(content, "read_file")
	if !truncated {
		t.Error("expected truncated=true")
	}
	// Should walk back to valid UTF-8 boundary: "h" is 1 byte, no room for "hé" (3 bytes)
	// MaxBytes=4 means we try "héll" (4 bytes) but 'é' is at bytes 1-2, so bytes 0-3 = "h" + partial "é"
	// But... wait, "héll" = h(1) + é(2) + l(1) = 4 bytes which is a valid UTF-8 boundary
	// Actually: h=1byte, é=2bytes, l=1byte, l=1byte, o=1byte = 6 bytes total
	// bytes[0:4] = h(1) + é(2) + l(1) = "hél" which is valid UTF-8
	if result != "hél" {
		t.Errorf("expected 'hél', got %q (len=%d)", result, len(result))
	}
}

func TestApplyPerToolTruncation_UTF8SafeWalkBack(t *testing.T) {
	cfg := map[string]ToolTruncationConfig{"read_file": {MaxBytes: 5}}
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	exec.SetPerToolTruncation(cfg)
	// "héllo" = h(1) + é(2) + l(1) + l(1) + o(1) = 6 bytes
	// bytes[0:5] = h(1) + é(2) + l(1) + l(1) = "héll" valid UTF-8
	content := "héllo"
	result, truncated := exec.applyPerToolTruncation(content, "read_file")
	if !truncated {
		t.Error("expected truncated=true")
	}
	if result != "héll" {
		t.Errorf("expected 'héll', got %q", result)
	}
}

func TestHandleTruncationStopReason_BelowThreshold(t *testing.T) {
	cfg := CircuitBreakerConfig{
		RepeatNudgeThreshold:         3,
		RepeatAbortThreshold:         4,
		TruncationAbortThreshold:     5,
		ParseErrorAbortThreshold:     3,
		FruitlessNudgeThreshold:      50,
		FruitlessAbortThreshold:      60,
		FruitlessMaxResultLen:        32,
		SameToolRepeatNudgeThreshold: 50,
		SameToolRepeatAbortThreshold: 60,
		SameToolResultSizeDelta:      64,
	}
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, cfg)
	exec.emitter = &NoopEvents{}
	exec.consecutiveTruncationCount = 1 // below threshold of 5

	resp := &llm.ChatResponse{
		Message: llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{
				{ID: "call_1", Name: "search", Input: []byte(`{"query":"test"}`)},
			},
		},
		Usage: llm.TokenUsage{InputTokens: 100, OutputTokens: 50},
	}
	state := &runState{effectiveMaxSteps: 10}
	cw := newMockContextManager()

	result, act := exec.handleTruncationStopReason(context.Background(), resp, "thinking", state, cw)
	if result != nil {
		t.Error("expected nil result below threshold")
	}
	if act != actionContinue {
		t.Errorf("expected actionContinue, got %v", act)
	}
	if len(state.allSteps) != 1 {
		t.Errorf("expected 1 step to be added, got %d", len(state.allSteps))
	}
	if exec.consecutiveTruncationCount != 2 {
		t.Errorf("expected consecutiveTruncationCount=2, got %d", exec.consecutiveTruncationCount)
	}
}

// --- processBatchTool tests (via processSingleToolCall) ---

func TestProcessBatchTool_ParseError(t *testing.T) {
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	exec.emitter = &NoopEvents{}

	batchInput := json.RawMessage(`invalid json`)
	action := llm.ToolCall{ID: "batch_1", Name: "batch", Input: batchInput}
	resp := &llm.ChatResponse{
		Message:    llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{action}},
		Usage:      llm.TokenUsage{InputTokens: 100, OutputTokens: 50},
		StopReason: "tool_use",
	}
	state := &runState{effectiveMaxSteps: 10}
	cw := newMockContextManager()

	result, act, err := exec.processSingleToolCall(context.Background(), action, 0, resp.Message.ToolCalls, resp, "thinking", state, cw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Error("expected nil result for batch parse error")
	}
	if act != actionNone {
		t.Errorf("expected actionNone, got %v", act)
	}
}

func TestProcessBatchTool_EmptyCalls(t *testing.T) {
	exec := newExecutorDefaultHITL(&mockLLMCaller{}, newMockToolExecutor(), &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	exec.emitter = &NoopEvents{}

	batchInput, _ := json.Marshal(map[string]interface{}{"calls": []interface{}{}})
	action := llm.ToolCall{ID: "batch_1", Name: "batch", Input: batchInput}
	resp := &llm.ChatResponse{
		Message:    llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{action}},
		Usage:      llm.TokenUsage{InputTokens: 100, OutputTokens: 50},
		StopReason: "tool_use",
	}
	state := &runState{effectiveMaxSteps: 10}
	cw := newMockContextManager()

	result, act, err := exec.processSingleToolCall(context.Background(), action, 0, resp.Message.ToolCalls, resp, "thinking", state, cw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Error("expected nil result for empty calls")
	}
	if act != actionNone {
		t.Errorf("expected actionNone, got %v", act)
	}
}

func TestProcessBatchTool_SubCalls(t *testing.T) {
	mockTools := newMockToolExecutor()
	mockTools.results["search"] = tools.ToolResult{Content: "search result"}
	mockTools.results["read"] = tools.ToolResult{Content: "file content"}

	exec := newExecutorDefaultHITL(&mockLLMCaller{}, mockTools, &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	exec.emitter = &NoopEvents{}

	subInput1, _ := json.Marshal(map[string]string{"query": "test"})
	subInput2, _ := json.Marshal(map[string]string{"path": "file.txt"})

	type batchCall struct {
		Tool  string          `json:"tool"`
		Input json.RawMessage `json:"input"`
	}
	batchInput, _ := json.Marshal(map[string][]batchCall{
		"calls": {
			{Tool: "search", Input: subInput1},
			{Tool: "read", Input: subInput2},
		},
	})

	action := llm.ToolCall{ID: "batch_1", Name: "batch", Input: batchInput}
	resp := &llm.ChatResponse{
		Message:    llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{action}},
		Usage:      llm.TokenUsage{InputTokens: 100, OutputTokens: 50},
		StopReason: "tool_use",
	}
	state := &runState{effectiveMaxSteps: 10}
	cw := newMockContextManager()

	result, act, err := exec.processSingleToolCall(context.Background(), action, 0, resp.Message.ToolCalls, resp, "thinking", state, cw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Error("expected nil result")
	}
	if act != actionNone {
		t.Errorf("expected actionNone, got %v", act)
	}
}

func TestProcessBatchTool_NestedBatch(t *testing.T) {
	mockTools := newMockToolExecutor()

	exec := newExecutorDefaultHITL(&mockLLMCaller{}, mockTools, &mockTokenCounter{}, 10, nil, false, ToolResultBudget{}, defaultCircuitBreakerConfig)
	exec.emitter = &NoopEvents{}

	nestedInput, _ := json.Marshal(map[string]string{})

	type batchCall struct {
		Tool  string          `json:"tool"`
		Input json.RawMessage `json:"input"`
	}
	batchInput, _ := json.Marshal(map[string][]batchCall{
		"calls": {
			{Tool: "batch", Input: nestedInput},
		},
	})

	action := llm.ToolCall{ID: "batch_1", Name: "batch", Input: batchInput}
	resp := &llm.ChatResponse{
		Message:    llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{action}},
		Usage:      llm.TokenUsage{InputTokens: 100, OutputTokens: 50},
		StopReason: "tool_use",
	}
	state := &runState{effectiveMaxSteps: 10}
	cw := newMockContextManager()

	result, act, err := exec.processSingleToolCall(context.Background(), action, 0, resp.Message.ToolCalls, resp, "thinking", state, cw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Error("expected nil result for nested batch")
	}
	if act != actionNone {
		t.Errorf("expected actionNone, got %v", act)
	}
}
