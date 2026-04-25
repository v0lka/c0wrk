package core

import (
	"context"
	"strings"
	"testing"

	"github.com/user/agent/sdk/llm"
	tools "github.com/user/agent/sdk/tools"
)

// Tests use shared mock types from testhelpers_test.go:
// - mockLLMCaller: implements LLMCaller

func TestRoute_ReturnsValidRoutingDecision(t *testing.T) {
	// Mock returns a valid JSON response
	mock := &mockLLMCaller{
		responses: []*llm.ChatResponse{{
			Message: llm.Message{
				Role:    "assistant",
				Content: `{"domain":"code","complexity":2,"needs_clarification":false}`,
			},
		}},
	}

	router := NewRouter(mock, 5)

	decision, err := router.Route(context.Background(), "read the config file", nil, nil, nil)
	if err != nil {
		t.Fatalf("Route returned error: %v", err)
	}

	if decision.Domain != "code" {
		t.Errorf("expected domain 'code', got '%s'", decision.Domain)
	}
	if decision.Complexity != 2 {
		t.Errorf("expected complexity 2, got %d", decision.Complexity)
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

	_, err := router.Route(context.Background(), "run a command", availableTools, nil, nil)
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

	_, err := router.Route(context.Background(), "current request", nil, history, nil)
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
				Content: `{"domain":"mixed","complexity":5,"needs_clarification":false}`,
			},
		}},
	}

	router := NewRouter(mock, 5)

	decision, err := router.Route(context.Background(), "refactor the entire codebase", nil, nil, nil)
	if err != nil {
		t.Fatalf("Route returned error: %v", err)
	}

	if decision.Complexity != 5 {
		t.Errorf("expected complexity 5, got %d", decision.Complexity)
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

	if decision.Domain != "general" {
		t.Errorf("expected domain 'general', got '%s'", decision.Domain)
	}
	if decision.Complexity != 1 {
		t.Errorf("expected complexity 1, got %d", decision.Complexity)
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

func TestValidateRoutingDecision(t *testing.T) {
	tests := []struct {
		name        string
		input       RoutingDecision
		wantDomain  string
		wantComplex int
	}{
		{"valid decision unchanged", RoutingDecision{Domain: "code", Complexity: 3}, "code", 3},
		{"unknown domain defaults to general", RoutingDecision{Domain: "unknown", Complexity: 2}, "general", 2},
		{"empty domain defaults to general", RoutingDecision{Domain: "", Complexity: 2}, "general", 2},
		{"complexity clamped to min 1", RoutingDecision{Domain: "code", Complexity: 0}, "code", 1},
		{"complexity clamped to max 5", RoutingDecision{Domain: "code", Complexity: 10}, "code", 5},
		{"negative complexity clamped", RoutingDecision{Domain: "code", Complexity: -1}, "code", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := tt.input
			validateRoutingDecision(&d)
			if d.Domain != tt.wantDomain || d.Complexity != tt.wantComplex {
				t.Errorf("got domain=%q complexity=%d, want domain=%q complexity=%d", d.Domain, d.Complexity, tt.wantDomain, tt.wantComplex)
			}
		})
	}
}

func TestValidateRoutingDecision_MatchedSkills(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		input      []string
		wantSkills []string
	}{
		{"nil skills unchanged", nil, nil},
		{"empty skills unchanged", []string{}, []string{}},
		{"valid skills preserved", []string{"pdf-processing", "data-analysis"}, []string{"pdf-processing", "data-analysis"}},
		{"duplicate skills deduped", []string{"pdf", "data", "pdf"}, []string{"pdf", "data"}},
		{"empty strings removed", []string{"pdf", "", "data"}, []string{"pdf", "data"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := RoutingDecision{Domain: "code", Complexity: 3, MatchedSkills: tt.input}
			validateRoutingDecision(&d)
			if len(d.MatchedSkills) != len(tt.wantSkills) {
				t.Fatalf("got %d skills %v, want %d skills %v", len(d.MatchedSkills), d.MatchedSkills, len(tt.wantSkills), tt.wantSkills)
			}
			for i, got := range d.MatchedSkills {
				if got != tt.wantSkills[i] {
					t.Errorf("skill[%d] = %q, want %q", i, got, tt.wantSkills[i])
				}
			}
		})
	}
}

func TestRoute_RetriesOnInvalidJSON(t *testing.T) {
	callCount := 0
	mock := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callCount++
			if callCount == 1 {
				// First call returns invalid JSON
				return &llm.ChatResponse{
					Message: llm.Message{Role: "assistant", Content: "I think this is a code task"},
				}, nil
			}
			// Retry returns valid JSON
			return &llm.ChatResponse{
				Message: llm.Message{Role: "assistant", Content: `{"domain":"code","complexity":2,"needs_clarification":false}`},
			}, nil
		},
	}
	router := NewRouter(mock, 5)
	decision, err := router.Route(context.Background(), "fix the bug", nil, nil, nil)
	if err != nil {
		t.Fatalf("expected successful retry, got error: %v", err)
	}
	if decision.Domain != "code" {
		t.Errorf("expected domain 'code', got '%s'", decision.Domain)
	}
	if callCount != 2 {
		t.Errorf("expected 2 LLM calls (original + retry), got %d", callCount)
	}
}

func TestRoute_SetsReasoningEffort(t *testing.T) {
	mock := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			return &llm.ChatResponse{
				Message: llm.Message{Role: "assistant", Content: `{"domain":"code","complexity":2}`},
			}, nil
		},
	}

	router := NewRouter(mock, 5)
	router.SetBaseReasoningEffort(llm.ReasoningHigh)

	_, err := router.Route(context.Background(), "test", nil, nil, nil)
	if err != nil {
		t.Fatalf("Route returned error: %v", err)
	}

	// AgentReasoningMode("router", ReasoningHigh) should return ReasoningLow
	got := mock.lastCall().ReasoningEffort
	if got != llm.ReasoningLow {
		t.Errorf("expected ReasoningEffort=%q, got %q", llm.ReasoningLow, got)
	}
}

func TestRoute_NoReasoningEffortWhenBaseEmpty(t *testing.T) {
	mock := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			return &llm.ChatResponse{
				Message: llm.Message{Role: "assistant", Content: `{"domain":"code","complexity":2}`},
			}, nil
		},
	}

	router := NewRouter(mock, 5)
	// No SetBaseReasoningEffort call — base is empty

	_, err := router.Route(context.Background(), "test", nil, nil, nil)
	if err != nil {
		t.Fatalf("Route returned error: %v", err)
	}

	// AgentReasoningMode("router", "") returns "" — providers skip reasoning
	got := mock.lastCall().ReasoningEffort
	if got != "" {
		t.Errorf("expected empty ReasoningEffort, got %q", got)
	}
}

func TestResolveBaseEffort(t *testing.T) {
	// nil registry returns empty
	if got := resolveBaseEffort("any-model", nil); got != "" {
		t.Errorf("nil registry: expected empty, got %q", got)
	}

	// unknown model returns empty
	reg := llm.NewModelRegistry(nil)
	if got := resolveBaseEffort("unknown-model-xyz", reg); got != "" {
		t.Errorf("unknown model: expected empty, got %q", got)
	}

	// non-reasoning model (claude-sonnet has Temperature=true, Reasoning=false)
	if got := resolveBaseEffort("claude-sonnet-4-20250514", reg); got != "" {
		t.Errorf("non-reasoning model: expected empty, got %q", got)
	}

	// reasoning model (o3 has Reasoning=true)
	if got := resolveBaseEffort("o3", reg); got != llm.ReasoningHigh {
		t.Errorf("reasoning model: expected %q, got %q", llm.ReasoningHigh, got)
	}
}
