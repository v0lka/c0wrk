package llm

import (
	"encoding/json"
	"testing"
)

func TestMessageWithToolCallsSerialization(t *testing.T) {
	msg := Message{
		Role:    "assistant",
		Content: "I'll help you with that.",
		ToolCalls: []ToolCall{
			{
				ID:    "call_123",
				Name:  "read_file",
				Input: json.RawMessage(`{"path":"/tmp/test.txt"}`),
			},
		},
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("failed to marshal Message: %v", err)
	}

	// Verify fields are present in JSON
	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("failed to unmarshal to map: %v", err)
	}

	if result["role"] != "assistant" {
		t.Errorf("expected role 'assistant', got %v", result["role"])
	}
	if result["content"] != "I'll help you with that." {
		t.Errorf("expected content 'I'll help you with that.', got %v", result["content"])
	}
	toolCalls, ok := result["tool_calls"].([]interface{})
	if !ok || len(toolCalls) != 1 {
		t.Errorf("expected tool_calls array with 1 element, got %v", result["tool_calls"])
	}
}

func TestChatRequestDeserialization(t *testing.T) {
	jsonStr := `{
		"model": "claude-3",
		"messages": [
			{"role": "user", "content": "Hello"}
		],
		"max_tokens": 1024,
		"temperature": 0.7
	}`

	var req ChatRequest
	if err := json.Unmarshal([]byte(jsonStr), &req); err != nil {
		t.Fatalf("failed to unmarshal ChatRequest: %v", err)
	}

	if req.Model != "claude-3" {
		t.Errorf("expected model 'claude-3', got %s", req.Model)
	}
	if len(req.Messages) != 1 {
		t.Errorf("expected 1 message, got %d", len(req.Messages))
	}
	if req.Messages[0].Role != "user" {
		t.Errorf("expected message role 'user', got %s", req.Messages[0].Role)
	}
	if req.Messages[0].Content != "Hello" {
		t.Errorf("expected message content 'Hello', got %s", req.Messages[0].Content)
	}
	if req.MaxTokens != 1024 {
		t.Errorf("expected max_tokens 1024, got %d", req.MaxTokens)
	}
	if req.Temperature == nil || *req.Temperature != 0.7 {
		t.Errorf("expected temperature 0.7, got %v", req.Temperature)
	}
}

func TestChatRequestTemperatureNilByDefault(t *testing.T) {
	var req ChatRequest

	if req.Temperature != nil {
		t.Errorf("expected Temperature to be nil by default, got %v", req.Temperature)
	}
}

func TestToolCallRoundTrip(t *testing.T) {
	original := ToolCall{
		ID:    "call_abc123",
		Name:  "search_codebase",
		Input: json.RawMessage(`{"query":"authentication","limit":10}`),
	}

	// Marshal
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("failed to marshal ToolCall: %v", err)
	}

	// Unmarshal
	var restored ToolCall
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("failed to unmarshal ToolCall: %v", err)
	}

	// Verify equality
	if restored.ID != original.ID {
		t.Errorf("ID mismatch: expected %s, got %s", original.ID, restored.ID)
	}
	if restored.Name != original.Name {
		t.Errorf("Name mismatch: expected %s, got %s", original.Name, restored.Name)
	}
	if string(restored.Input) != string(original.Input) {
		t.Errorf("Input mismatch: expected %s, got %s", string(original.Input), string(restored.Input))
	}
}
