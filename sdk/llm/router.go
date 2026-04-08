package llm

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"time"
)

// RouterConfig configures the LLM router.
// All values must be pre-resolved by the caller (env vars expanded, durations parsed).
type RouterConfig struct {
	ActiveProvider string        // Logical name of the active provider (e.g. "openai", "lmstudio")
	ProviderType   string        // Provider type: "openai", "lmstudio", "anthropic", "gemini"
	APIKey         string        // Already-expanded API key
	BaseURL        string        // Already-expanded base URL
	Model          string        // Default model to use
	MaxRetries     int           // Max retry attempts on retryable errors
	InitialBackoff time.Duration // Already parsed initial backoff duration
	MaxBackoff     time.Duration // Already parsed max backoff duration
}

// LLMRouter routes LLM calls to the active provider.
type LLMRouter struct {
	providers          map[string]LLMProvider
	activeProvider     LLMProvider
	activeModel        string
	activeProviderName string
	// Retry configuration
	maxRetries     int
	initialBackoff time.Duration
	maxBackoff     time.Duration
	// Pre-call context window validation
	registry     *ModelRegistry
	tokenCounter TokenCounter
}

// NewLLMRouter creates a new LLMRouter from the given configuration.
// The caller is responsible for resolving provider config, expanding env vars,
// and parsing durations before calling this function.
// If registry is provided, LM Studio providers will register their metadata sources.
func NewLLMRouter(cfg RouterConfig, registry *ModelRegistry) (*LLMRouter, error) {
	if cfg.ProviderType == "" {
		return nil, errors.New("no active provider configured")
	}

	// Create the active provider — values are already expanded by the caller
	provider, err := createProviderFromConfig(cfg.ProviderType, cfg.APIKey, cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to create provider %q: %w", cfg.ActiveProvider, err)
	}

	// Register LM Studio provider as metadata source
	if registry != nil {
		if lms, ok := provider.(*LMStudioProvider); ok {
			registry.RegisterSource(lms.MetadataSource())
		}
	}

	initialBackoff := cfg.InitialBackoff
	if initialBackoff == 0 {
		initialBackoff = 1 * time.Second
	}
	maxBackoff := cfg.MaxBackoff
	if maxBackoff == 0 {
		maxBackoff = 30 * time.Second
	}

	return &LLMRouter{
		providers: map[string]LLMProvider{
			cfg.ActiveProvider: provider,
		},
		activeProvider:     provider,
		activeModel:        cfg.Model,
		activeProviderName: cfg.ActiveProvider,
		maxRetries:         cfg.MaxRetries,
		initialBackoff:     initialBackoff,
		maxBackoff:         maxBackoff,
		registry:           registry,
		tokenCounter:       NewSimpleTokenCounter(),
	}, nil
}

// createProviderFromConfig creates an LLMProvider based on the provider type.
// The caller must have already expanded environment variables.
func createProviderFromConfig(provType, apiKey, baseURL string) (LLMProvider, error) {
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

// retryBackoff sleeps for the given duration with +/- 20% jitter, respecting context cancellation.
// Returns false if the context was cancelled during sleep.
func retryBackoff(ctx context.Context, backoff time.Duration) bool {
	// Add jitter: +/- 20%
	jitterFactor := 0.8 + 0.4*rand.Float64() // random factor between 0.8 and 1.2
	jitteredBackoff := float64(backoff) * jitterFactor
	jitter := time.Duration(jitteredBackoff)
	select {
	case <-time.After(jitter):
		return true
	case <-ctx.Done():
		return false
	}
}

// contextWindowSafetyMargin is the fraction (5%) subtracted from the effective
// max to account for token-counting inaccuracy.
const contextWindowSafetyMargin = 0.05

// validateContextWindow checks whether the estimated token count of msgs fits
// within the model's context window minus output reserve. Returns nil when
// validation passes or should be skipped (unknown model, zero context window,
// nil registry).
func (r *LLMRouter) validateContextWindow(model string, msgs []Message) error {
	if r.registry == nil || r.tokenCounter == nil {
		return nil
	}

	meta := r.registry.Resolve(model)

	// Skip validation when metadata is a fallback or context window is 0
	if meta.ContextWindow == 0 {
		return nil
	}

	outputReserve := meta.OutputLimit
	if outputReserve <= 0 {
		outputReserve = 4096
	}

	effectiveMax := meta.ContextWindow - outputReserve
	if effectiveMax <= 0 {
		return nil
	}

	// Apply safety margin to account for counting inaccuracy
	effectiveMax = int(float64(effectiveMax) * (1 - contextWindowSafetyMargin))

	estimated := r.tokenCounter.CountMessages(msgs)
	if estimated > effectiveMax {
		return NewContextWindowError(model, estimated, effectiveMax, meta.ContextWindow, outputReserve)
	}

	return nil
}

// Call sends a chat request to the active provider.
func (r *LLMRouter) Call(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	// Set model if not specified
	if req.Model == "" {
		req.Model = r.activeModel
	}

	// Hardcode temperature=0 for deterministic output
	if req.Temperature == nil {
		temp := 0.0
		req.Temperature = &temp
	}

	// Pre-call context window validation
	if err := r.validateContextWindow(req.Model, req.Messages); err != nil {
		return nil, err
	}

	var lastErr error
	backoff := r.initialBackoff

	for attempt := 0; attempt <= r.maxRetries; attempt++ {
		resp, err := r.activeProvider.ChatCompletion(ctx, req)
		if err == nil {
			return resp, nil
		}

		lastErr = err

		// Don't retry if not retryable or this was the last attempt
		if !IsRetryable(err) || attempt == r.maxRetries {
			return nil, err
		}

		// Wait before retrying

		// Sleep with jitter, respecting context cancellation
		if !retryBackoff(ctx, backoff) {
			return nil, lastErr
		}

		// Exponential backoff: double, capped at max
		backoff *= 2
		if backoff > r.maxBackoff {
			backoff = r.maxBackoff
		}
	}

	return nil, lastErr
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
func (r *LLMRouter) Stream(ctx context.Context, req ChatRequest) (<-chan ChatChunk, error) {
	// Set model if not specified
	if req.Model == "" {
		req.Model = r.activeModel
	}

	// Hardcode temperature=0 for deterministic output
	if req.Temperature == nil {
		temp := 0.0
		req.Temperature = &temp
	}

	// Pre-call context window validation
	if err := r.validateContextWindow(req.Model, req.Messages); err != nil {
		return nil, err
	}

	var lastErr error
	backoff := r.initialBackoff

	for attempt := 0; attempt <= r.maxRetries; attempt++ {
		ch, err := r.activeProvider.StreamChatCompletion(ctx, req)
		if err == nil {
			return ch, nil
		}

		lastErr = err

		if !IsRetryable(err) || attempt == r.maxRetries {
			return nil, err
		}

		// Wait before retrying stream
		if !retryBackoff(ctx, backoff) {
			return nil, lastErr
		}

		backoff *= 2
		if backoff > r.maxBackoff {
			backoff = r.maxBackoff
		}
	}

	return nil, lastErr
}
