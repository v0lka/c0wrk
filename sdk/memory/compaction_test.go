package memory

import (
	"context"
	"fmt"
	"strings"
	"testing"

	sdkagent "github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
)

// mockSummarizer returns a deterministic summary for testing.
func mockSummarizer(_ context.Context, text string) (string, error) {
	// Count how many "Step" occurrences to determine block size
	count := strings.Count(text, "Step ")
	// Extract zone name if present
	zoneName := ""
	if strings.Contains(text, "from the distant zone:") {
		zoneName = "distant "
	} else if strings.Contains(text, "from the middle zone:") {
		zoneName = "middle "
	}
	return fmt.Sprintf("SUMMARY: %s%d steps summarized", zoneName, count), nil
}

// createTestSteps creates a slice of test steps with given count.
func createTestSteps(count int) []sdkagent.Step {
	steps := make([]sdkagent.Step, count)
	for i := 0; i < count; i++ {
		steps[i] = sdkagent.Step{
			Thought:     fmt.Sprintf("Thought %d", i+1),
			Action:      llm.ToolCall{ID: fmt.Sprintf("action_%d", i+1), Name: fmt.Sprintf("tool_%d", i+1)},
			Observation: fmt.Sprintf("Observation %d", i+1),
			TokensUsed:  100,
		}
	}
	return steps
}

// --- SummarizationStrategy Tests ---

func TestSummarizationStrategy_CompactsOldSteps(t *testing.T) {
	// Create 20 steps, with blockSize=5 and keepLast=5
	// Should result in: 3 summary blocks (15 steps / 5) + 5 recent steps (10 messages)
	steps := createTestSteps(20)
	strategy := NewSummarizationStrategy(5, 5, 0, mockSummarizer, nil, 0)

	messages := strategy.Compact(context.Background(), steps, 10000)

	// Count summary messages
	summaryCount := 0
	for _, msg := range messages {
		if msg.Role == "system" && strings.HasPrefix(msg.Content, "SUMMARY:") {
			summaryCount++
		}
	}

	// Should have 3 summary blocks (steps 1-5, 6-10, 11-15)
	if summaryCount != 3 {
		t.Errorf("Expected 3 summary blocks, got %d", summaryCount)
	}

	// Verify recent steps are preserved: 5 steps = 5 assistant + 5 tool = 10 messages
	recentMsgCount := 0
	for _, msg := range messages {
		if msg.Role == "assistant" || msg.Role == "tool" {
			recentMsgCount++
		}
	}
	if recentMsgCount != 10 {
		t.Errorf("Expected 10 recent messages (5 steps), got %d", recentMsgCount)
	}
}

func TestSummarizationStrategy_PreservesRecentSteps(t *testing.T) {
	// Create 10 steps with keepLast=5
	// Only the first 5 should be summarized, last 5 kept verbatim
	steps := createTestSteps(10)
	strategy := NewSummarizationStrategy(10, 5, 0, mockSummarizer, nil, 0)

	messages := strategy.Compact(context.Background(), steps, 10000)

	// Verify the recent steps are preserved by checking for their content
	found := make(map[string]bool)
	for _, msg := range messages {
		if msg.Role == "assistant" {
			found[msg.Content] = true
		}
	}

	// The last 5 thoughts (6-10) should be preserved
	for i := 6; i <= 10; i++ {
		thought := fmt.Sprintf("Thought %d", i)
		if !found[thought] {
			t.Errorf("Recent step thought '%s' should be preserved, but was not found", thought)
		}
	}
}

func TestSummarizationStrategy_CallsSummarizer(t *testing.T) {
	// Verify that the summarizer is actually called for each block
	callCount := 0
	countingSummarizer := func(_ context.Context, text string) (string, error) {
		callCount++
		return fmt.Sprintf("SUMMARY: block %d", callCount), nil
	}

	steps := createTestSteps(25)
	strategy := NewSummarizationStrategy(5, 5, 0, countingSummarizer, nil, 0)

	strategy.Compact(context.Background(), steps, 10000)

	// 20 steps to summarize, blockSize=5 -> 4 calls
	expectedCalls := 4
	if callCount != expectedCalls {
		t.Errorf("Expected summarizer to be called %d times, got %d", expectedCalls, callCount)
	}
}

func TestSummarizationStrategy_NoCompactionNeeded(t *testing.T) {
	// When steps <= keepLast, no summarization should happen
	steps := createTestSteps(5)
	strategy := NewSummarizationStrategy(10, 5, 0, mockSummarizer, nil, 0)

	messages := strategy.Compact(context.Background(), steps, 10000)

	// All should be assistant/tool messages, no summaries
	for _, msg := range messages {
		if msg.Role == "system" {
			t.Errorf("No summary messages expected when steps <= keepLast, got: %v", msg)
		}
	}
}

func TestSummarizationStrategy_NilSummarizer(t *testing.T) {
	// When summarizer is nil, should use placeholder
	steps := createTestSteps(15)
	strategy := NewSummarizationStrategy(5, 5, 0, nil, nil, 0)

	messages := strategy.Compact(context.Background(), steps, 10000)

	// Should have placeholder summaries
	foundPlaceholder := false
	for _, msg := range messages {
		if msg.Role == "system" && strings.Contains(msg.Content, "steps summarized") {
			foundPlaceholder = true
			break
		}
	}
	if !foundPlaceholder {
		t.Error("Expected placeholder summary when summarizer is nil")
	}
}

// --- HierarchicalStrategy Tests ---

func TestHierarchicalStrategy_ThreeZones(t *testing.T) {
	// Create 30 steps with default ratios (0.4, 0.3, 0.3)
	// Distant: 12 steps, Middle: 9 steps, Recent: 9 steps
	steps := createTestSteps(30)
	strategy := NewHierarchicalStrategy(0.4, 0.3, 0.3, 0, mockSummarizer, nil, 0)

	messages := strategy.Compact(context.Background(), steps, 10000)

	// Count messages by type
	distantSummaries := 0
	middleSummaries := 0
	recentMessages := 0

	for _, msg := range messages {
		if msg.Role == "system" {
			if strings.Contains(msg.Content, "distant") {
				distantSummaries++
			} else if strings.Contains(msg.Content, "middle") {
				middleSummaries++
			}
		} else {
			recentMessages++
		}
	}

	// Should have at least one distant summary
	if distantSummaries < 1 {
		t.Errorf("Expected at least 1 distant zone summary, got %d", distantSummaries)
	}

	// Should have at least one middle summary
	if middleSummaries < 1 {
		t.Errorf("Expected at least 1 middle zone summary, got %d", middleSummaries)
	}

	// Recent zone should have preserved messages
	if recentMessages == 0 {
		t.Error("Expected recent zone to have preserved messages")
	}
}

func TestHierarchicalStrategy_PreservesRecentZone(t *testing.T) {
	// Create 30 steps with default ratios
	// Recent zone (last 30%) = 9 steps should be kept verbatim
	steps := createTestSteps(30)
	strategy := NewHierarchicalStrategy(0.4, 0.3, 0.3, 0, mockSummarizer, nil, 0)

	messages := strategy.Compact(context.Background(), steps, 10000)

	// Find preserved thoughts (should be from the last 9 steps)
	preservedThoughts := make(map[string]bool)
	for _, msg := range messages {
		if msg.Role == "assistant" {
			preservedThoughts[msg.Content] = true
		}
	}

	// The last few steps should definitely be preserved
	// With 30% recent ratio, steps 22-30 should be in recent zone
	for i := 28; i <= 30; i++ {
		thought := fmt.Sprintf("Thought %d", i)
		if !preservedThoughts[thought] {
			t.Errorf("Recent zone thought '%s' should be preserved, but was not found", thought)
		}
	}
}

func TestHierarchicalStrategy_SmallStepCount(t *testing.T) {
	// With 5 or fewer steps, should return all as messages without summarization
	steps := createTestSteps(5)
	strategy := NewHierarchicalStrategy(0.4, 0.3, 0.3, 0, mockSummarizer, nil, 0)

	messages := strategy.Compact(context.Background(), steps, 10000)

	// All should be assistant/tool messages, no summaries
	for _, msg := range messages {
		if msg.Role == "system" {
			t.Errorf("No summary messages expected for small step count, got: %v", msg)
		}
	}

	// Should have 5 steps * 2 messages = 10 messages
	if len(messages) != 10 {
		t.Errorf("Expected 10 messages for 5 steps, got %d", len(messages))
	}
}

func TestHierarchicalStrategy_DifferentCompressionLevels(t *testing.T) {
	// The distant zone uses larger blocks (15), middle uses smaller (5)
	// Create enough steps to see multiple blocks in middle zone
	steps := createTestSteps(50)
	strategy := NewHierarchicalStrategy(0.4, 0.3, 0.3, 0, mockSummarizer, nil, 0)

	messages := strategy.Compact(context.Background(), steps, 10000)

	// Verify we got summaries from both zones
	hasDistant := false
	hasMiddle := false

	for _, msg := range messages {
		if msg.Role == "system" {
			if strings.Contains(msg.Content, "distant") {
				hasDistant = true
			}
			if strings.Contains(msg.Content, "middle") {
				hasMiddle = true
			}
		}
	}

	if !hasDistant {
		t.Error("Expected distant zone summaries")
	}
	if !hasMiddle {
		t.Error("Expected middle zone summaries")
	}
}

func TestHierarchicalStrategy_NilSummarizer(t *testing.T) {
	// When summarizer is nil, should use placeholder
	steps := createTestSteps(15)
	strategy := NewHierarchicalStrategy(0.4, 0.3, 0.3, 0, nil, nil, 0)

	messages := strategy.Compact(context.Background(), steps, 10000)

	// Should have placeholder summaries
	foundPlaceholder := false
	for _, msg := range messages {
		if msg.Role == "system" && strings.Contains(msg.Content, "zone:") {
			foundPlaceholder = true
			break
		}
	}
	if !foundPlaceholder {
		t.Error("Expected placeholder summary when summarizer is nil")
	}
}

// --- Factory Tests ---

func TestNewCompactionStrategy_AllTypes(t *testing.T) {
	cfg := CompactionConfig{}
	cfg.SlidingWindow.KeepFirst = 3
	cfg.SlidingWindow.KeepLast = 10
	cfg.Summarization.BlockSize = 7
	cfg.Summarization.KeepLast = 5
	cfg.Hierarchical.DistantRatio = 0.4
	cfg.Hierarchical.MiddleRatio = 0.3
	cfg.Hierarchical.RecentRatio = 0.3

	deps := CompactionDeps{
		Summarize: mockSummarizer,
	}

	tests := []struct {
		name         string
		strategyType string
		expectType   string
	}{
		{"SlidingWindow", "sliding_window", "*memory.SlidingWindowStrategy"},
		{"Summarization", "summarization", "*memory.SummarizationStrategy"},
		{"Hierarchical", "hierarchical", "*memory.HierarchicalStrategy"},
		{"Default", "unknown", "*memory.SlidingWindowStrategy"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			strategy := NewCompactionStrategy(tt.strategyType, cfg, deps)
			gotType := fmt.Sprintf("%T", strategy)
			if gotType != tt.expectType {
				t.Errorf("NewCompactionStrategy(%q) = %v, want %v", tt.strategyType, gotType, tt.expectType)
			}
		})
	}
}

func TestNewCompactionStrategy_DefaultValues(t *testing.T) {
	// Test with zero/empty config - should use defaults
	cfg := CompactionConfig{}
	deps := CompactionDeps{
		Summarize: mockSummarizer,
	}

	// Summarization with no config should use default blockSize=10, keepLast=5
	strategy := NewCompactionStrategy("summarization", cfg, deps)
	if strategy == nil {
		t.Fatal("Expected non-nil strategy")
	}

	// Hierarchical with no config should use default ratios
	strategy = NewCompactionStrategy("hierarchical", cfg, deps)
	if strategy == nil {
		t.Fatal("Expected non-nil strategy")
	}
}

// --- Context Cancellation and Token Truncation Tests ---

func TestSummarizationStrategy_RespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	summarizer := func(ctx context.Context, text string) (string, error) {
		return "", ctx.Err()
	}

	steps := createTestSteps(15)
	strategy := NewSummarizationStrategy(5, 5, 0, summarizer, nil, 0)

	messages := strategy.Compact(ctx, steps, 10000)

	// Should have placeholder/error summaries instead of real summaries
	foundErrorSummary := false
	for _, msg := range messages {
		if msg.Role == "system" && strings.Contains(msg.Content, "failed") {
			foundErrorSummary = true
			break
		}
	}
	if !foundErrorSummary {
		t.Error("Expected error summary when context is cancelled")
	}
}

// mockTokenCounter is a simple token counter that returns a configurable count.
type mockTokenCounter struct {
	countPerChar int // tokens per character (for controlling size)
}

func (m *mockTokenCounter) Count(text string) int {
	if m.countPerChar <= 0 {
		return len(text) // 1 token per char by default
	}
	return len(text) * m.countPerChar
}

func (m *mockTokenCounter) CountMessages(msgs []llm.Message) int {
	total := 0
	for _, msg := range msgs {
		total += m.Count(msg.Content)
	}
	return total
}

func TestSummarizationStrategy_TruncatesLargeBlocks(t *testing.T) {
	// Track what text is passed to summarizer
	var receivedText string
	trackingSummarizer := func(_ context.Context, text string) (string, error) {
		receivedText = text
		return "SUMMARY: truncated", nil
	}

	// Create a mock token counter that returns a high count (10 tokens per char)
	mockCounter := &mockTokenCounter{countPerChar: 10}

	// Create steps with large observations
	steps := make([]sdkagent.Step, 10)
	for i := 0; i < 10; i++ {
		steps[i] = sdkagent.Step{
			Thought:     fmt.Sprintf("Thought %d", i+1),
			Action:      llm.ToolCall{ID: fmt.Sprintf("action_%d", i+1), Name: fmt.Sprintf("tool_%d", i+1)},
			Observation: strings.Repeat("x", 1000), // Large observation
			TokensUsed:  100,
		}
	}

	// Create strategy with maxSummarizeTokens = 100 (very low to trigger truncation)
	strategy := NewSummarizationStrategy(5, 5, 0, trackingSummarizer, mockCounter, 100)

	strategy.Compact(context.Background(), steps, 10000)

	// The text should be truncated (100 tokens * 3 chars/token = 300 chars max)
	// Original text would be much larger due to large observations
	if len(receivedText) > 400 { // Allow some buffer for truncation notice
		t.Errorf("Expected truncated text (~300 chars), got %d chars", len(receivedText))
	}

	// Should contain truncation indicator
	if !strings.Contains(receivedText, "truncated") {
		t.Error("Expected truncation indicator in the text passed to summarizer")
	}
}

func TestHierarchicalStrategy_TruncateToTokenBudget(t *testing.T) {
	// Test with small token budget: 10 tokens * 3 chars = 30 chars max
	maxTokens := 10

	tests := []struct {
		name        string
		input       string
		shouldTrunc bool
	}{
		{"short text within budget", "hello", false},
		{"exact budget", strings.Repeat("x", 30), false},
		{"over budget", strings.Repeat("x", 100), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncateToTokenBudget(tt.input, maxTokens)
			if tt.shouldTrunc {
				if !strings.Contains(result, "truncated") {
					t.Error("expected truncation marker")
				}
				if len(result) > 30+50 { // 30 chars + marker
					t.Errorf("truncated result too long: %d chars", len(result))
				}
			} else if result != tt.input {
				t.Errorf("expected unchanged text, got different")
			}
		})
	}
}
