package llm

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestAnthropicProvider_ImplementsInterface verifies that AnthropicProvider implements LLMProvider.
func TestAnthropicProvider_ImplementsInterface(t *testing.T) {
	var _ LLMProvider = (*AnthropicProvider)(nil)
}

// TestAnthropicProvider_NewRequiresAPIKey verifies that NewAnthropicProvider fails without an API key.
func TestAnthropicProvider_NewRequiresAPIKey(t *testing.T) {
	_, err := NewAnthropicProvider(AnthropicProviderConfig{})
	if err == nil {
		t.Error("expected error when API key is empty")
	}
}

// TestAnthropicProvider_Name verifies that Name() returns "anthropic".
func TestAnthropicProvider_Name(t *testing.T) {
	provider, err := NewAnthropicProvider(AnthropicProviderConfig{
		APIKey: "test-key",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if provider.Name() != "anthropic" {
		t.Errorf("expected name 'anthropic', got %q", provider.Name())
	}
}

// TestAnthropicProvider_Integration is an integration test that requires ANTHROPIC_API_KEY.
func TestAnthropicProvider_Integration(t *testing.T) {
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

// TestAnthropicProvider_IntegrationStream is an integration test for streaming.
func TestAnthropicProvider_IntegrationStream(t *testing.T) {
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

	chunks, err := provider.StreamChatCompletion(ctx, req)
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
