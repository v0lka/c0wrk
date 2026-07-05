package llm

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/liushuangls/go-anthropic/v2"
)

// TestAnthropicProvider_ImplementsInterface verifies that AnthropicProvider implements Provider.
func TestAnthropicProvider_ImplementsInterface(t *testing.T) {
	var _ Provider = (*AnthropicProvider)(nil)
}

// TestAnthropicProvider_NewAllowsEmptyAPIKey verifies that NewAnthropicProvider
// does not require an API key, mirroring NewOpenAIProvider so that local
// Anthropic-compatible servers (which may not need auth) are supported. The
// official endpoint will fail at call time with a 401 when the key is empty.
func TestAnthropicProvider_NewAllowsEmptyAPIKey(t *testing.T) {
	provider, err := NewAnthropicProvider(AnthropicProviderConfig{})
	if err != nil {
		t.Fatalf("unexpected error on empty API key: %v", err)
	}
	if provider == nil {
		t.Fatal("expected non-nil provider")
	}
}

// TestAnthropicProvider_Name verifies that Name() returns "anthropic" by default
// and the configured custom name when set.
func TestAnthropicProvider_Name(t *testing.T) {
	t.Run("default name", func(t *testing.T) {
		provider, err := NewAnthropicProvider(AnthropicProviderConfig{
			APIKey: "test-key",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if provider.Name() != "anthropic" {
			t.Errorf("expected name 'anthropic', got %q", provider.Name())
		}
	})

	t.Run("custom name", func(t *testing.T) {
		provider, err := NewAnthropicProvider(AnthropicProviderConfig{
			Name:   "my-anthropic-proxy",
			APIKey: "test-key",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if provider.Name() != "my-anthropic-proxy" {
			t.Errorf("expected name 'my-anthropic-proxy', got %q", provider.Name())
		}
	})
}

// TestAnthropicProvider_CustomBaseURL verifies that a custom BaseURL is accepted
// at construction (Anthropic-compatible endpoint) without error.
func TestAnthropicProvider_CustomBaseURL(t *testing.T) {
	provider, err := NewAnthropicProvider(AnthropicProviderConfig{
		Name:    "my-proxy",
		APIKey:  "test-key",
		BaseURL: "https://my-anthropic-proxy.example.com",
	})
	if err != nil {
		t.Fatalf("unexpected error with custom BaseURL: %v", err)
	}
	if provider.Name() != "my-proxy" {
		t.Errorf("expected name 'my-proxy', got %q", provider.Name())
	}
}

// TestAnthropicProvider_Integration is an integration test that requires ANTHROPIC_API_KEY.
func TestAnthropicProvider_Integration(t *testing.T) {
	t.Skip("integration test disabled: requires real Anthropic API connection")
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}

	provider, err := NewAnthropicProvider(AnthropicProviderConfig{
		APIKey: apiKey,
	})
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}

	ctx := context.Background()
	req := ChatRequest{
		Model:     "claude-3-haiku-20240307",
		MaxTokens: 100,
		Messages: []Message{
			{Role: "user", Content: "Say 'hello' and nothing else."},
		},
	}

	resp, err := provider.ChatCompletion(ctx, req)
	if err != nil {
		t.Fatalf("ChatCompletion failed: %v", err)
	}

	if resp.Message.Content == "" {
		t.Error("expected non-empty response content")
	}

	if resp.Message.Role != "assistant" {
		t.Errorf("expected role 'assistant', got %q", resp.Message.Role)
	}

	if resp.Usage.InputTokens == 0 || resp.Usage.OutputTokens == 0 {
		t.Error("expected non-zero token usage")
	}
}

func TestAnthropicProvider_BuildRequest(t *testing.T) {
	p, _ := NewAnthropicProvider(AnthropicProviderConfig{APIKey: "test-key"})

	temp := 0.5
	req := ChatRequest{
		Model:       "claude-3-sonnet",
		MaxTokens:   2048,
		Temperature: &temp,
		Messages: []Message{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "Hello"},
			{Role: "assistant", Content: "Hi there!"},
		},
		Tools: []ToolDefinition{
			{
				Name:        "search",
				Description: "Search codebase",
				InputSchema: json.RawMessage(`{"type":"object"}`),
			},
		},
	}

	anthropicReq, err := p.buildRequest(req)
	if err != nil {
		t.Fatalf("buildRequest failed: %v", err)
	}

	if anthropicReq.System != "You are helpful." {
		t.Errorf("expected system prompt 'You are helpful.', got %q", anthropicReq.System)
	}
	if anthropicReq.MaxTokens != 2048 {
		t.Errorf("expected MaxTokens 2048, got %d", anthropicReq.MaxTokens)
	}
	if anthropicReq.Temperature == nil || *anthropicReq.Temperature != 0.5 {
		t.Errorf("expected temperature 0.5, got %v", anthropicReq.Temperature)
	}
	// 2 non-system messages
	if len(anthropicReq.Messages) != 2 {
		t.Errorf("expected 2 messages (no system), got %d", len(anthropicReq.Messages))
	}
	if len(anthropicReq.Tools) != 1 {
		t.Errorf("expected 1 tool, got %d", len(anthropicReq.Tools))
	}
}

func TestAnthropicProvider_BuildRequest_NoSystemNoToolsNoTemp(t *testing.T) {
	p, _ := NewAnthropicProvider(AnthropicProviderConfig{APIKey: "test-key"})

	req := ChatRequest{
		Model:     "claude-3-sonnet",
		MaxTokens: 1024,
		Messages: []Message{
			{Role: "user", Content: "Hello"},
		},
	}

	anthropicReq, err := p.buildRequest(req)
	if err != nil {
		t.Fatalf("buildRequest failed: %v", err)
	}

	if anthropicReq.System != "" {
		t.Errorf("expected empty system prompt, got %q", anthropicReq.System)
	}
	if anthropicReq.Temperature != nil {
		t.Errorf("expected nil temperature, got %v", anthropicReq.Temperature)
	}
	if len(anthropicReq.Tools) != 0 {
		t.Errorf("expected 0 tools, got %d", len(anthropicReq.Tools))
	}
}

// TestAnthropicProvider_BuildRequest_DefaultMaxTokens verifies that the
// provider defaults MaxTokens to defaultAnthropicMaxTokens when the caller
// does not set it (zero). The Anthropic Messages API requires max_tokens > 0;
// omitting it causes a 400 "Missing key ['max_tokens']" error. Internal
// callers (classification router, planner, reflector, context-compaction)
// rely on this default.
func TestAnthropicProvider_BuildRequest_DefaultMaxTokens(t *testing.T) {
	p, _ := NewAnthropicProvider(AnthropicProviderConfig{APIKey: "test-key"})

	t.Run("zero MaxTokens defaults to safe value", func(t *testing.T) {
		req := ChatRequest{
			Model: "claude-3-sonnet",
			Messages: []Message{
				{Role: "user", Content: "Hello"},
			},
		}

		anthropicReq, err := p.buildRequest(req)
		if err != nil {
			t.Fatalf("buildRequest failed: %v", err)
		}

		if anthropicReq.MaxTokens != defaultAnthropicMaxTokens {
			t.Errorf("expected MaxTokens %d, got %d", defaultAnthropicMaxTokens, anthropicReq.MaxTokens)
		}
	})

	t.Run("negative MaxTokens defaults to safe value", func(t *testing.T) {
		req := ChatRequest{
			Model:     "claude-3-sonnet",
			MaxTokens: -1,
			Messages: []Message{
				{Role: "user", Content: "Hello"},
			},
		}

		anthropicReq, err := p.buildRequest(req)
		if err != nil {
			t.Fatalf("buildRequest failed: %v", err)
		}

		if anthropicReq.MaxTokens != defaultAnthropicMaxTokens {
			t.Errorf("expected MaxTokens %d, got %d", defaultAnthropicMaxTokens, anthropicReq.MaxTokens)
		}
	})

	t.Run("explicit MaxTokens is preserved", func(t *testing.T) {
		req := ChatRequest{
			Model:     "claude-3-sonnet",
			MaxTokens: 2048,
			Messages: []Message{
				{Role: "user", Content: "Hello"},
			},
		}

		anthropicReq, err := p.buildRequest(req)
		if err != nil {
			t.Fatalf("buildRequest failed: %v", err)
		}

		if anthropicReq.MaxTokens != 2048 {
			t.Errorf("expected MaxTokens 2048, got %d", anthropicReq.MaxTokens)
		}
	})
}

func TestAnthropicProvider_ConvertMessage(t *testing.T) {
	p, _ := NewAnthropicProvider(AnthropicProviderConfig{APIKey: "test-key"})

	tests := []struct {
		name     string
		msg      Message
		wantRole anthropic.ChatRole
		wantErr  bool
	}{
		{
			name:     "user message",
			msg:      Message{Role: "user", Content: "Hello"},
			wantRole: anthropic.RoleUser,
		},
		{
			name:     "assistant message",
			msg:      Message{Role: "assistant", Content: "Hi!"},
			wantRole: anthropic.RoleAssistant,
		},
		{
			name:     "tool message",
			msg:      Message{Role: "tool", Content: "result", ToolCallID: "tc-1"},
			wantRole: anthropic.RoleUser,
		},
		{
			name:    "unsupported role",
			msg:     Message{Role: "unknown", Content: "test"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := p.convertMessage(tt.msg)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Role != tt.wantRole {
				t.Errorf("role = %q, want %q", result.Role, tt.wantRole)
			}
		})
	}
}

func TestAnthropicProvider_ConvertMessage_AssistantWithToolCalls(t *testing.T) {
	p, _ := NewAnthropicProvider(AnthropicProviderConfig{APIKey: "test-key"})

	msg := Message{
		Role:    "assistant",
		Content: "I'll search.",
		ToolCalls: []ToolCall{
			{ID: "tc-1", Name: "search", Input: json.RawMessage(`{"q":"test"}`)},
		},
	}

	result, err := p.convertMessage(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should have text + tool_use content blocks
	if len(result.Content) != 2 {
		t.Errorf("expected 2 content blocks, got %d", len(result.Content))
	}
}

func TestAnthropicProvider_ConvertMessage_AssistantEmptyContent(t *testing.T) {
	p, _ := NewAnthropicProvider(AnthropicProviderConfig{APIKey: "test-key"})

	msg := Message{
		Role:    "assistant",
		Content: "",
		ToolCalls: []ToolCall{
			{ID: "tc-1", Name: "search", Input: json.RawMessage(`{}`)},
		},
	}

	result, err := p.convertMessage(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should only have tool_use (no empty text block)
	if len(result.Content) != 1 {
		t.Errorf("expected 1 content block (tool_use only), got %d", len(result.Content))
	}
}

func TestAnthropicProvider_ParseResponse(t *testing.T) {
	p, _ := NewAnthropicProvider(AnthropicProviderConfig{APIKey: "test-key"})

	t.Run("text response", func(t *testing.T) {
		resp := anthropic.MessagesResponse{
			Content: []anthropic.MessageContent{
				{Type: anthropic.MessagesContentTypeText, Text: stringPtr("Hello!")},
			},
			StopReason: "end_turn",
			Usage: anthropic.MessagesUsage{
				InputTokens:  10,
				OutputTokens: 5,
			},
		}

		result, err := p.parseResponse(resp)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Message.Content != "Hello!" {
			t.Errorf("content = %q, want 'Hello!'", result.Message.Content)
		}
		if result.Message.Role != "assistant" {
			t.Errorf("role = %q, want 'assistant'", result.Message.Role)
		}
		if result.StopReason != "end_turn" {
			t.Errorf("stop reason = %q, want 'end_turn'", result.StopReason)
		}
		if result.Usage.InputTokens != 10 {
			t.Errorf("input tokens = %d, want 10", result.Usage.InputTokens)
		}
	})

	t.Run("tool use response", func(t *testing.T) {
		resp := anthropic.MessagesResponse{
			Content: []anthropic.MessageContent{
				{
					Type: anthropic.MessagesContentTypeToolUse,
					MessageContentToolUse: &anthropic.MessageContentToolUse{
						ID:    "call-123",
						Name:  "get_weather",
						Input: json.RawMessage(`{"city":"NYC"}`),
					},
				},
			},
			StopReason: "tool_use",
		}

		result, err := p.parseResponse(resp)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result.Message.ToolCalls) != 1 {
			t.Fatalf("expected 1 tool call, got %d", len(result.Message.ToolCalls))
		}
		tc := result.Message.ToolCalls[0]
		if tc.ID != "call-123" {
			t.Errorf("tool call ID = %q, want 'call-123'", tc.ID)
		}
		if tc.Name != "get_weather" {
			t.Errorf("tool call Name = %q, want 'get_weather'", tc.Name)
		}
	})

	t.Run("mixed text blocks", func(t *testing.T) {
		resp := anthropic.MessagesResponse{
			Content: []anthropic.MessageContent{
				{Type: anthropic.MessagesContentTypeText, Text: stringPtr("Part 1")},
				{Type: anthropic.MessagesContentTypeText, Text: stringPtr("Part 2")},
			},
			StopReason: "end_turn",
		}

		result, err := p.parseResponse(resp)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := "Part 1\nPart 2"
		if result.Message.Content != expected {
			t.Errorf("content = %q, want %q", result.Message.Content, expected)
		}
	})

	t.Run("thinking response", func(t *testing.T) {
		resp := anthropic.MessagesResponse{
			Content: []anthropic.MessageContent{
				{
					Type: anthropic.MessagesContentTypeThinking,
					MessageContentThinking: &anthropic.MessageContentThinking{
						Thinking: "Let me think...",
					},
				},
				{Type: anthropic.MessagesContentTypeText, Text: stringPtr("Answer")},
			},
			StopReason: "end_turn",
		}

		result, err := p.parseResponse(resp)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Reasoning != "Let me think..." {
			t.Errorf("reasoning = %q, want 'Let me think...'", result.Reasoning)
		}
		if result.Message.Content != "Answer" {
			t.Errorf("content = %q, want 'Answer'", result.Message.Content)
		}
	})
}

func TestAnthropicProvider_WrapError(t *testing.T) {
	p, _ := NewAnthropicProvider(AnthropicProviderConfig{APIKey: "test-key"})

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

func TestAnthropicProvider_BuildRequest_MultiSystem(t *testing.T) {
	p, _ := NewAnthropicProvider(AnthropicProviderConfig{APIKey: "test-key"})

	t.Run("single system message uses scalar System field", func(t *testing.T) {
		req := ChatRequest{
			Model:     "claude-3-sonnet",
			MaxTokens: 1024,
			Messages: []Message{
				{Role: "system", Content: "You are helpful."},
				{Role: "user", Content: "Hello"},
			},
		}

		anthropicReq, err := p.buildRequest(req)
		if err != nil {
			t.Fatalf("buildRequest failed: %v", err)
		}

		if anthropicReq.System != "You are helpful." {
			t.Errorf("System = %q, want %q", anthropicReq.System, "You are helpful.")
		}
		if len(anthropicReq.MultiSystem) != 0 {
			t.Errorf("MultiSystem should be empty for single system message, got %d parts", len(anthropicReq.MultiSystem))
		}
	})

	t.Run("multiple system messages use MultiSystem with cache control", func(t *testing.T) {
		req := ChatRequest{
			Model:     "claude-3-sonnet",
			MaxTokens: 1024,
			Messages: []Message{
				{Role: "system", Content: "Stable instructions"},
				{Role: "system", Content: "Dynamic context"},
				{Role: "user", Content: "Hello"},
			},
		}

		anthropicReq, err := p.buildRequest(req)
		if err != nil {
			t.Fatalf("buildRequest failed: %v", err)
		}

		if anthropicReq.System != "" {
			t.Errorf("scalar System should be empty for multi-system, got %q", anthropicReq.System)
		}
		if len(anthropicReq.MultiSystem) != 2 {
			t.Fatalf("MultiSystem should have 2 parts, got %d", len(anthropicReq.MultiSystem))
		}

		// First part: stable, should have cache control
		if anthropicReq.MultiSystem[0].Text != "Stable instructions" {
			t.Errorf("MultiSystem[0].Text = %q, want %q", anthropicReq.MultiSystem[0].Text, "Stable instructions")
		}
		if anthropicReq.MultiSystem[0].CacheControl == nil {
			t.Error("MultiSystem[0].CacheControl should be set (stable part)")
		} else if anthropicReq.MultiSystem[0].CacheControl.Type != anthropic.CacheControlTypeEphemeral {
			t.Errorf("MultiSystem[0].CacheControl.Type = %q, want %q", anthropicReq.MultiSystem[0].CacheControl.Type, anthropic.CacheControlTypeEphemeral)
		}

		// Last part: dynamic, should NOT have cache control
		if anthropicReq.MultiSystem[1].Text != "Dynamic context" {
			t.Errorf("MultiSystem[1].Text = %q, want %q", anthropicReq.MultiSystem[1].Text, "Dynamic context")
		}
		if anthropicReq.MultiSystem[1].CacheControl != nil {
			t.Error("MultiSystem[1].CacheControl should be nil (last/dynamic part)")
		}
	})

	t.Run("three system messages caches all but last", func(t *testing.T) {
		req := ChatRequest{
			Model:     "claude-3-sonnet",
			MaxTokens: 1024,
			Messages: []Message{
				{Role: "system", Content: "Part A"},
				{Role: "system", Content: "Part B"},
				{Role: "system", Content: "Part C"},
				{Role: "user", Content: "Hello"},
			},
		}

		anthropicReq, err := p.buildRequest(req)
		if err != nil {
			t.Fatalf("buildRequest failed: %v", err)
		}

		if len(anthropicReq.MultiSystem) != 3 {
			t.Fatalf("MultiSystem should have 3 parts, got %d", len(anthropicReq.MultiSystem))
		}

		// Parts 0 and 1 should have cache control
		for i := 0; i < 2; i++ {
			if anthropicReq.MultiSystem[i].CacheControl == nil {
				t.Errorf("MultiSystem[%d].CacheControl should be set", i)
			}
		}
		// Part 2 (last) should NOT
		if anthropicReq.MultiSystem[2].CacheControl != nil {
			t.Error("MultiSystem[2].CacheControl should be nil (last part)")
		}
	})

	t.Run("no system messages produces empty system", func(t *testing.T) {
		req := ChatRequest{
			Model:     "claude-3-sonnet",
			MaxTokens: 1024,
			Messages: []Message{
				{Role: "user", Content: "Hello"},
			},
		}

		anthropicReq, err := p.buildRequest(req)
		if err != nil {
			t.Fatalf("buildRequest failed: %v", err)
		}

		if anthropicReq.System != "" {
			t.Errorf("System = %q, want empty", anthropicReq.System)
		}
		if len(anthropicReq.MultiSystem) != 0 {
			t.Errorf("MultiSystem should be empty, got %d", len(anthropicReq.MultiSystem))
		}
	})
}

// stringPtr is a helper to create *string from string for anthropic MessageContent.
func stringPtr(s string) *string {
	return &s
}

func TestAnthropicProvider_WrapError_Types(t *testing.T) {
	p, _ := NewAnthropicProvider(AnthropicProviderConfig{APIKey: "test-key"})

	t.Run("APIError rate limit is retryable", func(t *testing.T) {
		apiErr := &anthropic.APIError{
			Type:    anthropic.ErrTypeRateLimit,
			Message: "rate limited",
		}
		result := p.wrapError(apiErr)
		var llmErr *Error
		if !errors.As(result, &llmErr) {
			t.Fatal("expected *Error")
		}
		if !llmErr.Retryable {
			t.Error("expected retryable for rate limit")
		}
	})

	t.Run("APIError overloaded is retryable", func(t *testing.T) {
		apiErr := &anthropic.APIError{
			Type:    anthropic.ErrTypeOverloaded,
			Message: "overloaded",
		}
		result := p.wrapError(apiErr)
		var llmErr *Error
		if !errors.As(result, &llmErr) {
			t.Fatal("expected *Error")
		}
		if !llmErr.Retryable {
			t.Error("expected retryable for overloaded")
		}
	})

	t.Run("APIError api error is retryable", func(t *testing.T) {
		apiErr := &anthropic.APIError{
			Type:    anthropic.ErrTypeApi,
			Message: "internal error",
		}
		result := p.wrapError(apiErr)
		var llmErr *Error
		if !errors.As(result, &llmErr) {
			t.Fatal("expected *Error")
		}
		if !llmErr.Retryable {
			t.Error("expected retryable for api error")
		}
	})

	t.Run("APIError invalid request is not retryable", func(t *testing.T) {
		apiErr := &anthropic.APIError{
			Type:    anthropic.ErrTypeInvalidRequest,
			Message: "invalid request",
		}
		result := p.wrapError(apiErr)
		var llmErr *Error
		if !errors.As(result, &llmErr) {
			t.Fatal("expected *Error")
		}
		if llmErr.Retryable {
			t.Error("expected not retryable for invalid request")
		}
	})

	t.Run("RequestError", func(t *testing.T) {
		reqErr := &anthropic.RequestError{
			StatusCode: 400,
			Err:        errors.New("bad request"),
		}
		result := p.wrapError(reqErr)
		var llmErr *Error
		if !errors.As(result, &llmErr) {
			t.Fatal("expected *Error")
		}
		if llmErr.StatusCode != 400 {
			t.Errorf("expected status 400, got %d", llmErr.StatusCode)
		}
	})
}
