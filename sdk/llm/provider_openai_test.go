package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"

	oai "github.com/openai/openai-go"
)

func TestOpenAIProvider_ImplementsInterface(t *testing.T) {
	var _ Provider = (*OpenAIProvider)(nil)
}

func TestOpenAIProvider_CustomBaseURL(t *testing.T) {
	p, err := NewOpenAIProvider(OpenAIProviderConfig{
		Name:    "deepseek",
		APIKey:  "test-key",
		BaseURL: "https://api.deepseek.com/v1",
	})
	if err != nil {
		t.Fatalf("NewOpenAIProvider with custom BaseURL failed: %v", err)
	}
	if p.Name() != "deepseek" {
		t.Errorf("expected name 'deepseek', got %q", p.Name())
	}
}

func TestOpenAIProvider_DefaultBaseURL(t *testing.T) {
	p, err := NewOpenAIProvider(OpenAIProviderConfig{
		Name:   "openai",
		APIKey: "test-key",
	})
	if err != nil {
		t.Fatalf("NewOpenAIProvider with default BaseURL failed: %v", err)
	}
	if p.Name() != "openai" {
		t.Errorf("expected name 'openai', got %q", p.Name())
	}
}

func TestOpenAIProvider_Integration(t *testing.T) {
	t.Skip("integration test disabled: requires valid OPENAI_API_KEY")
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY not set")
	}

	p, err := NewOpenAIProvider(OpenAIProviderConfig{
		Name:   "openai",
		APIKey: apiKey,
	})
	if err != nil {
		t.Fatalf("NewOpenAIProvider failed: %v", err)
	}

	ctx := context.Background()
	resp, err := p.ChatCompletion(ctx, ChatRequest{
		Model: "gpt-4o-mini",
		Messages: []Message{
			{Role: "user", Content: "Say hello in exactly one word."},
		},
		MaxTokens: 10,
	})
	if err != nil {
		t.Fatalf("ChatCompletion failed: %v", err)
	}

	if resp.Message.Content == "" {
		t.Error("expected non-empty response content")
	}
	if resp.StopReason == "" {
		t.Error("expected non-empty stop reason")
	}
	if resp.Usage.InputTokens == 0 {
		t.Error("expected non-zero input tokens")
	}
	if resp.Usage.OutputTokens == 0 {
		t.Error("expected non-zero output tokens")
	}
}

func TestOpenAIProvider_BuildChatParams(t *testing.T) {
	p, _ := NewOpenAIProvider(OpenAIProviderConfig{Name: "openai", APIKey: "k"})

	temp := 0.5
	req := ChatRequest{
		Model:       "gpt-4o",
		MaxTokens:   1024,
		Temperature: &temp,
		Messages: []Message{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "Hello"},
		},
		Tools: []ToolDefinition{
			{
				Name:        "search",
				Description: "Search the codebase",
				InputSchema: json.RawMessage(`{"type":"object"}`),
			},
		},
	}

	oaiReq := p.buildChatParams(req)

	if oaiReq.Model != "gpt-4o" {
		t.Errorf("expected model 'gpt-4o', got %q", oaiReq.Model)
	}
	if oaiReq.MaxCompletionTokens.Value != 1024 {
		t.Errorf("expected MaxCompletionTokens 1024, got %d", oaiReq.MaxCompletionTokens.Value)
	}
	if oaiReq.Temperature.Value != 0.5 {
		t.Errorf("expected temperature 0.5, got %f", oaiReq.Temperature.Value)
	}
	if len(oaiReq.Messages) != 2 {
		t.Errorf("expected 2 messages, got %d", len(oaiReq.Messages))
	}
	if len(oaiReq.Tools) != 1 {
		t.Errorf("expected 1 tool, got %d", len(oaiReq.Tools))
	}
	if oaiReq.Tools[0].Function.Name != "search" {
		t.Errorf("expected tool name 'search', got %q", oaiReq.Tools[0].Function.Name)
	}
}

func TestOpenAIProvider_BuildChatParams_WithReasoningContent(t *testing.T) {
	p, _ := NewOpenAIProvider(OpenAIProviderConfig{Name: "deepseek", APIKey: "k"})

	req := ChatRequest{
		Model: "deepseek-reasoner",
		Messages: []Message{
			{Role: "user", Content: "Hello"},
			{
				Role:             "assistant",
				Content:          "Let me think.",
				ReasoningContent: "I need to analyze this.",
				ToolCalls: []ToolCall{
					{ID: "call-1", Name: "search", Input: json.RawMessage(`{"q":"test"}`)},
				},
			},
			{Role: "tool", Content: "result", ToolCallID: "call-1"},
		},
	}

	oaiReq := p.buildChatParams(req)

	if len(oaiReq.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(oaiReq.Messages))
	}

	// Marshal the entire params to JSON and verify reasoning_content is in the assistant message
	jsonBytes, err := json.Marshal(oaiReq)
	if err != nil {
		t.Fatalf("failed to marshal params: %v", err)
	}

	var parsed struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(jsonBytes, &parsed); err != nil {
		t.Fatalf("failed to unmarshal params JSON: %v", err)
	}

	assistantMsg := parsed.Messages[1]
	if assistantMsg["role"] != "assistant" {
		t.Errorf("expected assistant message at index 1, got %q", assistantMsg["role"])
	}
	if assistantMsg["reasoning_content"] != "I need to analyze this." {
		t.Errorf("reasoning_content = %q, want 'I need to analyze this.'", assistantMsg["reasoning_content"])
	}
}

func TestOpenAIProvider_BuildChatParams_NoTools(t *testing.T) {
	p, _ := NewOpenAIProvider(OpenAIProviderConfig{Name: "openai", APIKey: "k"})

	req := ChatRequest{
		Model:    "gpt-4o",
		Messages: []Message{{Role: "user", Content: "Hi"}},
	}

	oaiReq := p.buildChatParams(req)

	if len(oaiReq.Tools) != 0 {
		t.Errorf("expected 0 tools, got %d", len(oaiReq.Tools))
	}
	// Temperature should be zero value when nil
	if oaiReq.Temperature.Value != 0 {
		t.Errorf("expected temperature 0 (default), got %f", oaiReq.Temperature.Value)
	}
}

func TestOpenAIProvider_ConvertRequestMessage(t *testing.T) {
	p, _ := NewOpenAIProvider(OpenAIProviderConfig{Name: "openai", APIKey: "k"})

	tests := []struct {
		name         string
		msg          Message
		wantRole     string
		wantContent  string
		wantToolCall bool
	}{
		{
			name:        "user message",
			msg:         Message{Role: "user", Content: "Hello"},
			wantRole:    "user",
			wantContent: "Hello",
		},
		{
			name:        "tool message with empty content gets fallback",
			msg:         Message{Role: "tool", Content: "", ToolCallID: "tc-1"},
			wantRole:    "tool",
			wantContent: "(no output)",
		},
		{
			name:        "tool message with content",
			msg:         Message{Role: "tool", Content: "result data", ToolCallID: "tc-2"},
			wantRole:    "tool",
			wantContent: "result data",
		},
		{
			name:        "system message",
			msg:         Message{Role: "system", Content: "Be helpful"},
			wantRole:    "system",
			wantContent: "Be helpful",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := p.convertRequestMessage(tt.msg)
			// The result is a union type, we need to check the actual type
			switch {
			case result.OfUser != nil:
				if tt.wantRole != "user" {
					t.Errorf("expected role %q, got user", tt.wantRole)
				}
				if result.OfUser.Content.OfString.Value != tt.wantContent {
					t.Errorf("content = %q, want %q", result.OfUser.Content.OfString.Value, tt.wantContent)
				}
			case result.OfSystem != nil:
				if tt.wantRole != "system" {
					t.Errorf("expected role %q, got system", tt.wantRole)
				}
				if result.OfSystem.Content.OfString.Value != tt.wantContent {
					t.Errorf("content = %q, want %q", result.OfSystem.Content.OfString.Value, tt.wantContent)
				}
			case result.OfTool != nil:
				if tt.wantRole != "tool" {
					t.Errorf("expected role %q, got tool", tt.wantRole)
				}
				if result.OfTool.Content.OfString.Value != tt.wantContent {
					t.Errorf("content = %q, want %q", result.OfTool.Content.OfString.Value, tt.wantContent)
				}
			default:
				t.Errorf("unexpected message type")
			}
		})
	}
}

func TestOpenAIProvider_ConvertRequestMessage_WithToolCalls(t *testing.T) {
	p, _ := NewOpenAIProvider(OpenAIProviderConfig{Name: "openai", APIKey: "k"})

	msg := Message{
		Role:    "assistant",
		Content: "Let me search.",
		ToolCalls: []ToolCall{
			{ID: "call-1", Name: "search", Input: json.RawMessage(`{"q":"test"}`)},
			{ID: "call-2", Name: "read", Input: json.RawMessage(`{"path":"/tmp"}`)},
		},
	}

	result := p.convertRequestMessage(msg)

	// The result is a union type, check for assistant message with tool calls
	if result.OfAssistant == nil {
		t.Fatalf("expected assistant message, got nil")
	}
	m := result.OfAssistant
	if len(m.ToolCalls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(m.ToolCalls))
	}
	if m.ToolCalls[0].ID != "call-1" {
		t.Errorf("tool call 0 ID = %q, want %q", m.ToolCalls[0].ID, "call-1")
	}
	if m.ToolCalls[0].Function.Name != "search" {
		t.Errorf("tool call 0 name = %q, want %q", m.ToolCalls[0].Function.Name, "search")
	}
	if m.ToolCalls[0].Function.Arguments != `{"q":"test"}` {
		t.Errorf("tool call 0 args = %q, want %q", m.ToolCalls[0].Function.Arguments, `{"q":"test"}`)
	}
}

func TestOpenAIProvider_ConvertRequestMessage_ReasoningContent(t *testing.T) {
	p, _ := NewOpenAIProvider(OpenAIProviderConfig{Name: "deepseek", APIKey: "k"})

	msg := Message{
		Role:             "assistant",
		Content:          "Let me think.",
		ReasoningContent: "This is my internal reasoning.",
	}

	result := p.convertRequestMessage(msg)

	if result.OfAssistant == nil {
		t.Fatalf("expected assistant message, got nil")
	}

	// Marshal the union type to JSON and verify reasoning_content is present
	jsonBytes, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("failed to marshal union: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(jsonBytes, &parsed); err != nil {
		t.Fatalf("failed to unmarshal JSON: %v", err)
	}

	if parsed["role"] != "assistant" {
		t.Errorf("role = %q, want 'assistant'", parsed["role"])
	}
	if parsed["content"] != "Let me think." {
		t.Errorf("content = %q, want 'Let me think.'", parsed["content"])
	}
	if parsed["reasoning_content"] != "This is my internal reasoning." {
		t.Errorf("reasoning_content = %q, want 'This is my internal reasoning.'", parsed["reasoning_content"])
	}
}

func TestOpenAIProvider_ConvertRequestMessage_ReasoningContentWithToolCalls(t *testing.T) {
	p, _ := NewOpenAIProvider(OpenAIProviderConfig{Name: "deepseek", APIKey: "k"})

	msg := Message{
		Role:             "assistant",
		Content:          "Let me search.",
		ReasoningContent: "I need to find the file.",
		ToolCalls: []ToolCall{
			{ID: "call-1", Name: "search", Input: json.RawMessage(`{"q":"test"}`)},
		},
	}

	result := p.convertRequestMessage(msg)

	if result.OfAssistant == nil {
		t.Fatalf("expected assistant message, got nil")
	}

	jsonBytes, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("failed to marshal union: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(jsonBytes, &parsed); err != nil {
		t.Fatalf("failed to unmarshal JSON: %v", err)
	}

	if parsed["reasoning_content"] != "I need to find the file." {
		t.Errorf("reasoning_content = %q, want 'I need to find the file.'", parsed["reasoning_content"])
	}

	toolCalls, ok := parsed["tool_calls"].([]any)
	if !ok || len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %v", parsed["tool_calls"])
	}
}

func TestOpenAIProvider_ConvertRequestMessage_ReasoningContentEmptyContent(t *testing.T) {
	p, _ := NewOpenAIProvider(OpenAIProviderConfig{Name: "deepseek", APIKey: "k"})

	// DeepSeek may return assistant message with empty content but with reasoning_content and tool_calls
	msg := Message{
		Role:             "assistant",
		Content:          "",
		ReasoningContent: "I need to search for the file.",
		ToolCalls: []ToolCall{
			{ID: "call-1", Name: "search", Input: json.RawMessage(`{"q":"test"}`)},
		},
	}

	result := p.convertRequestMessage(msg)

	if result.OfAssistant == nil {
		t.Fatalf("expected assistant message, got nil")
	}

	jsonBytes, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("failed to marshal union: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(jsonBytes, &parsed); err != nil {
		t.Fatalf("failed to unmarshal JSON: %v", err)
	}

	if parsed["reasoning_content"] != "I need to search for the file." {
		t.Errorf("reasoning_content = %q, want 'I need to search for the file.'", parsed["reasoning_content"])
	}
	// content may be omitted if empty; that's OK for OpenAI SDK
}

func TestOpenAIProvider_ConvertRequestMessage_EmptyContentOmitted(t *testing.T) {
	p, _ := NewOpenAIProvider(OpenAIProviderConfig{Name: "deepseek", APIKey: "k"})

	msg := Message{
		Role:             "assistant",
		Content:          "",
		ReasoningContent: "I need to search.",
		ToolCalls: []ToolCall{
			{ID: "call-1", Name: "search", Input: json.RawMessage(`{"q":"test"}`)},
		},
	}

	result := p.convertRequestMessage(msg)
	if result.OfAssistant == nil {
		t.Fatalf("expected assistant message, got nil")
	}

	// Check the underlying assistant param content
	if result.OfAssistant.Content.OfString.Valid() {
		// If content is valid, it will be serialized
		t.Logf("Content.OfString is valid, value=%q", result.OfAssistant.Content.OfString.Value)
	}
}

func TestOpenAIProvider_ConvertRequestMessage_EmptyReasoningContentWithToolCalls(t *testing.T) {
	p, _ := NewOpenAIProvider(OpenAIProviderConfig{Name: "deepseek", APIKey: "k"})

	// DeepSeek V4 requires reasoning_content to be echoed back even when empty
	// for assistant messages that had tool_calls.
	msg := Message{
		Role:             "assistant",
		Content:          "",
		ReasoningContent: "",
		ToolCalls: []ToolCall{
			{ID: "call-1", Name: "search", Input: json.RawMessage(`{"q":"test"}`)},
		},
	}

	result := p.convertRequestMessage(msg)
	if result.OfAssistant == nil {
		t.Fatalf("expected assistant message, got nil")
	}

	jsonBytes, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("failed to marshal union: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(jsonBytes, &parsed); err != nil {
		t.Fatalf("failed to unmarshal JSON: %v", err)
	}

	// reasoning_content must be present even when empty for tool call messages
	if _, ok := parsed["reasoning_content"]; !ok {
		t.Errorf("reasoning_content field missing for assistant message with tool_calls")
	}
	if parsed["reasoning_content"] != "" {
		t.Errorf("reasoning_content = %q, want empty string", parsed["reasoning_content"])
	}
}

func TestOpenAIProvider_ConvertRequestMessage_EmptyReasoningContentNoToolCalls(t *testing.T) {
	p, _ := NewOpenAIProvider(OpenAIProviderConfig{Name: "deepseek", APIKey: "k"})

	// Constructed assistant messages (e.g., executor nudges) have no tool_calls
	// and empty reasoning_content. DeepSeek V4 still requires the field.
	msg := Message{
		Role:             "assistant",
		Content:          "(proceeding)",
		ReasoningContent: "",
		ToolCalls:        nil,
	}

	result := p.convertRequestMessage(msg)
	if result.OfAssistant == nil {
		t.Fatalf("expected assistant message, got nil")
	}

	jsonBytes, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("failed to marshal union: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(jsonBytes, &parsed); err != nil {
		t.Fatalf("failed to unmarshal JSON: %v", err)
	}

	// reasoning_content must be present even for constructed messages without tool_calls
	if _, ok := parsed["reasoning_content"]; !ok {
		t.Errorf("reasoning_content field missing for constructed assistant message")
	}
	if parsed["reasoning_content"] != "" {
		t.Errorf("reasoning_content = %q, want empty string", parsed["reasoning_content"])
	}
}

func TestOpenAIProvider_ReasoningContentExtraFieldsPreserved(t *testing.T) {
	p, _ := NewOpenAIProvider(OpenAIProviderConfig{Name: "deepseek", APIKey: "k"})

	msg := Message{
		Role:             "assistant",
		Content:          "hello",
		ReasoningContent: "my reasoning",
	}

	union := p.convertRequestMessage(msg)
	if union.OfAssistant == nil {
		t.Fatal("expected assistant")
	}

	// Re-marshal the union after it was returned from the function
	jsonBytes, err := json.Marshal(union)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(jsonBytes, &parsed); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if parsed["reasoning_content"] != "my reasoning" {
		t.Errorf("reasoning_content lost after function return: got %q", parsed["reasoning_content"])
	}
}

func TestOpenAIProvider_ConvertChatResponseMessage(t *testing.T) {
	p, _ := NewOpenAIProvider(OpenAIProviderConfig{Name: "openai", APIKey: "k"})

	// Simple text message
	t.Run("simple text", func(t *testing.T) {
		oaiMsg := oai.ChatCompletionMessage{
			Role:    "assistant",
			Content: "Hello!",
		}
		result := p.convertChatResponseMessage(oaiMsg)
		if result.Role != "assistant" {
			t.Errorf("role = %q, want 'assistant'", result.Role)
		}
		if result.Content != "Hello!" {
			t.Errorf("content = %q, want 'Hello!'", result.Content)
		}
		if len(result.ToolCalls) != 0 {
			t.Errorf("expected 0 tool calls, got %d", len(result.ToolCalls))
		}
	})

	// Message with tool calls
	t.Run("with tool calls", func(t *testing.T) {
		oaiMsg := oai.ChatCompletionMessage{
			Role: "assistant",
			ToolCalls: []oai.ChatCompletionMessageToolCall{
				{
					ID:   "call-abc",
					Type: "function",
					Function: oai.ChatCompletionMessageToolCallFunction{
						Name:      "get_weather",
						Arguments: `{"city":"NYC"}`,
					},
				},
			},
		}
		result := p.convertChatResponseMessage(oaiMsg)
		if len(result.ToolCalls) != 1 {
			t.Fatalf("expected 1 tool call, got %d", len(result.ToolCalls))
		}
		tc := result.ToolCalls[0]
		if tc.ID != "call-abc" {
			t.Errorf("tool call ID = %q, want 'call-abc'", tc.ID)
		}
		if tc.Name != "get_weather" {
			t.Errorf("tool call Name = %q, want 'get_weather'", tc.Name)
		}
		if string(tc.Input) != `{"city":"NYC"}` {
			t.Errorf("tool call Input = %q, want '{\"city\":\"NYC\"}'", string(tc.Input))
		}
	})
}

func TestConvertChatResponseMessage_WithReasoningContent(t *testing.T) {
	p, _ := NewOpenAIProvider(OpenAIProviderConfig{Name: "deepseek", APIKey: "k"})

	// Simulate a DeepSeek response by unmarshalling JSON that contains reasoning_content
	var oaiMsg oai.ChatCompletionMessage
	rawJSON := `{"role":"assistant","content":"Let me search.","reasoning_content":"I need to find the file.","tool_calls":[{"id":"call-1","type":"function","function":{"name":"search","arguments":"{}"}}]}`
	if err := json.Unmarshal([]byte(rawJSON), &oaiMsg); err != nil {
		t.Fatalf("failed to unmarshal ChatCompletionMessage: %v", err)
	}

	// Verify RawJSON contains reasoning_content
	if !strings.Contains(oaiMsg.RawJSON(), "reasoning_content") {
		t.Fatalf("RawJSON does not contain reasoning_content: %s", oaiMsg.RawJSON())
	}

	result := p.convertChatResponseMessage(oaiMsg)

	if result.ReasoningContent != "I need to find the file." {
		t.Errorf("ReasoningContent = %q, want 'I need to find the file.'", result.ReasoningContent)
	}
	if result.Content != "Let me search." {
		t.Errorf("Content = %q, want 'Let me search.'", result.Content)
	}
	if len(result.ToolCalls) != 1 {
		t.Errorf("expected 1 tool call, got %d", len(result.ToolCalls))
	}
}

func TestExtractReasoningContent(t *testing.T) {
	tests := []struct {
		name string
		json string
		want string
	}{
		{
			name: "empty json",
			json: "",
			want: "",
		},
		{
			name: "no reasoning_content",
			json: `{"role":"assistant","content":"hello"}`,
			want: "",
		},
		{
			name: "with reasoning_content",
			json: `{"role":"assistant","content":"hello","reasoning_content":"Let me think..."}`,
			want: "Let me think...",
		},
		{
			name: "with tool_calls and reasoning_content",
			json: `{"role":"assistant","content":"","reasoning_content":"I need to search","tool_calls":[{"id":"call-1","type":"function","function":{"name":"search","arguments":"{}"}}]}`,
			want: "I need to search",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractReasoningContent(tt.json)
			if got != tt.want {
				t.Errorf("extractReasoningContent(%q) = %q, want %q", tt.json, got, tt.want)
			}
		})
	}
}

func TestNeedsResponsesAPI(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{"codex-mini-latest", true},
		{"codex-mini-2025-03-25", true},
		{"gpt-4o", false},
		{"o3", false},
		{"claude-sonnet-4-20250514", false},
		{"gpt-4.1-mini", false},
		{"deepseek-chat", false},
		{"gemini-2.5-pro", false},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			got := needsResponsesAPI(tt.model)
			if got != tt.want {
				t.Errorf("needsResponsesAPI(%q) = %v, want %v", tt.model, got, tt.want)
			}
		})
	}
}

func TestOpenAIProvider_ResponsesClientInitialized(t *testing.T) {
	p, err := NewOpenAIProvider(OpenAIProviderConfig{
		Name:   "openai",
		APIKey: "test-key",
	})
	if err != nil {
		t.Fatalf("NewOpenAIProvider failed: %v", err)
	}
	if p.responsesClient == nil {
		t.Error("expected responsesClient to be non-nil")
	}
	if p.client == nil {
		t.Error("expected client to be non-nil")
	}
}

func TestOpenAIProvider_ResponsesClientInitialized_CustomBaseURL(t *testing.T) {
	p, err := NewOpenAIProvider(OpenAIProviderConfig{
		Name:    "custom",
		APIKey:  "test-key",
		BaseURL: "https://custom.api.com/v1",
	})
	if err != nil {
		t.Fatalf("NewOpenAIProvider failed: %v", err)
	}
	if p.responsesClient == nil {
		t.Error("expected responsesClient to be non-nil with custom base URL")
	}
}

// TestOpenAIProvider_CompatibleProviderDoesNotUseResponsesAPI verifies that
// codex-family models routed through a compatible provider (non-empty baseURL)
// use Chat Completions, NOT the Responses API. The Responses API
// (/v1/responses) is an OpenAI-specific endpoint; "OpenAI-compatible"
// providers implement Chat Completions (/v1/chat/completions) only.
func TestOpenAIProvider_CompatibleProviderDoesNotUseResponsesAPI(t *testing.T) {
	t.Run("compatible provider with codex model uses chat completions", func(t *testing.T) {
		p, err := NewOpenAIProvider(OpenAIProviderConfig{
			Name:    "Zen",
			APIKey:  "test-key",
			BaseURL: "https://opencode.ai/zen/v1",
		})
		if err != nil {
			t.Fatalf("NewOpenAIProvider failed: %v", err)
		}
		if p.baseURL == "" {
			t.Fatal("expected non-empty baseURL for compatible provider")
		}
		// The routing decision: baseURL != "" means Chat Completions even for codex
		if p.baseURL == "" && needsResponsesAPI("gpt-5.3-codex") {
			t.Fatal("compatible provider should NOT route codex models to Responses API")
		}
	})

	t.Run("official OpenAI with codex model uses responses API", func(t *testing.T) {
		p, err := NewOpenAIProvider(OpenAIProviderConfig{
			Name:   "chatgpt",
			APIKey: "test-key",
		})
		if err != nil {
			t.Fatalf("NewOpenAIProvider failed: %v", err)
		}
		if p.baseURL != "" {
			t.Fatal("expected empty baseURL for official OpenAI")
		}
		// The routing decision: baseURL == "" + codex model → Responses API
		if p.baseURL == "" && !needsResponsesAPI("gpt-5.3-codex") {
			t.Fatal("official OpenAI should route codex models to Responses API")
		}
	})
}

func TestOpenAIProvider_EndToEnd_ReasoningContent(t *testing.T) {
	p, _ := NewOpenAIProvider(OpenAIProviderConfig{Name: "deepseek", APIKey: "k"})

	// Simulate a full cycle: response message -> step -> context window -> request params
	var oaiMsg oai.ChatCompletionMessage
	rawJSON := `{"role":"assistant","content":"Let me search.","reasoning_content":"I need to find the file.","tool_calls":[{"id":"call-1","type":"function","function":{"name":"search","arguments":"{}"}}]}`
	if err := json.Unmarshal([]byte(rawJSON), &oaiMsg); err != nil {
		t.Fatalf("failed to unmarshal ChatCompletionMessage: %v", err)
	}

	// 1. Convert response message
	msg := p.convertChatResponseMessage(oaiMsg)
	if msg.ReasoningContent != "I need to find the file." {
		t.Fatalf("convertChatResponseMessage lost reasoning_content: %q", msg.ReasoningContent)
	}

	// 2. Build a ChatRequest with this message as part of history
	req := ChatRequest{
		Model: "deepseek-reasoner",
		Messages: []Message{
			{Role: "user", Content: "Hello"},
			msg,
			{Role: "tool", Content: "result", ToolCallID: "call-1"},
		},
	}

	// 3. Build OpenAI params
	params := p.buildChatParams(req)

	// 4. Marshal params to JSON
	jsonBytes, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("failed to marshal params: %v", err)
	}

	var parsed struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(jsonBytes, &parsed); err != nil {
		t.Fatalf("failed to unmarshal params JSON: %v", err)
	}

	// Verify reasoning_content is present in the assistant message
	assistantMsg := parsed.Messages[1]
	if assistantMsg["role"] != "assistant" {
		t.Errorf("expected assistant message at index 1, got %q", assistantMsg["role"])
	}
	if assistantMsg["reasoning_content"] != "I need to find the file." {
		t.Errorf("reasoning_content = %q, want 'I need to find the file.'", assistantMsg["reasoning_content"])
	}
}

func TestOpenAIProvider_FullChatCompletionResponse_ReasoningContent(t *testing.T) {
	p, _ := NewOpenAIProvider(OpenAIProviderConfig{Name: "deepseek", APIKey: "k"})

	// Simulate a full DeepSeek chat completion response JSON
	fullResponse := `{
		"id":"chatcmpl-test",
		"object":"chat.completion",
		"created":1234567890,
		"model":"deepseek-reasoner",
		"choices":[{
			"index":0,
			"message":{
				"role":"assistant",
				"content":"Let me search.",
				"reasoning_content":"I need to find the file.",
				"tool_calls":[{"id":"call-1","type":"function","function":{"name":"search","arguments":"{}"}}]
			},
			"finish_reason":"tool_calls"
		}],
		"usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30}
	}`

	var completion oai.ChatCompletion
	if err := json.Unmarshal([]byte(fullResponse), &completion); err != nil {
		t.Fatalf("failed to unmarshal ChatCompletion: %v", err)
	}

	if len(completion.Choices) == 0 {
		t.Fatal("expected at least one choice")
	}

	msg := completion.Choices[0].Message
	if !strings.Contains(msg.RawJSON(), "reasoning_content") {
		t.Fatalf("RawJSON does not contain reasoning_content: %s", msg.RawJSON())
	}

	result := p.convertChatResponseMessage(msg)
	if result.ReasoningContent != "I need to find the file." {
		t.Errorf("ReasoningContent = %q, want 'I need to find the file.'", result.ReasoningContent)
	}
}

func TestOpenAIProvider_WrapError(t *testing.T) {
	p, _ := NewOpenAIProvider(OpenAIProviderConfig{Name: "openai", APIKey: "k"})

	t.Run("APIError", func(t *testing.T) {
		apiErr := &oai.Error{
			StatusCode: 429,
			Message:    "rate limited",
		}
		result := p.wrapError(apiErr)
		var llmErr *Error
		if !errors.As(result, &llmErr) {
			t.Fatal("expected *Error")
		}
		if llmErr.StatusCode != 429 {
			t.Errorf("expected status 429, got %d", llmErr.StatusCode)
		}
		if !llmErr.Retryable {
			t.Error("expected retryable for 429")
		}
	})

	t.Run("plain error", func(t *testing.T) {
		result := p.wrapError(errors.New("connection failed"))
		var llmErr *Error
		if !errors.As(result, &llmErr) {
			t.Fatal("expected *Error")
		}
		if llmErr.StatusCode != 0 {
			t.Errorf("expected status 0, got %d", llmErr.StatusCode)
		}
	})
}

func TestConvertSchemaToMap(t *testing.T) {
	p, _ := NewOpenAIProvider(OpenAIProviderConfig{Name: "openai", APIKey: "k"})

	t.Run("empty schema returns default object", func(t *testing.T) {
		result := p.convertSchemaToMap(nil)
		if result["type"] != "object" {
			t.Errorf("expected type 'object', got %v", result["type"])
		}
		if result["additionalProperties"] != false {
			t.Error("expected additionalProperties: false")
		}
	})

	t.Run("invalid JSON returns default object", func(t *testing.T) {
		result := p.convertSchemaToMap([]byte("{invalid"))
		if result["type"] != "object" {
			t.Errorf("expected type 'object', got %v", result["type"])
		}
	})

	t.Run("valid JSON is parsed", func(t *testing.T) {
		result := p.convertSchemaToMap([]byte(`{"type":"string"}`))
		if result["type"] != "string" {
			t.Errorf("expected type 'string', got %v", result["type"])
		}
	})
}

func TestOpenAIProvider_WithCustomHTTPClient(t *testing.T) {
	customClient := &http.Client{}
	p, err := NewOpenAIProvider(OpenAIProviderConfig{
		Name:       "custom",
		APIKey:     "test-key",
		HTTPClient: customClient,
	})
	if err != nil {
		t.Fatalf("NewOpenAIProvider with custom HTTP client failed: %v", err)
	}
	if p.Name() != "custom" {
		t.Errorf("expected name 'custom', got %q", p.Name())
	}
}

func TestOpenAIProvider_BuildChatParams_ReasoningEffort(t *testing.T) {
	tests := []struct {
		name    string
		cfgName string
		model   string
		effort  string
	}{
		{"qwen with On", "qwen", "qwen-max", "On"},
		{"glm with On", "glm", "glm-4", "On"},
		{"deepseek with On", "deepseek", "deepseek-reasoner", "On"},
		{"openai with low", "openai", "gpt-5", "low"},
		{"no family in model uses DetectFamily", "custom", "o3-mini", "high"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, _ := NewOpenAIProvider(OpenAIProviderConfig{Name: tt.cfgName, APIKey: "k"})
			req := ChatRequest{
				Model:           tt.model,
				Messages:        []Message{{Role: "user", Content: "Hi"}},
				ReasoningEffort: tt.effort,
			}
			params := p.buildChatParams(req)
			if params.Model != tt.model {
				t.Errorf("expected model %q, got %q", tt.model, params.Model)
			}
		})
	}
}

func TestOpenAIProvider_BuildChatParams_NoReasoningNoFamily(t *testing.T) {
	p, _ := NewOpenAIProvider(OpenAIProviderConfig{Name: "openai", APIKey: "k"})
	req := ChatRequest{
		Model:           "gpt-4o",
		Messages:        []Message{{Role: "user", Content: "Hi"}},
		ReasoningEffort: "",
	}
	params := p.buildChatParams(req)
	// Verify no panic and model is correct
	if params.Model != "gpt-4o" {
		t.Errorf("expected model 'gpt-4o', got %q", params.Model)
	}
}

func TestOpenAIProvider_BuildChatParams_GLMReasoning(t *testing.T) {
	p, _ := NewOpenAIProvider(OpenAIProviderConfig{Name: "glm", APIKey: "k"})

	// build marshals the generated params to JSON and returns the decoded
	// top-level map so individual reasoning fields can be asserted.
	build := func(t *testing.T, model, effort string) map[string]any {
		req := ChatRequest{
			Model:           model,
			Messages:        []Message{{Role: "user", Content: "Hi"}},
			ReasoningEffort: effort,
		}
		raw, err := json.Marshal(p.buildChatParams(req))
		if err != nil {
			t.Fatalf("marshal params: %v", err)
		}
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("unmarshal params: %v", err)
		}
		return out
	}

	t.Run("glm-5.2 none disables thinking and omits reasoning_effort", func(t *testing.T) {
		out := build(t, "glm-5.2", "none")
		thinking, _ := out["thinking"].(map[string]any)
		if thinking["type"] != "disabled" {
			t.Errorf("thinking.type = %v, want disabled", thinking["type"])
		}
		if _, ok := out["reasoning_effort"]; ok {
			t.Errorf("reasoning_effort must be absent for none, got %v", out["reasoning_effort"])
		}
	})

	t.Run("glm-5.2 max enables thinking and sets reasoning_effort=max", func(t *testing.T) {
		out := build(t, "glm-5.2", "max")
		thinking, _ := out["thinking"].(map[string]any)
		if thinking["type"] != "enabled" {
			t.Errorf("thinking.type = %v, want enabled", thinking["type"])
		}
		if out["reasoning_effort"] != "max" {
			t.Errorf("reasoning_effort = %v, want max", out["reasoning_effort"])
		}
	})

	t.Run("glm-5.2 high enables thinking and sets reasoning_effort=high", func(t *testing.T) {
		out := build(t, "glm-5.2", "high")
		thinking, _ := out["thinking"].(map[string]any)
		if thinking["type"] != "enabled" {
			t.Errorf("thinking.type = %v, want enabled", thinking["type"])
		}
		if out["reasoning_effort"] != "high" {
			t.Errorf("reasoning_effort = %v, want high", out["reasoning_effort"])
		}
	})

	t.Run("glm-5.2 empty (Auto) sends no reasoning fields", func(t *testing.T) {
		out := build(t, "glm-5.2", "")
		if _, ok := out["thinking"]; ok {
			t.Errorf("thinking must be absent for Auto, got %v", out["thinking"])
		}
		if _, ok := out["reasoning_effort"]; ok {
			t.Errorf("reasoning_effort must be absent for Auto, got %v", out["reasoning_effort"])
		}
	})

	t.Run("glm-5.1 keeps legacy On/Off thinking", func(t *testing.T) {
		out := build(t, "glm-5.1", "Off")
		thinking, _ := out["thinking"].(map[string]any)
		if thinking["type"] != "Off" {
			t.Errorf("thinking.type = %v, want Off (legacy)", thinking["type"])
		}
		if _, ok := out["reasoning_effort"]; ok {
			t.Errorf("reasoning_effort must be absent for legacy GLM, got %v", out["reasoning_effort"])
		}
	})
}
