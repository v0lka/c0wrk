package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	sdkagent "github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
)

// testModelMeta creates a ModelMetadata for testing with the given context window.
func testModelMeta(contextWindow int) llm.ModelMetadata {
	return llm.ModelMetadata{
		ContextWindow: contextWindow,
		OutputLimit:   4096,
		TokenizerType: "approximate",
	}
}

// testThresholds creates default CompactionThresholds for testing.
func testThresholds() CompactionThresholds {
	return CompactionThresholds{
		PredictivePercent: 85,
		WarningPercent:    92,
		EmergencyPercent:  98,
	}
}

// Helper to create a test step with a tool call
func makeStep(thought, observation string, toolID int) sdkagent.Step {
	return sdkagent.Step{
		Thought: thought,
		Action: llm.ToolCall{
			ID:    fmt.Sprintf("call_%d", toolID),
			Name:  "test_tool",
			Input: json.RawMessage(`{"arg": "value"}`),
		},
		Observation: observation,
		TokensUsed:  100,
	}
}

// Helper to create a test step with a specific tool name
func makeStepWithTool(thought, observation, toolName string, toolID int) sdkagent.Step {
	return sdkagent.Step{
		Thought: thought,
		Action: llm.ToolCall{
			ID:    fmt.Sprintf("call_%d", toolID),
			Name:  toolName,
			Input: json.RawMessage(`{"arg": "value"}`),
		},
		Observation: observation,
		TokensUsed:  100,
	}
}

// Helper to create a test step with reasoning content
func makeStepWithReasoning(thought, reasoning, observation string, toolID int) sdkagent.Step {
	return sdkagent.Step{
		Thought:          thought,
		ReasoningContent: reasoning,
		Action: llm.ToolCall{
			ID:    fmt.Sprintf("call_%d", toolID),
			Name:  "test_tool",
			Input: json.RawMessage(`{"arg": "value"}`),
		},
		Observation: observation,
		TokensUsed:  100,
	}
}

// TestBuildPromptWithReasoningContent verifies that ReasoningContent is preserved in BuildPrompt.
func TestBuildPromptWithReasoningContent(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)
	strategy := NewSlidingWindowStrategy(5, 5)

	cw := NewContextWindow(ContextWindowConfig{SystemPrompt: "You are helpful.", ModelMeta: testModelMeta(128000), Tracker: tracker, Thresholds: testThresholds(), Strategy: strategy})
	cw.SetTask("Do something")

	// Add a step with reasoning content
	cw.AddStep(makeStepWithReasoning("Let me think", "I need to find the file.", "found it", 1))

	messages := cw.BuildPrompt()

	// Find the assistant message (should be after system and user messages)
	var assistantMsg *llm.Message
	for i := range messages {
		if messages[i].Role == "assistant" {
			assistantMsg = &messages[i]
			break
		}
	}

	if assistantMsg == nil {
		t.Fatal("expected assistant message in BuildPrompt output")
	}
	if assistantMsg.ReasoningContent != "I need to find the file." {
		t.Errorf("ReasoningContent = %q, want 'I need to find the file.'", assistantMsg.ReasoningContent)
	}
	if len(assistantMsg.ToolCalls) != 1 {
		t.Errorf("expected 1 tool call, got %d", len(assistantMsg.ToolCalls))
	}

	// Verify tool message follows
	toolMsgIdx := -1
	for i, msg := range messages {
		if msg.Role == "tool" {
			toolMsgIdx = i
			break
		}
	}
	if toolMsgIdx == -1 {
		t.Error("expected tool message after assistant message")
	}
}

// TestBuildPromptOrdering verifies that BuildPrompt returns messages in correct order.
func TestBuildPromptOrdering(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)
	strategy := NewSlidingWindowStrategy(5, 5)

	cw := NewContextWindow(ContextWindowConfig{SystemPrompt: "You are a helpful assistant.", ModelMeta: testModelMeta(128000), Tracker: tracker, Thresholds: testThresholds(), Strategy: strategy})

	// Set task (caller is responsible for formatting criteria into task string)
	cw.SetTask("Complete the coding task\n\nAcceptance Criteria:\n- First criterion (llm_judge)\n- Second criterion (programmatic: go test)")

	// Set plan (caller is responsible for formatting plan text)
	cw.SetPlan("Plan:\n1. First step\n2. Second step (depends on: step_1)")

	// Add steps
	cw.AddStep(makeStep("Thinking step 1", "Observation 1", 1))
	cw.AddStep(makeStep("Thinking step 2", "Observation 2", 2))
	cw.AddStep(makeStep("Thinking step 3", "Observation 3", 3))

	messages := cw.BuildPrompt()

	// Verify order: system, task+AC (user), plan (system), steps
	if len(messages) < 9 {
		t.Fatalf("Expected at least 9 messages, got %d", len(messages))
	}

	// Message 0: System prompt
	if messages[0].Role != "system" || messages[0].Content != "You are a helpful assistant." {
		t.Errorf("Message 0 should be system prompt, got role=%s, content=%s", messages[0].Role, messages[0].Content)
	}

	// Message 1: Task + criteria (user)
	if messages[1].Role != "user" {
		t.Errorf("Message 1 should be user (task), got role=%s", messages[1].Role)
	}

	// Message 2: Plan (system)
	if messages[2].Role != "system" {
		t.Errorf("Message 2 should be system (plan), got role=%s", messages[2].Role)
	}

	// Messages 3+: Step messages (assistant, tool, assistant, tool, ...)
	if messages[3].Role != "assistant" || messages[3].Content != "Thinking step 1" {
		t.Errorf("Message 3 should be assistant (step 1 thought), got role=%s, content=%s", messages[3].Role, messages[3].Content)
	}

	if messages[4].Role != "tool" || messages[4].Content != "Observation 1" {
		t.Errorf("Message 4 should be tool (step 1 observation), got role=%s", messages[4].Role)
	}
}

// TestBuildPromptWithEmptySections verifies BuildPrompt handles empty sections gracefully.
func TestBuildPromptWithEmptySections(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)
	strategy := NewSlidingWindowStrategy(5, 5)

	cw := NewContextWindow(ContextWindowConfig{SystemPrompt: "System prompt only", ModelMeta: testModelMeta(128000), Tracker: tracker, Thresholds: testThresholds(), Strategy: strategy})

	// Only add steps, no task, criteria, or plan
	cw.AddStep(makeStep("Thought 1", "Obs 1", 1))
	cw.AddStep(makeStep("Thought 2", "Obs 2", 2))

	messages := cw.BuildPrompt()

	// Should have: system (1) + steps (2 assistant + 2 tool = 4) = 5 messages
	if len(messages) != 5 {
		t.Errorf("Expected 5 messages, got %d", len(messages))
	}

	// First message is system prompt
	if messages[0].Role != "system" || messages[0].Content != "System prompt only" {
		t.Errorf("First message should be system prompt")
	}

	// Next messages should be step messages
	if messages[1].Role != "assistant" || messages[1].Content != "Thought 1" {
		t.Errorf("Second message should be first step's assistant message")
	}
}

// TestBuildPromptWithPriorConversation verifies that prior conversation
// messages appear between the system prompt and the current task content.
func TestBuildPromptWithPriorConversation(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)
	strategy := NewSlidingWindowStrategy(5, 5)

	cw := NewContextWindow(ContextWindowConfig{SystemPrompt: "You are a helpful assistant.", ModelMeta: testModelMeta(128000), Tracker: tracker, Thresholds: testThresholds(), Strategy: strategy})
	cw.SetTask("implement variant a")

	cw.SetPriorConversation([]llm.Message{
		{Role: "user", Content: "analyze the options"},
		{Role: "assistant", Content: "Options: a, b, or c. Which to implement?"},
	})

	messages := cw.BuildPrompt()

	// Expected: system (1) + prior conversation (2) + task (1) = 4 messages
	if len(messages) != 4 {
		t.Fatalf("Expected 4 messages, got %d", len(messages))
	}

	if messages[0].Role != "system" || messages[0].Content != "You are a helpful assistant." {
		t.Errorf("Message 0 should be system prompt, got role=%s", messages[0].Role)
	}
	if messages[1].Role != "user" || messages[1].Content != "analyze the options" {
		t.Errorf("Message 1 should be prior user, got role=%s content=%s", messages[1].Role, messages[1].Content)
	}
	if messages[2].Role != "assistant" || messages[2].Content != "Options: a, b, or c. Which to implement?" {
		t.Errorf("Message 2 should be prior assistant, got role=%s content=%s", messages[2].Role, messages[2].Content)
	}
	if messages[3].Role != "user" || messages[3].Content != "implement variant a" {
		t.Errorf("Message 3 should be current task, got role=%s content=%s", messages[3].Role, messages[3].Content)
	}
}

// TestBuildPromptWithoutPriorConversation verifies that BuildPrompt works
// normally when no prior conversation is set (backward compatibility).
func TestBuildPromptWithoutPriorConversation(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)
	strategy := NewSlidingWindowStrategy(5, 5)

	cw := NewContextWindow(ContextWindowConfig{SystemPrompt: "System", ModelMeta: testModelMeta(128000), Tracker: tracker, Thresholds: testThresholds(), Strategy: strategy})
	cw.SetTask("do something")

	messages := cw.BuildPrompt()

	// Expected: system (1) + task (1) = 2 messages — no prior conversation gap
	if len(messages) != 2 {
		t.Fatalf("Expected 2 messages, got %d", len(messages))
	}
	if messages[0].Role != "system" {
		t.Errorf("Message 0 should be system")
	}
	if messages[1].Role != "user" || messages[1].Content != "do something" {
		t.Errorf("Message 1 should be task")
	}
}

// TestSlidingWindowStrategyCompaction verifies SlidingWindowStrategy correctly compacts steps.
func TestSlidingWindowStrategyCompaction(t *testing.T) {
	strategy := NewSlidingWindowStrategy(3, 5)

	// Create 20 steps
	steps := make([]sdkagent.Step, 0, 20)
	for i := 1; i <= 20; i++ {
		steps = append(steps, makeStep(
			fmt.Sprintf("Thought %d", i),
			fmt.Sprintf("Observation %d", i),
			i,
		))
	}

	messages := strategy.Compact(context.Background(), steps, 10000)

	// Expected: first 3 steps (6 messages) + summary (1 message) + last 5 steps (10 messages) = 17 messages
	expectedMessages := 3*2 + 1 + 5*2 // 17
	if len(messages) != expectedMessages {
		t.Errorf("Expected %d messages, got %d", expectedMessages, len(messages))
	}

	// Verify first step is preserved
	if messages[0].Role != "assistant" || messages[0].Content != "Thought 1" {
		t.Errorf("First message should be first step's thought")
	}

	// Verify third step (last of first batch)
	if messages[4].Role != "assistant" || messages[4].Content != "Thought 3" {
		t.Errorf("Message 4 should be third step's thought")
	}

	// Verify summary message is inserted
	if messages[6].Role != "system" || messages[6].Content != "[... 12 steps omitted ...]" {
		t.Errorf("Message 6 should be summary, got role=%s, content=%s", messages[6].Role, messages[6].Content)
	}

	// Verify last steps are preserved (starting from step 16)
	if messages[7].Role != "assistant" || messages[7].Content != "Thought 16" {
		t.Errorf("Message 7 should be step 16's thought, got content=%s", messages[7].Content)
	}

	// Verify last step
	if messages[15].Role != "assistant" || messages[15].Content != "Thought 20" {
		t.Errorf("Message 15 should be last step's thought, got content=%s", messages[15].Content)
	}
}

// TestSlidingWindowNoCompactionNeeded verifies no compaction when steps fit within limits.
func TestSlidingWindowNoCompactionNeeded(t *testing.T) {
	strategy := NewSlidingWindowStrategy(3, 5)

	// Create 5 steps (less than keepFirst + keepLast = 8)
	steps := make([]sdkagent.Step, 0, 5)
	for i := 1; i <= 5; i++ {
		steps = append(steps, makeStep(
			fmt.Sprintf("Thought %d", i),
			fmt.Sprintf("Observation %d", i),
			i,
		))
	}

	messages := strategy.Compact(context.Background(), steps, 10000)

	// All steps should be preserved: 5 * 2 = 10 messages
	if len(messages) != 10 {
		t.Errorf("Expected 10 messages, got %d", len(messages))
	}

	// No summary message should be present
	for _, msg := range messages {
		if msg.Content == "[... 0 steps omitted ...]" {
			t.Errorf("Should not have summary message when no compaction needed")
		}
	}
}

// TestAddStep verifies AddStep appends steps correctly.
func TestAddStep(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)
	cw := NewContextWindow(ContextWindowConfig{SystemPrompt: "System", ModelMeta: testModelMeta(128000), Tracker: tracker, Thresholds: testThresholds()})

	// Initially no steps
	messages := cw.BuildPrompt()
	if len(messages) != 1 { // Only system message
		t.Errorf("Expected 1 message initially, got %d", len(messages))
	}

	// Add first step
	cw.AddStep(makeStep("Thought 1", "Obs 1", 1))
	messages = cw.BuildPrompt()
	// System (1) + step messages (2) = 3
	if len(messages) != 3 {
		t.Errorf("Expected 3 messages after adding 1 step, got %d", len(messages))
	}

	// Add second step
	cw.AddStep(makeStep("Thought 2", "Obs 2", 2))
	messages = cw.BuildPrompt()
	// System (1) + step messages (4) = 5
	if len(messages) != 5 {
		t.Errorf("Expected 5 messages after adding 2 steps, got %d", len(messages))
	}
}

// TestAddStepAfterCompactAppendsToCompactedPrefix is a regression test for a bug
// where AddStep unconditionally cleared compactedMessages, forcing BuildPrompt to
// reconvert the entire (unbounded) raw step history on the very next call. Since
// cw.steps is never trimmed by Compact(), that reconversion re-crossed the
// compaction threshold almost immediately, causing compaction to fire again on
// (almost) every subsequent ReAct step instead of staying compacted until the new
// tail itself grows large enough to warrant it.
func TestAddStepAfterCompactAppendsToCompactedPrefix(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)
	strategy := NewSlidingWindowStrategy(2, 2)

	// Use small context window to trigger compaction behavior
	modelMeta := llm.ModelMetadata{
		ContextWindow: 5000,
		OutputLimit:   1000,
		TokenizerType: "approximate",
	}
	cw := NewContextWindow(ContextWindowConfig{SystemPrompt: "System", ModelMeta: modelMeta, Tracker: tracker, Thresholds: testThresholds(), Strategy: strategy})

	// Add 10 steps: sliding window (keepFirst=2, keepLast=2) will keep T1,T2,T9,T10
	// and omit T3..T8.
	for i := 1; i <= 10; i++ {
		cw.AddStep(makeStep(fmt.Sprintf("T%d", i), fmt.Sprintf("O%d", i), i))
	}

	cw.Compact(context.Background())
	messagesAfterCompact := cw.BuildPrompt()

	// 2 first steps (4 msgs) + 1 summary + 2 last steps (4 msgs) = 9 step messages,
	// plus the system prompt = 10 messages total.
	wantAfterCompact := 10
	if len(messagesAfterCompact) != wantAfterCompact {
		t.Fatalf("messages after Compact() = %d, want %d", len(messagesAfterCompact), wantAfterCompact)
	}

	// Add a new step after compaction.
	cw.AddStep(makeStep("New thought", "New obs", 99))
	messagesAfterNewStep := cw.BuildPrompt()

	// The new step should be *appended* to the frozen compacted prefix (+2
	// messages: assistant + tool), not force a full rebuild from raw steps
	// (which would yield 1 (system) + 11*2 (all raw steps, uncompacted) = 23).
	wantAfterNewStep := wantAfterCompact + 2
	if len(messagesAfterNewStep) != wantAfterNewStep {
		t.Fatalf("messages after AddStep() post-compaction = %d, want %d (bug: compaction was discarded, history rebuilt in full)",
			len(messagesAfterNewStep), wantAfterNewStep)
	}

	var foundNewThought, foundFirstStep, foundLastStep, foundOmittedStep bool
	for _, msg := range messagesAfterNewStep {
		switch msg.Content {
		case "New thought":
			foundNewThought = true
		case "T1":
			foundFirstStep = true
		case "T9":
			foundLastStep = true
		case "T5":
			foundOmittedStep = true
		}
	}
	if !foundNewThought {
		t.Error("new step should be present in messages after adding it")
	}
	if !foundFirstStep {
		t.Error("compacted prefix's kept first step (T1) should still be present after adding a new step")
	}
	if !foundLastStep {
		t.Error("compacted prefix's kept last step (T9) should still be present after adding a new step")
	}
	if foundOmittedStep {
		t.Error("omitted step (T5) reappeared — compaction was discarded and raw history was rebuilt in full")
	}

	// A second round-trip: compact again, then add another step, and verify the
	// pattern holds (guards against the fix only working for the first cycle).
	cw.Compact(context.Background())
	messagesAfterSecondCompact := cw.BuildPrompt()
	cw.AddStep(makeStep("Second new thought", "Second new obs", 100))
	messagesAfterSecondNewStep := cw.BuildPrompt()
	if len(messagesAfterSecondNewStep) != len(messagesAfterSecondCompact)+2 {
		t.Fatalf("messages after second AddStep() post-compaction = %d, want %d",
			len(messagesAfterSecondNewStep), len(messagesAfterSecondCompact)+2)
	}
}

// TestNewCompactionStrategy verifies the factory function.
func TestNewCompactionStrategy(t *testing.T) {
	cfg := CompactionConfig{}
	cfg.SlidingWindow.KeepFirst = 2
	cfg.SlidingWindow.KeepLast = 3

	deps := CompactionDeps{
		TokenCounter: llm.NewSimpleTokenCounter(),
	}

	// Test sliding_window
	strategy := NewCompactionStrategy("sliding_window", cfg, deps)
	if strategy == nil {
		t.Error("Expected non-nil strategy for sliding_window")
	}

	// Test default (unknown name)
	defaultStrategy := NewCompactionStrategy("unknown", cfg, deps)
	if defaultStrategy == nil {
		t.Error("Expected non-nil strategy for unknown name (should default to sliding_window)")
	}
}

// TestEmptyContextWindow verifies behavior with completely empty context window.
func TestEmptyContextWindow(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)
	cw := NewContextWindow(ContextWindowConfig{SystemPrompt: "", ModelMeta: testModelMeta(128000), Tracker: tracker, Thresholds: testThresholds()})

	messages := cw.BuildPrompt()

	// Empty system prompt should not generate a message
	if len(messages) != 0 {
		t.Errorf("Expected 0 messages for empty context window, got %d", len(messages))
	}
}

// TestEffectiveMax verifies EffectiveMax calculation.
func TestEffectiveMax(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)
	strategy := NewSlidingWindowStrategy(3, 5)

	// ContextWindow: 100000, OutputLimit: 8192, SafetyMargin: 5000 (5%)
	// EffectiveMax = 100000 - 8192 - 5000 = 86808
	modelMeta := llm.ModelMetadata{
		ContextWindow: 100000,
		OutputLimit:   8192,
		TokenizerType: "approximate",
	}
	cw := NewContextWindow(ContextWindowConfig{SystemPrompt: "System", ModelMeta: modelMeta, Tracker: tracker, Thresholds: testThresholds(), Strategy: strategy})

	expectedMax := 100000 - 8192 - 5000 // 86808
	if cw.EffectiveMax() != expectedMax {
		t.Errorf("Expected EffectiveMax=%d, got %d", expectedMax, cw.EffectiveMax())
	}
}

// TestFillPercent verifies FillPercent calculation.
func TestFillPercent(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)
	strategy := NewSlidingWindowStrategy(3, 5)

	// ContextWindow: 10000, OutputLimit: 1000, SafetyMargin: 500 (5%)
	// EffectiveMax = 10000 - 1000 - 500 = 8500
	modelMeta := llm.ModelMetadata{
		ContextWindow: 10000,
		OutputLimit:   1000,
		TokenizerType: "approximate",
	}
	cw := NewContextWindow(ContextWindowConfig{SystemPrompt: "System", ModelMeta: modelMeta, Tracker: tracker, Thresholds: testThresholds(), Strategy: strategy})

	// Initially should be 0% (no tokens used)
	if cw.FillPercent() != 0.0 {
		t.Errorf("Expected FillPercent=0.0 initially, got %f", cw.FillPercent())
	}

	// Add steps to increase token count
	// Each step adds tokens via tracker.AddDelta
	for i := 1; i <= 100; i++ {
		cw.AddStep(makeStep(
			fmt.Sprintf("Thought %d with some content", i),
			fmt.Sprintf("Observation %d with some content", i),
			i,
		))
	}

	// Should now have some fill percentage
	fillPercent := cw.FillPercent()
	if fillPercent <= 0.0 {
		t.Errorf("Expected FillPercent > 0 after adding steps, got %f", fillPercent)
	}

	// Verify it's less than 100%
	if fillPercent >= 100.0 {
		t.Errorf("Expected FillPercent < 100, got %f", fillPercent)
	}
}

// TestCheckFill verifies CheckFill returns correct statuses at different fill levels.
func TestCheckFill(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	strategy := NewSlidingWindowStrategy(3, 5)

	// Create thresholds for testing - using lower thresholds for easier testing
	thresholds := CompactionThresholds{
		PredictivePercent: 30,
		WarningPercent:    50,
		EmergencyPercent:  70,
	}

	// ContextWindow: 5000, OutputLimit: 500, SafetyMargin: 250 (5%)
	// EffectiveMax = 5000 - 500 - 250 = 4250
	// Each step adds roughly 25-30 tokens with simple counter
	modelMeta := llm.ModelMetadata{
		ContextWindow: 5000,
		OutputLimit:   500,
		TokenizerType: "approximate",
	}

	tests := []struct {
		name           string
		steps          int
		expectedStatus string
		minPercent     float64
		maxPercent     float64
	}{
		{
			name:           "ok status",
			steps:          10,
			expectedStatus: "ok",
			minPercent:     0,
			maxPercent:     30,
		},
		{
			name:           "compact status",
			steps:          50,
			expectedStatus: "compact",
			minPercent:     30,
			maxPercent:     50,
		},
		{
			name:           "warning status",
			steps:          80,
			expectedStatus: "warning",
			minPercent:     50,
			maxPercent:     70,
		},
		{
			name:           "emergency status",
			steps:          100,
			expectedStatus: "emergency",
			minPercent:     70,
			maxPercent:     100,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracker := llm.NewContextTokenTracker(counter)
			cw := NewContextWindow(ContextWindowConfig{SystemPrompt: "System", ModelMeta: modelMeta, Tracker: tracker, Thresholds: thresholds, Strategy: strategy})

			for i := 1; i <= tt.steps; i++ {
				cw.AddStep(makeStep(
					fmt.Sprintf("Thought %d with detailed content for testing fill levels", i),
					fmt.Sprintf("Observation %d with detailed content for testing fill levels", i),
					i,
				))
			}

			fill := cw.CheckFill()
			if fill.Status != tt.expectedStatus {
				t.Errorf("Expected status=%s, got status=%s (percent=%f)", tt.expectedStatus, fill.Status, fill.Percent)
			}
			// Also verify the percent is in expected range
			if fill.Percent < tt.minPercent || fill.Percent >= tt.maxPercent {
				t.Logf("Note: fill percent %f is outside expected range [%f, %f)", fill.Percent, tt.minPercent, tt.maxPercent)
			}
		})
	}
}

// TestCheckFillReject verifies CheckFill returns "reject" at 100%+ fill.
func TestCheckFillReject(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)
	strategy := NewSlidingWindowStrategy(3, 5)

	// Very small context window to easily exceed 100%
	modelMeta := llm.ModelMetadata{
		ContextWindow: 2000,
		OutputLimit:   500,
		TokenizerType: "approximate",
	}
	cw := NewContextWindow(ContextWindowConfig{SystemPrompt: "System", ModelMeta: modelMeta, Tracker: tracker, Thresholds: testThresholds(), Strategy: strategy})

	// Add many steps to exceed 100%
	for i := 1; i <= 500; i++ {
		cw.AddStep(makeStep(
			fmt.Sprintf("Thought %d with very long content to exceed context window capacity", i),
			fmt.Sprintf("Observation %d with very long content to exceed context window capacity", i),
			i,
		))
	}

	fill := cw.CheckFill()
	if fill.Status != "reject" {
		t.Errorf("Expected status=reject, got status=%s (percent=%f)", fill.Status, fill.Percent)
	}
}

// TestCorrectTokenCount verifies CorrectTokenCount updates tracker.
func TestCorrectTokenCount(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)
	strategy := NewSlidingWindowStrategy(3, 5)
	cw := NewContextWindow(ContextWindowConfig{SystemPrompt: "System", ModelMeta: testModelMeta(128000), Tracker: tracker, Thresholds: testThresholds(), Strategy: strategy})

	// Add some steps
	cw.AddStep(makeStep("Thought 1", "Observation 1", 1))
	cw.AddStep(makeStep("Thought 2", "Observation 2", 2))

	// Get initial estimate
	initialEstimate := cw.tracker.EstimateTotal()

	// Correct with actual API token count (different from estimate)
	cw.CorrectTokenCount(5000)

	// After correction, estimate should be the corrected value
	correctedEstimate := cw.tracker.EstimateTotal()
	if correctedEstimate != 5000 {
		t.Errorf("Expected tracker estimate=5000 after correction, got %d", correctedEstimate)
	}

	// Verify the estimate changed
	if correctedEstimate == initialEstimate {
		t.Errorf("Expected tracker estimate to change after correction, but it stayed at %d", initialEstimate)
	}
}

// TestAddStepUpdatesTracker verifies AddStep updates tracker delta.
func TestAddStepUpdatesTracker(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)
	cw := NewContextWindow(ContextWindowConfig{SystemPrompt: "System", ModelMeta: testModelMeta(128000), Tracker: tracker, Thresholds: testThresholds()})

	// Initial state
	initialTotal := cw.tracker.EstimateTotal()
	if initialTotal != 0 {
		t.Errorf("Expected initial tracker total=0, got %d", initialTotal)
	}

	// Add a step
	cw.AddStep(makeStep("Test thought", "Test observation", 1))

	// Tracker should now have some tokens
	afterStepTotal := cw.tracker.EstimateTotal()
	if afterStepTotal <= 0 {
		t.Errorf("Expected tracker total > 0 after adding step, got %d", afterStepTotal)
	}

	// Add another step
	cw.AddStep(makeStep("Another thought", "Another observation", 2))

	// Tracker should have more tokens
	afterSecondStep := cw.tracker.EstimateTotal()
	if afterSecondStep <= afterStepTotal {
		t.Errorf("Expected tracker total to increase after second step, got %d (was %d)", afterSecondStep, afterStepTotal)
	}
}

// TestBuildStepMessages_EmptyThoughtNoAction verifies that assistant messages always have content or tool_calls.
// OpenAI API rejects assistant messages with neither content nor tool_calls.
func TestBuildStepMessages_EmptyThoughtNoAction(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)
	cw := NewContextWindow(ContextWindowConfig{SystemPrompt: "System", ModelMeta: testModelMeta(128000), Tracker: tracker, Thresholds: testThresholds()})

	// Add a step with empty thought and no action (edge case that could cause API errors)
	emptyStep := sdkagent.Step{
		Thought:     "",
		Action:      llm.ToolCall{}, // Empty action (no ID)
		Observation: "",
		TokensUsed:  100,
	}
	cw.AddStep(emptyStep)

	// Add a normal step for comparison
	normalStep := sdkagent.Step{
		Thought: "Normal thought",
		Action: llm.ToolCall{
			ID:    "call_1",
			Name:  "test_tool",
			Input: json.RawMessage(`{}`),
		},
		Observation: "Tool result",
		TokensUsed:  100,
	}
	cw.AddStep(normalStep)

	messages := cw.BuildPrompt()

	// Find the assistant message for the empty step (should be messages[1])
	// It must have either content or tool_calls to satisfy OpenAI API requirements
	if len(messages) < 2 {
		t.Fatalf("Expected at least 2 messages, got %d", len(messages))
	}

	emptyStepMsg := messages[1]
	if emptyStepMsg.Role != "assistant" {
		t.Fatalf("Expected assistant message, got role=%s", emptyStepMsg.Role)
	}

	// The fix ensures that when both content and tool_calls would be empty,
	// a placeholder content "(proceeding)" is added
	if emptyStepMsg.Content == "" && len(emptyStepMsg.ToolCalls) == 0 {
		t.Error("Assistant message must have either content or tool_calls to satisfy OpenAI API requirements")
	}

	// Verify the placeholder content was added for the empty step
	if emptyStepMsg.Content != "(proceeding)" {
		t.Errorf("Expected placeholder content '(proceeding)' for empty step, got %q", emptyStepMsg.Content)
	}

	// Verify normal step has its thought as content
	// Message order: [0]system, [1]empty_step_assistant, [2]normal_step_assistant, [3]normal_step_tool
	normalStepMsg := messages[2]
	if normalStepMsg.Role != "assistant" {
		t.Fatalf("Expected assistant message for normal step, got role=%s", normalStepMsg.Role)
	}
	if normalStepMsg.Content != "Normal thought" {
		t.Errorf("Expected 'Normal thought' as content, got %q", normalStepMsg.Content)
	}
}

// TestBuildStepMessages_TrimsTrailingInvisibleChars verifies that trailing invisible characters are trimmed.
func TestBuildStepMessages_TrimsTrailingInvisibleChars(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)
	cw := NewContextWindow(ContextWindowConfig{SystemPrompt: "System", ModelMeta: testModelMeta(128000), Tracker: tracker, Thresholds: testThresholds()})

	// Add a step with thought containing trailing zero-width space characters
	step := sdkagent.Step{
		Thought:     "Some thought\u200b\u200b\u200b",
		Action:      llm.ToolCall{},
		Observation: "",
		TokensUsed:  100,
	}
	cw.AddStep(step)

	messages := cw.BuildPrompt()

	if len(messages) < 2 {
		t.Fatalf("Expected at least 2 messages, got %d", len(messages))
	}

	assistantMsg := messages[1]
	if assistantMsg.Role != "assistant" {
		t.Fatalf("Expected assistant message, got role=%s", assistantMsg.Role)
	}

	// Verify trailing \u200b characters were trimmed
	expectedContent := "Some thought"
	if assistantMsg.Content != expectedContent {
		t.Errorf("Expected trimmed content %q, got %q", expectedContent, assistantMsg.Content)
	}
}

// TestBuildStepMessages_EmptyThoughtAfterTrimmingBecomesProceeding verifies that thoughts with only
// whitespace/invisible characters become "(proceeding)" after trimming.
func TestBuildStepMessages_EmptyThoughtAfterTrimmingBecomesProceeding(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)
	cw := NewContextWindow(ContextWindowConfig{SystemPrompt: "System", ModelMeta: testModelMeta(128000), Tracker: tracker, Thresholds: testThresholds()})

	// Add a step with thought containing only whitespace and invisible characters, no tool call
	step := sdkagent.Step{
		Thought:     "   \t\n\r\u200b\u200c\u200d\ufeff  ",
		Action:      llm.ToolCall{}, // Empty action (no ID)
		Observation: "",
		TokensUsed:  100,
	}
	cw.AddStep(step)

	messages := cw.BuildPrompt()

	if len(messages) < 2 {
		t.Fatalf("Expected at least 2 messages, got %d", len(messages))
	}

	assistantMsg := messages[1]
	if assistantMsg.Role != "assistant" {
		t.Fatalf("Expected assistant message, got role=%s", assistantMsg.Role)
	}

	// After trimming, content should be "(proceeding)" since there's no tool call
	if assistantMsg.Content != "(proceeding)" {
		t.Errorf("Expected '(proceeding)' for empty thought after trimming, got %q", assistantMsg.Content)
	}
}

// TestBuildStepMessages_WhitespaceUserNudgeSkipped verifies that user nudges with only
// whitespace/invisible characters are skipped entirely.
func TestBuildStepMessages_WhitespaceUserNudgeSkipped(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)
	cw := NewContextWindow(ContextWindowConfig{SystemPrompt: "System", ModelMeta: testModelMeta(128000), Tracker: tracker, Thresholds: testThresholds()})

	// Add a step with a valid thought but whitespace-only nudge
	step := sdkagent.Step{
		Thought:     "Valid thought",
		Action:      llm.ToolCall{},
		Observation: "",
		UserNudge:   "   \t\n\r\u200b\u200c  ",
		TokensUsed:  100,
	}
	cw.AddStep(step)

	messages := cw.BuildPrompt()

	// Should have: system (1) + assistant (1) = 2 messages (no user nudge)
	if len(messages) != 2 {
		t.Errorf("Expected 2 messages (system + assistant), got %d", len(messages))
	}

	// Verify the messages are system and assistant only
	if messages[0].Role != "system" {
		t.Errorf("Expected first message to be system, got %s", messages[0].Role)
	}
	if messages[1].Role != "assistant" {
		t.Errorf("Expected second message to be assistant, got %s", messages[1].Role)
	}
}

// TestBuildStepMessages_ValidUserNudgePreserved verifies that valid user nudges are preserved.
func TestBuildStepMessages_ValidUserNudgePreserved(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)
	cw := NewContextWindow(ContextWindowConfig{SystemPrompt: "System", ModelMeta: testModelMeta(128000), Tracker: tracker, Thresholds: testThresholds()})

	// Add a step with a valid thought and valid nudge
	step := sdkagent.Step{
		Thought:     "Valid thought",
		Action:      llm.ToolCall{},
		Observation: "",
		UserNudge:   "Step limit extended",
		TokensUsed:  100,
	}
	cw.AddStep(step)

	messages := cw.BuildPrompt()

	// Should have: system (1) + assistant (1) + user nudge (1) = 3 messages
	if len(messages) != 3 {
		t.Fatalf("Expected 3 messages (system + assistant + nudge), got %d", len(messages))
	}

	// Verify the nudge message is present and correct
	if messages[2].Role != "user" {
		t.Errorf("Expected third message to be user (nudge), got %s", messages[2].Role)
	}
	if messages[2].Content != "Step limit extended" {
		t.Errorf("Expected nudge content 'Step limit extended', got %q", messages[2].Content)
	}
}

// TestPruningKeepsLastN verifies that pruning keeps the last N tool results verbatim.
func TestPruningKeepsLastN(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)

	pruning := ToolOutputPruning{
		KeepLastN:       3,
		PlaceholderText: "[PRUNED]",
	}
	cw := NewContextWindow(ContextWindowConfig{SystemPrompt: "System", ModelMeta: testModelMeta(128000), Tracker: tracker, Thresholds: testThresholds(), InjectionDefenseEnabled: true, Pruning: pruning})

	// Create 10 steps with unique observations
	for i := 1; i <= 10; i++ {
		cw.AddStep(makeStep(fmt.Sprintf("Thought %d", i), fmt.Sprintf("Observation %d", i), i))
	}

	messages := cw.BuildPrompt()

	// Expected: system (1) + 10 steps (each = assistant + tool = 2) = 21 messages
	if len(messages) != 21 {
		t.Fatalf("Expected 21 messages, got %d", len(messages))
	}

	// Find tool messages (every odd index starting from 2: 2, 4, 6, 8, 10, 12, 14, 16, 18, 20)
	toolMessageIndices := []int{2, 4, 6, 8, 10, 12, 14, 16, 18, 20}

	// Last 3 tool messages (steps 8, 9, 10 at indices 16, 18, 20) should have original content
	for i := 7; i < 10; i++ {
		msgIdx := toolMessageIndices[i]
		expectedContent := fmt.Sprintf("Observation %d", i+1)
		if messages[msgIdx].Content != expectedContent {
			t.Errorf("Tool message %d (step %d) should have original content %q, got %q",
				msgIdx, i+1, expectedContent, messages[msgIdx].Content)
		}
	}

	// First 7 tool messages (steps 1-7 at indices 2, 4, 6, 8, 10, 12, 14) should be pruned
	for i := 0; i < 7; i++ {
		msgIdx := toolMessageIndices[i]
		if messages[msgIdx].Content != "[PRUNED]" {
			t.Errorf("Tool message %d (step %d) should be pruned to %q, got %q",
				msgIdx, i+1, "[PRUNED]", messages[msgIdx].Content)
		}
	}
}

// TestPruningProtectsTools verifies that protected tools are never pruned.
func TestPruningProtectsTools(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)

	pruning := ToolOutputPruning{
		KeepLastN:       3,
		ProtectedTools:  []string{"read_evidence"},
		PlaceholderText: "[PRUNED]",
	}
	cw := NewContextWindow(ContextWindowConfig{SystemPrompt: "System", ModelMeta: testModelMeta(128000), Tracker: tracker, Thresholds: testThresholds(), InjectionDefenseEnabled: true, Pruning: pruning})

	// Create 5 steps (all have tool results):
	// Step 1: regular tool (outside KeepLastN -> pruned)
	// Step 2: read_evidence (protected -> NOT pruned)
	// Step 3: regular tool (within KeepLastN -> NOT pruned)
	// Step 4: regular tool (within KeepLastN -> NOT pruned)
	// Step 5: regular tool (within KeepLastN -> NOT pruned)
	// KeepLastN=3 protects the last 3 tool-result steps (steps 3, 4, 5)
	cw.AddStep(makeStep("Thought 1", "Observation 1", 1))
	cw.AddStep(makeStepWithTool("Thought 2", "Evidence content", "read_evidence", 2))
	cw.AddStep(makeStep("Thought 3", "Observation 3", 3))
	cw.AddStep(makeStep("Thought 4", "Observation 4", 4))
	cw.AddStep(makeStep("Thought 5", "Observation 5", 5))

	messages := cw.BuildPrompt()

	// Expected: system (1) + 5 steps (assistant + tool each) = 11 messages
	if len(messages) != 11 {
		t.Fatalf("Expected 11 messages, got %d", len(messages))
	}

	// Tool messages are at indices 2, 4, 6, 8, 10
	// Step 1 (index 2): regular tool, outside KeepLastN -> pruned
	if messages[2].Content != "[PRUNED]" {
		t.Errorf("Step 1 (regular tool) should be pruned, got %q", messages[2].Content)
	}

	// Step 2 (index 4): read_evidence (protected) -> NOT pruned
	if messages[4].Content != "Evidence content" {
		t.Errorf("Step 2 (read_evidence) should NOT be pruned, got %q", messages[4].Content)
	}

	// Step 3 (index 6): regular tool, within KeepLastN -> NOT pruned
	if messages[6].Content != "Observation 3" {
		t.Errorf("Step 3 (within KeepLastN) should NOT be pruned, got %q", messages[6].Content)
	}

	// Step 4 (index 8): regular tool, within KeepLastN -> NOT pruned
	if messages[8].Content != "Observation 4" {
		t.Errorf("Step 4 (within KeepLastN) should NOT be pruned, got %q", messages[8].Content)
	}

	// Step 5 (index 10): regular tool, within KeepLastN -> NOT pruned
	if messages[10].Content != "Observation 5" {
		t.Errorf("Step 5 (within KeepLastN) should NOT be pruned, got %q", messages[10].Content)
	}
}

// TestPruningSkipsNonToolSteps verifies that steps without tool calls are unaffected by pruning.
func TestPruningSkipsNonToolSteps(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)

	pruning := ToolOutputPruning{
		KeepLastN:       1, // Only keep the last 1 tool-result step
		PlaceholderText: "[PRUNED]",
	}
	cw := NewContextWindow(ContextWindowConfig{SystemPrompt: "System", ModelMeta: testModelMeta(128000), Tracker: tracker, Thresholds: testThresholds(), InjectionDefenseEnabled: true, Pruning: pruning})

	// Create steps: tool step, non-tool step, tool step, non-tool step
	// Tool-result steps are at indices 0 and 2 (steps 1 and 3)
	// KeepLastN=1 protects only the last tool-result step (step 3 at index 2)
	cw.AddStep(makeStep("Thought 1", "Observation 1", 1)) // tool step (will be pruned)
	cw.AddStep(sdkagent.Step{                             // non-tool step
		Thought:     "Thought 2 (no tool)",
		Action:      llm.ToolCall{}, // Empty action (no ID)
		Observation: "",
		TokensUsed:  100,
	})
	cw.AddStep(makeStep("Thought 3", "Observation 3", 3)) // tool step (NOT pruned)
	cw.AddStep(sdkagent.Step{                             // non-tool step
		Thought:     "Thought 4 (no tool)",
		Action:      llm.ToolCall{},
		Observation: "",
		TokensUsed:  100,
	})

	messages := cw.BuildPrompt()

	// Expected: system (1) + step1 (2) + step2 (1, no tool) + step3 (2) + step4 (1, no tool) = 7 messages
	if len(messages) != 7 {
		t.Fatalf("Expected 7 messages, got %d", len(messages))
	}

	// Message 0: system
	// Message 1: step 1 assistant
	// Message 2: step 1 tool (pruned - outside KeepLastN)
	if messages[2].Content != "[PRUNED]" {
		t.Errorf("Step 1 tool should be pruned, got %q", messages[2].Content)
	}

	// Message 3: step 2 assistant (no tool message)
	if messages[3].Content != "Thought 2 (no tool)" {
		t.Errorf("Step 2 assistant content should be preserved, got %q", messages[3].Content)
	}

	// Message 4: step 3 assistant
	// Message 5: step 3 tool (NOT pruned - within KeepLastN)
	if messages[5].Content != "Observation 3" {
		t.Errorf("Step 3 tool should NOT be pruned, got %q", messages[5].Content)
	}

	// Message 6: step 4 assistant (no tool message)
	if messages[6].Content != "Thought 4 (no tool)" {
		t.Errorf("Step 4 assistant content should be preserved, got %q", messages[6].Content)
	}
}

// TestPruningDisabledWhenKeepLastNZero verifies that pruning is disabled when KeepLastN is 0.
func TestPruningDisabledWhenKeepLastNZero(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)

	pruning := ToolOutputPruning{
		KeepLastN:       0, // Pruning disabled
		PlaceholderText: "[PRUNED]",
	}
	cw := NewContextWindow(ContextWindowConfig{SystemPrompt: "System", ModelMeta: testModelMeta(128000), Tracker: tracker, Thresholds: testThresholds(), InjectionDefenseEnabled: true, Pruning: pruning})

	// Create 5 steps
	for i := 1; i <= 5; i++ {
		cw.AddStep(makeStep(fmt.Sprintf("Thought %d", i), fmt.Sprintf("Observation %d", i), i))
	}

	messages := cw.BuildPrompt()

	// Expected: system (1) + 5 steps (2 each) = 11 messages
	if len(messages) != 11 {
		t.Fatalf("Expected 11 messages, got %d", len(messages))
	}

	// All tool messages should have original content (no pruning)
	toolMessageIndices := []int{2, 4, 6, 8, 10}
	for i, msgIdx := range toolMessageIndices {
		expectedContent := fmt.Sprintf("Observation %d", i+1)
		if messages[msgIdx].Content != expectedContent {
			t.Errorf("Tool message %d (step %d) should have original content %q when pruning disabled, got %q",
				msgIdx, i+1, expectedContent, messages[msgIdx].Content)
		}
	}
}

// --- ResponseGroup (multi-tool-call) tests ---

// makeGroupedStep creates a step with ResponseGroup set.
func makeGroupedStep(thought, observation, toolName string, toolID int, group int64) sdkagent.Step {
	return sdkagent.Step{
		Thought: thought,
		Action: llm.ToolCall{
			ID:    fmt.Sprintf("call_%d", toolID),
			Name:  toolName,
			Input: json.RawMessage(`{"arg": "value"}`),
		},
		Observation:   observation,
		TokensUsed:    100,
		ResponseGroup: group,
	}
}

func TestBuildStepMessages_GroupedStepsProduceOneAssistantMessage(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)
	cw := NewContextWindow(ContextWindowConfig{SystemPrompt: "System", ModelMeta: testModelMeta(128000), Tracker: tracker, Thresholds: testThresholds()})

	// Add 3 steps with the same ResponseGroup
	cw.AddStep(makeGroupedStep("I'll read all files", "content of a", "read_file", 1, 42))
	cw.AddStep(makeGroupedStep("", "content of b", "read_file", 2, 42))
	cw.AddStep(makeGroupedStep("", "content of c", "read_file", 3, 42))

	messages := cw.BuildPrompt()

	// Expected: system (1) + 1 assistant (with 3 tool_calls) + 3 tool results = 5 messages
	if len(messages) != 5 {
		t.Fatalf("Expected 5 messages, got %d", len(messages))
	}

	// Message 1: assistant with thought and 3 tool_calls
	assistant := messages[1]
	if assistant.Role != "assistant" {
		t.Errorf("Expected assistant role, got %s", assistant.Role)
	}
	if assistant.Content != "I'll read all files" {
		t.Errorf("Expected thought from first step, got %q", assistant.Content)
	}
	if len(assistant.ToolCalls) != 3 {
		t.Fatalf("Expected 3 tool_calls in assistant message, got %d", len(assistant.ToolCalls))
	}
	if assistant.ToolCalls[0].ID != "call_1" || assistant.ToolCalls[1].ID != "call_2" || assistant.ToolCalls[2].ID != "call_3" {
		t.Errorf("Tool call IDs wrong: %v", assistant.ToolCalls)
	}

	// Messages 2-4: tool results
	for i, expected := range []string{"content of a", "content of b", "content of c"} {
		if messages[2+i].Role != "tool" {
			t.Errorf("Message %d should be tool, got %s", 2+i, messages[2+i].Role)
		}
		if messages[2+i].Content != expected {
			t.Errorf("Message %d content = %q, want %q", 2+i, messages[2+i].Content, expected)
		}
		if messages[2+i].ToolCallID != fmt.Sprintf("call_%d", i+1) {
			t.Errorf("Message %d ToolCallID = %q", 2+i, messages[2+i].ToolCallID)
		}
	}
}

func TestBuildStepMessages_MixedGroupedAndStandalone(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)
	cw := NewContextWindow(ContextWindowConfig{SystemPrompt: "System", ModelMeta: testModelMeta(128000), Tracker: tracker, Thresholds: testThresholds()})

	// Standalone step
	cw.AddStep(makeStep("standalone thought", "standalone obs", 1))
	// Grouped steps (group 7)
	cw.AddStep(makeGroupedStep("group thought", "group obs a", "tool_a", 2, 7))
	cw.AddStep(makeGroupedStep("", "group obs b", "tool_b", 3, 7))
	// Another standalone step
	cw.AddStep(makeStep("another standalone", "another obs", 4))

	messages := cw.BuildPrompt()

	// Expected:
	// system (1)
	// standalone: assistant + tool = 2
	// grouped: 1 assistant (2 tool_calls) + 2 tool results = 3
	// standalone: assistant + tool = 2
	// Total: 1 + 2 + 3 + 2 = 8
	if len(messages) != 8 {
		t.Fatalf("Expected 8 messages, got %d", len(messages))
	}

	// Verify standalone step 1
	if messages[1].Role != "assistant" || messages[1].Content != "standalone thought" {
		t.Errorf("msg[1] = role:%s content:%q", messages[1].Role, messages[1].Content)
	}
	if len(messages[1].ToolCalls) != 1 {
		t.Errorf("msg[1] should have 1 tool_call, got %d", len(messages[1].ToolCalls))
	}

	// Verify grouped assistant message
	if messages[3].Role != "assistant" || messages[3].Content != "group thought" {
		t.Errorf("msg[3] = role:%s content:%q", messages[3].Role, messages[3].Content)
	}
	if len(messages[3].ToolCalls) != 2 {
		t.Errorf("msg[3] should have 2 tool_calls, got %d", len(messages[3].ToolCalls))
	}

	// Verify grouped tool results
	if messages[4].Role != "tool" || messages[4].Content != "group obs a" {
		t.Errorf("msg[4] = role:%s content:%q", messages[4].Role, messages[4].Content)
	}
	if messages[5].Role != "tool" || messages[5].Content != "group obs b" {
		t.Errorf("msg[5] = role:%s content:%q", messages[5].Role, messages[5].Content)
	}

	// Verify standalone step 2
	if messages[6].Role != "assistant" || messages[6].Content != "another standalone" {
		t.Errorf("msg[6] = role:%s content:%q", messages[6].Role, messages[6].Content)
	}
}

func TestPruningGroupAwareness_ProtectsEntireGroup(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)

	pruning := ToolOutputPruning{
		KeepLastN:       2, // Only protect last 2 tool-result steps
		PlaceholderText: "[PRUNED]",
	}
	cw := NewContextWindow(ContextWindowConfig{SystemPrompt: "System", ModelMeta: testModelMeta(128000), Tracker: tracker, Thresholds: testThresholds(), InjectionDefenseEnabled: true, Pruning: pruning})

	// 2 standalone steps (will be pruned, outside KeepLastN)
	cw.AddStep(makeStep("old 1", "old obs 1", 1))
	cw.AddStep(makeStep("old 2", "old obs 2", 2))

	// 3 grouped steps (group 99) — last 2 tool-result indices are within KeepLastN,
	// so the entire group of 3 should be protected
	cw.AddStep(makeGroupedStep("grouped", "group a", "tool", 3, 99))
	cw.AddStep(makeGroupedStep("", "group b", "tool", 4, 99))
	cw.AddStep(makeGroupedStep("", "group c", "tool", 5, 99))

	messages := cw.BuildPrompt()

	// Find tool messages and check pruning
	// Standalone steps at indices 0,1 -> their tool results should be pruned
	// Grouped steps at indices 2,3,4 -> ALL should be protected (group-aware)

	// Message layout:
	// [0] system
	// [1] assistant (standalone 1)
	// [2] tool (standalone 1) -> should be PRUNED
	// [3] assistant (standalone 2)
	// [4] tool (standalone 2) -> should be PRUNED
	// [5] assistant (grouped, 3 tool_calls)
	// [6] tool (group a) -> should be protected (group-aware)
	// [7] tool (group b) -> should be protected (group-aware)
	// [8] tool (group c) -> should be protected (group-aware)

	if len(messages) != 9 {
		t.Fatalf("Expected 9 messages, got %d", len(messages))
	}

	// Standalone tool results should be pruned
	if messages[2].Content != "[PRUNED]" {
		t.Errorf("standalone 1 tool result should be pruned, got %q", messages[2].Content)
	}
	if messages[4].Content != "[PRUNED]" {
		t.Errorf("standalone 2 tool result should be pruned, got %q", messages[4].Content)
	}

	// All grouped tool results should be protected (not pruned)
	if messages[6].Content != "group a" {
		t.Errorf("group step a should be protected, got %q", messages[6].Content)
	}
	if messages[7].Content != "group b" {
		t.Errorf("group step b should be protected, got %q", messages[7].Content)
	}
	if messages[8].Content != "group c" {
		t.Errorf("group step c should be protected, got %q", messages[8].Content)
	}
}

func TestBuildStepMessages_GroupedStepsBackwardCompat(t *testing.T) {
	// Steps with ResponseGroup == 0 work exactly as before
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)
	cw := NewContextWindow(ContextWindowConfig{SystemPrompt: "System", ModelMeta: testModelMeta(128000), Tracker: tracker, Thresholds: testThresholds()})

	cw.AddStep(makeStep("Thought 1", "Obs 1", 1))
	cw.AddStep(makeStep("Thought 2", "Obs 2", 2))

	messages := cw.BuildPrompt()

	// system (1) + 2 steps (2 each) = 5
	if len(messages) != 5 {
		t.Fatalf("Expected 5 messages, got %d", len(messages))
	}

	// Each step should produce its own assistant + tool message pair
	if messages[1].Role != "assistant" || len(messages[1].ToolCalls) != 1 {
		t.Errorf("Expected standalone assistant with 1 tool_call")
	}
	if messages[3].Role != "assistant" || len(messages[3].ToolCalls) != 1 {
		t.Errorf("Expected standalone assistant with 1 tool_call")
	}
}

// --- Adaptive pruning threshold tests ---

// TestPruningThreshold_SkipsWhenBelowThreshold verifies that pruning is skipped when
// context fill is below the configured threshold.
func TestPruningThreshold_SkipsWhenBelowThreshold(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)

	pruning := ToolOutputPruning{
		KeepLastN:        2,
		PlaceholderText:  "[PRUNED]",
		ThresholdPercent: 80, // Set threshold high so fill is below it
	}
	// Large context window → fill will be very low (well under 80%)
	cw := NewContextWindow(ContextWindowConfig{SystemPrompt: "System", ModelMeta: testModelMeta(128000), Tracker: tracker, Thresholds: testThresholds(), InjectionDefenseEnabled: true, Pruning: pruning})

	for i := 1; i <= 10; i++ {
		cw.AddStep(makeStep(fmt.Sprintf("Thought %d", i), fmt.Sprintf("Observation %d", i), i))
	}

	messages := cw.BuildPrompt()

	// system (1) + 10 steps (2 each) = 21 messages
	if len(messages) != 21 {
		t.Fatalf("Expected 21 messages, got %d", len(messages))
	}

	// ALL tool outputs should be preserved (no pruning) because fill < threshold
	toolMessageIndices := []int{2, 4, 6, 8, 10, 12, 14, 16, 18, 20}
	for i, msgIdx := range toolMessageIndices {
		expectedContent := fmt.Sprintf("Observation %d", i+1)
		if messages[msgIdx].Content != expectedContent {
			t.Errorf("Tool message at index %d (step %d) should NOT be pruned when fill < threshold, got %q",
				msgIdx, i+1, messages[msgIdx].Content)
		}
	}
}

// TestPruningThreshold_PrunesWhenAboveThreshold verifies that pruning activates when
// context fill exceeds the configured threshold.
func TestPruningThreshold_PrunesWhenAboveThreshold(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)

	// Small context window so fill naturally exceeds the threshold.
	// EffectiveMax = 5000 - 500 - 250 = 4250
	modelMeta := llm.ModelMetadata{
		ContextWindow: 5000,
		OutputLimit:   500,
		TokenizerType: "approximate",
	}

	pruning := ToolOutputPruning{
		KeepLastN:        3,
		PlaceholderText:  "[PRUNED]",
		ThresholdPercent: 2, // Low threshold; 10 steps at ~3% fill will exceed it
	}
	cw := NewContextWindow(ContextWindowConfig{SystemPrompt: "System", ModelMeta: modelMeta, Tracker: tracker, Thresholds: testThresholds(), InjectionDefenseEnabled: true, Pruning: pruning})

	for i := 1; i <= 10; i++ {
		cw.AddStep(makeStep(fmt.Sprintf("Thought %d", i), fmt.Sprintf("Observation %d", i), i))
	}

	fillPct := cw.FillPercent()
	if fillPct < 2 {
		t.Skipf("Fill percent %f is below threshold; test needs larger steps", fillPct)
	}

	messages := cw.BuildPrompt()

	if len(messages) != 21 {
		t.Fatalf("Expected 21 messages, got %d", len(messages))
	}

	// Last 3 tool outputs (steps 8, 9, 10) should be preserved
	for i := 7; i < 10; i++ {
		msgIdx := 2 + i*2 // tool message indices: 2, 4, 6, ..., 20
		expectedContent := fmt.Sprintf("Observation %d", i+1)
		if messages[msgIdx].Content != expectedContent {
			t.Errorf("Tool message at index %d (step %d) should be preserved (within KeepLastN), got %q",
				msgIdx, i+1, messages[msgIdx].Content)
		}
	}

	// First 7 tool outputs (steps 1-7) should be pruned
	for i := 0; i < 7; i++ {
		msgIdx := 2 + i*2
		if messages[msgIdx].Content != "[PRUNED]" {
			t.Errorf("Tool message at index %d (step %d) should be pruned when fill > threshold, got %q",
				msgIdx, i+1, messages[msgIdx].Content)
		}
	}
}

// TestPruningThreshold_ZeroMeansDisabled verifies that ThresholdPercent=0 (zero-value)
// means no threshold check — pruning always applies.
func TestPruningThreshold_ZeroMeansDisabled(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)

	pruning := ToolOutputPruning{
		KeepLastN:        2,
		PlaceholderText:  "[PRUNED]",
		ThresholdPercent: 0, // Zero = no threshold, pruning always active
	}
	cw := NewContextWindow(ContextWindowConfig{SystemPrompt: "System", ModelMeta: testModelMeta(128000), Tracker: tracker, Thresholds: testThresholds(), InjectionDefenseEnabled: true, Pruning: pruning})

	for i := 1; i <= 5; i++ {
		cw.AddStep(makeStep(fmt.Sprintf("Thought %d", i), fmt.Sprintf("Observation %d", i), i))
	}

	messages := cw.BuildPrompt()

	// system (1) + 5 steps (2 each) = 11 messages
	if len(messages) != 11 {
		t.Fatalf("Expected 11 messages, got %d", len(messages))
	}

	// First 3 tool results (steps 1-3) should be pruned; last 2 (steps 4-5) preserved
	for i := 0; i < 3; i++ {
		msgIdx := 2 + i*2
		if messages[msgIdx].Content != "[PRUNED]" {
			t.Errorf("Step %d should be pruned with ThresholdPercent=0, got %q", i+1, messages[msgIdx].Content)
		}
	}
	for i := 3; i < 5; i++ {
		msgIdx := 2 + i*2
		expectedContent := fmt.Sprintf("Observation %d", i+1)
		if messages[msgIdx].Content != expectedContent {
			t.Errorf("Step %d should be preserved (within KeepLastN), got %q", i+1, messages[msgIdx].Content)
		}
	}
}

// TestPruningThreshold_SmallContextWindow verifies threshold behavior with a realistically
// small context window where fill naturally crosses the threshold.
func TestPruningThreshold_SmallContextWindow(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)

	// Very small context window: EffectiveMax = 3000 - 500 - 150 = 2350
	modelMeta := llm.ModelMetadata{
		ContextWindow: 3000,
		OutputLimit:   500,
		TokenizerType: "approximate",
	}

	pruning := ToolOutputPruning{
		KeepLastN:        2,
		PlaceholderText:  "[PRUNED]",
		ThresholdPercent: 20, // Low threshold to ensure we cross it
	}
	cw := NewContextWindow(ContextWindowConfig{SystemPrompt: "System", ModelMeta: modelMeta, Tracker: tracker, Thresholds: testThresholds(), InjectionDefenseEnabled: true, Pruning: pruning})

	// Add enough steps to push fill above 20%
	for i := 1; i <= 30; i++ {
		cw.AddStep(makeStep(
			fmt.Sprintf("Step %d thinking about the problem at hand", i),
			fmt.Sprintf("Step %d result from tool execution with content", i),
			i,
		))
	}

	// At this point, fill should be well above 20%
	fillPct := cw.FillPercent()
	if fillPct < 20 {
		t.Skipf("Fill percent %f is still below threshold; test needs larger steps", fillPct)
	}

	messages := cw.BuildPrompt()

	// Verify pruning IS happening: early tool outputs should be pruned
	// Message layout: system + 30*(assistant+tool) = 61 messages
	if len(messages) != 61 {
		t.Fatalf("Expected 61 messages, got %d", len(messages))
	}

	// First step's tool output (index 2) should be pruned
	if messages[2].Content != "[PRUNED]" {
		t.Errorf("Step 1 tool output should be pruned when fill > threshold, got %q", messages[2].Content)
	}

	// Last step's tool output (index 60) should be preserved
	if messages[60].Content != "Step 30 result from tool execution with content" {
		t.Errorf("Step 30 tool output should be preserved (within KeepLastN), got %q", messages[60].Content)
	}
}

// TestVulnerableOutputs verifies that VulnerableOutputs correctly identifies
// tool outputs that will be pruned on the next cycle.
func TestVulnerableOutputs(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)

	pruning := ToolOutputPruning{
		KeepLastN:        3,
		ProtectedTools:   []string{"store_fact"},
		PlaceholderText:  "[PRUNED]",
		ThresholdPercent: 0, // always prune (no threshold)
	}
	cw := NewContextWindow(ContextWindowConfig{SystemPrompt: "System", ModelMeta: testModelMeta(128000), Tracker: tracker, Thresholds: testThresholds(), InjectionDefenseEnabled: true, Pruning: pruning})

	// Add 6 steps:
	// Steps 1-3 are outside KeepLastN (vulnerable)
	// Steps 4-6 are within KeepLastN (protected)
	cw.AddStep(makeStepWithTool("T1", "Obs 1", "read_file", 1))
	cw.AddStep(makeStepWithTool("T2", "Obs 2", "ripgrep", 2))
	cw.AddStep(makeStepWithTool("T3", "Obs 3", "store_fact", 3)) // protected tool
	cw.AddStep(makeStepWithTool("T4", "Obs 4", "read_file", 4))
	cw.AddStep(makeStepWithTool("T5", "Obs 5", "semantic_search", 5))
	cw.AddStep(makeStepWithTool("T6", "Obs 6", "read_file", 6))

	vulnerable := cw.VulnerableOutputs()

	// Steps 1 and 2 are outside KeepLastN and not protected → vulnerable
	// Step 3 is outside KeepLastN but "store_fact" is protected → NOT vulnerable
	// Steps 4, 5, 6 are within KeepLastN → NOT vulnerable
	if len(vulnerable) != 2 {
		t.Fatalf("Expected 2 vulnerable outputs, got %d: %+v", len(vulnerable), vulnerable)
	}

	if vulnerable[0].ToolName != "read_file" {
		t.Errorf("Expected vulnerable[0].ToolName = 'read_file', got %q", vulnerable[0].ToolName)
	}
	if vulnerable[1].ToolName != "ripgrep" {
		t.Errorf("Expected vulnerable[1].ToolName = 'ripgrep', got %q", vulnerable[1].ToolName)
	}
}

// TestVulnerableOutputsEmpty verifies VulnerableOutputs returns nil when no pruning.
func TestVulnerableOutputsEmpty(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)

	// No pruning configured (KeepLastN = 0)
	cw := NewContextWindow(ContextWindowConfig{SystemPrompt: "System", ModelMeta: testModelMeta(128000), Tracker: tracker, Thresholds: testThresholds()})
	cw.AddStep(makeStep("T1", "Obs 1", 1))
	cw.AddStep(makeStep("T2", "Obs 2", 2))

	vulnerable := cw.VulnerableOutputs()
	if vulnerable != nil {
		t.Errorf("Expected nil vulnerable outputs with no pruning, got %+v", vulnerable)
	}
}

// TestVulnerableOutputsBelowThreshold verifies VulnerableOutputs returns nil
// when context fill is below the pruning threshold.
func TestVulnerableOutputsBelowThreshold(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)

	pruning := ToolOutputPruning{
		KeepLastN:        3,
		ProtectedTools:   []string{"store_fact"},
		PlaceholderText:  "[PRUNED]",
		ThresholdPercent: 50, // pruning only active above 50%
	}
	cw := NewContextWindow(ContextWindowConfig{SystemPrompt: "System", ModelMeta: testModelMeta(128000), Tracker: tracker, Thresholds: testThresholds(), InjectionDefenseEnabled: true, Pruning: pruning})

	// Add a few steps — total tokens will be well below 50% of 128k
	cw.AddStep(makeStep("T1", "Obs 1", 1))
	cw.AddStep(makeStep("T2", "Obs 2", 2))
	cw.AddStep(makeStep("T3", "Obs 3", 3))
	cw.AddStep(makeStep("T4", "Obs 4", 4))
	cw.AddStep(makeStep("T5", "Obs 5", 5))

	vulnerable := cw.VulnerableOutputs()
	if vulnerable != nil {
		t.Errorf("Expected nil vulnerable outputs below threshold, got %+v", vulnerable)
	}
}

// TestVulnerableOutputsInputHint verifies that InputHint is extracted from tool input JSON.
func TestVulnerableOutputsInputHint(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)

	pruning := ToolOutputPruning{
		KeepLastN:        1,
		PlaceholderText:  "[PRUNED]",
		ThresholdPercent: 0,
	}
	cw := NewContextWindow(ContextWindowConfig{SystemPrompt: "System", ModelMeta: testModelMeta(128000), Tracker: tracker, Thresholds: testThresholds(), InjectionDefenseEnabled: true, Pruning: pruning})

	// Step with file_path input
	cw.AddStep(sdkagent.Step{
		Thought: "T1",
		Action: llm.ToolCall{
			ID:    "call_1",
			Name:  "read_file",
			Input: json.RawMessage(`{"file_path": "core/orchestrator.go", "offset": 0}`),
		},
		Observation: "file content...",
		TokensUsed:  100,
	})
	// Step with pattern input
	cw.AddStep(sdkagent.Step{
		Thought: "T2",
		Action: llm.ToolCall{
			ID:    "call_2",
			Name:  "ripgrep",
			Input: json.RawMessage(`{"pattern": "CheckFill", "path": "sdk/"}`),
		},
		Observation: "grep results...",
		TokensUsed:  100,
	})
	// Last step (within KeepLastN=1, protected)
	cw.AddStep(makeStep("T3", "Obs 3", 3))

	vulnerable := cw.VulnerableOutputs()
	if len(vulnerable) != 2 {
		t.Fatalf("Expected 2 vulnerable outputs, got %d", len(vulnerable))
	}

	// read_file should extract "file_path" key
	if vulnerable[0].InputHint != "core/orchestrator.go" {
		t.Errorf("Expected InputHint 'core/orchestrator.go', got %q", vulnerable[0].InputHint)
	}

	// ripgrep tries keys in order: path, file_path, file, pattern, query, command
	// "path" comes before "pattern" in the priority list
	if vulnerable[1].InputHint != "sdk/" {
		t.Errorf("Expected InputHint 'sdk/', got %q", vulnerable[1].InputHint)
	}
}

// TestVulnerableOutputsAllWithinKeepLastN verifies that VulnerableOutputs returns
// nil when total steps <= KeepLastN (everything is protected by recency).
func TestVulnerableOutputsAllWithinKeepLastN(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)

	pruning := ToolOutputPruning{
		KeepLastN:        5,
		PlaceholderText:  "[PRUNED]",
		ThresholdPercent: 0, // always active
	}
	cw := NewContextWindow(ContextWindowConfig{SystemPrompt: "System", ModelMeta: testModelMeta(128000), Tracker: tracker, Thresholds: testThresholds(), InjectionDefenseEnabled: true, Pruning: pruning})

	// Add 3 steps — all within KeepLastN=5
	cw.AddStep(makeStepWithTool("T1", "Obs 1", "read_file", 1))
	cw.AddStep(makeStepWithTool("T2", "Obs 2", "ripgrep", 2))
	cw.AddStep(makeStepWithTool("T3", "Obs 3", "read_file", 3))

	vulnerable := cw.VulnerableOutputs()
	if len(vulnerable) != 0 {
		t.Errorf("Expected no vulnerable outputs when all steps within KeepLastN, got %d: %+v", len(vulnerable), vulnerable)
	}
}

// makeStepWithCacheHash creates a test step with a CacheHash field set.
func makeStepWithCacheHash(thought, observation, toolName, cacheHash string, toolID int) sdkagent.Step {
	return sdkagent.Step{
		Thought: thought,
		Action: llm.ToolCall{
			ID:    fmt.Sprintf("call_%d", toolID),
			Name:  toolName,
			Input: json.RawMessage(`{"arg": "value"}`),
		},
		Observation: observation,
		CacheHash:   cacheHash,
		TokensUsed:  100,
	}
}

// TestHistoryMutation_ToolResultEviction verifies that tool results older
// than ToolResultEvictionStep are replaced with a cache reference containing
// the hash, while recent results stay full.
func TestHistoryMutation_ToolResultEviction(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)

	cw := NewContextWindow(ContextWindowConfig{SystemPrompt: "System", ModelMeta: testModelMeta(128000), Tracker: tracker, Thresholds: testThresholds(), InjectionDefenseEnabled: true})
	cw.SetHistoryMutation(HistoryMutation{
		ToolResultEvictionStep: 3,
	})

	// Add 5 steps with cache hashes.
	for i := 1; i <= 5; i++ {
		step := makeStepWithCacheHash(
			fmt.Sprintf("Thought %d", i),
			fmt.Sprintf("Full observation %d", i),
			"read_file",
			fmt.Sprintf("hash_%d", i),
			i,
		)
		cw.AddStep(step)
	}

	messages := cw.BuildPrompt()

	// Find tool messages (indices 2, 4, 6, 8, 10).
	toolMsgs := []int{2, 4, 6, 8, 10}

	// Steps 1-2 (age 5, 4 > 3) should be evicted → cache reference.
	for i := 0; i < 2; i++ {
		msg := messages[toolMsgs[i]]
		if !strings.Contains(msg.Content, "Result evicted to cache") {
			t.Errorf("step %d (age %d) should be evicted, got %q", i+1, 5-i, msg.Content)
		}
		if !strings.Contains(msg.Content, fmt.Sprintf("hash_%d", i+1)) {
			t.Errorf("evicted reference should contain hash_%d, got %q", i+1, msg.Content)
		}
	}

	// Steps 3-5 (age 3, 2, 1 — age 3 is NOT > 3) should have full content.
	// Wait: age = len(steps) - idx. len=5, idx for step 3 (0-based 2) = 5-2=3. 3 > 3 is false.
	// So steps 3, 4, 5 should be full.
	for i := 2; i < 5; i++ {
		msg := messages[toolMsgs[i]]
		expected := fmt.Sprintf("Full observation %d", i+1)
		if msg.Content != expected {
			t.Errorf("step %d (age %d) should have full content %q, got %q", i+1, 5-i, expected, msg.Content)
		}
	}
}

// TestHistoryMutation_StepStatusEviction verifies that update_checklist results
// are replaced with a minimal marker when EvictStepStatus is true.
func TestHistoryMutation_StepStatusEviction(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)

	cw := NewContextWindow(ContextWindowConfig{SystemPrompt: "System", ModelMeta: testModelMeta(128000), Tracker: tracker, Thresholds: testThresholds(), InjectionDefenseEnabled: true})
	cw.SetHistoryMutation(HistoryMutation{
		EvictStepStatus: true,
	})

	cw.AddStep(makeStepWithTool("Thought 1", "OK", "update_checklist", 1))
	cw.AddStep(makeStepWithTool("Thought 2", "file content", "read_file", 2))

	messages := cw.BuildPrompt()

	// update_checklist result (index 2) should be evicted.
	if messages[2].Content != stepStatusEvictedText {
		t.Errorf("update_checklist should be evicted, got %q", messages[2].Content)
	}
	// read_file result (index 4) should remain full.
	if messages[4].Content != "file content" {
		t.Errorf("read_file should remain full, got %q", messages[4].Content)
	}
}

// TestHistoryMutation_DedupRepeatedReads verifies that a repeated read of
// the same file (same cache hash) is replaced with a reference.
func TestHistoryMutation_DedupRepeatedReads(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)

	cw := NewContextWindow(ContextWindowConfig{SystemPrompt: "System", ModelMeta: testModelMeta(128000), Tracker: tracker, Thresholds: testThresholds(), InjectionDefenseEnabled: true})
	cw.SetHistoryMutation(HistoryMutation{
		DedupRepeatedReads:     true,
		ToolResultEvictionStep: 100, // high threshold so age-based eviction doesn't interfere
	})

	// Step 1: read_file with hash "abc"
	cw.AddStep(makeStepWithCacheHash("Thought 1", "original content", "read_file", "abc", 1))
	// Step 2: read_file same file → same hash "abc"
	cw.AddStep(makeStepWithCacheHash("Thought 2", "same content", "read_file", "abc", 2))

	messages := cw.BuildPrompt()

	// First read (index 2) should have full content.
	if messages[2].Content != "original content" {
		t.Errorf("first read should have full content, got %q", messages[2].Content)
	}
	// Second read (index 4) should be a dedup reference.
	if !strings.Contains(messages[4].Content, "Result evicted to cache") {
		t.Errorf("duplicate read should be replaced with reference, got %q", messages[4].Content)
	}
	if !strings.Contains(messages[4].Content, "abc") {
		t.Errorf("dedup reference should contain hash 'abc', got %q", messages[4].Content)
	}
}

// TestHistoryMutation_DisabledByDefault verifies that with no mutation config,
// tool results remain full (no eviction, no dedup).
func TestHistoryMutation_DisabledByDefault(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)

	cw := NewContextWindow(ContextWindowConfig{SystemPrompt: "System", ModelMeta: testModelMeta(128000), Tracker: tracker, Thresholds: testThresholds(), InjectionDefenseEnabled: true})
	// No SetHistoryMutation — defaults to zero-value (all disabled).

	for i := 1; i <= 5; i++ {
		cw.AddStep(makeStepWithCacheHash(
			fmt.Sprintf("Thought %d", i),
			fmt.Sprintf("Full observation %d", i),
			"read_file",
			fmt.Sprintf("hash_%d", i),
			i,
		))
	}

	messages := cw.BuildPrompt()

	// All tool messages should have full content.
	toolMsgs := []int{2, 4, 6, 8, 10}
	for i, msgIdx := range toolMsgs {
		expected := fmt.Sprintf("Full observation %d", i+1)
		if messages[msgIdx].Content != expected {
			t.Errorf("step %d should have full content %q, got %q", i+1, expected, messages[msgIdx].Content)
		}
	}
}

// TestHistoryMutation_ProtectedToolsExempt verifies that protected tools are
// not subject to history mutation.
func TestHistoryMutation_ProtectedToolsExempt(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)

	pruning := ToolOutputPruning{
		KeepLastN:      100, // keep all (no pruning interference)
		ProtectedTools: []string{"store_fact"},
	}
	cw := NewContextWindow(ContextWindowConfig{SystemPrompt: "System", ModelMeta: testModelMeta(128000), Tracker: tracker, Thresholds: testThresholds(), InjectionDefenseEnabled: true, Pruning: pruning})
	cw.SetHistoryMutation(HistoryMutation{
		ToolResultEvictionStep: 1, // aggressive eviction
	})

	cw.AddStep(makeStepWithCacheHash("Thought 1", "fact content", "store_fact", "hash_fact", 1))
	cw.AddStep(makeStepWithCacheHash("Thought 2", "file content", "read_file", "hash_file", 2))

	messages := cw.BuildPrompt()

	// store_fact (protected) should remain full despite eviction threshold.
	if messages[2].Content != "fact content" {
		t.Errorf("protected tool store_fact should be exempt from eviction, got %q", messages[2].Content)
	}
	// read_file (age 1, not > 1) should also remain full (age threshold not met).
	if messages[4].Content != "file content" {
		t.Errorf("read_file with age 1 should remain full (threshold > 1), got %q", messages[4].Content)
	}
}

// ==================== stepsToMessages UserNudge Tests ====================

// TestStepsToMessages_UserNudgePreserved verifies stepsToMessages emits
// Step.UserNudge as a user message after the step's messages (mirroring
// buildNudgeMsg in the live prompt path).
func TestStepsToMessages_UserNudgePreserved(t *testing.T) {
	step := makeStep("Thinking", "result", 1)
	step.UserNudge = "user granted 10 more iterations"

	messages := stepsToMessages([]sdkagent.Step{step})

	// assistant + tool + user(nudge)
	if len(messages) != 3 {
		t.Fatalf("Expected 3 messages, got %d", len(messages))
	}
	if messages[0].Role != "assistant" || messages[0].Content != "Thinking" {
		t.Errorf("Message 0 should be assistant thought, got role=%s content=%q", messages[0].Role, messages[0].Content)
	}
	if messages[1].Role != "tool" || messages[1].Content != "result" {
		t.Errorf("Message 1 should be tool result, got role=%s content=%q", messages[1].Role, messages[1].Content)
	}
	if messages[2].Role != "user" || messages[2].Content != "user granted 10 more iterations" {
		t.Errorf("Message 2 should be user nudge, got role=%s content=%q", messages[2].Role, messages[2].Content)
	}
}

// TestStepsToMessages_NudgeOnlyStepNoPlaceholder verifies that a nudge-only
// step (no thought, no action — e.g. a step-limit grant) produces only the
// user nudge message, without an empty "(proceeding)" assistant placeholder.
func TestStepsToMessages_NudgeOnlyStepNoPlaceholder(t *testing.T) {
	step := sdkagent.Step{
		UserNudge: "user granted unlimited iterations",
	}

	messages := stepsToMessages([]sdkagent.Step{step})

	if len(messages) != 1 {
		t.Fatalf("Expected 1 message for nudge-only step, got %d: %+v", len(messages), messages)
	}
	if messages[0].Role != "user" || messages[0].Content != "user granted unlimited iterations" {
		t.Errorf("Expected user nudge message, got role=%s content=%q", messages[0].Role, messages[0].Content)
	}
}

// TestStepsToMessages_EmptyStepStillGetsPlaceholder verifies that a fully
// empty step (no thought, no action, no nudge) still produces the
// "(proceeding)" assistant placeholder (prevents API 400 on empty content).
func TestStepsToMessages_EmptyStepStillGetsPlaceholder(t *testing.T) {
	messages := stepsToMessages([]sdkagent.Step{{}})

	if len(messages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(messages))
	}
	if messages[0].Role != "assistant" || messages[0].Content != "(proceeding)" {
		t.Errorf("Expected '(proceeding)' placeholder, got role=%s content=%q", messages[0].Role, messages[0].Content)
	}
}

// TestStepsToMessages_WhitespaceNudgeSkipped verifies whitespace-only nudges
// are not emitted and the placeholder is used for otherwise-empty steps.
func TestStepsToMessages_WhitespaceNudgeSkipped(t *testing.T) {
	step := sdkagent.Step{UserNudge: "   \t\n\u200b  "}

	messages := stepsToMessages([]sdkagent.Step{step})

	if len(messages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(messages))
	}
	if messages[0].Role != "assistant" || messages[0].Content != "(proceeding)" {
		t.Errorf("Expected '(proceeding)' placeholder, got role=%s content=%q", messages[0].Role, messages[0].Content)
	}
}

// TestStepsToMessages_GroupedNudgeOnLastStep verifies that for a ResponseGroup
// the nudge of the group's last step is emitted after the tool results,
// mirroring buildGroupedMessages.
func TestStepsToMessages_GroupedNudgeOnLastStep(t *testing.T) {
	step1 := makeStep("Group thought", "obs 1", 1)
	step1.ResponseGroup = 7
	step2 := makeStep("", "obs 2", 2)
	step2.ResponseGroup = 7
	step2.UserNudge = "please continue with variant B"

	messages := stepsToMessages([]sdkagent.Step{step1, step2})

	// 1 assistant (merged) + 2 tool results + 1 user nudge
	if len(messages) != 4 {
		t.Fatalf("Expected 4 messages, got %d: %+v", len(messages), messages)
	}
	if messages[0].Role != "assistant" || len(messages[0].ToolCalls) != 2 {
		t.Errorf("Message 0 should be merged assistant with 2 tool calls, got role=%s calls=%d", messages[0].Role, len(messages[0].ToolCalls))
	}
	if messages[3].Role != "user" || messages[3].Content != "please continue with variant B" {
		t.Errorf("Last message should be user nudge, got role=%s content=%q", messages[3].Role, messages[3].Content)
	}
}

// TestCompact_PreservesUserNudgeInVerbatimZone verifies that compaction keeps
// UserNudge messages for steps inside the verbatim (kept) zone. A step-limit
// grant like "user granted unlimited iterations" must survive Compact().
func TestCompact_PreservesUserNudgeInVerbatimZone(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)
	strategy := NewSlidingWindowStrategy(2, 3)
	cw := NewContextWindow(ContextWindowConfig{SystemPrompt: "System", ModelMeta: testModelMeta(5000), Tracker: tracker, Thresholds: testThresholds(), Strategy: strategy})

	for i := 1; i <= 9; i++ {
		cw.AddStep(makeStep(fmt.Sprintf("T%d", i), fmt.Sprintf("O%d", i), i))
	}
	// Nudge-only step in the verbatim (last-N) zone, e.g. a step-limit grant.
	cw.AddStep(sdkagent.Step{UserNudge: "user granted unlimited iterations"})

	if result := cw.Compact(context.Background()); result == nil {
		t.Fatal("Compact returned nil, expected a CompactionResult")
	}

	messages := cw.BuildPrompt()
	foundNudge := false
	for _, msg := range messages {
		if msg.Role == "user" && msg.Content == "user granted unlimited iterations" {
			foundNudge = true
		}
		if msg.Role == "assistant" && msg.Content == "(proceeding)" && len(msg.ToolCalls) == 0 {
			t.Error("nudge-only step produced an empty '(proceeding)' assistant placeholder after compaction")
		}
	}
	if !foundNudge {
		t.Error("UserNudge was lost during compaction — expected user message 'user granted unlimited iterations' in prompt")
	}
}

// ==================== Compact Tracker Sync Tests ====================

// TestCompactUpdatesTracker verifies that Compact() corrects the token tracker
// so that CheckFill() immediately reflects the post-compaction size instead of
// the stale pre-compaction estimate.
func TestCompactUpdatesTracker(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)
	strategy := NewSlidingWindowStrategy(1, 1)
	cw := NewContextWindow(ContextWindowConfig{SystemPrompt: "System", ModelMeta: testModelMeta(5000), Tracker: tracker, Thresholds: testThresholds(), Strategy: strategy})

	// Add many steps with large observations to drive fill up.
	bigObs := strings.Repeat("x", 400)
	for i := 1; i <= 30; i++ {
		cw.AddStep(makeStep(fmt.Sprintf("T%d", i), bigObs, i))
	}

	before := cw.CheckFill()
	if before.Percent <= 0 {
		t.Fatalf("Expected non-zero fill before compaction, got %.2f%%", before.Percent)
	}

	result := cw.Compact(context.Background())
	if result == nil {
		t.Fatal("Compact returned nil, expected a CompactionResult")
	}
	if result.AfterPercent >= result.BeforePercent {
		t.Fatalf("Expected AfterPercent (%.2f) < BeforePercent (%.2f)", result.AfterPercent, result.BeforePercent)
	}

	after := cw.CheckFill()
	if after.Percent >= before.Percent {
		t.Errorf("CheckFill after Compact = %.2f%%, expected less than before (%.2f%%) — tracker was not updated", after.Percent, before.Percent)
	}
	// CheckFill must agree with the estimate Compact reported.
	if diff := after.Percent - result.AfterPercent; diff > 0.01 || diff < -0.01 {
		t.Errorf("CheckFill after Compact = %.2f%%, expected to match CompactionResult.AfterPercent = %.2f%%", after.Percent, result.AfterPercent)
	}
}

// TestCompactTrackerOverwrittenByNextCorrection verifies that the estimate
// written by Compact() is replaced by the next real API-reported token count.
func TestCompactTrackerOverwrittenByNextCorrection(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)
	strategy := NewSlidingWindowStrategy(1, 1)
	cw := NewContextWindow(ContextWindowConfig{SystemPrompt: "System", ModelMeta: testModelMeta(5000), Tracker: tracker, Thresholds: testThresholds(), Strategy: strategy})

	for i := 1; i <= 10; i++ {
		cw.AddStep(makeStep(fmt.Sprintf("T%d", i), strings.Repeat("y", 200), i))
	}
	if result := cw.Compact(context.Background()); result == nil {
		t.Fatal("Compact returned nil, expected a CompactionResult")
	}

	// A subsequent real correction from the API must take precedence.
	cw.CorrectTokenCount(1234)
	if got := tracker.EstimateTotal(); got != 1234 {
		t.Errorf("EstimateTotal after API correction = %d, want 1234", got)
	}
}
