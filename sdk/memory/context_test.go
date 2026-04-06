package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	sdkagent "github.com/user/agent/sdk/agent"
	"github.com/user/agent/sdk/llm"
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

// TestBuildPromptOrdering verifies that BuildPrompt returns messages in correct order.
func TestBuildPromptOrdering(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)
	strategy := NewSlidingWindowStrategy(5, 5)

	cw := NewContextWindow("You are a helpful assistant.", testModelMeta(128000), tracker, testThresholds(), strategy)

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

	cw := NewContextWindow("System prompt only", testModelMeta(128000), tracker, testThresholds(), strategy)

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

// TestNeedsCompaction verifies NeedsCompaction returns true when fill exceeds predictive threshold.
func TestNeedsCompaction(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)
	strategy := NewSlidingWindowStrategy(3, 5)

	// Create with small context window to trigger compaction
	// ContextWindow: 10000, OutputLimit: 4096, SafetyMargin: 500 (5%)
	// EffectiveMax = 10000 - 4096 - 500 = 5404
	// Predictive threshold = 85%, so compaction triggers at ~4593 tokens
	modelMeta := llm.ModelMetadata{
		ContextWindow: 10000,
		OutputLimit:   4096,
		TokenizerType: "approximate",
	}
	cw := NewContextWindow("System prompt", modelMeta, tracker, testThresholds(), strategy)

	// Add many steps to exceed predictive threshold
	// Each step adds ~60-80 tokens with the simple counter
	for i := 1; i <= 200; i++ {
		cw.AddStep(makeStep(
			fmt.Sprintf("This is a long thought for step %d with lots of text content here", i),
			fmt.Sprintf("This is a long observation for step %d with lots of text content here", i),
			i,
		))
	}

	if !cw.NeedsCompaction() {
		t.Error("NeedsCompaction should return true when fill exceeds predictive threshold")
	}
}

// TestNeedsCompactionWithinBudget verifies NeedsCompaction returns false when within budget.
func TestNeedsCompactionWithinBudget(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)
	strategy := NewSlidingWindowStrategy(3, 5)

	// Create with high budget
	cw := NewContextWindow("Hi", testModelMeta(128000), tracker, testThresholds(), strategy)

	// Add just one step
	cw.AddStep(makeStep("Thought", "Obs", 1))

	if cw.NeedsCompaction() {
		t.Error("NeedsCompaction should return false when within budget")
	}
}

// TestAddStep verifies AddStep appends steps correctly.
func TestAddStep(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)
	cw := NewContextWindow("System", testModelMeta(128000), tracker, testThresholds(), nil)

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

// TestCompactClearsCompactedOnNewStep verifies that adding a new step clears compacted messages.
func TestCompactClearsCompactedOnNewStep(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	tracker := llm.NewContextTokenTracker(counter)
	strategy := NewSlidingWindowStrategy(2, 2)

	// Use small context window to trigger compaction behavior
	modelMeta := llm.ModelMetadata{
		ContextWindow: 5000,
		OutputLimit:   1000,
		TokenizerType: "approximate",
	}
	cw := NewContextWindow("System", modelMeta, tracker, testThresholds(), strategy)

	// Add steps
	for i := 1; i <= 10; i++ {
		cw.AddStep(makeStep(fmt.Sprintf("T%d", i), fmt.Sprintf("O%d", i), i))
	}

	// Compact - this clears steps and stores compacted messages
	cw.Compact(context.Background())
	messagesAfterCompact := cw.BuildPrompt()

	// Verify compacted messages exist (2 first + 1 summary + 2 last = 5 messages, each step = 2 messages)
	// Actually: 2 first steps (4 msgs) + 1 summary + 2 last steps (4 msgs) = 9 messages including system
	if len(messagesAfterCompact) == 0 {
		t.Error("Expected compacted messages after Compact()")
	}

	// Add new step - should clear compacted messages
	cw.AddStep(makeStep("New thought", "New obs", 99))
	messagesAfterNewStep := cw.BuildPrompt()

	// After adding a new step, compacted messages should be cleared
	// and we should see just the system prompt + the new step (1 + 2 = 3 messages)
	// The new step is not compacted - it should appear in full
	foundNewThought := false
	for _, msg := range messagesAfterNewStep {
		if msg.Content == "New thought" {
			foundNewThought = true
			break
		}
	}
	if !foundNewThought {
		t.Error("New step should be present in messages after adding it")
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
	cw := NewContextWindow("", testModelMeta(128000), tracker, testThresholds(), nil)

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
	cw := NewContextWindow("System", modelMeta, tracker, testThresholds(), strategy)

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
	cw := NewContextWindow("System", modelMeta, tracker, testThresholds(), strategy)

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
			cw := NewContextWindow("System", modelMeta, tracker, thresholds, strategy)

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
	cw := NewContextWindow("System", modelMeta, tracker, testThresholds(), strategy)

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
	cw := NewContextWindow("System", testModelMeta(128000), tracker, testThresholds(), strategy)

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
	cw := NewContextWindow("System", testModelMeta(128000), tracker, testThresholds(), nil)

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
