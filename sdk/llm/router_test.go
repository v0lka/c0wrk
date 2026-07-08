package llm

import (
	"context"
	"errors"
	"testing"
	"time"
)

// mockProvider implements Provider for testing.
type mockProvider struct {
	name      string
	lastReq   ChatRequest
	response  *ChatResponse
	err       error // error to return
	callCount int   // track number of calls
	errUntil  int   // return error for calls <= errUntil, then succeed
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

// newTestRouter creates a router with mock providers for testing. activeModel
// is treated as the composite selector; the bare model name is derived from it.
func newTestRouter(providers map[string]*mockProvider, activeProviderName, activeModel string) *Router {
	providerMap := make(map[string]Provider)
	for name, p := range providers {
		providerMap[name] = p
	}
	var activeProvider Provider
	if p, ok := providerMap[activeProviderName]; ok {
		activeProvider = p
	}
	return &Router{
		providers:           providerMap,
		activeProvider:      activeProvider,
		activeModel:         activeModel,
		activeBareModel:     BareModel(activeModel),
		activeProviderName:  activeProviderName,
		maxRetries:          3,
		initialBackoff:      10 * time.Millisecond,
		maxBackoff:          100 * time.Millisecond,
		safetyMarginPercent: 5,
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
		err:      NewError("test", 429, true, errors.New("rate limited")),
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
		err:  NewError("test", 401, false, errors.New("unauthorized")),
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
		err:  NewError("test", 503, true, errors.New("service unavailable")),
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
		err:  NewError("test", 429, true, errors.New("rate limited")),
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

func TestRouter_DefaultProvider(t *testing.T) {
	mock := &mockProvider{name: "default-prov"}
	router := newTestRouter(
		map[string]*mockProvider{"primary": mock},
		"primary", "model",
	)

	p := router.DefaultProvider()
	if p == nil {
		t.Fatal("expected non-nil default provider")
	}
	if p.Name() != "default-prov" {
		t.Errorf("expected name 'default-prov', got %q", p.Name())
	}
}

func TestRouter_Call_SetsDefaultTemperature(t *testing.T) {
	mock := &mockProvider{
		name: "test",
		response: &ChatResponse{
			Message:    Message{Role: "assistant", Content: "OK"},
			StopReason: "end_turn",
		},
	}
	router := newTestRouter(map[string]*mockProvider{"primary": mock}, "primary", "model")

	_, err := router.Call(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.lastReq.Temperature == nil {
		t.Fatal("expected temperature to be set")
	}
	if *mock.lastReq.Temperature != 0.0 {
		t.Errorf("expected temperature 0.0, got %f", *mock.lastReq.Temperature)
	}
}

func TestRouter_Call_PreservesExistingTemperature(t *testing.T) {
	mock := &mockProvider{
		name: "test",
		response: &ChatResponse{
			Message:    Message{Role: "assistant", Content: "OK"},
			StopReason: "end_turn",
		},
	}
	router := newTestRouter(map[string]*mockProvider{"primary": mock}, "primary", "model")

	temp := 0.7
	_, err := router.Call(context.Background(), ChatRequest{
		Messages:    []Message{{Role: "user", Content: "Hi"}},
		Temperature: &temp,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *mock.lastReq.Temperature != 0.7 {
		t.Errorf("expected temperature 0.7, got %f", *mock.lastReq.Temperature)
	}
}

func TestRouter_Call_FamilyAwareTemperature(t *testing.T) {
	mock := &mockProvider{
		name: "test",
		response: &ChatResponse{
			Message:    Message{Role: "assistant", Content: "OK"},
			StopReason: "end_turn",
		},
	}

	// Build a registry with a known model that supports temperature
	registry := NewModelRegistry(map[string]ModelMetadata{
		"deepseek-chat": {
			ContextWindow: 128000,
			OutputLimit:   8192,
			Family:        "deepseek",
			Capabilities:  ModelCapabilities{Temperature: true, ToolCall: true},
		},
	})

	// Sampling func returns 0.3 for deepseek
	sampling := func(family string) *float64 {
		if family == "deepseek" {
			v := 0.3
			return &v
		}
		return nil
	}

	router := &Router{
		providers:           map[string]Provider{"primary": mock},
		activeProvider:      mock,
		activeModel:         "deepseek-chat",
		activeBareModel:     "deepseek-chat",
		activeProviderName:  "primary",
		maxRetries:          0,
		initialBackoff:      10 * time.Millisecond,
		maxBackoff:          100 * time.Millisecond,
		registry:            registry,
		tokenCounter:        NewSimpleTokenCounter(),
		sampling:            sampling,
		safetyMarginPercent: 5,
	}

	_, err := router.Call(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.lastReq.Temperature == nil {
		t.Fatal("expected temperature to be set")
	}
	if *mock.lastReq.Temperature != 0.3 {
		t.Errorf("expected family-aware temperature 0.3, got %f", *mock.lastReq.Temperature)
	}
}

func TestRouter_Call_SkipsTemperatureForReasoningModels(t *testing.T) {
	mock := &mockProvider{
		name: "test",
		response: &ChatResponse{
			Message:    Message{Role: "assistant", Content: "OK"},
			StopReason: "end_turn",
		},
	}

	// Reasoning model with Temperature capability = false
	registry := NewModelRegistry(map[string]ModelMetadata{
		"o3": {
			ContextWindow: 200000,
			OutputLimit:   100000,
			Family:        "openai_flagship",
			Capabilities:  ModelCapabilities{Reasoning: true, ToolCall: true}, // Temperature: false
		},
	})

	sampling := func(family string) *float64 {
		v := 0.3
		return &v // would return 0.3, but should be ignored for reasoning models
	}

	router := &Router{
		providers:           map[string]Provider{"primary": mock},
		activeProvider:      mock,
		activeModel:         "o3",
		activeBareModel:     "o3",
		activeProviderName:  "primary",
		maxRetries:          0,
		initialBackoff:      10 * time.Millisecond,
		maxBackoff:          100 * time.Millisecond,
		registry:            registry,
		tokenCounter:        NewSimpleTokenCounter(),
		sampling:            sampling,
		safetyMarginPercent: 5,
	}

	_, err := router.Call(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.lastReq.Temperature != nil {
		t.Errorf("expected nil temperature for reasoning model, got %f", *mock.lastReq.Temperature)
	}
}

func TestNewRouter(t *testing.T) {
	// Test with openai provider (doesn't require external services)
	t.Run("openai provider", func(t *testing.T) {
		cfg := RouterConfig{
			Providers: []ProviderEntry{
				{Name: "openai", ProviderType: "openai", BaseURL: "http://localhost:9999", Models: []string{"test-model"}},
			},
			MaxRetries:     2,
			InitialBackoff: 100 * time.Millisecond,
			MaxBackoff:     1 * time.Second,
		}

		router, err := NewRouter(context.Background(), cfg, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if router == nil {
			t.Fatal("expected non-nil router")
		}
		if router.activeModel != "openai/test-model" {
			t.Errorf("expected activeModel 'openai/test-model', got %q", router.activeModel)
		}
		if router.activeBareModel != "test-model" {
			t.Errorf("expected activeBareModel 'test-model', got %q", router.activeBareModel)
		}
		if router.maxRetries != 2 {
			t.Errorf("expected maxRetries 2, got %d", router.maxRetries)
		}
		if router.initialBackoff != 100*time.Millisecond {
			t.Errorf("expected initialBackoff 100ms, got %v", router.initialBackoff)
		}
		if router.maxBackoff != 1*time.Second {
			t.Errorf("expected maxBackoff 1s, got %v", router.maxBackoff)
		}
	})

	// Test with openai provider and model registry
	t.Run("openai with registry", func(t *testing.T) {
		cfg := RouterConfig{
			Providers: []ProviderEntry{
				{Name: "openai", ProviderType: "openai", BaseURL: "http://localhost:9999", Models: []string{"test-model"}},
			},
			MaxRetries:     1,
			InitialBackoff: 50 * time.Millisecond,
			MaxBackoff:     500 * time.Millisecond,
		}

		registry := NewModelRegistry(nil)
		router, err := NewRouter(context.Background(), cfg, registry)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if router == nil {
			t.Fatal("expected non-nil router")
		}
	})

	// Test with no active provider
	t.Run("no active provider", func(t *testing.T) {
		cfg := RouterConfig{}
		_, err := NewRouter(context.Background(), cfg, nil)
		if err == nil {
			t.Fatal("expected error for no active provider")
		}
	})

	// Test with openai provider (no key = ok, openai doesn't validate key at creation)
	t.Run("openai provider without key", func(t *testing.T) {
		cfg := RouterConfig{
			Providers: []ProviderEntry{
				{Name: "openai", ProviderType: "openai", BaseURL: "http://localhost:9999", Models: []string{"test-model"}},
			},
			MaxRetries: 0,
		}

		router, err := NewRouter(context.Background(), cfg, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if router.maxRetries != 3 {
			t.Errorf("expected default maxRetries 3, got %d", router.maxRetries)
		}
	})

	// Test with openai provider and zero backoff
	t.Run("zero backoff durations use defaults", func(t *testing.T) {
		cfg := RouterConfig{
			Providers: []ProviderEntry{
				{Name: "openai", ProviderType: "openai", BaseURL: "http://localhost:9999", Models: []string{"test-model"}},
			},
			InitialBackoff: 0,
			MaxBackoff:     0,
		}

		router, err := NewRouter(context.Background(), cfg, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Should use defaults
		if router.initialBackoff != 1*time.Second {
			t.Errorf("expected default initialBackoff 1s, got %v", router.initialBackoff)
		}
		if router.maxBackoff != 30*time.Second {
			t.Errorf("expected default maxBackoff 30s, got %v", router.maxBackoff)
		}
	})

	// Test with anthropic provider (no key = ok; the official endpoint will 401
	// at call time, but construction succeeds to support local
	// Anthropic-compatible servers that need no auth — parity with OpenAI).
	t.Run("anthropic without key succeeds", func(t *testing.T) {
		cfg := RouterConfig{
			Providers: []ProviderEntry{
				{Name: "anthropic", ProviderType: "anthropic", Models: []string{"claude-3-sonnet"}},
			},
		}

		_, err := NewRouter(context.Background(), cfg, nil)
		if err != nil {
			t.Fatalf("unexpected error for anthropic without API key: %v", err)
		}
	})
}

func TestCreateProviderFromConfig(t *testing.T) {
	tests := []struct {
		name         string
		providerName string
		provType     string
		apiKey       string
		baseURL      string
		wantErr      bool
		wantName     string
	}{
		{
			name:         "openai provider with named config key",
			providerName: "lmstudio",
			provType:     "openai",
			apiKey:       "test-key",
			baseURL:      "http://localhost:1234",
			wantErr:      false,
			wantName:     "lmstudio",
		},
		{
			name:         "openai provider with default name",
			providerName: "openai",
			provType:     "openai",
			apiKey:       "test-key",
			baseURL:      "https://api.openai.com/v1",
			wantErr:      false,
			wantName:     "openai",
		},
		{
			name:         "anthropic provider with named config key and custom base url",
			providerName: "my-anthropic-proxy",
			provType:     "anthropic",
			apiKey:       "test-key",
			baseURL:      "https://my-anthropic-proxy.example.com",
			wantErr:      false,
			wantName:     "my-anthropic-proxy",
		},
		{
			name:         "anthropic provider without key succeeds",
			providerName: "anthropic",
			provType:     "anthropic",
			apiKey:       "",
			wantErr:      false,
			wantName:     "anthropic",
		},
		{
			name:         "unknown provider",
			providerName: "whatever",
			provType:     "unknown",
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := createProviderFromConfig(context.Background(), tt.providerName, tt.provType, tt.apiKey, tt.baseURL, nil)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if p == nil {
				t.Fatal("expected non-nil provider")
			}
			if tt.wantName != "" && p.Name() != tt.wantName {
				t.Errorf("expected name %q, got %q", tt.wantName, p.Name())
			}
		})
	}
}

// newTestRouterWithRegistry creates a router with mock provider, model registry and token counter.
func newTestRouterWithRegistry(mock *mockProvider, activeModel string, registry *ModelRegistry) *Router {
	return &Router{
		providers:           map[string]Provider{"primary": mock},
		activeProvider:      mock,
		activeModel:         activeModel,
		activeBareModel:     BareModel(activeModel),
		activeProviderName:  "primary",
		maxRetries:          0,
		initialBackoff:      10 * time.Millisecond,
		maxBackoff:          100 * time.Millisecond,
		registry:            registry,
		tokenCounter:        NewSimpleTokenCounter(),
		safetyMarginPercent: 5,
	}
}

func TestRouter_ContextWindowValidation(t *testing.T) {
	// Helper: create a string of approximately N tokens (4 chars ≈ 1 token for SimpleTokenCounter)
	makeContent := func(approxTokens int) string {
		// SimpleTokenCounter uses (len+3)/4, so len = approxTokens*4 gives ~approxTokens tokens
		return string(make([]byte, approxTokens*4))
	}

	successResp := &ChatResponse{
		Message:    Message{Role: "assistant", Content: "OK"},
		StopReason: "end_turn",
	}

	tests := []struct {
		name          string
		contextWindow int
		outputLimit   int
		model         string
		msgTokens     int // approximate token count for request messages
		useRegistry   bool
		wantErr       bool
		errSentinel   error // if non-nil, check errors.Is
	}{
		{
			name:          "within context window - call proceeds",
			contextWindow: 10000,
			outputLimit:   2000,
			model:         "test-model",
			msgTokens:     100,
			useRegistry:   true,
			wantErr:       false,
		},
		{
			name:          "exceeds context window - returns error",
			contextWindow: 1000,
			outputLimit:   200,
			model:         "test-model",
			msgTokens:     900, // exceeds (1000-200)*0.95 = 760 effective limit
			useRegistry:   true,
			wantErr:       true,
			errSentinel:   ErrContextWindowExceeded,
		},
		{
			name:          "no registry - skips validation",
			contextWindow: 100,
			outputLimit:   50,
			model:         "test-model",
			msgTokens:     200,
			useRegistry:   false,
			wantErr:       false,
		},
		{
			name:          "context window 0 in metadata - skips validation",
			contextWindow: 0,
			outputLimit:   0,
			model:         "zero-ctx-model",
			msgTokens:     5000,
			useRegistry:   true,
			wantErr:       false,
		},
		{
			name:          "output limit 0 uses default reserve of 4096",
			contextWindow: 10000,
			outputLimit:   0,
			model:         "no-output-limit",
			msgTokens:     100,
			useRegistry:   true,
			wantErr:       false,
		},
		{
			name:          "safety margin - just over effective max fails",
			contextWindow: 1000,
			outputLimit:   200,
			model:         "margin-model",
			// effective = (1000-200)*0.95 = 760
			msgTokens:   761,
			useRegistry: true,
			wantErr:     true,
			errSentinel: ErrContextWindowExceeded,
		},
		{
			name:          "safety margin - just under effective max succeeds",
			contextWindow: 1000,
			outputLimit:   200,
			model:         "margin-model",
			// effective = (1000-200)*0.95 = 760
			msgTokens:   750,
			useRegistry: true,
			wantErr:     false,
		},
		{
			name:          "error is non-retryable",
			contextWindow: 1000,
			outputLimit:   200,
			model:         "test-model",
			msgTokens:     900,
			useRegistry:   true,
			wantErr:       true,
			errSentinel:   ErrContextWindowExceeded,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name+" (Call)", func(t *testing.T) {
			mock := &mockProvider{name: "test", response: successResp}

			var registry *ModelRegistry
			if tt.useRegistry {
				overrides := map[string]ModelMetadata{
					tt.model: {
						ContextWindow: tt.contextWindow,
						OutputLimit:   tt.outputLimit,
						TokenizerType: "approximate",
					},
				}
				registry = NewModelRegistry(overrides)
			}

			router := newTestRouterWithRegistry(mock, tt.model, registry)

			req := ChatRequest{
				Messages: []Message{{Role: "user", Content: makeContent(tt.msgTokens)}},
			}

			_, err := router.Call(context.Background(), req)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.errSentinel != nil && !errors.Is(err, tt.errSentinel) {
					t.Errorf("expected errors.Is(%v), got: %v", tt.errSentinel, err)
				}
				if IsRetryable(err) {
					t.Error("context window error should not be retryable")
				}
				// Provider should NOT have been called
				if mock.callCount != 0 {
					t.Errorf("expected 0 provider calls, got %d", mock.callCount)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if mock.callCount != 1 {
					t.Errorf("expected 1 provider call, got %d", mock.callCount)
				}
			}
		})
	}
}

// TestNewRouter_TwoProvidersSameModelName verifies that the router's reverse
// index keeps two providers exposing the same bare model name distinguishable.
// This is the core multi-provider disambiguation scenario.
func TestNewRouter_TwoProvidersSameModelName(t *testing.T) {
	cfg := RouterConfig{
		Providers: []ProviderEntry{
			{Name: "openai", ProviderType: "openai", BaseURL: "http://localhost:9999", Models: []string{"gpt-4"}},
			{Name: "lmstudio", ProviderType: "openai", BaseURL: "http://localhost:1234", Models: []string{"gpt-4"}},
		},
	}
	router, err := NewRouter(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Both composite keys must be present and map to distinct providers.
	if got := router.modelToProvider["openai/gpt-4"]; got != "openai" {
		t.Errorf("modelToProvider[openai/gpt-4] = %q, want openai", got)
	}
	if got := router.modelToProvider["lmstudio/gpt-4"]; got != "lmstudio" {
		t.Errorf("modelToProvider[lmstudio/gpt-4] = %q, want lmstudio", got)
	}
	// The bare key must NOT be present (the old collision-prone behaviour).
	if _, ok := router.modelToProvider["gpt-4"]; ok {
		t.Error("bare model name should not be a key in modelToProvider")
	}
}

// TestRouter_SetModel_CompositeAndBare covers SetModel with composite
// identifiers (disambiguation) and bare names (backward compatibility,
// including the ambiguous case).
func TestRouter_SetModel_CompositeAndBare(t *testing.T) {
	provA := &mockProvider{name: "openai"}
	provB := &mockProvider{name: "lmstudio"}
	router := &Router{
		providers: map[string]Provider{
			"openai":   provA,
			"lmstudio": provB,
		},
		modelToProvider: map[string]string{
			"openai/gpt-4":   "openai",
			"lmstudio/gpt-4": "lmstudio",
		},
		activeProvider:     provA,
		activeModel:        "openai/gpt-4",
		activeBareModel:    "gpt-4",
		activeProviderName: "openai",
		maxRetries:         0,
	}

	// Composite selector routes to the named provider.
	if err := router.SetModel(context.Background(), "lmstudio/gpt-4"); err != nil {
		t.Fatalf("SetModel(composite) error: %v", err)
	}
	if router.activeProviderName != "lmstudio" {
		t.Errorf("activeProviderName = %q, want lmstudio", router.activeProviderName)
	}
	if router.activeModel != "lmstudio/gpt-4" {
		t.Errorf("activeModel = %q, want lmstudio/gpt-4", router.activeModel)
	}
	if router.activeBareModel != "gpt-4" {
		t.Errorf("activeBareModel = %q, want gpt-4", router.activeBareModel)
	}
	if router.activeProvider != Provider(provB) {
		t.Error("activeProvider should be the lmstudio mock")
	}

	// Switch back via composite.
	if err := router.SetModel(context.Background(), "openai/gpt-4"); err != nil {
		t.Fatalf("SetModel(composite) error: %v", err)
	}
	if router.activeProviderName != "openai" {
		t.Errorf("activeProviderName = %q, want openai", router.activeProviderName)
	}

	// Bare name (backward compatibility) resolves to first match deterministically.
	if err := router.SetModel(context.Background(), "gpt-4"); err != nil {
		t.Fatalf("SetModel(bare) error: %v", err)
	}
	if router.activeBareModel != "gpt-4" {
		t.Errorf("activeBareModel = %q, want gpt-4", router.activeBareModel)
	}
	// First match by sorted composite id: "lmstudio/gpt-4" < "openai/gpt-4".
	if router.activeProviderName != "lmstudio" {
		t.Errorf("activeProviderName = %q, want lmstudio (first sorted match)", router.activeProviderName)
	}

	// Unknown composite provider.
	if err := router.SetModel(context.Background(), "nope/gpt-4"); err == nil {
		t.Error("expected error for unknown composite provider, got nil")
	}
	// Unknown bare model.
	if err := router.SetModel(context.Background(), "does-not-exist"); err == nil {
		t.Error("expected error for unknown bare model, got nil")
	}
}

// TestRouter_Call_SendsBareModelToProvider confirms the provider API receives
// the bare model name (without provider prefix), not the composite selector.
func TestRouter_Call_SendsBareModelToProvider(t *testing.T) {
	mock := &mockProvider{
		name: "lmstudio",
		response: &ChatResponse{
			Message:    Message{Role: "assistant", Content: "OK"},
			StopReason: "end_turn",
		},
	}
	router := &Router{
		providers: map[string]Provider{"lmstudio": mock},
		modelToProvider: map[string]string{
			"lmstudio/gpt-4": "lmstudio",
		},
		activeProvider:     mock,
		activeModel:        "lmstudio/gpt-4",
		activeBareModel:    "gpt-4",
		activeProviderName: "lmstudio",
		maxRetries:         0,
	}

	// Empty Model → router fills the bare model name.
	resp, err := router.Call(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.lastReq.Model != "gpt-4" {
		t.Errorf("provider received Model %q, want bare gpt-4", mock.lastReq.Model)
	}
	if resp.Model != "gpt-4" {
		t.Errorf("response Model = %q, want bare gpt-4", resp.Model)
	}
}

// TestParseCompositeModelID covers the split-on-first-slash semantics, including
// model names that themselves contain a slash.
func TestParseCompositeModelID(t *testing.T) {
	tests := []struct {
		id             string
		wantProvider   string
		wantModel      string
		wantOk         bool
		wantBare       string
		wantIsComp     bool
		wantProviderOf string
	}{
		{"openai/gpt-4", "openai", "gpt-4", true, "gpt-4", true, "openai"},
		{"lmstudio/gpt-4", "lmstudio", "gpt-4", true, "gpt-4", true, "lmstudio"},
		{"hf/meta-llama/Llama-3-70b", "hf", "meta-llama/Llama-3-70b", true, "meta-llama/Llama-3-70b", true, "hf"},
		{"gpt-4", "", "gpt-4", false, "gpt-4", false, ""},
		{"", "", "", false, "", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			prov, model, ok := ParseCompositeModelID(tt.id)
			if prov != tt.wantProvider || model != tt.wantModel || ok != tt.wantOk {
				t.Errorf("ParseCompositeModelID(%q) = (%q,%q,%v), want (%q,%q,%v)",
					tt.id, prov, model, ok, tt.wantProvider, tt.wantModel, tt.wantOk)
			}
			if got := BareModel(tt.id); got != tt.wantBare {
				t.Errorf("BareModel(%q) = %q, want %q", tt.id, got, tt.wantBare)
			}
			if got := IsCompositeModelID(tt.id); got != tt.wantIsComp {
				t.Errorf("IsCompositeModelID(%q) = %v, want %v", tt.id, got, tt.wantIsComp)
			}
			if got := providerOf(tt.id); got != tt.wantProviderOf {
				t.Errorf("providerOf(%q) = %q, want %q", tt.id, got, tt.wantProviderOf)
			}
		})
	}
}
