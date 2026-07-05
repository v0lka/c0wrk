package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
)

func TestModelRegistry_OverridePriority(t *testing.T) {
	// Create registry with override for a built-in model
	overrides := map[string]ModelMetadata{
		"gpt-4o": {
			ContextWindow: 999999,
			OutputLimit:   8888,
			TokenizerType: "custom-tokenizer",
		},
	}

	registry := NewModelRegistry(overrides)

	// Override should take priority over built-in
	meta, ok := registry.Resolve(context.Background(), "gpt-4o")
	if !ok {
		t.Fatal("expected ok=true for override model")
	}

	if meta.ContextWindow != 999999 {
		t.Errorf("expected ContextWindow 999999, got %d", meta.ContextWindow)
	}
	if meta.OutputLimit != 8888 {
		t.Errorf("expected OutputLimit 8888, got %d", meta.OutputLimit)
	}
	if meta.TokenizerType != "custom-tokenizer" {
		t.Errorf("expected TokenizerType 'custom-tokenizer', got %s", meta.TokenizerType)
	}
}

func TestModelRegistry_BuiltInResolution(t *testing.T) {
	registry := NewModelRegistry(nil)

	tests := []struct {
		model                 string
		expectedContextWindow int
		expectedOutputLimit   int
		expectedTokenizer     string
	}{
		// OpenAI models — verified July 2026 from platform.openai.com/docs/models
		{"gpt-5.4", 1050000, 128000, "tiktoken/o200k_base"},
		{"gpt-4o", 128000, 16384, "tiktoken/o200k_base"},
		{"o3-mini", 200000, 100000, "tiktoken/o200k_base"},

		// OpenAI Codex models
		{"codex-mini-latest", 200000, 100000, "tiktoken/o200k_base"},

		// Anthropic models — verified July 2026 from platform.claude.com/docs
		{"claude-opus-4.6", 1000000, 128000, "anthropic-api"},
		{"claude-3.5-sonnet", 200000, 8192, "anthropic-api"},

		// Gemini models
		{"gemini-2.5-pro", 1048576, 65536, "approximate"},
		{"gemini-2.0-flash", 1048576, 8192, "approximate"},

		// DeepSeek V4 models
		{"deepseek-v4-pro", 1000000, 16384, "approximate"},
		{"deepseek-v4-flash", 1000000, 16384, "approximate"},

		// Grok models — verified from docs.x.ai
		{"grok-4.20", 1000000, 32768, "approximate"},
		{"grok-3-mini", 131072, 32768, "approximate"},

		// GLM models (Zhipu AI) — verified from docs.z.ai
		{"glm-5.2", 1000000, 128000, "approximate"},
		{"glm-5.1", 200000, 128000, "approximate"},
		{"glm-5", 200000, 128000, "approximate"},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			meta, _ := registry.Resolve(context.Background(), tt.model)

			if meta.ContextWindow != tt.expectedContextWindow {
				t.Errorf("expected ContextWindow %d, got %d", tt.expectedContextWindow, meta.ContextWindow)
			}
			if meta.OutputLimit != tt.expectedOutputLimit {
				t.Errorf("expected OutputLimit %d, got %d", tt.expectedOutputLimit, meta.OutputLimit)
			}
			if meta.TokenizerType != tt.expectedTokenizer {
				t.Errorf("expected TokenizerType %s, got %s", tt.expectedTokenizer, meta.TokenizerType)
			}
		})
	}
}

func TestModelRegistry_FallbackForUnknownModel(t *testing.T) {
	registry := NewModelRegistry(nil)

	// Unknown model should return fallback defaults
	meta, ok := registry.Resolve(context.Background(), "unknown-model-v123")
	if ok {
		t.Fatal("expected ok=false for unknown model")
	}

	expected := ModelMetadata{
		ContextWindow: 128000,
		OutputLimit:   4096,
		TokenizerType: "approximate",
	}

	if meta.ContextWindow != expected.ContextWindow {
		t.Errorf("expected ContextWindow %d, got %d", expected.ContextWindow, meta.ContextWindow)
	}
	if meta.OutputLimit != expected.OutputLimit {
		t.Errorf("expected OutputLimit %d, got %d", expected.OutputLimit, meta.OutputLimit)
	}
	if meta.TokenizerType != expected.TokenizerType {
		t.Errorf("expected TokenizerType %s, got %s", expected.TokenizerType, meta.TokenizerType)
	}
}

func TestModelRegistry_Invalidate(t *testing.T) {
	registry := NewModelRegistry(nil)

	// Manually add an entry to the cache
	registry.mu.Lock()
	registry.cache["cached-model"] = ModelMetadata{
		ContextWindow: 50000,
		OutputLimit:   2000,
		TokenizerType: "cached-tokenizer",
	}
	registry.mu.Unlock()

	// Verify it's in cache
	registry.mu.RLock()
	_, exists := registry.cache["cached-model"]
	registry.mu.RUnlock()

	if !exists {
		t.Fatal("cached model should exist before invalidation")
	}

	// Invalidate the cache entry
	registry.Invalidate("cached-model")

	// Verify it's removed from cache
	registry.mu.RLock()
	_, exists = registry.cache["cached-model"]
	registry.mu.RUnlock()

	if exists {
		t.Error("cached model should not exist after invalidation")
	}
}

func TestModelRegistry_ThreadSafe(t *testing.T) {
	registry := NewModelRegistry(nil)

	// Run multiple goroutines concurrently accessing Resolve
	var wg sync.WaitGroup
	numGoroutines := 100
	numIterations := 50

	// Test concurrent reads of built-in models
	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < numIterations; j++ {
				_, _ = registry.Resolve(context.Background(), "gpt-4o")
				_, _ = registry.Resolve(context.Background(), "claude-opus-4.6")
				_, _ = registry.Resolve(context.Background(), "unknown-model")
			}
		}()
	}

	// Test concurrent cache invalidations
	wg.Add(numGoroutines / 2)
	for i := 0; i < numGoroutines/2; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < numIterations; j++ {
				registry.Invalidate("nonexistent-model")
			}
		}(i)
	}

	wg.Wait()

	// If we get here without panic or data race, the test passes
}

func TestModelRegistry_OverrideUnknownModel(t *testing.T) {
	// Create registry with override for a model not in built-in
	overrides := map[string]ModelMetadata{
		"custom-model": {
			ContextWindow: 50000,
			OutputLimit:   2000,
			TokenizerType: "custom",
		},
	}

	registry := NewModelRegistry(overrides)

	meta, _ := registry.Resolve(context.Background(), "custom-model")

	if meta.ContextWindow != 50000 {
		t.Errorf("expected ContextWindow 50000, got %d", meta.ContextWindow)
	}
	if meta.OutputLimit != 2000 {
		t.Errorf("expected OutputLimit 2000, got %d", meta.OutputLimit)
	}
	if meta.TokenizerType != "custom" {
		t.Errorf("expected TokenizerType 'custom', got %s", meta.TokenizerType)
	}
}

func TestModelRegistry_NilOverrides(t *testing.T) {
	// Test that nil overrides doesn't cause panic
	registry := NewModelRegistry(nil)

	meta, _ := registry.Resolve(context.Background(), "gpt-4o")

	if meta.ContextWindow != 128000 {
		t.Errorf("expected ContextWindow 128000, got %d", meta.ContextWindow)
	}
}

func TestModelRegistry_EmptyOverrides(t *testing.T) {
	// Test that empty overrides map works correctly
	registry := NewModelRegistry(map[string]ModelMetadata{})

	meta, _ := registry.Resolve(context.Background(), "gpt-4o")

	if meta.ContextWindow != 128000 {
		t.Errorf("expected ContextWindow 128000, got %d", meta.ContextWindow)
	}
}

func TestModelRegistry_RegisteredSource(t *testing.T) {
	// Create registry with no overrides and no built-in match for test model
	registry := NewModelRegistry(nil)

	// Register a source that returns known metadata for a test model
	testModel := "test-source-model-v1"
	expectedMeta := ModelMetadata{
		ContextWindow: 65536,
		OutputLimit:   2048,
		TokenizerType: "test-tokenizer",
	}

	registry.RegisterSource(func(model string) (ModelMetadata, bool) {
		if model == testModel {
			return expectedMeta, true
		}
		return ModelMetadata{}, false
	})

	// Resolve should use the registered source
	meta, ok := registry.Resolve(context.Background(), testModel)
	if !ok {
		t.Fatal("expected ok=true for registered source model")
	}

	if meta.ContextWindow != expectedMeta.ContextWindow {
		t.Errorf("expected ContextWindow %d, got %d", expectedMeta.ContextWindow, meta.ContextWindow)
	}
	if meta.OutputLimit != expectedMeta.OutputLimit {
		t.Errorf("expected OutputLimit %d, got %d", expectedMeta.OutputLimit, meta.OutputLimit)
	}
	if meta.TokenizerType != expectedMeta.TokenizerType {
		t.Errorf("expected TokenizerType %q, got %q", expectedMeta.TokenizerType, meta.TokenizerType)
	}
}

// TestResolveFamily_BuiltinModels verifies that known built-in models get the correct family.
func TestResolveFamily_BuiltinModels(t *testing.T) {
	registry := NewModelRegistry(nil)

	tests := []struct {
		model          string
		expectedFamily string
	}{
		// OpenAI flagship models
		{"gpt-5.4", "openai_flagship"},
		{"gpt-5.4-mini", "openai_flagship"},
		{"gpt-5", "openai_flagship"},
		{"gpt-4o", "openai_flagship"},
		{"gpt-4o-mini", "openai_flagship"},
		{"o4-mini", "openai_flagship"},
		{"o3", "openai_flagship"},
		{"o3-mini", "openai_flagship"},
		{"o1", "openai_flagship"},
		{"o1-mini", "openai_flagship"},

		// OpenAI standard models
		{"gpt-4.1", "openai_standard"},

		// OpenAI Codex models
		{"codex-mini-latest", "openai_codex"},

		// Anthropic models
		{"claude-opus-4.6", "anthropic"},
		{"claude-sonnet-4.5", "anthropic"},
		{"claude-haiku-4.5", "anthropic"},
		{"claude-3.5-sonnet", "anthropic"},
		{"claude-3.5-haiku", "anthropic"},

		// Gemini models
		{"gemini-3.1-pro", "google"},
		{"gemini-3.1-flash-lite", "google"},
		{"gemini-2.5-pro", "google"},
		{"gemini-2.5-flash", "google"},
		{"gemini-2.0-flash", "google"},

		// DeepSeek V4 models
		{"deepseek-v4-pro", "deepseek"},
		{"deepseek-v4-flash", "deepseek"},

		// Grok models → default family
		{"grok-4.20", "default"},
		{"grok-4", "default"},
		{"grok-3", "default"},
		{"grok-3-mini", "default"},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			meta, _ := registry.Resolve(context.Background(), tt.model)
			if meta.Family != tt.expectedFamily {
				t.Errorf("expected Family %q, got %q", tt.expectedFamily, meta.Family)
			}
		})
	}
}

// TestResolveFamily_PatternMatching verifies DetectFamily-based detection for unknown model IDs.
func TestResolveFamily_PatternMatching(t *testing.T) {
	registry := NewModelRegistry(nil)

	tests := []struct {
		model          string
		expectedFamily string
	}{
		// OpenAI flagship patterns
		{"gpt-4-turbo-custom", "openai_flagship"},
		{"gpt-5-preview", "openai_flagship"},
		{"o1-preview-custom", "openai_flagship"},
		{"o3-mini-custom", "openai_flagship"},
		{"o4-model", "openai_flagship"},

		// OpenAI standard patterns
		{"gpt-4.1-turbo", "openai_standard"},

		// Anthropic patterns
		{"claude-custom-model", "anthropic"},

		// Gemini patterns
		{"gemini-custom-pro", "google"},

		// DeepSeek patterns
		{"deepseek-v3-custom", "deepseek"},
		{"deepseek-reasoner-v2", "deepseek"},

		// Mistral patterns
		{"mistral-small-latest", "mistral"},
		{"mistral-7b-instruct", "mistral"},
		{"devstral-custom", "mistral"},
		{"codestral-latest", "mistral"},

		// Kimi patterns
		{"kimi-k2", "kimi"},

		// Qwen patterns
		{"qwen-2.5-72b", "qwen"},
		{"qwq-plus", "qwen"},

		// GLM patterns
		{"glm-z1-32b", "glm"},

		// Default family (no specific pattern)
		{"grok-custom-model", "default"},
		{"llama-3.1-70b", "default"},
		{"phi-3-mini", "default"},
		{"codellama-34b", "default"},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			// These are unknown models resolved via DetectFamily
			meta, _ := registry.Resolve(context.Background(), tt.model)
			if meta.Family != tt.expectedFamily {
				t.Errorf("expected Family %q for model %q, got %q", tt.expectedFamily, tt.model, meta.Family)
			}
		})
	}
}

// TestResolveFamily_SourceWithoutFamily verifies that when a source returns metadata
// without Family set, resolveFamily delegates to DetectFamily.
func TestResolveFamily_SourceWithoutFamily(t *testing.T) {
	tests := []struct {
		name           string
		model          string
		expectedFamily string
	}{
		{
			name:           "claude model from source gets anthropic family",
			model:          "claude-custom-v2",
			expectedFamily: "anthropic",
		},
		{
			name:           "gemini model from source gets google family",
			model:          "gemini-custom-pro",
			expectedFamily: "google",
		},
		{
			name:           "unknown model from source gets default family",
			model:          "custom-llm-v1",
			expectedFamily: "default",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := NewModelRegistry(nil)

			// Register a source that returns metadata without Family set
			registry.RegisterSource(func(model string) (ModelMetadata, bool) {
				if model == tt.model {
					return ModelMetadata{
						ContextWindow: 128000,
						OutputLimit:   8192,
						TokenizerType: "test",
					}, true
				}
				return ModelMetadata{}, false
			})

			meta, _ := registry.Resolve(context.Background(), tt.model)
			if meta.Family != tt.expectedFamily {
				t.Errorf("expected Family %q, got %q", tt.expectedFamily, meta.Family)
			}
		})
	}
}

// TestResolveFamily_UserOverride verifies that user override Family takes precedence.
func TestResolveFamily_UserOverride(t *testing.T) {
	tests := []struct {
		name           string
		model          string
		overrideFamily string
		expectedFamily string
	}{
		{
			name:           "override with explicit anthropic family",
			model:          "custom-model",
			overrideFamily: "anthropic",
			expectedFamily: "anthropic",
		},
		{
			name:           "override with explicit google family",
			model:          "custom-model",
			overrideFamily: "google",
			expectedFamily: "google",
		},
		{
			name:           "override without family should get DetectFamily result",
			model:          "claude-custom", // matches anthropic pattern
			overrideFamily: "",
			expectedFamily: "anthropic",
		},
		{
			name:           "override builtin model with different family",
			model:          "gpt-4o",    // normally openai_flagship
			overrideFamily: "anthropic", // overridden
			expectedFamily: "anthropic",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			overrideMeta := ModelMetadata{
				ContextWindow: 100000,
				OutputLimit:   5000,
				TokenizerType: "override",
			}
			if tt.overrideFamily != "" {
				overrideMeta.Family = tt.overrideFamily
			}

			registry := NewModelRegistry(map[string]ModelMetadata{
				tt.model: overrideMeta,
			})

			meta, _ := registry.Resolve(context.Background(), tt.model)
			if meta.Family != tt.expectedFamily {
				t.Errorf("expected Family %q, got %q", tt.expectedFamily, meta.Family)
			}
		})
	}
}

// TestResolveFamily_EmptyModelID verifies that Resolve("") returns "default" family.
// This is important because core components call Resolve("") when model ID
// isn't threaded through, and we want predictable fallback behavior.
func TestResolveFamily_EmptyModelID(t *testing.T) {
	reg := NewModelRegistry(nil)
	meta, _ := reg.Resolve(context.Background(), "")
	if meta.Family != "default" {
		t.Errorf("expected Family 'default' for empty model ID, got %q", meta.Family)
	}
}

func TestModelRegistry_SourcePriority(t *testing.T) {
	// Create registry with both a source and an override for the same model
	testModel := "priority-test-model"

	// Source returns these values
	sourceMeta := ModelMetadata{
		ContextWindow: 50000,
		OutputLimit:   2000,
		TokenizerType: "source-tokenizer",
	}

	// Override has different values (should win)
	overrideMeta := ModelMetadata{
		ContextWindow: 99999,
		OutputLimit:   9999,
		TokenizerType: "override-tokenizer",
	}

	registry := NewModelRegistry(map[string]ModelMetadata{
		testModel: overrideMeta,
	})

	registry.RegisterSource(func(model string) (ModelMetadata, bool) {
		if model == testModel {
			return sourceMeta, true
		}
		return ModelMetadata{}, false
	})

	// Override (tier 1) should take priority over source (tier 4)
	meta, _ := registry.Resolve(context.Background(), testModel)

	if meta.ContextWindow != overrideMeta.ContextWindow {
		t.Errorf("expected override ContextWindow %d, got %d", overrideMeta.ContextWindow, meta.ContextWindow)
	}
	if meta.OutputLimit != overrideMeta.OutputLimit {
		t.Errorf("expected override OutputLimit %d, got %d", overrideMeta.OutputLimit, meta.OutputLimit)
	}
	if meta.TokenizerType != overrideMeta.TokenizerType {
		t.Errorf("expected override TokenizerType %q, got %q", overrideMeta.TokenizerType, meta.TokenizerType)
	}
}

func TestModelRegistry_SourceFallback(t *testing.T) {
	// Register a source that returns false for a model
	registry := NewModelRegistry(nil)

	testModel := "fallback-test-model"

	registry.RegisterSource(func(model string) (ModelMetadata, bool) {
		// Source doesn't know about this model
		return ModelMetadata{}, false
	})

	// Resolve should use fallback defaults
	meta, ok := registry.Resolve(context.Background(), testModel)
	if ok {
		t.Fatal("expected ok=false when source returns false")
	}

	// Fallback defaults: ContextWindow: 128000, OutputLimit: 4096, TokenizerType: "approximate"
	if meta.ContextWindow != 128000 {
		t.Errorf("expected fallback ContextWindow 128000, got %d", meta.ContextWindow)
	}
	if meta.OutputLimit != 4096 {
		t.Errorf("expected fallback OutputLimit 4096, got %d", meta.OutputLimit)
	}
	if meta.TokenizerType != "approximate" {
		t.Errorf("expected fallback TokenizerType %q, got %q", "approximate", meta.TokenizerType)
	}
}

func TestBuiltInModelNames_AllModels(t *testing.T) {
	names := BuiltInModelNames("")
	if len(names) == 0 {
		t.Fatal("expected non-empty model names list")
	}
	// Should be sorted
	for i := 1; i < len(names); i++ {
		if names[i] < names[i-1] {
			t.Errorf("names not sorted: %q comes after %q", names[i], names[i-1])
		}
	}
}

func TestBuiltInModelNames_ByTokenizer(t *testing.T) {
	tests := []struct {
		tokenizer string
		wantMin   int
	}{
		{"tiktoken/o200k_base", 5},
		{"anthropic-api", 3},
		{"approximate", 5},
		{"nonexistent", 0},
	}

	for _, tt := range tests {
		t.Run(tt.tokenizer, func(t *testing.T) {
			names := BuiltInModelNames(tt.tokenizer)
			if len(names) < tt.wantMin {
				t.Errorf("expected at least %d models for tokenizer %q, got %d", tt.wantMin, tt.tokenizer, len(names))
			}
			// Verify all returned models actually have the correct tokenizer
			registry := makeBuiltInRegistry()
			for _, name := range names {
				if meta, ok := registry[name]; ok {
					if meta.TokenizerType != tt.tokenizer {
						t.Errorf("model %q has tokenizer %q, expected %q", name, meta.TokenizerType, tt.tokenizer)
					}
				}
			}
		})
	}
}

func TestModelRegistry_FetchFromHuggingFace(t *testing.T) {
	// Test successful fetch
	t.Run("success", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"max_position_embeddings": 8192}`))
		}))
		defer server.Close()

		registry := NewModelRegistry(nil)
		registry.httpClient = server.Client()
		// Override the URL by adjusting the httpClient transport
		registry.httpClient = &http.Client{
			Transport: &rewriteTransport{base: http.DefaultTransport, serverURL: server.URL},
		}

		meta, err := registry.fetchFromHuggingFace(context.Background(), "test-model")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if meta.ContextWindow != 8192 {
			t.Errorf("expected ContextWindow 8192, got %d", meta.ContextWindow)
		}
		if meta.OutputLimit != 4096 {
			t.Errorf("expected OutputLimit 4096, got %d", meta.OutputLimit)
		}
		if meta.TokenizerType != "approximate" {
			t.Errorf("expected TokenizerType 'approximate', got %q", meta.TokenizerType)
		}
	})

	// Test HTTP error
	t.Run("http_error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()

		registry := NewModelRegistry(nil)
		registry.httpClient = &http.Client{
			Transport: &rewriteTransport{base: http.DefaultTransport, serverURL: server.URL},
		}

		_, err := registry.fetchFromHuggingFace(context.Background(), "test-model")
		if err == nil {
			t.Error("expected error for 404 response")
		}
	})

	// Test invalid JSON
	t.Run("invalid_json", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`not json`))
		}))
		defer server.Close()

		registry := NewModelRegistry(nil)
		registry.httpClient = &http.Client{
			Transport: &rewriteTransport{base: http.DefaultTransport, serverURL: server.URL},
		}

		_, err := registry.fetchFromHuggingFace(context.Background(), "test-model")
		if err == nil {
			t.Error("expected error for invalid JSON")
		}
	})

	// Test zero max_position_embeddings
	t.Run("zero_embeddings", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"max_position_embeddings": 0}`))
		}))
		defer server.Close()

		registry := NewModelRegistry(nil)
		registry.httpClient = &http.Client{
			Transport: &rewriteTransport{base: http.DefaultTransport, serverURL: server.URL},
		}

		_, err := registry.fetchFromHuggingFace(context.Background(), "test-model")
		if err == nil {
			t.Error("expected error for zero max_position_embeddings")
		}
	})
}

// rewriteTransport rewrites the request URL to the test server.
type rewriteTransport struct {
	base      http.RoundTripper
	serverURL string
}

func (t *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme = "http"
	// Parse the server URL to extract host
	parsed, _ := url.Parse(t.serverURL)
	req.URL.Host = parsed.Host
	return t.base.RoundTrip(req)
}

func TestModelRegistry_CacheAfterFetchFromHuggingFace(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"max_position_embeddings": 4096}`))
	}))
	defer server.Close()

	registry := NewModelRegistry(nil)
	registry.httpClient = &http.Client{
		Transport: &rewriteTransport{base: http.DefaultTransport, serverURL: server.URL},
	}

	// First resolve should fetch from HuggingFace
	meta, _ := registry.Resolve(context.Background(), "hf-test-model")
	if meta.ContextWindow != 4096 {
		t.Errorf("expected ContextWindow 4096, got %d", meta.ContextWindow)
	}

	// Second resolve should use cache (no additional HTTP call)
	meta2, _ := registry.Resolve(context.Background(), "hf-test-model")
	if meta2.ContextWindow != 4096 {
		t.Errorf("expected cached ContextWindow 4096, got %d", meta2.ContextWindow)
	}

	if callCount != 1 {
		t.Errorf("expected 1 HTTP call (cached on second), got %d", callCount)
	}
}

func TestModelRegistry_MultipleSources(t *testing.T) {
	// Register two sources: first returns false, second returns metadata
	registry := NewModelRegistry(nil)

	testModel := "multi-source-model"
	expectedMeta := ModelMetadata{
		ContextWindow: 32768,
		OutputLimit:   1024,
		TokenizerType: "second-source-tokenizer",
	}

	// First source doesn't know the model
	registry.RegisterSource(func(model string) (ModelMetadata, bool) {
		return ModelMetadata{}, false
	})

	// Second source knows the model
	registry.RegisterSource(func(model string) (ModelMetadata, bool) {
		if model == testModel {
			return expectedMeta, true
		}
		return ModelMetadata{}, false
	})

	// Resolve should use the second source's metadata
	meta, _ := registry.Resolve(context.Background(), testModel)

	if meta.ContextWindow != expectedMeta.ContextWindow {
		t.Errorf("expected ContextWindow %d, got %d", expectedMeta.ContextWindow, meta.ContextWindow)
	}
	if meta.OutputLimit != expectedMeta.OutputLimit {
		t.Errorf("expected OutputLimit %d, got %d", expectedMeta.OutputLimit, meta.OutputLimit)
	}
	if meta.TokenizerType != expectedMeta.TokenizerType {
		t.Errorf("expected TokenizerType %q, got %q", expectedMeta.TokenizerType, meta.TokenizerType)
	}
}
