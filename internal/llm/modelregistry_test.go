package llm

import (
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
	meta := registry.Resolve("gpt-4o")
	
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
		model                string
		expectedContextWindow int
		expectedOutputLimit  int
		expectedTokenizer    string
	}{
		// OpenAI models
		{"gpt-5.4", 1050000, 32768, "tiktoken/o200k_base"},
		{"gpt-4o", 128000, 16384, "tiktoken/o200k_base"},
		{"o3-mini", 200000, 100000, "tiktoken/o200k_base"},
		
		// Anthropic models
		{"claude-opus-4.6", 1000000, 32768, "anthropic-api"},
		{"claude-3.5-sonnet", 200000, 8192, "anthropic-api"},
		
		// Gemini models
		{"gemini-2.5-pro", 1048576, 65536, "approximate"},
		{"gemini-2.0-flash", 1048576, 8192, "approximate"},
		
		// DeepSeek models
		{"deepseek-chat", 128000, 8192, "approximate"},
		{"deepseek-reasoner", 128000, 8192, "approximate"},
		
		// Grok models
		{"grok-4.20", 2000000, 32768, "approximate"},
		{"grok-3-mini", 131072, 32768, "approximate"},
	}
	
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			meta := registry.Resolve(tt.model)
			
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
	meta := registry.Resolve("unknown-model-v123")
	
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
				_ = registry.Resolve("gpt-4o")
				_ = registry.Resolve("claude-opus-4.6")
				_ = registry.Resolve("unknown-model")
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
	
	meta := registry.Resolve("custom-model")
	
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
	
	meta := registry.Resolve("gpt-4o")
	
	if meta.ContextWindow != 128000 {
		t.Errorf("expected ContextWindow 128000, got %d", meta.ContextWindow)
	}
}

func TestModelRegistry_EmptyOverrides(t *testing.T) {
	// Test that empty overrides map works correctly
	registry := NewModelRegistry(map[string]ModelMetadata{})
	
	meta := registry.Resolve("gpt-4o")
	
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
	meta := registry.Resolve(testModel)
	
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
	meta := registry.Resolve(testModel)
	
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
	meta := registry.Resolve(testModel)
	
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
	meta := registry.Resolve(testModel)
	
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
