package llm

import (
	"context"
	"errors"
	"fmt"

	"github.com/user/agent/internal/config"
)

// LLMRouter routes LLM calls to the active provider.
type LLMRouter struct {
	providers       map[string]LLMProvider
	activeProvider  LLMProvider
	activeModel     string
	activeProviderName string
}

// NewLLMRouter creates a new LLMRouter from the given configuration.
// If registry is provided, LM Studio providers will register their metadata sources.
func NewLLMRouter(cfg config.LLMConfig, registry *ModelRegistry) (*LLMRouter, error) {
	// Get active provider configuration
	provType, apiKey, baseURL, model := cfg.GetActiveProviderConfig()
	if provType == "" {
		return nil, errors.New("no active provider configured")
	}

	// Create the active provider
	provider, err := createProviderFromConfig(provType, apiKey, baseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to create provider %q: %w", cfg.ActiveProvider, err)
	}

	// Register LM Studio provider as metadata source
	if registry != nil {
		if lms, ok := provider.(*LMStudioProvider); ok {
			registry.RegisterSource(lms.MetadataSource())
		}
	}

	return &LLMRouter{
		providers: map[string]LLMProvider{
			cfg.ActiveProvider: provider,
		},
		activeProvider:     provider,
		activeModel:        model,
		activeProviderName: cfg.ActiveProvider,
	}, nil
}

// createProviderFromConfig creates an LLMProvider based on the provider type.
// Environment variables in apiKey and baseURL are expanded before passing to providers.
func createProviderFromConfig(provType, apiKey, baseURL string) (LLMProvider, error) {
	// Expand environment variables in API key and base URL
	apiKey = config.ExpandEnvVars(apiKey)
	baseURL = config.ExpandEnvVars(baseURL)

	switch provType {
	case "openai":
		return NewOpenAIProvider(OpenAIProviderConfig{
			APIKey:  apiKey,
			BaseURL: baseURL,
		})

	case "lmstudio":
		return NewLMStudioProvider(LMStudioProviderConfig{
			APIKey:  apiKey,
			BaseURL: baseURL,
		})

	case "anthropic":
		return NewAnthropicProvider(AnthropicProviderConfig{
			APIKey: apiKey,
		})

	case "gemini":
		return NewGeminiProvider(context.Background(), GeminiProviderConfig{
			APIKey: apiKey,
		})

	default:
		return nil, fmt.Errorf("unknown provider type: %s", provType)
	}
}

// Call sends a chat request to the active provider.
func (r *LLMRouter) Call(ctx context.Context, role string, req ChatRequest) (*ChatResponse, error) {
	// Set model if not specified
	if req.Model == "" {
		req.Model = r.activeModel
	}

	// Hardcode temperature=0 for deterministic output
	if req.Temperature == nil {
		temp := 0.0
		req.Temperature = &temp
	}

	return r.activeProvider.ChatCompletion(ctx, req)
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

// GetDefaultProvider returns the active provider.
// Returns nil if no provider is configured.
func (r *LLMRouter) GetDefaultProvider() LLMProvider {
	return r.activeProvider
}

// Stream sends a streaming chat request to the active provider.
func (r *LLMRouter) Stream(ctx context.Context, role string, req ChatRequest) (<-chan ChatChunk, error) {
	// Set model if not specified
	if req.Model == "" {
		req.Model = r.activeModel
	}

	// Hardcode temperature=0 for deterministic output
	if req.Temperature == nil {
		temp := 0.0
		req.Temperature = &temp
	}

	return r.activeProvider.StreamChatCompletion(ctx, req)
}
