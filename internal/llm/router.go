package llm

import (
	"context"
	"fmt"

	"github.com/user/agent/internal/config"
)

// LLMRouter routes LLM calls to a default provider.
type LLMRouter struct {
	providers       map[string]LLMProvider
	defaultProvider LLMProvider
	defaultModel    string
	defaults        LLMDefaults
}

// LLMDefaults holds default values for LLM calls.
type LLMDefaults struct {
	MaxTokens   int
	Temperature float64
}

// NewLLMRouter creates a new LLMRouter from the given configuration.
// If registry is provided, LM Studio providers will register their metadata sources.
func NewLLMRouter(cfg config.LLMConfig, registry *ModelRegistry) (*LLMRouter, error) {
	providers := make(map[string]LLMProvider)

	// Create providers from config
	for name, pcfg := range cfg.Providers {
		provider, err := createProvider(name, pcfg)
		if err != nil {
			return nil, fmt.Errorf("failed to create provider %q: %w", name, err)
		}
		providers[name] = provider
	}

	// Register LM Studio providers as metadata sources
	if registry != nil {
		for _, p := range providers {
			if lms, ok := p.(*LMStudioProvider); ok {
				registry.RegisterSource(lms.MetadataSource())
			}
		}
	}

	// Resolve default provider
	defaultProvider, ok := providers[cfg.DefaultProvider]
	if !ok {
		return nil, fmt.Errorf("default provider %q not found", cfg.DefaultProvider)
	}
	defaultModel := cfg.Providers[cfg.DefaultProvider].Model

	// Convert defaults
	defaults := LLMDefaults{
		MaxTokens:   cfg.Defaults.MaxTokens,
		Temperature: cfg.Defaults.Temperature,
	}

	return &LLMRouter{
		providers:       providers,
		defaultProvider: defaultProvider,
		defaultModel:    defaultModel,
		defaults:        defaults,
	}, nil
}

// createProvider creates an LLMProvider based on the provider type.
func createProvider(name string, pcfg config.ProviderConfig) (LLMProvider, error) {
	switch pcfg.Type {
	case "openai", "deepseek", "grok", "openrouter", "ollama":
		return NewOpenAIProvider(OpenAIProviderConfig{
			Name:    name,
			APIKey:  pcfg.APIKey,
			BaseURL: pcfg.BaseURL,
		})

	case "lmstudio":
		return NewLMStudioProvider(LMStudioProviderConfig{
			Name:    name,
			APIKey:  pcfg.APIKey,
			BaseURL: pcfg.BaseURL,
		})

	case "anthropic":
		return NewAnthropicProvider(AnthropicProviderConfig{
			APIKey: pcfg.APIKey,
		})

	case "gemini":
		return NewGeminiProvider(context.Background(), GeminiProviderConfig{
			APIKey:    pcfg.APIKey,
			ProjectID: pcfg.ProjectID,
			Location:  pcfg.Location,
		})

	default:
		return nil, fmt.Errorf("unknown provider type: %s", pcfg.Type)
	}
}

// Call sends a chat request to the default provider.
func (r *LLMRouter) Call(ctx context.Context, role string, req ChatRequest) (*ChatResponse, error) {
	// Set model if not specified
	if req.Model == "" {
		req.Model = r.defaultModel
	}

	// Apply defaults
	if req.MaxTokens == 0 {
		req.MaxTokens = r.defaults.MaxTokens
	}
	if req.Temperature == nil {
		temp := r.defaults.Temperature
		req.Temperature = &temp
	}

	return r.defaultProvider.ChatCompletion(ctx, req)
}

// GetProvider returns a provider by name.
// Returns nil if the provider is not found.
func (r *LLMRouter) GetProvider(name string) LLMProvider {
	provider, ok := r.providers[name]
	if !ok {
		return nil
	}
	return provider
}

// GetDefaultProvider returns the first available provider.
// Returns nil if no providers are configured.
func (r *LLMRouter) GetDefaultProvider() LLMProvider {
	for _, provider := range r.providers {
		return provider
	}
	return nil
}

// Stream sends a streaming chat request to the default provider.
func (r *LLMRouter) Stream(ctx context.Context, role string, req ChatRequest) (<-chan ChatChunk, error) {
	// Set model if not specified
	if req.Model == "" {
		req.Model = r.defaultModel
	}

	// Apply defaults
	if req.MaxTokens == 0 {
		req.MaxTokens = r.defaults.MaxTokens
	}
	if req.Temperature == nil {
		temp := r.defaults.Temperature
		req.Temperature = &temp
	}

	return r.defaultProvider.StreamChatCompletion(ctx, req)
}
