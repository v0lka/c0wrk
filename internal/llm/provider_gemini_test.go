package llm

import (
	"context"
	"os"
	"testing"
)

func TestGeminiProvider_ImplementsInterface(t *testing.T) {
	var _ LLMProvider = (*GeminiProvider)(nil)
}

func TestGeminiProvider_Name(t *testing.T) {
	// Create provider with dummy config - client creation will fail but that's OK for this test
	// We just need to verify the name field is set correctly
	ctx := context.Background()
	provider, err := NewGeminiProvider(ctx, GeminiProviderConfig{
		APIKey: "test-key",
	})
	if err != nil {
		t.Skipf("Cannot create provider without valid credentials: %v", err)
	}

	if provider.Name() != "gemini" {
		t.Errorf("expected name 'gemini', got '%s'", provider.Name())
	}
}

func TestGeminiProvider_Integration(t *testing.T) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY not set")
	}

	ctx := context.Background()
	provider, err := NewGeminiProvider(ctx, GeminiProviderConfig{
		APIKey: apiKey,
	})
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}

	// Simple request test
	req := ChatRequest{
		Model: "gemini-2.0-flash",
		Messages: []Message{
			{Role: "user", Content: "Say hello in exactly one word."},
		},
		MaxTokens: 50,
	}

	resp, err := provider.ChatCompletion(ctx, req)
	if err != nil {
		t.Fatalf("ChatCompletion failed: %v", err)
	}

	if resp.Message.Role != "assistant" {
		t.Errorf("expected role 'assistant', got '%s'", resp.Message.Role)
	}

	if resp.Message.Content == "" {
		t.Error("expected non-empty content")
	}

	t.Logf("Response: %s", resp.Message.Content)
	t.Logf("Usage: input=%d, output=%d", resp.Usage.InputTokens, resp.Usage.OutputTokens)
}

func TestGeminiProvider_StreamIntegration(t *testing.T) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY not set")
	}

	ctx := context.Background()
	provider, err := NewGeminiProvider(ctx, GeminiProviderConfig{
		APIKey: apiKey,
	})
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}

	req := ChatRequest{
		Model: "gemini-2.0-flash",
		Messages: []Message{
			{Role: "user", Content: "Count from 1 to 3."},
		},
		MaxTokens: 50,
	}

	ch, err := provider.StreamChatCompletion(ctx, req)
	if err != nil {
		t.Fatalf("StreamChatCompletion failed: %v", err)
	}

	var content string
	var gotStopReason bool
	for chunk := range ch {
		if chunk.Delta != "" {
			content += chunk.Delta
		}
		if chunk.StopReason != "" {
			gotStopReason = true
		}
	}

	if content == "" {
		t.Error("expected non-empty streamed content")
	}

	if !gotStopReason {
		t.Error("expected stop reason in stream")
	}

	t.Logf("Streamed content: %s", content)
}

func TestGeminiProvider_ToolCalling(t *testing.T) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY not set")
	}

	ctx := context.Background()
	provider, err := NewGeminiProvider(ctx, GeminiProviderConfig{
		APIKey: apiKey,
	})
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}

	req := ChatRequest{
		Model: "gemini-2.0-flash",
		Messages: []Message{
			{Role: "user", Content: "What's the weather in San Francisco?"},
		},
		Tools: []ToolDefinition{
			{
				Name:        "get_weather",
				Description: "Get the current weather in a given location",
				InputSchema: []byte(`{"type": "object", "properties": {"location": {"type": "string", "description": "The city name"}}, "required": ["location"]}`),
			},
		},
		MaxTokens: 200,
	}

	resp, err := provider.ChatCompletion(ctx, req)
	if err != nil {
		t.Fatalf("ChatCompletion failed: %v", err)
	}

	// The model should call the get_weather tool
	if len(resp.Message.ToolCalls) == 0 {
		t.Log("No tool calls returned (model might have responded directly)")
	} else {
		tc := resp.Message.ToolCalls[0]
		t.Logf("Tool call: %s with args: %s", tc.Name, string(tc.Input))
		if tc.Name != "get_weather" {
			t.Errorf("expected tool call 'get_weather', got '%s'", tc.Name)
		}
	}
}
