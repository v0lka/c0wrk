package llm

import (
	"context"
	"os"
	"strings"
	"testing"
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
