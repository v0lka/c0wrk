package memory

import (
	"strings"
	"testing"

	"github.com/v0lka/sp4rk/llm"
)

func TestCompactConversationHistory_UnderBudget(t *testing.T) {
	messages := []llm.Message{
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi there!"},
	}
	counter := llm.NewSimpleTokenCounter()
	budget := 1000

	result, err := compactConversationHistory(messages, budget, counter, 0.75)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 messages when under budget, got %d", len(result))
	}
	if result[0].Role != "user" || result[0].Content != "Hello" {
		t.Errorf("expected first message to be user:Hello, got %s:%s", result[0].Role, result[0].Content)
	}
	if result[1].Role != "assistant" || result[1].Content != "Hi there!" {
		t.Errorf("expected second message to be assistant:Hi there!, got %s:%s", result[1].Role, result[1].Content)
	}
}

func TestCompactConversationHistory_EmptyMessages(t *testing.T) {
	counter := llm.NewSimpleTokenCounter()
	result, err := compactConversationHistory(nil, 100, counter, 0.75)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Fatalf("expected 0 messages for nil input, got %d", len(result))
	}

	result, err = compactConversationHistory([]llm.Message{}, 100, counter, 0.75)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Fatalf("expected 0 messages for empty input, got %d", len(result))
	}
}

func TestCompactConversationHistory_ZeroBudget(t *testing.T) {
	messages := []llm.Message{
		{Role: "user", Content: "Hello"},
	}
	counter := llm.NewSimpleTokenCounter()
	result, err := compactConversationHistory(messages, 0, counter, 0.75)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 message when budget is 0 (no budget — return as-is), got %d", len(result))
	}
}

func TestCompactConversationHistory_OverBudget(t *testing.T) {
	// Create many messages that will exceed a small budget.
	messages := make([]llm.Message, 0, 200)
	for i := 0; i < 100; i++ {
		messages = append(messages,
			llm.Message{Role: "user", Content: "This is a user message about task " + strings.Repeat("x", 50)},
			llm.Message{Role: "assistant", Content: "This is an assistant response about task " + strings.Repeat("x", 50)})
	}

	counter := llm.NewSimpleTokenCounter()
	totalTokens := counter.CountMessages(messages)
	if totalTokens <= 2000 {
		t.Fatalf("test setup error: expected totalTokens > 2000, got %d", totalTokens)
	}

	// Budget is smaller than total — should trigger compaction.
	budget := totalTokens / 10 // ~10% of total tokens
	result, err := compactConversationHistory(messages, budget, counter, 0.75)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Result should have fewer messages than original.
	if len(result) >= len(messages) {
		t.Errorf("expected compacted result to have fewer messages, got %d vs %d", len(result), len(messages))
	}

	// First message should be a system summary.
	if len(result) == 0 {
		t.Fatal("expected non-empty compacted result")
	}
	if result[0].Role != "system" {
		t.Errorf("expected first message to be system (summary), got %s", result[0].Role)
	}

	// Summary should contain "Previous conversation history (summarised)"
	if !strings.Contains(result[0].Content, "Previous conversation history") {
		t.Errorf("expected summary to contain 'Previous conversation history', got: %s", result[0].Content[:min(100, len(result[0].Content))])
	}

	// Result tokens should be significantly less than original.
	compactedTokens := counter.CountMessages(result)
	if float64(compactedTokens) >= float64(totalTokens)*0.5 {
		t.Errorf("expected compacted tokens (%d) to be less than 50%% of original (%d)", compactedTokens, totalTokens)
	}

	// Recent messages should be preserved as user/assistant (not summarised).
	foundUser := false
	foundAssistant := false
	for _, msg := range result[1:] {
		if msg.Role == "user" {
			foundUser = true
		}
		if msg.Role == "assistant" {
			foundAssistant = true
		}
	}
	if !foundUser || !foundAssistant {
		t.Errorf("expected at least one user and one assistant message in recent portion, found user=%v assistant=%v", foundUser, foundAssistant)
	}
}

func TestCompactConversationHistory_SmallBudget(t *testing.T) {
	// Very small budget — all messages go to summary, nothing in recent.
	messages := []llm.Message{
		{Role: "user", Content: "Hello world"},
		{Role: "assistant", Content: "Hi!"},
		{Role: "user", Content: "How are you?"},
		{Role: "assistant", Content: "I'm fine"},
	}

	counter := llm.NewSimpleTokenCounter()
	budget := 5 // Very small

	result, err := compactConversationHistory(messages, budget, counter, 0.75)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) == 0 {
		t.Fatal("expected non-empty result even with small budget")
	}

	// First message should be system summary.
	if result[0].Role != "system" {
		t.Errorf("expected first message to be system, got %s", result[0].Role)
	}

	// With such a small budget, only the summary fits — no recent messages.
	// This is acceptable behaviour.
}

func TestCompactConversationHistory_InvalidRatio(t *testing.T) {
	messages := []llm.Message{
		{Role: "user", Content: "Hello"},
	}
	counter := llm.NewSimpleTokenCounter()

	// Ratio 0 should return an error.
	_, err := compactConversationHistory(messages, 100, counter, 0)
	if err == nil {
		t.Error("expected error for keepRecentRatio=0")
	}

	// Ratio 1.0 should return an error.
	_, err = compactConversationHistory(messages, 100, counter, 1.0)
	if err == nil {
		t.Error("expected error for keepRecentRatio=1.0")
	}

	// Ratio > 1.0 should return an error.
	_, err = compactConversationHistory(messages, 100, counter, 1.5)
	if err == nil {
		t.Error("expected error for keepRecentRatio=1.5")
	}
}

func TestBuildConversationSummary(t *testing.T) {
	messages := []llm.Message{
		{Role: "user", Content: "Write a REST API"},
		{Role: "assistant", Content: "I'll create the API endpoints"},
		{Role: "system", Content: "System message should be ignored"},
		{Role: "user", Content: "Add authentication"},
		{Role: "assistant", Content: "Adding JWT auth"},
		{Role: "tool", Content: "Tool result should be ignored"},
	}

	summary := buildConversationSummary(messages)

	if !strings.Contains(summary, "Write a REST API") {
		t.Errorf("expected summary to contain 'Write a REST API', got: %s", summary)
	}
	if !strings.Contains(summary, "I'll create the API endpoints") {
		t.Errorf("expected summary to contain assistant response, got: %s", summary)
	}
	if !strings.Contains(summary, "Add authentication") {
		t.Errorf("expected summary to contain 'Add authentication', got: %s", summary)
	}
	if !strings.Contains(summary, "Adding JWT auth") {
		t.Errorf("expected summary to contain assistant response, got: %s", summary)
	}
	if strings.Contains(summary, "System message") {
		t.Errorf("expected system messages to be excluded from summary, got: %s", summary)
	}
	if strings.Contains(summary, "Tool result") {
		t.Errorf("expected tool messages to be excluded from summary, got: %s", summary)
	}

	// Empty messages
	empty := buildConversationSummary(nil)
	if empty != "" {
		t.Errorf("expected empty summary for nil messages, got: %s", empty)
	}
	empty2 := buildConversationSummary([]llm.Message{})
	if empty2 != "" {
		t.Errorf("expected empty summary for empty messages, got: %s", empty2)
	}
}

func TestBuildConversationSummary_OnlyNonUser(t *testing.T) {
	// Only system and tool messages — no user messages.
	messages := []llm.Message{
		{Role: "system", Content: "System prompt"},
		{Role: "tool", Content: "Tool output"},
	}
	summary := buildConversationSummary(messages)
	if !strings.Contains(summary, "No previous user messages") {
		t.Errorf("expected 'No previous user messages' for non-user-only input, got: %s", summary)
	}
}

func TestTruncateSummaryContent(t *testing.T) {
	short := truncateSummaryContent("Hello", 20)
	if short != "Hello" {
		t.Errorf("expected 'Hello', got %q", short)
	}

	long := truncateSummaryContent("This is a very long text that should be truncated at some point", 30)
	if len(long) > 30 {
		t.Errorf("expected truncated text <= 30 chars, got %d: %q", len(long), long)
	}
	if !strings.HasSuffix(long, "...") {
		t.Errorf("expected truncated text to end with '...', got: %q", long)
	}
}
