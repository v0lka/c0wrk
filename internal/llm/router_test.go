package llm

import (
	"context"
	"testing"
)

// mockProvider implements LLMProvider for testing.
type mockProvider struct {
	name           string
	lastReq        ChatRequest
	response       *ChatResponse
	streamResponse []ChatChunk
}

func (m *mockProvider) Name() string {
	return m.name
}

func (m *mockProvider) ChatCompletion(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	m.lastReq = req
	return m.response, nil
}

func (m *mockProvider) StreamChatCompletion(ctx context.Context, req ChatRequest) (<-chan ChatChunk, error) {
	m.lastReq = req
	ch := make(chan ChatChunk)
	go func() {
		defer close(ch)
		for _, chunk := range m.streamResponse {
			ch <- chunk
		}
	}()
	return ch, nil
}

// newTestRouter creates a router with mock providers for testing.
func newTestRouter(providers map[string]*mockProvider, activeProviderName, activeModel string) *LLMRouter {
	providerMap := make(map[string]LLMProvider)
	for name, p := range providers {
		providerMap[name] = p
	}
	var activeProvider LLMProvider
	if p, ok := providerMap[activeProviderName]; ok {
		activeProvider = p
	}
	return &LLMRouter{
		providers:          providerMap,
		activeProvider:     activeProvider,
		activeModel:        activeModel,
		activeProviderName: activeProviderName,
	}
}

func TestRouter_Call_SetsModelAndDelegatesToProvider(t *testing.T) {
	mock := &mockProvider{
		name: "test-provider",
		response: &ChatResponse{
			Message:    Message{Role: "assistant", Content: "Hello!"},
			StopReason: "end_turn",
		},
	}

	router := newTestRouter(
		map[string]*mockProvider{"primary": mock},
		"primary", "gpt-4",
	)

	req := ChatRequest{
		Messages: []Message{{Role: "user", Content: "Hi"}},
	}

	resp, err := router.Call(context.Background(), "executor", req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify model was set
	if mock.lastReq.Model != "gpt-4" {
		t.Errorf("expected model 'gpt-4', got %q", mock.lastReq.Model)
	}

	// Verify response was returned
	if resp.Message.Content != "Hello!" {
		t.Errorf("expected content 'Hello!', got %q", resp.Message.Content)
	}
}

func TestRouter_Call_DoesNotOverrideExistingModel(t *testing.T) {
	mock := &mockProvider{
		name: "test-provider",
		response: &ChatResponse{
			Message:    Message{Role: "assistant", Content: "OK"},
			StopReason: "end_turn",
		},
	}

	router := newTestRouter(
		map[string]*mockProvider{"primary": mock},
		"primary", "gpt-4",
	)

	// Request with explicit model
	req := ChatRequest{
		Model:    "gpt-4-turbo",
		Messages: []Message{{Role: "user", Content: "Test"}},
	}

	_, err := router.Call(context.Background(), "executor", req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify model was preserved
	if mock.lastReq.Model != "gpt-4-turbo" {
		t.Errorf("expected Model 'gpt-4-turbo', got %q", mock.lastReq.Model)
	}
}

func TestRouter_Stream_DelegatesToProvider(t *testing.T) {
	mock := &mockProvider{
		name: "test-provider",
		streamResponse: []ChatChunk{
			{Delta: "Hello"},
			{Delta: " World"},
			{StopReason: "end_turn"},
		},
	}

	router := newTestRouter(
		map[string]*mockProvider{"primary": mock},
		"primary", "claude-3",
	)

	req := ChatRequest{
		Messages: []Message{{Role: "user", Content: "Stream test"}},
	}

	ch, err := router.Stream(context.Background(), "executor", req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify model was set
	if mock.lastReq.Model != "claude-3" {
		t.Errorf("expected model 'claude-3', got %q", mock.lastReq.Model)
	}

	// Collect chunks (preallocate with capacity from mock)
	chunks := make([]ChatChunk, 0, len(mock.streamResponse))
	for chunk := range ch {
		chunks = append(chunks, chunk)
	}

	if len(chunks) != 3 {
		t.Errorf("expected 3 chunks, got %d", len(chunks))
	}

	if chunks[0].Delta != "Hello" {
		t.Errorf("expected first chunk 'Hello', got %q", chunks[0].Delta)
	}
}
