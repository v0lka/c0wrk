package llm

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/sashabaranov/go-openai"
)

func TestOpenAIProvider_ImplementsInterface(t *testing.T) {
	var _ LLMProvider = (*OpenAIProvider)(nil)
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

func TestOpenAIProvider_StreamIntegration(t *testing.T) {
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
	chunks, err := p.StreamChatCompletion(ctx, ChatRequest{
		Model: "gpt-4o-mini",
		Messages: []Message{
			{Role: "user", Content: "Say hello in exactly one word."},
		},
		MaxTokens: 10,
	})
	if err != nil {
		t.Fatalf("StreamChatCompletion failed: %v", err)
	}

	var sb strings.Builder
	var gotStopReason bool
	for chunk := range chunks {
		sb.WriteString(chunk.Delta)
		if chunk.StopReason != "" {
			gotStopReason = true
		}
	}
	content := sb.String()

	if content == "" {
		t.Error("expected non-empty streamed content")
	}
	if !gotStopReason {
		t.Error("expected stop reason in stream")
	}
}

func TestOpenAIProvider_BuildRequest(t *testing.T) {
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

	oaiReq := p.buildRequest(req)

	if oaiReq.Model != "gpt-4o" {
		t.Errorf("expected model 'gpt-4o', got %q", oaiReq.Model)
	}
	if oaiReq.MaxCompletionTokens != 1024 {
		t.Errorf("expected MaxCompletionTokens 1024, got %d", oaiReq.MaxCompletionTokens)
	}
	if oaiReq.Temperature != 0.5 {
		t.Errorf("expected temperature 0.5, got %f", oaiReq.Temperature)
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

func TestOpenAIProvider_BuildRequest_NoTools(t *testing.T) {
	p, _ := NewOpenAIProvider(OpenAIProviderConfig{Name: "openai", APIKey: "k"})

	req := ChatRequest{
		Model:    "gpt-4o",
		Messages: []Message{{Role: "user", Content: "Hi"}},
	}

	oaiReq := p.buildRequest(req)

	if len(oaiReq.Tools) != 0 {
		t.Errorf("expected 0 tools, got %d", len(oaiReq.Tools))
	}
	// Temperature should be zero value when nil
	if oaiReq.Temperature != 0 {
		t.Errorf("expected temperature 0 (default), got %f", oaiReq.Temperature)
	}
}

func TestOpenAIProvider_ConvertRequestMessage(t *testing.T) {
	p, _ := NewOpenAIProvider(OpenAIProviderConfig{Name: "openai", APIKey: "k"})

	tests := []struct {
		name        string
		msg         Message
		wantRole    string
		wantContent string
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
			if result.Role != tt.wantRole {
				t.Errorf("role = %q, want %q", result.Role, tt.wantRole)
			}
			if result.Content != tt.wantContent {
				t.Errorf("content = %q, want %q", result.Content, tt.wantContent)
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

	if len(result.ToolCalls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(result.ToolCalls))
	}
	if result.ToolCalls[0].ID != "call-1" {
		t.Errorf("tool call 0 ID = %q, want %q", result.ToolCalls[0].ID, "call-1")
	}
	if result.ToolCalls[0].Function.Name != "search" {
		t.Errorf("tool call 0 name = %q, want %q", result.ToolCalls[0].Function.Name, "search")
	}
	if result.ToolCalls[0].Function.Arguments != `{"q":"test"}` {
		t.Errorf("tool call 0 args = %q, want %q", result.ToolCalls[0].Function.Arguments, `{"q":"test"}`)
	}
}

func TestOpenAIProvider_ConvertResponseMessage(t *testing.T) {
	p, _ := NewOpenAIProvider(OpenAIProviderConfig{Name: "openai", APIKey: "k"})

	// Simple text message
	t.Run("simple text", func(t *testing.T) {
		oaiMsg := openai.ChatCompletionMessage{
			Role:    "assistant",
			Content: "Hello!",
		}
		result := p.convertResponseMessage(oaiMsg)
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
		oaiMsg := openai.ChatCompletionMessage{
			Role: "assistant",
			ToolCalls: []openai.ToolCall{
				{
					ID:   "call-abc",
					Type: openai.ToolTypeFunction,
					Function: openai.FunctionCall{
						Name:      "get_weather",
						Arguments: `{"city":"NYC"}`,
					},
				},
			},
		}
		result := p.convertResponseMessage(oaiMsg)
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
			t.Errorf("tool call Input = %q, want '{\"city\":\"NYC\"}'" , string(tc.Input))
		}
	})
}

func TestOpenAIProvider_WrapError(t *testing.T) {
	p, _ := NewOpenAIProvider(OpenAIProviderConfig{Name: "openai", APIKey: "k"})

	t.Run("APIError", func(t *testing.T) {
		apiErr := &openai.APIError{
			HTTPStatusCode: 429,
			Message:        "rate limited",
		}
		result := p.wrapError(apiErr)
		var llmErr *LLMError
		if !errors.As(result, &llmErr) {
			t.Fatal("expected *LLMError")
		}
		if llmErr.StatusCode != 429 {
			t.Errorf("expected status 429, got %d", llmErr.StatusCode)
		}
		if !llmErr.Retryable {
			t.Error("expected retryable for 429")
		}
	})

	t.Run("RequestError", func(t *testing.T) {
		reqErr := &openai.RequestError{
			HTTPStatusCode: 500,
			Err:            errors.New("server error"),
		}
		result := p.wrapError(reqErr)
		var llmErr *LLMError
		if !errors.As(result, &llmErr) {
			t.Fatal("expected *LLMError")
		}
		if llmErr.StatusCode != 500 {
			t.Errorf("expected status 500, got %d", llmErr.StatusCode)
		}
	})

	t.Run("plain error", func(t *testing.T) {
		result := p.wrapError(errors.New("connection failed"))
		var llmErr *LLMError
		if !errors.As(result, &llmErr) {
			t.Fatal("expected *LLMError")
		}
		if llmErr.StatusCode != 0 {
			t.Errorf("expected status 0, got %d", llmErr.StatusCode)
		}
	})
}
