package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// writeSSE is a helper function to write SSE events to the response writer.
func writeSSE(w http.ResponseWriter, event, data string) {
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// TestLMStudioProvider_ImplementsInterface verifies that LMStudioProvider implements LLMProvider.
func TestLMStudioProvider_ImplementsInterface(t *testing.T) {
	var _ LLMProvider = (*LMStudioProvider)(nil)
}

// TestLMStudioDefaultBaseURL verifies that an empty BaseURL defaults to localhost:1234.
func TestLMStudioDefaultBaseURL(t *testing.T) {
	p, err := NewLMStudioProvider(LMStudioProviderConfig{
		Name:    "lmstudio",
		BaseURL: "",
	})
	if err != nil {
		t.Fatalf("NewLMStudioProvider failed: %v", err)
	}
	if p.baseURL != "http://localhost:1234" {
		t.Errorf("expected baseURL 'http://localhost:1234', got %q", p.baseURL)
	}
}

// TestLMStudioChatCompletion tests non-streaming chat completion.
func TestLMStudioChatCompletion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request
		if r.Method != "POST" {
			t.Errorf("expected POST request, got %s", r.Method)
		}
		if r.URL.Path != "/api/v1/chat" {
			t.Errorf("expected path /api/v1/chat, got %s", r.URL.Path)
		}

		// Decode and verify request body
		var req lmStudioRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("failed to decode request body: %v", err)
		}

		if req.Model != "test-model" {
			t.Errorf("expected model 'test-model', got %q", req.Model)
		}
		if req.Stream != false {
			t.Errorf("expected stream=false, got %v", req.Stream)
		}
		if req.SystemPrompt != "You are helpful." {
			t.Errorf("expected system_prompt 'You are helpful.', got %q", req.SystemPrompt)
		}
		if len(req.Input) != 1 || req.Input[0].Role != "user" || req.Input[0].Content != "Hello" {
			t.Errorf("unexpected input messages: %+v", req.Input)
		}

		// Send response
		resp := lmStudioResponse{
			Output: []lmStudioOutputItem{
				{Type: "message", Content: "Hi there!"},
			},
			Stats: lmStudioStats{
				InputTokens:       10,
				TotalOutputTokens: 5,
			},
			ResponseID: "resp-123",
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Errorf("failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	p, err := NewLMStudioProvider(LMStudioProviderConfig{
		Name:    "lmstudio",
		BaseURL: server.URL,
	})
	if err != nil {
		t.Fatalf("NewLMStudioProvider failed: %v", err)
	}

	ctx := context.Background()
	resp, err := p.ChatCompletion(ctx, ChatRequest{
		Model: "test-model",
		Messages: []Message{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "Hello"},
		},
	})
	if err != nil {
		t.Fatalf("ChatCompletion failed: %v", err)
	}

	if resp.Message.Content != "Hi there!" {
		t.Errorf("expected content 'Hi there!', got %q", resp.Message.Content)
	}
	if resp.Message.Role != "assistant" {
		t.Errorf("expected role 'assistant', got %q", resp.Message.Role)
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("expected stop_reason 'end_turn', got %q", resp.StopReason)
	}
	if resp.Usage.InputTokens != 10 {
		t.Errorf("expected input tokens 10, got %d", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 5 {
		t.Errorf("expected output tokens 5, got %d", resp.Usage.OutputTokens)
	}
}

// TestLMStudioStreamChatCompletion tests streaming chat completion with basic content.
func TestLMStudioStreamChatCompletion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request
		if r.Method != "POST" {
			t.Errorf("expected POST request, got %s", r.Method)
		}
		if r.URL.Path != "/api/v1/chat" {
			t.Errorf("expected path /api/v1/chat, got %s", r.URL.Path)
		}

		// Decode and verify request body
		var req lmStudioRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("failed to decode request body: %v", err)
		}

		if req.Stream != true {
			t.Errorf("expected stream=true, got %v", req.Stream)
		}

		// Set SSE headers
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		// Write SSE events
		writeSSE(w, "chat.start", `{"model":"test-model"}`)
		writeSSE(w, "content.delta", `{"content":"Hello"}`)
		writeSSE(w, "content.delta", `{"content":" world"}`)
		writeSSE(w, "content.delta", `{"content":"!"}`)
		writeSSE(w, "chat.end", `{}`)
	}))
	defer server.Close()

	p, err := NewLMStudioProvider(LMStudioProviderConfig{
		Name:    "lmstudio",
		BaseURL: server.URL,
	})
	if err != nil {
		t.Fatalf("NewLMStudioProvider failed: %v", err)
	}

	ctx := context.Background()
	chunks, err := p.StreamChatCompletion(ctx, ChatRequest{
		Model:    "test-model",
		Messages: []Message{{Role: "user", Content: "Say hi"}},
		MaxTokens: 100,
	})
	if err != nil {
		t.Fatalf("StreamChatCompletion failed: %v", err)
	}

	var content string
	var stopReason string
	for chunk := range chunks {
		content += chunk.Delta
		if chunk.StopReason != "" {
			stopReason = chunk.StopReason
		}
	}

	if content != "Hello world!" {
		t.Errorf("expected content 'Hello world!', got %q", content)
	}
	if stopReason != "end_turn" {
		t.Errorf("expected stop_reason 'end_turn', got %q", stopReason)
	}
}

// TestLMStudioStreamChatCompletionWithReasoning tests streaming with reasoning delta events.
func TestLMStudioStreamChatCompletionWithReasoning(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		// Write SSE events with reasoning and content
		writeSSE(w, "chat.start", `{"model":"test-model"}`)
		writeSSE(w, "reasoning.delta", `{"content":"Let me think..."}`)
		writeSSE(w, "reasoning.delta", `{"content":" about this."}`)
		writeSSE(w, "content.delta", `{"content":"The answer is 42."}`)
		writeSSE(w, "chat.end", `{}`)
	}))
	defer server.Close()

	p, err := NewLMStudioProvider(LMStudioProviderConfig{
		Name:    "lmstudio",
		BaseURL: server.URL,
	})
	if err != nil {
		t.Fatalf("NewLMStudioProvider failed: %v", err)
	}

	ctx := context.Background()
	chunks, err := p.StreamChatCompletion(ctx, ChatRequest{
		Model:    "test-model",
		Messages: []Message{{Role: "user", Content: "What is the answer?"}},
	})
	if err != nil {
		t.Fatalf("StreamChatCompletion failed: %v", err)
	}

	var content string
	for chunk := range chunks {
		content += chunk.Delta
	}

	// Both reasoning and content deltas should be concatenated as Delta
	expected := "Let me think... about this.The answer is 42."
	if content != expected {
		t.Errorf("expected content %q, got %q", expected, content)
	}
}

// TestLMStudioStreamChatCompletionToolCalls tests streaming with tool call events.
func TestLMStudioStreamChatCompletionToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		// Write SSE events for tool call
		writeSSE(w, "chat.start", `{"model":"test-model"}`)
		writeSSE(w, "tool_call.start", `{"id":"call-123","name":"get_weather"}`)
		writeSSE(w, "tool_call.arguments", `{"arguments":"{\"location\":"}`)
		writeSSE(w, "tool_call.arguments", `{"arguments":"\"Paris\"}"}`)
		writeSSE(w, "tool_call.success", `{}`)
		writeSSE(w, "chat.end", `{}`)
	}))
	defer server.Close()

	p, err := NewLMStudioProvider(LMStudioProviderConfig{
		Name:    "lmstudio",
		BaseURL: server.URL,
	})
	if err != nil {
		t.Fatalf("NewLMStudioProvider failed: %v", err)
	}

	ctx := context.Background()
	chunks, err := p.StreamChatCompletion(ctx, ChatRequest{
		Model:    "test-model",
		Messages: []Message{{Role: "user", Content: "What's the weather?"}},
	})
	if err != nil {
		t.Fatalf("StreamChatCompletion failed: %v", err)
	}

	var toolCall *ToolCall
	var stopReason string
	for chunk := range chunks {
		if chunk.ToolCall != nil {
			toolCall = chunk.ToolCall
		}
		if chunk.StopReason != "" {
			stopReason = chunk.StopReason
		}
	}

	if toolCall == nil {
		t.Fatal("expected tool call, got nil")
	}
	if toolCall.ID != "call-123" {
		t.Errorf("expected tool call ID 'call-123', got %q", toolCall.ID)
	}
	if toolCall.Name != "get_weather" {
		t.Errorf("expected tool call name 'get_weather', got %q", toolCall.Name)
	}

	expectedInput := `{"location":"Paris"}`
	if string(toolCall.Input) != expectedInput {
		t.Errorf("expected tool call input %q, got %q", expectedInput, string(toolCall.Input))
	}
	if stopReason != "end_turn" {
		t.Errorf("expected stop_reason 'end_turn', got %q", stopReason)
	}
}

// TestLMStudioStreamChatCompletionError tests streaming error handling.
func TestLMStudioStreamChatCompletionError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		// Write error event
		writeSSE(w, "chat.start", `{"model":"test-model"}`)
		writeSSE(w, "error", `{"message":"Model not loaded","code":"model_not_loaded"}`)
	}))
	defer server.Close()

	p, err := NewLMStudioProvider(LMStudioProviderConfig{
		Name:    "lmstudio",
		BaseURL: server.URL,
	})
	if err != nil {
		t.Fatalf("NewLMStudioProvider failed: %v", err)
	}

	ctx := context.Background()
	chunks, err := p.StreamChatCompletion(ctx, ChatRequest{
		Model:    "test-model",
		Messages: []Message{{Role: "user", Content: "Hello"}},
	})
	if err != nil {
		t.Fatalf("StreamChatCompletion failed: %v", err)
	}

	var stopReason string
	for chunk := range chunks {
		if chunk.StopReason != "" {
			stopReason = chunk.StopReason
		}
	}

	if stopReason != "error" {
		t.Errorf("expected stop_reason 'error', got %q", stopReason)
	}
}

// TestLMStudioListModels tests listing available models.
func TestLMStudioListModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("expected GET request, got %s", r.Method)
		}
		if r.URL.Path != "/api/v1/models" {
			t.Errorf("expected path /api/v1/models, got %s", r.URL.Path)
		}

		resp := struct {
			Data []LMStudioModel `json:"data"`
		}{
			Data: []LMStudioModel{
				{
					ID:           "llama-3.2-1b",
					Type:         "llm",
					State:        "loaded",
					Architecture: "llama",
					Quantization: "Q4_K_M",
					MaxContext:   8192,
				},
				{
					ID:           "mistral-7b",
					Type:         "llm",
					State:        "not_loaded",
					Architecture: "mistral",
					Quantization: "Q5_K_M",
					MaxContext:   32768,
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Errorf("failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	p, err := NewLMStudioProvider(LMStudioProviderConfig{
		Name:    "lmstudio",
		BaseURL: server.URL,
	})
	if err != nil {
		t.Fatalf("NewLMStudioProvider failed: %v", err)
	}

	ctx := context.Background()
	models, err := p.ListModels(ctx)
	if err != nil {
		t.Fatalf("ListModels failed: %v", err)
	}

	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}

	if models[0].ID != "llama-3.2-1b" {
		t.Errorf("expected model ID 'llama-3.2-1b', got %q", models[0].ID)
	}
	if models[0].Type != "llm" {
		t.Errorf("expected type 'llm', got %q", models[0].Type)
	}
	if models[0].State != "loaded" {
		t.Errorf("expected state 'loaded', got %q", models[0].State)
	}
	if models[0].Architecture != "llama" {
		t.Errorf("expected architecture 'llama', got %q", models[0].Architecture)
	}
	if models[0].Quantization != "Q4_K_M" {
		t.Errorf("expected quantization 'Q4_K_M', got %q", models[0].Quantization)
	}
	if models[0].MaxContext != 8192 {
		t.Errorf("expected max_context 8192, got %d", models[0].MaxContext)
	}

	if models[1].ID != "mistral-7b" {
		t.Errorf("expected model ID 'mistral-7b', got %q", models[1].ID)
	}
	if models[1].State != "not_loaded" {
		t.Errorf("expected state 'not_loaded', got %q", models[1].State)
	}
}

// TestLMStudioLoadModel tests loading a model.
func TestLMStudioLoadModel(t *testing.T) {
	var receivedModel string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST request, got %s", r.Method)
		}
		if r.URL.Path != "/api/v1/models/load" {
			t.Errorf("expected path /api/v1/models/load, got %s", r.URL.Path)
		}

		// Decode request body
		var req map[string]string
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("failed to decode request body: %v", err)
		}
		receivedModel = req["model"]

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	p, err := NewLMStudioProvider(LMStudioProviderConfig{
		Name:    "lmstudio",
		BaseURL: server.URL,
	})
	if err != nil {
		t.Fatalf("NewLMStudioProvider failed: %v", err)
	}

	ctx := context.Background()
	err = p.LoadModel(ctx, "llama-3.2-1b")
	if err != nil {
		t.Fatalf("LoadModel failed: %v", err)
	}

	if receivedModel != "llama-3.2-1b" {
		t.Errorf("expected model 'llama-3.2-1b', got %q", receivedModel)
	}
}

// TestLMStudioUnloadModel tests unloading a model.
func TestLMStudioUnloadModel(t *testing.T) {
	var receivedModel string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST request, got %s", r.Method)
		}
		if r.URL.Path != "/api/v1/models/unload" {
			t.Errorf("expected path /api/v1/models/unload, got %s", r.URL.Path)
		}

		// Decode request body
		var req map[string]string
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("failed to decode request body: %v", err)
		}
		receivedModel = req["model"]

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	p, err := NewLMStudioProvider(LMStudioProviderConfig{
		Name:    "lmstudio",
		BaseURL: server.URL,
	})
	if err != nil {
		t.Fatalf("NewLMStudioProvider failed: %v", err)
	}

	ctx := context.Background()
	err = p.UnloadModel(ctx, "llama-3.2-1b")
	if err != nil {
		t.Fatalf("UnloadModel failed: %v", err)
	}

	if receivedModel != "llama-3.2-1b" {
		t.Errorf("expected model 'llama-3.2-1b', got %q", receivedModel)
	}
}

// TestLMStudioChatCompletionWithTools tests non-streaming response with tool calls.
func TestLMStudioChatCompletionWithTools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request has tools
		var req lmStudioRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("failed to decode request body: %v", err)
		}

		if len(req.Tools) != 1 {
			t.Errorf("expected 1 tool, got %d", len(req.Tools))
		}
		if req.Tools[0].Function.Name != "get_weather" {
			t.Errorf("expected tool name 'get_weather', got %q", req.Tools[0].Function.Name)
		}

		// Send response with tool call
		resp := lmStudioResponse{
			Output: []lmStudioOutputItem{
				{
					Type:      "tool_call",
					ID:        "call-456",
					Tool:      "get_weather",
					Arguments: json.RawMessage(`{"location":"Tokyo"}`),
				},
			},
			Stats: lmStudioStats{
				InputTokens:       20,
				TotalOutputTokens: 10,
			},
			ResponseID: "resp-456",
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Errorf("failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	p, err := NewLMStudioProvider(LMStudioProviderConfig{
		Name:    "lmstudio",
		BaseURL: server.URL,
	})
	if err != nil {
		t.Fatalf("NewLMStudioProvider failed: %v", err)
	}

	ctx := context.Background()
	resp, err := p.ChatCompletion(ctx, ChatRequest{
		Model: "test-model",
		Messages: []Message{
			{Role: "user", Content: "What's the weather in Tokyo?"},
		},
		Tools: []ToolDefinition{
			{
				Name:        "get_weather",
				Description: "Get weather for a location",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"location":{"type":"string"}}}`),
			},
		},
	})
	if err != nil {
		t.Fatalf("ChatCompletion failed: %v", err)
	}

	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.Message.ToolCalls))
	}

	tc := resp.Message.ToolCalls[0]
	if tc.ID != "call-456" {
		t.Errorf("expected tool call ID 'call-456', got %q", tc.ID)
	}
	if tc.Name != "get_weather" {
		t.Errorf("expected tool call name 'get_weather', got %q", tc.Name)
	}
	if string(tc.Input) != `{"location":"Tokyo"}` {
		t.Errorf("expected tool call input '{\"location\":\"Tokyo\"}', got %q", string(tc.Input))
	}
	if resp.StopReason != "tool_use" {
		t.Errorf("expected stop_reason 'tool_use', got %q", resp.StopReason)
	}
}

// TestLMStudioChatCompletionWithMixedOutput tests response with both message and reasoning.
func TestLMStudioChatCompletionWithMixedOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := lmStudioResponse{
			Output: []lmStudioOutputItem{
				{Type: "reasoning", Content: "Let me think about this..."},
				{Type: "message", Content: "Here is my answer."},
			},
			Stats: lmStudioStats{
				InputTokens:       10,
				TotalOutputTokens: 15,
			},
			ResponseID: "resp-mixed",
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Errorf("failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	p, err := NewLMStudioProvider(LMStudioProviderConfig{
		Name:    "lmstudio",
		BaseURL: server.URL,
	})
	if err != nil {
		t.Fatalf("NewLMStudioProvider failed: %v", err)
	}

	ctx := context.Background()
	resp, err := p.ChatCompletion(ctx, ChatRequest{
		Model:    "test-model",
		Messages: []Message{{Role: "user", Content: "Hello"}},
	})
	if err != nil {
		t.Fatalf("ChatCompletion failed: %v", err)
	}

	expectedContent := "Let me think about this...\nHere is my answer."
	if resp.Message.Content != expectedContent {
		t.Errorf("expected content %q, got %q", expectedContent, resp.Message.Content)
	}
}

// TestLMStudioChatCompletionError tests error response handling.
func TestLMStudioChatCompletionError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"error":{"message":"Internal server error","code":"internal_error"}}`)
	}))
	defer server.Close()

	p, err := NewLMStudioProvider(LMStudioProviderConfig{
		Name:    "lmstudio",
		BaseURL: server.URL,
	})
	if err != nil {
		t.Fatalf("NewLMStudioProvider failed: %v", err)
	}

	ctx := context.Background()
	_, err = p.ChatCompletion(ctx, ChatRequest{
		Model:    "test-model",
		Messages: []Message{{Role: "user", Content: "Hello"}},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !strings.Contains(err.Error(), "Internal server error") {
		t.Errorf("expected error to contain 'Internal server error', got %v", err)
	}
}

// TestLMStudioProviderWithAPIKey tests that API key is sent in Authorization header.
func TestLMStudioProviderWithAPIKey(t *testing.T) {
	var receivedAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"output":[],"stats":{}}`)
	}))
	defer server.Close()

	p, err := NewLMStudioProvider(LMStudioProviderConfig{
		Name:    "lmstudio",
		BaseURL: server.URL,
		APIKey:  "test-api-key",
	})
	if err != nil {
		t.Fatalf("NewLMStudioProvider failed: %v", err)
	}

	ctx := context.Background()
	_, _ = p.ChatCompletion(ctx, ChatRequest{
		Model:    "test-model",
		Messages: []Message{{Role: "user", Content: "Hello"}},
	})

	if receivedAuth != "Bearer test-api-key" {
		t.Errorf("expected Authorization 'Bearer test-api-key', got %q", receivedAuth)
	}
}

// TestLMStudioStreamChatCompletionMultipleToolCalls tests multiple tool calls in a stream.
func TestLMStudioStreamChatCompletionMultipleToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		// First tool call
		writeSSE(w, "chat.start", `{"model":"test-model"}`)
		writeSSE(w, "tool_call.start", `{"id":"call-1","name":"get_weather"}`)
		writeSSE(w, "tool_call.arguments", `{"arguments":"{\"loc\":\"NYC\"}"}`)
		writeSSE(w, "tool_call.success", `{}`)

		// Second tool call
		writeSSE(w, "tool_call.start", `{"id":"call-2","name":"get_time"}`)
		writeSSE(w, "tool_call.arguments", `{"arguments":"{\"tz\":\"EST\"}"}`)
		writeSSE(w, "tool_call.success", `{}`)

		writeSSE(w, "chat.end", `{}`)
	}))
	defer server.Close()

	p, err := NewLMStudioProvider(LMStudioProviderConfig{
		Name:    "lmstudio",
		BaseURL: server.URL,
	})
	if err != nil {
		t.Fatalf("NewLMStudioProvider failed: %v", err)
	}

	ctx := context.Background()
	chunks, err := p.StreamChatCompletion(ctx, ChatRequest{
		Model:    "test-model",
		Messages: []Message{{Role: "user", Content: "Weather and time?"}},
	})
	if err != nil {
		t.Fatalf("StreamChatCompletion failed: %v", err)
	}

	var toolCalls []ToolCall
	for chunk := range chunks {
		if chunk.ToolCall != nil {
			toolCalls = append(toolCalls, *chunk.ToolCall)
		}
	}

	if len(toolCalls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(toolCalls))
	}

	if toolCalls[0].ID != "call-1" || toolCalls[0].Name != "get_weather" {
		t.Errorf("unexpected first tool call: %+v", toolCalls[0])
	}
	if toolCalls[1].ID != "call-2" || toolCalls[1].Name != "get_time" {
		t.Errorf("unexpected second tool call: %+v", toolCalls[1])
	}
}

// TestLMStudioRequestWithTemperature tests that temperature is passed correctly.
func TestLMStudioRequestWithTemperature(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req lmStudioRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("failed to decode request body: %v", err)
		}

		if req.Temperature == nil || *req.Temperature != 0.7 {
			t.Errorf("expected temperature 0.7, got %v", req.Temperature)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"output":[{"type":"message","content":"ok"}],"stats":{}}`)
	}))
	defer server.Close()

	p, err := NewLMStudioProvider(LMStudioProviderConfig{
		Name:    "lmstudio",
		BaseURL: server.URL,
	})
	if err != nil {
		t.Fatalf("NewLMStudioProvider failed: %v", err)
	}

	temp := 0.7
	ctx := context.Background()
	_, err = p.ChatCompletion(ctx, ChatRequest{
		Model:       "test-model",
		Messages:    []Message{{Role: "user", Content: "Hello"}},
		Temperature: &temp,
	})
	if err != nil {
		t.Fatalf("ChatCompletion failed: %v", err)
	}
}

// TestLMStudioRequestWithToolCallHistory tests sending tool call history.
func TestLMStudioRequestWithToolCallHistory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req lmStudioRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("failed to decode request body: %v", err)
		}

		// Verify all 3 input messages: user, assistant with tool_calls, tool
		if len(req.Input) != 3 {
			t.Fatalf("expected 3 input messages, got %d", len(req.Input))
		}
		// First message is user
		if req.Input[0].Role != "user" {
			t.Errorf("expected first message role 'user', got %q", req.Input[0].Role)
		}
		// Second message is assistant with tool calls
		if req.Input[1].Role != "assistant" {
			t.Errorf("expected second message role 'assistant', got %q", req.Input[1].Role)
		}
		if len(req.Input[1].ToolCalls) != 1 {
			t.Errorf("expected 1 tool call in assistant message, got %d", len(req.Input[1].ToolCalls))
		}
		if req.Input[1].ToolCalls[0].ID != "tc-123" {
			t.Errorf("expected tool call ID 'tc-123', got %q", req.Input[1].ToolCalls[0].ID)
		}
		if req.Input[1].ToolCalls[0].Tool != "get_weather" {
			t.Errorf("expected tool call tool 'get_weather', got %q", req.Input[1].ToolCalls[0].Tool)
		}

		// Third message is tool response
		if req.Input[2].Role != "tool" {
			t.Errorf("expected third message role 'tool', got %q", req.Input[2].Role)
		}
		if req.Input[2].ToolCallID != "tc-123" {
			t.Errorf("expected tool_call_id 'tc-123', got %q", req.Input[2].ToolCallID)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"output":[{"type":"message","content":"done"}],"stats":{}}`)
	}))
	defer server.Close()

	p, err := NewLMStudioProvider(LMStudioProviderConfig{
		Name:    "lmstudio",
		BaseURL: server.URL,
	})
	if err != nil {
		t.Fatalf("NewLMStudioProvider failed: %v", err)
	}

	ctx := context.Background()
	_, err = p.ChatCompletion(ctx, ChatRequest{
		Model: "test-model",
		Messages: []Message{
			{Role: "user", Content: "What's the weather?"},
			{
				Role:    "assistant",
				Content: "",
				ToolCalls: []ToolCall{
					{ID: "tc-123", Name: "get_weather", Input: json.RawMessage(`{"loc":"NYC"}`)},
				},
			},
			{Role: "tool", Content: "Sunny, 72F", ToolCallID: "tc-123"},
		},
	})
	if err != nil {
		t.Fatalf("ChatCompletion failed: %v", err)
	}
}

// TestLMStudioProviderName tests the Name method.
func TestLMStudioProviderName(t *testing.T) {
	p, err := NewLMStudioProvider(LMStudioProviderConfig{
		Name:    "my-lmstudio",
		BaseURL: "http://localhost:1234",
	})
	if err != nil {
		t.Fatalf("NewLMStudioProvider failed: %v", err)
	}

	if p.Name() != "my-lmstudio" {
		t.Errorf("expected name 'my-lmstudio', got %q", p.Name())
	}
}

// TestLMStudioBaseURLTrailingSlash tests that trailing slashes are removed.
func TestLMStudioBaseURLTrailingSlash(t *testing.T) {
	p, err := NewLMStudioProvider(LMStudioProviderConfig{
		Name:    "lmstudio",
		BaseURL: "http://localhost:1234/",
	})
	if err != nil {
		t.Fatalf("NewLMStudioProvider failed: %v", err)
	}

	if p.baseURL != "http://localhost:1234" {
		t.Errorf("expected baseURL 'http://localhost:1234', got %q", p.baseURL)
	}
}

// TestLMStudioStreamChatCompletionWithContextCancellation tests context cancellation.
func TestLMStudioStreamChatCompletionWithContextCancellation(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		close(started)
		writeSSE(w, "chat.start", `{"model":"test-model"}`)
		// Slow stream - client should cancel before this completes
		time.Sleep(100 * time.Millisecond)
		writeSSE(w, "content.delta", `{"content":"Hello"}`)
	}))
	defer server.Close()

	p, err := NewLMStudioProvider(LMStudioProviderConfig{
		Name:    "lmstudio",
		BaseURL: server.URL,
	})
	if err != nil {
		t.Fatalf("NewLMStudioProvider failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	chunks, err := p.StreamChatCompletion(ctx, ChatRequest{
		Model:    "test-model",
		Messages: []Message{{Role: "user", Content: "Hello"}},
	})
	if err != nil {
		t.Fatalf("StreamChatCompletion failed: %v", err)
	}

	// Read chunks - should complete without blocking
	<-started
	for range chunks {
		// Just drain the channel
	}
}

// TestLMStudioMetadataSource tests the MetadataSource method.
func TestLMStudioMetadataSource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("expected GET request, got %s", r.Method)
		}
		if r.URL.Path != "/api/v1/models" {
			t.Errorf("expected path /api/v1/models, got %s", r.URL.Path)
		}

		resp := struct {
			Data []LMStudioModel `json:"data"`
		}{
			Data: []LMStudioModel{
				{
					ID:          "local-llama-model",
					Type:        "llm",
					State:       "loaded",
					MaxContext:  32768,
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Errorf("failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	p, err := NewLMStudioProvider(LMStudioProviderConfig{
		Name:    "lmstudio",
		BaseURL: server.URL,
	})
	if err != nil {
		t.Fatalf("NewLMStudioProvider failed: %v", err)
	}

	// Get the metadata source function
	source := p.MetadataSource()
	if source == nil {
		t.Fatal("MetadataSource returned nil")
	}

	// Test with a model that exists
	meta, found := source("local-llama-model")
	if !found {
		t.Error("expected found=true for existing model")
	}
	if meta.ContextWindow != 32768 {
		t.Errorf("expected ContextWindow 32768, got %d", meta.ContextWindow)
	}
	if meta.OutputLimit != 4096 {
		t.Errorf("expected OutputLimit 4096, got %d", meta.OutputLimit)
	}
	if meta.TokenizerType != "approximate" {
		t.Errorf("expected TokenizerType 'approximate', got %q", meta.TokenizerType)
	}

	// Test with a model that doesn't exist
	_, found = source("non-existent-model")
	if found {
		t.Error("expected found=false for non-existent model")
	}
}
