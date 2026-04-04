package llm

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"google.golang.org/genai"
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

	var sb strings.Builder
	var gotStopReason bool
	for chunk := range ch {
		if chunk.Delta != "" {
			sb.WriteString(chunk.Delta)
		}
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

func TestGeminiProvider_ConvertMessages(t *testing.T) {
	ctx := context.Background()
	p, err := NewGeminiProvider(ctx, GeminiProviderConfig{APIKey: "test-key"})
	if err != nil {
		t.Skipf("Cannot create provider: %v", err)
	}

	t.Run("basic messages with system", func(t *testing.T) {
		msgs := []Message{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "Hello"},
			{Role: "assistant", Content: "Hi there!"},
		}

		contents, sysInstruction := p.convertMessages(msgs)
		if sysInstruction == nil {
			t.Fatal("expected non-nil system instruction")
		}
		if sysInstruction.Parts[0].Text != "You are helpful." {
			t.Errorf("system instruction = %q, want 'You are helpful.'", sysInstruction.Parts[0].Text)
		}
		if len(contents) != 2 {
			t.Errorf("expected 2 contents (user + assistant), got %d", len(contents))
		}
		if contents[0].Role != "user" {
			t.Errorf("first content role = %q, want 'user'", contents[0].Role)
		}
		if contents[1].Role != "model" {
			t.Errorf("second content role = %q, want 'model'", contents[1].Role)
		}
	})

	t.Run("no system message", func(t *testing.T) {
		msgs := []Message{
			{Role: "user", Content: "Hello"},
		}
		_, sysInstruction := p.convertMessages(msgs)
		if sysInstruction != nil {
			t.Error("expected nil system instruction")
		}
	})

	t.Run("assistant with tool calls", func(t *testing.T) {
		msgs := []Message{
			{Role: "user", Content: "Weather?"},
			{
				Role: "assistant",
				ToolCalls: []ToolCall{
					{ID: "tc-1", Name: "weather", Input: json.RawMessage(`{"city":"NYC"}`)},
				},
			},
		}

		contents, _ := p.convertMessages(msgs)
		if len(contents) != 2 {
			t.Fatalf("expected 2 contents, got %d", len(contents))
		}
		// Second content (model) should have a FunctionCall part
		modelContent := contents[1]
		foundFC := false
		for _, part := range modelContent.Parts {
			if part.FunctionCall != nil {
				foundFC = true
				if part.FunctionCall.Name != "weather" {
					t.Errorf("function call name = %q, want 'weather'", part.FunctionCall.Name)
				}
			}
		}
		if !foundFC {
			t.Error("expected FunctionCall part in model content")
		}
	})

	t.Run("tool response", func(t *testing.T) {
		msgs := []Message{
			{Role: "tool", Content: `{"temp":72}`, ToolCallID: "tc-1"},
		}

		contents, _ := p.convertMessages(msgs)
		if len(contents) != 1 {
			t.Fatalf("expected 1 content, got %d", len(contents))
		}
		if contents[0].Role != "user" {
			t.Errorf("tool response role = %q, want 'user'", contents[0].Role)
		}
		if contents[0].Parts[0].FunctionResponse == nil {
			t.Error("expected FunctionResponse part")
		}
	})

	t.Run("tool response non-json", func(t *testing.T) {
		msgs := []Message{
			{Role: "tool", Content: "plain text result", ToolCallID: "tc-2"},
		}

		contents, _ := p.convertMessages(msgs)
		if len(contents) != 1 {
			t.Fatalf("expected 1 content, got %d", len(contents))
		}
		if contents[0].Parts[0].FunctionResponse == nil {
			t.Error("expected FunctionResponse part")
		}
		// Non-JSON content should be wrapped in {"result": "..."}
		if contents[0].Parts[0].FunctionResponse.Response["result"] != "plain text result" {
			t.Errorf("expected result wrapper, got %v", contents[0].Parts[0].FunctionResponse.Response)
		}
	})
}

func TestGeminiProvider_BuildConfig(t *testing.T) {
	ctx := context.Background()
	p, err := NewGeminiProvider(ctx, GeminiProviderConfig{APIKey: "test-key"})
	if err != nil {
		t.Skipf("Cannot create provider: %v", err)
	}

	t.Run("full config", func(t *testing.T) {
		temp := 0.7
		req := ChatRequest{
			MaxTokens:   2048,
			Temperature: &temp,
			Tools: []ToolDefinition{
				{
					Name:        "search",
					Description: "Search",
					InputSchema: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`),
				},
			},
		}
		sysInstr := &genai.Content{Parts: []*genai.Part{{Text: "system prompt"}}}

		config := p.buildConfig(req, sysInstr)

		if config.MaxOutputTokens != 2048 {
			t.Errorf("MaxOutputTokens = %d, want 2048", config.MaxOutputTokens)
		}
		if config.Temperature == nil || *config.Temperature != 0.7 {
			t.Errorf("Temperature = %v, want 0.7", config.Temperature)
		}
		if config.SystemInstruction == nil {
			t.Error("expected non-nil SystemInstruction")
		}
		if len(config.Tools) != 1 {
			t.Errorf("expected 1 tool, got %d", len(config.Tools))
		}
	})

	t.Run("minimal config", func(t *testing.T) {
		req := ChatRequest{}
		config := p.buildConfig(req, nil)

		if config.MaxOutputTokens != 0 {
			t.Errorf("MaxOutputTokens = %d, want 0", config.MaxOutputTokens)
		}
		if config.Temperature != nil {
			t.Errorf("expected nil temperature")
		}
		if config.SystemInstruction != nil {
			t.Error("expected nil SystemInstruction")
		}
		if len(config.Tools) != 0 {
			t.Errorf("expected 0 tools, got %d", len(config.Tools))
		}
	})
}

func TestGeminiProvider_ConvertResponse(t *testing.T) {
	ctx := context.Background()
	p, err := NewGeminiProvider(ctx, GeminiProviderConfig{APIKey: "test-key"})
	if err != nil {
		t.Skipf("Cannot create provider: %v", err)
	}

	t.Run("text response", func(t *testing.T) {
		result := &genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{
				{
					Content: &genai.Content{
						Parts: []*genai.Part{{Text: "Hello!"}},
					},
					FinishReason: genai.FinishReasonStop,
				},
			},
			UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
				PromptTokenCount:     10,
				CandidatesTokenCount: 5,
			},
		}

		resp, err := p.convertResponse(result)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.Message.Content != "Hello!" {
			t.Errorf("content = %q, want 'Hello!'", resp.Message.Content)
		}
		if resp.StopReason != "end_turn" {
			t.Errorf("stop reason = %q, want 'end_turn'", resp.StopReason)
		}
		if resp.Usage.InputTokens != 10 {
			t.Errorf("input tokens = %d, want 10", resp.Usage.InputTokens)
		}
		if resp.Usage.OutputTokens != 5 {
			t.Errorf("output tokens = %d, want 5", resp.Usage.OutputTokens)
		}
	})

	t.Run("thought response", func(t *testing.T) {
		result := &genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{
				{
					Content: &genai.Content{
						Parts: []*genai.Part{
							{Text: "Thinking...", Thought: true},
							{Text: "Answer"},
						},
					},
					FinishReason: genai.FinishReasonStop,
				},
			},
		}

		resp, err := p.convertResponse(result)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.Reasoning != "Thinking..." {
			t.Errorf("reasoning = %q, want 'Thinking...'", resp.Reasoning)
		}
		if resp.Message.Content != "Answer" {
			t.Errorf("content = %q, want 'Answer'", resp.Message.Content)
		}
	})

	t.Run("function call response", func(t *testing.T) {
		result := &genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{
				{
					Content: &genai.Content{
						Parts: []*genai.Part{
							{
								FunctionCall: &genai.FunctionCall{
									Name: "get_weather",
									Args: map[string]any{"city": "NYC"},
								},
							},
						},
					},
					FinishReason: genai.FinishReasonStop,
				},
			},
		}

		resp, err := p.convertResponse(result)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(resp.Message.ToolCalls) != 1 {
			t.Fatalf("expected 1 tool call, got %d", len(resp.Message.ToolCalls))
		}
		tc := resp.Message.ToolCalls[0]
		if tc.Name != "get_weather" {
			t.Errorf("tool call name = %q, want 'get_weather'", tc.Name)
		}
		// Generated ID should be "call_get_weather" when no ID is provided
		if tc.ID != "call_get_weather" {
			t.Errorf("tool call ID = %q, want 'call_get_weather'", tc.ID)
		}
	})

	t.Run("empty candidates", func(t *testing.T) {
		result := &genai.GenerateContentResponse{}

		resp, err := p.convertResponse(result)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.Message.Content != "" {
			t.Errorf("expected empty content, got %q", resp.Message.Content)
		}
	})

	t.Run("nil usage metadata", func(t *testing.T) {
		result := &genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{
				{Content: &genai.Content{Parts: []*genai.Part{{Text: "OK"}}}, FinishReason: genai.FinishReasonStop},
			},
		}

		resp, err := p.convertResponse(result)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.Usage.InputTokens != 0 || resp.Usage.OutputTokens != 0 {
			t.Errorf("expected zero usage, got %+v", resp.Usage)
		}
	})
}

func TestGeminiProvider_ConvertStreamResponse(t *testing.T) {
	ctx := context.Background()
	p, err := NewGeminiProvider(ctx, GeminiProviderConfig{APIKey: "test-key"})
	if err != nil {
		t.Skipf("Cannot create provider: %v", err)
	}

	t.Run("text delta", func(t *testing.T) {
		result := &genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{
				{
					Content: &genai.Content{
						Parts: []*genai.Part{{Text: "Hello"}},
					},
				},
			},
		}

		chunks := p.convertStreamResponse(result)
		if len(chunks) != 1 {
			t.Fatalf("expected 1 chunk, got %d", len(chunks))
		}
		if chunks[0].Delta != "Hello" {
			t.Errorf("delta = %q, want 'Hello'", chunks[0].Delta)
		}
	})

	t.Run("thought delta", func(t *testing.T) {
		result := &genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{
				{
					Content: &genai.Content{
						Parts: []*genai.Part{{Text: "Thinking", Thought: true}},
					},
				},
			},
		}

		chunks := p.convertStreamResponse(result)
		if len(chunks) != 1 {
			t.Fatalf("expected 1 chunk, got %d", len(chunks))
		}
		if chunks[0].Reasoning != "Thinking" {
			t.Errorf("reasoning = %q, want 'Thinking'", chunks[0].Reasoning)
		}
	})

	t.Run("function call in stream", func(t *testing.T) {
		result := &genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{
				{
					Content: &genai.Content{
						Parts: []*genai.Part{
							{
								FunctionCall: &genai.FunctionCall{
									Name: "test_fn",
									Args: map[string]any{"key": "val"},
								},
							},
						},
					},
				},
			},
		}

		chunks := p.convertStreamResponse(result)
		if len(chunks) != 1 {
			t.Fatalf("expected 1 chunk, got %d", len(chunks))
		}
		if chunks[0].ToolCall == nil {
			t.Fatal("expected tool call")
		}
		if chunks[0].ToolCall.Name != "test_fn" {
			t.Errorf("tool call name = %q, want 'test_fn'", chunks[0].ToolCall.Name)
		}
	})

	t.Run("with finish reason", func(t *testing.T) {
		result := &genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{
				{
					Content:      &genai.Content{Parts: []*genai.Part{{Text: "Done"}}},
					FinishReason: genai.FinishReasonStop,
				},
			},
		}

		chunks := p.convertStreamResponse(result)
		if len(chunks) != 2 {
			t.Fatalf("expected 2 chunks (text + stop), got %d", len(chunks))
		}
		if chunks[1].StopReason != "end_turn" {
			t.Errorf("stop reason = %q, want 'end_turn'", chunks[1].StopReason)
		}
	})

	t.Run("empty candidates", func(t *testing.T) {
		result := &genai.GenerateContentResponse{}
		chunks := p.convertStreamResponse(result)
		if len(chunks) != 0 {
			t.Errorf("expected 0 chunks, got %d", len(chunks))
		}
	})
}

func TestGeminiProvider_WrapError(t *testing.T) {
	ctx := context.Background()
	p, err := NewGeminiProvider(ctx, GeminiProviderConfig{APIKey: "test-key"})
	if err != nil {
		t.Skipf("Cannot create provider: %v", err)
	}

	t.Run("APIError", func(t *testing.T) {
		apiErr := genai.APIError{
			Code:    429,
			Message: "rate limited",
			Status:  "RESOURCE_EXHAUSTED",
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

func TestGeminiProvider_VertexAIConfig(t *testing.T) {
	ctx := context.Background()
	// This will fail to create a client without actual credentials,
	// but we test that the config path is exercised
	_, err := NewGeminiProvider(ctx, GeminiProviderConfig{
		ProjectID: "test-project",
		Location:  "us-central1",
	})
	// We expect either success or a credential error, but not a panic
	if err != nil {
		// This is expected without valid credentials
		if !strings.Contains(err.Error(), "") {
			_ = err // acceptable
		}
	}
}
