package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// ModelMetadata holds the capabilities and configuration for a language model.
type ModelMetadata struct {
	ContextWindow int    // max input tokens (e.g., 200000)
	OutputLimit   int    // max output tokens (e.g., 8192)
	TokenizerType string // "tiktoken/o200k_base", "tiktoken/cl100k_base", "anthropic-api", "approximate", etc.
	Tier          string // "large" or "small" — model capability tier
}

// ModelMetadataSource is a function that can resolve model metadata from an external source.
// Returns metadata and true if found, or zero value and false if not found.
type ModelMetadataSource func(model string) (ModelMetadata, bool)

// ModelRegistry provides a 5-tier resolution system for model metadata.
type ModelRegistry struct {
	builtIn    map[string]ModelMetadata
	overrides  map[string]ModelMetadata
	cache      map[string]ModelMetadata
	sources    []ModelMetadataSource // external metadata sources (e.g., LM Studio)
	mu         sync.RWMutex
	httpClient *http.Client
}

// NewModelRegistry creates a new registry with built-in data and optional user overrides.
func NewModelRegistry(overrides map[string]ModelMetadata) *ModelRegistry {
	registry := &ModelRegistry{
		builtIn:    makeBuiltInRegistry(),
		overrides:  overrides,
		cache:      make(map[string]ModelMetadata),
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}

	if registry.overrides == nil {
		registry.overrides = make(map[string]ModelMetadata)
	}

	return registry
}

// Resolve returns model metadata using 5-tier lookup:
// 1. User overrides (from config)
// 2. Built-in registry (hardcoded table)
// 3. HuggingFace API lookup (lazy, cached)
// 4. Registered sources (e.g., LM Studio provider)
// 5. Fallback defaults (ok=false)
//
// The second return value indicates whether the model was found in a known source.
// When ok is false, the returned metadata contains usable fallback defaults.
func (r *ModelRegistry) Resolve(model string) (ModelMetadata, bool) {
	// Priority 1: Check overrides (no lock needed for read-only map after construction)
	if meta, ok := r.overrides[model]; ok {
		meta.Tier = resolveTier(model, meta)
		return meta, true
	}

	// Priority 2: Check built-in registry (no lock needed for read-only map)
	if meta, ok := r.builtIn[model]; ok {
		meta.Tier = resolveTier(model, meta)
		return meta, true
	}

	// Priority 3: Check cache (needs lock)
	r.mu.RLock()
	if meta, ok := r.cache[model]; ok {
		r.mu.RUnlock()
		meta.Tier = resolveTier(model, meta)
		return meta, true
	}
	r.mu.RUnlock()

	// Priority 3: Fetch from HuggingFace
	meta, err := r.fetchFromHuggingFace(model)
	if err == nil {
		meta.Tier = resolveTier(model, meta)
		r.mu.Lock()
		r.cache[model] = meta
		r.mu.Unlock()
		return meta, true
	}

	// Priority 4: Try registered sources
	// Copy sources slice under read lock, then call sources without lock
	// (sources may do HTTP calls, so we don't want to hold the lock)
	r.mu.RLock()
	sources := make([]ModelMetadataSource, len(r.sources))
	copy(sources, r.sources)
	r.mu.RUnlock()

	for _, src := range sources {
		m, ok := src(model)
		if !ok {
			continue
		}
		m.Tier = resolveTier(model, m)
		r.mu.Lock()
		r.cache[model] = m
		r.mu.Unlock()
		return m, true
	}

	// Priority 5: Fallback to defaults
	meta = ModelMetadata{
		ContextWindow: 128000,
		OutputLimit:   4096,
		TokenizerType: "approximate",
	}
	meta.Tier = resolveTier(model, meta)
	return meta, false
}

// Invalidate removes an entry from the cache map (for model change mid-session).
func (r *ModelRegistry) Invalidate(model string) {
	r.mu.Lock()
	delete(r.cache, model)
	r.mu.Unlock()
}

// resolveTier determines the tier for a model based on pattern matching and heuristics.
// This is used when tier is not set by user override or builtin registry.
func resolveTier(modelID string, meta ModelMetadata) string {
	// Already set (e.g., from builtin or user override)
	if meta.Tier != "" {
		return meta.Tier
	}

	id := strings.ToLower(modelID)

	// No model ID provided — default to large tier for safety.
	// Core components call Resolve("") when model ID isn't threaded through;
	// defaulting to large preserves rich prompt behavior.
	if id == "" {
		return "large"
	}

	// Known large model patterns
	largePatterns := []string{"gpt-4", "gpt-5", "o1-", "o3-", "o4-", "claude-", "gemini-", "deepseek-v3", "deepseek-reasoner", "grok-", "command-r-plus"}
	for _, p := range largePatterns {
		if strings.Contains(id, p) {
			return "large"
		}
	}

	// Known small model patterns
	smallPatterns := []string{"llama", "qwen", "phi-", "gemma", "mistral-small", "mistral-7b", "codellama"}
	for _, p := range smallPatterns {
		if strings.Contains(id, p) {
			return "small"
		}
	}

	// Heuristic fallback based on capabilities
	if meta.ContextWindow >= 128000 && meta.OutputLimit >= 8192 {
		return "large"
	}
	return "small"
}

// RegisterSource adds a metadata source to the registry.
// Sources are called in order during resolution after HuggingFace lookup fails.
func (r *ModelRegistry) RegisterSource(src ModelMetadataSource) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sources = append(r.sources, src)
}

// fetchFromHuggingFace queries HuggingFace API for model config.
// HTTP GET to https://huggingface.co/{model}/resolve/main/config.json
// with redirect following. Parses JSON for max_position_embeddings.
func (r *ModelRegistry) fetchFromHuggingFace(model string) (ModelMetadata, error) {
	url := fmt.Sprintf("https://huggingface.co/%s/resolve/main/config.json", model)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, http.NoBody)
	if err != nil {
		return ModelMetadata{}, fmt.Errorf("failed to create request: %w", err)
	}

	// Follow redirects automatically (http.Client default behavior)
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return ModelMetadata{}, fmt.Errorf("http request failed: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return ModelMetadata{}, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ModelMetadata{}, fmt.Errorf("failed to read response body: %w", err)
	}

	// Parse config.json for max_position_embeddings
	var config struct {
		MaxPositionEmbeddings int `json:"max_position_embeddings"`
	}

	if err := json.Unmarshal(body, &config); err != nil {
		return ModelMetadata{}, fmt.Errorf("failed to parse config.json: %w", err)
	}

	if config.MaxPositionEmbeddings == 0 {
		return ModelMetadata{}, errors.New("max_position_embeddings not found in config")
	}

	return ModelMetadata{
		ContextWindow: config.MaxPositionEmbeddings,
		OutputLimit:   4096,
		TokenizerType: "approximate",
	}, nil
}

// makeBuiltInRegistry creates the hardcoded model metadata table.
func makeBuiltInRegistry() map[string]ModelMetadata {
	return map[string]ModelMetadata{
		// OpenAI models
		"gpt-5.4": {
			ContextWindow: 1050000,
			OutputLimit:   32768,
			TokenizerType: "tiktoken/o200k_base",
			Tier:          "large",
		},
		"gpt-5.4-mini": {
			ContextWindow: 400000,
			OutputLimit:   16384,
			TokenizerType: "tiktoken/o200k_base",
			Tier:          "large",
		},
		"gpt-5.4-nano": {
			ContextWindow: 400000,
			OutputLimit:   16384,
			TokenizerType: "tiktoken/o200k_base",
			Tier:          "large",
		},
		"gpt-5": {
			ContextWindow: 400000,
			OutputLimit:   32768,
			TokenizerType: "tiktoken/o200k_base",
			Tier:          "large",
		},
		"gpt-4.1": {
			ContextWindow: 1047576,
			OutputLimit:   32768,
			TokenizerType: "tiktoken/o200k_base",
			Tier:          "large",
		},
		"gpt-4.1-mini": {
			ContextWindow: 1047576,
			OutputLimit:   32768,
			TokenizerType: "tiktoken/o200k_base",
			Tier:          "large",
		},
		"gpt-4.1-nano": {
			ContextWindow: 1047576,
			OutputLimit:   32768,
			TokenizerType: "tiktoken/o200k_base",
			Tier:          "large",
		},
		"o4-mini": {
			ContextWindow: 200000,
			OutputLimit:   100000,
			TokenizerType: "tiktoken/o200k_base",
			Tier:          "large",
		},
		"o3": {
			ContextWindow: 200000,
			OutputLimit:   100000,
			TokenizerType: "tiktoken/o200k_base",
			Tier:          "large",
		},
		"o3-mini": {
			ContextWindow: 200000,
			OutputLimit:   100000,
			TokenizerType: "tiktoken/o200k_base",
			Tier:          "large",
		},
		"o1": {
			ContextWindow: 200000,
			OutputLimit:   100000,
			TokenizerType: "tiktoken/o200k_base",
			Tier:          "large",
		},
		"o1-mini": {
			ContextWindow: 128000,
			OutputLimit:   65536,
			TokenizerType: "tiktoken/o200k_base",
			Tier:          "large",
		},
		"gpt-4o": {
			ContextWindow: 128000,
			OutputLimit:   16384,
			TokenizerType: "tiktoken/o200k_base",
			Tier:          "large",
		},
		"gpt-4o-mini": {
			ContextWindow: 128000,
			OutputLimit:   16384,
			TokenizerType: "tiktoken/o200k_base",
			Tier:          "large",
		},

		// Anthropic models
		"claude-opus-4.6": {
			ContextWindow: 1000000,
			OutputLimit:   32768,
			TokenizerType: "anthropic-api",
			Tier:          "large",
		},
		"claude-sonnet-4.6": {
			ContextWindow: 1000000,
			OutputLimit:   32768,
			TokenizerType: "anthropic-api",
			Tier:          "large",
		},
		"claude-haiku-4.5": {
			ContextWindow: 200000,
			OutputLimit:   8192,
			TokenizerType: "anthropic-api",
			Tier:          "large",
		},
		"claude-sonnet-4.5": {
			ContextWindow: 200000,
			OutputLimit:   16384,
			TokenizerType: "anthropic-api",
			Tier:          "large",
		},
		"claude-opus-4.5": {
			ContextWindow: 200000,
			OutputLimit:   32768,
			TokenizerType: "anthropic-api",
			Tier:          "large",
		},
		"claude-sonnet-4": {
			ContextWindow: 200000,
			OutputLimit:   16384,
			TokenizerType: "anthropic-api",
			Tier:          "large",
		},
		"claude-opus-4": {
			ContextWindow: 200000,
			OutputLimit:   32768,
			TokenizerType: "anthropic-api",
			Tier:          "large",
		},
		"claude-3.5-sonnet": {
			ContextWindow: 200000,
			OutputLimit:   8192,
			TokenizerType: "anthropic-api",
			Tier:          "large",
		},
		"claude-3.5-haiku": {
			ContextWindow: 200000,
			OutputLimit:   8192,
			TokenizerType: "anthropic-api",
			Tier:          "large",
		},

		// Google Gemini models
		"gemini-3.1-pro": {
			ContextWindow: 1048576,
			OutputLimit:   65536,
			TokenizerType: "approximate",
			Tier:          "large",
		},
		"gemini-3.1-flash-lite": {
			ContextWindow: 1048576,
			OutputLimit:   65536,
			TokenizerType: "approximate",
			Tier:          "large",
		},
		"gemini-3-flash": {
			ContextWindow: 1048576,
			OutputLimit:   65536,
			TokenizerType: "approximate",
			Tier:          "large",
		},
		"gemini-2.5-pro": {
			ContextWindow: 1048576,
			OutputLimit:   65536,
			TokenizerType: "approximate",
			Tier:          "large",
		},
		"gemini-2.5-flash": {
			ContextWindow: 1048576,
			OutputLimit:   65536,
			TokenizerType: "approximate",
			Tier:          "large",
		},
		"gemini-2.5-flash-lite": {
			ContextWindow: 1048576,
			OutputLimit:   65536,
			TokenizerType: "approximate",
			Tier:          "large",
		},
		"gemini-2.0-flash": {
			ContextWindow: 1048576,
			OutputLimit:   8192,
			TokenizerType: "approximate",
			Tier:          "large",
		},

		// DeepSeek models
		"deepseek-chat": {
			ContextWindow: 128000,
			OutputLimit:   8192,
			TokenizerType: "approximate",
			Tier:          "large",
		},
		"deepseek-reasoner": {
			ContextWindow: 128000,
			OutputLimit:   8192,
			TokenizerType: "approximate",
			Tier:          "large",
		},

		// xAI Grok models
		"grok-4.20": {
			ContextWindow: 2000000,
			OutputLimit:   32768,
			TokenizerType: "approximate",
			Tier:          "large",
		},
		"grok-4.1-fast": {
			ContextWindow: 2000000,
			OutputLimit:   32768,
			TokenizerType: "approximate",
			Tier:          "large",
		},
		"grok-4": {
			ContextWindow: 256000,
			OutputLimit:   32768,
			TokenizerType: "approximate",
			Tier:          "large",
		},
		"grok-3": {
			ContextWindow: 131072,
			OutputLimit:   32768,
			TokenizerType: "approximate",
			Tier:          "large",
		},
		"grok-3-mini": {
			ContextWindow: 131072,
			OutputLimit:   32768,
			TokenizerType: "approximate",
			Tier:          "large",
		},
	}
}

// BuiltInModelNames returns model names from the built-in registry filtered by tokenizer type.
// If tokenizerType is empty, returns all model names.
func BuiltInModelNames(tokenizerType string) []string {
	registry := makeBuiltInRegistry()
	names := []string{}
	for name, meta := range registry {
		if tokenizerType == "" || meta.TokenizerType == tokenizerType {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// BuiltInModelNamesByPrefix returns model names that start with the given prefix.
func BuiltInModelNamesByPrefix(prefix string) []string {
	registry := makeBuiltInRegistry()
	names := []string{}
	for name := range registry {
		if strings.HasPrefix(name, prefix) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}
