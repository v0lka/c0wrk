package llm

import (
	"context"
	"fmt"

	"github.com/user/agent/internal/config"
)

// LLMRouter maps roles to providers and models.
type LLMRouter struct {
	providers map[string]LLMProvider // key = provider name from config
	roles     map[string]RoleConfig  // key = role name
	defaults  LLMDefaults
}

// RoleConfig maps a role to a provider and model.
type RoleConfig struct {
	Provider string // key in providers map
	Model    string // model name for API
}

// LLMDefaults holds default values for LLM calls.
type LLMDefaults struct {
	MaxTokens   int
	Temperature float64
}

// NewLLMRouter creates a new LLMRouter from the given configuration.
func NewLLMRouter(cfg config.LLMConfig) (*LLMRouter, error) {
	providers := make(map[string]LLMProvider)

	// Create providers from config
	for name, pcfg := range cfg.Providers {
		provider, err := createProvider(name, pcfg)
		if err != nil {
			return nil, fmt.Errorf("failed to create provider %q: %w", name, err)
		}
		providers[name] = provider
	}

	// Convert config roles to internal RoleConfig
	roles := make(map[string]RoleConfig)
	for name, rcfg := range cfg.Roles {
		roles[name] = RoleConfig{
			Provider: rcfg.Provider,
			Model:    rcfg.Model,
		}
	}

	// Validate that each role references an existing provider
	for roleName, role := range roles {
		if _, exists := providers[role.Provider]; !exists {
			return nil, fmt.Errorf("role %q references non-existent provider %q", roleName, role.Provider)
		}
	}

	// Convert defaults
	defaults := LLMDefaults{
		MaxTokens:   cfg.Defaults.MaxTokens,
		Temperature: cfg.Defaults.Temperature,
	}

	return &LLMRouter{
		providers: providers,
		roles:     roles,
		defaults:  defaults,
	}, nil
}

// createProvider creates an LLMProvider based on the provider type.
func createProvider(name string, pcfg config.ProviderConfig) (LLMProvider, error) {
	switch pcfg.Type {
	case "openai", "deepseek", "grok", "openrouter", "ollama", "lmstudio":
		return NewOpenAIProvider(OpenAIProviderConfig{
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

// Call sends a chat request to the provider associated with the given role.
func (r *LLMRouter) Call(ctx context.Context, role string, req ChatRequest) (*ChatResponse, error) {
	// Find role config
	roleConfig, ok := r.roles[role]
	if !ok {
		return nil, fmt.Errorf("unknown role: %s", role)
	}

	// Find provider
	provider, ok := r.providers[roleConfig.Provider]
	if !ok {
		return nil, fmt.Errorf("provider %q not found for role %q", roleConfig.Provider, role)
	}

	// Set model if not specified
	if req.Model == "" {
		req.Model = roleConfig.Model
	}

	// Apply defaults
	if req.MaxTokens == 0 {
		req.MaxTokens = r.defaults.MaxTokens
	}
	if req.Temperature == nil {
		temp := r.defaults.Temperature
		req.Temperature = &temp
	}

	return provider.ChatCompletion(ctx, req)
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

// Stream sends a streaming chat request to the provider associated with the given role.
func (r *LLMRouter) Stream(ctx context.Context, role string, req ChatRequest) (<-chan ChatChunk, error) {
	// Find role config
	roleConfig, ok := r.roles[role]
	if !ok {
		return nil, fmt.Errorf("unknown role: %s", role)
	}

	// Find provider
	provider, ok := r.providers[roleConfig.Provider]
	if !ok {
		return nil, fmt.Errorf("provider %q not found for role %q", roleConfig.Provider, role)
	}

	// Set model if not specified
	if req.Model == "" {
		req.Model = roleConfig.Model
	}

	// Apply defaults
	if req.MaxTokens == 0 {
		req.MaxTokens = r.defaults.MaxTokens
	}
	if req.Temperature == nil {
		temp := r.defaults.Temperature
		req.Temperature = &temp
	}

	return provider.StreamChatCompletion(ctx, req)
}
