package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/user/agent/internal/llm"
)

// mockLLMProvider is a mock implementation of llm.LLMProvider for testing.
type mockLLMProvider struct {
	response    *llm.ChatResponse
	err         error
	callCount   int
	lastRequest *llm.ChatRequest
}

func (m *mockLLMProvider) ChatCompletion(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	m.callCount++
	m.lastRequest = &req
	if m.err != nil {
		return nil, m.err
	}
	return m.response, nil
}

func (m *mockLLMProvider) StreamChatCompletion(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	return nil, errors.New("not implemented")
}

func (m *mockLLMProvider) Name() string {
	return "mock"
}

func TestJudgeCacheKey(t *testing.T) {
	// Same tool name and input should produce same key
	key1 := judgeCacheKey("bash", json.RawMessage(`{"command":"ls"}`))
	key2 := judgeCacheKey("bash", json.RawMessage(`{"command":"ls"}`))
	if key1 != key2 {
		t.Errorf("expected same keys, got %q and %q", key1, key2)
	}

	// Different tool name should produce different key
	key3 := judgeCacheKey("file_write", json.RawMessage(`{"command":"ls"}`))
	if key1 == key3 {
		t.Errorf("expected different keys for different tool names, got same key %q", key1)
	}

	// Different input should produce different key
	key4 := judgeCacheKey("bash", json.RawMessage(`{"command":"rm -rf /"}`))
	if key1 == key4 {
		t.Errorf("expected different keys for different inputs, got same key %q", key1)
	}
}

func TestJudge_CacheHit(t *testing.T) {
	mockProvider := &mockLLMProvider{
		response: &llm.ChatResponse{
			Message: llm.Message{Content: "VERDICT: ALLOW\nREASON: Safe operation"},
		},
	}
	judge := NewToolJudge(mockProvider, "test-model")

	ctx := context.Background()
	input := json.RawMessage(`{"command":"ls"}`)

	// First call - should hit LLM
	verdict1, reason1, err := judge.Judge(ctx, "bash", input, "list files")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict1 != VerdictAllow {
		t.Errorf("expected VerdictAllow, got %d", verdict1)
	}
	if reason1 != "Safe operation" {
		t.Errorf("expected reason 'Safe operation', got %q", reason1)
	}
	if mockProvider.callCount != 1 {
		t.Errorf("expected 1 LLM call, got %d", mockProvider.callCount)
	}

	// Second call - should use cache
	verdict2, reason2, err := judge.Judge(ctx, "bash", input, "list files")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict2 != VerdictAllow {
		t.Errorf("expected VerdictAllow, got %d", verdict2)
	}
	if reason2 != "Safe operation" {
		t.Errorf("expected reason 'Safe operation', got %q", reason2)
	}
	if mockProvider.callCount != 1 {
		t.Errorf("expected 1 LLM call (cached), got %d", mockProvider.callCount)
	}
}

func TestJudge_CacheMiss(t *testing.T) {
	mockProvider := &mockLLMProvider{
		response: &llm.ChatResponse{
			Message: llm.Message{Content: "VERDICT: CONFIRM\nREASON: Potentially dangerous"},
		},
	}
	judge := NewToolJudge(mockProvider, "test-model")

	ctx := context.Background()

	// First call with one input
	verdict1, _, err := judge.Judge(ctx, "bash", json.RawMessage(`{"command":"ls"}`), "list files")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict1 != VerdictConfirm {
		t.Errorf("expected VerdictConfirm, got %d", verdict1)
	}

	// Second call with different input - should hit LLM again
	verdict2, _, err := judge.Judge(ctx, "bash", json.RawMessage(`{"command":"rm file"}`), "delete file")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict2 != VerdictConfirm {
		t.Errorf("expected VerdictConfirm, got %d", verdict2)
	}

	if mockProvider.callCount != 2 {
		t.Errorf("expected 2 LLM calls, got %d", mockProvider.callCount)
	}
}

func TestJudge_AllowVerdict(t *testing.T) {
	mockProvider := &mockLLMProvider{
		response: &llm.ChatResponse{
			Message: llm.Message{Content: "VERDICT: ALLOW\nREASON: Safe file listing command"},
		},
	}
	judge := NewToolJudge(mockProvider, "test-model")

	ctx := context.Background()
	verdict, reason, err := judge.Judge(ctx, "bash", json.RawMessage(`{"command":"ls -la"}`), "list directory contents")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict != VerdictAllow {
		t.Errorf("expected VerdictAllow, got %d", verdict)
	}
	if reason != "Safe file listing command" {
		t.Errorf("expected reason 'Safe file listing command', got %q", reason)
	}
}

func TestJudge_ConfirmVerdict(t *testing.T) {
	mockProvider := &mockLLMProvider{
		response: &llm.ChatResponse{
			Message: llm.Message{Content: "VERDICT: CONFIRM\nREASON: Destructive command detected"},
		},
	}
	judge := NewToolJudge(mockProvider, "test-model")

	ctx := context.Background()
	verdict, reason, err := judge.Judge(ctx, "bash", json.RawMessage(`{"command":"rm -rf /"}`), "delete everything")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict != VerdictConfirm {
		t.Errorf("expected VerdictConfirm, got %d", verdict)
	}
	if reason != "Destructive command detected" {
		t.Errorf("expected reason 'Destructive command detected', got %q", reason)
	}
}

func TestJudge_LLMError_FallsBackToConfirm(t *testing.T) {
	mockProvider := &mockLLMProvider{
		err: errors.New("LLM connection error"),
	}
	judge := NewToolJudge(mockProvider, "test-model")

	ctx := context.Background()
	verdict, reason, err := judge.Judge(ctx, "bash", json.RawMessage(`{"command":"ls"}`), "list files")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// On error, should default to CONFIRM (fail-safe)
	if verdict != VerdictConfirm {
		t.Errorf("expected VerdictConfirm (fail-safe), got %d", verdict)
	}
	if reason != "Judge evaluation failed; requiring manual confirmation for safety" {
		t.Errorf("expected fail-safe reason, got %q", reason)
	}
}

func TestJudge_ResetCache(t *testing.T) {
	mockProvider := &mockLLMProvider{
		response: &llm.ChatResponse{
			Message: llm.Message{Content: "VERDICT: ALLOW\nREASON: Safe operation"},
		},
	}
	judge := NewToolJudge(mockProvider, "test-model")

	ctx := context.Background()
	input := json.RawMessage(`{"command":"ls"}`)

	// First call
	_, _, _ = judge.Judge(ctx, "bash", input, "list files")
	if mockProvider.callCount != 1 {
		t.Errorf("expected 1 LLM call, got %d", mockProvider.callCount)
	}

	// Reset cache
	judge.ResetCache()

	// Should hit LLM again after reset
	_, _, _ = judge.Judge(ctx, "bash", input, "list files")
	if mockProvider.callCount != 2 {
		t.Errorf("expected 2 LLM calls after reset, got %d", mockProvider.callCount)
	}
}

func TestJudge_TaskContextFromCtx(t *testing.T) {
	mockProvider := &mockLLMProvider{
		response: &llm.ChatResponse{
			Message: llm.Message{Content: "VERDICT: ALLOW\nREASON: Safe"},
		},
	}
	judge := NewToolJudge(mockProvider, "test-model")

	// Create context with task context
	ctx := WithTaskContext(context.Background(), "task from context")

	verdict, _, err := judge.Judge(ctx, "bash", json.RawMessage(`{"command":"ls"}`), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict != VerdictAllow {
		t.Errorf("expected VerdictAllow, got %d", verdict)
	}

	// Verify the task context from context was used in the request
	if mockProvider.lastRequest == nil {
		t.Fatal("last request was not captured")
	}
	found := false
	for _, msg := range mockProvider.lastRequest.Messages {
		if msg.Role == "user" && contains(msg.Content, "task from context") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected task context from context to be used in request")
	}
}

func TestJudge_TaskContextParameter_TakesPrecedence(t *testing.T) {
	mockProvider := &mockLLMProvider{
		response: &llm.ChatResponse{
			Message: llm.Message{Content: "VERDICT: ALLOW\nREASON: Safe"},
		},
	}
	judge := NewToolJudge(mockProvider, "test-model")

	// Create context with task context
	ctx := WithTaskContext(context.Background(), "task from context")

	verdict, _, err := judge.Judge(ctx, "bash", json.RawMessage(`{"command":"ls"}`), "explicit parameter")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict != VerdictAllow {
		t.Errorf("expected VerdictAllow, got %d", verdict)
	}

	// Verify the explicit parameter was used, not the context value
	if mockProvider.lastRequest == nil {
		t.Fatal("last request was not captured")
	}
	found := false
	for _, msg := range mockProvider.lastRequest.Messages {
		if msg.Role == "user" && contains(msg.Content, "explicit parameter") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected explicit parameter to be used in request")
	}
}

func TestWithTaskContext(t *testing.T) {
	ctx := context.Background()
	desc := "test task description"

	ctxWithTask := WithTaskContext(ctx, desc)
	retrieved := TaskContextFrom(ctxWithTask)

	if retrieved != desc {
		t.Errorf("expected %q, got %q", desc, retrieved)
	}
}

func TestTaskContextFrom_EmptyContext(t *testing.T) {
	ctx := context.Background()
	retrieved := TaskContextFrom(ctx)

	if retrieved != "" {
		t.Errorf("expected empty string, got %q", retrieved)
	}
}

func TestJudge_InputTruncation(t *testing.T) {
	mockProvider := &mockLLMProvider{
		response: &llm.ChatResponse{
			Message: llm.Message{Content: "VERDICT: ALLOW\nREASON: Safe"},
		},
	}
	judge := NewToolJudge(mockProvider, "test-model")

	// Create a very long input
	longInput := make([]byte, 3000)
	for i := range longInput {
		longInput[i] = 'a'
	}
	input := json.RawMessage(longInput)

	ctx := context.Background()
	_, _, err := judge.Judge(ctx, "bash", input, "test task")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the input was truncated in the request
	if mockProvider.lastRequest == nil {
		t.Fatal("last request was not captured")
	}
	for _, msg := range mockProvider.lastRequest.Messages {
		if msg.Role == "user" {
			if !contains(msg.Content, "(truncated)") {
				t.Error("expected truncated input to contain '(truncated)' marker")
			}
			break
		}
	}
}

func TestParseJudgeResponse_AllowWithReason(t *testing.T) {
	content := "VERDICT: ALLOW\nREASON: Safe file read operation"
	verdict, reason := parseJudgeResponse(content)
	if verdict != VerdictAllow {
		t.Errorf("expected VerdictAllow, got %d", verdict)
	}
	if reason != "Safe file read operation" {
		t.Errorf("expected reason 'Safe file read operation', got %q", reason)
	}
}

func TestParseJudgeResponse_ConfirmWithReason(t *testing.T) {
	content := "VERDICT: CONFIRM\nREASON: Potentially destructive command"
	verdict, reason := parseJudgeResponse(content)
	if verdict != VerdictConfirm {
		t.Errorf("expected VerdictConfirm, got %d", verdict)
	}
	if reason != "Potentially destructive command" {
		t.Errorf("expected reason 'Potentially destructive command', got %q", reason)
	}
}

func TestParseJudgeResponse_AllowCaseInsensitive(t *testing.T) {
	content := "VERDICT: allow\nREASON: lowercase verdict"
	verdict, reason := parseJudgeResponse(content)
	if verdict != VerdictAllow {
		t.Errorf("expected VerdictAllow for lowercase 'allow', got %d", verdict)
	}
	if reason != "lowercase verdict" {
		t.Errorf("expected reason 'lowercase verdict', got %q", reason)
	}
}

func TestParseJudgeResponse_MissingReason(t *testing.T) {
	content := "VERDICT: ALLOW"
	verdict, reason := parseJudgeResponse(content)
	if verdict != VerdictAllow {
		t.Errorf("expected VerdictAllow, got %d", verdict)
	}
	// Should have default reason for ALLOW when missing
	if reason != "Tool call appears safe and relevant to the task" {
		t.Errorf("expected default ALLOW reason, got %q", reason)
	}
}

func TestParseJudgeResponse_MissingVerdict(t *testing.T) {
	content := "REASON: Some explanation"
	verdict, reason := parseJudgeResponse(content)
	// Should default to CONFIRM when verdict missing
	if verdict != VerdictConfirm {
		t.Errorf("expected VerdictConfirm (default), got %d", verdict)
	}
	if reason != "Some explanation" {
		t.Errorf("expected reason 'Some explanation', got %q", reason)
	}
}

func TestParseJudgeResponse_EmptyContent(t *testing.T) {
	content := ""
	verdict, reason := parseJudgeResponse(content)
	if verdict != VerdictConfirm {
		t.Errorf("expected VerdictConfirm (default), got %d", verdict)
	}
	if reason != "Unable to parse judge response; requiring manual confirmation for safety" {
		t.Errorf("expected default fail-safe reason, got %q", reason)
	}
}

func TestParseJudgeResponse_ExtraWhitespace(t *testing.T) {
	content := "VERDICT:   ALLOW   \nREASON:   Extra spaces   "
	verdict, reason := parseJudgeResponse(content)
	if verdict != VerdictAllow {
		t.Errorf("expected VerdictAllow, got %d", verdict)
	}
	if reason != "Extra spaces" {
		t.Errorf("expected reason 'Extra spaces', got %q", reason)
	}
}

// Helper function to check if a string contains a substring.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
