package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"
)

// ModelCapabilities describes what a model supports.
type ModelCapabilities struct {
	Attachment  bool   // image/PDF support
	Reasoning   bool   // reasoning/thinking mode
	Temperature bool   // accepts temperature parameter
	ToolCall    bool   // function calling support
	Interleaved string // "reasoning_content" | "reasoning_details" | ""
}

// ModelMetadata holds the capabilities and configuration for a language model.
type ModelMetadata struct {
	ContextWindow       int
	OutputLimit         int
	TokenizerType       string
	Family              string
	Capabilities        ModelCapabilities
	InputCostPer1M      float64
	OutputCostPer1M     float64
	CacheReadCostPer1M  float64
	CacheWriteCostPer1M float64
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
//
// The overrides map is defensively copied at construction time so that callers
// (e.g. config reloads) can mutate their own map without racing the registry's
// concurrent readers.
func NewModelRegistry(overrides map[string]ModelMetadata) *ModelRegistry {
	copied := make(map[string]ModelMetadata, len(overrides))
	for k, v := range overrides {
		copied[k] = v
	}
	return &ModelRegistry{
		builtIn:    getBuiltInRegistry(),
		overrides:  copied,
		cache:      make(map[string]ModelMetadata),
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// SetHTTPClient replaces the HTTP client used for metadata lookups (e.g., HuggingFace).
func (r *ModelRegistry) SetHTTPClient(client *http.Client) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if client != nil {
		r.httpClient = client
	}
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
func (r *ModelRegistry) Resolve(ctx context.Context, model string) (ModelMetadata, bool) {
	// Priority 1: Check overrides (no lock needed for read-only map after construction)
	if meta, ok := r.overrides[model]; ok {
		meta.Family = resolveFamily(model, meta)
		return meta, true
	}

	// Priority 2: Check built-in registry (no lock needed for read-only map)
	if meta, ok := r.builtIn[model]; ok {
		meta.Family = resolveFamily(model, meta)
		return meta, true
	}

	// Priority 3: Check cache (needs lock)
	r.mu.RLock()
	if meta, ok := r.cache[model]; ok {
		r.mu.RUnlock()
		meta.Family = resolveFamily(model, meta)
		return meta, true
	}
	r.mu.RUnlock()

	// Priority 3: Fetch from HuggingFace
	meta, err := r.fetchFromHuggingFace(ctx, model)
	if err == nil {
		meta.Family = resolveFamily(model, meta)
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
		m.Family = resolveFamily(model, m)
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
	meta.Family = resolveFamily(model, meta)
	return meta, false
}

// Invalidate removes an entry from the cache map (for model change mid-session).
func (r *ModelRegistry) Invalidate(model string) {
	r.mu.Lock()
	delete(r.cache, model)
	r.mu.Unlock()
}

// resolveFamily determines the family for a model.
// If already set in metadata, returns it directly; otherwise delegates to DetectFamily.
func resolveFamily(modelID string, meta ModelMetadata) string {
	if meta.Family != "" {
		return meta.Family
	}
	return string(DetectFamily(modelID))
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
func (r *ModelRegistry) fetchFromHuggingFace(ctx context.Context, model string) (ModelMetadata, error) {
	url := fmt.Sprintf("https://huggingface.co/%s/resolve/main/config.json", model)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
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
			Family:        "openai_flagship",
			Capabilities:  ModelCapabilities{Attachment: true, Temperature: true, ToolCall: true},
		},
		"gpt-5.4-mini": {
			ContextWindow: 400000,
			OutputLimit:   16384,
			TokenizerType: "tiktoken/o200k_base",
			Family:        "openai_flagship",
			Capabilities:  ModelCapabilities{Attachment: true, Temperature: true, ToolCall: true},
		},
		"gpt-5.4-nano": {
			ContextWindow: 400000,
			OutputLimit:   16384,
			TokenizerType: "tiktoken/o200k_base",
			Family:        "openai_flagship",
			Capabilities:  ModelCapabilities{Temperature: true, ToolCall: true},
		},
		"gpt-5": {
			ContextWindow: 400000,
			OutputLimit:   32768,
			TokenizerType: "tiktoken/o200k_base",
			Family:        "openai_flagship",
			Capabilities:  ModelCapabilities{Attachment: true, Temperature: true, ToolCall: true},
		},
		"gpt-4.1": {
			ContextWindow: 1047576,
			OutputLimit:   32768,
			TokenizerType: "tiktoken/o200k_base",
			Family:        "openai_standard",
			Capabilities:  ModelCapabilities{Attachment: true, Temperature: true, ToolCall: true},
		},
		"gpt-4.1-mini": {
			ContextWindow: 1047576,
			OutputLimit:   32768,
			TokenizerType: "tiktoken/o200k_base",
			Family:        "openai_standard",
			Capabilities:  ModelCapabilities{Attachment: true, Temperature: true, ToolCall: true},
		},
		"gpt-4.1-nano": {
			ContextWindow: 1047576,
			OutputLimit:   32768,
			TokenizerType: "tiktoken/o200k_base",
			Family:        "openai_standard",
			Capabilities:  ModelCapabilities{Temperature: true, ToolCall: true},
		},
		"o4-mini": {
			ContextWindow: 200000,
			OutputLimit:   100000,
			TokenizerType: "tiktoken/o200k_base",
			Family:        "openai_flagship",
			Capabilities:  ModelCapabilities{Reasoning: true, ToolCall: true},
		},
		"o3": {
			ContextWindow: 200000,
			OutputLimit:   100000,
			TokenizerType: "tiktoken/o200k_base",
			Family:        "openai_flagship",
			Capabilities:  ModelCapabilities{Reasoning: true, ToolCall: true},
		},
		"o3-mini": {
			ContextWindow: 200000,
			OutputLimit:   100000,
			TokenizerType: "tiktoken/o200k_base",
			Family:        "openai_flagship",
			Capabilities:  ModelCapabilities{Reasoning: true, ToolCall: true},
		},
		"o1": {
			ContextWindow: 200000,
			OutputLimit:   100000,
			TokenizerType: "tiktoken/o200k_base",
			Family:        "openai_flagship",
			Capabilities:  ModelCapabilities{Reasoning: true, ToolCall: true},
		},
		"o1-mini": {
			ContextWindow: 128000,
			OutputLimit:   65536,
			TokenizerType: "tiktoken/o200k_base",
			Family:        "openai_flagship",
			Capabilities:  ModelCapabilities{Reasoning: true, ToolCall: true},
		},
		"gpt-4o": {
			ContextWindow: 128000,
			OutputLimit:   16384,
			TokenizerType: "tiktoken/o200k_base",
			Family:        "openai_flagship",
			Capabilities:  ModelCapabilities{Attachment: true, Temperature: true, ToolCall: true},
		},
		"gpt-4o-mini": {
			ContextWindow: 128000,
			OutputLimit:   16384,
			TokenizerType: "tiktoken/o200k_base",
			Family:        "openai_flagship",
			Capabilities:  ModelCapabilities{Attachment: true, Temperature: true, ToolCall: true},
		},
		"codex-mini-latest": {
			ContextWindow: 192000,
			OutputLimit:   16384,
			TokenizerType: "tiktoken/o200k_base",
			Family:        "openai_codex",
			Capabilities: ModelCapabilities{
				ToolCall:  true,
				Reasoning: true,
			},
		},
		"gpt-5.3-codex": {
			ContextWindow: 192000,
			OutputLimit:   16384,
			TokenizerType: "tiktoken/o200k_base",
			Family:        "openai_codex",
			Capabilities: ModelCapabilities{
				ToolCall:  true,
				Reasoning: true,
			},
		},

		// Anthropic models
		"claude-opus-4.6": {
			ContextWindow: 1000000,
			OutputLimit:   32768,
			TokenizerType: "anthropic-api",
			Family:        "anthropic",
			Capabilities:  ModelCapabilities{Attachment: true, Temperature: true, ToolCall: true},
		},
		"claude-sonnet-4.6": {
			ContextWindow: 1000000,
			OutputLimit:   32768,
			TokenizerType: "anthropic-api",
			Family:        "anthropic",
			Capabilities:  ModelCapabilities{Attachment: true, Temperature: true, ToolCall: true},
		},
		"claude-haiku-4.5": {
			ContextWindow: 200000,
			OutputLimit:   8192,
			TokenizerType: "anthropic-api",
			Family:        "anthropic",
			Capabilities:  ModelCapabilities{Attachment: true, Temperature: true, ToolCall: true},
		},
		"claude-sonnet-4.5": {
			ContextWindow: 200000,
			OutputLimit:   16384,
			TokenizerType: "anthropic-api",
			Family:        "anthropic",
			Capabilities:  ModelCapabilities{Attachment: true, Reasoning: true, Temperature: true, ToolCall: true},
		},
		"claude-opus-4.5": {
			ContextWindow: 200000,
			OutputLimit:   32768,
			TokenizerType: "anthropic-api",
			Family:        "anthropic",
			Capabilities:  ModelCapabilities{Attachment: true, Reasoning: true, Temperature: true, ToolCall: true},
		},
		"claude-sonnet-4": {
			ContextWindow: 200000,
			OutputLimit:   16384,
			TokenizerType: "anthropic-api",
			Family:        "anthropic",
			Capabilities:  ModelCapabilities{Attachment: true, Reasoning: true, Temperature: true, ToolCall: true},
		},
		"claude-opus-4": {
			ContextWindow: 200000,
			OutputLimit:   32768,
			TokenizerType: "anthropic-api",
			Family:        "anthropic",
			Capabilities:  ModelCapabilities{Attachment: true, Reasoning: true, Temperature: true, ToolCall: true},
		},
		"claude-3.5-sonnet": {
			ContextWindow: 200000,
			OutputLimit:   8192,
			TokenizerType: "anthropic-api",
			Family:        "anthropic",
			Capabilities:  ModelCapabilities{Attachment: true, Temperature: true, ToolCall: true},
		},
		"claude-3.5-haiku": {
			ContextWindow: 200000,
			OutputLimit:   8192,
			TokenizerType: "anthropic-api",
			Family:        "anthropic",
			Capabilities:  ModelCapabilities{Attachment: true, Temperature: true, ToolCall: true},
		},

		// Google Gemini models — accessed via the openai_compatible provider
		// (Google's Gemini API supports the OpenAI /v1/chat/completions protocol).
		"gemini-3.1-pro": {
			ContextWindow: 1048576,
			OutputLimit:   65536,
			TokenizerType: "approximate",
			Family:        "google",
			Capabilities:  ModelCapabilities{Attachment: true, Temperature: true, ToolCall: true},
		},
		"gemini-3.1-flash-lite": {
			ContextWindow: 1048576,
			OutputLimit:   65536,
			TokenizerType: "approximate",
			Family:        "google",
			Capabilities:  ModelCapabilities{Attachment: true, Temperature: true, ToolCall: true},
		},
		"gemini-3-flash": {
			ContextWindow: 1048576,
			OutputLimit:   65536,
			TokenizerType: "approximate",
			Family:        "google",
			Capabilities:  ModelCapabilities{Attachment: true, Temperature: true, ToolCall: true},
		},
		"gemini-2.5-pro": {
			ContextWindow: 1048576,
			OutputLimit:   65536,
			TokenizerType: "approximate",
			Family:        "google",
			Capabilities:  ModelCapabilities{Attachment: true, Reasoning: true, Temperature: true, ToolCall: true},
		},
		"gemini-2.5-flash": {
			ContextWindow: 1048576,
			OutputLimit:   65536,
			TokenizerType: "approximate",
			Family:        "google",
			Capabilities:  ModelCapabilities{Attachment: true, Reasoning: true, Temperature: true, ToolCall: true},
		},
		"gemini-2.5-flash-lite": {
			ContextWindow: 1048576,
			OutputLimit:   65536,
			TokenizerType: "approximate",
			Family:        "google",
			Capabilities:  ModelCapabilities{Attachment: true, Temperature: true, ToolCall: true},
		},
		"gemini-2.0-flash": {
			ContextWindow: 1048576,
			OutputLimit:   8192,
			TokenizerType: "approximate",
			Family:        "google",
			Capabilities:  ModelCapabilities{Attachment: true, Temperature: true, ToolCall: true},
		},
		// Google Gemma models
		"gemma-4-31b-it": {
			ContextWindow: 128000,
			OutputLimit:   8192,
			TokenizerType: "approximate",
			Family:        "google",
			Capabilities:  ModelCapabilities{Temperature: true, ToolCall: true},
		},

		// DeepSeek V4 models
		"deepseek-v4-pro": {
			ContextWindow: 1000000,
			OutputLimit:   16384,
			TokenizerType: "approximate",
			Family:        "deepseek",
			Capabilities:  ModelCapabilities{Reasoning: true, Temperature: true, ToolCall: true},
		},
		"deepseek-v4-flash": {
			ContextWindow: 1000000,
			OutputLimit:   16384,
			TokenizerType: "approximate",
			Family:        "deepseek",
			Capabilities:  ModelCapabilities{Reasoning: true, Temperature: true, ToolCall: true},
		},

		// Kimi models (Moonshot AI)
		"kimi-k2": {
			ContextWindow: 131072,
			OutputLimit:   8192,
			TokenizerType: "approximate",
			Family:        "kimi",
			Capabilities:  ModelCapabilities{Temperature: true, ToolCall: true},
		},
		"kimi-k2-thinking": {
			ContextWindow: 131072,
			OutputLimit:   8192,
			TokenizerType: "approximate",
			Family:        "kimi",
			Capabilities:  ModelCapabilities{Reasoning: true, ToolCall: true},
		},

		// Qwen models (Alibaba)
		"qwen-plus": {
			ContextWindow: 131072,
			OutputLimit:   8192,
			TokenizerType: "approximate",
			Family:        "qwen",
			Capabilities:  ModelCapabilities{Reasoning: true, Temperature: true, ToolCall: true},
		},
		"qwen-max": {
			ContextWindow: 131072,
			OutputLimit:   8192,
			TokenizerType: "approximate",
			Family:        "qwen",
			Capabilities:  ModelCapabilities{Reasoning: true, Temperature: true, ToolCall: true},
		},
		"qwq-plus": {
			ContextWindow: 131072,
			OutputLimit:   16384,
			TokenizerType: "approximate",
			Family:        "qwen",
			Capabilities:  ModelCapabilities{Reasoning: true, ToolCall: true},
		},

		// GLM models (Zhipu AI)
		"glm-5.2": {
			ContextWindow: 1000000,
			OutputLimit:   16384,
			TokenizerType: "approximate",
			Family:        "glm",
			Capabilities:  ModelCapabilities{Reasoning: true, Temperature: true, ToolCall: true},
		},
		"glm-5.1": {
			ContextWindow: 200000,
			OutputLimit:   16384,
			TokenizerType: "approximate",
			Family:        "glm",
			Capabilities:  ModelCapabilities{Reasoning: true, Temperature: true, ToolCall: true},
		},
		"glm-5": {
			ContextWindow: 200000,
			OutputLimit:   16384,
			TokenizerType: "approximate",
			Family:        "glm",
			Capabilities:  ModelCapabilities{Reasoning: true, Temperature: true, ToolCall: true},
		},
		"glm-4.7": {
			ContextWindow: 128000,
			OutputLimit:   16384,
			TokenizerType: "approximate",
			Family:        "glm",
			Capabilities:  ModelCapabilities{Reasoning: true, Temperature: true, ToolCall: true},
		},
		"glm-z1-32b": {
			ContextWindow: 128000,
			OutputLimit:   16384,
			TokenizerType: "approximate",
			Family:        "glm",
			Capabilities:  ModelCapabilities{Reasoning: true, ToolCall: true},
		},

		// xAI Grok models
		"grok-4.20": {
			ContextWindow: 2000000,
			OutputLimit:   32768,
			TokenizerType: "approximate",
			Family:        "default",
			Capabilities:  ModelCapabilities{Temperature: true, ToolCall: true},
		},
		"grok-4.1-fast": {
			ContextWindow: 2000000,
			OutputLimit:   32768,
			TokenizerType: "approximate",
			Family:        "default",
			Capabilities:  ModelCapabilities{Temperature: true, ToolCall: true},
		},
		"grok-4": {
			ContextWindow: 256000,
			OutputLimit:   32768,
			TokenizerType: "approximate",
			Family:        "default",
			Capabilities:  ModelCapabilities{Temperature: true, ToolCall: true},
		},
		"grok-3": {
			ContextWindow: 131072,
			OutputLimit:   32768,
			TokenizerType: "approximate",
			Family:        "default",
			Capabilities:  ModelCapabilities{Temperature: true, ToolCall: true},
		},
		"grok-3-mini": {
			ContextWindow: 131072,
			OutputLimit:   32768,
			TokenizerType: "approximate",
			Family:        "default",
			Capabilities:  ModelCapabilities{Reasoning: true, Temperature: true, ToolCall: true},
		},
	}
}

// builtInRegistryOnce guards lazy initialization of the cached built-in registry.
var builtInRegistryOnce sync.Once
var builtInRegistryCache map[string]ModelMetadata

// getBuiltInRegistry returns the cached built-in model registry, initializing it
// on first call. The registry is immutable data so sharing is safe.
func getBuiltInRegistry() map[string]ModelMetadata {
	builtInRegistryOnce.Do(func() {
		builtInRegistryCache = makeBuiltInRegistry()
	})
	return builtInRegistryCache
}

// BuiltInModelNames returns model names from the built-in registry filtered by tokenizer type.
// If tokenizerType is empty, returns all model names.
func BuiltInModelNames(tokenizerType string) []string {
	registry := getBuiltInRegistry()
	names := []string{}
	for name, meta := range registry {
		if tokenizerType == "" || meta.TokenizerType == tokenizerType {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}
