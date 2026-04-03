package llm

import (
	"context"
	"errors"
	"testing"
	"time"
)

// mockProvider implements LLMProvider for testing.
type mockProvider struct {
	name           string
	lastReq        ChatRequest
	response       *ChatResponse
	streamResponse []ChatChunk
	err            error // error to return
	callCount      int   // track number of calls
	errUntil       int   // return error for calls <= errUntil, then succeed
}

func (m *mockProvider) Name() string {
	return m.name
}

func (m *mockProvider) ChatCompletion(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	m.lastReq = req
	m.callCount++
	if m.errUntil > 0 && m.callCount <= m.errUntil {
		return nil, m.err
	}
	if m.err != nil && m.errUntil == 0 {
		return nil, m.err
	}
	return m.response, nil
}

func (m *mockProvider) StreamChatCompletion(ctx context.Context, req ChatRequest) (<-chan ChatChunk, error) {
	m.lastReq = req
	m.callCount++
	if m.errUntil > 0 && m.callCount <= m.errUntil {
		return nil, m.err
	}
	if m.err != nil && m.errUntil == 0 {
		return nil, m.err
	}
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
		maxRetries:         3,
		initialBackoff:     10 * time.Millisecond,
		maxBackoff:         100 * time.Millisecond,
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

	resp, err := router.Call(context.Background(), req)
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

	_, err := router.Call(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify model was preserved
	if mock.lastReq.Model != "gpt-4-turbo" {
		t.Errorf("expected Model 'gpt-4-turbo', got %q", mock.lastReq.Model)
	}
}

func TestRouter_Call_RetriesOnRetryableError(t *testing.T) {
	mock := &mockProvider{
		name: "test",
		response: &ChatResponse{
			Message:    Message{Role: "assistant", Content: "OK"},
			StopReason: "end_turn",
		},
		err:      NewLLMError("test", 429, true, errors.New("rate limited")),
		errUntil: 1,
	}
	router := newTestRouter(map[string]*mockProvider{"primary": mock}, "primary", "model")

	resp, err := router.Call(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "Hi"}}})
	if err != nil {
		t.Fatalf("expected success after retry, got: %v", err)
	}
	if resp.Message.Content != "OK" {
		t.Errorf("unexpected content: %s", resp.Message.Content)
	}
	if mock.callCount != 2 {
		t.Errorf("expected 2 calls (1 fail + 1 success), got %d", mock.callCount)
	}
}

func TestRouter_Call_NoRetryOnNonRetryableError(t *testing.T) {
	mock := &mockProvider{
		name: "test",
		err:  NewLLMError("test", 401, false, errors.New("unauthorized")),
	}
	router := newTestRouter(map[string]*mockProvider{"primary": mock}, "primary", "model")

	_, err := router.Call(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "Hi"}}})
	if err == nil {
		t.Fatal("expected error")
	}
	if mock.callCount != 1 {
		t.Errorf("expected 1 call (no retry), got %d", mock.callCount)
	}
}

func TestRouter_Call_ExhaustsRetries(t *testing.T) {
	mock := &mockProvider{
		name: "test",
		err:  NewLLMError("test", 503, true, errors.New("service unavailable")),
	}
	router := newTestRouter(map[string]*mockProvider{"primary": mock}, "primary", "model")

	_, err := router.Call(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "Hi"}}})
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	// maxRetries=3, so 4 total attempts (initial + 3 retries)
	if mock.callCount != 4 {
		t.Errorf("expected 4 calls (1 initial + 3 retries), got %d", mock.callCount)
	}
}

func TestRouter_Call_RespectsContextCancellation(t *testing.T) {
	mock := &mockProvider{
		name: "test",
		err:  NewLLMError("test", 429, true, errors.New("rate limited")),
	}
	router := newTestRouter(map[string]*mockProvider{"primary": mock}, "primary", "model")

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel immediately so backoff sleep is interrupted
	cancel()

	_, err := router.Call(ctx, ChatRequest{Messages: []Message{{Role: "user", Content: "Hi"}}})
	if err == nil {
		t.Fatal("expected error")
	}
	// Should have made only 1 call, then context cancelled during backoff
	if mock.callCount > 2 {
		t.Errorf("expected at most 2 calls with cancelled context, got %d", mock.callCount)
	}
}

func TestRouter_Stream_RetriesOnRetryableError(t *testing.T) {
	mock := &mockProvider{
		name:           "test",
		streamResponse: []ChatChunk{{Delta: "Hello"}, {StopReason: "end_turn"}},
		err:            NewLLMError("test", 502, true, errors.New("bad gateway")),
		errUntil:       1,
	}
	router := newTestRouter(map[string]*mockProvider{"primary": mock}, "primary", "model")

	ch, err := router.Stream(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "Hi"}}})
	if err != nil {
		t.Fatalf("expected success after retry, got: %v", err)
	}

	chunks := make([]ChatChunk, 0)
	for chunk := range ch {
		chunks = append(chunks, chunk)
	}
	if len(chunks) != 2 {
		t.Errorf("expected 2 chunks, got %d", len(chunks))
	}
	if mock.callCount != 2 {
		t.Errorf("expected 2 calls, got %d", mock.callCount)
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

	ch, err := router.Stream(context.Background(), req)
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
