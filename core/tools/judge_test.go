package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/user/agent/sdk/llm"
	tools "github.com/user/agent/sdk/tools"
)

// mockLLMProvider is a mock implementation of llm.Provider for testing.
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
	judge := NewToolJudge(mockProvider, "test-model", 0)

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
	judge := NewToolJudge(mockProvider, "test-model", 0)

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
	judge := NewToolJudge(mockProvider, "test-model", 0)

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
	judge := NewToolJudge(mockProvider, "test-model", 0)

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
	judge := NewToolJudge(mockProvider, "test-model", 0)

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
	judge := NewToolJudge(mockProvider, "test-model", 0)

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
	judge := NewToolJudge(mockProvider, "test-model", 0)

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
	judge := NewToolJudge(mockProvider, "test-model", 0)

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
	judge := NewToolJudge(mockProvider, "test-model", 0)

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
	return s != "" && (s == substr || s != "" && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// --- Workspace pre-check tests ---

func TestJudge_WorkspacePreCheck_AllowsInternalPaths(t *testing.T) {
	mockProvider := &mockLLMProvider{
		response: &llm.ChatResponse{
			Message: llm.Message{Content: "VERDICT: CONFIRM\nREASON: Should not reach here"},
		},
	}
	judge := NewToolJudge(mockProvider, "test-model", 0)

	ctx := WithWorkspacePath(context.Background(), "/tmp/test-workspace")
	input := json.RawMessage(`{"path":"/tmp/test-workspace/src/main.go"}`)

	verdict, reason, err := judge.Judge(ctx, "file_write", input, "write file")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict != VerdictAllow {
		t.Errorf("expected VerdictAllow, got %d", verdict)
	}
	if reason != "all paths are within the session workspace" {
		t.Errorf("unexpected reason: %q", reason)
	}
	if mockProvider.callCount != 0 {
		t.Errorf("expected 0 LLM calls (short-circuited), got %d", mockProvider.callCount)
	}
}

func TestJudge_WorkspacePreCheck_DeniesExternalPaths(t *testing.T) {
	mockProvider := &mockLLMProvider{
		response: &llm.ChatResponse{
			Message: llm.Message{Content: "VERDICT: CONFIRM\nREASON: External path"},
		},
	}
	judge := NewToolJudge(mockProvider, "test-model", 0)

	ctx := WithWorkspacePath(context.Background(), "/tmp/test-workspace")
	input := json.RawMessage(`{"path":"/etc/passwd"}`)

	verdict, _, err := judge.Judge(ctx, "file_read", input, "read file")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict != VerdictConfirm {
		t.Errorf("expected VerdictConfirm (fell through to LLM), got %d", verdict)
	}
	if mockProvider.callCount != 1 {
		t.Errorf("expected 1 LLM call, got %d", mockProvider.callCount)
	}
}

func TestJudge_WorkspacePreCheck_MixedPaths(t *testing.T) {
	mockProvider := &mockLLMProvider{
		response: &llm.ChatResponse{
			Message: llm.Message{Content: "VERDICT: CONFIRM\nREASON: Mixed paths"},
		},
	}
	judge := NewToolJudge(mockProvider, "test-model", 0)

	ctx := WithWorkspacePath(context.Background(), "/tmp/test-workspace")
	input := json.RawMessage(`{"src":"/tmp/test-workspace/file.go","dest":"/etc/somefile"}`)

	verdict, _, err := judge.Judge(ctx, "file_copy", input, "copy file")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict != VerdictConfirm {
		t.Errorf("expected VerdictConfirm (fell through to LLM), got %d", verdict)
	}
	if mockProvider.callCount != 1 {
		t.Errorf("expected 1 LLM call, got %d", mockProvider.callCount)
	}
}

func TestJudge_WorkspacePreCheck_NoWorkspace(t *testing.T) {
	mockProvider := &mockLLMProvider{
		response: &llm.ChatResponse{
			Message: llm.Message{Content: "VERDICT: ALLOW\nREASON: From LLM"},
		},
	}
	judge := NewToolJudge(mockProvider, "test-model", 0)

	ctx := context.Background() // no workspace path
	input := json.RawMessage(`{"path":"/tmp/test-workspace/file.go"}`)

	verdict, _, err := judge.Judge(ctx, "file_write", input, "write file")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict != VerdictAllow {
		t.Errorf("expected VerdictAllow from LLM, got %d", verdict)
	}
	if mockProvider.callCount != 1 {
		t.Errorf("expected 1 LLM call (no workspace shortcut), got %d", mockProvider.callCount)
	}
}

func TestJudge_WorkspacePreCheck_NoPaths(t *testing.T) {
	mockProvider := &mockLLMProvider{
		response: &llm.ChatResponse{
			Message: llm.Message{Content: "VERDICT: ALLOW\nREASON: From LLM"},
		},
	}
	judge := NewToolJudge(mockProvider, "test-model", 0)

	ctx := WithWorkspacePath(context.Background(), "/tmp/test-workspace")
	input := json.RawMessage(`{"query":"SELECT * FROM users"}`)

	verdict, _, err := judge.Judge(ctx, "sql", input, "run query")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict != VerdictAllow {
		t.Errorf("expected VerdictAllow from LLM, got %d", verdict)
	}
	if mockProvider.callCount != 1 {
		t.Errorf("expected 1 LLM call (no paths found), got %d", mockProvider.callCount)
	}
}

func TestJudge_WorkspacePreCheck_BashCommand(t *testing.T) {
	mockProvider := &mockLLMProvider{
		response: &llm.ChatResponse{
			Message: llm.Message{Content: "VERDICT: CONFIRM\nREASON: Should not reach here"},
		},
	}
	judge := NewToolJudge(mockProvider, "test-model", 0)

	ctx := WithWorkspacePath(context.Background(), "/tmp/test-workspace")
	input := json.RawMessage(`{"command":"cat /tmp/test-workspace/src/main.go | grep func"}`)

	verdict, reason, err := judge.Judge(ctx, "bash", input, "search functions")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict != VerdictAllow {
		t.Errorf("expected VerdictAllow, got %d", verdict)
	}
	if reason != "all paths are within the session workspace" {
		t.Errorf("unexpected reason: %q", reason)
	}
	if mockProvider.callCount != 0 {
		t.Errorf("expected 0 LLM calls (short-circuited), got %d", mockProvider.callCount)
	}
}

func TestJudge_WorkspacePreCheck_RelativePaths(t *testing.T) {
	mockProvider := &mockLLMProvider{
		response: &llm.ChatResponse{
			Message: llm.Message{Content: "VERDICT: ALLOW\nREASON: From LLM"},
		},
	}
	judge := NewToolJudge(mockProvider, "test-model", 0)

	ctx := WithWorkspacePath(context.Background(), "/tmp/test-workspace")
	input := json.RawMessage(`{"path":"src/main.go"}`)

	verdict, _, err := judge.Judge(ctx, "file_write", input, "write file")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Relative paths don't start with /, so no absolute paths found → falls through to LLM
	if verdict != VerdictAllow {
		t.Errorf("expected VerdictAllow from LLM, got %d", verdict)
	}
	if mockProvider.callCount != 1 {
		t.Errorf("expected 1 LLM call (relative path not matched), got %d", mockProvider.callCount)
	}
}

func TestAllPathsInDir(t *testing.T) {
	tests := []struct {
		name  string
		dir   string
		input string
		want  bool
	}{
		{
			name:  "single path inside dir",
			dir:   "/tmp/session-temp",
			input: `{"file":"/tmp/session-temp/cache/data.txt"}`,
			want:  true,
		},
		{
			name:  "dir path itself",
			dir:   "/tmp/session-temp",
			input: `{"path":"/tmp/session-temp"}`,
			want:  true,
		},
		{
			name:  "path outside dir",
			dir:   "/tmp/session-temp",
			input: `{"file":"/etc/passwd"}`,
			want:  false,
		},
		{
			name:  "mixed paths",
			dir:   "/tmp/session-temp",
			input: `{"src":"/tmp/session-temp/a.txt","dst":"/tmp/other/b.txt"}`,
			want:  false,
		},
		{
			name:  "no paths in input",
			dir:   "/tmp/session-temp",
			input: `{"query":"hello world"}`,
			want:  false,
		},
		{
			name:  "empty dir",
			dir:   "",
			input: `{"file":"/tmp/session-temp/main.go"}`,
			want:  false,
		},
		{
			name:  "nested JSON with paths",
			dir:   "/tmp/session-temp",
			input: `{"args":{"file":"/tmp/session-temp/data.json"}}`,
			want:  true,
		},
		{
			name:  "array of paths inside dir",
			dir:   "/tmp/session-temp",
			input: `{"files":["/tmp/session-temp/a.txt","/tmp/session-temp/b.txt"]}`,
			want:  true,
		},
		{
			name:  "path traversal attempt",
			dir:   "/tmp/session-temp",
			input: `{"file":"/tmp/session-temp/../../../etc/passwd"}`,
			want:  false,
		},
		{
			name:  "bash command with dir path",
			dir:   "/tmp/session-temp",
			input: `{"command":"rm -rf /tmp/session-temp/cache"}`,
			want:  true,
		},
		{
			name:  "bash command with external path",
			dir:   "/tmp/session-temp",
			input: `{"command":"cat /etc/hosts"}`,
			want:  false,
		},
		{
			name:  "invalid JSON",
			dir:   "/tmp/session-temp",
			input: `not json`,
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := allPathsInDir(json.RawMessage(tt.input), tt.dir)
			if got != tt.want {
				t.Errorf("allPathsInDir() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAllPathsInWorkspace(t *testing.T) {
	tests := []struct {
		name      string
		workspace string
		input     string
		want      bool
	}{
		{
			name:      "single path inside workspace",
			workspace: "/home/user/project",
			input:     `{"file":"/home/user/project/main.go"}`,
			want:      true,
		},
		{
			name:      "workspace path itself",
			workspace: "/home/user/project",
			input:     `{"path":"/home/user/project"}`,
			want:      true,
		},
		{
			name:      "path outside workspace",
			workspace: "/home/user/project",
			input:     `{"file":"/etc/passwd"}`,
			want:      false,
		},
		{
			name:      "mixed paths",
			workspace: "/home/user/project",
			input:     `{"src":"/home/user/project/a.go","dst":"/tmp/b.go"}`,
			want:      false,
		},
		{
			name:      "no paths in input",
			workspace: "/home/user/project",
			input:     `{"query":"hello world"}`,
			want:      false,
		},
		{
			name:      "empty workspace",
			workspace: "",
			input:     `{"file":"/home/user/project/main.go"}`,
			want:      false,
		},
		{
			name:      "nested JSON with paths",
			workspace: "/workspace",
			input:     `{"args":{"file":"/workspace/src/app.go"}}`,
			want:      true,
		},
		{
			name:      "array of paths inside workspace",
			workspace: "/workspace",
			input:     `{"files":["/workspace/a.go","/workspace/b.go"]}`,
			want:      true,
		},
		{
			name:      "path traversal attempt",
			workspace: "/home/user/project",
			input:     `{"file":"/home/user/project/../../../etc/passwd"}`,
			want:      false,
		},
		{
			name:      "bash command with workspace path",
			workspace: "/workspace",
			input:     `{"command":"rm -rf /workspace/tmp/cache"}`,
			want:      true,
		},
		{
			name:      "bash command with external path",
			workspace: "/workspace",
			input:     `{"command":"cat /etc/hosts"}`,
			want:      false,
		},
		{
			name:      "invalid JSON",
			workspace: "/workspace",
			input:     `not json`,
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			if tt.workspace != "" {
				ctx = WithWorkspacePath(ctx, tt.workspace)
			}
			got := allPathsInWorkspace(ctx, json.RawMessage(tt.input))
			if got != tt.want {
				t.Errorf("allPathsInWorkspace() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestJudgeEvaluate_WithEnvInfo verifies that the judge's user prompt includes
// the compact environment block when EnvInfo is present in context.
func TestJudge_TempDirPreCheck_AllowsInternalPaths(t *testing.T) {
	mockProvider := &mockLLMProvider{
		response: &llm.ChatResponse{
			Message: llm.Message{Content: "VERDICT: CONFIRM\nREASON: Should not reach here"},
		},
	}
	judge := NewToolJudge(mockProvider, "test-model", 0)

	ctx := tools.WithTempDir(context.Background(), "/tmp/session-temp")
	input := json.RawMessage(`{"path":"/tmp/session-temp/cache/data.json"}`)

	verdict, reason, err := judge.Judge(ctx, "file_write", input, "write file")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict != VerdictAllow {
		t.Errorf("expected VerdictAllow, got %d", verdict)
	}
	if reason != "all paths are within the session temp directory" {
		t.Errorf("unexpected reason: %q", reason)
	}
	if mockProvider.callCount != 0 {
		t.Errorf("expected 0 LLM calls (short-circuited), got %d", mockProvider.callCount)
	}
}

func TestJudge_TempDirPreCheck_DeniesExternalPaths(t *testing.T) {
	mockProvider := &mockLLMProvider{
		response: &llm.ChatResponse{
			Message: llm.Message{Content: "VERDICT: CONFIRM\nREASON: External path"},
		},
	}
	judge := NewToolJudge(mockProvider, "test-model", 0)

	ctx := tools.WithTempDir(context.Background(), "/tmp/session-temp")
	input := json.RawMessage(`{"path":"/etc/passwd"}`)

	verdict, _, err := judge.Judge(ctx, "file_read", input, "read file")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict != VerdictConfirm {
		t.Errorf("expected VerdictConfirm (fell through to LLM), got %d", verdict)
	}
	if mockProvider.callCount != 1 {
		t.Errorf("expected 1 LLM call, got %d", mockProvider.callCount)
	}
}

func TestJudge_TempDirPreCheck_MixedPaths(t *testing.T) {
	mockProvider := &mockLLMProvider{
		response: &llm.ChatResponse{
			Message: llm.Message{Content: "VERDICT: CONFIRM\nREASON: Mixed paths"},
		},
	}
	judge := NewToolJudge(mockProvider, "test-model", 0)

	ctx := tools.WithTempDir(context.Background(), "/tmp/session-temp")
	input := json.RawMessage(`{"src":"/tmp/session-temp/file.txt","dest":"/tmp/other/file.txt"}`)

	verdict, _, err := judge.Judge(ctx, "file_copy", input, "copy file")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict != VerdictConfirm {
		t.Errorf("expected VerdictConfirm (fell through to LLM), got %d", verdict)
	}
	if mockProvider.callCount != 1 {
		t.Errorf("expected 1 LLM call, got %d", mockProvider.callCount)
	}
}

func TestJudge_TempDirPreCheck_NoTempDir(t *testing.T) {
	mockProvider := &mockLLMProvider{
		response: &llm.ChatResponse{
			Message: llm.Message{Content: "VERDICT: ALLOW\nREASON: From LLM"},
		},
	}
	judge := NewToolJudge(mockProvider, "test-model", 0)

	ctx := context.Background() // no temp dir
	input := json.RawMessage(`{"path":"/tmp/session-temp/file.txt"}`)

	verdict, _, err := judge.Judge(ctx, "file_write", input, "write file")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict != VerdictAllow {
		t.Errorf("expected VerdictAllow from LLM, got %d", verdict)
	}
	if mockProvider.callCount != 1 {
		t.Errorf("expected 1 LLM call (no temp dir shortcut), got %d", mockProvider.callCount)
	}
}

func TestJudge_TempDirPreCheck_TakesPrecedenceOverWorkspace(t *testing.T) {
	mockProvider := &mockLLMProvider{
		response: &llm.ChatResponse{
			Message: llm.Message{Content: "VERDICT: CONFIRM\nREASON: Should not reach here"},
		},
	}
	judge := NewToolJudge(mockProvider, "test-model", 0)

	// Both temp dir and workspace are set, but path is only in temp dir
	ctx := tools.WithTempDir(context.Background(), "/tmp/session-temp")
	ctx = WithWorkspacePath(ctx, "/home/user/project")
	input := json.RawMessage(`{"path":"/tmp/session-temp/cache/data.json"}`)

	verdict, reason, err := judge.Judge(ctx, "file_write", input, "write file")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict != VerdictAllow {
		t.Errorf("expected VerdictAllow, got %d", verdict)
	}
	// Should use temp dir reason, not workspace reason
	if reason != "all paths are within the session temp directory" {
		t.Errorf("expected temp dir reason, got %q", reason)
	}
	if mockProvider.callCount != 0 {
		t.Errorf("expected 0 LLM calls (short-circuited), got %d", mockProvider.callCount)
	}
}

func TestJudgeEvaluate_WithEnvInfo(t *testing.T) {
	mockProvider := &mockLLMProvider{
		response: &llm.ChatResponse{
			Message: llm.Message{Content: "VERDICT: ALLOW\nREASON: Safe operation"},
		},
	}
	judge := NewToolJudge(mockProvider, "test-model", 0)

	info := &EnvInfo{
		OS:   "macOS 15.4 (Darwin 24.4.0)",
		Arch: "arm64",
	}
	ctx := WithEnvInfo(context.Background(), info)

	// Use an input that will NOT be short-circuited by workspace path check
	// (no workspace in context, so it falls through to LLM).
	input := json.RawMessage(`{"query":"SELECT * FROM users"}`)

	verdict, _, err := judge.Judge(ctx, "sql", input, "run query")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict != VerdictAllow {
		t.Errorf("expected VerdictAllow, got %d", verdict)
	}

	// Verify the user prompt contains the compact env block
	if mockProvider.lastRequest == nil {
		t.Fatal("last request was not captured")
	}
	found := false
	for _, msg := range mockProvider.lastRequest.Messages {
		if msg.Role == "user" && contains(msg.Content, "## Environment") && contains(msg.Content, "macOS 15.4") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected environment block with OS info in judge user prompt")
	}
}
