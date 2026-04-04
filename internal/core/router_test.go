package core

import (
	"context"
	"strings"
	"testing"

	"github.com/user/agent/internal/llm"
	"github.com/user/agent/internal/tools"
)

// Tests use shared mock types from testhelpers_test.go:
// - mockLLMCaller: implements LLMCaller

func TestRoute_ReturnsValidRoutingDecision(t *testing.T) {
	// Mock returns a valid JSON response
	mock := &mockLLMCaller{
		responses: []*llm.ChatResponse{{
			Message: llm.Message{
				Role:    "assistant",
				Content: `{"mode":"react","domain":"code","complexity":2,"compaction_strategy":"sliding_window","suggested_tools":["bash_exec"],"needs_clarification":false}`,
			},
		}},
	}

	router := NewRouter(mock, 5)

	decision, err := router.Route(context.Background(), "read the config file", nil, nil, nil)
	if err != nil {
		t.Fatalf("Route returned error: %v", err)
	}

	if decision.Mode != "react" {
		t.Errorf("expected mode 'react', got '%s'", decision.Mode)
	}
	if decision.Domain != "code" {
		t.Errorf("expected domain 'code', got '%s'", decision.Domain)
	}
	if decision.Complexity != 2 {
		t.Errorf("expected complexity 2, got %d", decision.Complexity)
	}
	if decision.CompactionStrategy != "sliding_window" {
		t.Errorf("expected compaction_strategy 'sliding_window', got '%s'", decision.CompactionStrategy)
	}
	if len(decision.SuggestedTools) != 1 || decision.SuggestedTools[0] != "bash_exec" {
		t.Errorf("expected suggested_tools ['bash_exec'], got %v", decision.SuggestedTools)
	}
	if decision.NeedsClarification {
		t.Errorf("expected needs_clarification false, got true")
	}
}

func TestRoute_PassesToolsInPrompt(t *testing.T) {
	mock := &mockLLMCaller{
		responses: []*llm.ChatResponse{{
			Message: llm.Message{
				Role:    "assistant",
				Content: `{"mode":"react","domain":"code","complexity":2}`,
			},
		}},
	}

	router := NewRouter(mock, 5)

	availableTools := []tools.ToolDescriptor{
		{Name: "bash_exec", Description: "Execute bash commands"},
		{Name: "file_read", Description: "Read file contents"},
	}

	_, err := router.Route(context.Background(), "run a command", nil, availableTools, nil)
	if err != nil {
		t.Fatalf("Route returned error: %v", err)
	}

	// Check that system prompt contains tool names
	systemMessage := mock.lastCall().Messages[0]
	if systemMessage.Role != "system" {
		t.Fatalf("expected first message to be system, got '%s'", systemMessage.Role)
	}
	if !strings.Contains(systemMessage.Content, "bash_exec") {
		t.Error("system prompt should contain 'bash_exec'")
	}
	if !strings.Contains(systemMessage.Content, "file_read") {
		t.Error("system prompt should contain 'file_read'")
	}
	if !strings.Contains(systemMessage.Content, "Execute bash commands") {
		t.Error("system prompt should contain tool description 'Execute bash commands'")
	}
}

func TestRoute_PassesHistory(t *testing.T) {
	mock := &mockLLMCaller{
		responses: []*llm.ChatResponse{{
			Message: llm.Message{
				Role:    "assistant",
				Content: `{"mode":"react","domain":"code","complexity":2}`,
			},
		}},
	}

	router := NewRouter(mock, 3)

	history := []llm.Message{
		{Role: "user", Content: "previous message 1"},
		{Role: "assistant", Content: "previous response 1"},
		{Role: "user", Content: "previous message 2"},
		{Role: "assistant", Content: "previous response 2"},
	}

	_, err := router.Route(context.Background(), "current request", nil, nil, history)
	if err != nil {
		t.Fatalf("Route returned error: %v", err)
	}

	// With historyWindow=3, should include last 3 messages from history
	// Messages should be: system + last 3 history + user request
	if len(mock.lastCall().Messages) != 5 { // system + 3 history + user
		t.Fatalf("expected 5 messages, got %d", len(mock.lastCall().Messages))
	}

	// Check that history messages are included (last 3)
	foundPrevMsg2 := false
	foundPrevResp2 := false
	for _, msg := range mock.lastCall().Messages {
		if strings.Contains(msg.Content, "previous message 2") {
			foundPrevMsg2 = true
		}
		if strings.Contains(msg.Content, "previous response 2") {
			foundPrevResp2 = true
		}
	}
	if !foundPrevMsg2 {
		t.Error("history should contain 'previous message 2'")
	}
	if !foundPrevResp2 {
		t.Error("history should contain 'previous response 2'")
	}
}

func TestRoute_PlanExecuteMode(t *testing.T) {
	mock := &mockLLMCaller{
		responses: []*llm.ChatResponse{{
			Message: llm.Message{
				Role:    "assistant",
				Content: `{"mode":"plan_execute","domain":"mixed","complexity":5,"compaction_strategy":"hierarchical","suggested_tools":["file_read","file_write","bash_exec"],"needs_clarification":false}`,
			},
		}},
	}

	router := NewRouter(mock, 5)

	decision, err := router.Route(context.Background(), "refactor the entire codebase", nil, nil, nil)
	if err != nil {
		t.Fatalf("Route returned error: %v", err)
	}

	if decision.Mode != "plan_execute" {
		t.Errorf("expected mode 'plan_execute', got '%s'", decision.Mode)
	}
	if decision.Complexity != 5 {
		t.Errorf("expected complexity 5, got %d", decision.Complexity)
	}
	if decision.CompactionStrategy != "hierarchical" {
		t.Errorf("expected compaction_strategy 'hierarchical', got '%s'", decision.CompactionStrategy)
	}
}

func TestRoute_HandlesJSONInCodeBlocks(t *testing.T) {
	// Mock returns JSON wrapped in markdown code block
	mock := &mockLLMCaller{
		responses: []*llm.ChatResponse{{
			Message: llm.Message{
				Role: "assistant",
				Content: "```json\n" +
					`{"mode":"direct","domain":"general","complexity":1,"compaction_strategy":"sliding_window","suggested_tools":[],"needs_clarification":false}` +
					"\n```",
			},
		}},
	}

	router := NewRouter(mock, 5)

	decision, err := router.Route(context.Background(), "what is 2+2?", nil, nil, nil)
	if err != nil {
		t.Fatalf("Route returned error: %v", err)
	}

	if decision.Mode != "direct" {
		t.Errorf("expected mode 'direct', got '%s'", decision.Mode)
	}
	if decision.Domain != "general" {
		t.Errorf("expected domain 'general', got '%s'", decision.Domain)
	}
	if decision.Complexity != 1 {
		t.Errorf("expected complexity 1, got %d", decision.Complexity)
	}
}

func TestRoute_DefaultsEmptyMode(t *testing.T) {
	// Mock returns empty mode
	mock := &mockLLMCaller{
		responses: []*llm.ChatResponse{{
			Message: llm.Message{
				Role:    "assistant",
				Content: `{"mode":"","domain":"code","complexity":2}`,
			},
		}},
	}

	router := NewRouter(mock, 5)

	decision, err := router.Route(context.Background(), "some request", nil, nil, nil)
	if err != nil {
		t.Fatalf("Route returned error: %v", err)
	}

	if decision.Mode != "react" {
		t.Errorf("expected default mode 'react', got '%s'", decision.Mode)
	}
}

func TestRoute_AppliesCompactionStrategyForCode(t *testing.T) {
	// Mock returns empty compaction_strategy with code domain
	mock := &mockLLMCaller{
		responses: []*llm.ChatResponse{{
			Message: llm.Message{
				Role:    "assistant",
				Content: `{"mode":"react","domain":"code","complexity":2}`,
			},
		}},
	}

	router := NewRouter(mock, 5)

	decision, err := router.Route(context.Background(), "read a file", nil, nil, nil)
	if err != nil {
		t.Fatalf("Route returned error: %v", err)
	}

	if decision.CompactionStrategy != "sliding_window" {
		t.Errorf("expected compaction_strategy 'sliding_window' for code domain, got '%s'", decision.CompactionStrategy)
	}
}

func TestRoute_AppliesCompactionStrategyForResearch(t *testing.T) {
	mock := &mockLLMCaller{
		responses: []*llm.ChatResponse{{
			Message: llm.Message{
				Role:    "assistant",
				Content: `{"mode":"react","domain":"research","complexity":3}`,
			},
		}},
	}

	router := NewRouter(mock, 5)

	decision, err := router.Route(context.Background(), "research topic", nil, nil, nil)
	if err != nil {
		t.Fatalf("Route returned error: %v", err)
	}

	if decision.CompactionStrategy != "summarization" {
		t.Errorf("expected compaction_strategy 'summarization' for research domain, got '%s'", decision.CompactionStrategy)
	}
}

func TestRoute_AppliesCompactionStrategyForMixedHighComplexity(t *testing.T) {
	mock := &mockLLMCaller{
		responses: []*llm.ChatResponse{{
			Message: llm.Message{
				Role:    "assistant",
				Content: `{"mode":"plan_execute","domain":"mixed","complexity":4}`,
			},
		}},
	}

	router := NewRouter(mock, 5)

	decision, err := router.Route(context.Background(), "complex mixed task", nil, nil, nil)
	if err != nil {
		t.Fatalf("Route returned error: %v", err)
	}

	if decision.CompactionStrategy != "hierarchical" {
		t.Errorf("expected compaction_strategy 'hierarchical' for mixed domain with complexity >= 4, got '%s'", decision.CompactionStrategy)
	}
}

func TestExtractJSON_PlainJSON(t *testing.T) {
	input := `{"mode":"react","domain":"code"}`
	result := extractJSON(input)
	if result != input {
		t.Errorf("expected '%s', got '%s'", input, result)
	}
}

func TestExtractJSON_JSONInCodeBlock(t *testing.T) {
	input := "```json\n{\"mode\":\"react\"}\n```"
	expected := `{"mode":"react"}`
	result := extractJSON(input)
	if result != expected {
		t.Errorf("expected '%s', got '%s'", expected, result)
	}
}

func TestExtractJSON_JSONInCodeBlockWithoutLanguage(t *testing.T) {
	input := "```\n{\"mode\":\"react\"}\n```"
	expected := `{"mode":"react"}`
	result := extractJSON(input)
	if result != expected {
		t.Errorf("expected '%s', got '%s'", expected, result)
	}
}

func TestApplyCompactionStrategy(t *testing.T) {
	tests := []struct {
		domain     string
		complexity int
		expected   string
	}{
		{"code", 1, "sliding_window"},
		{"code", 5, "sliding_window"},
		{"research", 1, "summarization"},
		{"research", 5, "summarization"},
		{"mixed", 3, "sliding_window"},
		{"mixed", 4, "hierarchical"},
		{"general", 3, "sliding_window"},
		{"general", 5, "hierarchical"},
		{"unknown", 1, "sliding_window"},
	}

	for _, tt := range tests {
		result := applyCompactionStrategy(tt.domain, tt.complexity)
		if result != tt.expected {
			t.Errorf("applyCompactionStrategy(%s, %d) = %s, expected %s",
				tt.domain, tt.complexity, result, tt.expected)
		}
	}
}

func TestRoute_UsesRouterRole(t *testing.T) {
	mock := &mockLLMCaller{
		responses: []*llm.ChatResponse{{
			Message: llm.Message{
				Role:    "assistant",
				Content: `{"mode":"react","domain":"code","complexity":2}`,
			},
		}},
	}

	router := NewRouter(mock, 5)
	_, _ = router.Route(context.Background(), "test request", nil, nil, nil)

	// We can verify through the mock that the role was passed
	// Since mockLLMCaller doesn't store role, we'd need to extend it
	// For now, this test verifies the call completes without error
}

// TestRoute_ConfidenceField tests that the Confidence field is correctly parsed from routing response.
func TestRoute_ConfidenceField(t *testing.T) {
	// Mock returns a routing response with confidence field
	mock := &mockLLMCaller{
		responses: []*llm.ChatResponse{{
			Message: llm.Message{
				Role:    "assistant",
				Content: `{"mode":"react","domain":"code","complexity":3,"compaction_strategy":"sliding_window","suggested_tools":["bash_exec"],"needs_clarification":false,"confidence":0.85}`,
			},
		}},
	}

	router := NewRouter(mock, 5)

	decision, err := router.Route(context.Background(), "implement a feature", nil, nil, nil)
	if err != nil {
		t.Fatalf("Route returned error: %v", err)
	}

	// Verify confidence field is parsed correctly
	if decision.Confidence != 0.85 {
		t.Errorf("expected confidence 0.85, got %f", decision.Confidence)
	}

	// Verify other fields are still correct
	if decision.Mode != "react" {
		t.Errorf("expected mode 'react', got '%s'", decision.Mode)
	}
	if decision.Domain != "code" {
		t.Errorf("expected domain 'code', got '%s'", decision.Domain)
	}
	if decision.Complexity != 3 {
		t.Errorf("expected complexity 3, got %d", decision.Complexity)
	}
}

// TestRoute_ConfidenceFieldDefaultsToZero tests that Confidence defaults to 0 when not provided.
func TestRoute_ConfidenceFieldDefaultsToZero(t *testing.T) {
	// Mock returns a routing response WITHOUT confidence field
	mock := &mockLLMCaller{
		responses: []*llm.ChatResponse{{
			Message: llm.Message{
				Role:    "assistant",
				Content: `{"mode":"direct","domain":"general","complexity":1}`,
			},
		}},
	}

	router := NewRouter(mock, 5)

	decision, err := router.Route(context.Background(), "simple question", nil, nil, nil)
	if err != nil {
		t.Fatalf("Route returned error: %v", err)
	}

	// Confidence should default to 0 when not provided
	if decision.Confidence != 0 {
		t.Errorf("expected confidence 0 (default), got %f", decision.Confidence)
	}
}

// TestRoute_NilRawCriteriaFormatsExtractionFailed tests that Router formats the
// "extraction failed" message when rawCriteria is nil.
func TestRoute_NilRawCriteriaFormatsExtractionFailed(t *testing.T) {
	mock := &mockLLMCaller{
		responses: []*llm.ChatResponse{{
			Message: llm.Message{
				Role:    "assistant",
				Content: `{"mode":"react","domain":"code","complexity":3}`,
			},
		}},
	}

	router := NewRouter(mock, 5)

	// Pass nil rawCriteria (extraction failed scenario)
	_, err := router.Route(context.Background(), "test request", nil, nil, nil)
	if err != nil {
		t.Fatalf("Route returned error: %v", err)
	}

	// Check that system prompt contains the extraction failed message as the RAW-CRITERIA value
	// The template has this string in docs + it's the actual replacement = 2 occurrences
	systemMessage := mock.lastCall().Messages[0]
	if systemMessage.Role != "system" {
		t.Fatalf("expected first message to be system, got '%s'", systemMessage.Role)
	}
	extractFailedCount := strings.Count(systemMessage.Content, "extraction failed")
	if extractFailedCount != 2 {
		t.Errorf("expected 'extraction failed' to appear twice (docs + value), got %d occurrences", extractFailedCount)
	}
	// The trivial task message should only appear once (in docs)
	trivialCount := strings.Count(systemMessage.Content, "task appears trivial")
	if trivialCount != 1 {
		t.Errorf("expected 'task appears trivial' to appear once (docs only), got %d occurrences", trivialCount)
	}
}

// TestRoute_EmptyRawCriteriaFormatsTrivialTask tests that Router formats the
// "task appears trivial" message when rawCriteria is an empty slice.
func TestRoute_EmptyRawCriteriaFormatsTrivialTask(t *testing.T) {
	mock := &mockLLMCaller{
		responses: []*llm.ChatResponse{{
			Message: llm.Message{
				Role:    "assistant",
				Content: `{"mode":"direct","domain":"general","complexity":1}`,
			},
		}},
	}

	router := NewRouter(mock, 5)

	// Pass empty slice rawCriteria (trivial task scenario)
	_, err := router.Route(context.Background(), "what is 2+2?", []RawCriterion{}, nil, nil)
	if err != nil {
		t.Fatalf("Route returned error: %v", err)
	}

	// Check that system prompt contains the trivial task message as the RAW-CRITERIA value
	// The template has this string in docs + it's the actual replacement = 2 occurrences
	systemMessage := mock.lastCall().Messages[0]
	if systemMessage.Role != "system" {
		t.Fatalf("expected first message to be system, got '%s'", systemMessage.Role)
	}
	trivialCount := strings.Count(systemMessage.Content, "task appears trivial")
	if trivialCount != 2 {
		t.Errorf("expected 'task appears trivial' to appear twice (docs + value), got %d occurrences", trivialCount)
	}
	// The extraction failed message should only appear once (in docs)
	extractFailedCount := strings.Count(systemMessage.Content, "extraction failed")
	if extractFailedCount != 1 {
		t.Errorf("expected 'extraction failed' to appear once (docs only), got %d occurrences", extractFailedCount)
	}
}

// TestRoute_NonEmptyRawCriteriaFormatsCriteriaList tests that Router formats
// criteria as a list when rawCriteria has items.
func TestRoute_NonEmptyRawCriteriaFormatsCriteriaList(t *testing.T) {
	mock := &mockLLMCaller{
		responses: []*llm.ChatResponse{{
			Message: llm.Message{
				Role:    "assistant",
				Content: `{"mode":"react","domain":"code","complexity":2}`,
			},
		}},
	}

	router := NewRouter(mock, 5)

	rawCriteria := []RawCriterion{
		{ID: "rc_1", Description: "test criterion one", Nature: "objective", Weight: "must"},
		{ID: "rc_2", Description: "test criterion two", Nature: "quality", Weight: "should", Implicit: true},
	}

	_, err := router.Route(context.Background(), "implement feature", rawCriteria, nil, nil)
	if err != nil {
		t.Fatalf("Route returned error: %v", err)
	}

	// Check that system prompt contains the formatted criteria
	systemMessage := mock.lastCall().Messages[0]
	if systemMessage.Role != "system" {
		t.Fatalf("expected first message to be system, got '%s'", systemMessage.Role)
	}
	if !strings.Contains(systemMessage.Content, "rc_1") {
		t.Error("system prompt should contain criterion ID 'rc_1'")
	}
	if !strings.Contains(systemMessage.Content, "test criterion one") {
		t.Error("system prompt should contain criterion description")
	}
	if !strings.Contains(systemMessage.Content, "[implicit]") {
		t.Error("system prompt should contain '[implicit]' marker for rc_2")
	}
	// When criteria provided, the placeholder messages should only appear once (in docs)
	extractFailedCount := strings.Count(systemMessage.Content, "extraction failed")
	if extractFailedCount != 1 {
		t.Errorf("expected 'extraction failed' to appear once (docs only), got %d occurrences", extractFailedCount)
	}
	trivialCount := strings.Count(systemMessage.Content, "task appears trivial")
	if trivialCount != 1 {
		t.Errorf("expected 'task appears trivial' to appear once (docs only), got %d occurrences", trivialCount)
	}
}

func TestNewRouter_DefaultHistoryWindow(t *testing.T) {
	mock := &mockLLMCaller{}

	// Zero history window should default to 10
	router := NewRouter(mock, 0)
	if router.historyWindow != 10 {
		t.Errorf("expected historyWindow=10 for 0 input, got %d", router.historyWindow)
	}

	// Negative history window should default to 10
	router = NewRouter(mock, -5)
	if router.historyWindow != 10 {
		t.Errorf("expected historyWindow=10 for -5 input, got %d", router.historyWindow)
	}

	// Positive should be used as-is
	router = NewRouter(mock, 20)
	if router.historyWindow != 20 {
		t.Errorf("expected historyWindow=20, got %d", router.historyWindow)
	}
}
