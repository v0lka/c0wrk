package router

import (
	"context"
	"strings"
	"testing"

	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/tools"
)

// mockLLMCaller is a minimal mock implementation of agent.LLMCaller for testing.
type mockLLMCaller struct {
	responses []*llm.ChatResponse
	callIdx   int
	calls     []llm.ChatRequest
	callFn    func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error)
	err       error
}

func (m *mockLLMCaller) Call(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	m.calls = append(m.calls, req)
	if m.callFn != nil {
		return m.callFn(ctx, req)
	}
	if m.err != nil {
		return nil, m.err
	}
	if len(m.responses) == 0 {
		return &llm.ChatResponse{Message: llm.Message{Role: "assistant", Content: "{}"}}, nil
	}
	resp := m.responses[m.callIdx%len(m.responses)]
	m.callIdx++
	return resp, nil
}

func (m *mockLLMCaller) lastCall() llm.ChatRequest {
	if len(m.calls) == 0 {
		return llm.ChatRequest{}
	}
	return m.calls[len(m.calls)-1]
}

func newTestRouter(mock *mockLLMCaller, historyWindow int) *Router {
	return New(mock, Config{
		SystemPrompt:  "Tools: {{AVAILABLE-TOOLS}}\nSkills: {{AVAILABLE-SKILLS}}",
		HistoryWindow: historyWindow,
	})
}

func TestRoute_ReturnsValidRoutingDecision(t *testing.T) {
	mock := &mockLLMCaller{
		responses: []*llm.ChatResponse{{
			Message: llm.Message{
				Role:    "assistant",
				Content: `{"domain":"code","complexity":2,"needs_clarification":false}`,
			},
		}},
	}

	r := newTestRouter(mock, 5)

	decision, err := r.Route(context.Background(), "read the config file", nil, nil, nil)
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

	r := newTestRouter(mock, 5)

	availableTools := []tools.ToolDescriptor{
		{Name: "bash_exec", Description: "Execute bash commands"},
		{Name: "file_read", Description: "Read file contents"},
	}

	_, err := r.Route(context.Background(), "run a command", availableTools, nil, nil)
	if err != nil {
		t.Fatalf("Route returned error: %v", err)
	}

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

	r := newTestRouter(mock, 3)

	history := []llm.Message{
		{Role: "user", Content: "previous message 1"},
		{Role: "assistant", Content: "previous response 1"},
		{Role: "user", Content: "previous message 2"},
		{Role: "assistant", Content: "previous response 2"},
	}

	_, err := r.Route(context.Background(), "current request", nil, history, nil)
	if err != nil {
		t.Fatalf("Route returned error: %v", err)
	}

	// With historyWindow=3, should include last 3 messages from history
	// Messages should be: system + last 3 history + user request
	if len(mock.lastCall().Messages) != 5 {
		t.Fatalf("expected 5 messages, got %d", len(mock.lastCall().Messages))
	}

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

	r := newTestRouter(mock, 5)

	decision, err := r.Route(context.Background(), "refactor the entire codebase", nil, nil, nil)
	if err != nil {
		t.Fatalf("Route returned error: %v", err)
	}

	if decision.Complexity != 5 {
		t.Errorf("expected complexity 5, got %d", decision.Complexity)
	}
}

func TestRoute_HandlesJSONInCodeBlocks(t *testing.T) {
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

	r := newTestRouter(mock, 5)

	decision, err := r.Route(context.Background(), "what is 2+2?", nil, nil, nil)
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
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			return &llm.ChatResponse{
				Message: llm.Message{Role: "assistant", Content: `{"domain":"code","complexity":2}`},
			}, nil
		},
	}

	r := newTestRouter(mock, 5)
	_, _ = r.Route(context.Background(), "test request", nil, nil, nil)
}

func TestNew_DefaultHistoryWindow(t *testing.T) {
	mock := &mockLLMCaller{}

	// Zero history window should default to 10
	r := New(mock, Config{HistoryWindow: 0})
	if r.historyWindow != 10 {
		t.Errorf("expected historyWindow=10 for 0 input, got %d", r.historyWindow)
	}

	// Negative history window should default to 10
	r = New(mock, Config{HistoryWindow: -5})
	if r.historyWindow != 10 {
		t.Errorf("expected historyWindow=10 for -5 input, got %d", r.historyWindow)
	}

	// Positive should be used as-is
	r = New(mock, Config{HistoryWindow: 20})
	if r.historyWindow != 20 {
		t.Errorf("expected historyWindow=20, got %d", r.historyWindow)
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
				return &llm.ChatResponse{
					Message: llm.Message{Role: "assistant", Content: "I think this is a code task"},
				}, nil
			}
			return &llm.ChatResponse{
				Message: llm.Message{Role: "assistant", Content: `{"domain":"code","complexity":2,"needs_clarification":false}`},
			}, nil
		},
	}
	r := newTestRouter(mock, 5)
	decision, err := r.Route(context.Background(), "fix the bug", nil, nil, nil)
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

	r := newTestRouter(mock, 5)
	r.SetReasoningEffort("high")

	_, err := r.Route(context.Background(), "test", nil, nil, nil)
	if err != nil {
		t.Fatalf("Route returned error: %v", err)
	}

	got := mock.lastCall().ReasoningEffort
	if got != "high" {
		t.Errorf("expected ReasoningEffort=%q, got %q", "high", got)
	}
}

func TestRoute_NoReasoningEffortWhenEmpty(t *testing.T) {
	mock := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			return &llm.ChatResponse{
				Message: llm.Message{Role: "assistant", Content: `{"domain":"code","complexity":2}`},
			}, nil
		},
	}

	r := newTestRouter(mock, 5)

	_, err := r.Route(context.Background(), "test", nil, nil, nil)
	if err != nil {
		t.Fatalf("Route returned error: %v", err)
	}

	got := mock.lastCall().ReasoningEffort
	if got != "" {
		t.Errorf("expected empty ReasoningEffort, got %q", got)
	}
}

func TestRoute_AppendContextSections(t *testing.T) {
	mock := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			return &llm.ChatResponse{
				Message: llm.Message{Role: "assistant", Content: `{"domain":"code","complexity":3,"matched_skills":["go-lint"]}`},
			}, nil
		},
	}

	appendCalled := false
	r := New(mock, Config{
		SystemPrompt:  "Tools: {{AVAILABLE-TOOLS}}\nSkills: {{AVAILABLE-SKILLS}}",
		HistoryWindow: 5,
		AppendContextSections: func(ctx context.Context) string {
			appendCalled = true
			return "\n\n## Project Context\nTech stack: Go 1.26, React 19."
		},
	})

	_, err := r.Route(context.Background(), "fix the bug", nil, nil, nil)
	if err != nil {
		t.Fatalf("Route returned error: %v", err)
	}

	if !appendCalled {
		t.Error("AppendContextSections was not called")
	}

	systemMsg := mock.lastCall().Messages[0]
	if !strings.Contains(systemMsg.Content, "## Project Context") {
		t.Error("system prompt should contain appended context section")
	}
	if !strings.Contains(systemMsg.Content, "Tech stack: Go 1.26, React 19.") {
		t.Error("system prompt should contain appended context content")
	}
}

func TestRoute_AppendContextSections_Nil(t *testing.T) {
	// Verify that nil AppendContextSections is a no-op.
	mock := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			return &llm.ChatResponse{
				Message: llm.Message{Role: "assistant", Content: `{"domain":"code","complexity":2}`},
			}, nil
		},
	}

	r := newTestRouter(mock, 5) // newTestRouter does NOT set AppendContextSections

	_, err := r.Route(context.Background(), "test", nil, nil, nil)
	if err != nil {
		t.Fatalf("Route returned error: %v", err)
	}

	systemMsg := mock.lastCall().Messages[0]
	if strings.Contains(systemMsg.Content, "AGENTS.md") {
		t.Error("system prompt should NOT contain AGENTS.md when AppendContextSections is nil")
	}
}

func TestSetModelRegistry(t *testing.T) {
	mock := &mockLLMCaller{}
	r := New(mock, Config{HistoryWindow: 5})
	if r.modelRegistry != nil {
		t.Error("modelRegistry should be nil initially")
	}
	reg := &llm.ModelRegistry{}
	r.SetModelRegistry(reg)
	if r.modelRegistry != reg {
		t.Error("modelRegistry should be set by SetModelRegistry")
	}
}

func TestRoute_NilResponse(t *testing.T) {
	mock := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			return nil, nil
		},
	}
	r := newTestRouter(mock, 5)
	_, err := r.Route(context.Background(), "test", nil, nil, nil)
	if err == nil {
		t.Fatal("expected error for nil response")
	}
	if !strings.Contains(err.Error(), "nil response") {
		t.Errorf("expected 'nil response' in error, got: %v", err)
	}
}
